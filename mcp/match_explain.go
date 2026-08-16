package mcp

import (
	"strings"

	"github.com/townsendmerino/aikit/bm25"
)

// Per-result "why this matched" explanation for the search tool's opt-in
// `explain` argument.
//
// Honest scope: this is a LEXICAL-overlap explanation, not full retrieval
// provenance. ken's hybrid ranker fuses a BM25 arm and a dense (semantic) arm
// via RRF plus boosts/penalties/rerank; attributing a result's final rank to
// each stage would need per-result instrumentation threaded through the whole
// pipeline. What's cheaply and honestly computable post-hoc is which of the
// query's tokens appear in the chunk — a strong signal for "did this match on
// the words I typed, or on meaning?" A result with no term overlap surfaced via
// the semantic arm. That's the useful 80% for debugging "why is this here?",
// and the MatchInfo.Kind names exactly what it is so nobody mistakes it for a
// full ranking breakdown.

// MatchInfo is the per-result explanation surfaced when `explain` is set.
type MatchInfo struct {
	// Kind is "lexical" when ≥1 query term appears verbatim (post-tokenization)
	// in the chunk, else "semantic" (no term overlap — surfaced by the dense arm).
	Kind string `json:"kind"`
	// Terms are the query tokens present in the chunk (lexical kind only),
	// deduped and in query order.
	Terms []string `json:"terms,omitempty"`
}

// human renders MatchInfo as a compact markdown suffix.
func (m MatchInfo) human() string {
	if m.Kind == "lexical" && len(m.Terms) > 0 {
		return "match: terms " + strings.Join(m.Terms, ", ")
	}
	return "match: semantic (no exact term overlap)"
}

// matchInfo computes the lexical-overlap explanation for one result. Uses the
// same identifier-aware BM25 tokenizer the retriever uses, so "getUserById"
// splits to get/user/by/id and matches a query mentioning any of them.
func matchInfo(query, chunkText string) MatchInfo {
	qTerms := bm25.Tokenize(query)
	if len(qTerms) == 0 {
		return MatchInfo{Kind: "semantic"}
	}
	present := make(map[string]struct{}, 64)
	for _, t := range bm25.Tokenize(chunkText) {
		present[t] = struct{}{}
	}
	var matched []string
	seen := make(map[string]struct{}, len(qTerms))
	for _, t := range qTerms {
		if _, dup := seen[t]; dup {
			continue
		}
		if _, ok := present[t]; ok {
			seen[t] = struct{}{}
			matched = append(matched, t)
		}
	}
	if len(matched) == 0 {
		return MatchInfo{Kind: "semantic"}
	}
	return MatchInfo{Kind: "lexical", Terms: matched}
}

// explainAnnotator returns a formatResults annotator that appends the lexical
// match explanation for `query` to each result. Only pass when args.Explain is set.
func explainAnnotator(query string) func(Result) string {
	return func(r Result) string {
		return matchInfo(query, r.Chunk.Text).human()
	}
}
