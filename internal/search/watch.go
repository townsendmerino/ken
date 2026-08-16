package search

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/townsendmerino/aikit/chunk"
	"github.com/townsendmerino/aikit/embed"
	"github.com/townsendmerino/ken/internal/repo"
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

	// stagedPending is true from construction until the M4 staged warm pass
	// fills w.vecs. While set, w.vecs is intentionally empty (BM25 served) and
	// the watch-path append sites (appendFile/refoldMigrationDir) must NOT
	// append per-chunk vectors — otherwise w.vecs grows to `c` entries against
	// N+c chunks and compactCorpus panics (audit N1). Cleared by the warm pass
	// under corpusMu once it publishes a full-length vecs. Written only under
	// corpusMu; read there too.
	stagedPending bool

	// loopWakeups counts debounce-timer fires the loop has serviced. Test-only
	// observability (audit round-3 "test quality"): a slow-flush composition
	// test asserts this stays bounded, which is what actually catches R1's
	// busy-spin — eventual convergence alone passed even with R1 reverted.
	loopWakeups atomic.Int64

	// loopRemoveDelivered counts Remove/Rename events the loop has received,
	// incremented at delivery BEFORE the accept decision. Test-only
	// observability (audit round-4 "test quality"): the N2 bite test fires a
	// dir rename while a flush holds the corpus in its unpublished window and
	// waits on this counter so the gate decision is pinned to happen BEFORE the
	// flush publishes — without that synchronization the event can land after
	// publish, where even the reverted gate would accept it, and the test stops
	// biting (the original N2 test's exact flaw).
	loopRemoveDelivered atomic.Int64

	// onReconcileAppended, when non-nil, is invoked inside reconcileCorpusLocked
	// after the batch's chunks have been appended to w.chunks but before the
	// caller publishes — the in-flight unpublished window N2 concerns. Test-only
	// (nil in production). It receives the batch and returns true to consume the
	// hook (fire once) or false to stay installed for a later flush — so a test
	// can wait for the specific batch it cares about even when startup Create
	// events trigger earlier flushes (audit round-5 robustness note).
	onReconcileAppended func(batch map[string]fsnotify.Op) bool

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

// EmbedModel returns the Model2Vec model this index queries with (nil for
// BM25 mode). ken-mcp uses it to compute the snapshot config-key's model
// fingerprint without reloading the model from disk. The returned model is
// read-only; callers must not mutate it.
func (w *WatchedIndex) EmbedModel() *embed.StaticModel { return w.model }

// Load returns the current Index snapshot. Goroutine-safe; one atomic
// load. Never returns nil after NewWatchedIndex succeeds.
func (w *WatchedIndex) Load() *Index { return w.ix.Load() }

// setReconcileHook installs a hook fired inside reconcileCorpusLocked after
// appends and before publish (test-only; see onReconcileAppended). The hook
// receives the batch and returns true to consume itself. Set under corpusMu so
// it synchronizes with the flush goroutine's read.
func (w *WatchedIndex) setReconcileHook(f func(batch map[string]fsnotify.Op) bool) {
	w.corpusMu.Lock()
	w.onReconcileAppended = f
	w.corpusMu.Unlock()
}

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
// them, accumulates a dirty set, and dispatches a flush after the debounce
// window. corpusMu is a REAL lock, not defensive (audit R8 note): since the
// async flush landed, w.chunks/w.vecs are written by the flush goroutine,
// warmCorpusInBackground, and SetExtraChunks — not only this goroutine. The
// loop itself never takes corpusMu (isTrackedRel / enqueueFullResync read
// the published snapshot lock-free) so it can keep draining events while a
// flush holds the lock.
func (w *WatchedIndex) loop() {
	defer close(w.done)

	dirty := make(map[string]fsnotify.Op)
	// resyncPresent, when non-nil, is the disk truth captured by the most recent
	// event-queue-overflow resync; it travels with `dirty` into the next flush,
	// which tombstones any indexed file absent from it (audit R4-6). nil on the
	// normal path.
	var resyncPresent map[string]bool
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
			delay = max(remaining, 0)
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
			// REMOVE/RENAME: the file is already gone; ShouldIndex would
			// return false (stat fails). Accept UNCONDITIONALLY (audit N2):
			// the old `knownIndexedFile || isTrackedRel` gate read a snapshot
			// that's stale for the whole in-flight flush, so a directory
			// rename during a flush that just appended that dir's chunks was
			// dropped and its chunks orphaned forever (§14's exact case). The
			// batched tombstoneFile is a documented no-op when nothing matches
			// ("false positives can't over-tombstone"), so accepting every
			// Count Remove/Rename delivery before the accept for test
			// synchronization (see loopRemoveDelivered); a no-op in production.
			if ev.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
				w.loopRemoveDelivered.Add(1)
			}
			// Remove/Rename is safe; a stray non-indexed remove costs at most
			// one no-op rebuild (the flush short-circuits it, audit R4-3).
			// (.git isn't watched, so no git-op flood.)
			if ev.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
				dirty[rel] = mergeOp(dirty[rel], ev.Op)
				resetTimer()
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
					// Gitignore-gate the new directory (audit R9): without this,
					// `npm install` creating 30k node_modules/ subdirs each
					// triggers a full recursive watch + WalkDir inline here,
					// exhausting the inotify watch table and stalling the loop.
					// ShouldDescend rejects gitignored (and .git/.ken) trees
					// cheaply before any walk.
					if !w.matcher.ShouldDescend(rel) {
						continue
					}
					w.addRecursiveWatch(ev.Name)
					// Only arm the debounce if the new dir actually contributed
					// indexable files (audit N8): a mkdir of an empty or
					// all-ignored dir must not set firstDirty with an empty
					// batch, or the ceiling later drives delay to 0 and bypasses
					// the debounce for the next real event.
					if w.enqueueDirFiles(ev.Name, dirty) {
						resetTimer()
					}
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
				resyncPresent = w.enqueueFullResync(dirty)
				resetTimer()
				continue
			}
			// Non-overflow errors (EBADF on close, transient inotify hiccups)
			// are logged but not acted on — the watcher stays armed.
			w.logf("fsnotify error: %v", err)
		case <-timerC():
			w.loopWakeups.Add(1)
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
			batchResync := resyncPresent
			resyncPresent = nil
			timer = nil
			firstDirty = time.Time{} // start a fresh debounce window for the next batch
			flushing = true
			// Tracked by bgWG so Close() (which does <-w.done then
			// bgWG.Wait()) awaits an in-flight flush before teardown —
			// no flush goroutine writes w.ix after the cache reaps this
			// index / rm -rf's a temp clone.
			w.bgWG.Go(func() {
				w.flush(batch, batchResync)
				select {
				case flushDone <- struct{}{}:
				default: // loop already gone (shutdown) — don't block
				}
			})
		case <-flushDone:
			flushing = false
			if len(dirty) > 0 {
				// Events landed while the flush ran — schedule the next one.
				resetTimer()
			} else {
				// Nothing pending — clear firstDirty (audit N8) so a stray
				// timer-fire-while-flushing that kept it set can't make the
				// next event compute delay=0 and bypass the debounce.
				firstDirty = time.Time{}
			}
		}
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

// enqueueDirFiles walks a directory that just appeared (created or moved-in)
// and enqueues every indexable file inside for (re)indexing (audit §2). No
// per-file fsnotify event fires for files that already existed when the watch
// was added, so we discover them ourselves — filtering through the same
// Matcher.ShouldIndex the initial walk uses. Prunes .git/ and .ken/. Best
// effort: walk errors on individual entries are skipped.
func (w *WatchedIndex) enqueueDirFiles(absDir string, dirty map[string]fsnotify.Op) bool {
	added := false
	_ = filepath.WalkDir(absDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if name := d.Name(); name == ".git" || name == ".ken" {
				return fs.SkipDir
			}
			// Prune gitignored subtrees so a legit new dir containing an
			// ignored subdir (e.g. its own node_modules) doesn't walk it
			// (audit R9). SkipDir on the dir's own path is a no-op.
			if reld := w.relPath(p); reld != "" && !w.matcher.ShouldDescend(reld) {
				return fs.SkipDir
			}
			return nil
		}
		rel := w.relPath(p)
		if rel != "" && w.matcher.ShouldIndex(rel) {
			dirty[rel] = mergeOp(dirty[rel], fsnotify.Create)
			added = true
		}
		return nil
	})
	return added
}

// enqueueFullResync re-walks the repo and marks every indexable file dirty
// (Write) plus every currently-indexed FS file that no longer exists on
// disk (Remove), so the next flush rebuilds the corpus from ground truth.
// Called after an fsnotify overflow, when queued events were dropped and
// the incremental view can no longer be trusted (audit search §3). Extra
// (Tier-2 DB) chunks live in w.extraChunks, not w.chunks, so they are not
// scanned here and never get spuriously tombstoned.
func (w *WatchedIndex) enqueueFullResync(dirty map[string]fsnotify.Op) map[string]bool {
	// Re-register watches over the whole tree first (audit search §3): the
	// overflow may have dropped the Create events for directories added
	// during the burst, so those subtrees have no inotify watch and would
	// stay dark. addRecursive is idempotent (re-Adding a watched dir is a
	// no-op) and gitignore-pruning happens via the per-file ShouldIndex
	// filter below. addRecursiveWatch no-ops when w.fs is nil (ReconcileFiles
	// callers) and prunes gitignored trees so the resync doesn't itself
	// exhaust the watch table (audit §3 leftover).
	w.addRecursiveWatch(w.root)

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
	// Deletion detection is deferred to the flush (audit R4-6): computing it
	// here against the published snapshot (w.ix.Load()) MISSES a file that an
	// in-flight flush already appended to w.chunks but hasn't published yet —
	// if that file was deleted with its Remove dropped by the same overflow, it
	// would appear in neither `present` (gone from disk) nor the stale snapshot,
	// so its chunks would never be tombstoned. Instead we hand `present` to the
	// flush, which (single-flighted after the in-flight one completes) tombstones
	// every chunk whose File is absent from disk AND untouched by this batch,
	// reading the authoritative w.chunks under corpusMu.
	return present
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

// addRecursiveWatch registers absRoot and every subdirectory with the
// fsnotify watcher, EXCEPT .git/ (load-bearing: any git op fires hundreds of
// events inside .git/objects), .ken/, and any gitignored subtree (audit
// R9/§3 leftover): without the ignore prune, a legit dir containing its own
// node_modules — or the overflow resync re-walking the whole root — registers
// an inotify watch on every ignored directory, exhausting max_user_watches.
// Pruning here mirrors enqueueDirFiles's ShouldDescend gate.
//
// Never fails the build over a watch problem (audit search §4): a dir we
// can't read (any WalkDir error, not only ErrPermission — the prior code
// aborted on the rest) is skipped, and a failed w.Add (ENOSPC when inotify
// max_user_watches is exhausted, EACCES) just loses live updates for that
// subtree. Add-failures are counted and reported as ONE summary line, not
// one-per-dir (audit R12: ~42k lines at startup on a 50k-dir tree past an
// 8192 watch limit).
func (w *WatchedIndex) addRecursiveWatch(absRoot string) {
	if w.fs == nil {
		return
	}
	var addFails int
	var firstErr error
	var firstPath string
	_ = filepath.WalkDir(absRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return fs.SkipDir // unreadable dir — skip, never abort
		}
		if !d.IsDir() {
			return nil
		}
		if name := d.Name(); name == ".git" || name == ".ken" {
			return fs.SkipDir
		}
		// Don't watch gitignored subtrees (R9/§3): also prunes their descent.
		if reld := w.relPath(p); reld != "" && !w.matcher.ShouldDescend(reld) {
			return fs.SkipDir
		}
		if aerr := w.fs.Add(p); aerr != nil {
			addFails++
			if firstErr == nil {
				firstErr, firstPath = aerr, p
			}
		}
		return nil
	})
	if addFails > 0 {
		w.logf("watch registration failed for %d dir(s) under %q (first: %q: %v); live updates disabled for those subtrees",
			addFails, absRoot, firstPath, firstErr)
	}
}
