package search

import (
	"testing"
)

// TestEmbeddingLRU_evicts: the simplest correctness contract — once
// the cache is at capacity, the LEAST-recently-used entry leaves and
// touching (get) on an entry promotes it to MRU.
func TestEmbeddingLRU_evicts(t *testing.T) {
	c := newEmbeddingLRU(3)
	c.put(1, []float32{1})
	c.put(2, []float32{2})
	c.put(3, []float32{3})
	// All three present; size==3.
	if _, _, size := c.stats(); size != 3 {
		t.Fatalf("size: got %d want 3", size)
	}
	// Touch 1 ⇒ now MRU. Put 4 ⇒ evicts 2 (LRU).
	if _, ok := c.get(1); !ok {
		t.Fatal("get(1) should hit")
	}
	c.put(4, []float32{4})
	if _, ok := c.get(2); ok {
		t.Error("get(2) should have been evicted")
	}
	if _, ok := c.get(1); !ok {
		t.Error("get(1) should still hit (was MRU)")
	}
	if _, ok := c.get(3); !ok {
		t.Error("get(3) should still hit")
	}
	if _, ok := c.get(4); !ok {
		t.Error("get(4) should hit (just put)")
	}
}

func TestEmbeddingLRU_zeroCapDisablesCache(t *testing.T) {
	c := newEmbeddingLRU(0)
	c.put(1, []float32{1})
	if _, ok := c.get(1); ok {
		t.Error("cap=0 should never retain entries")
	}
	if _, _, size := c.stats(); size != 0 {
		t.Errorf("size: got %d want 0", size)
	}
}

// TestEmbeddingLRU_stats: hit / miss / size counters track correctly.
func TestEmbeddingLRU_stats(t *testing.T) {
	c := newEmbeddingLRU(2)
	_, _ = c.get(99) // miss
	c.put(1, []float32{1})
	_, _ = c.get(1) // hit
	_, _ = c.get(1) // hit
	_, _ = c.get(2) // miss
	h, m, sz := c.stats()
	if h != 2 || m != 2 || sz != 1 {
		t.Errorf("got hits=%d misses=%d size=%d, want 2/2/1", h, m, sz)
	}
}

func TestEmbeddingLRU_putRefreshesExistingKey(t *testing.T) {
	c := newEmbeddingLRU(2)
	c.put(1, []float32{1})
	c.put(2, []float32{2})
	c.put(1, []float32{10}) // refresh
	if v, ok := c.get(1); !ok || v[0] != 10 {
		t.Errorf("get(1): got %v ok=%v, want [10] true", v, ok)
	}
	if _, _, sz := c.stats(); sz != 2 {
		t.Errorf("size: got %d want 2 (refresh shouldn't grow)", sz)
	}
}

func TestFnvHash_stableAndDistinct(t *testing.T) {
	// Stable across calls.
	if fnvHash("hello") != fnvHash("hello") {
		t.Error("fnvHash should be deterministic")
	}
	// Distinct for distinct inputs (trivial collision check).
	if fnvHash("a") == fnvHash("b") {
		t.Error("fnvHash collision on 'a' vs 'b'")
	}
	if fnvHash("") == fnvHash("\x00") {
		t.Error("fnvHash collision on empty vs null-byte (FNV offset basis differs)")
	}
}

// TestL2NormalizeCopy: zero-norm input stays zero (no NaN); non-zero
// input normalizes to unit L2; original input is not mutated.
func TestL2NormalizeCopy(t *testing.T) {
	zero := []float32{0, 0, 0}
	got := l2NormalizeCopy(zero)
	for _, v := range got {
		if v != 0 {
			t.Errorf("zero-norm: got %v want 0", v)
		}
	}
	// Non-zero.
	in := []float32{3, 4, 0}
	got = l2NormalizeCopy(in)
	if in[0] != 3 || in[1] != 4 || in[2] != 0 {
		t.Errorf("input mutated: %v", in)
	}
	var sumSq float64
	for _, v := range got {
		sumSq += float64(v) * float64(v)
	}
	if diff := sumSq - 1.0; diff < -1e-6 || diff > 1e-6 {
		t.Errorf("L2 norm of result: got %v want ~1", sumSq)
	}
}

func TestDot64_basic(t *testing.T) {
	a := []float32{1, 0, 0}
	b := []float32{0, 1, 0}
	if got := dot64(a, b); got != 0 {
		t.Errorf("orthogonal: got %v want 0", got)
	}
	a = []float32{1, 0, 0}
	b = []float32{1, 0, 0}
	if got := dot64(a, b); got != 1 {
		t.Errorf("parallel: got %v want 1", got)
	}
}
