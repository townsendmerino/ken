package search

import (
	"regexp"
	"strings"
)

// Adaptive weighting — ported verbatim from semble ranking/weighting.py +
// ranking/boosting.py:is_symbol_query.
//
// IMPORTANT divergence from the Stage-4 prompt's reconstruction: semble's
// "adaptive" weighting re-weights the RRF *inputs* via alpha, it does NOT
// merely gate post-fusion boosts. alpha is the semantic weight; BM25 gets
// (1-alpha). A bare/qualified identifier query leans BM25 (alpha 0.3); a
// natural-language query is balanced (alpha 0.5). See ken-prompts.md
// Prompt 4 patch notes.
const (
	alphaSymbol = 0.3 // lean BM25 for exact keyword matching
	alphaNL     = 0.5 // balanced semantic + BM25
)

// symbolQueryRE is semble's _SYMBOL_QUERY_RE, verbatim. It full-matches the
// stripped query: namespace-qualified, leading-underscore, contains an
// uppercase/underscore, or starts uppercase. A plain lowercase word or
// multi-word phrase ("session", "how to parse config") is NL, not symbol —
// note this contradicts the prompt's "short lowercase ⇒ symbol" heuristic.
var symbolQueryRE = regexp.MustCompile(
	`^(?:` +
		`[A-Za-z_][A-Za-z0-9_]*(?:(?:::|\\|->|\.)[A-Za-z_][A-Za-z0-9_]*)+` + // namespace-qualified
		`|_[A-Za-z0-9_]*` + // leading underscore
		`|[A-Za-z][A-Za-z0-9]*[A-Z_][A-Za-z0-9_]*` + // contains uppercase or underscore
		`|[A-Z][A-Za-z0-9]*` + // starts with uppercase
		`)$`,
)

// isSymbolQuery reports whether the query looks like a bare or
// namespace-qualified identifier (semble boosting.is_symbol_query).
func isSymbolQuery(query string) bool {
	return symbolQueryRE.MatchString(strings.TrimSpace(query))
}

// AlphaPair pins the adaptive-α constants for a single search. A
// NEGATIVE component means "use the shipped constant for that query
// class", so [AdaptiveAlphas] reproduces the default behavior and a
// pinned α of 0.0 stays distinguishable from "not pinned" — which
// matters, because 0.0 (pure BM25) is a real point on the sweep the
// α-sensitivity harness walks.
//
// Both classes are carried together rather than as one override
// because the classifier picks which constant applies per query: a
// sweep over the symbol-class weight has to hold the NL-class weight
// at its default while [isSymbolQuery] routes each query.
type AlphaPair struct {
	// Symbol is the semantic weight for identifier-shaped queries
	// (shipped default 0.3 — lean BM25 for exact keyword matching).
	Symbol float64
	// NL is the semantic weight for natural-language queries
	// (shipped default 0.5 — balanced semantic + BM25).
	NL float64
}

// AdaptiveAlphas is the shipped behavior: resolve both classes from
// the constants above. Every production entry point passes this; only
// the α-sensitivity bench pins anything else.
var AdaptiveAlphas = AlphaPair{Symbol: -1, NL: -1}

// resolveAlpha returns the semantic blend weight for query, honoring
// any pinned component of a (semble ranking/weighting.resolve_alpha,
// extended with the per-class pin).
func resolveAlpha(query string, a AlphaPair) float64 {
	if isSymbolQuery(query) {
		if a.Symbol >= 0 {
			return a.Symbol
		}
		return alphaSymbol
	}
	if a.NL >= 0 {
		return a.NL
	}
	return alphaNL
}

// IsSymbolQuery reports whether query looks like a bare or
// namespace-qualified identifier — semble's is_symbol_query, the
// classifier that decides WHICH α constant applies.
//
// Exported for the α-sensitivity harness, which has to bucket queries
// by the same rule the fusion uses. Note this is NOT interchangeable
// with bench/tokens' ClassifyQuery: that one deliberately models what
// a heuristic agent router would do (short, single-token, identifier-
// shaped), while this one is semble's regex and is the only classifier
// that actually selects an α.
func IsSymbolQuery(query string) bool { return isSymbolQuery(query) }

// DefaultAlphas returns the two adaptive-α constants — the semantic
// blend weight applied to a symbol-class query and to an NL-class
// query respectively — as resolveAlpha would apply them when no
// override is given.
//
// Exported for the bench provenance block (docs/internal/rag-thread-followups.md
// item 4): a result JSON has to record the α pair it was produced at,
// and hardcoding 0.3/0.5 in the harness would let the recorded value
// drift away from the value actually used. Read-only; nothing in the
// search path calls it.
func DefaultAlphas() (symbol, nl float64) { return alphaSymbol, alphaNL }
