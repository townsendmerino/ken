// Package engine is ken's public library surface: open a Workspace over a
// local repo and query it for hybrid lexical+semantic search and
// structural (symbol-level) lookups, without depending on any of ken's
// internal packages.
//
// Open builds (or loads a persisted snapshot of, ADR-039) the same watched
// index and structural index that cmd/ken-mcp serves over MCP — ken-mcp is
// this package's first client, so the two surfaces can't drift apart (see
// docs/internal/DECISIONS.md ADR-046). A Workspace is safe for concurrent
// use; every method is context-aware and none writes to stdout (a
// requirement inherited from ken-mcp's JSON-RPC stdout contract — see
// cmd/ken-mcp/main.go's header comment).
//
// Search results (Hit) carry the file's exact lines: Hit.Text is
// StartLine..EndLine as on disk, and Hit.Hash changes only when those
// lines do. Markdown hits carry their heading chain, and Sections lists a
// markdown file's sections (ADR-047).
//
// Structural results carry line spans (DefinitionSite.EndLine,
// OutlineEntry.StartLine/EndLine, CallSite.Calls, Reference.Lines). Around
// them: SymbolAt maps a line to the symbols containing it; TestsFor finds
// the tests that call a symbol and the directory to run them from;
// Callees lists the calls a symbol makes. Dotted names resolve as
// Type.method and, for Go, pkg.Name and pkg.Type.method. Spans are only
// useful while current, so Refresh brings both indexes up to date for
// files the caller just wrote, and with Watch on the watcher does the same
// after its debounce (ADR-048).
//
// What's NOT here yet (scoped to later work): change-event subscriptions,
// a combined "symbol bundle" fetch, a snapshot directory outside the repo,
// and a persisted structural index.
package engine

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	// Registers the "markdown" chunker (heading-aware .md/.mdx/.markdown
	// chunking) so any engine consumer gets it by default — same
	// cost/benefit cmd/ken and cmd/ken-mcp already made (ADR-023's binary-
	// size seam is about the treesitter chunker specifically; markdown has
	// no embedded grammar data and is cheap to always link). Without this,
	// internal/search's chunkOneFile falls back to whatever Options.Chunker
	// names (regex by default) for .md files, and Hit.HeadingPath/
	// (*Workspace).Sections would have nothing markdown-shaped to agree
	// with.
	_ "github.com/townsendmerino/aikit/chunk/markdown"

	"github.com/townsendmerino/ken/internal/repo"
	"github.com/townsendmerino/ken/internal/search"
	"github.com/townsendmerino/ken/internal/structural"
)

// Options configures Open. The zero value is a reasonable default: hybrid
// mode, the regex chunker, the model at ~/.ken/model, snapshot persistence
// off, no file watcher — i.e. a one-shot, build-once-and-return workspace.
// Set Snapshot and Watch for a long-lived server (this is what cmd/ken-mcp
// does).
type Options struct {
	// Mode selects the retrieval pipeline: "bm25", "semantic", "hybrid", or
	// "hybrid-rerank" — the same strings search.ParseMode accepts. ""
	// defaults to "hybrid".
	Mode string

	// Chunker is the registered aikit/chunk.Chunker name (e.g. "regex",
	// "line", or "treesitter" if the calling binary registered it — see
	// this file's package doc and ADR-023: engine itself never imports the
	// treesitter chunker, so registering it is the importing binary's job,
	// same as cmd/ken and cmd/ken-mcp already do). "" defaults to "regex".
	Chunker string

	// ModelDir is the Model2Vec snapshot directory (must contain
	// model.safetensors), needed for every Mode but "bm25". "" defaults to
	// ~/.ken/model, the canonical end-user location ("ken download-model"
	// populates it without any Python tooling).
	ModelDir string

	// Watch keeps a background file watcher running, republishing a fresh
	// snapshot ~2s after a file changes (ADR-012). false builds once and
	// never updates — the right shape for a one-shot CLI/batch caller;
	// true is what a long-lived server (ken-mcp, tack) wants.
	Watch bool

	// Snapshot persists the built index to <root>/.ken/{snapshot.bin,
	// snapshot.manifest} and, on the next Open of the same root, loads +
	// drift-checks it instead of a full rebuild (ADR-039, cold-start M1).
	Snapshot bool

	// DisableEnrichment turns off the Arm B structural-enrichment label
	// that's prepended to each chunk by default (search.FSOptions.DisableEnrichment).
	DisableEnrichment bool

	// LazyEnrichment defers the enrichment parse off the cold-build path:
	// serve raw hybrid first, enrich and republish in the background
	// (cold-start M2; search.FSOptions.LazyEnrichment).
	LazyEnrichment bool

	// StagedEmbedding defers the embedding arm off the cold-build path:
	// serve BM25 first, upgrade to the configured Mode once vectors are
	// ready (cold-start M4; search.FSOptions.StagedEmbedding).
	StagedEmbedding bool

	// MmapModel memory-maps the embedding model instead of heap-reading it
	// (ADR-045; search.FSOptions.MmapModel). Right for a long-lived server
	// sharing one model file's pages across repos; wrong for a short-lived
	// process where cold-start latency dominates more than peak RSS.
	MmapModel bool

	// DenseRetriever selects the dense-arm implementation: "" (ann.Flat,
	// exact), "flat-i8", or "flat-binary-i8" (ADR-043;
	// search.FSOptions.DenseRetriever). flat-binary-i8 is the repo/mid-scale
	// choice; flat-i8 holds recall at real kernel scale where
	// flat-binary-i8's agreement with exact search degrades.
	DenseRetriever string

	// DisableFoldMigrations turns off Tier-1 SQL migration-history folding
	// (search.FSOptions.DisableFoldMigrations).
	DisableFoldMigrations bool

	// EmbedCache, when non-nil, is a persistent content-hash→vector cache
	// consulted on every embed during the build/watch/lazy-enrich paths
	// (cold-start M3; search.FSOptions.EmbedCache). nil (the default) means
	// no cache — every embed encodes fresh.
	EmbedCache VecCache

	// LogWriter receives best-effort diagnostic lines (snapshot load/write
	// outcomes, fold-migration warnings). nil discards. MUST NOT be stdout
	// for any caller that also speaks a stdio protocol on stdout (ken-mcp's
	// JSON-RPC contract) — write to stderr or a file instead.
	LogWriter io.Writer
}

// Workspace is a open, queryable view of one repo: a hybrid lexical +
// semantic index (Search, FindRelated) and a structural symbol index
// (Definition, References, Callers, Outline, Symbols). Methods are safe for
// concurrent use. Call Close when done.
type Workspace struct {
	root     string
	mode     search.Mode
	chunker  string
	modelDir string
	watch    bool

	snapshotEnabled bool
	fsOpts          search.FSOptions
	logf            func(format string, args ...any)

	index *search.WatchedIndex

	onFlushMu sync.Mutex
	onFlush   func(msg string)

	structural structuralState

	// matcher is the walk's admission rules (gitignore, size cap, binary
	// and symlink rejection), built on the first structural update; see
	// admit.
	matcherOnce sync.Once
	matcher     *repo.Matcher

	// refreshing counts Refresh calls in flight; while it is non-zero a
	// flush marks the snapshot dirty instead of rewriting it.
	refreshing    atomic.Int32
	snapshotDirty atomic.Bool
}

// admit reports whether the initial walk would have indexed rel — the
// same rules the search side re-checks on every flush.
func (w *Workspace) admit(rel string) bool {
	w.matcherOnce.Do(func() { w.matcher = repo.NewMatcher(repo.Options{Root: w.root}) })
	return w.matcher.ShouldIndex(rel)
}

// defaultModelDir returns ~/.ken/model, falling back to "" (meaning: let
// the model load fail with a clear error) if the home directory can't be
// resolved.
func defaultModelDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".ken", "model")
}

// Open builds (or loads a persisted snapshot of) a Workspace over root.
// root must be a local directory; resolving a remote/URL repo to a local
// clone is the caller's job (ken-mcp's mcp/clone.go does this before
// calling Open with the clone's temp directory).
//
// ctx scopes only the initial build (a long walk+chunk+embed can be
// cancelled); it is not retained. An error is returned only for a
// genuinely unusable root or a model that's required but missing/corrupt
// — a bad or stale on-disk snapshot degrades to a live rebuild instead of
// failing (see snapshot.go).
func Open(ctx context.Context, root string, opts Options) (*Workspace, error) {
	modeStr := opts.Mode
	if modeStr == "" {
		modeStr = "hybrid"
	}
	mode, err := search.ParseMode(modeStr)
	if err != nil {
		return nil, fmt.Errorf("engine: %w", err)
	}
	chunker := opts.Chunker
	if chunker == "" {
		chunker = "regex"
	}
	modelDir := opts.ModelDir
	if modelDir == "" {
		modelDir = defaultModelDir()
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("engine: resolve root %q: %w", root, err)
	}
	if fi, statErr := os.Stat(absRoot); statErr != nil {
		return nil, fmt.Errorf("engine: root %q: %w", root, statErr)
	} else if !fi.IsDir() {
		return nil, fmt.Errorf("engine: root %q is not a directory", root)
	}

	logWriter := opts.LogWriter
	if logWriter == nil {
		logWriter = io.Discard
	}

	w := &Workspace{
		root:            absRoot,
		mode:            mode,
		chunker:         chunker,
		modelDir:        modelDir,
		watch:           opts.Watch,
		snapshotEnabled: opts.Snapshot,
		fsOpts: search.FSOptions{
			DisableFoldMigrations: opts.DisableFoldMigrations,
			LogWriter:             logWriter,
			DisableEnrichment:     opts.DisableEnrichment,
			LazyEnrichment:        opts.LazyEnrichment,
			StagedEmbedding:       opts.StagedEmbedding,
			DenseRetriever:        opts.DenseRetriever,
			MmapModel:             opts.MmapModel,
			EmbedCache:            opts.EmbedCache,
		},
		logf: func(format string, args ...any) { fmt.Fprintf(logWriter, format+"\n", args...) },
	}
	w.structural.builder = func(ctx context.Context) (*structural.Index, error) {
		return structural.BuildWithContext(ctx, w.root)
	}
	w.structural.update = func(idx *structural.Index, paths []string) (*structural.Index, bool) {
		return idx.Update(w.root, paths, w.admit, w.logf)
	}

	var wi *search.WatchedIndex
	if opts.Snapshot {
		wi = w.tryLoadSnapshot(modeStr)
	}
	if wi == nil {
		wi, err = search.NewWatchedIndexWithContext(ctx, absRoot, mode, chunker, modelDir, opts.Watch, w.fsOpts)
		if err != nil {
			return nil, fmt.Errorf("engine: build index for %s: %w", absRoot, err)
		}
		if opts.Snapshot {
			w.persistSnapshot(wi)
		}
	}
	// Every flush — the watcher's, or Refresh's — hands its paths to the
	// structural index too (K13), on the flushing goroutine, before the
	// OnFlush callbacks below run.
	wi.SetOnBatch(w.structural.apply)
	wi.SetOnFlush(func(msg string) {
		if opts.Snapshot {
			if w.refreshing.Load() > 0 {
				// Refresh defers the snapshot write to Close (or the
				// next watcher flush): a caller refreshing after every
				// edit would otherwise rewrite the whole snapshot each
				// time. A snapshot left behind is safe — the next Open's
				// drift check reconciles the files that changed.
				w.snapshotDirty.Store(true)
			} else {
				w.snapshotDirty.Store(false)
				w.persistSnapshot(wi)
			}
		}
		w.onFlushMu.Lock()
		cb := w.onFlush
		w.onFlushMu.Unlock()
		if cb != nil {
			cb(msg)
		}
	})
	w.index = wi
	return w, nil
}

// Root returns the absolute path Open was given.
func (w *Workspace) Root() string { return w.root }

// SetOnFlush installs a callback invoked once per published index snapshot
// (file-watch republish, or extra-chunk union) with a one-line
// human-readable message — the same hook search.WatchedIndex.SetOnFlush
// exposes, so a caller can log reindex activity without reaching past
// Workspace for a *search.WatchedIndex it can't even name (internal/search
// isn't importable outside this module). Pass nil to remove. If Snapshot
// was requested at Open, the snapshot is re-persisted on every flush
// regardless of whether a callback is set.
func (w *Workspace) SetOnFlush(f func(msg string)) {
	w.onFlushMu.Lock()
	w.onFlush = f
	w.onFlushMu.Unlock()
}

// Close stops the file watcher (if any) and any in-flight background
// structural build, and — if Snapshot was requested and a Refresh left
// the on-disk snapshot behind the index — writes it. Idempotent.
func (w *Workspace) Close() error {
	w.structural.stop()
	err := w.index.Close()
	if w.snapshotEnabled && w.snapshotDirty.CompareAndSwap(true, false) {
		w.persistSnapshot(w.index)
	}
	return err
}
