package search

import "time"

// Telemetry is the per-query timing breakdown SearchModeWithTelemetry
// returns. Operators / interactive users / bench harnesses consume
// this to diagnose slow queries.
//
// All durations are wall-clock. Zero values mean "stage didn't run"
// (e.g. RerankWall=0 for a non-rerank mode; RerankerCacheHits=0 if
// the reranker doesn't expose telemetry — fallback to the basic
// Rerank interface).
//
// Stable enough to log / serialize: field names match what
// `ken bench` emits as JSON keys and what `ken-mcp` logs at info
// level. New fields can be added but existing ones shouldn't be
// renamed without coordinating with bench harness consumers.
type Telemetry struct {
	// Total query wall time (sum-ish of the components below; small
	// gap for the search package's own bookkeeping).
	TotalWall time.Duration `json:"total_wall_us"`

	// Stage-1 hybrid retrieval (BM25 + Model2Vec + RRF + heuristics).
	// Always non-zero for hybrid / hybrid-rerank modes; zero for
	// bm25 / semantic modes (those go through different code paths
	// not yet instrumented).
	Stage1Wall time.Duration `json:"stage1_wall_us"`

	// Stage-2 neural rerank (NeuralReranker.Rerank): includes
	// tokenization + query encode + candidate encode + cosine. Zero
	// for non-rerank modes. Detailed sub-breakdown in the Rerank*
	// fields below when the reranker supports it (NeuralReranker
	// does; user-supplied rerankers may not).
	RerankWall time.Duration `json:"rerank_wall_us"`

	// Score blend + sort + tombstone filter. Tiny (microseconds) but
	// reported for completeness.
	BlendWall time.Duration `json:"blend_wall_us"`

	// --- Reranker sub-breakdown (NeuralReranker only) ---

	// Query forward pass (the [CLS]-prefixed query through the 12-layer
	// transformer). 150 ms – 5 s depending on query length.
	RerankerQueryEncode time.Duration `json:"rerank_query_encode_us"`

	// Candidate forward passes (the missing-from-cache subset, batched
	// per worker). Dominates the rerank wall on cold caches; near zero
	// on fully-warm caches.
	RerankerCandidateEncode time.Duration `json:"rerank_candidate_encode_us"`

	// Cache stats for this query: how many of the rerankN candidates
	// were already in the LRU vs needed encoding.
	RerankerCacheHits   int `json:"rerank_cache_hits"`
	RerankerCacheMisses int `json:"rerank_cache_misses"`

	// Effective rerank head — when adaptive rerankN triggers
	// (stage-1 confident), this is the actual N used, ≤ configured
	// rerankN. Useful to confirm adaptive is firing.
	RerankerN int `json:"rerank_n"`
}
