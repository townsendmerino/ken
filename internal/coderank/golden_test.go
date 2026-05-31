package coderank

import (
	"encoding/json"
	"errors"
	"io/fs"
	"math"
	"os"
	"testing"

	"github.com/townsendmerino/ken/internal/embed"
)

// Golden fixture produced by scripts/pin_coderank.py. Stored in
// testdata/ (not gitignored — it's small, ~450 KB, and pins the
// reference for future change-control). Forward-pass parity (cosine)
// arrives in M2; M1 pins schema + tokenizer parity.
const goldenPath = "../../testdata/coderank_golden.json"

type goldenPayload struct {
	ModelID      string       `json:"model_id"`
	QueryPrefix  string       `json:"query_prefix"`
	MaxSeqLength int          `json:"max_seq_length"`
	EmbeddingDim int          `json:"embedding_dim"`
	NCases       int          `json:"n_cases"`
	VocabSize    int          `json:"vocab_size"`
	Note         string       `json:"note"`
	Cases        []goldenCase `json:"cases"`
}

type goldenCase struct {
	Text        string     `json:"text"`
	IsQuery     bool       `json:"is_query"`
	Note        string     `json:"note"`
	ModelInput  string     `json:"model_input"`
	TokenIDs    []int32    `json:"token_ids"`
	Embedding   []*float64 `json:"embedding"` // null entries → non-finite
	EmbeddingL2 float64    `json:"embedding_l2"`
}

func loadGolden(t *testing.T) *goldenPayload {
	t.Helper()
	b, err := os.ReadFile(goldenPath)
	if errors.Is(err, fs.ErrNotExist) {
		t.Skipf("no fixture at %s — regenerate with scripts/pin_coderank.py", goldenPath)
	}
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var g goldenPayload
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	return &g
}

func TestGoldenFixture_schema(t *testing.T) {
	g := loadGolden(t)

	if g.ModelID != "nomic-ai/CodeRankEmbed" {
		t.Errorf("ModelID: got %q want %q", g.ModelID, "nomic-ai/CodeRankEmbed")
	}
	if g.QueryPrefix != QueryPrefix {
		t.Errorf("QueryPrefix mismatch: fixture=%q, code=%q", g.QueryPrefix, QueryPrefix)
	}
	if g.MaxSeqLength != DefaultMaxSeqLength {
		t.Errorf("MaxSeqLength: got %d want %d", g.MaxSeqLength, DefaultMaxSeqLength)
	}
	if g.EmbeddingDim != 768 {
		t.Errorf("EmbeddingDim: got %d want 768", g.EmbeddingDim)
	}
	if len(g.Cases) != g.NCases || g.NCases == 0 {
		t.Fatalf("Cases length %d != NCases %d (or NCases=0)", len(g.Cases), g.NCases)
	}
	for i, c := range g.Cases {
		if len(c.Embedding) != g.EmbeddingDim {
			t.Errorf("case[%d] embedding length: got %d want %d", i, len(c.Embedding), g.EmbeddingDim)
		}
		if len(c.TokenIDs) < 2 || c.TokenIDs[0] != 101 || c.TokenIDs[len(c.TokenIDs)-1] != 102 {
			t.Errorf("case[%d] token_ids: missing [CLS]/[SEP] wrap (got first=%d last=%d)",
				i, c.TokenIDs[0], c.TokenIDs[len(c.TokenIDs)-1])
		}
		if len(c.TokenIDs) > g.MaxSeqLength {
			t.Errorf("case[%d] token_ids length %d > max_seq_length %d", i, len(c.TokenIDs), g.MaxSeqLength)
		}
		// Every embedding component should be finite (sanitizer should
		// have produced nulls for any non-finite; we then assert no
		// nulls remain — a null here means the reference itself was bad).
		for j, v := range c.Embedding {
			if v == nil {
				t.Errorf("case[%d] embedding[%d] is null (non-finite)", i, j)
				break
			}
			if !math.IsNaN(*v) && !math.IsInf(*v, 0) && *v == 0 && c.EmbeddingL2 == 0 {
				// zero entry on a zero-norm case is fine (degenerate)
				continue
			}
		}
	}
}

// TestGoldenFixture_tokenizerParity: our Go tokenizer (EncodeQuery /
// EncodeDoc) must produce byte-identical IDs to the HF BertTokenizer
// reference baked into the fixture. This is the plan §11.3 spot-check.
// The base WordPiece path already has an 11447-input parity harness in
// internal/embed; this is the smaller test that pins the [CLS]/[SEP]
// wrapping + the query prefix's tokenization.
func TestGoldenFixture_tokenizerParity(t *testing.T) {
	g := loadGolden(t)
	if _, err := os.Stat(tokenizerPath); errors.Is(err, fs.ErrNotExist) {
		t.Skipf("no tokenizer at %s — symlink testdata/coderank-model -> HF snapshot", tokenizerPath)
	}
	tok, err := embed.LoadTokenizer(tokenizerPath)
	if err != nil {
		t.Fatalf("LoadTokenizer: %v", err)
	}

	for i, c := range g.Cases {
		var got []int32
		if c.IsQuery {
			got, err = EncodeQuery(tok, c.Text, g.MaxSeqLength)
		} else {
			got, err = EncodeDoc(tok, c.Text, g.MaxSeqLength)
		}
		if err != nil {
			t.Errorf("case[%d] (%s) encode: %v", i, c.Note, err)
			continue
		}
		if len(got) != len(c.TokenIDs) {
			t.Errorf("case[%d] (%s) length: got %d want %d (text=%q)",
				i, c.Note, len(got), len(c.TokenIDs), preview(c.Text))
			continue
		}
		for j, id := range got {
			if id != c.TokenIDs[j] {
				t.Errorf("case[%d] (%s) token[%d]: got %d want %d (text=%q)",
					i, c.Note, j, id, c.TokenIDs[j], preview(c.Text))
				break // one report per case is enough
			}
		}
	}
}

// TestGoldenFixture_queryPrefixIsLiteral: case 10 ("hello world" as
// query) and case 11 (prefix-concatenated as doc) MUST tokenize to the
// same ID sequence. Pins the plan §5 claim that the prefix is "ordinary
// wordpieces" — no special-token handling, no normalization quirks.
func TestGoldenFixture_queryPrefixIsLiteral(t *testing.T) {
	g := loadGolden(t)
	var qCase, dCase *goldenCase
	for i := range g.Cases {
		c := &g.Cases[i]
		if c.IsQuery && c.Text == "hello world" {
			qCase = c
		}
		if !c.IsQuery && c.Text == QueryPrefix+"hello world" {
			dCase = c
		}
	}
	if qCase == nil || dCase == nil {
		t.Skip("fixture missing the paired prefix-equivalence cases; regenerate pin_coderank.py")
	}
	if len(qCase.TokenIDs) != len(dCase.TokenIDs) {
		t.Fatalf("prefix not tokenized as ordinary wordpieces: query has %d ids, doc has %d",
			len(qCase.TokenIDs), len(dCase.TokenIDs))
	}
	for i, id := range qCase.TokenIDs {
		if id != dCase.TokenIDs[i] {
			t.Errorf("token[%d]: query=%d doc=%d (prefix tokenizes differently?)",
				i, id, dCase.TokenIDs[i])
		}
	}
}

func preview(s string) string {
	if len(s) > 60 {
		return s[:60] + "..."
	}
	return s
}
