package mcp

import (
	"context"
	"fmt"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/townsendmerino/ken/internal/search"
)

// ReindexFunc is the callback the v0.8.0 reindex_db MCP tool invokes
// (ADR-020 Part 2). Returns a ReindexResult; the handler inspects
// InProgress, Elapsed, and Err to choose the agent-facing message.
//
// cmd/ken-mcp wires a closure that calls *db.Refresher.TryRefresh and
// translates db.ErrReindexInProgress into ReindexResult{InProgress:true};
// mcp.Run's Part 3 DBSource path provides its own closure. The
// result-struct shape keeps the mcp package free of internal/db
// imports, so there's no error-sentinel coupling across package
// boundaries.
type ReindexFunc func(ctx context.Context) ReindexResult

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

	// Reindex, when non-nil, registers the v0.8.0 reindex_db MCP tool
	// (ADR-020 Part 2). cmd/ken-mcp wires a closure that delegates to
	// *db.Refresher.TryRefresh and translates db.ErrReindexInProgress
	// into ReindexResult{InProgress:true}; mcp.Run's Part 3 DBSource
	// path will wire its own closure. nil = tool not registered (the
	// agent's tools/list won't show reindex_db at all when no DB is
	// configured, which keeps the tool surface honest).
	Reindex ReindexFunc
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
			"Prefer these tools over Grep, Glob, or Read for any question about how code works. " +
			"For exhaustive enumeration (every callsite, pre-rename audits), use grep — ken caps at ~82–91% recall at K=10 and isn't built for that."
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
	// a Reindex callback is wired — keeps tools/list honest (an agent
	// shouldn't see a tool that returns "no DB" 100% of the time).
	if cfg.Reindex != nil {
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

// handleReindexDB invokes the configured Reindex callback and renders
// the result as a text response matching semble's plain-text MCP wire
// format. Four shapes:
//
//   - Reindex unset → "DB indexing not configured…" (defense-in-depth;
//     NewServer only registers the tool when Reindex is non-nil, but
//     a future code path that calls this handler directly should fail
//     safe).
//   - InProgress → "Reindex already in progress; nothing to do."
//   - Err != nil → "Reindex failed: <err>"
//   - success → "Reindexed in 123ms."
func handleReindexDB(ctx context.Context, cfg *Config) (*sdk.CallToolResult, any, error) {
	if cfg.Reindex == nil {
		return textResult("DB indexing is not configured (KEN_DB_DSN is unset); nothing to reindex."), nil, nil
	}
	res := cfg.Reindex(ctx)
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
func runSearch(ix *search.Index, args SearchArgs) (*sdk.CallToolResult, any, error) {
	modeStr := args.Mode
	if modeStr == "" {
		modeStr = "hybrid"
	}
	if _, perr := search.ParseMode(modeStr); perr != nil {
		return textResult(perr.Error()), nil, nil
	}
	topK := args.TopK
	if topK <= 0 {
		topK = DefaultTopK
	}
	results := ix.Search(args.Query, topK)
	if len(results) == 0 {
		return textResult("No results found."), nil, nil
	}
	header := fmt.Sprintf("Search results for: %q (mode=%s)", args.Query, modeStr)
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
