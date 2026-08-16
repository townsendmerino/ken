package mcp

import (
	"fmt"

	"github.com/townsendmerino/ken/internal/search"
)

// Token-budget support for the search / find_related tools' max_tokens argument.
//
// top_k bounds the result COUNT, but a chunk can be a one-line helper or a
// 300-line class, so a count gives an agent no guarantee about how much of its
// context window the response will consume. max_tokens is the size-based knob:
// fill the ranked list top-down, drop the tail once the budget would be blown.
//
// Deliberately dependency-free: ken does NOT link a BPE tokenizer (tiktoken)
// into the released binary — the slim-binary contract, guarded by
// binary_contract_test.go — so the budget is computed from a heuristic estimate,
// not an exact cl100k count. For a soft ceiling on response size that's the
// right trade: no multi-MB BPE tables in every ken-mcp, and the estimate tracks
// real tokenizers closely enough to decide what to keep.

// rowScaffoldTokens approximates the per-result formatting overhead (rank, line
// range, score, markdown scaffolding) that wraps each chunk's text in the
// rendered output. The chunk text + file path dominate the cost; this is the
// small fixed remainder.
const rowScaffoldTokens = 8

// approxTokens is a fast, dependency-free estimate of how many LLM tokens s
// costs. It counts ~4 characters per alphanumeric run (BPE splits long
// identifiers into several tokens) plus one token per other non-whitespace
// rune. It errs slightly high on dense punctuation, which is the safe direction
// for a budget — return a little less rather than overshoot. It is NOT an exact
// token count; see the file header for why ken keeps it approximate.
func approxTokens(s string) int {
	tokens, run := 0, 0
	flush := func() {
		if run > 0 {
			tokens += (run + 3) / 4 // ceil(run/4)
			run = 0
		}
	}
	for _, r := range s {
		switch {
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			flush()
		case r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			run++
		default:
			flush()
			tokens++ // a symbol / punctuation rune ≈ its own token
		}
	}
	flush()
	return tokens
}

// resultTokens estimates the token cost of one rendered result: its chunk text,
// its file path (part of every result's header), plus the fixed scaffolding.
func resultTokens(r search.Result) int {
	return approxTokens(r.Chunk.Text) + approxTokens(r.Chunk.File) + rowScaffoldTokens
}

// applyTokenBudget greedily keeps the ranked results whose cumulative estimated
// token cost stays within maxTokens, ALWAYS keeping at least the top result (a
// single over-budget top hit still returns something, not an empty list).
// Returns the kept prefix, how many were dropped, and the approximate token
// total of the kept set. maxTokens <= 0 is a no-op passthrough (dropped=0,
// approxTotal=0) so callers can invoke it unconditionally.
func applyTokenBudget(results []search.Result, maxTokens int) (kept []search.Result, dropped, approxTotal int) {
	if maxTokens <= 0 || len(results) == 0 {
		return results, 0, 0
	}
	used, n := 0, 0
	for i := range results {
		cost := resultTokens(results[i])
		if n > 0 && used+cost > maxTokens {
			break
		}
		used += cost
		n++
	}
	return results[:n], len(results) - n, used
}

// budgetResult applies the token budget and returns the trimmed results, the
// BudgetMeta to attach to the JSON response (nil when maxTokens <= 0, so the
// `budget` field is present only when the agent asked for one), and a markdown
// note to append (non-empty only when results were actually dropped). One place
// so search and find_related stay consistent.
func budgetResult(results []search.Result, maxTokens int) ([]search.Result, *BudgetMeta, string) {
	if maxTokens <= 0 {
		return results, nil, ""
	}
	kept, dropped, used := applyTokenBudget(results, maxTokens)
	meta := &BudgetMeta{MaxTokens: maxTokens, ApproxTokens: used, Truncated: dropped > 0, Dropped: dropped}
	note := ""
	if dropped > 0 {
		note = fmt.Sprintf("_Trimmed to fit max_tokens=%d: showing %d of %d results (~%d tokens); %d dropped._",
			maxTokens, len(kept), len(kept)+dropped, used, dropped)
	}
	return kept, meta, note
}
