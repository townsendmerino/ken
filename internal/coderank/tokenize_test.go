package coderank

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/townsendmerino/ken/internal/embed"
)

const tokenizerPath = "../../testdata/coderank-model/tokenizer.json"

func loadCoderankTokenizer(t *testing.T) *embed.Tokenizer {
	t.Helper()
	if _, err := os.Stat(tokenizerPath); errors.Is(err, fs.ErrNotExist) {
		t.Skipf("no tokenizer at %s — symlink testdata/coderank-model -> HF snapshot", tokenizerPath)
	}
	tok, err := embed.LoadTokenizer(tokenizerPath)
	if err != nil {
		t.Fatalf("LoadTokenizer: %v", err)
	}
	return tok
}

func TestSpecialID(t *testing.T) {
	tok := loadCoderankTokenizer(t)
	cases := []struct {
		name string
		want int32
	}{
		{"[CLS]", 101},
		{"[SEP]", 102},
		{"[PAD]", 0},
		{"[UNK]", 100},
		{"[MASK]", 103},
	}
	for _, c := range cases {
		got, ok := tok.SpecialID(c.name)
		if !ok {
			t.Errorf("SpecialID(%q): not found", c.name)
			continue
		}
		if got != c.want {
			t.Errorf("SpecialID(%q): got %d want %d", c.name, got, c.want)
		}
	}
	if _, ok := tok.SpecialID("[NOPE]"); ok {
		t.Error("SpecialID([NOPE]): expected not-found, got ok=true")
	}
}

func TestEncodeWithSpecials_wrapsCLSSEP(t *testing.T) {
	tok := loadCoderankTokenizer(t)
	ids, err := tok.EncodeWithSpecials("hello world", 512)
	if err != nil {
		t.Fatalf("EncodeWithSpecials: %v", err)
	}
	if len(ids) < 2 {
		t.Fatalf("too short: %v", ids)
	}
	if ids[0] != 101 {
		t.Errorf("leading token: got %d want [CLS]=101", ids[0])
	}
	if ids[len(ids)-1] != 102 {
		t.Errorf("trailing token: got %d want [SEP]=102", ids[len(ids)-1])
	}
	body := tok.Encode("hello world")
	if len(ids) != len(body)+2 {
		t.Errorf("length: got %d want %d (body+2)", len(ids), len(body)+2)
	}
	for i, b := range body {
		if ids[i+1] != b {
			t.Errorf("body[%d]: got %d want %d", i, ids[i+1], b)
		}
	}
}

// TestEncodeWithSpecials_truncatesRight pins the contract: long inputs
// are truncated from the right while [CLS] (front) and [SEP] (back) are
// preserved. The plan §5 lists this explicitly because tokenizer_config
// truncation_side=right is the reference behavior.
func TestEncodeWithSpecials_truncatesRight(t *testing.T) {
	tok := loadCoderankTokenizer(t)
	// Make body longer than maxLen-2 = 8.
	body := tok.Encode("the quick brown fox jumps over the lazy dog and runs into the night")
	if len(body) < 8 {
		t.Fatalf("test setup: body too short (%d) for the truncation case", len(body))
	}
	ids, err := tok.EncodeWithSpecials("the quick brown fox jumps over the lazy dog and runs into the night", 10)
	if err != nil {
		t.Fatalf("EncodeWithSpecials: %v", err)
	}
	if len(ids) != 10 {
		t.Errorf("length: got %d want 10", len(ids))
	}
	if ids[0] != 101 || ids[9] != 102 {
		t.Errorf("specials clipped: got first=%d last=%d", ids[0], ids[9])
	}
	for i := 0; i < 8; i++ {
		if ids[i+1] != body[i] {
			t.Errorf("ids[%d]: got %d want %d (body[%d])", i+1, ids[i+1], body[i], i)
		}
	}
}

func TestEncodeWithSpecials_maxLenLeqTwo(t *testing.T) {
	tok := loadCoderankTokenizer(t)
	ids, err := tok.EncodeWithSpecials("some text", 2)
	if err != nil {
		t.Fatalf("EncodeWithSpecials: %v", err)
	}
	if len(ids) != 2 || ids[0] != 101 || ids[1] != 102 {
		t.Errorf("maxLen=2: got %v want [101 102]", ids)
	}
	ids, err = tok.EncodeWithSpecials("some text", 1)
	if err != nil {
		t.Fatalf("EncodeWithSpecials(1): %v", err)
	}
	if len(ids) != 2 || ids[0] != 101 || ids[1] != 102 {
		t.Errorf("maxLen=1: got %v want [101 102]", ids)
	}
}

// TestEncodeQuery_prependsPrefix ensures EncodeQuery is exactly
// EncodeWithSpecials(QueryPrefix+text). The model card mandates this
// prefix on queries only; using it on docs degrades cosine sharply.
func TestEncodeQuery_prependsPrefix(t *testing.T) {
	tok := loadCoderankTokenizer(t)
	q := "how do i parse json"
	ids, err := EncodeQuery(tok, q, 512)
	if err != nil {
		t.Fatalf("EncodeQuery: %v", err)
	}
	want, err := tok.EncodeWithSpecials(QueryPrefix+q, 512)
	if err != nil {
		t.Fatalf("EncodeWithSpecials reference: %v", err)
	}
	if len(ids) != len(want) {
		t.Fatalf("length: got %d want %d", len(ids), len(want))
	}
	for i, id := range ids {
		if id != want[i] {
			t.Errorf("token[%d]: got %d want %d", i, id, want[i])
		}
	}
	// Sanity: also != EncodeDoc on the bare query (the prefix MUST change ids).
	doc, _ := EncodeDoc(tok, q, 512)
	if len(ids) == len(doc) {
		t.Error("EncodeQuery should be longer than EncodeDoc on the same text (prefix tokens)")
	}
}

func TestEncodeQuery_zeroMaxLenDefaults(t *testing.T) {
	tok := loadCoderankTokenizer(t)
	a, err := EncodeQuery(tok, "x", 0)
	if err != nil {
		t.Fatal(err)
	}
	b, err := EncodeQuery(tok, "x", DefaultMaxSeqLength)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != len(b) {
		t.Errorf("maxLen=0 (default) should match DefaultMaxSeqLength; got %d vs %d", len(a), len(b))
	}
}

// TestEncodeWithSpecials_missingSpecial covers the error UX: a tokenizer
// loaded from a non-BERT JSON (no [CLS]/[SEP] in added_tokens) returns a
// clear error rather than panicking. Builds a synthetic tokenizer.json so
// this runs on every `go test` regardless of testdata/.
func TestEncodeWithSpecials_missingSpecial(t *testing.T) {
	dir := t.TempDir()
	tokJSON := `{
		"added_tokens": [{"id": 100, "content": "[UNK]", "special": true}],
		"normalizer": {"type": "BertNormalizer", "clean_text": true, "handle_chinese_chars": true, "strip_accents": null, "lowercase": true},
		"pre_tokenizer": {"type": "BertPreTokenizer"},
		"model": {"type": "WordPiece", "unk_token": "[UNK]", "continuing_subword_prefix": "##", "vocab": {"hello": 1}}
	}`
	p := filepath.Join(dir, "tokenizer.json")
	if err := os.WriteFile(p, []byte(tokJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	tok, err := embed.LoadTokenizer(p)
	if err != nil {
		t.Fatalf("LoadTokenizer: %v", err)
	}
	_, err = tok.EncodeWithSpecials("anything", 64)
	if err == nil {
		t.Fatal("expected error when [CLS] is missing, got nil")
	}
	if !strings.Contains(err.Error(), "[CLS]") {
		t.Errorf("error should name [CLS]; got: %v", err)
	}
}
