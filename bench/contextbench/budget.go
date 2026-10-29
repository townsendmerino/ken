//go:build bench

package contextbench

import (
	"github.com/townsendmerino/ken/bench/tokens"
)

// Standard retrieval token budgets specified by CB1 brief.
const (
	Budget2K  = 2048
	Budget8K  = 8192 // Primary evaluation budget
	Budget16K = 16384
)

// DefaultBudgets is the list of evaluation token budgets.
var DefaultBudgets = []int{Budget2K, Budget8K, Budget16K}

// PackBudget fills a token budget top-down from ranked hits using cl100k_base token counting.
// Rules (from K18 brief):
// 1. The #1 ranked chunk is ALWAYS kept, even if it exceeds maxTokens alone.
// 2. Subsequent chunks that would cause cumulative tokens to cross maxTokens are dropped (not truncated).
// 3. Chunks that fit within the remaining budget are kept.
func PackBudget(hits []Hit, maxTokens int) (kept []Hit, usedTokens int) {
	if len(hits) == 0 {
		return nil, 0
	}

	// Ensure token counts are computed
	ensureTokens := func(h *Hit) {
		if h.Tokens <= 0 && h.Text != "" {
			h.Tokens = tokens.Count(h.Text)
		}
	}

	// Always keep rank #1 chunk
	first := hits[0]
	ensureTokens(&first)
	kept = append(kept, first)
	usedTokens = first.Tokens

	for i := 1; i < len(hits); i++ {
		h := hits[i]
		ensureTokens(&h)
		if usedTokens+h.Tokens <= maxTokens {
			kept = append(kept, h)
			usedTokens += h.Tokens
		}
		// If usedTokens + h.Tokens > maxTokens, drop chunk (do not truncate)
	}

	return kept, usedTokens
}
