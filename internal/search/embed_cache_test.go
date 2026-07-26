package search

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/townsendmerino/aikit/embed"
)

func TestHashText_Deterministic(t *testing.T) {
	a, b, c := hashText("func Login()"), hashText("func Login()"), hashText("other")
	if string(a) != string(b) {
		t.Error("same text must hash the same")
	}
	if string(a) == string(c) {
		t.Error("different text must hash differently")
	}
	if len(a) != 32 {
		t.Errorf("sha256 length = %d, want 32", len(a))
	}
}

// fakeVecCache is an in-memory VecCache for testing encodeCached without the
// SQLite impl (which lives in internal/embedcache).
type fakeVecCache struct {
	store map[string][]float32
	gets  int
	puts  int
}

func (f *fakeVecCache) Get(k []byte) ([]float32, bool) {
	f.gets++
	v, ok := f.store[string(k)]
	return v, ok
}
func (f *fakeVecCache) Put(k []byte, v []float32) {
	f.puts++
	f.store[string(k)] = v
}

// TestEncodeCached_HitSkipsEncode: a cache miss encodes + stores; a subsequent
// hit returns the cached vector without re-encoding. Needs a real model.
func TestEncodeCached_HitSkipsEncode(t *testing.T) {
	md := filepath.Join("..", "..", "testdata", "model")
	if _, err := os.Stat(filepath.Join(md, "model.safetensors")); err != nil {
		t.Skip("testdata/model not present; see testdata/README.md")
	}
	model, err := embed.LoadFromFS(os.DirFS(md), ".")
	if err != nil {
		t.Fatalf("load model: %v", err)
	}
	fake := &fakeVecCache{store: map[string][]float32{}}

	v1 := encodeCached(fake, model, "hello world")
	if fake.puts != 1 {
		t.Fatalf("first call: puts=%d, want 1 (miss → store)", fake.puts)
	}
	v2 := encodeCached(fake, model, "hello world")
	if fake.puts != 1 {
		t.Errorf("second call must not re-store: puts=%d, want 1", fake.puts)
	}
	if len(v1) == 0 || len(v1) != len(v2) {
		t.Fatalf("vec length mismatch: %d vs %d", len(v1), len(v2))
	}
	for i := range v1 {
		if v1[i] != v2[i] {
			t.Errorf("cached hit vector differs at %d", i)
			break
		}
	}

	// nil cache → encode directly (non-nil); nil model → nil.
	if encodeCached(nil, model, "x") == nil {
		t.Error("nil cache should encode directly")
	}
	if encodeCached(fake, nil, "x") != nil {
		t.Error("nil model should yield nil")
	}
}
