package search

// construct.go — WatchedIndex constructors (split out of watch.go, #6).
//
// The set looks like many entry points but is four DISTINCT construction
// inputs, not seven variants of one build (why a functional-options collapse
// would be worse, not better — #7):
//
//   - WrapStatic: wrap an already-built *Index (ADR-024 prebuilt), frozen, no
//     watcher, no live corpus.
//   - NewWatchedIndex / …WithOptions / …WithContext: WALK the tree and build.
//     The three differ only in whether a ctx and FSOptions are supplied; all
//     three delegate to newWatchedIndexWithDebounce → assembleWatched.
//   - NewWatchedIndexFromSnapshot: SEED from an in-hand corpus (chunks+vecs
//     loaded off disk, cold-start M1), no walk.
//   - NewWatchedIndexReconciled: seed from a snapshot corpus AND apply a drift
//     batch before the first publish.
//
// assembleWatched is the shared assembly+publish+watcher-start core every path
// funnels through; SnapshotBytes is the inverse (serialize the live corpus).

import (
	"context"
	"fmt"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/townsendmerino/aikit/chunk"
	"github.com/townsendmerino/aikit/embed"
	"github.com/townsendmerino/ken/internal/repo"
)

// WrapStatic wraps an already-built *Index (typically from
// LoadSerializedIndex — ADR-024's pre-built-index path) in a
// WatchedIndex that never watches, never re-publishes, and owns no
// fsnotify state. This is the cache.Builder-compatible shape for serving
// a frozen on-disk index: the ken-mcp binary loads <corpus>/.ken/index.bin
// in ~1-2s instead of paying the full walk+chunk+embed build, and the
// frozen index is exactly the demo-appropriate behavior (no watcher).
//
// Close() is a no-op (done is pre-closed; there's no goroutine to stop).
// SetExtraChunks is a guarded no-op (see its doc) because a static index
// has no live corpus to union against.
func WrapStatic(ix *Index, root string, mode Mode, chunkerName string) *WatchedIndex {
	wi := &WatchedIndex{
		root:        root,
		mode:        mode,
		chunkerName: chunkerName,
		done:        make(chan struct{}),
		static:      true,
	}
	wi.ix.Store(ix)
	close(wi.done) // no watcher goroutine; Close()'s <-w.done returns immediately
	return wi
}

// NewWatchedIndex builds the initial snapshot via FromPath, then (if
// watch=true) starts the fsnotify-driven watcher goroutine. If
// watch=false, the returned WatchedIndex serves reads via Load() but
// never publishes a new snapshot — equivalent to v0.2 behavior, no
// watcher goroutine, no fsnotify state.
//
// As of v0.7.1 NewWatchedIndex enables Tier-1 migration-history folding
// by default. Callers that want the v0.7.0 per-file behavior should use
// NewWatchedIndexWithOptions with FSOptions.DisableFoldMigrations=true.
//
// Close() is safe to call regardless of `watch` and is idempotent.
// Uses the package-level WatchDebounce constant; tests override it
// via newWatchedIndexForTest below.
func NewWatchedIndex(root string, mode Mode, chunkerName, modelDir string, watch bool) (*WatchedIndex, error) {
	return newWatchedIndexWithDebounce(context.Background(), root, mode, chunkerName, modelDir, watch, WatchDebounce, FSOptions{})
}

// NewWatchedIndexWithOptions is NewWatchedIndex plus the FSOptions knob
// added in v0.7.1 (currently the migration-folding opt-out). The zero
// value of FSOptions matches NewWatchedIndex exactly.
func NewWatchedIndexWithOptions(root string, mode Mode, chunkerName, modelDir string, watch bool, opts FSOptions) (*WatchedIndex, error) {
	return newWatchedIndexWithDebounce(context.Background(), root, mode, chunkerName, modelDir, watch, WatchDebounce, opts)
}

// NewWatchedIndexWithContext is NewWatchedIndexWithOptions with a caller
// context that scopes the INITIAL walk+chunk+embed build only. Cancelling
// ctx aborts an in-progress build promptly (returning a wrapped ctx.Err()
// and publishing no partial index); the long-lived file watcher, once the
// build succeeds, has its own lifecycle and is stopped via Close(), not this
// ctx. ken-mcp threads its shutdown context here so a SIGINT during a large
// uncached build doesn't hang shutdown for the whole build.
func NewWatchedIndexWithContext(ctx context.Context, root string, mode Mode, chunkerName, modelDir string, watch bool, opts FSOptions) (*WatchedIndex, error) {
	return newWatchedIndexWithDebounce(ctx, root, mode, chunkerName, modelDir, watch, WatchDebounce, opts)
}

// newWatchedIndexWithDebounce is the test-friendly constructor. The
// debounce is captured into wi.debounce BEFORE the watcher goroutine
// starts reading it, eliminating the race we'd have if tests set
// wi.debounce post-construction. ctx scopes the initial build only (see
// NewWatchedIndexWithContext); the watcher goroutine created below uses its
// own context.
func newWatchedIndexWithDebounce(ctx context.Context, root string, mode Mode, chunkerName, modelDir string, watch bool, debounce time.Duration, opts FSOptions) (*WatchedIndex, error) {
	chunks, vecs, model, migDirs, err := walkAndChunk(ctx, root, mode, chunkerName, modelDir, opts)
	if err != nil {
		return nil, err
	}
	return assembleWatched(root, mode, chunkerName, modelDir, model, chunks, vecs, migDirs, watch, debounce, opts, nil)
}

// NewWatchedIndexFromSnapshot seeds a *watching* index from a corpus already
// materialized off disk (cold-start M1 / ADR-039) — the chunks and their
// embedding vectors loaded from a persisted snapshot — instead of walking +
// chunking + enriching + embedding the tree. This is the everyday-cold fast
// path: the caller has verified (via the sidecar manifest) that the tree has
// not drifted, so the snapshot's corpus is current and only the cheap
// BM25/ANN rebuild in buildUnionedIndexLocked runs before the first query.
//
// Unlike WrapStatic (which is frozen and carries no live corpus), the
// returned index owns chunks/vecs and starts the fsnotify watcher when
// watch=true, so post-restart edits are picked up normally. migrationDirs is
// left nil — Tier-1 migration folding re-establishes on the next full
// rebuild; a snapshot-seeded index doesn't recompute it (a minor, documented
// fidelity gap, ADR-039).
func NewWatchedIndexFromSnapshot(root string, mode Mode, chunkerName, modelDir string, model *embed.StaticModel, chunks []chunk.Chunk, vecs [][]float32, watch bool, opts FSOptions) (*WatchedIndex, error) {
	return assembleWatched(root, mode, chunkerName, modelDir, model, chunks, vecs, nil, watch, WatchDebounce, opts, nil)
}

// NewWatchedIndexReconciled seeds a watching index from a snapshot corpus and
// applies a drift batch (changed = added/modified files to re-index, deleted =
// files to drop) BEFORE the initial publish, so the first — and only — BM25/ANN
// build reflects the reconciled state (cold-start M1 Increment 2, single-publish
// path). Equivalent result to NewWatchedIndexFromSnapshot followed by
// ReconcileFiles, but without that pair's throwaway first publish.
//
// Empty changed+deleted degenerates to a plain snapshot seed. migrationDirs is
// nil, same fidelity gap as NewWatchedIndexFromSnapshot.
func NewWatchedIndexReconciled(root string, mode Mode, chunkerName, modelDir string, model *embed.StaticModel, chunks []chunk.Chunk, vecs [][]float32, changed, deleted []string, watch bool, opts FSOptions) (*WatchedIndex, error) {
	var reconcile func(*WatchedIndex)
	if len(changed)+len(deleted) > 0 {
		reconcile = func(w *WatchedIndex) {
			batch := make(map[string]fsnotify.Op, len(changed)+len(deleted))
			for _, f := range changed {
				batch[f] = fsnotify.Write
			}
			for _, f := range deleted {
				batch[f] = fsnotify.Remove
			}
			w.reconcileCorpusLocked(batch, nil) // explicit drift batch, not an overflow resync
		}
	}
	return assembleWatched(root, mode, chunkerName, modelDir, model, chunks, vecs, nil, watch, WatchDebounce, opts, reconcile)
}

// assembleWatched builds a WatchedIndex from an in-hand corpus (chunks +
// vecs + model), publishes the initial snapshot, and — when watch=true —
// starts the fsnotify watcher goroutine. Shared by the walk-based
// constructor and the snapshot-seeded one so both take the identical
// publish + watcher-start path.
func assembleWatched(root string, mode Mode, chunkerName, modelDir string, model *embed.StaticModel, chunks []chunk.Chunk, vecs [][]float32, migDirs map[string]bool, watch bool, debounce time.Duration, opts FSOptions, reconcile func(*WatchedIndex)) (*WatchedIndex, error) {
	wi := &WatchedIndex{
		root:          root,
		mode:          mode,
		chunkerName:   chunkerName,
		modelDir:      modelDir,
		chunks:        chunks,
		vecs:          vecs,
		model:         model,
		matcher:       repo.NewMatcher(repo.Options{Root: root}),
		done:          make(chan struct{}),
		debounce:      debounce,
		fsOpts:        opts,
		migrationDirs: migDirs,
	}
	// Cold-start warm deferral. staged (M4) defers BOTH the enrich parse and the
	// embed, serving BM25 lexical instantly; lazyEnrich (M2) defers only the
	// enrich parse (embedding stays inline). Either way the background warm pass
	// finishes the work. warmDoEnrich records that the label must be (re)added
	// there (it was skipped on the initial build).
	// Staging (M4) only makes sense on a genuinely cold build — no vectors yet.
	// A snapshot-seeded / reconciled boot arrives with vecs already full-length
	// (LoadSerializedCorpus), so staging there would both needlessly re-embed
	// AND misalign vecs/chunks: the pre-publish reconcile below can append
	// chunks under the stagedPending gate that skips their vectors, and
	// compactCorpus then indexes w.vecs[i] past its end → panic at startup
	// (audit R4-1). Derive the decision from the corpus, not just the option, so
	// stagedPending is true only when vecs is empty and the invariant
	// len(vecs)∈{0,len(chunks)} holds through every reconcile mutation.
	staged := opts.StagedEmbedding && model != nil && mode.needsModel() && len(vecs) == 0
	lazyEnrich := opts.LazyEnrichment && !opts.DisableEnrichment
	warm := staged || lazyEnrich
	if warm {
		wi.warmDoEnrich = !opts.DisableEnrichment
	}
	if staged {
		wi.realMode = mode // publish as BM25; the warm pass upgrades to this
		wi.mode = ModeBM25
		wi.stagedPending = true // vecs stay empty until the warm pass fills them (audit N1)
	}

	// Optional pre-publish reconcile (snapshot-seeded drift): mutate the
	// corpus BEFORE the initial build so the first published Index already
	// reflects the changes — one build, not seed-then-reconcile's two. Runs
	// single-threaded here (the watcher hasn't started), same as the
	// buildUnionedIndexLocked call below.
	if reconcile != nil {
		reconcile(wi)
	}
	wi.ix.Store(wi.buildUnionedIndexLocked())

	// Bring the deferred build to full quality. On the non-watching path
	// there's no long-lived process, so warm synchronously before returning
	// (the returned index is fully built, as callers expect).
	if !watch {
		if warm {
			wi.warmCorpusInBackground(context.Background())
		}
		close(wi.done)
		return wi, nil
	}

	w, err := fsnotify.NewWatcher()
	if err != nil {
		close(wi.done)
		return nil, err
	}
	wi.fs = w
	wi.ctx, wi.cancel = context.WithCancel(context.Background())

	wi.addRecursiveWatch(root) // never fails the build (audit §4); gitignore-pruned (R9/§3)

	go wi.loop()

	// Background warm goroutine, tracked so Close() waits for it and cancels it
	// via wi.ctx.
	if warm {
		wi.bgWG.Go(func() {
			wi.warmCorpusInBackground(wi.ctx)
		})
	}
	return wi, nil
}

// SnapshotBytes serializes the current published corpus (chunks + vectors)
// to the KEN1 on-disk format (index_serialize.go) so ken-mcp can persist it
// under <repo>/.ken/index.bin (ADR-039). It reads the published *Index —
// whose chunks are already compacted (no tombstones) — so it needs no lock
// and captures a consistent snapshot even if a flush is mid-flight.
func (w *WatchedIndex) SnapshotBytes() ([]byte, error) {
	ix := w.Load()
	if ix == nil {
		return nil, fmt.Errorf("search: SnapshotBytes: no index published")
	}
	// Serialize the PUBLISHED index's own mode (audit N3): reading w.mode here
	// races the warm pass's `w.mode = targetMode` write under corpusMu, and
	// ix.Mode() is also the correct value — it matches the very chunks/vecs
	// being serialized (a staged pre-warm snapshot is BM25 + vector-less
	// together). w.chunkerName is set once at construction and never mutated.
	return serializeIndex(ix.Chunks(), ix.Vecs(), ix.Mode(), w.chunkerName)
}
