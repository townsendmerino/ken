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
// a generative model — see outputs/m0b-phase-b-results.md and the
// M0c memo for the rationale.
//
// The interface deliberately doesn't expose a configurable K — that
// is a property of the predictor instance, set at construction. Keeps
// the SearchWithQVecPredicted hot path branch-free.
type Predictor interface {
	Predict(query string) []string
}

// predictedSymbolBoostScale is the discount applied to the boost a
// predicted identifier earns relative to a query-embedded one. Reuses
// the existing embeddedSymbolBoostScale (0.5) — a predicted name is
// treated identically to a name the user happened to put inline.
// Lower values (e.g. 0.25) would penalize wrong predictions less but
// also reward right ones less; left as a tuning knob for the production
// wiring once M0c picks a winning predictor.
const predictedSymbolBoostScale = embeddedSymbolBoostScale
