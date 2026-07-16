package search

// Predictor produces a set of code identifiers that a natural-language
// query may be reaching for but did not name explicitly. The Stage-7a
// transform #2 (vocab-gap identifier expansion) feeds the returned
// identifiers into BM25 and the existing embedded-symbol boost path,
// bridging the NL→code gap on the lexical side.
//
// Implementations are goroutine-safe. May return nil/empty when no
// confident prediction is available — callers MUST treat nil and
// empty as a no-op.
//
// Cost contract: implementations should be near-free at query time.
// The shipped predictor (when M0c chooses one) will be a
// discriminative match against vectors precomputed at index time, not
// a generative model — see the HyDE Phase B analysis and the M0c
// predictor experiments for the rationale.
//
// The interface deliberately doesn't expose a configurable K — that
// is a property of the predictor instance, set at construction. Keeps
// the SearchWithQVecPredicted hot path branch-free.
type Predictor interface {
	Predict(query string) []string
}

// NOTE: predicted identifiers ride the same boost scale as
// query-embedded symbols (embeddedSymbolBoostScale = 0.5 in
// internal/search/rerank.go). A separate predictedSymbolBoostScale
// constant existed earlier as a placeholder for a tuning knob, but
// was unused — the current implementation in boostEmbeddedSymbols
// folds predicted names into the existing scale. If a future
// predictor wants a different discount (e.g. 0.25 to penalize
// noisier predictions less), reintroduce the constant and route
// it through a separate boost loop.
