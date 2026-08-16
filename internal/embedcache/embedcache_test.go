package embedcache

import (
	"path/filepath"
	"testing"
)

func TestVecSerializationRoundtrip(t *testing.T) {
	in := []float32{0, 1, -1, 3.14159, 1e-9, -2.5e8}
	out := bytesToVec(vecToBytes(in))
	if len(out) != len(in) {
		t.Fatalf("len %d != %d", len(out), len(in))
	}
	for i := range in {
		if out[i] != in[i] {
			t.Errorf("[%d] = %v, want %v", i, out[i], in[i])
		}
	}
	if bytesToVec([]byte{1, 2, 3}) != nil {
		t.Error("non-multiple-of-4 bytes should decode to nil")
	}
}

func TestPutGetRoundtrip(t *testing.T) {
	c, err := Open(filepath.Join(t.TempDir(), "embed.db"), "modelA", 4, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = c.Close() }()

	key := []byte("chunk-hash-1")
	if _, ok := c.Get(key); ok {
		t.Fatal("miss expected before put")
	}
	want := []float32{0.1, 0.2, 0.3, 0.4}
	c.Put(key, want)
	got, ok := c.Get(key)
	if !ok || len(got) != len(want) {
		t.Fatalf("get ok=%v len=%d, want true/%d", ok, len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

// TestScopeInvalidation: reopening under a different model fingerprint or dim
// truncates the cache (old vectors are in the wrong space).
func TestScopeInvalidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "embed.db")
	key := []byte("k")

	c1, err := Open(path, "modelA", 256, 0)
	if err != nil {
		t.Fatal(err)
	}
	c1.Put(key, []float32{1, 2})
	if _, ok := c1.Get(key); !ok {
		t.Fatal("entry should be present in c1")
	}
	_ = c1.Close()

	c2, _ := Open(path, "modelA", 256, 0) // same → survives
	if _, ok := c2.Get(key); !ok {
		t.Error("entry should survive a same-model reopen")
	}
	_ = c2.Close()

	c3, _ := Open(path, "modelB", 256, 0) // different model → cleared
	if _, ok := c3.Get(key); ok {
		t.Error("entry should be gone after a model-fingerprint change")
	}
	c3.Put(key, []float32{3})
	_ = c3.Close()

	c4, _ := Open(path, "modelB", 512, 0) // different dim → cleared
	if _, ok := c4.Get(key); ok {
		t.Error("entry should be gone after a dim change")
	}
	_ = c4.Close()
}

func TestEviction(t *testing.T) {
	c, err := Open(filepath.Join(t.TempDir(), "embed.db"), "m", 4, 3) // cap 3
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	for i := range 6 {
		c.Put([]byte{byte('a' + i)}, []float32{float32(i)})
	}
	// Puts buffer in memory; force a flush (which evicts) to hit the store.
	c.mu.Lock()
	c.flushLocked()
	c.mu.Unlock()
	var n int
	if err := c.db.QueryRow(`SELECT COUNT(*) FROM vecs`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("after eviction count = %d, want 3 (cap)", n)
	}
	// Which 3 survive is unspecified within a single batched flush (map order);
	// the guarantee is the size bound. Exactly 3 of the 6 must remain.
	remaining := 0
	for i := range 6 {
		if _, ok := c.Get([]byte{byte('a' + i)}); ok {
			remaining++
		}
	}
	if remaining != 3 {
		t.Errorf("remaining entries = %d, want 3 (cap enforced)", remaining)
	}
}
