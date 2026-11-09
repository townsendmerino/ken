package engine_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/townsendmerino/ken/engine"
)

// pastCapFiles is more source files than SearchFiles' old 5,000-candidate
// cap, so the instruction file's one hit ranks below it in the whole
// ranking. The shape of tack's tools/searchfilescheck TestCheckFailsPastTheCap.
const pastCapFiles = 5300

// writePastCapRepo writes pastCapFiles short Go files that each repeat the
// query word, and a GUIDE.md that names it once inside one long section of
// unrelated words. Every Go chunk outscores the guide's in both arms, so in
// bm25, semantic and hybrid every guide hit ranks past 5,000 — all of them
// outside the old capped candidate list.
func writePastCapRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for i := range pastCapFiles {
		src := fmt.Sprintf("package p%d\n\n// frobnicate frobnicate frobnicate\nfunc F%d() {}\n", i, i)
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%04d.go", i)), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	filler := strings.Repeat("Unrelated words about setup, layout, review and release notes.\n", 40)
	guide := "## Notes\n\n" + filler + "Then frobnicate once.\n" + filler + "\n"
	if err := os.WriteFile(filepath.Join(dir, "GUIDE.md"), []byte(guide), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// wholeRankingFiltered is SearchFiles' contract spelled out: the whole
// ranking (k past the index size) filtered to files, truncated to k. At
// that k, SearchMode's per-arm rank lookups (armOverfetch(k) deep) cover the
// whole index, so the reference hits carry exact arm ranks too.
func wholeRankingFiltered(t *testing.T, ctx context.Context, ws *engine.Workspace, q string, k int, mode engine.Mode, file string) ([]engine.Hit, engine.Mode) {
	t.Helper()
	all, eff, err := ws.SearchMode(ctx, q, 4*pastCapFiles, mode)
	if err != nil {
		t.Fatalf("SearchMode: %v", err)
	}
	var out []engine.Hit
	for _, h := range all {
		if h.File != file {
			continue
		}
		out = append(out, h)
		if len(out) == k {
			break
		}
	}
	return out, eff
}

// diffHit names the first Hit field where got differs from want, the
// whole-ranking hit it should equal (Rank aside: got's is its position in
// the filtered list).
func diffHit(got, want engine.Hit, pos int) string {
	switch {
	case got.File != want.File || got.StartLine != want.StartLine || got.EndLine != want.EndLine:
		return fmt.Sprintf("span %s:%d-%d, want %s:%d-%d", got.File, got.StartLine, got.EndLine, want.File, want.StartLine, want.EndLine)
	case got.Score != want.Score:
		return fmt.Sprintf("Score %v, want %v", got.Score, want.Score)
	case got.Text != want.Text:
		return "Text differs"
	case got.Hash != want.Hash:
		return "Hash differs"
	case got.Gen != want.Gen:
		return fmt.Sprintf("Gen %d, want %d", got.Gen, want.Gen)
	case got.BM25Rank != want.BM25Rank || got.DenseRank != want.DenseRank:
		return fmt.Sprintf("BM25Rank/DenseRank %d/%d, want %d/%d", got.BM25Rank, got.DenseRank, want.BM25Rank, want.DenseRank)
	case !slices.Equal(got.HeadingPath, want.HeadingPath):
		return fmt.Sprintf("HeadingPath %q, want %q", got.HeadingPath, want.HeadingPath)
	case got.Rank != pos+1:
		return fmt.Sprintf("Rank %d, want %d", got.Rank, pos+1)
	}
	return ""
}

func describe(hs []engine.Hit) string {
	var b strings.Builder
	for _, h := range hs {
		fmt.Fprintf(&b, "  %s:%d-%d score=%v whole-rank=%d\n", h.File, h.StartLine, h.EndLine, h.Score, h.Rank)
	}
	return b.String()
}

// checkPastCap requires every GUIDE.md hit in the whole ranking to rank past
// the old 5,000 cap (so the capped SearchFiles returned none of them), then
// requires SearchFiles to return exactly those hits, field for field.
func checkPastCap(t *testing.T, ws *engine.Workspace, mode engine.Mode) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	const q = "frobnicate"
	for _, k := range []int{1, 5, 20} {
		want, wantEff := wholeRankingFiltered(t, ctx, ws, q, k, mode, "GUIDE.md")
		if len(want) == 0 {
			t.Fatalf("mode %v k=%d: the whole ranking has no GUIDE.md hit; fixture is broken", mode, k)
		}
		for _, h := range want {
			if h.Rank <= 5000 {
				t.Fatalf("mode %v: a GUIDE.md hit ranks %d in the whole ranking, inside the old 5,000 cap; fixture is too weak\n%s", mode, h.Rank, describe(want))
			}
		}
		if k == 20 {
			t.Logf("mode %v: GUIDE.md's whole-ranking hits:\n%s", mode, describe(want))
		}
		if wantEff != mode {
			t.Fatalf("mode %v: SearchMode ran as %v; fixture lacks a retriever", mode, wantEff)
		}
		got, eff, err := ws.SearchFiles(ctx, q, k, mode, []string{"GUIDE.md"})
		if err != nil {
			t.Fatalf("SearchFiles: %v", err)
		}
		if eff != wantEff {
			t.Errorf("mode %v k=%d: SearchFiles ran as %v, want %v", mode, k, eff, wantEff)
		}
		if len(got) != len(want) {
			t.Errorf("mode %v k=%d: SearchFiles returned %d hits, the whole ranking filtered has %d\n got:\n%s want:\n%s",
				mode, k, len(got), len(want), describe(got), describe(want))
			continue
		}
		for i := range got {
			if d := diffHit(got[i], want[i], i); d != "" {
				t.Errorf("mode %v k=%d hit %d: %s", mode, k, i, d)
			}
		}
	}
}

// TestSearchFiles_ExactPastTheCap fails on the capped SearchFiles: the
// guide's hit ranks below 5,000 in the whole BM25 ranking, so filtering a
// 5,000-candidate list returns nothing.
func TestSearchFiles_ExactPastTheCap(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a 5,301-file index")
	}
	dir := writePastCapRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	ws, err := engine.Open(ctx, dir, engine.Options{Mode: "bm25", DisableEnrichment: true})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = ws.Close() }()
	checkPastCap(t, ws, engine.ModeBM25)
}

// TestSearchFiles_ExactPastTheCapDense is the same check for the modes with
// a dense arm. Skipped without testdata/model.
func TestSearchFiles_ExactPastTheCapDense(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a 5,301-file index")
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
	checkPastCap(t, ws, engine.ModeSemantic)
	checkPastCap(t, ws, engine.ModeHybrid)
	checkPastCap(t, ws, engine.ModeBM25)
}
