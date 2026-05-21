//go:build bench

// Package tokens is a benchmark-only token-budget counter used by
// bench/tokens/{coir,semble}_test.go to compare agent-input costs:
// how many cl100k_base BPE tokens does ken's formatted-output for a
// query cost vs the grep+Read fallback path's tokens for the same
// query?
//
// Build-tag gated (//go:build bench) so the tiktoken-go dep and the
// embedded BPE tables never compile into the released ken / ken-mcp
// binaries. The release graph stays at its v0.3 sizes (3.9 MB / 16 MB
// without treesitter, 30 MB / 42 MB with). Verify with:
//
//	go list -deps ./cmd/ken ./cmd/ken-mcp | grep -E 'tiktoken|regexp2|uuid'
//
// — that list must be empty.
//
// Tokenizer choice rationale: cl100k_base (GPT-4's encoder) is used as
// a universal LLM-input proxy because Anthropic doesn't publish a
// local Claude tokenizer. Claude tokens typically run 10–20% different
// from cl100k_base counts; for token-budget *ratios* between ken and
// grep on the same query, the absolute encoder doesn't matter much,
// but the absolute numbers in the BENCH.md table are advisory not
// authoritative. The BENCH.md "Caveats" subsection records this.
package tokens

import (
	"fmt"
	"sync"

	"github.com/pkoukk/tiktoken-go"
)

// encoderOnce + encoder cache the cl100k_base encoder for the lifetime
// of the test run. tiktoken-go's GetEncoding is internally
// thread-safe and amortizes the BPE-table parse on first call;
// caching the *Tiktoken pointer dodges per-call lookups in the inner
// loop of a multi-thousand-query bench.
var (
	encoderOnce sync.Once
	encoder     *tiktoken.Tiktoken
	encoderErr  error
)

func loadEncoder() (*tiktoken.Tiktoken, error) {
	encoderOnce.Do(func() {
		encoder, encoderErr = tiktoken.GetEncoding("cl100k_base")
	})
	return encoder, encoderErr
}

// Count returns the cl100k_base token count for s. Deterministic.
// Panics with a clear message if the encoder fails to load; an
// unloadable encoder is a hard test-environment problem, not a
// recoverable per-call error.
func Count(s string) int {
	enc, err := loadEncoder()
	if err != nil {
		panic(fmt.Sprintf("tokens.Count: load cl100k_base: %v", err))
	}
	if s == "" {
		return 0
	}
	return len(enc.Encode(s, nil, nil))
}
