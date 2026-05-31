package coderank

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"runtime"
	"testing"
	"time"
)

// TestModelQ8_cosineMatchesF32: M8 acceptance bar. The int8 model
// must produce vectors that match the f32 model within cosine ≥ 0.97
// (per plan §11's loose-cosine SECONDARY bar — end-to-end NDCG is the
// primary gate, but we want a per-input sanity check too).
//
// Per-matmul int8 quantization introduces ~0.8% relative L2 error
// (TestMatmulBTQ8_endToEndError). That error accumulates additively
// through 12 layers × 5 big matmuls each (60 q8 matmuls total) but
// gets attenuated by the LayerNorm renormalization between layers,
// so the END-to-END cosine drift on real model weights typically
// lands at 0.99+, not the worst-case 1 - 60×0.008 = 0.52.
func TestModelQ8_cosineMatchesF32(t *testing.T) {
	if _, err := os.Stat(modelDir); errors.Is(err, fs.ErrNotExist) {
		t.Skipf("no model at %s", modelDir)
	}
	mF32, err := Load(modelDir)
	if err != nil {
		t.Fatal(err)
	}
	mQ8, err := LoadQ8(modelDir)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		text    string
		isQuery bool
	}{
		{"hello world", true},
		{"how do i parse json", true},
		{"def add(a, b):\n    return a + b", false},
		{"class Dog:\n    def bark(self):\n        print('woof')", false},
		{"compute the sha256 hash of a file", true},
		{"def sha256_of(path):\n    import hashlib\n    h = hashlib.sha256()\n    return h.hexdigest()", false},
	}

	for _, c := range cases {
		vF32, err := mF32.Encode(c.text, c.isQuery)
		if err != nil {
			t.Fatal(err)
		}
		vQ8, err := mQ8.Encode(c.text, c.isQuery)
		if err != nil {
			t.Fatal(err)
		}
		if len(vF32) != len(vQ8) {
			t.Fatalf("dim mismatch: f32=%d q8=%d", len(vF32), len(vQ8))
		}
		cos := cosineF32(vF32, vQ8)
		if cos < 0.97 {
			t.Errorf("cosine %.6f < 0.97 (text=%q)", cos, previewSnippet(c.text))
		} else {
			t.Logf("cosine=%.6f (text=%q)", cos, previewSnippet(c.text))
		}
	}
}

// TestModelQ8_batchedConsistency: the batched q8 encoder must produce
// the same per-position output as the single-sequence q8 encoder,
// within reduction-order tolerance (cosine ≥ 0.997 — the same M2
// contract Model.forwardBatch holds against Model.forward).
func TestModelQ8_batchedConsistency(t *testing.T) {
	if _, err := os.Stat(modelDir); errors.Is(err, fs.ErrNotExist) {
		t.Skipf("no model at %s", modelDir)
	}
	m, err := LoadQ8(modelDir)
	if err != nil {
		t.Fatal(err)
	}
	texts := []string{
		"hello",
		"def fib(n): return n",
		"class Foo:\n    pass",
	}
	isQ := []bool{true, false, false}

	want := make([][]float32, len(texts))
	for i := range texts {
		want[i], _ = m.Encode(texts[i], isQ[i])
	}
	got, _ := m.EncodeBatch(texts, isQ, 2)
	for i := range texts {
		cos := cosineF32(got[i], want[i])
		if cos < 0.997 {
			t.Errorf("[%d] cosine %.6f < 0.997 (text=%q)", i, cos, previewSnippet(texts[i]))
		}
	}
}

// TestModelQ8_rerankN50_latency: the headline M8 latency probe. Same
// rerankN=50 workload as TestEncodeBatch_rerankN50; this is the
// number that decides whether int8 actually moves the wall.
func TestModelQ8_rerankN50_latency(t *testing.T) {
	if testing.Short() {
		t.Skip("rerankN=50 measurement runs ~10-20s; skipped under -short")
	}
	if _, err := os.Stat(modelDir); errors.Is(err, fs.ErrNotExist) {
		t.Skipf("no model at %s", modelDir)
	}
	m, err := LoadQ8(modelDir)
	if err != nil {
		t.Fatal(err)
	}
	templates := []string{
		"def %s(x):\n    if x < 2:\n        return x\n    return %s(x-1) + %s(x-2)",
		"class %s:\n    def __init__(self, name):\n        self.name = name\n    def %s(self):\n        return self.name",
		"async def %s(url):\n    async with httpx.AsyncClient() as c:\n        r = await c.get(url)\n        return r.json()",
		"func %s(in []byte) ([]byte, error) {\n    if len(in) == 0 {\n        return nil, fmt.Errorf(\"empty\")\n    }\n    return in, nil\n}",
		"impl %s for Foo {\n    fn bar(&self) -> Result<()> {\n        self.inner.lock().unwrap().do_thing();\n        Ok(())\n    }\n}",
	}
	names := []string{
		"alpha", "beta", "gamma", "delta", "epsilon", "zeta", "eta", "theta",
		"iota", "kappa", "lambda", "mu", "nu", "xi", "omicron", "pi",
		"rho", "sigma", "tau", "upsilon", "phi", "chi", "psi", "omega",
		"add", "sub", "mul", "div", "mod", "pow", "log", "exp",
		"sin", "cos", "tan", "asin", "acos", "atan", "min", "max",
		"sort", "reverse", "shuffle", "merge", "split", "concat", "filter", "map",
		"reduce", "fold",
	}
	N := 50
	texts := make([]string, N)
	isQ := make([]bool, N)
	for i := 0; i < N; i++ {
		texts[i] = fmt.Sprintf(templates[i%len(templates)], names[i], names[i], names[i])
	}
	t0 := time.Now()
	out, err := m.EncodeBatch(texts, isQ, 0)
	if err != nil {
		t.Fatal(err)
	}
	took := time.Since(t0)
	t.Logf("[q8] rerankN=%d cold, NumCPU=%d: %v wall (%.0fms/candidate amortized)",
		N, runtime.NumCPU(), took, float64(took.Milliseconds())/float64(N))
	if len(out) != N {
		t.Fatalf("got %d outputs, want %d", len(out), N)
	}
}
