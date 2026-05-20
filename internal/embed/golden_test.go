package embed

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Golden test against ken_golden.json (produced by pin_inference.py).
// Two phases:
//
//  1. Tokenizer parity: for every case, our tokenizer must produce
//     byte-equal token IDs to the Python reference.
//
//  2. Embedding parity: for every case, our StaticModel.Encode must
//     produce a vector with cosine ≥ 1 - cosineTolerance vs the Python
//     reference's ground truth.
//
// The test skips itself if testdata/model/ is absent (the Hugging Face
// snapshot of minishlab/potion-code-16M is not committed; see
// testdata/README.md).

const cosineTolerance = 1e-5

type goldenFixture struct {
	ModelID           string       `json:"model_id"`
	VocabSize         int          `json:"vocab_size"`
	EmbeddingDim      int          `json:"embedding_dim"`
	MappingIsIdentity bool         `json:"mapping_is_identity"`
	MatchThreshold    float64      `json:"match_threshold"`
	Cases             []goldenCase `json:"cases"`
}

type goldenCase struct {
	Text            string    `json:"text"`
	Error           string    `json:"error,omitempty"`
	Note            string    `json:"note,omitempty"`
	Tokens          []string  `json:"tokens,omitempty"`
	IDs             []int64   `json:"ids,omitempty"`
	WeightsPerToken []float64 `json:"weights_per_token,omitempty"`
	GroundTruth     []float64 `json:"ground_truth,omitempty"`
}

func loadGolden(t *testing.T) goldenFixture {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "golden.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			t.Skip("testdata/golden.json not present; run pin_inference.py and copy ken_golden.json")
		}
		t.Fatalf("read golden.json: %v", err)
	}
	var fx goldenFixture
	if err := json.Unmarshal(data, &fx); err != nil {
		t.Fatalf("parse golden.json: %v", err)
	}
	return fx
}

func loadGoldenModel(t *testing.T) *StaticModel {
	t.Helper()
	modelDir := filepath.Join("..", "..", "testdata", "model")
	if _, err := os.Stat(filepath.Join(modelDir, "model.safetensors")); err != nil {
		t.Skip("testdata/model/ not present; see testdata/README.md")
	}
	m, err := Load(modelDir)
	if err != nil {
		t.Fatalf("Load(%q): %v", modelDir, err)
	}
	return m
}

func cosine(a []float32, b []float64) float64 {
	if len(a) != len(b) {
		panic("cosine: length mismatch")
	}
	var dot, sa, sb float64
	for i := range a {
		ai := float64(a[i])
		bi := b[i]
		dot += ai * bi
		sa += ai * ai
		sb += bi * bi
	}
	if sa == 0 || sb == 0 {
		return 0
	}
	return dot / (math.Sqrt(sa) * math.Sqrt(sb))
}

func TestGolden_TokenIDs(t *testing.T) {
	fx := loadGolden(t)
	m := loadGoldenModel(t)
	tok := m.Tokenizer()

	for _, c := range fx.Cases {
		if c.Error != "" || c.Note != "" {
			// Skip cases the Python reference flagged as exceptional.
			continue
		}
		t.Run(c.Text, func(t *testing.T) {
			gotIDs := tok.Encode(c.Text)
			if len(gotIDs) != len(c.IDs) {
				t.Errorf("text=%q: got %d ids %v, want %d ids %v",
					c.Text, len(gotIDs), gotIDs, len(c.IDs), c.IDs)
				return
			}
			for i := range gotIDs {
				if int64(gotIDs[i]) != c.IDs[i] {
					t.Errorf("text=%q: id[%d] = %d, want %d (tokens=%v full=%v)",
						c.Text, i, gotIDs[i], c.IDs[i], c.Tokens, gotIDs)
					return
				}
			}
		})
	}
}

func TestGolden_EmbeddingCosine(t *testing.T) {
	fx := loadGolden(t)
	m := loadGoldenModel(t)

	for _, c := range fx.Cases {
		if c.Error != "" || c.Note != "" || c.GroundTruth == nil {
			continue
		}
		t.Run(c.Text, func(t *testing.T) {
			got := m.Encode(c.Text)
			if len(got) != len(c.GroundTruth) {
				t.Fatalf("text=%q: dim mismatch got=%d want=%d",
					c.Text, len(got), len(c.GroundTruth))
			}
			cos := cosine(got, c.GroundTruth)
			if cos < 1-cosineTolerance {
				t.Errorf("text=%q: cosine = %.10f (want ≥ %.10f)",
					c.Text, cos, 1-cosineTolerance)
			}
		})
	}
}

func TestGolden_EmptyInput(t *testing.T) {
	m := loadGoldenModel(t)
	v := m.Encode("")
	if len(v) != m.Dim() {
		t.Fatalf("empty input: got dim %d, want %d", len(v), m.Dim())
	}
	for i, x := range v {
		if x != 0 {
			t.Errorf("empty input: v[%d] = %v, want 0 (degenerate ⇒ zero vector)", i, x)
		}
	}
}

func TestGolden_AllUNK_LongWord(t *testing.T) {
	m := loadGoldenModel(t)
	// 200 chars exceeds max_input_chars_per_word=100 → single UNK
	var long strings.Builder
	for range 200 {
		long.WriteString("x")
	}
	v := m.Encode(long.String())
	// The Python reference produces NaN here. Ken's contract is: return zero vector.
	for i, x := range v {
		if math.IsNaN(float64(x)) {
			t.Errorf("long-word input: v[%d] is NaN; ken should return zero vector", i)
		}
	}
}
