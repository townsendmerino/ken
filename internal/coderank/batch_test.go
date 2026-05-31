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

// TestEncodeBatch_matchesEncode: per-position output of EncodeBatch
// must match a sequential Encode loop bit-for-bit. The model is
// immutable and per-call buffers are local, so this should hold
// regardless of worker count — a deviation here means a data race.
func TestEncodeBatch_matchesEncode(t *testing.T) {
	if _, err := os.Stat(modelDir); errors.Is(err, fs.ErrNotExist) {
		t.Skipf("no model at %s — symlink testdata/coderank-model -> HF snapshot", modelDir)
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
	}
	isQ := []bool{true, false, true, true}

	// Reference: one-at-a-time.
	want := make([][]float32, len(texts))
	for i := range texts {
		v, err := m.Encode(texts[i], isQ[i])
		if err != nil {
			t.Fatalf("Encode[%d]: %v", i, err)
		}
		want[i] = v
	}

	// EncodeBatch at several worker counts (1 = degenerate sequential, NumCPU = full fan-out).
	for _, c := range []int{1, 2, runtime.NumCPU()} {
		t.Run(fmt.Sprintf("conc=%d", c), func(t *testing.T) {
			got, err := m.EncodeBatch(texts, isQ, c)
			if err != nil {
				t.Fatalf("EncodeBatch: %v", err)
			}
			if len(got) != len(want) {
				t.Fatalf("len: got %d want %d", len(got), len(want))
			}
			for i := range want {
				if len(got[i]) != len(want[i]) {
					t.Fatalf("[%d] len: got %d want %d", i, len(got[i]), len(want[i]))
				}
				for j := range want[i] {
					if got[i][j] != want[i][j] {
						t.Fatalf("[%d][%d]: got %v want %v", i, j, got[i][j], want[i][j])
					}
				}
			}
		})
	}
}

// TestEncodeBatch_errorPath: an empty-input contract sanity + length-mismatch error.
func TestEncodeBatch_errorPath(t *testing.T) {
	if _, err := os.Stat(modelDir); errors.Is(err, fs.ErrNotExist) {
		t.Skipf("no model at %s", modelDir)
	}
	m, err := Load(modelDir)
	if err != nil {
		t.Fatal(err)
	}
	// Empty: no work, no error.
	got, err := m.EncodeBatch(nil, nil, 0)
	if err != nil || got != nil {
		t.Errorf("empty: got=%v err=%v want nil/nil", got, err)
	}
	// Length mismatch: clear error.
	_, err = m.EncodeBatch([]string{"a", "b"}, []bool{true}, 0)
	if err == nil {
		t.Errorf("expected length-mismatch error")
	}
}

// TestEncodeBatch_speedup: report the wall-time ratio of sequential
// (Encode in a loop) vs parallel (EncodeBatch with NumCPU workers) on
// a realistic rerankN=8 batch of ~80-token inputs. This is the M3
// latency-gate input — we want >= 4× on an 8-core box (linear
// scaling of independent forward passes with some Amdahl loss).
// Reports timings via t.Logf; the bar is forgiving (>= 2×) so this
// test is informative rather than flaky on machines with other load.
func TestEncodeBatch_speedup(t *testing.T) {
	if testing.Short() {
		t.Skip("speedup measurement runs ~50s; skipped under -short")
	}
	if _, err := os.Stat(modelDir); errors.Is(err, fs.ErrNotExist) {
		t.Skipf("no model at %s", modelDir)
	}
	m, err := Load(modelDir)
	if err != nil {
		t.Fatal(err)
	}
	// 8 candidates of roughly-uniform length (~70-90 tokens), so the
	// fan-out has minimal load-imbalance noise.
	texts := []string{
		"def add(a, b):\n    return a + b\n# comment line\nprint('hello')\n# more lines to pad",
		"import json\ndef load(s):\n    return json.loads(s)\n# more code\nx = 1; y = 2",
		"class Dog:\n    def bark(self):\n        print('woof')\n        return None\n# pad",
		"def fib(n):\n    if n < 2:\n        return n\n    return fib(n-1) + fib(n-2)",
		"def sha256_of(path):\n    import hashlib\n    h = hashlib.sha256()\n    return h.hexdigest()",
		"def parse_url(s):\n    scheme, _, rest = s.partition('://')\n    return scheme, rest",
		"async def fetch(url):\n    async with httpx.AsyncClient() as c:\n        return await c.get(url)",
		"def walk(root):\n    for d, _, fs in os.walk(root):\n        for f in fs:\n            yield f",
	}
	isQ := make([]bool, len(texts))

	// Sequential baseline.
	t0 := time.Now()
	for i := range texts {
		_, err := m.Encode(texts[i], isQ[i])
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}
	}
	seq := time.Since(t0)

	// Parallel.
	t1 := time.Now()
	_, err = m.EncodeBatch(texts, isQ, 0) // 0 = NumCPU
	if err != nil {
		t.Fatal(err)
	}
	par := time.Since(t1)

	speedup := float64(seq) / float64(par)
	t.Logf("N=%d candidates, NumCPU=%d: sequential=%v parallel=%v -> %.2fx speedup",
		len(texts), runtime.NumCPU(), seq, par, speedup)
	if speedup < 2.0 {
		t.Errorf("parallelism speedup %.2fx below floor 2.0x (machine under heavy other load?)", speedup)
	}
}

// BenchmarkEncodeBatch_rerankN50 wraps the same workload as
// TestEncodeBatch_rerankN50 in a Benchmark so `go test -bench` can
// drive it with -cpuprofile / -memprofile / -trace flags. Skipped when
// the model isn't present (CI / fresh checkouts). The benchmark runs
// the WARM path — b.N iterations share a single Model and re-encode
// the same texts each iter. For COLD-cache numbers use the test.
func BenchmarkEncodeBatch_rerankN50(b *testing.B) {
	if _, err := os.Stat(modelDir); errors.Is(err, fs.ErrNotExist) {
		b.Skipf("no model at %s", modelDir)
	}
	m, err := Load(modelDir)
	if err != nil {
		b.Fatal(err)
	}
	templates := []string{
		"def %s(x):\n    if x < 2:\n        return x\n    return %s(x-1) + %s(x-2)",
		"class %s:\n    def __init__(self, name):\n        self.name = name\n    def %s(self):\n        return self.name",
		"async def %s(url):\n    async with httpx.AsyncClient() as c:\n        r = await c.get(url)\n        return r.json()",
		"func %s(in []byte) ([]byte, error) {\n    if len(in) == 0 {\n        return nil, fmt.Errorf(\"empty\")\n    }\n    return in, nil\n}",
		"impl %s for Foo {\n    fn bar(&self) -> Result<()> {\n        self.inner.lock().unwrap().do_thing();\n        Ok(())\n    }\n}",
	}
	names := []string{"alpha", "beta", "gamma", "delta", "epsilon", "zeta", "eta", "theta",
		"iota", "kappa", "lambda", "mu", "nu", "xi", "omicron", "pi",
		"rho", "sigma", "tau", "upsilon", "phi", "chi", "psi", "omega",
		"add", "sub", "mul", "div", "mod", "pow", "log", "exp",
		"sin", "cos", "tan", "asin", "acos", "atan", "min", "max",
		"sort", "reverse", "shuffle", "merge", "split", "concat", "filter", "map",
		"reduce", "fold"}
	N := 50
	texts := make([]string, N)
	isQ := make([]bool, N)
	for i := 0; i < N; i++ {
		tmpl := templates[i%len(templates)]
		name := names[i%len(names)]
		texts[i] = fmt.Sprintf(tmpl, name, name, name)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := m.EncodeBatch(texts, isQ, 0)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// TestEncodeBatch_rerankN50: the M0 reference workload — 50
// candidates of ~80 tokens, cold cache. This is the headline M3
// latency number for the GO/NO-GO call: how long does a fresh
// hybrid-rerank query take end-to-end? Single repetition; logs the
// wall time. Skipped under -short (~20s on an 8-core M1 Pro).
func TestEncodeBatch_rerankN50(t *testing.T) {
	if testing.Short() {
		t.Skip("rerankN=50 measurement runs ~20s; skipped under -short")
	}
	if _, err := os.Stat(modelDir); errors.Is(err, fs.ErrNotExist) {
		t.Skipf("no model at %s", modelDir)
	}
	m, err := Load(modelDir)
	if err != nil {
		t.Fatal(err)
	}
	// 50 distinct candidate chunks of ~80-100 tokens each — synthesized
	// from a single template per category, varied enough that the dedup
	// cache (which this test deliberately doesn't model) wouldn't help.
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
		tmpl := templates[i%len(templates)]
		name := names[i%len(names)]
		texts[i] = fmt.Sprintf(tmpl, name, name, name)
	}

	t0 := time.Now()
	out, err := m.EncodeBatch(texts, isQ, 0) // NumCPU
	if err != nil {
		t.Fatal(err)
	}
	took := time.Since(t0)
	t.Logf("rerankN=%d cold, NumCPU=%d: %v wall (%.0fms/candidate amortized)",
		N, runtime.NumCPU(), took, float64(took.Milliseconds())/float64(N))
	if len(out) != N {
		t.Fatalf("got %d outputs, want %d", len(out), N)
	}
}
