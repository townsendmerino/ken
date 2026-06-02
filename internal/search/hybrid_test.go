package search

import (
	"testing"

	"github.com/townsendmerino/aikit/ann"
	"github.com/townsendmerino/aikit/bm25"
	"github.com/townsendmerino/aikit/chunk"
)

func TestRRFScores(t *testing.T) {
	got := rrfScores([]int{5, 2, 9}) // rank 1-indexed: 1/(60+rank)
	want := map[int]float64{5: 1.0 / 61, 2: 1.0 / 62, 9: 1.0 / 63}
	for k, w := range want {
		if !approx(got[k], w) {
			t.Errorf("rrf[%d] = %v, want %v", k, got[k], w)
		}
	}
	if rrfScores(nil) == nil {
		t.Error("rrfScores(nil) should return an empty (non-nil) map")
	}
}

func TestHybridSearch_EndToEnd(t *testing.T) {
	chunks := []chunk.Chunk{
		{File: "user.go", Text: "func getUser() string { return userName }", StartLine: 1, EndLine: 1},
		{File: "user_test.go", Text: "func TestGetUser() { getUser() }", StartLine: 1, EndLine: 1},
		{File: "notes.md", Text: "getUser appears in notes about the user model", StartLine: 1, EndLine: 1},
	}
	docs := make([][]string, len(chunks))
	for i, c := range chunks {
		docs[i] = bm25.Tokenize(c.Text)
	}
	bm := bm25.Build(docs)
	flat := ann.New([][]float32{{1, 0}, {0.8, 0.6}, {0, 1}})
	q := []float32{1, 0}

	ranked := hybridSearch("getUser", q, flat, bm, chunks, 5, -1, nil) // symbol ⇒ alpha 0.3, penalties on
	if len(ranked) == 0 {
		t.Fatal("hybridSearch returned nothing")
	}
	if ranked[0].idx != 0 {
		t.Errorf("top idx = %d, want 0 (user.go defines getUser)", ranked[0].idx)
	}
	pos := map[int]int{}
	for i, r := range ranked {
		pos[r.idx] = i
	}
	if p0, p1 := pos[0], pos[1]; p1 <= p0 {
		t.Errorf("user_test.go (pos %d) not ranked below user.go (pos %d) despite test penalty", p1, p0)
	}
}
