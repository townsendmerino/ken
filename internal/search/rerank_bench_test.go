package search

import (
	"fmt"
	"testing"

	"github.com/townsendmerino/aikit/chunk"
)

// buildRerankCorpus builds a synthetic chunks slice with realistic shape:
// `numFiles` files × `chunksPerFile` chunks each. The chunk text contains
// Go-like func/method definitions so the boost passes (which scan for
// definition keywords like `func`, `class`, `def`) hit realistic
// code paths.
func buildRerankCorpus(numFiles, chunksPerFile int) []chunk.Chunk {
	out := make([]chunk.Chunk, 0, numFiles*chunksPerFile)
	for f := 0; f < numFiles; f++ {
		file := fmt.Sprintf("pkg/sub%d/file%d.go", f%5, f)
		for c := 0; c < chunksPerFile; c++ {
			start := c*40 + 1
			text := fmt.Sprintf(`package sub%d

// Symbol%d_%d does something interesting.
func Symbol%d_%d(ctx Context, name string) (*Result, error) {
	if name == "" {
		return nil, ErrEmpty
	}
	return process(ctx, name)
}
`, f%5, f, c, f, c)
			out = append(out, chunk.Chunk{
				File:      file,
				StartLine: start,
				EndLine:   start + 8,
				Text:      text,
			})
		}
	}
	return out
}

// buildFusedScores synthesises a post-fusion candidate map with RRF-style
// scores (1/(k+rank), k=60). nCandidates is the count semble's hybrid
// produces — k=20 final × candidateCount=5×k = 100 candidates flowing
// into rerank.
func buildFusedScores(corpusLen, nCandidates int) map[int]float64 {
	if nCandidates > corpusLen {
		nCandidates = corpusLen
	}
	out := make(map[int]float64, nCandidates)
	for rank := 1; rank <= nCandidates; rank++ {
		out[rank-1] = 1.0 / float64(60+rank)
	}
	return out
}

// BenchmarkRerank exercises the boost + penalty + saturation pipeline that
// hybridSearch invokes after RRF fusion. Two query shapes — a symbol-like
// query (single identifier; isSymbolQuery → true; takes the
// boostSymbolDefinitions path) and a natural-language query (takes the
// boostStemMatches + boostEmbeddedSymbols path).
//
// Briefing prediction #7 says rerank is NOT a hotspot at k=20; this
// benchmark is its falsifier.
func BenchmarkRerank(b *testing.B) {
	chunks := buildRerankCorpus(20, 5) // 100 chunks total
	fused := buildFusedScores(len(chunks), 100)
	cases := []struct {
		name  string
		query string
	}{
		{"Symbol", "Symbol0_0"},
		{"NaturalLang", "build index from path"},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				boosted := applyQueryBoost(fused, tc.query, chunks)
				_ = rerankTopK(boosted, chunks, 20, true)
			}
		})
	}
}
