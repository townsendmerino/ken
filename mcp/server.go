package mcp

import (
	"context"
	"fmt"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/townsendmerino/ken/internal/chunk"
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
	}, func(ctx context.Context, _ *sdk.CallToolRequest, args SearchArgs) (*sdk.CallToolResult, any, error) {
		return handleSearch(ctx, &cfg, args)
	})

	sdk.AddTool(srv, &sdk.Tool{
		Name: "find_related",
		Description: "Find code chunks semantically similar to a specific location in a file. " +
			"Use after `search` to explore related implementations or callers. " +
			"Pass file_path and line from a prior search result.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, args FindRelatedArgs) (*sdk.CallToolResult, any, error) {
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
	return runSearch(wi.Load(), args)
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
	return runFindRelated(wi.Load(), args)
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
	results, effectiveMode := ix.SearchMode(args.Query, topK, requestedMode)
	if len(results) == 0 {
		return textResult("No results found."), nil, nil
	}
	header := fmt.Sprintf("Search results for: %q (mode=%s)",
		args.Query, search.ModeNames()[int(effectiveMode)])
	return textResult(FormatResults(header, results)), nil, nil
}

// runFindRelated executes a find_related against a resolved Index. The
// caller has already validated args.FilePath and args.Line.
func runFindRelated(ix *search.Index, args FindRelatedArgs) (*sdk.CallToolResult, any, error) {
	topK := args.TopK
	if topK <= 0 {
		topK = DefaultTopK
	}
	results, err := ix.FindRelated(args.FilePath, args.Line, topK)
	if err != nil {
		// e.g. BM25-only index: semantic similarity is unavailable. Surface
		// as text so the agent can adapt rather than fail.
		return textResult(err.Error()), nil, nil
	}
	if results == nil {
		return textResult(fmt.Sprintf("No chunk found at %s:%d. Make sure the file is indexed and the line number is within a known chunk.", args.FilePath, args.Line)), nil, nil
	}
	if len(results) == 0 {
		return textResult(fmt.Sprintf("No related chunks found for %s:%d.", args.FilePath, args.Line)), nil, nil
	}
	header := fmt.Sprintf("Chunks related to %s:%d", args.FilePath, args.Line)
	return textResult(FormatResults(header, results)), nil, nil
}
