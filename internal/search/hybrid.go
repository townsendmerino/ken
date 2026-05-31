package search

import (
	"github.com/townsendmerino/aikit/ann"
	"github.com/townsendmerino/aikit/bm25"
	"github.com/townsendmerino/aikit/chunk"
)

// Hybrid retrieval — ported from semble search.py:search_hybrid.
//
// Divergences from the Stage-4 prompt's reconstruction (live source wins,
// patched in ken-prompts.md Prompt 4):
//   - fusion is α-weighted, NOT an equal-weight sum:
//     combined = α·rrf_sem + (1−α)·rrf_bm25, α from resolveAlpha (adaptive).
//   - RRF rank is 1-indexed: 1/(k+rank), rank ∈ {1,2,…}, k=60.
//   - order of operations: file-coherence boost → query boost → penalties.
//   - path penalties run only when α < 1.0 (skipped for pure semantic).

const rrfK = 60

// rrfScores converts a retriever's already-rank-ordered hits to RRF scores
// 1/(k+rank), rank 1-indexed (semble search._rrf_scores; the inputs are
// pre-sorted descending so position is the rank).
func rrfScores(order []int) map[int]float64 {
	out := make(map[int]float64, len(order))
	for pos, idx := range order {
		out[idx] = 1.0 / float64(rrfK+pos+1)
	}
	return out
}

// hybridSearch runs the full semble hybrid pipeline and returns ranked
// (chunkIndex, score) pairs. alphaOverride < 0 ⇒ auto-detect from query.
func hybridSearch(
	query string,
	qVec []float32,
	flat *ann.Flat,
	bm *bm25.Index,
	chunks []chunk.Chunk,
	topK int,
	alphaOverride float64,
) []rankedItem {
	alpha := resolveAlpha(query, alphaOverride)
	candidateCount := topK * 5

	// Semantic candidates (cosine similarity, already sorted desc).
	var semOrder []int
	for _, h := range flat.Query(qVec, candidateCount) {
		semOrder = append(semOrder, h.Index)
	}
	// BM25 candidates, excluding zero-score (semble drops score≤0).
	var bmOrder []int
	for _, r := range bm.TopK(bm25.Tokenize(query), candidateCount) {
		if r.Score > 0 {
			bmOrder = append(bmOrder, r.Doc)
		}
	}

	semRRF := rrfScores(semOrder)
	bmRRF := rrfScores(bmOrder)

	combined := map[int]float64{}
	for idx := range semRRF {
		combined[idx] = alpha * semRRF[idx]
	}
	for idx, v := range bmRRF {
		combined[idx] += (1.0 - alpha) * v
	}

	boostMultiChunkFiles(combined, chunks)
	combined = applyQueryBoost(combined, query, chunks)
	return rerankTopK(combined, chunks, topK, alpha < 1.0)
}
