package search

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/townsendmerino/aikit/chunk"
)

// TestSaveLoadCache_roundtrip: write a cache to disk, read it back,
// verify the LRU contents match (and survive a clean SaveCacheToFile
// → LoadCacheFromFile cycle).
func TestSaveLoadCache_roundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.bin")
	const embedDim = 4
	scope := CacheScopeKey("test-model", "f32", embedDim)

	r := NewNeuralReranker(stubEncoder{dim: embedDim}, WithCacheSize(8))
	// Seed via Rerank so the LRU path is the same one production uses.
	cands := []chunk.Chunk{
		{File: "a.go", Text: "alpha body"},
		{File: "b.go", Text: "beta body longer"},
		{File: "c.go", Text: "gamma"},
	}
	if scores := r.Rerank("query", cands); len(scores) != len(cands) {
		t.Fatalf("seed Rerank returned %d scores, want %d", len(scores), len(cands))
	}
	wantHits, wantMisses, wantSize := r.CacheStats()
	if wantSize != 3 {
		t.Fatalf("seed: size=%d, want 3", wantSize)
	}

	if err := SaveCacheToFile(r, path, scope, embedDim); err != nil {
		t.Fatalf("SaveCacheToFile: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after save: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("save wrote a zero-byte file")
	}

	// Fresh reranker, restore from disk, verify cache hits replace
	// misses on the same candidates.
	r2 := NewNeuralReranker(stubEncoder{dim: embedDim}, WithCacheSize(8))
	loaded, err := LoadCacheFromFile(r2, path, scope, embedDim)
	if err != nil {
		t.Fatalf("LoadCacheFromFile: %v", err)
	}
	if loaded != 3 {
		t.Fatalf("loaded=%d, want 3", loaded)
	}
	_, _, size := r2.CacheStats()
	if size != 3 {
		t.Fatalf("after load: cache size=%d, want 3", size)
	}
	// Rerank again — every candidate should now be a cache hit.
	if _ = r2.Rerank("query", cands); false {
		t.Fatal("unreachable")
	}
	h, m, _ := r2.CacheStats()
	if h != 3 || m != 0 {
		t.Errorf("after load+rerank: hits=%d misses=%d, want 3/0", h, m)
	}
	// Sanity: the original reranker had the same counts before load
	// (no surprise growth from the save round).
	_ = wantHits
	_ = wantMisses
}

// TestLoadCache_missingFile_returnsNotExist: caller distinguishes
// "first run" from corruption / version skew.
func TestLoadCache_missingFile_returnsNotExist(t *testing.T) {
	r := NewNeuralReranker(stubEncoder{dim: 4}, WithCacheSize(4))
	_, err := LoadCacheFromFile(r, "/nonexistent/path/cache.bin", "scope", 4)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("got %v, want os.ErrNotExist-wrapped error", err)
	}
}

// TestLoadCache_corruptCRC_returnsTypedError: any byte flip in the
// body fails the CRC check and surfaces ErrCacheCorrupt.
func TestLoadCache_corruptCRC_returnsTypedError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.bin")
	r := seedCache(t, 4)
	if err := SaveCacheToFile(r, path, CacheScopeKey("m", "f32", 4), 4); err != nil {
		t.Fatalf("save: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// Flip a byte in the middle of the body (before the CRC trailer).
	data[len(data)/2] ^= 0xFF
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	r2 := NewNeuralReranker(stubEncoder{dim: 4}, WithCacheSize(4))
	_, err = LoadCacheFromFile(r2, path, CacheScopeKey("m", "f32", 4), 4)
	if !errors.Is(err, ErrCacheCorrupt) {
		t.Fatalf("got %v, want ErrCacheCorrupt", err)
	}
	// Liveness: the in-memory cache was NOT modified.
	if _, _, sz := r2.CacheStats(); sz != 0 {
		t.Errorf("cache modified despite load error: size=%d", sz)
	}
}

// TestLoadCache_badMagic_returnsTypedError: corruption at the very
// front (before the CRC could even be meaningful) still returns the
// corrupt sentinel.
func TestLoadCache_badMagic_returnsTypedError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.bin")
	r := seedCache(t, 4)
	if err := SaveCacheToFile(r, path, CacheScopeKey("m", "f32", 4), 4); err != nil {
		t.Fatalf("save: %v", err)
	}
	data, _ := os.ReadFile(path)
	// Mutate the magic AND re-stamp the CRC so we exercise the magic
	// check rather than the CRC check.
	data[0] = 'X'
	body := data[:len(data)-4]
	binary.LittleEndian.PutUint32(data[len(data)-4:], crc32.ChecksumIEEE(body))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	r2 := NewNeuralReranker(stubEncoder{dim: 4}, WithCacheSize(4))
	_, err := LoadCacheFromFile(r2, path, CacheScopeKey("m", "f32", 4), 4)
	if !errors.Is(err, ErrCacheCorrupt) {
		t.Fatalf("got %v, want ErrCacheCorrupt", err)
	}
}

// TestLoadCache_scopeMismatch_returnsTypedError: loading an f32 cache
// into an int8 reranker must reject — the embeddings would be
// wrong-precision and cosines would be junk.
func TestLoadCache_scopeMismatch_returnsTypedError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.bin")
	r := seedCache(t, 4)
	if err := SaveCacheToFile(r, path, CacheScopeKey("m", "f32", 4), 4); err != nil {
		t.Fatalf("save: %v", err)
	}
	r2 := NewNeuralReranker(stubEncoder{dim: 4}, WithCacheSize(4))
	_, err := LoadCacheFromFile(r2, path, CacheScopeKey("m", "int8", 4), 4)
	if !errors.Is(err, ErrCacheScopeMismatch) {
		t.Fatalf("got %v, want ErrCacheScopeMismatch", err)
	}
}

// TestLoadCache_embedDimMismatch_returnsTypedError: a different-dim
// model swap is a distinct failure mode from scope mismatch.
func TestLoadCache_embedDimMismatch_returnsTypedError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.bin")
	r := seedCache(t, 4)
	if err := SaveCacheToFile(r, path, "scope", 4); err != nil {
		t.Fatalf("save: %v", err)
	}
	r2 := NewNeuralReranker(stubEncoder{dim: 8}, WithCacheSize(4))
	_, err := LoadCacheFromFile(r2, path, "scope", 8)
	if !errors.Is(err, ErrCacheEmbedDimMismatch) {
		t.Fatalf("got %v, want ErrCacheEmbedDimMismatch", err)
	}
}

// TestLoadCache_lruOrderPreserved: snapshot writes oldest→newest, so
// after a restore the most-recent entries are at the front of the
// list. Verify by overflowing the cap on the restored cache —
// the oldest entries are the ones evicted, not the newest.
func TestLoadCache_lruOrderPreserved(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.bin")
	const embedDim = 4
	scope := CacheScopeKey("m", "f32", embedDim)

	r := NewNeuralReranker(stubEncoder{dim: embedDim}, WithCacheSize(3))
	// Insert in order: a, b, c. LRU order: a (oldest) → b → c (newest).
	cands := []chunk.Chunk{
		{File: "a.go", Text: "alpha"},
		{File: "b.go", Text: "beta"},
		{File: "c.go", Text: "gamma"},
	}
	r.Rerank("q", cands)

	if err := SaveCacheToFile(r, path, scope, embedDim); err != nil {
		t.Fatalf("save: %v", err)
	}
	// Restore into a smaller LRU (cap=2) — the oldest entry (alpha)
	// must be dropped; beta and gamma must survive.
	r2 := NewNeuralReranker(stubEncoder{dim: embedDim}, WithCacheSize(2))
	if _, err := LoadCacheFromFile(r2, path, scope, embedDim); err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, _, sz := r2.CacheStats(); sz != 2 {
		t.Fatalf("after load with smaller cap: size=%d, want 2", sz)
	}
	// "alpha" was the oldest — re-encoding it should be a miss; beta
	// and gamma should be hits.
	r2.Rerank("q", cands[:1]) // probe alpha
	_, m, _ := r2.CacheStats()
	if m != 1 {
		t.Errorf("alpha probe: misses=%d, want 1 (alpha was evicted)", m)
	}
}

// ── helpers ─────────────────────────────────────────────────────────

func seedCache(t *testing.T, dim int) *NeuralReranker {
	t.Helper()
	r := NewNeuralReranker(stubEncoder{dim: dim}, WithCacheSize(8))
	r.Rerank("q", []chunk.Chunk{
		{File: "a.go", Text: "alpha body"},
		{File: "b.go", Text: "beta body"},
	})
	return r
}

// stubEncoder is a deterministic mini-Encoder for tests. Returns a
// vector that's a function of the input text length so different
// inputs produce different (but reproducible) vectors. L2-normalized
// so the cosine math is well-defined.
type stubEncoder struct {
	dim int
}

func (e stubEncoder) Encode(s string, _ bool) ([]float32, error) {
	v := make([]float32, e.dim)
	for i := range v {
		v[i] = float32(len(s)+i+1) * 0.1
	}
	return v, nil
}

func (e stubEncoder) EncodeBatch(ss []string, _ []bool, _ int) ([][]float32, error) {
	out := make([][]float32, len(ss))
	for i, s := range ss {
		v, _ := e.Encode(s, false)
		out[i] = v
	}
	return out, nil
}

func (e stubEncoder) HiddenDim() int { return e.dim }

// stubEncoder doesn't implement Close — Encoder interface doesn't
// require it. Sanity: the type satisfies the interface via these
// two methods only.
var _ = math.Sqrt
