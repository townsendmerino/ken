package search

import (
	"github.com/townsendmerino/aikit/ann"
	"github.com/townsendmerino/aikit/bm25"
	"github.com/townsendmerino/aikit/chunk"
	"github.com/townsendmerino/aikit/fuse"
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
//
// As of aikit v0.2.0 the RRF math lives in aikit/fuse.RRFWeighted —
// numerically identical to the prior ken-local rrfScores helper (same
// 1-indexed `w/(k+rank)`), but now shared with other consumers of the
// toolkit. k=60 stays explicit at the call site (= fuse.DefaultK).

// candidateOverfetch is the per-arm over-fetch factor: each retrieval arm
// (semantic flat scan + BM25) returns topK*candidateOverfetch candidates so
// the downstream fusion + boost + penalty stages rerank from a pool deeper
// than the final topK — a chunk ranked mid-pack in one arm can still surface
// in the top-K after RRF. Pinned to semble's pipeline (verbatim port; see
// docs/DESIGN.md §7).
const candidateOverfetch = 5

// hybridSearch runs the full semble hybrid pipeline and returns ranked
// (chunkIndex, score) pairs. alphaOverride < 0 ⇒ auto-detect from query.
//
// predicted, when non-empty, is the Stage-7a transform #2 vocab-gap
// expansion: identifiers predicted from the NL query (oracle, PRF,
// encoder, etc.). They are appended to the BM25 token bag and passed
// to the boost path so the existing embedded-symbol heuristics light
// up on them. Empty/nil predicted is a no-op (the M0 baseline path).
func hybridSearch(
	query string,
	qVec []float32,
	flat *ann.Flat,
	bm *bm25.Index,
	chunks []chunk.Chunk,
	topK int,
	alphaOverride float64,
	predicted []string,
) []rankedItem {
	alpha := resolveAlpha(query, alphaOverride)
	candidateCount := topK * candidateOverfetch

	// Semantic candidates (cosine similarity, already sorted desc).
	var semOrder []int
	for _, h := range flat.Query(qVec, candidateCount) {
		semOrder = append(semOrder, h.Index)
	}
	// BM25 candidates, excluding zero-score (semble drops score≤0).
	// Predicted identifiers extend the query token bag (canonicalized
	// via the same tokenizer the index used, so camel/snake/acronym
	// splits match). No down-weighting at the BM25 layer in v0 — IDF
	// implicitly down-weights frequent terms; if M0c shows runaway
	// lost-cases we'll add a dual-retrieval RRF blend.
	bmTerms := bm25.Tokenize(query)
	if len(predicted) > 0 {
		for _, p := range predicted {
			bmTerms = append(bmTerms, bm25.Tokenize(p)...)
		}
	}
	var bmOrder []int
	for _, r := range bm.TopK(bmTerms, candidateCount) {
		if r.Score > 0 {
			bmOrder = append(bmOrder, r.Doc)
		}
	}

	fused := fuse.RRFWeighted(fuse.DefaultK, []float64{alpha, 1.0 - alpha}, semOrder, bmOrder)
	combined := make(map[int]float64, len(fused))
	for _, r := range fused {
		combined[r.Key] = r.Score
	}

	boostMultiChunkFiles(combined, chunks)
	combined = applyQueryBoost(combined, query, chunks, predicted)
	return rerankTopK(combined, chunks, topK, alpha < 1.0)
}
