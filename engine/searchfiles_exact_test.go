package engine_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/townsendmerino/ken/engine"
)

// pastCapFiles is more source files than SearchFiles' old 5,000-candidate
// cap, so the instruction file's one hit ranks below it in the whole
// ranking. The shape of tack's tools/searchfilescheck TestCheckFailsPastTheCap.
const pastCapFiles = 5100

// writePastCapRepo writes pastCapFiles short Go files that each repeat the
// query word, and a GUIDE.md that names it once inside a long section: every
// Go chunk outscores the guide's in BM25.
func writePastCapRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for i := range pastCapFiles {
		src := fmt.Sprintf("package p%d\n\n// frobnicate frobnicate frobnicate\nfunc F%d() {}\n", i, i)
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%04d.go", i)), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	filler := strings.Repeat("Unrelated words about setup, layout, review and release notes. ", 40)
	guide := "# Guide\n\n## Notes\n\n" + filler + "Then frobnicate once. " + filler + "\n\n## Other\n\nNothing to see here.\n"
	if err := os.WriteFile(filepath.Join(dir, "GUIDE.md"), []byte(guide), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// wholeRankingFiltered is SearchFiles' contract spelled out: the whole
// ranking (k past the index size) filtered to files, truncated to k, plus
// the best whole-ranking rank any kept hit had.
func wholeRankingFiltered(t *testing.T, ctx context.Context, ws *engine.Workspace, q string, k int, mode engine.Mode, file string) ([]engine.Hit, int) {
	t.Helper()
	all, _, err := ws.SearchMode(ctx, q, 4*pastCapFiles, mode)
	if err != nil {
		t.Fatalf("SearchMode: %v", err)
	}
	var out []engine.Hit
	best := 0
	for _, h := range all {
		if h.File != file {
			continue
		}
		if best == 0 {
			best = h.Rank
		}
		out = append(out, h)
		if len(out) == k {
			break
		}
	}
	return out, best
}

func sameHits(a, b []engine.Hit) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].File != b[i].File || a[i].StartLine != b[i].StartLine ||
			a[i].EndLine != b[i].EndLine || a[i].Score != b[i].Score {
			return false
		}
	}
	return true
}

func describe(hs []engine.Hit) string {
	var b strings.Builder
	for _, h := range hs {
		fmt.Fprintf(&b, "  %s:%d-%d score=%v\n", h.File, h.StartLine, h.EndLine, h.Score)
	}
	return b.String()
}

func checkPastCap(t *testing.T, ws *engine.Workspace, mode engine.Mode, needPastCap bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	const q = "frobnicate"
	for _, k := range []int{1, 5, 20} {
		want, best := wholeRankingFiltered(t, ctx, ws, q, k, mode, "GUIDE.md")
		if len(want) == 0 {
			t.Fatalf("mode %v k=%d: the whole ranking has no GUIDE.md hit; fixture is broken", mode, k)
		}
		if needPastCap && best <= 5000 {
			t.Fatalf("mode %v: GUIDE.md's best whole-ranking rank is %d, not past the old 5,000 cap; fixture is too weak", mode, best)
		}
		got, _, err := ws.SearchFiles(ctx, q, k, mode, []string{"GUIDE.md"})
		if err != nil {
			t.Fatalf("SearchFiles: %v", err)
		}
		if !sameHits(got, want) {
			t.Errorf("mode %v k=%d: SearchFiles differs from the whole ranking filtered to GUIDE.md (best rank %d)\n got:\n%s want:\n%s",
				mode, k, best, describe(got), describe(want))
		}
		for i, h := range got {
			if h.Rank != i+1 {
				t.Errorf("mode %v k=%d: hit %d has Rank %d", mode, k, i, h.Rank)
			}
		}
	}
}

// TestSearchFiles_ExactPastTheCap fails on the capped SearchFiles: the
// guide's only hit ranks below 5,000 in the whole BM25 ranking, so filtering
// a 5,000-candidate list returns nothing.
func TestSearchFiles_ExactPastTheCap(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a 5,101-file index")
	}
	dir := writePastCapRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	ws, err := engine.Open(ctx, dir, engine.Options{Mode: "bm25", DisableEnrichment: true})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = ws.Close() }()
	checkPastCap(t, ws, engine.ModeBM25, true)
}

// TestSearchFiles_ExactPastTheCapDense is the same check for the modes with
// a dense arm. Skipped without testdata/model.
func TestSearchFiles_ExactPastTheCapDense(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a 5,101-file index")
	}
	modelDir := modelDirForTest(t)
	dir := writePastCapRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	ws, err := engine.Open(ctx, dir, engine.Options{Mode: "hybrid", ModelDir: modelDir, DisableEnrichment: true})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = ws.Close() }()
	checkPastCap(t, ws, engine.ModeSemantic, false)
	checkPastCap(t, ws, engine.ModeHybrid, false)
	checkPastCap(t, ws, engine.ModeBM25, true)
}
