package engine

// ApproxTokens is a fast, dependency-free estimate of how many LLM tokens s
// costs: ~4 characters per alphanumeric run (BPE splits long identifiers
// into several tokens) plus one token per other non-whitespace rune. It
// errs slightly high on dense punctuation, the safe direction for a
// budget. It is NOT an exact token count — ken deliberately does not link
// a BPE tokenizer (e.g. tiktoken) into its released binaries, so this is
// the same heuristic mcp/token_budget.go uses to apply a search/find_related
// max_tokens budget, exported here so other callers (and tack) can budget
// their own context assembly against it without duplicating the estimate.
func ApproxTokens(s string) int {
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
