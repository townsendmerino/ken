package search

import (
	"fmt"
	"sync"
	"testing"
)

// TestChunkDefinesSymbol_ConcurrentDistinctSymbols_NoRace: as of audit §6
// the per-symbol regex memoization cache (an unbounded package-level map
// guarded by an RWMutex) is gone — chunkDefinesSymbol now scans against two
// precompiled, immutable patterns. *regexp.Regexp is safe for concurrent
// use, so this must be race-clean with no shared mutable state at all.
//
// We drive chunkDefinesSymbol directly rather than through Index.Search: the
// boost path is hybrid/semantic-only (BM25 mode is raw lexical, no rerank),
// and hybrid needs a model — so a Search-based test would t.Skip in CI's
// no-model `-race` job, exactly where this guard must run.
func TestChunkDefinesSymbol_ConcurrentDistinctSymbols_NoRace(t *testing.T) {
	const nSymbols = 64
	symbols := make([]string, nSymbols)
	for i := range symbols {
		symbols[i] = fmt.Sprintf("WidgetSymbol%d", i)
	}
	const content = "package p\nfunc WidgetSymbol0(x int) error { return nil }\n" +
		"class Foo {}\ntype Bar struct{}\n"

	const workers = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range workers {
		wg.Go(func() {
			<-start // release all workers together to maximize overlap
			for _, s := range symbols {
				_ = chunkDefinesSymbol(content, s)
			}
		})
	}
	close(start)
	wg.Wait()

	// Sanity: the one symbol actually defined in content is detected, the
	// rest are not — proving the scan/compare logic under concurrency.
	if !chunkDefinesSymbol(content, "WidgetSymbol0") {
		t.Error("chunkDefinesSymbol should detect the defined WidgetSymbol0")
	}
	if chunkDefinesSymbol(content, "WidgetSymbol1") {
		t.Error("chunkDefinesSymbol should NOT detect the undefined WidgetSymbol1")
	}
}

// TestLastIdentComponent covers the namespace-stripping the definition
// capture group relies on (audit §6).
func TestLastIdentComponent(t *testing.T) {
	cases := map[string]string{
		"baz":         "baz",
		"Foo.bar":     "bar",
		"Foo.Bar.baz": "baz",
		"A::b":        "b",
		"a::b::c":     "c",
		"_x":          "_x",
		"pkg.Type_2":  "Type_2",
	}
	for in, want := range cases {
		if got := lastIdentComponent(in); got != want {
			t.Errorf("lastIdentComponent(%q) = %q, want %q", in, got, want)
		}
	}
}
