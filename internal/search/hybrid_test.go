package search

import (
	"testing"

	"github.com/townsendmerino/aikit/ann"
	"github.com/townsendmerino/aikit/bm25"
	"github.com/townsendmerino/aikit/chunk"
)

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

	ranked := hybridSearch("getUser", q, flat, bm, chunks, 5, AdaptiveAlphas, nil, RankAblation{}) // symbol ⇒ alpha 0.3, penalties on
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

func TestHybridSearch_RankAblation(t *testing.T) {
	chunks := []chunk.Chunk{
		{File: "user.go", Text: "func getUser() string { return userName }", StartLine: 1, EndLine: 1},
		{File: "user.go", Text: "func setUser(n string) { userName = n }", StartLine: 3, EndLine: 3},
		{File: "notes.md", Text: "getUser appears in notes about the user model", StartLine: 1, EndLine: 1},
	}
	docs := make([][]string, len(chunks))
	for i, c := range chunks {
		docs[i] = bm25.Tokenize(c.Text)
	}
	bm := bm25.Build(docs)
	flat := ann.New([][]float32{{1, 0}, {0.8, 0.6}, {0, 1}})
	q := []float32{1, 0}
	scoreOf := func(abl RankAblation) map[int]float64 {
		m := map[int]float64{}
		for _, r := range hybridSearch("getUser", q, flat, bm, chunks, 5, AdaptiveAlphas, nil, abl) {
			m[r.idx] = r.score
		}
		return m
	}
	shipped := scoreOf(RankAblation{})
	if again := scoreOf(RankAblation{}); len(again) != len(shipped) {
		t.Fatalf("zero RankAblation not stable")
	}
	noCoh := scoreOf(RankAblation{NoCoherenceBoost: true})
	if noCoh[0] >= shipped[0] {
		t.Errorf("coherence boost off: user.go best chunk %v, want below shipped %v", noCoh[0], shipped[0])
	}
	noSat := scoreOf(RankAblation{NoFileSaturation: true})
	if noSat[1] <= shipped[1] {
		t.Errorf("saturation off: user.go second chunk %v, want above shipped %v", noSat[1], shipped[1])
	}
}
