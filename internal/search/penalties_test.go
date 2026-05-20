package search

import (
	"testing"

	"github.com/townsendmerino/ken/internal/chunk"
)

func TestFilePathPenalty(t *testing.T) {
	cases := map[string]float64{
		"pkg/auth.go":        1.0,                             // clean
		"foo_test.go":        strongPenalty,                   // test file
		"pkg/tests/x.go":     strongPenalty,                   // test dir
		"a/__init__.py":      moderatePenalty,                 // re-export barrel
		"compat/x.go":        strongPenalty,                   // compat dir
		"legacy/y.go":        strongPenalty,                   // legacy dir
		"examples/z.go":      strongPenalty,                   // examples dir
		"types/foo.d.ts":     mildPenalty,                     // .d.ts stub
		"tests/foo_test.go":  strongPenalty,                   // test file AND dir → single 0.3
		"legacy/__init__.py": strongPenalty * moderatePenalty, // 0.3 * 0.5
		"x.d.ts":             mildPenalty,
	}
	for p, want := range cases {
		if got := filePathPenalty(p); !approx(got, want) {
			t.Errorf("filePathPenalty(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestRerankTopK_SaturationDecay(t *testing.T) {
	chunks := []chunk.Chunk{
		mkChunk("a.go", "x"), mkChunk("a.go", "y"), mkChunk("a.go", "z"),
	}
	chunks[1].StartLine, chunks[1].EndLine = 2, 2
	chunks[2].StartLine, chunks[2].EndLine = 3, 3
	got := rerankTopK(map[int]float64{0: 1.0, 1: 1.0, 2: 1.0}, chunks, 5, false)
	// Same file: 1st keeps 1.0, 2nd ×0.5, 3rd ×0.25.
	want := []float64{1.0, 0.5, 0.25}
	if len(got) != 3 {
		t.Fatalf("got %d items, want 3", len(got))
	}
	for i, w := range want {
		if !approx(got[i].score, w) {
			t.Errorf("rank %d score = %v, want %v", i, got[i].score, w)
		}
	}
}

func TestRerankTopK_PenaltyGatingAndTopK(t *testing.T) {
	chunks := []chunk.Chunk{mkChunk("svc_test.go", "x"), mkChunk("svc.go", "y")}
	chunks[1].StartLine, chunks[1].EndLine = 2, 2

	// penalise on: test file drops to 0.3, so svc.go (1.0) ranks first.
	on := rerankTopK(map[int]float64{0: 1.0, 1: 1.0}, chunks, 5, true)
	if on[0].idx != 1 {
		t.Errorf("with penalties, top idx = %d, want 1 (test file penalised)", on[0].idx)
	}
	if !approx(on[1].score, strongPenalty) {
		t.Errorf("penalised test-file score = %v, want %v", on[1].score, strongPenalty)
	}
	// penalise off: tie, deterministic order by start line ⇒ idx 0 first.
	off := rerankTopK(map[int]float64{0: 1.0, 1: 1.0}, chunks, 5, false)
	if off[0].score != 1.0 || off[1].score != 1.0 {
		t.Errorf("penalties not gated off: %+v", off)
	}
	// topK truncation.
	if k := rerankTopK(map[int]float64{0: 1.0, 1: 1.0}, chunks, 1, false); len(k) != 1 {
		t.Errorf("topK=1 returned %d items", len(k))
	}
}
