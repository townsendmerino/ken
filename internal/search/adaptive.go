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

// resolveAlpha returns the semantic blend weight. override<0 means
// auto-detect from query type (semble ranking/weighting.resolve_alpha).
func resolveAlpha(query string, override float64) float64 {
	if override >= 0 {
		return override
	}
	if isSymbolQuery(query) {
		return alphaSymbol
	}
	return alphaNL
}
