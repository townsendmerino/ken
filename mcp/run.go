package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/townsendmerino/ken/internal/chunk"
	"github.com/townsendmerino/ken/internal/embed"
	"github.com/townsendmerino/ken/internal/search"
)

// Options configures Run. The zero value is meaningful: it serves a
// BM25-only index over the supplied fsys with the regex chunker, logging
// warnings to os.Stderr at the "warn" level.
//
// Validation mirrors cmd/ken-mcp's env-var contract (ADR-009): a typoed
// enum logs a stderr warning and falls back to the documented default
// rather than failing the run, so a misconfigured callsite still produces
// a usable server.
type Options struct {
	// Mode is the search mode: "bm25", "semantic", or "hybrid". Default
	// "hybrid". If mode != "bm25" but the model can't be loaded (neither
	// ModelFS nor ModelDir is usable), Run downgrades to "bm25" with a
	// stderr warning — matches cmd/ken-mcp's first-launch behavior.
	Mode string

	// ChunkerName picks the chunker for the embedded corpus. Default
	// "regex". Other values: "treesitter" (requires importing
	// internal/chunk/treesitter for side-effect registration), "line"
	// (universal fallback), "markdown" (requires importing
	// internal/chunk/markdown; recommended for docs corpora).
	ChunkerName string

	// ModelDir is a directory path to a Model2Vec snapshot, used when
	// ModelFS is nil. The standard HF layout applies: tokenizer.json,
	// config.json, model.safetensors. Ignored entirely if ModelFS is set.
	ModelDir string

	// ModelFS is an optional fs.FS whose root contains a Model2Vec
	// snapshot. Typical use: bake the model into a single-binary MCP
	// server via //go:embed. When set, ModelDir is ignored.
	//
	// Root your embed.FS at the snapshot directory itself (or use fs.Sub
	// to rebase). The model files must live at ./tokenizer.json,
	// ./config.json, ./model.safetensors inside ModelFS.
	ModelFS fs.FS

	// LogLevel is "debug", "info", "warn", or "error" (default "warn").
	LogLevel string

	// LogWriter is the destination for server logs (default os.Stderr).
	// MUST NOT be os.Stdout when using the default StdioTransport — see
	// the stdout/stderr contract in cmd/ken-mcp/main.go.
	LogWriter io.Writer

	// Reindex, when non-nil, registers the v0.8.0 Part 2 reindex_db MCP
	// tool (ADR-020). SDK authors using mcp.Run wire this via
	// mcp/db.Setup; mcp.Run itself stays DB-free so embedded-corpus
	// binaries that don't import mcp/db keep the v0.6.0 small-binary
	// posture (no pgx / sqlite / mysql in the dep tree).
	//
	// Important caveat for v0.8.0 Part 3: when Reindex is non-nil, the
	// reindex_db tool DOES run IndexSchema on each invocation (and the
	// LISTEN/interval pipelines fire normally) — but the chunks
	// captured are NOT yet unioned into the embedded-corpus search
	// results that mcp.Run serves. Chunk integration into mcp.Run's
	// static *search.Index is deferred to v0.9.0; ADR-020 Part 3's
	// Consequences section documents the deferral with the rationale.
	// The tool is still useful — it validates the DSN works, fires
	// LISTEN handlers on schema changes, exercises the interval-ticker
	// path, and gives operators an explicit "reindex on demand"
	// signal — but the schema isn't searchable from within the
	// embedded binary until the v0.9.0 follow-up lands.
	Reindex ReindexFunc
}

// Run starts an MCP server (over the SDK's default StdioTransport)
// serving search and find_related over the single fixed corpus rooted at
// fsys. Blocks until ctx is canceled or the client closes the transport;
// returns nil for either clean-shutdown path.
//
// The tool wire format and argument schemas are identical to the stock
// cmd/ken-mcp binary's, so any agent already trained against semble's or
// ken's MCP server works unchanged. The agent-supplied `repo` argument
// is ignored (logged at debug level when non-empty): the corpus is fixed
// by fsys for the lifetime of the call.
//
// Run is the embedded-corpus build pattern (ADR-016). Canonical use:
//
//	//go:embed docs/*.md
//	var corpus embed.FS
//
//	//go:embed model/*
//	var modelFS embed.FS
//
//	func main() {
//	    if err := mcp.Run(context.Background(), corpus, mcp.Options{
//	        Mode: "hybrid", ChunkerName: "markdown", ModelFS: modelFS,
//	    }); err != nil { log.Fatal(err) }
//	}
//
// For multi-repo / per-request URL clone / file-watching use cases, use
// cmd/ken-mcp directly — Run intentionally doesn't carry that machinery.
func Run(ctx context.Context, fsys fs.FS, opts Options) error {
	return runOnTransport(ctx, fsys, opts, &sdk.StdioTransport{})
}

// runOnTransport is Run with an explicit transport seam. mcp.Run wires
// StdioTransport; in-process tests wire one half of NewInMemoryTransports
// so they can drive the server without spawning a subprocess.
func runOnTransport(ctx context.Context, fsys fs.FS, opts Options, transport sdk.Transport) error {
	srv, err := buildEmbeddedServer(fsys, opts)
	if err != nil {
		return err
	}
	if err := srv.Run(ctx, transport); err != nil {
		// Client-closed-stdin is the canonical clean-shutdown path; surface
		// it as nil error so callers don't have to special-case it.
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	return nil
}

// buildEmbeddedServer materializes a single-corpus *sdk.Server from opts
// and fsys: validates opts, loads the model (downgrades to bm25 with a
// warning if unavailable), builds the index once, and registers the
// search/find_related tools with handlers wired to that index.
func buildEmbeddedServer(fsys fs.FS, opts Options) (*sdk.Server, error) {
	// Bootstrap logger at warn so we can warn about an invalid LogLevel
	// before applying it (same chicken-and-egg dance as cmd/ken-mcp).
	logger := NewLogger(opts.LogWriter, LogWarn)
	logLevelStr := ValidateEnum("Options.LogLevel", opts.LogLevel, LogLevelNames(), "warn", logger)
	logger.Level = ParseLogLevel(logLevelStr)

	chunkerName := ValidateEnum("Options.ChunkerName", opts.ChunkerName, chunk.Names(), "regex", logger)
	modeStr := ValidateEnum("Options.Mode", opts.Mode, search.ModeNames(), "hybrid", logger)
	mode, err := search.ParseMode(modeStr)
	if err != nil {
		// ValidateEnum guaranteed modeStr ∈ ModeNames(); only a future
		// drift between ParseMode and ModeNames can land here.
		logger.Logf(LogError, "internal: validated mode %q failed ParseMode: %v — defaulting to bm25", modeStr, err)
		mode = search.ModeBM25
	}

	// Resolve the model. ModelFS wins over ModelDir. Failure here is
	// non-fatal: downgrade to bm25 with a warning (cmd/ken-mcp parity).
	var model *embed.StaticModel
	if mode != search.ModeBM25 {
		m, src, lerr := loadModel(opts)
		switch {
		case lerr != nil:
			logger.Logf(LogWarn,
				"could not load model (%s): %v — downgrading to bm25 "+
					"(set Options.ModelFS or Options.ModelDir to enable semantic/hybrid)",
				src, lerr)
			mode = search.ModeBM25
		default:
			model = m
			logger.Logf(LogInfo, "loaded model from %s (dim=%d vocab=%d)", src, model.Dim(), model.VocabSize())
		}
	}

	ix, err := search.FromFSWithModel(fsys, mode, chunkerName, model)
	if err != nil {
		return nil, fmt.Errorf("build index from fsys: %w", err)
	}
	logger.Logf(LogInfo, "indexed %d chunks (mode=%s chunker=%s)", ix.Len(), search.ModeNames()[int(mode)], chunkerName)

	return newServerForIndex(ix, logger, opts.Reindex), nil
}

// loadModel returns the Model2Vec snapshot to use, preferring ModelFS
// over ModelDir. Returns the model, a short description of where it came
// from (for logging), and any error. If neither source is configured,
// returns (nil, "no-model-source", err).
func loadModel(opts Options) (*embed.StaticModel, string, error) {
	if opts.ModelFS != nil {
		m, err := embed.LoadFromFS(opts.ModelFS, ".")
		if err != nil {
			return nil, "Options.ModelFS", err
		}
		return m, "Options.ModelFS", nil
	}
	if opts.ModelDir != "" {
		m, err := embed.LoadFromFS(os.DirFS(opts.ModelDir), ".")
		if err != nil {
			return nil, fmt.Sprintf("Options.ModelDir=%q", opts.ModelDir), err
		}
		return m, fmt.Sprintf("Options.ModelDir=%q", opts.ModelDir), nil
	}
	return nil, "no-model-source", errors.New("neither Options.ModelFS nor Options.ModelDir is set")
}

// newServerForIndex constructs an *sdk.Server whose search /
// find_related tools dispatch to the fixed index. The agent's `repo`
// argument is accepted (the wire schema stays identical to the
// cmd/ken-mcp Cache-backed server) but ignored — logged at debug when
// non-empty so an operator can see when an agent is mis-passing repo.
//
// v0.8.0 Part 3 (ADR-020): when reindex is non-nil, the reindex_db
// tool is registered alongside search + find_related, matching
// NewServer's conditional-registration pattern. SDK authors using
// mcp.Run wire this via mcp/db.Setup; reindex stays nil for plain
// docs-only embedded-corpus binaries. See the caveat on
// Options.Reindex: in v0.8.0 the tool RUNS introspection but the
// chunks aren't yet unioned into ix's search results — chunk
// integration is v0.9.0.
func newServerForIndex(ix *search.Index, logger *Logger, reindex ReindexFunc) *sdk.Server {
	srv := sdk.NewServer(&sdk.Implementation{
		Name:    "ken",
		Version: "0",
	}, &sdk.ServerOptions{
		Instructions: "Instant code search over a fixed embedded corpus. " +
			"Call `search` to find relevant content; call `find_related` on a result to discover similar passages. " +
			"The `repo` argument is accepted for wire compatibility with semble's MCP server but is ignored — the corpus is fixed at server startup.",
	})

	sdk.AddTool(srv, &sdk.Tool{
		Name: "search",
		Description: "Search the embedded corpus with a natural-language or code query. " +
			"The `repo` argument is accepted but ignored.",
	}, func(_ context.Context, _ *sdk.CallToolRequest, args SearchArgs) (*sdk.CallToolResult, any, error) {
		if args.Repo != "" {
			logger.Logf(LogDebug, "search: repo=%q ignored (embedded-corpus mode)", args.Repo)
		}
		return runSearch(ix, args)
	})

	sdk.AddTool(srv, &sdk.Tool{
		Name: "find_related",
		Description: "Find passages semantically similar to a specific location. " +
			"Pass file_path and line from a prior search result. The `repo` argument is accepted but ignored.",
	}, func(_ context.Context, _ *sdk.CallToolRequest, args FindRelatedArgs) (*sdk.CallToolResult, any, error) {
		if args.Repo != "" {
			logger.Logf(LogDebug, "find_related: repo=%q ignored (embedded-corpus mode)", args.Repo)
		}
		if args.FilePath == "" || args.Line <= 0 {
			return textResult("file_path is required and line must be ≥ 1."), nil, nil
		}
		return runFindRelated(ix, args)
	})

	// v0.8.0 Part 2 + Part 3 (ADR-020): reindex_db tool. Same
	// conditional-registration pattern as NewServer — registered ONLY
	// when reindex is non-nil so tools/list stays honest for plain
	// docs-only embedded-corpus binaries that don't wire mcp/db.Setup.
	//
	// Captures cfg via closure with just the Reindex field so the
	// handler shape matches handleReindexDB's contract.
	if reindex != nil {
		cfg := &Config{Reindex: reindex}
		sdk.AddTool(srv, &sdk.Tool{
			Name: "reindex_db",
			Description: "Trigger a Tier 2 database schema reindex on demand. " +
				"Use after running a migration to refresh ken's view of the database schema before asking schema-dependent questions. " +
				"Returns immediately with `already in progress` if another reindex is in flight (no queuing). " +
				"Available whenever a DB is configured.",
		}, func(ctx context.Context, _ *sdk.CallToolRequest, _ ReindexDBArgs) (*sdk.CallToolResult, any, error) {
			return handleReindexDB(ctx, cfg)
		})
	}

	return srv
}
