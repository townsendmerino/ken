package search

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/townsendmerino/aikit/bm25"
	"github.com/townsendmerino/aikit/chunk"
)

// makeTokChunks builds n chunks with distinct, tokenizer-exercising text
// (identifiers with camelCase/snake_case so Tokenize does real work).
// n is chosen > 64 in the callers that want the parallel path.
func makeTokChunks(n int) []chunk.Chunk {
	cs := make([]chunk.Chunk, n)
	for i := range cs {
		cs[i] = chunk.Chunk{
			File: fmt.Sprintf("f%d.go", i),
			Text: fmt.Sprintf("func handleRequest%d(userID int) { parseToken_%d(userID) }", i, i),
		}
	}
	return cs
}

// TestTokenizeDocs_ParallelEqualsSerial: the NumCPU-worker tokenize must
// produce byte-identical per-chunk token lists to a straight serial
// bm25.Tokenize — the byte-stability contract the build path preserves.
func TestTokenizeDocs_ParallelEqualsSerial(t *testing.T) {
	chunks := makeTokChunks(500) // > 64 → exercises the parallel branch
	chunks[7].Tombstoned = true
	chunks[300].Tombstoned = true

	got := tokenizeDocs(chunks, nil)
	if len(got) != len(chunks) {
		t.Fatalf("len(docs)=%d, want %d", len(got), len(chunks))
	}
	for i := range chunks {
		if chunks[i].Tombstoned {
			if got[i] != nil {
				t.Errorf("docs[%d] for tombstoned chunk = %v, want nil", i, got[i])
			}
			continue
		}
		want := bm25.Tokenize(chunks[i].Text)
		if !reflect.DeepEqual(got[i], want) {
			t.Errorf("docs[%d] = %v, want %v", i, got[i], want)
		}
	}
}

// TestTokenizeDocs_CacheEqualsFresh: with a cache, the tokens are still
// identical to the fresh path — the cache is a pure memoization, never a
// behavior change.
func TestTokenizeDocs_CacheEqualsFresh(t *testing.T) {
	chunks := makeTokChunks(120)
	cache := newTokenCache()
	fresh := tokenizeDocs(chunks, nil)
	cached := tokenizeDocs(chunks, cache)
	if !reflect.DeepEqual(fresh, cached) {
		t.Fatal("cached tokenize != fresh tokenize")
	}
}

// TestTokenizeDocs_CacheReusesAndEvicts is the audit §5 core: on the
// second pass only the edited chunk is re-tokenized (others reuse the
// cached slice), and the edited-away text is evicted from the cache.
func TestTokenizeDocs_CacheReusesAndEvicts(t *testing.T) {
	chunks := makeTokChunks(100)
	cache := newTokenCache()
	first := tokenizeDocs(chunks, cache)

	oldText := chunks[42].Text
	// Simulate a single-file re-chunk: one chunk's text changes.
	chunks[42].Text = "func brandNewSymbol(x int) { totallyDifferent(x) }"
	second := tokenizeDocs(chunks, cache)

	for i := range chunks {
		if i == 42 {
			// Re-tokenized: value must match the new text and differ
			// from the old cached slice.
			want := bm25.Tokenize(chunks[i].Text)
			if !reflect.DeepEqual(second[i], want) {
				t.Errorf("docs[42] not re-tokenized to new text")
			}
			continue
		}
		// Unchanged chunks must REUSE the exact cached slice (same
		// backing array), proving they weren't re-tokenized.
		if len(first[i]) > 0 && &first[i][0] != &second[i][0] {
			t.Errorf("docs[%d] was re-tokenized (new backing array) instead of reused from cache", i)
		}
	}

	// The edited-away text must be gone from the cache (bounded growth).
	if _, stale := cache.byHash[hashText64(oldText)]; stale {
		t.Errorf("cache still holds evicted text %q", oldText)
	}
	// The cache holds exactly the live chunks (100), not 101.
	if len(cache.byHash) != 100 {
		t.Errorf("cache size = %d, want 100 (one per live chunk, stale evicted)", len(cache.byHash))
	}
}

// TestBuildIndex_CachedDocsEqualFreshIndex: an index built via the cached
// docs path yields identical BM25 search results to a fresh BuildIndex
// over the same chunks — the integration guarantee that §5 is invisible
// to retrieval.
func TestBuildIndex_CachedDocsEqualFreshIndex(t *testing.T) {
	chunks := makeTokChunks(150)
	cache := newTokenCache()

	fresh := BuildIndex(chunks, nil, ModeBM25, nil)
	cachedDocs := tokenizeDocs(chunks, cache)
	viaCache := buildIndexFromDocs(chunks, cachedDocs, nil, ModeBM25, nil)

	for _, q := range []string{"handleRequest42", "parseToken", "userID"} {
		a := fresh.Search(q, 10)
		b := viaCache.Search(q, 10)
		if len(a) != len(b) {
			t.Fatalf("query %q: fresh got %d hits, cached got %d", q, len(a), len(b))
		}
		for i := range a {
			if a[i].Chunk.File != b[i].Chunk.File {
				t.Errorf("query %q rank %d: fresh=%s cached=%s", q, i, a[i].Chunk.File, b[i].Chunk.File)
			}
		}
	}
}
