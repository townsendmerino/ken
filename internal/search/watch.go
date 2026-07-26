package search

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/townsendmerino/aikit/chunk"
	"github.com/townsendmerino/aikit/embed"
	"github.com/townsendmerino/ken/internal/repo"
	"github.com/townsendmerino/ken/internal/sql"
)

// WatchDebounce is the fixed delay between the first dirty event and
// the snapshot rebuild. Hard-coded by design (ADR-012) — above
// editor save-on-keystroke timescales (VS Code, vim temp-file rename)
// but small enough that an interactive agent doesn't notice it.
const WatchDebounce = 2 * time.Second

// WatchedIndex wraps an Index with a file watcher that publishes new
// snapshots when files in `root` change. The current snapshot is read
// via Load() and is goroutine-safe; the underlying *Index value is
// never mutated after construction, so callers can hold the returned
// pointer across operations.
//
// Concurrency model: writers (the debouncer goroutine) build a fresh
// *Index off to the side from the corpus state and publish it via
// atomic.Pointer.Store. Readers do one atomic.Pointer.Load at query
// entry and use that pointer for the entire call. Readers never wait
// on the writer; writers never invalidate an in-flight reader. See
// docs/internal/DECISIONS.md ADR-012 for the rationale (and why this isn't
// RWMutex).
//
// Methods Search / FindRelated / ResolveChunk wrap Load() + delegate so
// callers don't have to remember the snapshot-pointer dance. Each
// method does exactly one Load() at entry; the returned snapshot is
// used throughout the call.
type WatchedIndex struct {
	root        string
	mode        Mode
	chunkerName string
	modelDir    string

	// realMode is the configured mode when serving is STAGED (M4): the initial
	// index is published as `mode` = ModeBM25 (lexical-only, instant), and the
	// background warm pass upgrades `mode` to realMode (e.g. ModeHybrid) once
	// vectors are ready. Zero (ModeBM25) when not staging.
	realMode Mode

	// warmDoEnrich records whether the background warm pass must prepend the
	// Arm B label (i.e. enrichment was deferred off the initial build — M2 lazy
	// enrich, or M4 staged which defers both enrich and embed).
	warmDoEnrich bool

	// Current snapshot. Read via wi.ix.Load(); never nil after
	// NewWatchedIndex returns successfully.
	ix atomic.Pointer[Index]

	// Mutable corpus state owned by the debouncer goroutine. chunks
	// and vecs are parallel. During a flush, deletes mark entries as
	// Tombstoned in-place; compactCorpus then drops tombstoned entries
	// (and their parallel vecs slots) into fresh slices before
	// BuildIndex publishes, so published snapshots never carry
	// tombstones. A previously-published *Index references the prior
	// backing slices; those stay intact (and readable, with tombstones
	// filtered on every read path) until GC reclaims them.
	//
	// Held under corpusMu. As of v0.7.0 there are TWO writers: the
	// debouncer goroutine (touching chunks/vecs from fsnotify events)
	// and any caller of SetExtraChunks (typically the db.Refresher
	// from cmd/ken-mcp updating the Tier-2 DB chunks). The published
	// Index is always built from chunks ∪ extraChunks so a flush from
	// either trigger publishes a consistent unioned snapshot.
	corpusMu sync.Mutex
	chunks   []chunk.Chunk
	vecs     [][]float32
	model    *embed.StaticModel

	// tokens memoizes BM25 tokenization keyed by chunk text so a flush
	// only re-tokenizes re-chunked files, not the whole corpus (audit
	// §5). Written under corpusMu by buildUnionedIndexLocked; nil-safe
	// (tokenizeDocs treats a nil cache as "tokenize everything fresh").
	tokens *tokenCache

	// v0.7.0 (ADR-017): "extra" chunks injected by the orchestrator
	// from non-FS sources — currently database introspection via
	// internal/db.Refresher. These survive fsnotify-driven flushes
	// (never tombstoned by file removes) and are themselves replaced
	// wholesale by SetExtraChunks. extraVecs is the parallel
	// embedding slice when running in semantic/hybrid mode.
	extraChunks []chunk.Chunk
	extraVecs   [][]float32

	// Filter shared with the debouncer: which fsnotify events
	// correspond to files ken would index.
	matcher *repo.Matcher

	// Watcher goroutine lifecycle.
	fs      *fsnotify.Watcher
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}  // closed by the goroutine just before exit
	closeMu sync.Mutex     // serializes Close() — idempotent
	bgWG    sync.WaitGroup // tracks the M2 lazy-enrichment background pass

	// Test hook: receives one value per published snapshot. nil
	// disables. Use SetOnSwap to set before any events arrive.
	onSwapMu sync.Mutex
	onSwap   chan<- struct{}

	// Caller-facing hook: invoked once per published snapshot with a
	// one-line human-readable message. Used by `ken index --watch` to
	// give interactive users feedback that the watcher is alive, and
	// by ken-mcp at info-level to log reindex activity. nil disables.
	// Set via SetOnFlush.
	onFlushMu sync.Mutex
	onFlush   func(msg string)

	// Debounce delay; overridable for tests. Defaults to WatchDebounce.
	debounce time.Duration

	// v0.7.1: FSOptions snapshot from construction. Carries
	// DisableFoldMigrations + LogWriter so the watch path's re-fold can
	// match the build-time semantics exactly.
	fsOpts FSOptions

	// v0.7.1: set of directories the build-time pass classified as
	// migration directories. Read under corpusMu; written only on
	// construction and on re-fold (when a new migration dir is detected
	// or an existing one no longer qualifies). On a flush touching any
	// file in one of these dirs, the WHOLE dir is re-folded.
	migrationDirs map[string]bool

	// static marks a WatchedIndex created by WrapStatic — a frozen
	// pre-built index (ADR-024) with no watcher and no live corpus
	// (chunks/vecs/model are empty because the index was loaded from
	// serialized bytes, not built from a walk). SetExtraChunks guards
	// on this so the DB-Tier-2 union-rebuild path can't replace the
	// loaded snapshot with one built from an empty corpus.
	static bool

	// M4/M5: the neural reranker re-applied to every published snapshot.
	// nil = no reranker; SearchMode then downgrades ModeHybridRerank to
	// ModeHybrid transparently. Held under corpusMu so SetReranker
	// serializes against the flush path that publishes new snapshots.
	// The reranker's own LRU cache is content-hashed and survives
	// snapshot swaps, so re-applying the same instance carries the
	// warm cache forward across rebuilds.
	reranker  Reranker
	rerankCfg rerankerConfig
}

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
			w.reconcileCorpusLocked(batch)
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
	staged := opts.StagedEmbedding && model != nil && mode.needsModel()
	lazyEnrich := opts.LazyEnrichment && !opts.DisableEnrichment
	warm := staged || lazyEnrich
	if warm {
		wi.warmDoEnrich = !opts.DisableEnrichment
	}
	if staged {
		wi.realMode = mode // publish as BM25; the warm pass upgrades to this
		wi.mode = ModeBM25
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

	if err := addRecursive(w, root, wi.fsOpts.LogWriter); err != nil {
		_ = w.Close()
		wi.cancel()
		close(wi.done)
		return nil, err
	}

	go wi.loop()

	// Background warm goroutine, tracked so Close() waits for it and cancels it
	// via wi.ctx.
	if warm {
		wi.bgWG.Add(1)
		go func() {
			defer wi.bgWG.Done()
			wi.warmCorpusInBackground(wi.ctx)
		}()
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
	return serializeIndex(ix.Chunks(), ix.Vecs(), w.mode, w.chunkerName)
}

// warmCorpusInBackground is the unified cold-start warm pass for M2 (lazy
// enrichment) and M4 (staged embedding). After a deferred build published a
// partial index — un-labelled (M2/M4) and/or vector-less BM25 (M4) — for a fast
// first query, this brings it to full quality: prepend the Arm B label per file
// (when warmDoEnrich), re-embed under the target mode, and republish atomically,
// upgrading the served mode (bm25 → realMode) if staged.
//
// It works on FRESH slices — the published partial index shares w.chunks/w.vecs
// backing arrays and readers use them lock-free, so it must never mutate those
// in place. Holds corpusMu for the pass (reads unaffected; watch flushes queue,
// and at cold start there are typically no edits yet). ctx cancellation (Close)
// aborts at the next file boundary. The parse honors KEN_ENRICH_FILE_BUDGET_MS
// via ExtractFile; embedding honors the M3 cache.
func (w *WatchedIndex) warmCorpusInBackground(ctx context.Context) {
	start := time.Now()
	w.corpusMu.Lock()
	defer w.corpusMu.Unlock()

	// Target mode: realMode when staged (upgrades bm25 → hybrid), else the
	// current mode (M2 lazy-only keeps its mode).
	targetMode := w.mode
	if w.realMode != ModeBM25 {
		targetMode = w.realMode
	}
	needEmbed := w.model != nil && targetMode.needsModel()

	newChunks := make([]chunk.Chunk, len(w.chunks))
	copy(newChunks, w.chunks)
	newVecs := make([][]float32, len(newChunks))
	if !needEmbed {
		// Preserve any existing vectors verbatim (nothing to (re)embed).
		copy(newVecs, w.vecs)
	}

	byFile := map[string][]int{}
	for i := range newChunks {
		if newChunks[i].Tombstoned {
			continue
		}
		byFile[newChunks[i].File] = append(byFile[newChunks[i].File], i)
	}

	for rel, idxs := range byFile {
		if ctx.Err() != nil {
			return // Close() cancelled — leave the partial index published
		}
		var label string
		if w.warmDoEnrich {
			if data, err := os.ReadFile(filepath.Join(w.root, rel)); err == nil {
				label = enrichLabelFor(rel, data)
			}
		}
		for _, i := range idxs {
			if label != "" {
				// Idempotent (audit R2): strip any label already present before
				// prepending. A snapshot-seeded corpus (M1) can arrive already
				// enriched, and this warm pass runs on every boot — without the
				// strip the label compounds one line per restart, re-breaking
				// the byte-fidelity invariant §10 restored. reassigns the copy,
				// not w.chunks[i].
				newChunks[i].Text = label + stripEnrichLabel(newChunks[i].Text)
			}
			if needEmbed {
				newVecs[i] = encodeCached(w.fsOpts.EmbedCache, w.model, newChunks[i].Text)
			}
		}
	}

	w.chunks = newChunks
	w.vecs = newVecs
	w.mode = targetMode // upgrade bm25 → hybrid when staged; unchanged otherwise
	w.ix.Store(w.buildUnionedIndexLocked())
	w.notifySwap()
	// Reuse the flush notification so ken-mcp's OnFlush re-persists the now-full
	// snapshot (M1) — a boot after this is a clean full-quality load.
	w.notifyFlush(len(w.chunks)+len(w.extraChunks), len(byFile), 0, time.Since(start))
}

// Warming reports whether the semantic arm is still being built in the
// background (M4 staged embedding): the served index is BM25 but will upgrade
// to the configured mode. False when not staging, or once the upgrade lands.
// realMode is set once at construction (before any goroutine), so it's safe to
// read; the served mode is an atomic Load.
func (w *WatchedIndex) Warming() bool {
	if w.realMode == ModeBM25 {
		return false
	}
	ix := w.Load()
	return ix == nil || ix.Mode() != w.realMode
}

// EmbedModel returns the Model2Vec model this index queries with (nil for
// BM25 mode). ken-mcp uses it to compute the snapshot config-key's model
// fingerprint without reloading the model from disk. The returned model is
// read-only; callers must not mutate it.
func (w *WatchedIndex) EmbedModel() *embed.StaticModel { return w.model }

// ReconcileFiles applies a boot-time drift batch to a snapshot-seeded index
// (cold-start M1 Increment 2): `changed` files (added or modified since the
// snapshot) are re-chunked / enriched / embedded and their old chunks
// replaced; `deleted` files have their chunks dropped. Only these files are
// touched — unchanged files keep the chunks + vectors loaded from the
// snapshot, so a small edit doesn't re-index the whole tree.
//
// It reuses the fsnotify flush path verbatim (tombstone → append → compact →
// rebuild → publish) by synthesizing the equivalent event batch, so a
// boot reconcile and a live edit take the identical, already-tested code.
// Safe to call after the watcher has started (flush holds corpusMu). No-op
// when there's nothing to reconcile.
func (w *WatchedIndex) ReconcileFiles(changed, deleted []string) {
	if len(changed)+len(deleted) == 0 {
		return
	}
	batch := make(map[string]fsnotify.Op, len(changed)+len(deleted))
	for _, f := range changed {
		batch[f] = fsnotify.Write // tombstone existing (if any) + re-append
	}
	for _, f := range deleted {
		batch[f] = fsnotify.Remove // tombstone only
	}
	w.flush(batch)
}

// Load returns the current Index snapshot. Goroutine-safe; one atomic
// load. Never returns nil after NewWatchedIndex succeeds.
func (w *WatchedIndex) Load() *Index { return w.ix.Load() }

// Search loads the current snapshot once and delegates. The snapshot
// is consistent for the duration of the call even if the watcher
// publishes a new one mid-call.
func (w *WatchedIndex) Search(query string, k int) []Result {
	return w.Load().Search(query, k)
}

// FindRelated loads the current snapshot and delegates. See
// (*Index).FindRelated for semantics.
func (w *WatchedIndex) FindRelated(filePath string, line, k int) ([]Result, error) {
	return w.Load().FindRelated(filePath, line, k)
}

// ResolveChunk loads the current snapshot and delegates.
func (w *WatchedIndex) ResolveChunk(filePath string, line int) *chunk.Chunk {
	return w.Load().ResolveChunk(filePath, line)
}

// Len returns the current snapshot's chunk count. Published snapshots
// never carry tombstones (compaction runs at flush time, before the
// snapshot is exposed), so this is the live-chunk count.
func (w *WatchedIndex) Len() int { return w.Load().Len() }

// SetOnSwap installs a channel that receives one nonblocking send
// each time the watcher publishes a new snapshot. Used by tests to
// synchronize on rebuilds. Calling with nil disables. Safe to call
// before NewWatchedIndex returns or between rebuilds.
func (w *WatchedIndex) SetOnSwap(ch chan<- struct{}) {
	w.onSwapMu.Lock()
	defer w.onSwapMu.Unlock()
	w.onSwap = ch
}

// SetOnFlush installs a callback invoked once per snapshot publish
// with a one-line summary like "reindexed: 1234 chunks total,
// 3 files changed in 47 ms". `ken index --watch` uses this to give
// interactive users feedback that the watcher is alive; ken-mcp uses
// it at info-level so reindex activity shows up in --log-level=info
// runs. Pass nil to disable. Safe to call at any time.
func (w *WatchedIndex) SetOnFlush(f func(msg string)) {
	w.onFlushMu.Lock()
	defer w.onFlushMu.Unlock()
	w.onFlush = f
}

// Close stops the watcher, cancels in-flight work, and waits for the
// debouncer goroutine to exit. Idempotent; returns nil for symmetry
// with io.Closer.
func (w *WatchedIndex) Close() error {
	w.closeMu.Lock()
	defer w.closeMu.Unlock()
	if w.cancel != nil {
		w.cancel()
	}
	if w.fs != nil {
		_ = w.fs.Close()
	}
	// Wait for the goroutine to drain. If watch=false there's no
	// goroutine but `done` was closed eagerly in NewWatchedIndex.
	<-w.done
	// Wait for the M2 background-enrichment pass (if any) — cancel() above
	// signals it via wi.ctx; it aborts at the next file boundary.
	w.bgWG.Wait()
	return nil
}

// loop is the watcher goroutine. Receives fsnotify events, filters
// them, accumulates a dirty set, and flushes after the debounce window.
// Owns wi.chunks and wi.vecs; the corpusMu lock is defensive (only this
// goroutine writes).
func (w *WatchedIndex) loop() {
	defer close(w.done)

	dirty := make(map[string]fsnotify.Op)
	var timer *time.Timer

	// Async single-flight flush (audit search §12): the flush does file
	// I/O + embedding + a full BM25/ANN rebuild, which used to run inline
	// on this goroutine — for its whole duration (seconds on a big
	// checkout) w.fs.Events wasn't drained, filling the kernel queue toward
	// overflow. Now the flush runs on a tracked goroutine so the loop keeps
	// draining events; `flushing` is the single-flight guard and flushDone
	// signals completion. Correctness doesn't rely on the guard — flush()
	// takes corpusMu — but it stops redundant flushes piling up under churn.
	flushing := false
	flushDone := make(chan struct{}, 1)

	// Debounce starvation ceiling (audit search §13): every accepted event
	// pushes the debounce deadline out by w.debounce, so a tool writing an
	// indexed file more often than that (webpack/tsc/cargo --watch, a
	// test-runner loop) would reset the timer forever and NEVER flush — the
	// index frozen for the whole session. Cap the total wait since the first
	// dirty event at maxDebounce; when we hit it, arm for 0 so the next tick
	// flushes. firstDirty resets on flush.
	maxDebounce := 5 * w.debounce
	var firstDirty time.Time

	resetTimer := func() {
		if firstDirty.IsZero() {
			firstDirty = time.Now()
		}
		delay := w.debounce
		if remaining := maxDebounce - time.Since(firstDirty); remaining < delay {
			delay = remaining
			if delay < 0 {
				delay = 0
			}
		}
		if timer == nil {
			timer = time.NewTimer(delay)
			return
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(delay)
	}

	// timerC returns the timer's channel iff a timer is armed, else
	// nil — selecting on a nil channel blocks forever, which is what
	// we want when there's nothing pending.
	timerC := func() <-chan time.Time {
		if timer == nil {
			return nil
		}
		return timer.C
	}

	for {
		select {
		case <-w.ctx.Done():
			return
		case ev, ok := <-w.fs.Events:
			if !ok {
				return
			}
			rel := w.relPath(ev.Name)
			if rel == "" {
				continue
			}
			// Skip uninteresting ops up front to avoid the matcher
			// stat cost for CHMOD storms.
			if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) == 0 {
				continue
			}
			// REMOVE/RENAME: the file is already gone; ShouldIndex
			// would return false (stat fails). Accept those without
			// matcher check so we can still tombstone.
			if ev.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
				if knownIndexedFile(rel) || w.isTrackedRel(rel) {
					dirty[rel] = mergeOp(dirty[rel], ev.Op)
					resetTimer()
				}
				continue
			}
			// A newly-created or moved-in DIRECTORY (audit §2): fsnotify
			// delivers one Create for the dir and NO per-file events for
			// contents that already existed (a `mv` or `mkdir && write`
			// race), and ShouldIndex is false for directories — so the
			// file-oriented path below would skip it and the new package
			// would be invisible until restart. Watch the whole subtree and
			// enqueue its indexable files ourselves. Must run BEFORE the
			// matcher gate (which rejects the dir).
			if ev.Op&fsnotify.Create != 0 {
				if fi, err := os.Stat(ev.Name); err == nil && fi.IsDir() {
					_ = addRecursive(w.fs, ev.Name, w.fsOpts.LogWriter)
					w.enqueueDirFiles(ev.Name, dirty)
					resetTimer()
					continue
				}
			}
			// WRITE/CREATE: filter through matcher rules so we
			// don't reindex .git/HEAD, oversized binaries, etc.
			if !w.matcher.ShouldIndex(rel) {
				continue
			}
			dirty[rel] = mergeOp(dirty[rel], ev.Op)
			resetTimer()
		case err, ok := <-w.fs.Errors:
			if !ok {
				return
			}
			// Overflow is NOT transient (audit search §3): the kernel
			// permanently drops the queued events (IN_Q_OVERFLOW on Linux,
			// the macOS FSEvents equivalent), so any files touched during
			// the overflow keep serving pre-change chunks forever with no
			// signal. Recover by re-walking the tree and enqueueing every
			// indexable file (mods + adds) plus tombstones for vanished
			// files, then flushing on the next tick. Costly (a full
			// re-chunk/re-embed) but rare and correct; §5's token cache and
			// the optional embed cache blunt it.
			if errors.Is(err, fsnotify.ErrEventOverflow) {
				w.logf("fsnotify event-queue overflow — dropped events; triggering a full resync")
				w.enqueueFullResync(dirty)
				resetTimer()
				continue
			}
			// Non-overflow errors (EBADF on close, transient inotify hiccups)
			// are logged but not acted on — the watcher stays armed.
			w.logf("fsnotify error: %v", err)
		case <-timerC():
			if flushing {
				// A flush is still running — keep accumulating into dirty and
				// just disarm. The `case <-flushDone` arm re-arms the timer if
				// dirty is non-empty when the flush finishes, so no batch is
				// lost. Re-arming HERE instead (audit R1) busy-spins: once the
				// §13 ceiling drives delay to 0, timer.Reset(0) fires
				// immediately, re-hits this branch, re-arms at 0, and pegs a
				// core for the rest of the flush. Do NOT resetTimer() here.
				timer = nil
				continue
			}
			if len(dirty) == 0 {
				timer = nil
				firstDirty = time.Time{}
				continue
			}
			batch := dirty
			dirty = make(map[string]fsnotify.Op)
			timer = nil
			firstDirty = time.Time{} // start a fresh debounce window for the next batch
			flushing = true
			// Tracked by bgWG so Close() (which does <-w.done then
			// bgWG.Wait()) awaits an in-flight flush before teardown —
			// no flush goroutine writes w.ix after the cache reaps this
			// index / rm -rf's a temp clone.
			w.bgWG.Add(1)
			go func() {
				defer w.bgWG.Done()
				w.flush(batch)
				select {
				case flushDone <- struct{}{}:
				default: // loop already gone (shutdown) — don't block
				}
			}()
		case <-flushDone:
			flushing = false
			if len(dirty) > 0 {
				// Events landed while the flush ran — schedule the next one.
				resetTimer()
			}
		}
	}
}

// flush rebuilds the snapshot from the current corpus state plus the
// batched dirty events. Called from the debouncer goroutine only.
func (w *WatchedIndex) flush(batch map[string]fsnotify.Op) {
	start := time.Now()
	w.corpusMu.Lock()
	defer w.corpusMu.Unlock()

	compacted := w.reconcileCorpusLocked(batch)
	newIx := w.buildUnionedIndexLocked()
	w.ix.Store(newIx)
	w.notifySwap()
	w.notifyFlush(len(w.chunks)+len(w.extraChunks), len(batch), compacted, time.Since(start))
}

// reconcileCorpusLocked applies a batch of file events to the mutable corpus
// (tombstone/append per file + migration refold + compact) and returns the
// number of tombstones compacted away. It does NOT rebuild or publish the
// Index — the caller runs buildUnionedIndexLocked + publish. Shared by flush
// (which publishes after) and the snapshot-seeded reconcile constructor (which
// runs it BEFORE the single initial publish, avoiding a seed-then-reconcile
// double build). Caller holds corpusMu (or is single-threaded construction).
func (w *WatchedIndex) reconcileCorpusLocked(batch map[string]fsnotify.Op) int {
	// Copy-on-write (audit search §1): BuildIndex stores the chunks slice
	// header verbatim, so the currently-published *Index aliases w.chunks'
	// backing array — and readers touch chunk[i].Tombstoned lock-free
	// (tombstoneCount/ResolveChunk). tombstoneFile below writes that field in
	// place, which would be an unsynchronized read/write on the live snapshot
	// (data race, and the file's chunks vanish from results mid-flush). Clone
	// first so every mutation here lands on a fresh array; the old array the
	// published snapshot serves from is never touched (ARCHITECTURE.md
	// invariant #2). Cheap next to the whole-corpus BM25 rebuild this flush
	// already does. (At construction — NewWatchedIndexReconciled — nothing is
	// published yet, so the clone is a harmless no-op-cost.)
	w.chunks = slices.Clone(w.chunks)

	// Migration dirs touched by this batch; we'll re-fold them in one
	// pass after per-file tombstone/append, so an ALTER added in one
	// migration file shows up in the folded chunk for the whole dir.
	touchedMigDirs := map[string]bool{}

	// Iterate the batch in sorted path order (audit §17): appending in Go's
	// randomized map-iteration order gave two processes on the same repo
	// different chunk orders → different BM25 doc IDs → different tie-breaks
	// in rerankTopK, and non-reproducible SnapshotBytes. The build path
	// sorts for exactly this reason; the flush path must too.
	rels := make([]string, 0, len(batch))
	for rel := range batch {
		rels = append(rels, rel)
	}
	sort.Strings(rels)

	for _, rel := range rels {
		op := batch[rel]
		if w.migrationDirs[path.Dir(rel)] {
			touchedMigDirs[path.Dir(rel)] = true
		}
		if op&(fsnotify.Remove|fsnotify.Rename) != 0 {
			w.tombstoneFile(rel)
			continue
		}
		// WRITE or CREATE: tombstone existing chunks for this file,
		// then re-chunk + re-embed and append.
		w.tombstoneFile(rel)
		w.appendFile(rel)
	}

	// Re-fold every touched migration dir (sorted, same determinism reason).
	// Tombstone the dir's folded chunks first (those live in the chunks
	// slice with File pointing at one of the dir's source files); then
	// re-walk the dir on disk via sql.FoldMigrations and append the fresh
	// folded chunks.
	migDirs := make([]string, 0, len(touchedMigDirs))
	for d := range touchedMigDirs {
		migDirs = append(migDirs, d)
	}
	sort.Strings(migDirs)
	for _, d := range migDirs {
		w.tombstoneFoldedChunksForDir(d)
		w.refoldMigrationDir(d)
	}

	return w.compactCorpus()
}

// tombstoneFoldedChunksForDir marks every chunk whose File lives inside
// migration directory `dir` AND whose Text identifies it as a folded
// chunk ("-- folded from migrations" marker emitted by
// renderFoldedTableChunk). Caller holds corpusMu.
//
// We can't tombstone every chunk in dir indiscriminately: line-chunked
// raw .sql content for files in the dir is owned by per-file appendFile
// and has already been re-emitted by the caller's per-file tombstone +
// re-append. Only the folded chunks need this dir-wide refresh.
func (w *WatchedIndex) tombstoneFoldedChunksForDir(dir string) {
	const marker = "-- folded from migrations"
	for i := range w.chunks {
		if w.chunks[i].Tombstoned {
			continue
		}
		if path.Dir(w.chunks[i].File) != dir {
			continue
		}
		if strings.Contains(w.chunks[i].Text, marker) {
			w.chunks[i].Tombstoned = true
		}
	}
}

// refoldMigrationDir re-runs sql.FoldMigrations against the migration
// directory `dir` on disk and appends the resulting chunks. Caller
// holds corpusMu. Read errors / fold errors are logged via the FSOptions
// LogWriter (nil discards) and the dir keeps the previously-tombstoned
// folded chunks invalidated — net: the snapshot reflects "best effort"
// with no stale folded chunk surviving the flush.
func (w *WatchedIndex) refoldMigrationDir(dir string) {
	folded, err := sql.FoldMigrations(os.DirFS(w.root), dir, w.fsOpts.LogWriter)
	if err != nil {
		if w.fsOpts.LogWriter != nil {
			fmt.Fprintf(w.fsOpts.LogWriter, "search: WatchedIndex.refoldMigrationDir(%q): %v\n", dir, err)
		}
		return
	}
	for _, c := range folded {
		w.chunks = append(w.chunks, c)
		if w.model != nil {
			w.vecs = append(w.vecs, encodeCached(w.fsOpts.EmbedCache, w.model, c.Text))
		}
	}
}

// buildUnionedIndexLocked constructs the published Index from the union
// of FS chunks (w.chunks/w.vecs) and the orchestrator-injected extra
// chunks (w.extraChunks/w.extraVecs). Caller MUST hold corpusMu.
//
// The concatenation order is "FS first, extras second" so a chunk's
// index inside the published Index is stable for FS chunks across
// snapshot republishes that only changed the extras — important for
// any reader holding chunk indices across calls (none currently, but
// the invariant is cheap to preserve).
//
// M5: re-applies the watched-level reranker (if any) to every newly
// built snapshot. The reranker instance is shared across rebuilds so
// its content-hash LRU cache carries forward.
func (w *WatchedIndex) buildUnionedIndexLocked() *Index {
	if w.tokens == nil {
		w.tokens = newTokenCache()
	}
	var ix *Index
	if len(w.extraChunks) == 0 {
		docs := tokenizeDocs(w.chunks, w.tokens)
		ix = buildIndexFromDocs(w.chunks, docs, w.vecs, w.mode, w.model)
	} else {
		merged := make([]chunk.Chunk, 0, len(w.chunks)+len(w.extraChunks))
		merged = append(merged, w.chunks...)
		merged = append(merged, w.extraChunks...)
		var mergedVecs [][]float32
		if w.vecs != nil || w.extraVecs != nil {
			mergedVecs = make([][]float32, 0, len(w.vecs)+len(w.extraVecs))
			mergedVecs = append(mergedVecs, w.vecs...)
			mergedVecs = append(mergedVecs, w.extraVecs...)
		}
		// FS chunks reuse the cache (the common, per-save path); the
		// extras (Tier-2 DB chunks) are tokenized fresh — they change on
		// their own refresh cadence and caching them would only add map
		// churn. Concatenated FS-first so indices stay stable (matching
		// the merged chunk order above).
		docs := make([][]string, 0, len(merged))
		docs = append(docs, tokenizeDocs(w.chunks, w.tokens)...)
		docs = append(docs, tokenizeDocs(w.extraChunks, nil)...)
		ix = buildIndexFromDocs(merged, docs, mergedVecs, w.mode, w.model)
	}
	if w.reranker != nil {
		ix.reranker = w.reranker
		ix.rerankCfg = w.rerankCfg
	}
	return ix
}

// SetReranker attaches a neural reranker to this WatchedIndex. The
// reranker is re-applied to every newly published snapshot (live
// flushes, SetExtraChunks rebuilds, etc.) so its content-hash LRU
// cache survives snapshot swaps — the per-snapshot cost is zero, only
// the field is re-pointed.
//
// Pass nil to detach. ken-mcp calls this once at startup with the
// boot-time NeuralReranker; production code does NOT call this from
// hot paths because it takes the corpus lock briefly.
//
// Goroutine-safe; serializes against the flush path via corpusMu.
func (w *WatchedIndex) SetReranker(r Reranker, opts ...RerankerOption) {
	w.corpusMu.Lock()
	defer w.corpusMu.Unlock()
	w.reranker = r
	if r == nil {
		w.rerankCfg = rerankerConfig{}
	} else {
		w.rerankCfg = defaultRerankerConfig
		for _, o := range opts {
			o(&w.rerankCfg)
		}
	}
	// Apply to the currently-published snapshot immediately (without
	// waiting for the next flush) — but via the atomic swap, NOT by
	// mutating the live *Index in place (M4, code review §3). Lock-free
	// Search readers dereference ix.reranker / ix.rerankCfg with no lock,
	// so writing them on the published value is a data race: a torn 2-word
	// interface read can pair a new itab with a stale data word and crash
	// on method dispatch. Index has no lock fields and all its referenced
	// data (bm/flat/model/chunks/vecs) is immutable after construction, so
	// a shallow copy safely shares that data; only the fresh pointer carries
	// the new reranker, and Store makes readers see old-or-new, never a
	// half-written field. (Cheaper than buildUnionedIndexLocked's full
	// BuildIndex rebuild for a field-only change.)
	if cur := w.ix.Load(); cur != nil {
		next := *cur
		next.reranker = r
		next.rerankCfg = w.rerankCfg
		w.ix.Store(&next)
	}
}

// SetExtraChunks replaces the orchestrator-injected extra chunks and
// publishes a new snapshot. Called by cmd/ken-mcp's db.Refresher swap
// callback whenever Tier-2 DB introspection produces a fresh chunk set.
//
// Calling with chunks==nil (or empty) is the canonical "DB unreachable,
// clear the DB chunks" path — the swap callback always fires so the
// published snapshot reflects the latest known state from each source.
//
// Goroutine-safe: serialized via corpusMu against the debouncer's flush
// path. Embedding (when model != nil) happens inside the lock; for
// large DB snapshots this can block fsnotify flushes briefly. That's
// acceptable — DB refreshes are infrequent (startup, periodic ≥1m,
// SIGHUP) and embedding cost is bounded by chunk count.
func (w *WatchedIndex) SetExtraChunks(chunks []chunk.Chunk) {
	// A static (pre-built) index has no live corpus in w.chunks/w.vecs —
	// the snapshot was loaded from serialized bytes. buildUnionedIndexLocked
	// would rebuild from an empty corpus ∪ extras, silently dropping the
	// entire loaded index. Guard against that: pre-built + DB-Tier-2 is an
	// unsupported combination (bake DB chunks in at `ken build-index` time
	// instead), so the right behavior is to keep serving the loaded index
	// and ignore the extras rather than corrupt the snapshot.
	if w.static {
		return
	}

	w.corpusMu.Lock()
	defer w.corpusMu.Unlock()

	w.extraChunks = chunks
	if w.model != nil && len(chunks) > 0 {
		vecs := make([][]float32, len(chunks))
		for i, c := range chunks {
			vecs[i] = encodeCached(w.fsOpts.EmbedCache, w.model, c.Text)
		}
		w.extraVecs = vecs
	} else {
		w.extraVecs = nil
	}
	w.ix.Store(w.buildUnionedIndexLocked())
	w.notifySwap()
}

// compactCorpus drops tombstoned chunks (and their parallel vecs slots)
// from the writer-side corpus state, allocating fresh backing slices so
// any previously-published *Index that references the old slices stays
// unmodified. Returns the number of tombstones dropped — zero when
// there's nothing to compact. Caller holds corpusMu.
//
// Unconditional: runs on every flush. The iteration is already paid by
// BuildIndex below; an unconditional rule has no failure mode where
// compaction silently never triggers. See ADR-012.
func (w *WatchedIndex) compactCorpus() int {
	dropped := 0
	for i := range w.chunks {
		if w.chunks[i].Tombstoned {
			dropped++
		}
	}
	if dropped == 0 {
		return 0
	}
	newChunks := make([]chunk.Chunk, 0, len(w.chunks)-dropped)
	var newVecs [][]float32
	if w.vecs != nil {
		newVecs = make([][]float32, 0, len(w.vecs)-dropped)
	}
	for i, c := range w.chunks {
		if c.Tombstoned {
			continue
		}
		newChunks = append(newChunks, c)
		if newVecs != nil {
			newVecs = append(newVecs, w.vecs[i])
		}
	}
	w.chunks = newChunks
	w.vecs = newVecs
	return dropped
}

// notifyFlush calls the OnFlush callback (if set) with a one-line
// summary of the just-published snapshot. Format is stable enough for
// users to grep but not part of any public contract.
func (w *WatchedIndex) notifyFlush(totalChunks, filesChanged, compacted int, dur time.Duration) {
	w.onFlushMu.Lock()
	f := w.onFlush
	w.onFlushMu.Unlock()
	if f == nil {
		return
	}
	f(formatFlush(totalChunks, filesChanged, compacted, dur))
}

// formatFlush builds the OnFlush message. Pulled out for testability.
// Duration is always emitted as integer milliseconds — a sub-ms rebuild
// shows as "0 ms" rather than "0s" (time.Duration.String collapses
// fractions, which makes the message inconsistent across small repos).
// The "(compacted N tombstones)" suffix is appended only when N>0 so
// pure-write flushes keep their existing v0.3 format.
func formatFlush(totalChunks, filesChanged, compacted int, dur time.Duration) string {
	msg := "reindexed: " +
		intStr(totalChunks) + " chunks total, " +
		intStr(filesChanged) + " files changed in " +
		intStr(int(dur.Milliseconds())) + " ms"
	if compacted > 0 {
		msg += " (compacted " + intStr(compacted) + " tombstones)"
	}
	return msg
}

// intStr is a tiny strconv helper to keep the formatFlush call site
// readable. Avoids importing strconv just for one call.
func intStr(n int) string {
	if n == 0 {
		return "0"
	}
	var digits [20]byte
	i := len(digits)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		digits[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		digits[i] = '-'
	}
	return string(digits[i:])
}

// readCappedFile reads abs but refuses anything larger than the matcher's
// size ceiling (audit search §15): even after ShouldIndex's Lstat passes,
// the file could grow before this read (TOCTOU), so bound it with a
// LimitReader at cap+1 and reject if the extra byte materializes. Returns
// the bytes, or an error the caller treats as "skip this file".
func (w *WatchedIndex) readCappedFile(abs string) ([]byte, error) {
	cap := int64(repo.DefaultMaxFileBytes)
	if w.matcher != nil {
		cap = w.matcher.MaxFileBytes()
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, cap+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > cap {
		return nil, fmt.Errorf("search: %s exceeds max file bytes (%d)", abs, cap)
	}
	return data, nil
}

// tombstoneFile marks every existing chunk whose File == rel as
// Tombstoned. Caller holds corpusMu.
func (w *WatchedIndex) tombstoneFile(rel string) {
	// Match the exact file OR — for a directory Remove/Rename, which fires no
	// per-file events (audit §14) — every chunk under rel+"/". The "/" boundary
	// keeps `internal/auth` from matching `internal/authz/…`. A regular file
	// remove (rel="a.go") has an empty prefix match, so this is a no-op beyond
	// the exact case.
	prefix := rel + "/"
	for i := range w.chunks {
		if w.chunks[i].Tombstoned {
			continue
		}
		if w.chunks[i].File == rel || strings.HasPrefix(w.chunks[i].File, prefix) {
			w.chunks[i].Tombstoned = true
		}
	}
}

// appendFile re-reads, re-chunks, and re-embeds `rel`, appending the
// resulting chunks and (if semantic/hybrid) vecs to the corpus state.
// A read error silently drops the file — by the time flush runs, the
// file may have been deleted again; we already tombstoned the old
// chunks, so falling through to "no new chunks" is the correct outcome.
// Caller holds corpusMu.
//
// v0.7.1: when rel lives in a migration directory, the SQL structural
// chunks are produced by the post-pass refoldMigrationDir rather than
// per-file — so skipSQLStructural is set to true here. Line-chunked
// raw text still flows through chunk.ChunkFile.
func (w *WatchedIndex) appendFile(rel string) {
	// Re-check admission at flush time (audit §15): ShouldIndex (with its
	// size + binary-sniff guards) ran at EVENT time, up to a debounce window
	// ago. A file that was 1 KB when its Create fired can be 800 MB by now
	// (a build artifact / growing log), or have turned binary. Re-running
	// ShouldIndex here re-Lstats and rejects it.
	if w.matcher != nil && !w.matcher.ShouldIndex(rel) {
		return
	}
	abs := filepath.Join(w.root, filepath.FromSlash(rel))
	data, err := w.readCappedFile(abs)
	if err != nil {
		return
	}
	skipSQLStructural := w.migrationDirs[path.Dir(rel)]
	cs, err := chunkOneFile(w.chunkerName, rel, data, skipSQLStructural)
	if err != nil {
		return
	}
	// M3 (code review §3): apply the SAME Arm B enrichment the initial build
	// does, gated on the same flag, BEFORE embedding — otherwise every file
	// edited during a watch session is re-indexed without the func:/calls:/
	// raises: label, and its BM25 tokens + embedding diverge from a fresh
	// build, degrading the index as edits accumulate.
	enrichChunks(rel, data, cs, w.fsOpts.DisableEnrichment)
	for _, c := range cs {
		w.chunks = append(w.chunks, c)
		if w.model != nil {
			w.vecs = append(w.vecs, encodeCached(w.fsOpts.EmbedCache, w.model, c.Text))
		}
	}
}

// notifySwap delivers one nonblocking signal to the onSwap channel if
// one is registered. Tests use this to synchronize on rebuilds.
func (w *WatchedIndex) notifySwap() {
	w.onSwapMu.Lock()
	ch := w.onSwap
	w.onSwapMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}

// relPath converts an absolute filesystem path from fsnotify into a
// repo-relative, slash-separated path matching how Walk emits files.
// Returns "" if path is outside root.
func (w *WatchedIndex) relPath(absPath string) string {
	rel, err := filepath.Rel(w.root, absPath)
	if err != nil || rel == "." {
		return ""
	}
	rel = filepath.ToSlash(rel)
	// Reject only genuine "outside root" escapes (audit §25): match exactly
	// ".." or a "../" prefix, NOT any name starting with ".." — legitimately
	// named entries like "..data" / "..config" (Kubernetes ConfigMap mounts)
	// must pass through.
	if rel == "" || rel == ".." || strings.HasPrefix(rel, "../") {
		return ""
	}
	return rel
}

// isTrackedRel reports whether the watcher has any existing chunks for
// the given relPath. Used to accept REMOVE/RENAME events even when the
// file is already gone (and thus stat-unavailable) — if we previously
// indexed it, we want to tombstone its chunks now.
func (w *WatchedIndex) isTrackedRel(rel string) bool {
	w.corpusMu.Lock()
	defer w.corpusMu.Unlock()
	// Exact file, OR a directory whose subtree we've indexed (audit §14: a
	// dir Remove/Rename must be accepted so its subtree gets tombstoned).
	prefix := rel + "/"
	for i := range w.chunks {
		if w.chunks[i].Tombstoned {
			continue
		}
		if w.chunks[i].File == rel || strings.HasPrefix(w.chunks[i].File, prefix) {
			return true
		}
	}
	return false
}

// enqueueDirFiles walks a directory that just appeared (created or moved-in)
// and enqueues every indexable file inside for (re)indexing (audit §2). No
// per-file fsnotify event fires for files that already existed when the watch
// was added, so we discover them ourselves — filtering through the same
// Matcher.ShouldIndex the initial walk uses. Prunes .git/ and .ken/. Best
// effort: walk errors on individual entries are skipped.
func (w *WatchedIndex) enqueueDirFiles(absDir string, dirty map[string]fsnotify.Op) {
	_ = filepath.WalkDir(absDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if name := d.Name(); name == ".git" || name == ".ken" {
				return fs.SkipDir
			}
			return nil
		}
		rel := w.relPath(p)
		if rel != "" && w.matcher.ShouldIndex(rel) {
			dirty[rel] = mergeOp(dirty[rel], fsnotify.Create)
		}
		return nil
	})
}

// logf writes a diagnostic to the FSOptions.LogWriter (nil discards).
// Used by the watcher loop, which has no other logger — every message
// goes to stderr in the ken-mcp server, never stdout (the JSON-RPC
// channel). Prefixed so operators can grep the watcher's output.
func (w *WatchedIndex) logf(format string, args ...any) {
	if w.fsOpts.LogWriter == nil {
		return
	}
	fmt.Fprintf(w.fsOpts.LogWriter, "search: watch: "+format+"\n", args...)
}

// enqueueFullResync re-walks the repo and marks every indexable file dirty
// (Write) plus every currently-indexed FS file that no longer exists on
// disk (Remove), so the next flush rebuilds the corpus from ground truth.
// Called after an fsnotify overflow, when queued events were dropped and
// the incremental view can no longer be trusted (audit search §3). Extra
// (Tier-2 DB) chunks live in w.extraChunks, not w.chunks, so they are not
// scanned here and never get spuriously tombstoned.
func (w *WatchedIndex) enqueueFullResync(dirty map[string]fsnotify.Op) {
	present := make(map[string]bool)
	_ = filepath.WalkDir(w.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if name := d.Name(); name == ".git" || name == ".ken" {
				return fs.SkipDir
			}
			return nil
		}
		rel := w.relPath(p)
		if rel != "" && w.matcher.ShouldIndex(rel) {
			dirty[rel] = mergeOp(dirty[rel], fsnotify.Write)
			present[rel] = true
		}
		return nil
	})
	// Tombstone indexed FS files that vanished while events were dropped.
	w.corpusMu.Lock()
	seen := make(map[string]bool)
	for i := range w.chunks {
		f := w.chunks[i].File
		if w.chunks[i].Tombstoned || seen[f] {
			continue
		}
		seen[f] = true
		if !present[f] {
			dirty[f] = mergeOp(dirty[f], fsnotify.Remove)
		}
	}
	w.corpusMu.Unlock()
}

// mergeOp combines two op bitmasks for the same path during a debounce
// window. The "latest op wins for REMOVE" rule means a write followed
// by remove keeps the remove; a remove followed by write keeps the
// write (the file came back).
func mergeOp(a, b fsnotify.Op) fsnotify.Op {
	if b&(fsnotify.Remove|fsnotify.Rename) != 0 {
		return b
	}
	if a&(fsnotify.Remove|fsnotify.Rename) != 0 && b&(fsnotify.Write|fsnotify.Create) != 0 {
		return b // resurrection
	}
	return a | b
}

// addRecursive registers `root` and every subdirectory with the
// fsnotify watcher except .git/ (load-bearing skip: any git operation
// fires hundreds of events inside .git/objects) and .ken/ (v0.8.3
// pre-built-index directory — paired with the matching prunes in
// internal/repo's WalkFS + Matcher.ShouldIndex so the watcher
// doesn't pay kernel-event cost for index.bin writes). Errors on
// individual dirs are logged silently — a permission-denied subdir
// shouldn't fail the whole watcher.
func addRecursive(w *fsnotify.Watcher, root string, logw io.Writer) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Permission denied on a single dir is non-fatal; skip it.
			if errors.Is(err, fs.ErrPermission) {
				return fs.SkipDir
			}
			return err
		}
		if !d.IsDir() {
			return nil
		}
		name := d.Name()
		if name == ".git" || name == ".ken" {
			return fs.SkipDir
		}
		if aerr := w.Add(path); aerr != nil {
			// Watch registration failing must NOT fail the whole index build
			// (audit search §4): the corpus is already walked/chunked/embedded,
			// and this only costs live updates for one subtree. ENOSPC
			// (inotify max_user_watches exhausted on a node_modules-heavy
			// monorepo) and EACCES are the common causes. Log once per dir and
			// continue — matching addRecursive's own doc-comment promise.
			if logw != nil {
				fmt.Fprintf(logw, "search: watch registration failed for %q: %v (live updates disabled for this subtree)\n", path, aerr)
			}
		}
		return nil
	})
}

// knownIndexedFile is a small helper for events on files that no longer
// exist on disk: we can't stat them, but if the rel path has one of ken's
// recognized source-file extensions we trust the event and let
// tombstoneFile + "no match found" be the safe no-op behavior.
//
// This is intentionally permissive: false negatives just mean a
// REMOVE/RENAME on a never-indexed file becomes a no-op tombstone attempt
// (no-op because no chunks match). False positives can't over-tombstone —
// tombstoneFile only marks matching chunks. (audit §26: dropped the unused
// `root` parameter and the never-implemented "special filenames" claim.)
func knownIndexedFile(rel string) bool {
	return chunk.Language(rel) != ""
}
