package mcp

import (
	"context"
	"fmt"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/townsendmerino/ken/internal/search"
)

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
			"Prefer these tools over Grep, Glob, or Read for any question about how code works."
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

	return srv
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
	return "", fmt.Errorf("No repo specified and no default index. Pass an https:// or http:// git URL or local directory path as `repo`.")
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
	// Validate / default the mode arg. An unknown mode is a user-input
	// error → return as text rather than an MCP-protocol error.
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

	ix, err := cfg.Cache.Get(ctx, source)
	if err != nil {
		return textResult(fmt.Sprintf("Failed to index %q: %s", source, err.Error())), nil, nil
	}
	results := ix.Search(args.Query, topK)
	if len(results) == 0 {
		return textResult("No results found."), nil, nil
	}
	header := fmt.Sprintf("Search results for: %q (mode=%s)", args.Query, modeStr)
	return textResult(FormatResults(header, results)), nil, nil
}

func handleFindRelated(ctx context.Context, cfg *Config, args FindRelatedArgs) (*sdk.CallToolResult, any, error) {
	if args.FilePath == "" || args.Line <= 0 {
		return textResult("file_path is required and line must be ≥ 1."), nil, nil
	}
	source, err := resolveRepo(cfg, args.Repo)
	if err != nil {
		return textResult(err.Error()), nil, nil
	}
	topK := args.TopK
	if topK <= 0 {
		topK = DefaultTopK
	}

	ix, err := cfg.Cache.Get(ctx, source)
	if err != nil {
		return textResult(fmt.Sprintf("Failed to index %q: %s", source, err.Error())), nil, nil
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
