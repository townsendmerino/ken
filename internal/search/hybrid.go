package search

import (
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
// (chunkIndex, score) pairs. Pass [AdaptiveAlphas] for the shipped
// per-class weights; a non-negative component of alphas pins that
// class (see [AlphaPair]).
//
// predicted, when non-empty, is the Stage-7a transform #2 vocab-gap
// expansion: identifiers predicted from the NL query (oracle, PRF,
// encoder, etc.). They are appended to the BM25 token bag and passed
// to the boost path so the existing embedded-symbol heuristics light
// up on them. Empty/nil predicted is a no-op (the M0 baseline path).
func hybridSearch(
	query string,
	qVec []float32,
	flat denseRetriever,
	bm *bm25.Index,
	chunks []chunk.Chunk,
	topK int,
	alphas AlphaPair,
	predicted []string,
	abl RankAblation,
) []rankedItem {
	return hybridRankFused(fuseArms(query, qVec, flat, bm, topK, alphas, predicted), query, chunks, topK, predicted, abl)
}

// hybridRankFused is hybridSearch after the arms: the boosts, penalties and
// saturation over f.combined (which it consumes), cut to topK. f must come
// from fuseArms at the same topK.
func hybridRankFused(f fusedArms, query string, chunks []chunk.Chunk, topK int, predicted []string, abl RankAblation) []rankedItem {
	combined := f.combined
	if !abl.NoCoherenceBoost {
		boostMultiChunkFiles(combined, chunks)
	}
	applyQueryBoostInPlace(combined, query, chunks, predicted)
	return rerankTopK(combined, chunks, topK, f.alpha < 1.0, abl.saturationThreshold(), abl.saturationDecay())
}

// fusedArms is the first stage of hybridSearch: both arms' candidate lists
// and their α-weighted RRF fusion, before any boost. SearchFilesMode reuses
// it so its arms are the same calls, with the same depth and tie-breaks, as
// the whole ranking's.
type fusedArms struct {
	alpha    float64
	semOrder []int // dense arm, best first
	bmOrder  []int // BM25 arm (score > 0 only), best first
	combined map[int]float64
}

// fuseArms runs the semantic and BM25 arms at depth topK*candidateOverfetch
// and fuses them with weighted RRF.
//
// predicted, when non-empty, is the Stage-7a transform #2 vocab-gap
// expansion (see hybridSearch).
func fuseArms(
	query string,
	qVec []float32,
	flat denseRetriever,
	bm *bm25.Index,
	topK int,
	alphas AlphaPair,
	predicted []string,
) fusedArms {
	f := runArms(query, qVec, flat, bm, topK, alphas, predicted)
	weights := [2]float64{f.alpha, 1.0 - f.alpha}
	fused := fuse.RRFWeighted(fuse.DefaultK, weights[:], f.semOrder, f.bmOrder)
	f.combined = make(map[int]float64, len(fused))
	for _, r := range fused {
		f.combined[r.Key] = r.Score
	}
	return f
}

// runArms is fuseArms without the fusion: alpha and both arms' candidate
// lists, combined left nil.
func runArms(
	query string,
	qVec []float32,
	flat denseRetriever,
	bm *bm25.Index,
	topK int,
	alphas AlphaPair,
	predicted []string,
) fusedArms {
	alpha := resolveAlpha(query, alphas)
	candidateCount := topK * candidateOverfetch

	// Semantic candidates (cosine similarity, already sorted desc).
	semHits := flat.Query(qVec, candidateCount)
	semOrder := make([]int, len(semHits))
	for i, h := range semHits {
		semOrder[i] = h.Index
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
	bmHits := bm.TopK(bmTerms, candidateCount)
	bmOrder := make([]int, 0, len(bmHits))
	for _, r := range bmHits {
		if r.Score > 0 {
			bmOrder = append(bmOrder, r.Doc)
		}
	}
	return fusedArms{alpha: alpha, semOrder: semOrder, bmOrder: bmOrder}
}
