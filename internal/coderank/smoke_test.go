package coderank

import (
	"encoding/json"
	"errors"
	"io/fs"
	"math"
	"os"
	"testing"
)

// TestForward_smokeOneCase: run the pure-Go forward pass on the FIRST
// short golden case and report the cosine vs the Python reference.
// This is the very first end-to-end signal — if cosine is high we
// have a working transformer; if it's near zero we have a layer-level
// bug to chase via component tests. Long-running, gated by the model
// dir + golden being present.
func TestForward_smokeOneCase(t *testing.T) {
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

	// Pick the first SHORT case to keep this initial smoke fast.
	// "hello world" (case 10, is_query=true, token len ~12) is ideal.
	var pick *goldenCase
	for i := range g.Cases {
		c := &g.Cases[i]
		if c.Text == "hello world" && c.IsQuery {
			pick = c
			break
		}
	}
	if pick == nil {
		t.Skip("fixture missing the 'hello world' query case")
	}

	m, err := Load(modelDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, err := m.Encode(pick.Text, pick.IsQuery)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if len(got) != g.EmbeddingDim {
		t.Fatalf("output dim: got %d want %d", len(got), g.EmbeddingDim)
	}

	want := make([]float64, g.EmbeddingDim)
	for i, v := range pick.Embedding {
		if v == nil {
			t.Fatalf("reference embedding[%d] is null — fixture is degenerate", i)
		}
		want[i] = *v
	}
	got64 := make([]float64, len(got))
	for i, v := range got {
		got64[i] = float64(v)
	}
	cos := cosineSimilarity(got64, want)
	t.Logf("smoke cosine vs reference: %.6f", cos)
	if cos < 0.997 { // loose bar per plan §11
		t.Fatalf("cosine %.6f below loose bar 0.997 — forward pass has a layer-level bug", cos)
	}
}

func cosineSimilarity(a, b []float64) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
