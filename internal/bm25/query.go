package bm25

import (
	"math"
	"sort"
)

// Result is one scored document, highest Score first.
type Result struct {
	Doc   int
	Score float64
}

// idf is the Lucene/bm25s BM25 IDF: ln(1 + (N - df + 0.5)/(df + 0.5)).
// The +1 inside the log keeps it non-negative even for terms in most docs,
// which is the variant bm25s uses by default.
func (ix *Index) idf(term string) float64 {
	df := ix.df[term]
	if df == 0 {
		return 0
	}
	n := float64(ix.N())
	return math.Log(1 + (n-float64(df)+0.5)/(float64(df)+0.5))
}

// Scores returns the BM25 score of every document for query (already
// tokenized). The result is indexed by document id; length == ix.N().
func (ix *Index) Scores(query []string) []float64 {
	scores := make([]float64, ix.N())
	seen := make(map[string]struct{}, len(query))
	for _, term := range query {
		if _, dup := seen[term]; dup {
			continue // term contributes once; tf is per-document, not per-query
		}
		seen[term] = struct{}{}
		idf := ix.idf(term)
		if idf == 0 {
			continue
		}
		for _, p := range ix.postings[term] {
			var norm float64
			if ix.avgdl > 0 {
				norm = float64(ix.docLen[p.doc]) / ix.avgdl
			}
			denom := float64(p.tf) + ix.K1*(1-ix.B+ix.B*norm)
			scores[p.doc] += idf * (float64(p.tf) * (ix.K1 + 1)) / denom
		}
	}
	return scores
}

// TopK returns the k highest-scoring documents with Score > 0, ties broken
// by ascending document id so results are deterministic.
func (ix *Index) TopK(query []string, k int) []Result {
	scores := ix.Scores(query)
	res := make([]Result, 0, len(scores))
	for d, s := range scores {
		if s > 0 {
			res = append(res, Result{Doc: d, Score: s})
		}
	}
	sort.Slice(res, func(i, j int) bool {
		if res[i].Score != res[j].Score {
			return res[i].Score > res[j].Score
		}
		return res[i].Doc < res[j].Doc
	})
	if k >= 0 && k < len(res) {
		res = res[:k]
	}
	return res
}
