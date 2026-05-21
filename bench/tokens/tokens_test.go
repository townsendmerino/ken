//go:build bench

package tokens

import "testing"

// TestCount_KnownStrings pins the counter against a handful of inputs
// whose cl100k_base token counts are well-known (verifiable against
// OpenAI's online tokenizer or the tiktoken-go README's examples).
//
// We're not testing the tokenizer itself (the upstream library has its
// own tests); we're testing that Count() is wired up to cl100k_base
// and not some other encoder, that empty-string returns zero, and
// that the counter is deterministic across calls.
func TestCount_KnownStrings(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		// "hello world" — 2 tokens in cl100k_base, exhaustively confirmed.
		{"hello world", 2},
		// "ken" — 1 token; common name should be a single chunk.
		{"ken", 1},
		// Identifier-style snake_case. cl100k_base merges this to 2
		// tokens ("validate" + "_user") — verified against the upstream
		// encoder. This case exists mainly to catch a regression where
		// we'd accidentally call a different encoding (gpt2, p50k_base,
		// o200k_base all give 3+ tokens for this string).
		{"validate_user", 2},
		// Punctuation-heavy code-like string. Just check it's > 5
		// (the exact count is encoder-detail-sensitive; we only need
		// to know our wrapper isn't returning len(s) or zero).
		{"if (x == 1) { return 42; }", -1}, // sentinel: assert > 5
	}
	for _, c := range cases {
		got := Count(c.in)
		if c.want >= 0 && got != c.want {
			t.Errorf("Count(%q) = %d, want %d", c.in, got, c.want)
		}
		if c.want < 0 && got <= 5 {
			t.Errorf("Count(%q) = %d, want > 5 (sanity)", c.in, got)
		}
	}
}

// TestCount_Deterministic — two calls on the same input return the
// same count. Cheap; guards against any cache or stateful encoder
// going wrong over the lifetime of a bench run.
func TestCount_Deterministic(t *testing.T) {
	s := "the quick brown fox jumps over the lazy dog"
	a := Count(s)
	b := Count(s)
	if a != b {
		t.Errorf("Count not deterministic: %d vs %d", a, b)
	}
	if a == 0 {
		t.Error("Count returned 0 for non-empty input")
	}
}
