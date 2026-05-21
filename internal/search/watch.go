package search

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/townsendmerino/ken/internal/chunk"
	"github.com/townsendmerino/ken/internal/embed"
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
// docs/DECISIONS.md ADR-012 for the rationale (and why this isn't
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

	// Current snapshot. Read via wi.ix.Load(); never nil after
	// NewWatchedIndex returns successfully.
	ix atomic.Pointer[Index]

	// Mutable corpus state owned by the debouncer goroutine. chunks
	// and vecs are parallel; tombstoned chunks stay in `chunks` for
	// index stability across rebuilds, their vecs slot stays as the
	// originally-computed embedding (cheap; not zeroed).
	//
	// Held under corpusMu, but ONLY the debouncer touches them — the
	// mutex is defensive against future code that might grow a second
	// writer. Readers never take it.
	corpusMu sync.Mutex
	chunks   []chunk.Chunk
	vecs     [][]float32
	model    *embed.StaticModel

	// Filter shared with the debouncer: which fsnotify events
	// correspond to files ken would index.
	matcher *repo.Matcher

	// Watcher goroutine lifecycle.
	fs      *fsnotify.Watcher
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{} // closed by the goroutine just before exit
	closeMu sync.Mutex    // serializes Close() — idempotent

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
}

// NewWatchedIndex builds the initial snapshot via FromPath, then (if
// watch=true) starts the fsnotify-driven watcher goroutine. If
// watch=false, the returned WatchedIndex serves reads via Load() but
// never publishes a new snapshot — equivalent to v0.2 behavior, no
// watcher goroutine, no fsnotify state.
//
// Close() is safe to call regardless of `watch` and is idempotent.
// Uses the package-level WatchDebounce constant; tests override it
// via newWatchedIndexForTest below.
func NewWatchedIndex(root string, mode Mode, chunkerName, modelDir string, watch bool) (*WatchedIndex, error) {
	return newWatchedIndexWithDebounce(root, mode, chunkerName, modelDir, watch, WatchDebounce)
}

// newWatchedIndexWithDebounce is the test-friendly constructor. The
// debounce is captured into wi.debounce BEFORE the watcher goroutine
// starts reading it, eliminating the race we'd have if tests set
// wi.debounce post-construction.
func newWatchedIndexWithDebounce(root string, mode Mode, chunkerName, modelDir string, watch bool, debounce time.Duration) (*WatchedIndex, error) {
	chunks, vecs, model, err := walkAndChunk(root, mode, chunkerName, modelDir)
	if err != nil {
		return nil, err
	}
	wi := &WatchedIndex{
		root:        root,
		mode:        mode,
		chunkerName: chunkerName,
		modelDir:    modelDir,
		chunks:      chunks,
		vecs:        vecs,
		model:       model,
		matcher:     repo.NewMatcher(repo.Options{Root: root}),
		done:        make(chan struct{}),
		debounce:    debounce,
	}
	wi.ix.Store(BuildIndex(chunks, vecs, mode, model))

	if !watch {
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

	if err := addRecursive(w, root); err != nil {
		_ = w.Close()
		wi.cancel()
		close(wi.done)
		return nil, err
	}

	go wi.loop()
	return wi, nil
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

// Len returns the current snapshot's chunk count (includes tombstones).
// Useful for diagnostics; for an "active" count use Load().Len() minus
// the tombstone count (or just trust the chunks tombstone density to
// stay reasonable until v0.3.x compaction lands).
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

	resetTimer := func() {
		if timer == nil {
			timer = time.NewTimer(w.debounce)
			return
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(w.debounce)
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
				if knownIndexedFile(w.root, rel) || w.isTrackedRel(rel) {
					dirty[rel] = mergeOp(dirty[rel], ev.Op)
					resetTimer()
				}
				continue
			}
			// WRITE/CREATE: filter through matcher rules so we
			// don't reindex .git/HEAD, oversized binaries, etc.
			if !w.matcher.ShouldIndex(rel) {
				continue
			}
			dirty[rel] = mergeOp(dirty[rel], ev.Op)
			// Newly-created directory? Add it to the watcher so we
			// see events for files inside.
			if ev.Op&fsnotify.Create != 0 {
				w.addNewDir(ev.Name)
			}
			resetTimer()
		case err, ok := <-w.fs.Errors:
			if !ok {
				return
			}
			// fsnotify errors are transient (event-buffer overflow
			// on macOS, EBADF on close); log via... we don't have a
			// logger here. Silently continue — the watcher remains
			// armed and the next event lands fine.
			_ = err
		case <-timerC():
			batch := dirty
			dirty = make(map[string]fsnotify.Op)
			timer = nil
			w.flush(batch)
		}
	}
}

// flush rebuilds the snapshot from the current corpus state plus the
// batched dirty events. Called from the debouncer goroutine only.
func (w *WatchedIndex) flush(batch map[string]fsnotify.Op) {
	start := time.Now()
	w.corpusMu.Lock()
	defer w.corpusMu.Unlock()

	for rel, op := range batch {
		if op&(fsnotify.Remove|fsnotify.Rename) != 0 {
			w.tombstoneFile(rel)
			continue
		}
		// WRITE or CREATE: tombstone existing chunks for this file,
		// then re-chunk + re-embed and append.
		w.tombstoneFile(rel)
		w.appendFile(rel)
	}

	newIx := BuildIndex(w.chunks, w.vecs, w.mode, w.model)
	w.ix.Store(newIx)
	w.notifySwap()
	w.notifyFlush(len(w.chunks), len(batch), time.Since(start))
}

// notifyFlush calls the OnFlush callback (if set) with a one-line
// summary of the just-published snapshot. Format is stable enough for
// users to grep but not part of any public contract.
func (w *WatchedIndex) notifyFlush(totalChunks, filesChanged int, dur time.Duration) {
	w.onFlushMu.Lock()
	f := w.onFlush
	w.onFlushMu.Unlock()
	if f == nil {
		return
	}
	f(formatFlush(totalChunks, filesChanged, dur))
}

// formatFlush builds the OnFlush message. Pulled out for testability.
// Duration is always emitted as integer milliseconds — a sub-ms rebuild
// shows as "0 ms" rather than "0s" (time.Duration.String collapses
// fractions, which makes the message inconsistent across small repos).
func formatFlush(totalChunks, filesChanged int, dur time.Duration) string {
	return "reindexed: " +
		intStr(totalChunks) + " chunks total, " +
		intStr(filesChanged) + " files changed in " +
		intStr(int(dur.Milliseconds())) + " ms"
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

// tombstoneFile marks every existing chunk whose File == rel as
// Tombstoned. Caller holds corpusMu.
func (w *WatchedIndex) tombstoneFile(rel string) {
	for i := range w.chunks {
		if w.chunks[i].File == rel && !w.chunks[i].Tombstoned {
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
func (w *WatchedIndex) appendFile(rel string) {
	abs := filepath.Join(w.root, filepath.FromSlash(rel))
	data, err := os.ReadFile(abs)
	if err != nil {
		return
	}
	cs, err := chunk.ChunkFile(w.chunkerName, rel, data, chunk.DefaultChunkSize)
	if err != nil {
		return
	}
	for _, c := range cs {
		w.chunks = append(w.chunks, c)
		if w.model != nil {
			w.vecs = append(w.vecs, w.model.Encode(c.Text))
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
	if rel == "" || rel[0] == '.' && len(rel) >= 2 && rel[1] == '.' {
		// rel starting with ".." means outside root.
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
	for i := range w.chunks {
		if w.chunks[i].File == rel && !w.chunks[i].Tombstoned {
			return true
		}
	}
	return false
}

// addNewDir adds a newly-created directory to the watcher recursively.
// Used when CREATE events report a new subdirectory: without this,
// files created inside it never fire events. Skips .git silently.
func (w *WatchedIndex) addNewDir(absPath string) {
	info, err := os.Stat(absPath)
	if err != nil || !info.IsDir() {
		return
	}
	if filepath.Base(absPath) == ".git" {
		return
	}
	_ = w.fs.Add(absPath)
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
// fires hundreds of events inside .git/objects). Errors on individual
// dirs are logged silently — a permission-denied subdir shouldn't fail
// the whole watcher.
func addRecursive(w *fsnotify.Watcher, root string) error {
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
		if d.Name() == ".git" {
			return fs.SkipDir
		}
		return w.Add(path)
	})
}

// knownIndexedFile is a small helper for events on files that no
// longer exist on disk: we can't stat them, but if the rel path looks
// like one of ken's normal source-file extensions OR is a known
// special filename, we'll trust the event and let tombstoneFile +
// "no match found" be the safe no-op behavior.
//
// This is intentionally permissive: false negatives just mean a
// REMOVE/RENAME on a never-indexed file becomes a no-op tombstone
// attempt (no-op because no chunks match). False positives can't
// over-tombstone — tombstoneFile only marks matching chunks.
func knownIndexedFile(root, rel string) bool {
	if chunk.Language(rel) != "" {
		return true
	}
	return false
}
