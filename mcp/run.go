package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sync/atomic"

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

	// DB, when non-nil, wires Tier-2 DB support: registers the
	// reindex_db MCP tool AND integrates DB chunks into the embedded
	// search results. SDK authors using mcp.Run wire this via
	// mcp/db.Setup → mcp/db.Refresher. mcp.Run itself stays DB-free
	// (no pgx / sqlite / mysql in the import graph) so embedded-corpus
	// binaries that don't import mcp/db keep the v0.6.0 small-binary
	// posture — verified by mcp/binary_contract_test.go.
	//
	// Lifecycle:
	//   1. mcp.Run builds the embedded *search.Index from fsys.
	//   2. Calls opts.DB.Start(ctx, onExtras) — onExtras is an
	//      mcp.Run-internal closure that calls
	//      baseIx.WithExtraChunks(extras) and atomic-stores the
	//      result. Cleanup is deferred for process shutdown.
	//   3. Search / find_related handlers read the current Index via
	//      atomic.Pointer.Load() — they see the latest snapshot
	//      including DB chunks after each refresh.
	//   4. The reindex_db tool calls opts.DB.TryRefresh which
	//      eventually fires onExtras with the latest chunks.
	//
	// v0.8.0 Part 3 addendum (ADR-020): this completes the chunk-
	// integration loop that v0.8.0 Part 3's initial ship deferred
	// to v0.9.0. The deferral is no longer applicable — DB chunks
	// become searchable in the next search/find_related call after
	// a successful refresh.
	DB DBIntegration

	// PrebuiltIndex is an optional pre-serialized index produced by
	// `ken build-index` (or search.BuildAndSerializeIndex programmatically).
	// When non-nil, mcp.Run loads it via search.LoadSerializedIndex
	// instead of walking + chunking + embedding fsys at startup —
	// the v0.8.3 cold-start optimization (ADR-024, closes #10).
	//
	// SDK authors who follow the convention (write the bytes to
	// `<corpus>/.ken/index.bin`) can leave this nil; mcp.Run
	// auto-discovers the file in fsys at that path. The explicit
	// override is for SDK authors using a non-conventional layout
	// (index outside the corpus FS, in a sibling embed.FS, etc.).
	//
	// On any load failure (corrupt bytes, format-version mismatch,
	// mode/chunker mismatch vs Options.Mode/Options.ChunkerName),
	// mcp.Run logs a stderr warning naming the reason and falls
	// back to building from fsys — the pre-built path is purely an
	// optimization, never a requirement.
	//
	// v0.8.3 (ADR-024).
	PrebuiltIndex []byte
}

// prebuiltIndexPath is the convention-over-configuration location
// inside the corpus FS where mcp.Run looks for a serialized index.
// No env var, no Options field for the path itself — SDK authors
// who follow the convention get auto-discovery; those who don't can
// set Options.PrebuiltIndex explicitly.
const prebuiltIndexPath = ".ken/index.bin"

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
//
// v0.8.0 Part 3 addendum (ADR-020): orchestrates the chunk-integration
// loop when opts.DB is non-nil. Build sequence:
//
//  1. Build the embedded *search.Index from fsys (via buildEmbeddedIndex).
//  2. Wrap in an atomic.Pointer[search.Index] — handlers read via .Load().
//  3. If opts.DB != nil, define onExtras = func(extras) {
//     ixPtr.Store(baseIx.WithExtraChunks(extras))
//     } and call opts.DB.Start(ctx, onExtras). Defer the returned
//     cleanup func for process shutdown.
//  4. Build the server with handlers wired to ixPtr.Load().
//  5. Run.
//
// The baseIx closure captures the original snapshot so each refresh
// rebuilds against the SAME original — extras replace, they don't
// accumulate. See *search.Index.WithExtraChunks for the rebuild
// contract.
func runOnTransport(ctx context.Context, fsys fs.FS, opts Options, transport sdk.Transport) error {
	ix, logger, err := buildEmbeddedIndex(fsys, opts)
	if err != nil {
		return err
	}

	// Atomic-pointer indirection so refreshes can publish new snapshots
	// without the handlers needing a mutex. Initialized with the
	// baseline (no extras yet) Index.
	var ixPtr atomic.Pointer[search.Index]
	ixPtr.Store(ix)

	// Tier-2 chunk integration (v0.8.0 Part 3 addendum). When opts.DB
	// is wired, start the integration BEFORE the server runs so the
	// initial introspection's swap fires before any agent search.
	cleanup := func() {}
	if opts.DB != nil {
		baseIx := ix // capture the no-extras snapshot for rebuilds
		onExtras := func(extras []chunk.Chunk) {
			newIx := baseIx.WithExtraChunks(extras)
			ixPtr.Store(newIx)
			logger.Logf(LogInfo, "mcp.Run: integrated %d DB chunks into the embedded index", len(extras))
		}
		c, derr := opts.DB.Start(ctx, onExtras)
		if derr != nil {
			return fmt.Errorf("mcp.Run: opts.DB.Start: %w", derr)
		}
		if c != nil {
			cleanup = c
		}
	}
	defer cleanup()

	srv := newServerForIndex(&ixPtr, logger, opts.DB)
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

// buildEmbeddedIndex materializes the corpus *search.Index + the
// scoped *Logger. Validates opts, loads the model (downgrades to bm25
// with a warning if unavailable), and either loads a pre-built index
// (v0.8.3 cold-start optimization, ADR-024) or runs the
// walk+chunk+embed pass. The caller (runOnTransport) wraps the Index
// in an atomic.Pointer and wires DB chunk integration on top.
func buildEmbeddedIndex(fsys fs.FS, opts Options) (*search.Index, *Logger, error) {
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

	// v0.8.3 (ADR-024): try the pre-built index path before falling
	// back to the build-from-corpus pass. Two sources:
	//   1. Options.PrebuiltIndex (explicit override; non-conventional layout)
	//   2. fsys/.ken/index.bin (convention-over-configuration)
	// Either source's failure is non-fatal: the lazy fallback ensures
	// the binary always works even if the pre-built bytes go bad.
	if ix, src := tryLoadPrebuilt(fsys, opts, mode, chunkerName, model, logger); ix != nil {
		logger.Logf(LogInfo, "loaded pre-built index from %s (%d chunks, mode=%s chunker=%s)",
			src, ix.Len(), search.ModeNames()[int(mode)], chunkerName)
		return ix, logger, nil
	}

	ix, err := search.FromFSWithModel(fsys, mode, chunkerName, model)
	if err != nil {
		return nil, nil, fmt.Errorf("build index from fsys: %w", err)
	}
	logger.Logf(LogInfo, "indexed %d chunks (mode=%s chunker=%s)", ix.Len(), search.ModeNames()[int(mode)], chunkerName)

	return ix, logger, nil
}

// tryLoadPrebuilt attempts the v0.8.3 pre-built-index path. Returns
// the loaded *Index + a human-readable src tag on success, or
// (nil, "") on any failure (corrupt, version mismatch, mode/chunker
// mismatch, missing file). All non-"missing file" failures are
// logged at warn so operators can see why the optimization was
// skipped; the missing-file case is silent (the pre-built path is
// opt-in).
//
// The src tag distinguishes the explicit-override vs auto-discovery
// path — "Options.PrebuiltIndex" vs ".ken/index.bin (auto-discovered)".
// buildEmbeddedIndex threads it into the success info log so
// operators (and tests, per M5 of the post-v0.8.3 bug review) can
// see which path actually fired.
//
// Explicit Options.PrebuiltIndex wins over the .ken/index.bin
// convention; if both are present and explicit is set, the
// convention file is never consulted.
func tryLoadPrebuilt(fsys fs.FS, opts Options, mode search.Mode, chunkerName string, model *embed.StaticModel, logger *Logger) (*search.Index, string) {
	var (
		data []byte
		src  string
	)
	switch {
	case opts.PrebuiltIndex != nil:
		data = opts.PrebuiltIndex
		src = "Options.PrebuiltIndex"
	default:
		b, err := fs.ReadFile(fsys, prebuiltIndexPath)
		if err != nil {
			// Missing file = no opt-in; silent. Any other error
			// (permission, malformed FS) is unusual enough to log.
			if !errors.Is(err, fs.ErrNotExist) {
				logger.Logf(LogDebug, "no pre-built index at %s: %v", prebuiltIndexPath, err)
			}
			return nil, ""
		}
		data = b
		src = fmt.Sprintf("%s (auto-discovered)", prebuiltIndexPath)
	}

	loadOpts := search.LoadOptions{
		ExpectedMode:    search.ModeNames()[int(mode)],
		ExpectedChunker: chunkerName,
		Model:           model,
	}
	ix, err := search.LoadSerializedIndex(data, loadOpts)
	if err != nil {
		logger.Logf(LogWarn,
			"failed to load pre-built index from %s (%v); falling back to build-from-corpus. "+
				"Re-run `ken build-index` to refresh.", src, err)
		return nil, ""
	}
	return ix, src
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
// find_related tools dispatch to the index pointed to by ixPtr. The
// agent's `repo` argument is accepted (the wire schema stays identical
// to the cmd/ken-mcp Cache-backed server) but ignored — logged at
// debug when non-empty so an operator can see when an agent is
// mis-passing repo.
//
// v0.8.0 Part 3 addendum (ADR-020): handlers read via ixPtr.Load()
// rather than capturing a fixed *Index, so DB chunk refreshes that
// publish a new snapshot are visible to the very next agent
// search/find_related call. When db is non-nil, the reindex_db tool
// is registered too, calling db.TryRefresh.
func newServerForIndex(ixPtr *atomic.Pointer[search.Index], logger *Logger, db DBIntegration) *sdk.Server {
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
		return runSearch(ixPtr.Load(), args)
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
		return runFindRelated(ixPtr.Load(), args)
	})

	// v0.8.0 Part 2 + Part 3 (ADR-020): reindex_db tool. Same
	// conditional-registration pattern as NewServer — registered ONLY
	// when db is non-nil so tools/list stays honest for plain docs-only
	// embedded-corpus binaries that don't wire mcp/db.Setup.
	if db != nil {
		cfg := &Config{DB: db}
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
