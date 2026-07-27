package search

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

// makeTempRepo materializes a tiny Python-only repo under t.TempDir()
// for the watcher tests. Returns the root and a per-test cleanup hook
// (t.TempDir handles directory cleanup itself; this is just for
// readability).
func makeTempRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		path := filepath.Join(root, rel)
		if dir := filepath.Dir(path); dir != root {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", dir, err)
			}
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	return root
}

// shortDebounce is a per-test override of the global WatchDebounce so
// the test suite finishes in seconds, not minutes. Installed via
// WatchedIndex.debounce.
const shortDebounce = 60 * time.Millisecond

// withShortDebounce constructs a WatchedIndex with a short debounce
// installed via the package-private test constructor (passing the
// debounce as a constructor arg is the only way to avoid racing on
// wi.debounce against the goroutine).
func withShortDebounce(t *testing.T, root string, watch bool) *WatchedIndex {
	t.Helper()
	wi, err := newWatchedIndexWithDebounce(context.Background(), root, ModeBM25, "regex", "", watch, shortDebounce, FSOptions{})
	if err != nil {
		t.Fatalf("NewWatchedIndex: %v", err)
	}
	t.Cleanup(func() { _ = wi.Close() })
	return wi
}

// TestWatchedIndex_NoWatch_LoadReturnsInitial — watch=false: Load()
// returns the initial snapshot indefinitely; no goroutine, no panic,
// repeated Loads return the same pointer.
func TestWatchedIndex_NoWatch_LoadReturnsInitial(t *testing.T) {
	root := makeTempRepo(t, map[string]string{
		"a.py": "def alpha():\n    return 1\n",
	})
	wi := withShortDebounce(t, root, false)

	first := wi.Load()
	if first == nil {
		t.Fatal("Load() returned nil")
	}
	if first.Len() == 0 {
		t.Fatal("initial snapshot has 0 chunks; expected ≥1 for a.py")
	}

	// Re-Load — same pointer, no rebuild.
	if wi.Load() != first {
		t.Error("Load() returned a different pointer with watch=false")
	}

	// Wait a beat to confirm nothing fires async.
	time.Sleep(4 * shortDebounce)
	if wi.Load() != first {
		t.Error("snapshot changed despite watch=false")
	}
}

// TestWatchedIndex_FileWrite_Republishes — watch=true: writing a new
// file triggers a debounce flush, the resulting snapshot has the new
// file's chunks visible to Search.
func TestWatchedIndex_FileWrite_Republishes(t *testing.T) {
	root := makeTempRepo(t, map[string]string{
		"a.py": "def alpha():\n    return 1\n",
	})
	wi := withShortDebounce(t, root, true)
	swaps := make(chan struct{}, 4)
	wi.SetOnSwap(swaps)

	// Drain any startup events (some platforms fire CREATE on the
	// initial walk-then-watch transition).
	drainSwaps(swaps)

	// Write a new file. Use a name that the regex chunker recognizes
	// as Python (.py) so it produces an indexable chunk.
	newFile := filepath.Join(root, "beta.py")
	if err := os.WriteFile(newFile, []byte("def beta():\n    return 'beta'\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if !waitForSwap(t, swaps, 5*time.Second) {
		t.Fatal("did not observe a snapshot publish after file write")
	}

	got := wi.Load()
	if !containsFile(got, "beta.py") {
		t.Errorf("post-write snapshot missing beta.py; chunks: %s", chunkFiles(got))
	}
	results := wi.Search("beta", 5)
	if len(results) == 0 {
		t.Error("Search('beta') returned 0 results; expected to find beta.py")
	}
}

// TestWatchedIndex_FileDelete_DropsChunks — watch=true: removing a file
// drops its chunks from the published snapshot entirely (compaction
// runs every flush, so the tombstones don't survive into the published
// snapshot) and Search no longer surfaces them.
func TestWatchedIndex_FileDelete_DropsChunks(t *testing.T) {
	root := makeTempRepo(t, map[string]string{
		"keep.py":   "def keep():\n    return 'keep'\n",
		"delete.py": "def doomed():\n    return 'doomed'\n",
	})
	wi := withShortDebounce(t, root, true)
	swaps := make(chan struct{}, 4)
	wi.SetOnSwap(swaps)
	drainSwaps(swaps)

	if err := os.Remove(filepath.Join(root, "delete.py")); err != nil {
		t.Fatal(err)
	}

	if !waitForSwap(t, swaps, 5*time.Second) {
		t.Fatal("no snapshot publish after delete")
	}

	got := wi.Load()
	// Post-compaction the published snapshot has zero entries for
	// delete.py — live OR tombstoned. The tombstone existed transiently
	// during the flush but was dropped before publish.
	for _, c := range got.chunks {
		if c.File == "delete.py" {
			t.Errorf("post-delete snapshot still has chunk for delete.py (tombstoned=%v); compaction should have dropped it", c.Tombstoned)
		}
	}

	for _, r := range wi.Search("doomed", 5) {
		if r.Chunk.File == "delete.py" {
			t.Errorf("Search returned delete.py chunk after delete: %+v", r.Chunk)
		}
	}
}

// TestWatchedIndex_Compaction_DropsTombstones — multiple deletes within
// a single debounce window result in a published snapshot whose chunks
// slice has zero tombstoned entries at all (not just zero for the
// deleted files — none for any file).
func TestWatchedIndex_Compaction_DropsTombstones(t *testing.T) {
	root := makeTempRepo(t, map[string]string{
		"keep.py":  "def keep(): return 'keep'\n",
		"drop1.py": "def doomed_one(): return 1\n",
		"drop2.py": "def doomed_two(): return 2\n",
	})
	wi := withShortDebounce(t, root, true)
	if wi.Load().Len() < 3 {
		t.Fatalf("expected ≥3 initial chunks, got %d", wi.Load().Len())
	}

	swaps := make(chan struct{}, 4)
	wi.SetOnSwap(swaps)
	drainSwaps(swaps)

	for _, name := range []string{"drop1.py", "drop2.py"} {
		if err := os.Remove(filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}

	if !waitForSwap(t, swaps, 5*time.Second) {
		t.Fatal("no snapshot publish after deletes")
	}

	got := wi.Load()
	var tombs int
	for _, c := range got.chunks {
		if c.Tombstoned {
			tombs++
		}
		if c.File == "drop1.py" || c.File == "drop2.py" {
			t.Errorf("published snapshot still references %s (tombstoned=%v)", c.File, c.Tombstoned)
		}
	}
	if tombs != 0 {
		t.Errorf("published snapshot has %d tombstones; compaction should have dropped them all", tombs)
	}
}

// TestWatchedIndex_Compaction_PreservesChunkContent — deleting a file in
// the middle of the chunks slice still resolves the surviving files'
// chunks correctly via ResolveChunk and Search. Catches any remap bug
// where the compacted slice and its parallel structures (vecs, BM25
// postings, ann.Flat rows) drift out of sync.
//
// As of the audit §1 fix, reconcileCorpusLocked copies-on-write
// (slices.Clone) before any tombstoneFile mutation, so a published
// snapshot's chunk array is never written after Store — reads via
// ResolveChunk / Search (and even a direct wi.Load().chunks read) no
// longer race the debouncer.
//
// NB the earlier guidance here was imprecise: atomic.Pointer.Load
// synchronizes the *pointer*, not the pointed-to array, so before the
// COW fix ResolveChunk / Search raced tombstoneFile's in-place write
// exactly the same as a direct read would (the race detector just
// couldn't see fsnotify's OS-mediated happens-before to flag it).
// The invariant that makes reads safe is "the writer never mutates a
// published array," not the atomic Load. Commit eaa9406 has the
// original race this guideline came from.
func TestWatchedIndex_Compaction_PreservesChunkContent(t *testing.T) {
	root := makeTempRepo(t, map[string]string{
		"a.py": "def alpha():\n    return 'a'\n",
		"b.py": "def beta():\n    return 'b'\n",
		"c.py": "def gamma():\n    return 'c'\n",
	})
	wi := withShortDebounce(t, root, true)
	swaps := make(chan struct{}, 4)
	wi.SetOnSwap(swaps)
	drainSwaps(swaps)

	if err := os.Remove(filepath.Join(root, "b.py")); err != nil {
		t.Fatal(err)
	}
	if !waitForSwap(t, swaps, 5*time.Second) {
		t.Fatal("no snapshot publish after delete")
	}

	// ResolveChunk: survivors round-trip with content matching the
	// substring we wrote. A bad remap would either return nil or
	// surface a chunk whose Text belongs to a different file.
	for _, want := range []struct{ file, contains string }{
		{"a.py", "alpha"},
		{"c.py", "gamma"},
	} {
		r := wi.ResolveChunk(want.file, 1)
		if r == nil {
			t.Errorf("ResolveChunk(%s, 1) returned nil after compaction", want.file)
			continue
		}
		if !strings.Contains(r.Text, want.contains) {
			t.Errorf("ResolveChunk(%s) returned wrong content: text=%q want substring %q", want.file, r.Text, want.contains)
		}
	}

	// Search: BM25-rebuild path also survives the compaction — a
	// remap-induced off-by-one in postings would either drop the result
	// or attach it to the wrong file.
	found := false
	for _, r := range wi.Search("gamma", 5) {
		if r.Chunk.File == "c.py" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Search('gamma') did not return c.py's chunk after b.py was compacted")
	}
}

// TestWatchedIndex_Compaction_RaceClean — writer deletes + recreates
// files in a tight loop while readers query. Must run race-clean and
// never observe a tombstoned chunk in results. The atomic snapshot model
// is what makes this safe: each compaction allocates fresh backing
// slices, so the previously-published *Index that an in-flight reader
// holds keeps pointing at the older (immutable) slice.
func TestWatchedIndex_Compaction_RaceClean(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping concurrency stress in -short")
	}
	root := makeTempRepo(t, map[string]string{
		"a.py": "def alpha(): return 1\n",
		"b.py": "def beta(): return 2\n",
	})
	wi := withShortDebounce(t, root, true)

	var stop atomic.Bool
	var wg sync.WaitGroup
	const readers = 10
	const queries = 100

	for range readers {
		wg.Go(func() {
			for q := 0; q < queries && !stop.Load(); q++ {
				for _, r := range wi.Search("alpha", 5) {
					if r.Chunk.Tombstoned {
						t.Errorf("Search returned tombstoned chunk: %+v", r.Chunk)
						return
					}
				}
			}
		})
	}

	// Writer: delete-then-recreate cycle on synth.py, repeatedly. Every
	// recreate adds chunks, every delete tombstones them — every flush
	// hits the compaction path.
	go func() {
		synth := filepath.Join(root, "synth.py")
		for i := range 8 {
			time.Sleep(10 * time.Millisecond)
			_ = os.WriteFile(synth, []byte("def synth(): return "+string(rune('0'+i))+"\n"), 0o644)
			time.Sleep(10 * time.Millisecond)
			_ = os.Remove(synth)
		}
	}()

	wg.Wait()
	stop.Store(true)
	time.Sleep(5 * shortDebounce)
}

// TestWatchedIndex_Debounce_BatchedWrites — five writes in quick
// succession should result in exactly one snapshot publish, not five.
func TestWatchedIndex_Debounce_BatchedWrites(t *testing.T) {
	root := makeTempRepo(t, map[string]string{
		"seed.py": "def seed():\n    return 0\n",
	})
	wi := withShortDebounce(t, root, true)
	swaps := make(chan struct{}, 16)
	wi.SetOnSwap(swaps)
	drainSwaps(swaps)

	// Five rapid writes, well inside the debounce window.
	for i := range 5 {
		name := filepath.Join(root, "batch_"+string(rune('a'+i))+".py")
		if err := os.WriteFile(name, []byte("def f(): pass\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if !waitForSwap(t, swaps, 5*time.Second) {
		t.Fatal("no swap after batched writes")
	}

	// Wait for the debounce window to fully clear + a little margin,
	// then count any additional swaps.
	time.Sleep(5 * shortDebounce)
	extras := drainSwaps(swaps)
	if extras > 0 {
		t.Errorf("expected exactly one swap for batched writes, got %d additional swaps", extras+1)
	}

	// Sanity: all five files should be present in the final snapshot.
	got := wi.Load()
	for i := range 5 {
		name := "batch_" + string(rune('a'+i)) + ".py"
		if !containsFile(got, name) {
			t.Errorf("post-batch snapshot missing %s", name)
		}
	}
}

// TestWatchedIndex_ConcurrentReads_DuringWrite — the load-bearing
// concurrency test. 10 reader goroutines doing 100 Search() calls
// each; concurrent writer triggers 5 rewrites. Must run race-clean and
// never return a tombstoned chunk.
func TestWatchedIndex_ConcurrentReads_DuringWrite(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping concurrency stress in -short")
	}
	root := makeTempRepo(t, map[string]string{
		"a.py": "def alpha(): return 1\n",
		"b.py": "def beta(): return 2\n",
		"c.py": "def gamma(): return 3\n",
	})
	wi := withShortDebounce(t, root, true)

	var stop atomic.Bool
	var wg sync.WaitGroup
	const readers = 10
	const queries = 100

	for range readers {
		wg.Go(func() {
			for q := 0; q < queries && !stop.Load(); q++ {
				results := wi.Search("alpha", 5)
				for _, r := range results {
					if r.Chunk.Tombstoned {
						t.Errorf("Search returned tombstoned chunk: %+v", r.Chunk)
						return
					}
					if r.Chunk.File == "" {
						t.Errorf("Search returned chunk with empty File")
						return
					}
				}
			}
		})
	}

	// Writer: 5 file rewrites at 10ms intervals — within the
	// per-iteration query bursts, so reads and snapshot swaps overlap.
	go func() {
		for i := range 5 {
			time.Sleep(10 * time.Millisecond)
			name := filepath.Join(root, "synth_"+string(rune('a'+i))+".py")
			_ = os.WriteFile(name, []byte("def f(): return "+string(rune('0'+i))+"\n"), 0o644)
		}
	}()

	wg.Wait()
	stop.Store(true)

	// Let the watcher's debounce window flush any pending dirties so
	// Close doesn't race with an in-flight rebuild.
	time.Sleep(5 * shortDebounce)
}

// TestWatchedIndex_Close_StopsWatcher — Close() returns nil, no
// goroutine leak.
func TestWatchedIndex_Close_StopsWatcher(t *testing.T) {
	root := makeTempRepo(t, map[string]string{"a.py": "def a(): pass\n"})

	before := runtime.NumGoroutine()
	wi, err := newWatchedIndexWithDebounce(context.Background(), root, ModeBM25, "regex", "", true, shortDebounce, FSOptions{})
	if err != nil {
		t.Fatal(err)
	}

	if err := wi.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := wi.Close(); err != nil {
		t.Errorf("second Close (idempotent): %v", err)
	}

	// Give the runtime a beat to GC away the watcher goroutine.
	time.Sleep(50 * time.Millisecond)
	after := runtime.NumGoroutine()
	// Allow some slack — Go's testing framework spawns scheduler
	// helpers under -race that can vary. Tolerate ≤2 extra goroutines
	// to keep the test stable; the watcher goroutine going away is
	// what we care about, and that's far less than the slack.
	if after > before+2 {
		t.Errorf("goroutine leak: before=%d, after=%d (slack 2)", before, after)
	}
}

// TestWatchedIndex_OnFlush_DeliversMessage — SetOnFlush installs a
// callback that fires once per debounce flush with a non-empty
// "reindexed: ..." string. The CLI's `ken index --watch` interactive
// log and ken-mcp's info-level diagnostic both depend on this.
func TestWatchedIndex_OnFlush_DeliversMessage(t *testing.T) {
	root := makeTempRepo(t, map[string]string{
		"a.py": "def alpha():\n    return 1\n",
	})
	wi := withShortDebounce(t, root, true)

	swaps := make(chan struct{}, 4)
	wi.SetOnSwap(swaps)

	var mu sync.Mutex
	var msgs []string
	wi.SetOnFlush(func(msg string) {
		mu.Lock()
		defer mu.Unlock()
		msgs = append(msgs, msg)
	})

	drainSwaps(swaps)

	if err := os.WriteFile(filepath.Join(root, "beta.py"), []byte("def beta(): pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !waitForSwap(t, swaps, 5*time.Second) {
		t.Fatal("no swap after file write")
	}
	// Give the goroutine a beat to call the callback after the swap.
	time.Sleep(20 * time.Millisecond)

	mu.Lock()
	got := append([]string(nil), msgs...)
	mu.Unlock()

	if len(got) == 0 {
		t.Fatal("OnFlush never fired after a file write")
	}
	last := got[len(got)-1]
	for _, want := range []string{"reindexed:", "chunks total", "files changed", "ms"} {
		if !strings.Contains(last, want) {
			t.Errorf("flush message %q missing %q", last, want)
		}
	}
}

// TestWatchedIndex_GitDirIgnored — touching files inside .git/ does
// not trigger a snapshot publish.
func TestWatchedIndex_GitDirIgnored(t *testing.T) {
	root := makeTempRepo(t, map[string]string{
		"a.py": "def a(): pass\n",
	})
	// Create a .git/ inside the repo and a file beneath it.
	gitDir := filepath.Join(root, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}

	wi := withShortDebounce(t, root, true)
	swaps := make(chan struct{}, 8)
	wi.SetOnSwap(swaps)
	drainSwaps(swaps)

	// Write some .git/ noise that a real git command would produce.
	for _, rel := range []string{"HEAD", "index", "logs/HEAD", "objects/aa/bb"} {
		path := filepath.Join(gitDir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("noise"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Wait one full debounce + margin. Expect zero swaps.
	time.Sleep(5 * shortDebounce)
	if extras := drainSwaps(swaps); extras > 0 {
		t.Errorf(".git/ writes produced %d snapshot publishes; expected 0", extras)
	}
}

// --- helpers --------------------------------------------------------

// drainSwaps consumes all currently-pending values on ch nonblockingly
// and returns how many it drained.
func drainSwaps(ch <-chan struct{}) int {
	n := 0
	for {
		select {
		case <-ch:
			n++
		default:
			return n
		}
	}
}

// waitForSwap blocks until a swap arrives or the deadline elapses.
// Returns true if a swap was observed.
func waitForSwap(t *testing.T, ch <-chan struct{}, timeout time.Duration) bool {
	t.Helper()
	select {
	case <-ch:
		return true
	case <-time.After(timeout):
		return false
	}
}

// containsFile reports whether any non-tombstoned chunk in the
// snapshot has File == name.
func containsFile(ix *Index, name string) bool {
	for _, c := range ix.chunks {
		if !c.Tombstoned && c.File == name {
			return true
		}
	}
	return false
}

// chunkFiles returns a multiset of (File, Tombstoned) for the snapshot,
// formatted for error messages.
func chunkFiles(ix *Index) string {
	seen := map[string]int{}
	for _, c := range ix.chunks {
		key := c.File
		if c.Tombstoned {
			key += "[T]"
		}
		seen[key]++
	}
	var b []byte
	for k, n := range seen {
		b = append(b, ' ')
		b = append(b, k...)
		if n > 1 {
			b = append(b, '×')
			b = append(b, byte('0'+n))
		}
	}
	return string(b)
}

// TestWatchedIndex_DeleteWhileReading_NoRace is the audit §1 regression: delete
// an already-indexed file (→ tombstoneFile in-place write) while reader
// goroutines loop Search / ResolveChunk (which read chunk[i].Tombstoned
// lock-free). Before the copy-on-write fix this was an unsynchronized
// read/write on the published snapshot's array; with the clone it's clean.
// Run under -race.
func TestWatchedIndex_DeleteWhileReading_NoRace(t *testing.T) {
	root := makeTempRepo(t, map[string]string{
		"a.py": "def alpha():\n    return 'a'\n",
		"b.py": "def beta():\n    return 'b'\n",
	})
	wi, err := NewWatchedIndex(root, ModeBM25, "line", "", true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = wi.Close() }()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					wi.Search("alpha", 5)
					wi.ResolveChunk("a.py", 1)
				}
			}
		}()
	}

	// Delete a.py (tombstone) and churn b.py — reconcile runs the mutation
	// path under corpusMu while the readers race the published array.
	_ = os.Remove(filepath.Join(root, "a.py"))
	wi.ReconcileFiles(nil, []string{"a.py"})
	for range 5 {
		wi.ReconcileFiles([]string{"b.py"}, nil)
	}

	close(stop)
	wg.Wait()

	if len(wi.Search("alpha", 5)) != 0 {
		t.Error("deleted a.py should be gone from results after reconcile")
	}
}

// TestWatchedIndex_MovedInDirectory_Indexed is the audit §2 regression: moving
// a directory (with existing files) into the watched tree fires ONE Create for
// the dir and no per-file events, so enqueueDirFiles is the only path that
// indexes its contents. mkdir-then-write would also exercise it, but a move is
// the deterministic "files already exist" case.
func TestWatchedIndex_MovedInDirectory_Indexed(t *testing.T) {
	root := makeTempRepo(t, map[string]string{
		"main.py": "def main():\n    return 1\n",
	})
	wi := withShortDebounce(t, root, true)
	swaps := make(chan struct{}, 8)
	wi.SetOnSwap(swaps)
	drainSwaps(swaps)

	// Stage a package OUTSIDE the watched root, then move it in.
	staging := filepath.Join(t.TempDir(), "authpkg")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "token.py"),
		[]byte("def mint_token():\n    return 'jwt'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(staging, filepath.Join(root, "auth")); err != nil {
		t.Skipf("cross-dir rename unavailable: %v", err)
	}

	if !waitForSwap(t, swaps, 5*time.Second) {
		t.Fatal("no snapshot publish after moving a directory in")
	}
	if !containsFile(wi.Load(), "auth/token.py") {
		t.Errorf("moved-in auth/token.py not indexed; chunks: %s", chunkFiles(wi.Load()))
	}
	if len(wi.Search("mint_token", 5)) == 0 {
		t.Error("Search('mint_token') found nothing — moved-in package is invisible")
	}
}

// TestWatchedIndex_DirectoryRename_RepathsSubtree is the audit §14 regression:
// renaming a directory (MOVED_FROM old + MOVED_TO new, no per-file events)
// must tombstone the whole old subtree and index the new one.
func TestWatchedIndex_DirectoryRename_RepathsSubtree(t *testing.T) {
	root := makeTempRepo(t, map[string]string{
		"auth/session.py": "def open_session():\n    return 's'\n",
		"main.py":         "def main():\n    return 1\n",
	})
	wi := withShortDebounce(t, root, true)
	if !containsFile(wi.Load(), "auth/session.py") {
		t.Fatalf("precondition: auth/session.py should be indexed initially")
	}
	swaps := make(chan struct{}, 8)
	wi.SetOnSwap(swaps)
	drainSwaps(swaps)

	if err := os.Rename(filepath.Join(root, "auth"), filepath.Join(root, "authz")); err != nil {
		t.Fatal(err)
	}

	// Wait until the re-path lands (poll a few swaps — MOVED_FROM/TO may arrive
	// in separate debounce windows).
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		waitForSwap(t, swaps, time.Second)
		got := wi.Load()
		if !containsFile(got, "auth/session.py") && containsFile(got, "authz/session.py") {
			return // re-pathed
		}
	}
	got := wi.Load()
	if containsFile(got, "auth/session.py") {
		t.Errorf("stale old path auth/session.py still indexed after dir rename; chunks: %s", chunkFiles(got))
	}
	if !containsFile(got, "authz/session.py") {
		t.Errorf("new path authz/session.py not indexed after dir rename; chunks: %s", chunkFiles(got))
	}
}

// TestWatchedIndex_FullResync_RecoversFromDroppedEvents is the audit §3
// regression: after an fsnotify overflow drops events, enqueueFullResync
// re-derives disk ground truth — new/modified files get Write in `dirty`, and
// the returned `present` set drives the flush's deletion pass (audit R4-6) so
// vanished files are tombstoned — restoring a correct index.
func TestWatchedIndex_FullResync_RecoversFromDroppedEvents(t *testing.T) {
	root := makeTempRepo(t, map[string]string{
		"a.py": "def alpha():\n    return 1\n",
		"b.py": "def beta():\n    return 2\n",
	})
	wi := withShortDebounce(t, root, false) // no watcher; drive resync+flush directly
	if !containsFile(wi.Load(), "a.py") || !containsFile(wi.Load(), "b.py") {
		t.Fatal("precondition: a.py and b.py should be indexed")
	}

	// Simulate the changes whose events the overflow dropped: delete b.py,
	// add c.py, modify a.py.
	if err := os.Remove(filepath.Join(root, "b.py")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "c.py"), []byte("def gamma():\n    return 3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.py"), []byte("def alpha_v2():\n    return 11\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	dirty := map[string]fsnotify.Op{}
	present := wi.enqueueFullResync(dirty)
	if dirty["a.py"]&fsnotify.Write == 0 {
		t.Errorf("a.py (modified) should be enqueued Write; got %v", dirty["a.py"])
	}
	if dirty["c.py"]&fsnotify.Write == 0 {
		t.Errorf("c.py (new) should be enqueued Write; got %v", dirty["c.py"])
	}
	// Deletion is no longer enqueued as a Remove op (audit R4-6): b.py is simply
	// absent from disk truth, and the flush's resync pass tombstones it.
	if present["b.py"] {
		t.Errorf("b.py (deleted) should be absent from the disk-present set; got present")
	}
	if !present["a.py"] || !present["c.py"] {
		t.Errorf("a.py and c.py should be in the disk-present set; got %v", present)
	}

	wi.flush(dirty, present)
	got := wi.Load()
	if containsFile(got, "b.py") {
		t.Errorf("b.py still indexed after resync; chunks: %s", chunkFiles(got))
	}
	if !containsFile(got, "c.py") {
		t.Errorf("c.py not indexed after resync; chunks: %s", chunkFiles(got))
	}
	if len(wi.Search("alpha_v2", 5)) == 0 {
		t.Error("modified a.py content (alpha_v2) not searchable after resync")
	}
}

// TestWatchedIndex_Flush_NoOpDoesNotPublish is the audit R4-3 regression guard
// (flagged in round 5 as having no test): a batch that changes nothing — a stray
// Remove of a never-indexed path, e.g. a gitignored .swp inside a watched dir —
// must NOT rebuild + publish + notify, because in ken-mcp that tail is a
// whole-repo WalkFS + serialize + two fsync'd writes + FreeOSMemory. Driven via
// direct flush() calls for determinism (no fsnotify timing). Bites: with the
// `if !changed && compacted == 0 { return }` short-circuit removed, the no-op
// flush Stores a fresh index and notifies swap, tripping both assertions.
func TestWatchedIndex_Flush_NoOpDoesNotPublish(t *testing.T) {
	root := makeTempRepo(t, map[string]string{"a.py": "def a():\n    return 1\n"})
	wi := withShortDebounce(t, root, false) // no watcher; drive flush directly
	before := wi.Load()
	swaps := make(chan struct{}, 4)
	wi.SetOnSwap(swaps)

	// No-op: Remove of a path with no chunks → changed=false → short-circuit.
	wi.flush(map[string]fsnotify.Op{"ghost.swp": fsnotify.Remove}, nil)
	select {
	case <-swaps:
		t.Error("no-op flush notified swap — the R4-3 changed short-circuit is missing")
	default:
	}
	if wi.Load() != before {
		t.Error("no-op flush replaced the published index pointer")
	}

	// A real change still publishes.
	wi.flush(map[string]fsnotify.Op{"a.py": fsnotify.Write}, nil)
	select {
	case <-swaps:
	default:
		t.Error("real-change flush did not notify swap")
	}
	if wi.Load() == before {
		t.Error("real-change flush did not replace the published index")
	}
}

// TestWatchedIndex_AsyncFlush_BackToBack exercises the audit §12 async
// single-flight flush across multiple flush cycles: sequential writes each
// trigger their own flush (via the flushing→flushDone→re-arm path) and all
// accumulate correctly, with the event loop never wedged.
func TestWatchedIndex_AsyncFlush_BackToBack(t *testing.T) {
	root := makeTempRepo(t, map[string]string{
		"base.py": "def base():\n    return 0\n",
	})
	wi := withShortDebounce(t, root, true)
	swaps := make(chan struct{}, 16)
	wi.SetOnSwap(swaps)
	drainSwaps(swaps)

	for i := 0; i < 4; i++ {
		name := filepath.Join(root, "gen"+string(rune('A'+i))+".py")
		body := "def gen" + string(rune('A'+i)) + "():\n    return " + string(rune('0'+i)) + "\n"
		if err := os.WriteFile(name, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if !waitForSwap(t, swaps, 5*time.Second) {
			t.Fatalf("no swap after write %d", i)
		}
	}

	got := wi.Load()
	// base + all 4 generated files must be present — nothing dropped by the
	// async flush cycles.
	for _, want := range []string{"base.py", "genA.py", "genB.py", "genC.py", "genD.py"} {
		if !containsFile(got, want) {
			t.Errorf("%s missing after back-to-back async flushes; chunks: %s", want, chunkFiles(got))
		}
	}
}

// TestWatchedIndex_DebounceMaxDelay_NoStarvation is the audit §13
// regression: continuous churn (writes faster than the debounce) must still
// flush within the maxDebounce ceiling (5× debounce) instead of resetting
// the timer forever.
func TestWatchedIndex_DebounceMaxDelay_NoStarvation(t *testing.T) {
	root := makeTempRepo(t, map[string]string{"a.py": "def a():\n    return 0\n"})
	wi := withShortDebounce(t, root, true)
	swaps := make(chan struct{}, 32)
	wi.SetOnSwap(swaps)
	drainSwaps(swaps)

	// Write a churn file more often than the debounce (60ms) for longer than
	// the maxDebounce ceiling (5×60ms=300ms). Without the ceiling the timer
	// would reset forever and no swap would fire.
	stop := make(chan struct{})
	go func() {
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
				_ = os.WriteFile(filepath.Join(root, "churn.py"),
					[]byte("def churn():\n    return "+string(rune('0'+i%10))+"\n"), 0o644)
				i++
				time.Sleep(shortDebounce / 3) // faster than the debounce
			}
		}
	}()
	defer close(stop)

	// A swap MUST occur within a few maxDebounce windows despite the churn.
	if !waitForSwap(t, swaps, 3*time.Second) {
		t.Fatal("no flush within the maxDebounce ceiling — continuous churn starved the reindex (§13)")
	}
}

// TestWatchedIndex_OversizedSinceEvent_Skipped is the audit §15 regression:
// a file that passes admission at event time but has grown past the size cap
// by flush time must be skipped, not read wholesale.
func TestWatchedIndex_OversizedSinceEvent_Skipped(t *testing.T) {
	root := makeTempRepo(t, map[string]string{"keep.py": "def keep():\n    return 1\n"})
	wi := withShortDebounce(t, root, false) // drive appendFile via ReconcileFiles

	// Write a file larger than the default 2 MiB cap, then reconcile it.
	big := filepath.Join(root, "huge.py")
	blob := make([]byte, (2<<20)+1024)
	for i := range blob {
		blob[i] = 'a'
	}
	if err := os.WriteFile(big, blob, 0o644); err != nil {
		t.Fatal(err)
	}
	wi.ReconcileFiles([]string{"huge.py"}, nil)

	if containsFile(wi.Load(), "huge.py") {
		t.Errorf("oversized huge.py was indexed on the watch path; §15 size guard not enforced")
	}
}

// TestWatchedIndex_SlowFlush_EventsDrainAndConverge is the composition test
// the 2026-07-26 re-audit called the single highest-value fixture — and it
// now actually BITES (round-3 test-quality finding: the old version passed
// even with R1 reverted, because it asserted only eventual convergence). It
// blocks OnFlush far past the debounce ceiling with events arriving during
// it, and asserts BOTH: (a) the loop's timer-fire count stays tiny — R1's
// busy-spin would push it into the thousands+ — and (b) the corpus converges
// (R8: the loop keeps draining while the flush goroutine is busy; flush
// unlocks before OnFlush).
func TestWatchedIndex_SlowFlush_EventsDrainAndConverge(t *testing.T) {
	root := makeTempRepo(t, map[string]string{
		"base.py": "def base():\n    return 0\n",
	})
	wi := withShortDebounce(t, root, true)

	var flushes atomic.Int64
	firstFlushDone := make(chan struct{})
	wi.SetOnFlush(func(string) {
		// Block the FIRST flush well past the maxDebounce ceiling
		// (5×debounce) so a pending event would trip R1's old spin.
		if flushes.Add(1) == 1 {
			time.Sleep(8 * shortDebounce)
			close(firstFlushDone)
		}
	})

	swaps := make(chan struct{}, 32)
	wi.SetOnSwap(swaps)
	drainSwaps(swaps)

	// Trigger flush 1 (its OnFlush will block).
	if err := os.WriteFile(filepath.Join(root, "f1.py"), []byte("def f1():\n    return 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !waitForSwap(t, swaps, 5*time.Second) {
		t.Fatal("flush 1 never published")
	}
	wakeupsBefore := wi.loopWakeups.Load()

	// While flush 1's OnFlush is still sleeping, add a pending event (this is
	// the past-the-ceiling-with-pending-work state R1 spun in).
	if err := os.WriteFile(filepath.Join(root, "f2.py"), []byte("def f2():\n    return 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Let the slow OnFlush run to completion (the whole ceiling-exceeded window).
	<-firstFlushDone

	// R1: across that multi-ceiling window the loop must NOT have spun. A
	// busy-spin fires the timer ~1.7M/s; a correct loop fires it a handful of
	// times. Generous bound catches the spin without flaking on scheduling.
	if spun := wi.loopWakeups.Load() - wakeupsBefore; spun > 200 {
		t.Errorf("loop woke %d times during a slow flush — busy-spin (R1 regressed)", spun)
	}

	// R8/handoff: f1 + f2 both converge.
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		got := wi.Load()
		if containsFile(got, "f1.py") && containsFile(got, "f2.py") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	got := wi.Load()
	t.Errorf("did not converge: f1=%v f2=%v; chunks: %s",
		containsFile(got, "f1.py"), containsFile(got, "f2.py"), chunkFiles(got))
}

// TestWatchedIndex_DirRenameDuringFlush_Tombstoned is the audit N2 regression,
// rewritten in round 4 to actually bite (the original passed with N2 reverted —
// it indexed pkg/ at construction, so the rename always found it in the
// PUBLISHED snapshot and the old gate accepted it regardless). Two things make
// this version faithful:
//
//   - The dir's chunks are present in w.chunks but NOT yet published — the
//     in-flight window between a flush appending them and Store. We hit it
//     deterministically with a reconcile hook, rename the dir inside it, and
//     synchronize on loopRemoveDelivered (incremented at delivery, before the
//     accept gate) so the gate's verdict is pinned BEFORE this flush publishes.
//   - The indexed file has an UNRECOGNIZED extension (.xyz, line-chunked). The
//     old gate was `knownIndexedFile(rel) || isTrackedRel(rel)`, and
//     knownIndexedFile == chunk.Language(rel) != "" accepts any recognized
//     source path (a .py) on extension alone, regardless of publish state — so
//     a .py rename would NOT bite. Only a path where knownIndexedFile is false
//     (a directory, or an unrecognized extension) exercises the isTrackedRel
//     branch that N2 removed. .xyz is false on every platform, whether fsnotify
//     delivers the rename as the dir "pkg" or as the file "pkg/data.xyz".
//
// Verified RED with the `knownIndexedFile(rel) || isTrackedRel(rel)` gate
// restored around the Remove/Rename accept: the rename is dropped while pkg/ is
// unpublished and pkg/data.xyz is orphaned.
func TestWatchedIndex_DirRenameDuringFlush_Tombstoned(t *testing.T) {
	root := makeTempRepo(t, map[string]string{"keep.py": "def keep():\n    return 0\n"})
	wi := withShortDebounce(t, root, true)
	// pkg/ is deliberately NOT present at construction.
	if containsFile(wi.Load(), "pkg/data.xyz") {
		t.Fatal("precondition: pkg/data.xyz must NOT be indexed yet")
	}

	// The hook fires inside the flush that first appends pkg/data.xyz — pkg/ is
	// in w.chunks but not published. Rename the dir there and wait until the loop
	// has DELIVERED the rename (before the accept gate), so the gate's verdict is
	// fixed before this flush publishes.
	var hookErr error
	hookDone := make(chan struct{})
	wi.setReconcileHook(func(batch map[string]fsnotify.Op) bool {
		// Only act on the flush that actually appended pkg/ (round-5 robustness
		// note): a platform that emits startup Create events could otherwise
		// consume a one-shot hook on an earlier flush, and os.Rename would then
		// ENOENT. Stay installed until our batch arrives.
		ours := false
		for k := range batch {
			if k == "pkg" || strings.HasPrefix(k, "pkg/") {
				ours = true
				break
			}
		}
		if !ours {
			return false
		}
		base := wi.loopRemoveDelivered.Load()
		if err := os.Rename(filepath.Join(root, "pkg"), filepath.Join(root, "pkg2")); err != nil {
			hookErr = err
			close(hookDone)
			return true
		}
		deadline := time.Now().Add(3 * time.Second)
		for wi.loopRemoveDelivered.Load() == base {
			if time.Now().After(deadline) {
				break // safety net; the assertion below still catches a miss
			}
			time.Sleep(time.Millisecond)
		}
		close(hookDone)
		return true
	})

	// Create pkg/data.xyz → triggers the flush whose reconcile fires the hook.
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pkg", "data.xyz"), []byte("alpha beta gamma delta\nepsilon zeta\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	select {
	case <-hookDone:
	case <-time.After(10 * time.Second):
		t.Fatal("reconcile hook never fired — pkg/ was not appended by a flush")
	}
	if hookErr != nil {
		t.Fatalf("rename inside hook: %v", hookErr)
	}

	// The renamed-away dir's chunks must be tombstoned (not orphaned), and the
	// new path picked up.
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if !containsFile(wi.Load(), "pkg/data.xyz") {
			return // old path gone — N2 holds
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("pkg/data.xyz still indexed after the dir was renamed during its in-flight flush (N2); chunks: %s", chunkFiles(wi.Load()))
}
