// Package ndcg is a tiny pure-Go NDCG@k implementation used by ken's
// external-reference benchmark harnesses (see coir_test.go, build tag
// `bench`). Deliberately scoped to what those harnesses need rather
// than a general-purpose IR-metrics library:
//
//   - graded relevance allowed (relevance map → float64), not just
//     binary 0/1 — CoIR-CSN-Python uses {0, 1} but the BEIR convention
//     does support multi-grade and we want to be drop-in for that.
//   - "if not in qrels, relevance is zero" — i.e. unjudged-as-non-relevant,
//     same convention as `pytrec_eval` / `ir_measures` and what BEIR-
//     family benchmarks expect.
//   - ranking is provided as a slice of doc IDs (top-1 first); the
//     metric does not care how it was produced.
//
// Reference: Järvelin & Kekäläinen (2002), DCG_k = Σ_{i=1..k} rel_i /
// log2(i+1), iDCG_k = same formula on the relevances sorted descending,
// NDCG_k = DCG_k / iDCG_k. We use the standard "rel / log2(i+1)"
// formulation rather than the "(2^rel − 1) / log2(i+1)" Burges
// alternative — semble's harness uses the standard form, and CoIR
// reports under the same convention.
package ndcg

import (
	"math"
	"sort"
)

// AtK returns NDCG@k for a single query.
//
//   - ranked: the doc IDs the retriever returned, best-rank-first.
//     Anything past position k is ignored. Duplicates are accepted but
//     only the first occurrence of each doc ID contributes (matches
//     pytrec_eval behavior on duplicate retrievals).
//   - rels: the graded-relevance map for this query. Doc IDs not in
//     this map have relevance zero. The query has no relevant docs iff
//     no entry has a positive relevance — NDCG@k is defined as 0 in
//     that case (avoiding division by zero on iDCG).
//   - k: cutoff. k <= 0 panics; callers should pass 10 for NDCG@10.
//
// Returns a value in [0, 1].
func AtK(ranked []string, rels map[string]float64, k int) float64 {
	if k <= 0 {
		panic("ndcg.AtK: k must be positive")
	}

	// DCG over the retrieved top-k. Track which doc IDs we've already
	// counted so duplicate retrievals don't double-credit.
	var dcg float64
	seen := make(map[string]struct{}, k)
	for i, doc := range ranked {
		if i >= k {
			break
		}
		if _, dup := seen[doc]; dup {
			continue
		}
		seen[doc] = struct{}{}
		if rel, ok := rels[doc]; ok && rel > 0 {
			dcg += rel / math.Log2(float64(i+2)) // i is 0-indexed; rank 1 → log2(2)
		}
	}

	// iDCG: the same formula applied to the top-k graded relevances
	// in descending order. If the qrels have fewer than k judged
	// docs, the rest contribute zero.
	ideal := make([]float64, 0, len(rels))
	for _, rel := range rels {
		if rel > 0 {
			ideal = append(ideal, rel)
		}
	}
	if len(ideal) == 0 {
		return 0
	}
	sort.Slice(ideal, func(a, b int) bool { return ideal[a] > ideal[b] })

	var idcg float64
	for i, rel := range ideal {
		if i >= k {
			break
		}
		idcg += rel / math.Log2(float64(i+2))
	}
	if idcg == 0 {
		return 0
	}
	return dcg / idcg
}

// Average is the macro-average of per-query NDCG@k scores — what BEIR
// and CoIR report as "NDCG@10" in their leaderboards. Empty input is
// defined as 0.
func Average(scores []float64) float64 {
	if len(scores) == 0 {
		return 0
	}
	var sum float64
	for _, s := range scores {
		sum += s
	}
	return sum / float64(len(scores))
}
