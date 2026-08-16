package search

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/townsendmerino/aikit/chunk"
)

// TestPeekRerankCacheCount round-trips a real cache written by SaveCacheToFile
// and confirms the header-only peek reports the same entry count LoadCacheFromFile
// would restore — without reading the vectors or the CRC.
func TestPeekRerankCacheCount(t *testing.T) {
	dir := t.TempDir()

	// Absent file → the wrapped os.ErrNotExist (a cold/first-run cache).
	if _, err := PeekRerankCacheCount(filepath.Join(dir, "nope.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("absent file: err = %v, want os.ErrNotExist", err)
	}

	const embedDim = 4
	r := NewNeuralReranker(stubEncoder{dim: embedDim}, WithCacheSize(8))
	// Seed three distinct chunks through the production Rerank path.
	r.Rerank("query", []chunk.Chunk{
		{File: "a.go", Text: "alpha"},
		{File: "b.go", Text: "beta longer body"},
		{File: "c.go", Text: "gamma gamma"},
	})
	path := filepath.Join(dir, "cache.bin")
	if err := SaveCacheToFile(r, path, CacheScopeKey("m", "f32", embedDim), embedDim); err != nil {
		t.Fatalf("SaveCacheToFile: %v", err)
	}

	got, err := PeekRerankCacheCount(path)
	if err != nil {
		t.Fatalf("PeekRerankCacheCount: %v", err)
	}
	if got != 3 {
		t.Errorf("PeekRerankCacheCount = %d, want 3", got)
	}

	// A too-short / bad-magic file is reported corrupt, not a panic.
	bad := filepath.Join(dir, "bad.bin")
	if err := os.WriteFile(bad, []byte("XXXX not a real header"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := PeekRerankCacheCount(bad); err == nil {
		t.Error("bad-magic file should return an error")
	}
}
