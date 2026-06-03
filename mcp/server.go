package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/townsendmerino/aikit/chunk"
	"github.com/townsendmerino/ken/internal/search"
)

// DBIntegration is the seam between mcp.Run / mcp.NewServer and a
// Tier-2 DB provider (typically *mcp/db.Refresher; mock impls in
// tests). The v0.8.0 Part 3 addendum (ADR-020) bundles both
// chunk-integration (Start) and tool invocation (TryRefresh) into
// one interface so the same provider drives both surfaces.
//
// Lifecycle:
//
//   - Start is called once by mcp.Run (or NewServer's caller) AFTER
//     the index is built. The onExtras callback fires each time the
//     provider has fresh DB chunks ready (initial introspection,
//     interval ticks, LISTEN/NOTIFY, agent-triggered reindex). The
//     receiver should treat each onExtras call as a complete
//     snapshot replacement, not an append — see *Index.WithExtraChunks
//     for the production pattern. The returned cleanup func is
//     deferred by the caller; canceling ctx is the secondary signal.
//
//   - TryRefresh is invoked by the reindex_db MCP tool handler. It
//     MUST be fail-fast on contention (return InProgress: true rather
//     than block) because the agent is waiting on the MCP response.
//
// Implementations must be safe to call TryRefresh concurrently with
// in-flight onExtras callbacks AND with other TryRefresh callers.
// *mcp/db.Refresher satisfies this via the underlying
// internal/db.Refresher's mutex serialization (the four blocking
// trigger sources + TryLock for the fifth — ADR-020 Part 2).
//
// The mcp package stays internal/db-free: this interface is what
// callers (cmd/ken-mcp; SDK authors using mcp.Run + mcp/db) implement
// in their layer. The v0.6.0 binary-size contract holds — see
// TestBinary_MCPPackageStaysDBFree.
type DBIntegration interface {
	// Start registers onExtras as the swap callback that fires on
	// each refresh. Starts whatever interval / LISTEN goroutines the
	// implementation needs. Returns a cleanup func the caller MUST
	// defer; canceling ctx is also honored.
	Start(ctx context.Context, onExtras func([]chunk.Chunk)) (cleanup func(), err error)

	// TryRefresh triggers an immediate refresh and returns the
	// outcome. Fail-fast on contention: in-flight refreshes (interval
	// tick, LISTEN burst, SIGHUP, prior TryRefresh) cause this call
	// to return ReindexResult{InProgress: true} without queuing.
	TryRefresh(ctx context.Context) ReindexResult
}

// ReindexResult is the outcome of one reindex attempt. Exactly one of
// InProgress=true, Err!=nil, or "everything else (success)" is the
// agent-facing case; the handler picks the message based on that.
type ReindexResult struct {
	// InProgress is true if another refresh is currently holding the
	// Refresher's mutex (interval ticker, SIGHUP, LISTEN/NOTIFY
	// listener, or a prior reindex_db call). The handler reports
	// "already in progress" without waiting. NEVER set true when
	// Err is also non-nil — they're mutually exclusive.
	InProgress bool

	// Elapsed is wall-clock duration of the refresh, measured by the
	// callback wrapper. Surfaced to the agent so it can reason about
	// whether to retry-on-stale or trust the freshness.
	Elapsed time.Duration

	// Err is non-nil on real failure (connection error, query failure).
	// InProgress is false in this case; the handler reports the error
	// text verbatim so the agent can decide whether to surface or retry.
	Err error
}

// Config is the wiring for a ken-mcp server. Defaults applied by
// NewServer for any zero value.
type Config struct {
	Cache         *Cache      // repo→Index cache (required)
	DefaultRepo   string      // optional pre-configured source; if set, tools may be called without a `repo` arg
	Mode          search.Mode // default ModeHybrid
	Chunker       string      // default "regex"
	Instructions  string      // server-instructions string; default mirrors semble's
	ServerName    string      // default "ken"
	ServerVersion string      // default "0"

	// DB, when non-nil, registers the v0.8.0 reindex_db MCP tool
	// (ADR-020 Part 2) and wires Tier-2 chunk integration via the
	// caller's swap target (ADR-020 Part 3 addendum). nil = tool
	// not registered + no chunk integration; the agent's tools/list
	// won't show reindex_db at all when no DB is configured, which
	// keeps the tool surface honest.
	//
	// cmd/ken-mcp wires this with a *mcp/db.Refresher whose Start
	// callback writes to the WatchedIndex.SetExtraChunks of the
	// pre-warmed default-repo Index. SDK authors using mcp.Run wire
	// the same *mcp/db.Refresher; mcp.Run's Start callback updates
	// an atomic.Pointer[search.Index] via WithExtraChunks.
	DB DBIntegration

	// TelemetryLog, when non-nil, is called once per search tool
	// invocation with the per-query timing breakdown. Used by
	// cmd/ken-mcp to emit an info-level stderr line per query when
	// the log level permits. The callback runs on the request
	// goroutine BEFORE the response is returned, so it should be
	// cheap (a single Logf is fine).
	//
	// Setting this knob also enables telemetry collection in the
	// search code path — leave nil to opt out of the (small) per-
	// query timing overhead.
	TelemetryLog func(query string, t search.Telemetry)

	// TelemetryInResponse, when true, appends a "[telemetry]" line
	// to the search tool response body so the agent can see timing
	// alongside results. Off by default — adding fields to the agent-
	// facing wire format is a behavior change and should be opt-in
	// (cmd/ken-mcp gates this behind KEN_MCP_RERANK_TELEMETRY=1).
	//
	// Setting this knob also enables telemetry collection. Like
	// TelemetryLog, leave false to skip the overhead.
	TelemetryInResponse bool

	// UsageRecorder, when non-nil, receives one Record call per
	// successful search / find_related invocation. cmd/ken-mcp wires
	// a *usage.Recorder writing to ~/.ken/savings.jsonl by default;
	// nil disables tracking (no file writes, no per-call overhead).
	// See internal/usage for the privacy contract — only counts and
	// timestamps are persisted, never query text or file paths.
	UsageRecorder UsageRecorder
}

// UsageRecorder is the minimal seam mcp/server.go uses to log a
// successful tool call. The concrete implementation lives in
// internal/usage to keep that package's deps + privacy contract
// isolated from the MCP wire layer.
type UsageRecorder interface {
	Record(callType string, results, snippetChars, fileChars int)
}

// NewServer returns an MCP server with `search` and `find_related`
// registered. The server speaks JSON-RPC over whatever Transport the
// caller passes to server.Run — ken-mcp uses sdk.StdioTransport.
func NewServer(cfg Config) *sdk.Server {
	if cfg.ServerName == "" {
		cfg.ServerName = "ken"
	}
	if cfg.ServerVersion == "" {
		cfg.ServerVersion = "0"
	}
	if cfg.Chunker == "" {
		cfg.Chunker = "regex"
	}
	if cfg.Instructions == "" {
		cfg.Instructions = "Instant code search for any local or remote git repository. " +
			"Call `search` to find relevant code; call `find_related` on a result to discover similar code elsewhere. " +
			"When working in a local project, pass the project root as `repo`. " +
			"For remote repos, pass an explicit https:// URL. Never guess or infer URLs. " +
			"Prefer ken for conceptual queries (\"where do we handle X?\"), locating definitions, and \"show me the surface of this area\" explorations. " +
			"For refactors, renames, or any operation that must be exhaustive, fall back to your native grep / file-search tool — grep gives 100% recall on literal matches, while ken's hybrid search optimizes for relevance over completeness and isn't designed for exhaustive enumeration."
	}

	srv := sdk.NewServer(&sdk.Implementation{
		Name:    cfg.ServerName,
		Version: cfg.ServerVersion,
	}, &sdk.ServerOptions{Instructions: cfg.Instructions})

	sdk.AddTool(srv, &sdk.Tool{
		Name: "search",
		Description: "Search a codebase with a natural-language or code query. " +
			"Pass a git URL or local path as `repo` to index it on demand; indexes are cached for the session. " +
			"Use this to find where something is implemented, understand a library, or locate related code.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, args SearchArgs) (*sdk.CallToolResult, any, error) {
		stop := startProgressHeartbeat(ctx, req, "ken search")
		defer stop()
		return handleSearch(ctx, &cfg, args)
	})

	sdk.AddTool(srv, &sdk.Tool{
		Name: "find_related",
		Description: "Find code chunks semantically similar to a specific location in a file. " +
			"Use after `search` to explore related implementations or callers. " +
			"Pass file_path and line from a prior search result.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, args FindRelatedArgs) (*sdk.CallToolResult, any, error) {
		stop := startProgressHeartbeat(ctx, req, "ken find_related")
		defer stop()
		return handleFindRelated(ctx, &cfg, args)
	})

	// v0.8.0 Part 2 (ADR-020): reindex_db tool. Registered ONLY when
	// a DBIntegration is wired — keeps tools/list honest (an agent
	// shouldn't see a tool that returns "no DB" 100% of the time).
	// Part 3 addendum: same condition, different field name
	// (Reindex ReindexFunc → DB DBIntegration).
	if cfg.DB != nil {
		sdk.AddTool(srv, &sdk.Tool{
			Name: "reindex_db",
			Description: "Trigger a Tier 2 database schema reindex on demand. " +
				"Use after running a migration to refresh ken's view of the database schema before asking schema-dependent questions. " +
				"Returns immediately with `already in progress` if another reindex is in flight (no queuing). " +
				"Available whenever a DB is configured.",
		}, func(ctx context.Context, _ *sdk.CallToolRequest, _ ReindexDBArgs) (*sdk.CallToolResult, any, error) {
			return handleReindexDB(ctx, &cfg)
		})
	}

	// Stage 8 Track 2: exact-answer structural tools. Always
	// registered — every repo has *some* structural index (Python
	// files at least; the extractor is extensible to other
	// languages). On repos with no supported source files the
	// tools return a clear "no structural index available" text
	// response rather than failing the call.
	//
	// Honest framing in each description: tree-sitter-grade,
	// name-resolved, NOT type-resolved. The agent reads these
	// strings; the calibration that "the same name in different
	// files may collapse into one result" is part of the wire
	// contract.
	sdk.AddTool(srv, &sdk.Tool{
		Name: "definition",
		Description: "Locate where a function, class, or method is defined. " +
			"Bare `symbol` (\"Login\") returns ALL definition sites — top-level functions/classes " +
			"with that name AND every method named Login on any class across the corpus. " +
			"Qualified `symbol` (\"User.Login\") returns ONLY methods on the named type. " +
			"Tree-sitter-grade: name-resolved, not type-resolved. Each result is labeled " +
			"function / class / method; method results carry their qualified `Type.method` " +
			"form in parentheses so an agent can disambiguate when the bare name lives on " +
			"multiple types. Collisions return all sites in alphabetical-by-file order; " +
			"ordering does NOT reflect confidence ranking.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, args DefinitionArgs) (*sdk.CallToolResult, any, error) {
		return handleDefinition(ctx, &cfg, args)
	})

	sdk.AddTool(srv, &sdk.Tool{
		Name: "references",
		Description: "Find every file referencing a name. " +
			"Returns the file(s) where `symbol` appears in a recognized syntactic context: " +
			"call sites, import statements, and raise statements. " +
			"Tree-sitter-grade: name-resolved, not type-resolved — same-spelled identifiers " +
			"in different files collapse into a single result list with no semantic " +
			"disambiguation. Use this for `where is X used` style questions, not for " +
			"compiler-grade rename refactors.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, args ReferencesArgs) (*sdk.CallToolResult, any, error) {
		return handleReferences(ctx, &cfg, args)
	})

	sdk.AddTool(srv, &sdk.Tool{
		Name: "callers",
		Description: "Find the files that contain a call to a given function name. " +
			"Returns file-level callers (\"this file has a call to X\"), not function-level " +
			"call hierarchy. Tree-sitter-grade: name-resolved, not type-resolved — same-spelled " +
			"functions across types collapse into one list. Stage 8 Gate 2 precision sample: " +
			"100% on a 400-edge sample across 8 languages. For function-level call hierarchy " +
			"(which specific function calls X), fall back to an LSP — ken doesn't track caller-" +
			"function scopes.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, args CallersArgs) (*sdk.CallToolResult, any, error) {
		return handleCallers(ctx, &cfg, args)
	})

	sdk.AddTool(srv, &sdk.Tool{
		Name: "outline",
		Description: "Show the structural outline of a file or directory. " +
			"For a file path: returns every top-level function, class, and method " +
			"(with parameter names) defined in that file. " +
			"For a directory path: returns the outline of every indexed file under it. " +
			"Fast structural overview for understanding what a file or package contains " +
			"without reading the source.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, args OutlineArgs) (*sdk.CallToolResult, any, error) {
		return handleOutline(ctx, &cfg, args)
	})

	sdk.AddTool(srv, &sdk.Tool{
		Name: "symbols",
		Description: "List every top-level symbol (function or class) defined in the repo, " +
			"optionally filtered by a subdirectory `path` prefix. " +
			"Useful as a starting point: `what's in this package?` or `what does this repo export?`. " +
			"Names only — pair with `definition` or `outline` to get locations + structure.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, args SymbolsArgs) (*sdk.CallToolResult, any, error) {
		return handleSymbols(ctx, &cfg, args)
	})

	sdk.AddTool(srv, &sdk.Tool{
		Name: "recently_changed",
		Description: "List the last N commits that touched the repo, with the files each commit changed. " +
			"Sourced from git history via go-git PlainOpen on the working tree. " +
			"Pass `n` (default 10, max 100) for how far back to look; pass `path` to filter to " +
			"commits that touched a path prefix (e.g. 'src/api'). " +
			"LOCAL REPO PATHS ONLY in this version — https:// URLs return a helpful error; " +
			"clone manually first if you need git history for a remote repo. " +
			"Use this to understand `what's been worked on recently` or `who is touching this area` " +
			"without shelling out to git log.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, args RecentlyChangedArgs) (*sdk.CallToolResult, any, error) {
		return handleRecentlyChanged(ctx, &cfg, args)
	})

	sdk.AddTool(srv, &sdk.Tool{
		Name: "status",
		Description: "Report ken's current state. Without args, returns build identity " +
			"(commit, aikit / gotreesitter / go versions), model availability " +
			"(embedding + rerank dirs and size on disk), Arm B enrichment state, " +
			"the persistent token-savings summary (today / 7d / all-time), and the " +
			"server's cache state. With `repo` set, ALSO includes live index state " +
			"(file count, chunks, mode, chunker) and structural-index counts for that " +
			"repo. Use this to verify the right binary / model / index is loaded, or " +
			"to surface how many tokens ken has saved across sessions. " +
			"Pass `output: 'json'` for a machine-parsable response; default is markdown.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, args StatusArgs) (*sdk.CallToolResult, any, error) {
		return handleStatus(ctx, &cfg, args)
	})

	return srv
}

// handleReindexDB invokes the configured DBIntegration's TryRefresh and
// renders the result as a text response matching semble's plain-text
// MCP wire format. Four shapes:
//
//   - DB unset → "DB indexing not configured…" (defense-in-depth;
//     NewServer only registers the tool when DB is non-nil, but a
//     future code path that calls this handler directly should fail
//     safe).
//   - InProgress → "Reindex already in progress; nothing to do."
//   - Err != nil → "Reindex failed: <err>"
//   - success → "Reindexed in 123ms."
func handleReindexDB(ctx context.Context, cfg *Config) (*sdk.CallToolResult, any, error) {
	if cfg.DB == nil {
		return textResult("DB indexing is not configured (KEN_DB_DSN is unset); nothing to reindex."), nil, nil
	}
	res := cfg.DB.TryRefresh(ctx)
	switch {
	case res.InProgress:
		return textResult("Reindex already in progress; nothing to do."), nil, nil
	case res.Err != nil:
		return textResult(fmt.Sprintf("Reindex failed: %s", res.Err.Error())), nil, nil
	default:
		return textResult(fmt.Sprintf("Reindexed in %dms.", res.Elapsed.Milliseconds())), nil, nil
	}
}

// resolveRepo picks the index source: explicit args.Repo wins, else the
// server's DefaultRepo, else a user-facing validation error.
func resolveRepo(cfg *Config, argRepo string) (string, error) {
	if argRepo != "" {
		return argRepo, nil
	}
	if cfg.DefaultRepo != "" {
		return cfg.DefaultRepo, nil
	}
	return "", fmt.Errorf("no repo specified and no default index; pass an https:// or http:// git URL or local directory path as `repo`")
}

// textResult wraps a plain string as the tool's result content. semble's
// MCP returns formatted strings (not structured objects); we match.
func textResult(s string) *sdk.CallToolResult {
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: s}}}
}

func handleSearch(ctx context.Context, cfg *Config, args SearchArgs) (*sdk.CallToolResult, any, error) {
	source, err := resolveRepo(cfg, args.Repo)
	if err != nil {
		return textResult(err.Error()), nil, nil
	}
	wi, err := cfg.Cache.Get(ctx, source)
	if err != nil {
		return textResult(fmt.Sprintf("Failed to index %q: %s", source, err.Error())), nil, nil
	}
	// sourceRoot for usage stats: only meaningful for local-path
	// sources where the chunk's relative path can be stat()ed for
	// file size. http(s) clones land in a temp dir under TMPDIR;
	// passing that root is fine — the stats are best-effort.
	return runSearchWithTelemetry(wi.Load(), args, cfg.TelemetryLog, cfg.TelemetryInResponse, cfg.UsageRecorder, source)
}

func handleFindRelated(ctx context.Context, cfg *Config, args FindRelatedArgs) (*sdk.CallToolResult, any, error) {
	if args.FilePath == "" || args.Line <= 0 {
		return textResult("file_path is required and line must be ≥ 1."), nil, nil
	}
	source, err := resolveRepo(cfg, args.Repo)
	if err != nil {
		return textResult(err.Error()), nil, nil
	}
	wi, err := cfg.Cache.Get(ctx, source)
	if err != nil {
		return textResult(fmt.Sprintf("Failed to index %q: %s", source, err.Error())), nil, nil
	}
	return runFindRelatedWithUsage(wi.Load(), args, cfg.UsageRecorder, source)
}

// runSearch executes a search against a resolved Index — independent of
// how the Index was selected (Cache lookup, fixed single-corpus, etc.).
// Shared between handleSearch (Cache-backed NewServer) and the
// embedded-corpus tool handlers built by mcp.Run.
//
// args.Mode semantics (ken-side extension; semble's MCP search has no
// such arg): if non-empty, overrides the index's build-time mode for
// just this call via Index.SearchMode. If empty, the index's
// build-time mode is used. The mode reported in the response header
// is the EFFECTIVE mode actually used — so an agent that asks for
// hybrid against a BM25-only index sees "mode=bm25" in the header
// (capability downgrade is visible, not silent).
func runSearch(ix *search.Index, args SearchArgs) (*sdk.CallToolResult, any, error) {
	return runSearchWithTelemetry(ix, args, nil, false, nil, "")
}

// runSearchWithTelemetry is the runSearch variant that optionally
// collects + surfaces per-query telemetry and records usage stats.
// Called by handleSearch with the Config knobs; the pure runSearch
// above keeps the zero-config path overhead-free (no time.Now
// bookkeeping in the search code path, no file stats).
//
// recorder: when non-nil, one Record call is appended on each
// successful search (len(results) > 0). sourceRoot scopes file_chars
// computation — file paths in results are joined to it before stat.
func runSearchWithTelemetry(ix *search.Index, args SearchArgs, log func(query string, t search.Telemetry), includeInResponse bool, recorder UsageRecorder, sourceRoot string) (*sdk.CallToolResult, any, error) {
	requestedMode := ix.Mode()
	if args.Mode != "" {
		parsed, perr := search.ParseMode(args.Mode)
		if perr != nil {
			return textResult(perr.Error()), nil, nil
		}
		requestedMode = parsed
	}
	topK := args.TopK
	if topK <= 0 {
		topK = DefaultTopK
	}
	// 1.0 filters: when any of Languages / PathContains /
	// ExcludePathContains is set, over-fetch by a factor so the
	// post-filter top-K still has plausible candidates. The factor
	// is generous (10× topK, capped at 200) because filter
	// selectivity is unpredictable — "Go only" might keep 30% of
	// hits while "src/api only" might keep 1%. Over-fetching past
	// the cap caps wall-time at known-bounded levels.
	fetchK := topK
	hasFilters := len(args.Languages) > 0 || args.PathContains != "" || args.ExcludePathContains != ""
	if hasFilters {
		fetchK = topK * 10
		if fetchK > 200 {
			fetchK = 200
		}
	}
	collect := log != nil || includeInResponse
	var (
		results       []search.Result
		effectiveMode search.Mode
		tel           search.Telemetry
	)
	if collect {
		results, effectiveMode, tel = ix.SearchModeWithTelemetry(args.Query, fetchK, requestedMode)
	} else {
		results, effectiveMode = ix.SearchMode(args.Query, fetchK, requestedMode)
	}
	if log != nil {
		log(args.Query, tel)
	}
	// Apply filters and truncate. recall@filter is reported in the
	// header (`X of Y results matched filters`) so callers can see
	// when the filter was the limiting factor.
	rawCount := len(results)
	if hasFilters {
		results = applySearchFilters(results, args)
	}
	if len(results) > topK {
		results = results[:topK]
	}
	resp := SearchResponse{
		Query: args.Query,
		Mode:  search.ModeNames()[int(effectiveMode)],
	}
	if hasFilters {
		resp.Filter = &SearchFilterMeta{
			Languages:              args.Languages,
			PathContains:           args.PathContains,
			ExcludePathContains:    args.ExcludePathContains,
			CandidatesBeforeFilter: rawCount,
			ResultsAfterFilter:     len(results),
		}
	}
	if len(results) == 0 {
		if hasFilters && rawCount > 0 {
			md := fmt.Sprintf(
				"No results match the filters (search returned %d candidate%s before filtering). "+
					"Try removing or loosening languages / path_contains / exclude_path_contains.",
				rawCount, pluralS(rawCount))
			resp.Results = []SearchResultRow{}
			return dispatchOutput(args.Output, resp, md)
		}
		resp.Results = []SearchResultRow{}
		return dispatchOutput(args.Output, resp, "No results found.")
	}
	recordSearchUsage(recorder, "search", results, sourceRoot)
	for i, r := range results {
		resp.Results = append(resp.Results, SearchResultRow{
			Rank:      i + 1,
			File:      r.Chunk.File,
			StartLine: r.Chunk.StartLine,
			EndLine:   r.Chunk.EndLine,
			Score:     r.Score,
			Text:      r.Chunk.Text,
		})
	}
	header := fmt.Sprintf("Search results for: %q (mode=%s)",
		args.Query, search.ModeNames()[int(effectiveMode)])
	if hasFilters {
		header += fmt.Sprintf(" — %d of %d candidate%s passed filter",
			len(results), rawCount, pluralS(rawCount))
	}
	body := FormatResults(header, results)
	if includeInResponse {
		body += "\n\n" + formatTelemetryLine(tel)
	}
	return dispatchOutput(args.Output, resp, body)
}

// applySearchFilters drops results whose file path doesn't satisfy
// the args' Languages / PathContains / ExcludePathContains
// constraints. Filters are AND-ed; a result must pass every set
// constraint to survive. Languages normalize a leading dot (so
// both "py" and ".py" work); path filters are case-sensitive
// substring matches.
func applySearchFilters(results []search.Result, args SearchArgs) []search.Result {
	// Build a quick-lookup extension set; tolerate either "py" or
	// ".py" inputs.
	var allowExt map[string]bool
	if len(args.Languages) > 0 {
		allowExt = make(map[string]bool, len(args.Languages))
		for _, lang := range args.Languages {
			lang = strings.TrimSpace(lang)
			if lang == "" {
				continue
			}
			if !strings.HasPrefix(lang, ".") {
				lang = "." + lang
			}
			allowExt[strings.ToLower(lang)] = true
		}
	}
	out := results[:0]
	for _, r := range results {
		path := r.Chunk.File
		if allowExt != nil {
			ext := strings.ToLower(filepath.Ext(path))
			if !allowExt[ext] {
				continue
			}
		}
		if args.PathContains != "" && !strings.Contains(path, args.PathContains) {
			continue
		}
		if args.ExcludePathContains != "" && strings.Contains(path, args.ExcludePathContains) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// recordSearchUsage computes snippet_chars + file_chars (the unique-
// file-set's on-disk size) from results and appends one Record. No-op
// if recorder is nil. Stat failures are silent — the count missing a
// file is a smaller wrong than failing the whole tool call over a
// race with the file watcher.
func recordSearchUsage(rec UsageRecorder, callType string, results []search.Result, sourceRoot string) {
	if rec == nil || len(results) == 0 {
		return
	}
	snippetChars := 0
	uniqueFiles := make(map[string]struct{}, len(results))
	for _, r := range results {
		snippetChars += len(r.Chunk.Text)
		if r.Chunk.File != "" {
			uniqueFiles[r.Chunk.File] = struct{}{}
		}
	}
	fileChars := 0
	for f := range uniqueFiles {
		path := f
		if sourceRoot != "" && !filepath.IsAbs(path) {
			path = filepath.Join(sourceRoot, path)
		}
		if info, err := os.Stat(path); err == nil {
			fileChars += int(info.Size())
		}
	}
	rec.Record(callType, len(results), snippetChars, fileChars)
}

// formatTelemetryLine renders a Telemetry as the single "[telemetry]"
// line appended to MCP search responses when TelemetryInResponse is on.
// Matches the format ken search --verbose emits so an operator can grep
// across both surfaces with the same pattern.
func formatTelemetryLine(t search.Telemetry) string {
	return fmt.Sprintf("[telemetry] total=%s stage1=%s rerank=%s blend=%s n=%d cache=%d/%d q_enc=%s cand_enc=%s",
		t.TotalWall, t.Stage1Wall, t.RerankWall, t.BlendWall,
		t.RerankerN, t.RerankerCacheHits, t.RerankerCacheMisses,
		t.RerankerQueryEncode, t.RerankerCandidateEncode)
}

// runFindRelated executes a find_related against a resolved Index. The
// caller has already validated args.FilePath and args.Line.
func runFindRelated(ix *search.Index, args FindRelatedArgs) (*sdk.CallToolResult, any, error) {
	return runFindRelatedWithUsage(ix, args, nil, "")
}

// runFindRelatedWithUsage is runFindRelated + optional usage recording.
// recorder=nil disables tracking; otherwise one Record call is appended
// per successful response. sourceRoot scopes file_chars computation.
func runFindRelatedWithUsage(ix *search.Index, args FindRelatedArgs, recorder UsageRecorder, sourceRoot string) (*sdk.CallToolResult, any, error) {
	topK := args.TopK
	if topK <= 0 {
		topK = DefaultTopK
	}
	resp := FindRelatedResponse{
		Anchor: FindRelatedAnchor{File: args.FilePath, Line: args.Line},
	}
	results, err := ix.FindRelated(args.FilePath, args.Line, topK)
	if err != nil {
		// e.g. BM25-only index: semantic similarity is unavailable. Surface
		// as text so the agent can adapt rather than fail.
		resp.Results = []SearchResultRow{}
		return dispatchOutput(args.Output, resp, err.Error())
	}
	if results == nil {
		resp.Results = []SearchResultRow{}
		return dispatchOutput(args.Output, resp, fmt.Sprintf(
			"No chunk found at %s:%d. Make sure the file is indexed and the line number is within a known chunk.",
			args.FilePath, args.Line))
	}
	if len(results) == 0 {
		resp.Results = []SearchResultRow{}
		return dispatchOutput(args.Output, resp, fmt.Sprintf(
			"No related chunks found for %s:%d.", args.FilePath, args.Line))
	}
	recordSearchUsage(recorder, "find_related", results, sourceRoot)
	for i, r := range results {
		resp.Results = append(resp.Results, SearchResultRow{
			Rank:      i + 1,
			File:      r.Chunk.File,
			StartLine: r.Chunk.StartLine,
			EndLine:   r.Chunk.EndLine,
			Score:     r.Score,
			Text:      r.Chunk.Text,
		})
	}
	header := fmt.Sprintf("Chunks related to %s:%d", args.FilePath, args.Line)
	return dispatchOutput(args.Output, resp, FormatResults(header, results))
}
