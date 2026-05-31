package search

import (
	"testing"

	"github.com/townsendmerino/aikit/chunk"
)

// TestWrapStatic covers the pre-built-index wrapper used by the ken-mcp
// binary: a frozen *Index served through the WatchedIndex shape with no
// watcher goroutine. Verifies reads work, Close is a non-blocking no-op,
// and the SetExtraChunks guard preserves the loaded snapshot instead of
// rebuilding from an empty corpus.
func TestWrapStatic(t *testing.T) {
	chunks := []chunk.Chunk{
		{File: "a.go", StartLine: 1, EndLine: 3, Text: "func Alpha() {}\n"},
		{File: "b.go", StartLine: 1, EndLine: 3, Text: "func Bravo() {}\n"},
	}
	ix := BuildIndex(chunks, nil, ModeBM25, nil)

	wi := WrapStatic(ix, "/some/corpus", ModeBM25, "treesitter")

	if got := wi.Len(); got != 2 {
		t.Fatalf("Len() = %d, want 2", got)
	}
	if res := wi.Search("Alpha", 5); len(res) == 0 {
		t.Errorf("Search(Alpha) returned no results from the static index")
	}

	// Close() must not block — WrapStatic pre-closes done (no watcher
	// goroutine to wait on). A regression here would hang the cache's
	// eviction path.
	done := make(chan struct{})
	go func() { _ = wi.Close(); close(done) }()
	select {
	case <-done:
	default:
		// Close should have completed synchronously; give the goroutine a
		// scheduling beat before declaring a hang.
		<-done
	}

	// SetExtraChunks on a static index is a guarded no-op: it must NOT
	// rebuild from the (empty) live corpus, which would drop the loaded
	// snapshot. Post-call, the original chunks must still be served.
	wi.SetExtraChunks([]chunk.Chunk{
		{File: "db.sql", StartLine: 1, EndLine: 1, Text: "CREATE TABLE t (id int);\n"},
	})
	if got := wi.Len(); got != 2 {
		t.Errorf("after SetExtraChunks on static index: Len() = %d, want 2 (loaded snapshot must be preserved, extras ignored)", got)
	}
}
