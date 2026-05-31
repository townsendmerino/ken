package search

import (
	"sort"

	"github.com/townsendmerino/ken/chunk"
)

// Reranker scores stage-1 candidates against a query. Higher score =
// more relevant. Implementations are goroutine-safe.
//
// Returned slice length MUST equal len(cands); callers (applyReranker)
// rely on positional alignment. On any internal error implementations
// should return nil so the orchestrator can pass-through the stage-1
// ranking unchanged.
type Reranker interface {
	Rerank(query string, cands []chunk.Chunk) []float64
}

// telemetryReranker is the optional Reranker extension that exposes
// per-call timing breakdowns. NeuralReranker implements it; user-
// supplied rerankers may not. SearchModeWithTelemetry type-asserts
// to this interface and falls back to the basic Rerank if absent.
//
// Lower-case because it's an internal coordination protocol — third
// parties should just implement Reranker. The fields filled into
// *Telemetry are the Reranker* ones (QueryEncode, CandidateEncode,
// CacheHits, CacheMisses).
type telemetryReranker interface {
	RerankWithTelemetry(query string, cands []chunk.Chunk, t *Telemetry) []float64
}

// rerankerConfig captures the orchestration knobs SearchMode uses when
// reranking the hybrid head. Stored on Index so the rerank policy
// survives WatchedIndex snapshot swaps (the reranker itself is shared
// across snapshots — its LRU cache is content-hashed, so stale entries
// just never get hit; see plan §8).
type rerankerConfig struct {
	rerankN int     // depth of the head to rerank from stage-1
	beta    float64 // blend weight: final = β·rerankCos + (1−β)·fusedScore

	// Adaptive rerankN (M8c): when stage-1 is highly confident in its
	// top result — top-1 score is much larger than top-2 — reranking
	// 50 candidates wastes compute reordering a tail that's already
	// well-discriminated. If the RELATIVE MARGIN between stage-1's #1
	// and #2 score exceeds adaptiveThreshold, cap the rerank head at
	// adaptiveMinN (cheap rerank just to refine the top few).
	//
	// adaptiveThreshold ≤ 0 disables the adaptive path entirely
	// (the M5 default). When enabled, typical values are 0.30–0.50
	// (top-1 30–50% better than top-2). adaptiveMinN must be ≥ k
	// to avoid truncating user-visible results.
	adaptiveThreshold float64
	adaptiveMinN      int
}

// defaultRerankerConfig: M0-amended defaults (outputs/m0-results.md).
//
//   - rerankN=50 is plan §9.3's recommended depth (≥ 5*k for typical k=10).
//   - beta=0.25 is the only β tested on the full semble bench that produced
//     a positive lift in every category (architecture +0.007, semantic
//     +0.010, symbol +0.009). β=0 is pure hybrid (no rerank effect),
//     β=0.5 is roughly neutral, β=1 (pure replacement) regressed semble
//     by 10 NDCG points — so β=0.25 is the safe non-zero default.
//
// The plan's original §9.3 proposed pure replacement (β=1) with an
// isSymbolQuery skip. M0 inverted both:
//
//   - blend is mandatory, not optional (the lone tuning knob);
//   - isSymbolQuery skip is unjustified (all three semble categories
//     react identically to the blend), so it's intentionally absent here.
var defaultRerankerConfig = rerankerConfig{rerankN: 50, beta: 0.25}

// RerankerOption configures the orchestration knobs passed to
// Index.SetReranker. Used by the CLI / MCP layer to expose
// KEN_MCP_RERANK_TOP_N and similar.
type RerankerOption func(*rerankerConfig)

// WithRerankN sets the rerank depth (default 50). Must be ≥ k (the
// user's requested result count); values smaller than k are silently
// clamped up at query time so a too-small config doesn't truncate
// results.
func WithRerankN(n int) RerankerOption {
	return func(c *rerankerConfig) {
		if n > 0 {
			c.rerankN = n
		}
	}
}

// WithRerankBlendBeta sets the score-blend weight (default 0.25 per
// M0). β in [0, 1]; β=0 is pure stage-1 order, β=1 is pure neural.
// Out-of-range values are clamped.
func WithRerankBlendBeta(beta float64) RerankerOption {
	return func(c *rerankerConfig) {
		if beta < 0 {
			beta = 0
		}
		if beta > 1 {
			beta = 1
		}
		c.beta = beta
	}
}

// WithAdaptiveRerankN enables the M8c adaptive path: when stage-1's
// top-1 score exceeds the top-2 by `threshold` (relative margin in
// (0, 1)), the rerank head is capped at `minN` instead of the
// configured rerankN. The point is to spend less time reranking
// "easy" queries where stage-1 is already confident.
//
// threshold ≤ 0 (or ≥ 1) disables the adaptive path. Typical:
// threshold=0.30 means "if top-1 is at least 30% better than top-2,
// rerank only the top minN instead of all rerankN."
//
// minN should be ≥ the caller's expected k (the actual returned
// result count); 10 is a good default for k=10 callers.
func WithAdaptiveRerankN(threshold float64, minN int) RerankerOption {
	return func(c *rerankerConfig) {
		if threshold <= 0 || threshold >= 1 || minN <= 0 {
			c.adaptiveThreshold = 0
			c.adaptiveMinN = 0
			return
		}
		c.adaptiveThreshold = threshold
		c.adaptiveMinN = minN
	}
}

// applyReranker reorders ranked[:n] (n = cfg.rerankN, clamped to
// len(ranked)) using r.Rerank's cosines blended with the stage-1
// fused scores via min-max normalization, then sorted desc. The tail
// (ranked[n:]) is appended in its original order. Returns a fresh
// slice; ranked is not mutated.
//
// If r is nil, n ≤ 0, or r.Rerank returns the wrong length (contract
// violation), the input is returned unchanged so a downstream
// glitch can't produce empty results.
func applyReranker(r Reranker, query string, ranked []Result, cfg rerankerConfig) []Result {
	return applyRerankerWithTelemetry(r, query, ranked, cfg, nil)
}

// applyRerankerWithTelemetry is the telemetry-aware variant. If t is
// non-nil and r implements telemetryReranker, the reranker's sub-
// breakdown (query encode, candidate encode, cache hits/misses) is
// filled in. The blend wall is the caller's to measure.
func applyRerankerWithTelemetry(r Reranker, query string, ranked []Result, cfg rerankerConfig, t *Telemetry) []Result {
	if r == nil || cfg.rerankN <= 0 || len(ranked) == 0 {
		return ranked
	}
	n := cfg.rerankN
	if n > len(ranked) {
		n = len(ranked)
	}
	// M8c adaptive: when stage-1 is confident (top-1 ≫ top-2 by
	// relative margin), shrink the rerank head to adaptiveMinN. Saves
	// most of the rerank cost on "easy" queries without changing the
	// final ordering (the displaced tail entries weren't going to
	// flip into position 1 anyway).
	if cfg.adaptiveThreshold > 0 && cfg.adaptiveMinN > 0 && len(ranked) >= 2 {
		s0 := ranked[0].Score
		s1 := ranked[1].Score
		if s0 > 0 {
			margin := (s0 - s1) / s0
			if margin >= cfg.adaptiveThreshold && cfg.adaptiveMinN < n {
				n = cfg.adaptiveMinN
			}
		}
	}
	head := ranked[:n]
	tail := ranked[n:]

	cands := make([]chunk.Chunk, len(head))
	for i, h := range head {
		cands[i] = h.Chunk
	}
	if t != nil {
		t.RerankerN = len(head)
	}
	var cosines []float64
	if t != nil {
		if tr, ok := r.(telemetryReranker); ok {
			cosines = tr.RerankWithTelemetry(query, cands, t)
		} else {
			cosines = r.Rerank(query, cands)
		}
	} else {
		cosines = r.Rerank(query, cands)
	}
	if len(cosines) != len(head) {
		// Contract violation by the reranker. Pass-through is safer
		// than producing junk; the caller still gets stage-1 order.
		return ranked
	}

	cosN := minmaxNormalize(cosines)
	fused := make([]float64, len(head))
	for i, h := range head {
		fused[i] = h.Score
	}
	fusedN := minmaxNormalize(fused)

	type scored struct {
		idx   int
		final float64
	}
	blended := make([]scored, len(head))
	for i := range head {
		blended[i] = scored{i, cfg.beta*cosN[i] + (1.0-cfg.beta)*fusedN[i]}
	}
	sort.Slice(blended, func(a, b int) bool { return blended[a].final > blended[b].final })

	out := make([]Result, 0, len(ranked))
	for _, b := range blended {
		out = append(out, Result{Chunk: head[b.idx].Chunk, Score: b.final})
	}
	out = append(out, tail...)
	return out
}

// minmaxNormalize maps each entry to [0,1] via (x-lo)/(hi-lo). If all
// inputs are equal (degenerate score distribution), every entry maps
// to 0.5 — keeps the blend well-defined without privileging any
// position. Empty input returns nil.
func minmaxNormalize(xs []float64) []float64 {
	if len(xs) == 0 {
		return nil
	}
	lo, hi := xs[0], xs[0]
	for _, x := range xs {
		if x < lo {
			lo = x
		}
		if x > hi {
			hi = x
		}
	}
	out := make([]float64, len(xs))
	if hi <= lo {
		for i := range out {
			out[i] = 0.5
		}
		return out
	}
	span := hi - lo
	for i, x := range xs {
		out[i] = (x - lo) / span
	}
	return out
}
