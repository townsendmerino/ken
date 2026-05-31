package coderank

import (
	"errors"
	"io/fs"
	"math"
	"os"
	"testing"
	"time"
)

// TestForwardBatch_matchesSingle: for every input, the batched forward
// must produce the same CLS vector (within f32 reduction-order
// tolerance) as the single-sequence forward. f32 matmul order changes
// with M (the batched path has M = B*Lmax instead of L), so a ULP-level
// difference is expected; the meaningful bar is cosine ≥ 0.997 per the
// M2 acceptance contract.
//
// Inputs: hand-mixed lengths to exercise the padding path AND the
// degenerate cases (L=2 [CLS][SEP] only, max L). Skipped without the
// model dir.
func TestForwardBatch_matchesSingle(t *testing.T) {
	if _, err := os.Stat(modelDir); errors.Is(err, fs.ErrNotExist) {
		t.Skipf("no model at %s", modelDir)
	}
	m, err := Load(modelDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Hand-built batch with intentional length variance — ensures the
	// padding + per-sequence attention loop fires on real ragged input.
	texts := []string{
		"hello",
		"how do i parse json",
		"def add(a, b):\n    return a + b\n# pad",
		"class Dog:\n    def bark(self):\n        print('woof')",
		"def fib(n):\n    if n < 2: return n\n    return fib(n-1) + fib(n-2)",
	}
	isQ := []bool{true, true, false, false, false}

	// Reference: single-sequence outputs.
	want := make([][]float32, len(texts))
	for i := range texts {
		v, err := m.Encode(texts[i], isQ[i])
		if err != nil {
			t.Fatalf("[%d] Encode: %v", i, err)
		}
		want[i] = v
	}

	// Batched: encode them all together as a real batch by calling
	// the package-level forwardBatch on tokenized ids directly so the
	// codepath through forwardBatch is exercised (EncodeBatch routes
	// through it too, but bypassing the worker split here keeps the
	// test focused on the batched forward kernel).
	idsList := make([][]int32, len(texts))
	for i := range texts {
		var ids []int32
		if isQ[i] {
			ids, err = EncodeQuery(m.tok, texts[i], m.maxSeqLength)
		} else {
			ids, err = EncodeDoc(m.tok, texts[i], m.maxSeqLength)
		}
		if err != nil {
			t.Fatalf("[%d] tokenize: %v", i, err)
		}
		idsList[i] = ids
	}
	got := m.weights.forwardBatch(idsList)
	if len(got) != len(texts) {
		t.Fatalf("len: got %d want %d", len(got), len(texts))
	}

	for i := range texts {
		if len(got[i]) != len(want[i]) {
			t.Errorf("[%d] dim: got %d want %d", i, len(got[i]), len(want[i]))
			continue
		}
		// Compare via cosine, not bit-equality — f32 reduction order
		// changes between single (M=L) and batched (M=B*Lmax). M2's
		// 0.997 acceptance bar applies the same way here.
		cos := cosineF32(got[i], want[i])
		if cos < 0.997 {
			// Diagnostic: max-abs componentwise diff.
			var maxAbs float32
			for j := range got[i] {
				d := got[i][j] - want[i][j]
				if d < 0 {
					d = -d
				}
				if d > maxAbs {
					maxAbs = d
				}
			}
			t.Errorf("[%d] cosine %.6f < 0.997 (text=%q, maxAbs=%v)",
				i, cos, previewSnippet(texts[i]), maxAbs)
		} else {
			t.Logf("[%d] cosine=%.6f (text=%q)", i, cos, previewSnippet(texts[i]))
		}
	}
}

// TestForwardBatch_edgeCases: empty list, B=1 fast path, empty
// sequences ([CLS][SEP] only) — the corner cases that would silently
// segfault without the guards.
func TestForwardBatch_edgeCases(t *testing.T) {
	if _, err := os.Stat(modelDir); errors.Is(err, fs.ErrNotExist) {
		t.Skipf("no model at %s", modelDir)
	}
	m, err := Load(modelDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Empty list.
	if got := m.weights.forwardBatch(nil); got != nil {
		t.Errorf("nil input: got %v want nil", got)
	}

	// B=1 — should match single-sequence forward exactly (no padding).
	ids, _ := EncodeDoc(m.tok, "def foo(): pass", m.maxSeqLength)
	batchedOut := m.weights.forwardBatch([][]int32{ids})
	singleOut := m.weights.forward(ids)
	if len(batchedOut) != 1 || len(batchedOut[0]) != len(singleOut) {
		t.Fatalf("B=1 shape mismatch")
	}
	// B=1 short-circuits to forward(), so this MUST be bit-equal.
	for j := range singleOut {
		if batchedOut[0][j] != singleOut[j] {
			t.Errorf("B=1 should bit-match single forward; pos %d: %v vs %v",
				j, batchedOut[0][j], singleOut[j])
			break
		}
	}

	// All sequences L=0 (degenerate).
	zeroBatch := [][]int32{{}, {}, {}}
	got := m.weights.forwardBatch(zeroBatch)
	if len(got) != 3 {
		t.Fatalf("len: got %d want 3", len(got))
	}
	D := m.HiddenDim()
	for i, v := range got {
		if len(v) != D {
			t.Errorf("[%d] empty-seq dim: got %d want %d", i, len(v), D)
		}
		for j, x := range v {
			if x != 0 {
				t.Errorf("[%d] empty-seq[%d]: got %v want 0", i, j, x)
				break
			}
		}
	}
}

// TestEncodeBatch_perWorkerBatched: post-M7, EncodeBatch routes through
// forwardBatch per worker. Output for each (text, isQuery) MUST still
// match single-Encode within cosine 0.997 on every position.
func TestEncodeBatch_perWorkerBatched(t *testing.T) {
	if _, err := os.Stat(modelDir); errors.Is(err, fs.ErrNotExist) {
		t.Skipf("no model at %s", modelDir)
	}
	m, err := Load(modelDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	texts := []string{
		"how do i parse json",
		"def add(a, b):\n    return a + b",
		"compute the sha256 hash of a file",
		"recursive directory walk that respects gitignore",
		"class Dog:\n    def bark(self):\n        print('woof')",
		"async def fetch(url):\n    pass",
		"def fib(n):\n    if n < 2: return n",
		"import os\nos.walk('.')",
	}
	isQ := []bool{true, false, true, true, false, false, false, false}

	// Reference: sequential Encode.
	want := make([][]float32, len(texts))
	for i := range texts {
		v, _ := m.Encode(texts[i], isQ[i])
		want[i] = v
	}

	// Batched: EncodeBatch with concurrency=4 splits into ~2 chunks.
	t0 := time.Now()
	got, err := m.EncodeBatch(texts, isQ, 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("EncodeBatch wall: %v", time.Since(t0))

	for i := range texts {
		cos := cosineF32(got[i], want[i])
		if cos < 0.997 {
			t.Errorf("[%d] cosine %.6f < 0.997 (text=%q)",
				i, cos, previewSnippet(texts[i]))
		}
	}
}

func cosineF32(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

func previewSnippet(s string) string {
	if len(s) > 50 {
		return s[:50] + "..."
	}
	return s
}
