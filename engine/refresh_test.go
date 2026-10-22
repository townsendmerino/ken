package engine_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/townsendmerino/ken/engine"
	"github.com/townsendmerino/ken/internal/search"
)

// K13: a structural index that follows edits.

const pullTypes = `package pull

type Model struct{ n int }

func Select(m Model) int { return m.n }
`

const pullMethods = `package pull

func (m Model) Size() int {
	return m.n
}
`

// pullMethodsShifted is pullMethods with three comment lines inserted
// above Size, which moves it from line 3 to line 6.
const pullMethodsShifted = `package pull

// one
// two
// three
func (m Model) Size() int {
	return m.n + uniqueShiftedToken()
}
`

func writePull(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "pull"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"types.go": pullTypes, "methods.go": pullMethods} {
		if err := os.WriteFile(filepath.Join(dir, "pull", name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func sizeSpan(t *testing.T, ws *engine.Workspace, ctx context.Context) (int, int) {
	t.Helper()
	sites, err := ws.Definition(ctx, "Size")
	if err != nil {
		t.Fatalf("Definition(Size): %v", err)
	}
	if len(sites) != 1 {
		t.Fatalf("Definition(Size) = %+v, want one site", sites)
	}
	return sites[0].Line, sites[0].EndLine
}

func outlineSizeSpan(t *testing.T, ws *engine.Workspace, ctx context.Context) (int, int) {
	t.Helper()
	outs, err := ws.Outline(ctx, "pull/methods.go")
	if err != nil || len(outs) != 1 {
		t.Fatalf("Outline = %v, %v", outs, err)
	}
	for _, e := range outs[0].Entries {
		if e.Name == "Size" {
			return e.StartLine, e.EndLine
		}
	}
	t.Fatalf("Outline has no Size: %+v", outs[0].Entries)
	return 0, 0
}

func TestRefreshMovesSpansWatchOff(t *testing.T) {
	dir := writePull(t)
	ws, ctx := openBM25(t, dir, false)

	if s, e := sizeSpan(t, ws, ctx); s != 3 || e != 5 {
		t.Fatalf("before: Size at %d-%d, want 3-5", s, e)
	}
	st0 := ws.Status()

	if err := os.WriteFile(filepath.Join(dir, "pull", "methods.go"), []byte(pullMethodsShifted), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ws.Refresh(ctx, []string{"pull/methods.go"}, nil); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	if s, e := sizeSpan(t, ws, ctx); s != 6 || e != 8 {
		t.Errorf("after Refresh: Definition(Size) at %d-%d, want 6-8", s, e)
	}
	if s, e := outlineSizeSpan(t, ws, ctx); s != 6 || e != 8 {
		t.Errorf("after Refresh: Outline Size at %d-%d, want 6-8", s, e)
	}
	hits, err := ws.Search(ctx, "uniqueShiftedToken", 5)
	if err != nil || len(hits) == 0 || !strings.Contains(hits[0].Text, "uniqueShiftedToken") {
		t.Errorf("after Refresh: Search found %+v, %v; want the new text", hits, err)
	}
	st := ws.Status()
	if st.Generation <= st0.Generation {
		t.Errorf("Generation %d -> %d, want it to go up", st0.Generation, st.Generation)
	}
	if st.StructuralGeneration <= st0.StructuralGeneration {
		t.Errorf("StructuralGeneration %d -> %d, want it to go up", st0.StructuralGeneration, st.StructuralGeneration)
	}
}

func TestWatcherMovesSpans(t *testing.T) {
	dir := writePull(t)
	ws, ctx := openBM25(t, dir, true)

	if s, _ := sizeSpan(t, ws, ctx); s != 3 {
		t.Fatalf("before: Size at %d, want 3", s)
	}
	sgen := ws.Status().StructuralGeneration
	if err := os.WriteFile(filepath.Join(dir, "pull", "methods.go"), []byte(pullMethodsShifted), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for ws.Status().StructuralGeneration == sgen {
		if time.Now().After(deadline) {
			t.Fatal("the watcher never updated the structural index")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if s, e := sizeSpan(t, ws, ctx); s != 6 || e != 8 {
		t.Errorf("after the watcher's flush: Size at %d-%d, want 6-8", s, e)
	}
	if s, _ := outlineSizeSpan(t, ws, ctx); s != 6 {
		t.Errorf("after the watcher's flush: Outline Size at %d, want 6", s)
	}
}

func TestRefreshDeletedFile(t *testing.T) {
	dir := writePull(t)
	ws, ctx := openBM25(t, dir, false)
	sizeSpan(t, ws, ctx) // builds the structural index

	if err := os.Remove(filepath.Join(dir, "pull", "methods.go")); err != nil {
		t.Fatal(err)
	}
	if err := ws.Refresh(ctx, nil, []string{"pull/methods.go"}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if sites, err := ws.Definition(ctx, "Size"); err != nil || len(sites) != 0 {
		t.Errorf("Definition(Size) after delete = %+v, %v; want none", sites, err)
	}
	if outs, err := ws.Outline(ctx, "pull/methods.go"); err != nil || len(outs) != 0 {
		t.Errorf("Outline(methods.go) after delete = %+v, %v; want none", outs, err)
	}
	hits, err := ws.Search(ctx, "Size", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		if h.File == "pull/methods.go" {
			t.Errorf("Search still returns the deleted file: %+v", h)
		}
	}
}

func TestRefreshRejectsEscapingPaths(t *testing.T) {
	dir := writePull(t)
	ws, ctx := openBM25(t, dir, false)
	sizeSpan(t, ws, ctx)
	// A real edit alongside the bad path: if Refresh read anything before
	// rejecting, it would pick this up.
	if err := os.WriteFile(filepath.Join(dir, "pull", "methods.go"), []byte(pullMethodsShifted), 0o644); err != nil {
		t.Fatal(err)
	}
	st0 := ws.Status()
	for _, bad := range []string{"../outside.go", "pull/../../outside.go", filepath.Join(dir, "pull", "types.go"), "..", "."} {
		err := ws.Refresh(ctx, []string{"pull/methods.go", bad}, nil)
		if !errors.Is(err, engine.ErrPathOutsideRoot) {
			t.Errorf("Refresh(%q) = %v, want ErrPathOutsideRoot", bad, err)
		}
	}
	if st := ws.Status(); st.Generation != st0.Generation || st.StructuralGeneration != st0.StructuralGeneration {
		t.Errorf("a rejected Refresh changed the indexes: %+v -> %+v", st0, st)
	}
	if s, _ := sizeSpan(t, ws, ctx); s != 3 {
		t.Errorf("a rejected Refresh read methods.go: Size at %d, want still 3", s)
	}
}

func TestRefreshDoesNotStartStructuralBuild(t *testing.T) {
	dir := writePull(t)
	ws, ctx := openBM25(t, dir, false)
	if err := os.WriteFile(filepath.Join(dir, "pull", "methods.go"), []byte(pullMethodsShifted), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ws.Refresh(ctx, []string{"pull/methods.go"}, nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond) // long enough for a stray build of two files
	if ws.StructuralIfBuilt() != nil || ws.Status().StructuralBuilt {
		t.Fatal("Refresh started a structural build")
	}
	// The lazy build then reads the current files.
	if s, _ := sizeSpan(t, ws, ctx); s != 6 {
		t.Errorf("first structural call after Refresh: Size at %d, want 6", s)
	}
}

// TestRefreshConcurrentReaders runs readers over every structural method
// and Search while Refresh flips a file between two versions. Each read
// must see one version or the other, whole. Run under -race.
func TestRefreshConcurrentReaders(t *testing.T) {
	dir := writePull(t)
	ws, ctx := openBM25(t, dir, false)
	sizeSpan(t, ws, ctx)

	versions := []string{pullMethods, pullMethodsShifted}
	okSpan := func(s, e int) bool { return (s == 3 && e == 5) || (s == 6 && e == 8) }

	stop := make(chan struct{})
	var failures atomic.Int32
	var wg sync.WaitGroup
	fail := func(format string, args ...any) {
		if failures.Add(1) <= 5 {
			t.Errorf(format, args...)
		}
	}
	for range 4 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				sites, err := ws.Definition(ctx, "Size")
				if err != nil || len(sites) != 1 || !okSpan(sites[0].Line, sites[0].EndLine) {
					fail("Definition(Size) mid-refresh = %+v, %v", sites, err)
				}
				outs, err := ws.Outline(ctx, "pull/methods.go")
				if err != nil || len(outs) != 1 {
					fail("Outline mid-refresh = %+v, %v", outs, err)
				} else {
					for _, e := range outs[0].Entries {
						if e.Name == "Size" && !okSpan(e.StartLine, e.EndLine) {
							fail("Outline Size mid-refresh at %d-%d", e.StartLine, e.EndLine)
						}
					}
				}
				if _, err := ws.Callers(ctx, "Size"); err != nil {
					fail("Callers mid-refresh: %v", err)
				}
				if _, err := ws.Search(ctx, "Size Model", 5); err != nil {
					fail("Search mid-refresh: %v", err)
				}
			}
		})
	}
	for i := range 40 {
		if err := os.WriteFile(filepath.Join(dir, "pull", "methods.go"), []byte(versions[i%2]), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := ws.Refresh(ctx, []string{"pull/methods.go"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
	if s, e := sizeSpan(t, ws, ctx); s != 6 || e != 8 {
		t.Errorf("after the last Refresh (shifted version): Size at %d-%d, want 6-8", s, e)
	}
}

func TestRefreshDefersSnapshotToClose(t *testing.T) {
	dir := writePull(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	ws, err := engine.Open(ctx, dir, engine.Options{Mode: "bm25", Snapshot: true})
	if err != nil {
		t.Fatal(err)
	}
	binPath := search.SnapshotBinPath(ws.Root())
	before, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatalf("Open with Snapshot wrote no snapshot: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "pull", "methods.go"), []byte(pullMethodsShifted), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ws.Refresh(ctx, []string{"pull/methods.go"}, nil); err != nil {
		t.Fatal(err)
	}
	if mid, _ := os.ReadFile(binPath); !bytes.Equal(mid, before) {
		t.Error("Refresh rewrote the snapshot; it should defer the write to Close")
	}
	if err := ws.Close(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(binPath)
	if bytes.Equal(after, before) {
		t.Fatal("Close didn't write the snapshot Refresh left behind")
	}

	ws2, err := engine.Open(ctx, dir, engine.Options{Mode: "bm25", Snapshot: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ws2.Close() }()
	hits, err := ws2.Search(ctx, "uniqueShiftedToken", 5)
	if err != nil || len(hits) == 0 {
		t.Errorf("reopened from the snapshot Close wrote: Search = %+v, %v; want the refreshed text", hits, err)
	}
}
