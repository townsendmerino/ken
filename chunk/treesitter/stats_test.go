package treesitter

import (
	"testing"
)

// TestStats_UnsupportedLanguageIncrementsFallback exercises the
// language-not-in-kenToTreeSitter branch of Chunk — the silent
// fallback path the OSS-demo playbook needs to be able to quantify.
// Asserts that:
//
//   - Total counts every non-empty source we attempted to chunk.
//   - UnsupportedLang increments for each call routed to the line
//     chunker via "no grammar for this language" (vs the
//     pool.Parse-error / nil-root / invalid-spans branches, which
//     this test doesn't try to trigger because they're hard to
//     construct deterministically without grammar-internal knowledge).
//   - Fallback equals the sum of the per-reason counters.
//   - The empty-source early return does NOT count toward Total
//     (no parse attempted).
func TestStats_UnsupportedLanguageIncrementsFallback(t *testing.T) {
	c := New()

	// Pick a language name guaranteed to miss kenToTreeSitter so we
	// hit poolFor()==nil. "csharp" is deliberately omitted from
	// kenToTreeSitter per chunker.go's documented language list (the
	// grammar OOMs on real C# content); "qbasic" would also work as
	// a never-was-supported sentinel.
	const bogus = "qbasic-never-supported"
	chunks, err := c.Chunk([]byte("PRINT \"HELLO\"\n"), bogus, 1024)
	if err != nil {
		t.Fatalf("Chunk(unsupported lang) returned err: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatalf("expected line-fallback to produce at least one chunk; got 0")
	}
	stats := c.Stats()
	if stats.Total != 1 {
		t.Errorf("Total = %d, want 1 (one non-empty source attempted)", stats.Total)
	}
	if stats.UnsupportedLang != 1 {
		t.Errorf("UnsupportedLang = %d, want 1", stats.UnsupportedLang)
	}
	if stats.Fallback != 1 {
		t.Errorf("Fallback = %d, want 1 (sum of per-reason counters)", stats.Fallback)
	}

	// Second unsupported-lang call: counters accumulate, not reset.
	_, _ = c.Chunk([]byte("X = 1\n"), bogus, 1024)
	stats = c.Stats()
	if stats.Total != 2 || stats.UnsupportedLang != 2 || stats.Fallback != 2 {
		t.Errorf("after 2 calls: Total=%d UnsupportedLang=%d Fallback=%d; want 2,2,2",
			stats.Total, stats.UnsupportedLang, stats.Fallback)
	}

	// Empty-source path is the documented short-circuit in Chunk
	// (returns (nil, nil) before any counter increment). Verify Total
	// is NOT bumped — otherwise the fallback rate the playbook reads
	// would be diluted by every empty file the walker happens to surface.
	_, _ = c.Chunk(nil, bogus, 1024)
	_, _ = c.Chunk([]byte{}, bogus, 1024)
	stats = c.Stats()
	if stats.Total != 2 {
		t.Errorf("empty-source calls should not bump Total; got %d, want 2", stats.Total)
	}

	// A supported language with valid input should NOT increment any
	// fallback counter — Total bumps, fallback counters don't. Uses
	// "go" because the existing TestByteFidelity proves it parses
	// cleanly through to a real cAST result.
	const goSrc = `package x

func F() int { return 1 }
`
	_, err = c.Chunk([]byte(goSrc), "go", 1024)
	if err != nil {
		t.Fatalf("Chunk(go) returned err: %v", err)
	}
	stats = c.Stats()
	if stats.Total != 3 {
		t.Errorf("after supported-lang call: Total=%d, want 3", stats.Total)
	}
	if stats.Fallback != 2 {
		t.Errorf("supported-lang call should not increment Fallback; got %d, want 2 (carry from earlier)", stats.Fallback)
	}
	if stats.ParseErr != 0 || stats.NilRoot != 0 || stats.InvalidSpans != 0 {
		t.Errorf("unexpected non-zero per-reason counter: ParseErr=%d NilRoot=%d InvalidSpans=%d",
			stats.ParseErr, stats.NilRoot, stats.InvalidSpans)
	}
}
