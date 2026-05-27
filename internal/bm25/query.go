package bm25

import (
	"math"
	"sort"

	"github.com/townsendmerino/ken/internal/topk"
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
//
// Two paths by design:
//
//   - k<0: full sort over every scoring doc. Preserves the "no truncation"
//     escape hatch the original `if k >= 0 && k < len(res)` gate exposed
//     to callers that want every positive-scored document.
//   - k>=0: min-heap of size K via internal/topk. O(N log K) vs the
//     full-sort path's O(N log N). At medium scale (~378k chunks, k=10)
//     this was 36% of bm25 search CPU per ADR-025. Final sort.SliceStable
//     imposes the ascending-Doc tie-break the doc comment promises,
//     which the heap on its own doesn't guarantee. K-sized stable sort
//     is O(K log K) — cheap at K=10. k=0 returns an empty slice (topk
//     with cap 0 always discards), matching the prior `k=0 → empty`
//     behavior from the original truncation gate.
func (ix *Index) TopK(query []string, k int) []Result {
	scores := ix.Scores(query)

	// Full-sort path: k<0 means "no truncation, return everything".
	if k < 0 {
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
		return res
	}

	// Heap path: k>=0. Push every positive-scored doc; the heap retains
	// the K highest. k=0 selector discards everything → empty result,
	// matching the prior gate's k=0 behavior.
	sel := topk.New[int](k)
	for d, s := range scores {
		if s > 0 {
			sel.Push(d, s)
		}
	}
	items := sel.Result()
	// Stable secondary sort by ascending Doc id to honor the doc-comment
	// tie-break contract.
	sort.SliceStable(items, func(a, b int) bool {
		if items[a].Score != items[b].Score {
			return items[a].Score > items[b].Score
		}
		return items[a].Item < items[b].Item
	})
	out := make([]Result, len(items))
	for j, s := range items {
		out[j] = Result{Doc: s.Item, Score: s.Score}
	}
	return out
}
