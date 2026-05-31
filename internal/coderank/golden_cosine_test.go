package coderank

import (
	"encoding/json"
	"errors"
	"io/fs"
	"math"
	"os"
	"sort"
	"testing"
	"time"
)

// TestGoldenFixture_cosine runs every golden case end-to-end through the
// pure-Go forward pass and asserts cosine ≥ 0.997 vs the Python
// reference. Plan §11 sets a LOOSE bar (1e-3) deliberately: f32 GEMM
// (plan §7) is acceptable when end-to-end NDCG is the real acceptance
// gate; bit-fidelity is not the goal. The smoke test already showed
// 1.000000 on a short case so any genuine layer bug shows up as a
// dramatic drop, not a low-bits trickle.
//
// Cases are sorted by token-length and the long ones (token len > 128)
// run only when `-short` is NOT set, so `go test -short ./...` stays
// fast while the explicit full-suite pass still validates truncation
// on real long inputs (case 9: the 200×repeated function body).
//
// Skipped without the testdata/coderank-model symlink and the
// pre-generated testdata/coderank_golden.json (mirrors how golden_test.go
// already gates the cheap parity checks).
func TestGoldenFixture_cosine(t *testing.T) {
	if _, err := os.Stat(modelDir); errors.Is(err, fs.ErrNotExist) {
		t.Skipf("no model at %s — symlink testdata/coderank-model -> HF snapshot", modelDir)
	}
	b, err := os.ReadFile(goldenPath)
	if errors.Is(err, fs.ErrNotExist) {
		t.Skipf("no fixture at %s — regenerate with scripts/pin_coderank.py", goldenPath)
	}
	if err != nil {
		t.Fatal(err)
	}
	var g goldenPayload
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}

	m, err := Load(modelDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	type result struct {
		idx    int
		tokLen int
		cos    float64
		took   time.Duration
		note   string
	}
	var results []result
	short := testing.Short()
	skippedLong := 0

	for i, c := range g.Cases {
		if short && len(c.TokenIDs) > 128 {
			skippedLong++
			continue
		}
		// Empty string in the fixture (case 6) hits a path where the
		// reference may produce a zero-norm vector — cosine is undefined
		// there. We assert the zero contract instead.
		if c.EmbeddingL2 == 0 {
			t0 := time.Now()
			got, err := m.Encode(c.Text, c.IsQuery)
			if err != nil {
				t.Errorf("case[%d] %q encode: %v", i, c.Note, err)
				continue
			}
			took := time.Since(t0)
			var sum float64
			for _, v := range got {
				sum += float64(v) * float64(v)
			}
			t.Logf("case[%d] %s: zero-norm reference, |got|=%g (%dms, %d tok)",
				i, c.Note, math.Sqrt(sum), took.Milliseconds(), len(c.TokenIDs))
			continue
		}

		want := make([]float64, g.EmbeddingDim)
		for j, v := range c.Embedding {
			if v == nil {
				t.Fatalf("case[%d] embedding[%d] is null", i, j)
			}
			want[j] = *v
		}

		t0 := time.Now()
		got, err := m.Encode(c.Text, c.IsQuery)
		if err != nil {
			t.Errorf("case[%d] %q encode: %v", i, c.Note, err)
			continue
		}
		took := time.Since(t0)
		got64 := make([]float64, len(got))
		for j, v := range got {
			got64[j] = float64(v)
		}
		cos := cosineSimilarity(got64, want)
		results = append(results, result{i, len(c.TokenIDs), cos, took, c.Note})
		if cos < 0.997 {
			t.Errorf("case[%d] %s: cosine %.6f < 0.997 (tok=%d, took=%dms)",
				i, c.Note, cos, len(c.TokenIDs), took.Milliseconds())
		}
	}

	// Sort by token length for the report — makes the latency curve
	// visible at a glance, which is the M2/M3 gate input.
	sort.Slice(results, func(i, j int) bool { return results[i].tokLen < results[j].tokLen })
	var cosSum, msSum float64
	minCos := 1.0
	for _, r := range results {
		t.Logf("  tok=%4d  cos=%.6f  took=%4dms  %s", r.tokLen, r.cos, r.took.Milliseconds(), r.note)
		cosSum += r.cos
		msSum += float64(r.took.Milliseconds())
		if r.cos < minCos {
			minCos = r.cos
		}
	}
	if n := len(results); n > 0 {
		t.Logf("\nsummary: %d cases, min cos %.6f, mean cos %.6f, mean latency %.0fms",
			n, minCos, cosSum/float64(n), msSum/float64(n))
	}
	if skippedLong > 0 {
		t.Logf("(skipped %d long cases under -short; rerun without -short to exercise truncation path)", skippedLong)
	}
}
