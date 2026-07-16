package search

import (
	"fmt"
	"sync"
	"testing"
)

// TestDefPatternCache_ConcurrentDistinctSymbols_NoRace guards the
// definitionPattern memoization cache (rerank.go) against the data race the
// MCP SDK's per-goroutine tool dispatch exposes. In a symbol search the
// rerank boost path calls chunkDefinesSymbol(content, symbol) →
// definitionPattern(symbol), which reads and writes a package-level map;
// concurrent searches with *distinct* symbols all miss and write it at once.
//
// We drive chunkDefinesSymbol directly rather than through Index.Search: the
// boost path is hybrid/semantic-only (BM25 mode is raw lexical, no rerank),
// and hybrid needs a model — so a Search-based test would t.Skip in CI's
// no-model `-race` job, exactly where this guard must run. Distinct symbols
// are the whole point: a shared symbol only reads after the first write and
// never surfaces the race (the gap the prior concurrency test left open).
//
// Without the RWMutex this trips `go test -race` (and a plain run can panic
// with "concurrent map writes").
func TestDefPatternCache_ConcurrentDistinctSymbols_NoRace(t *testing.T) {
	const nSymbols = 64
	symbols := make([]string, nSymbols)
	for i := range symbols {
		symbols[i] = fmt.Sprintf("WidgetSymbol%d", i)
	}
	const content = "package p\nfunc WidgetSymbol0(x int) error { return nil }\n" +
		"class Foo {}\ntype Bar struct{}\n"

	// Cold cache so every symbol is a miss → compile → write.
	defPatternMu.Lock()
	defPatternCache = map[string]defPattern{}
	defPatternMu.Unlock()

	const workers = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range workers {
		wg.Go(func() {
			<-start // release all workers together to maximize overlap
			for _, s := range symbols {
				_ = chunkDefinesSymbol(content, s) // → definitionPattern(s)
			}
		})
	}
	close(start)
	wg.Wait()

	defPatternMu.RLock()
	n := len(defPatternCache)
	defPatternMu.RUnlock()
	if n < nSymbols {
		t.Fatalf("cache has %d entries, want %d — distinct symbols weren't all memoized", n, nSymbols)
	}
}
