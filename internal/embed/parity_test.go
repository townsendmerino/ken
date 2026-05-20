//go:build parity

// Stage-3 corpus-scale tokenizer parity test (docs/DESIGN.md §3 acceptance bar).
//
// Excluded from `go test ./...` by the `parity` build tag. Run with:
//
//	.venv/bin/python scripts/parity_dump.py        # regenerate testdata/parity.jsonl
//	go test -tags=parity ./internal/embed/ -run TestParity -v
//
// The dump emits HF's normalize/pre_tokenize/ids per input; this test
// walks the same stages in ken's tokenizer and classifies any mismatch
// by the first stage at which it diverges — so drift is attributable,
// not just countable.

package embed

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type parityRec struct {
	Text       string   `json:"text"`
	Normalized string   `json:"normalized"`
	PreTokens  []string `json:"pre_tokens"`
	IDs        []int32  `json:"ids"`
}

type parityCat int

const (
	catNormalize parityCat = iota
	catPreTokenize
	catWordPiece
	catOther // end-to-end IDs differ despite every stage matching in isolation
	nCat
)

func (c parityCat) String() string {
	return [...]string{"normalize", "pre_tokenize", "wordpiece", "other"}[c]
}

func TestParity(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	modelDir := filepath.Join(repoRoot, "testdata", "model")
	if _, err := os.Stat(filepath.Join(modelDir, "model.safetensors")); err != nil {
		t.Skip("testdata/model/ not present; see testdata/README.md")
	}
	parityPath := filepath.Join(repoRoot, "testdata", "parity.jsonl")
	f, err := os.Open(parityPath)
	if err != nil {
		if os.IsNotExist(err) {
			t.Skip("testdata/parity.jsonl not present; run `.venv/bin/python scripts/parity_dump.py`")
		}
		t.Fatal(err)
	}
	defer f.Close()

	m, err := Load(modelDir)
	if err != nil {
		t.Fatalf("Load model: %v", err)
	}
	tok := m.Tokenizer()

	var counts [nCat]int
	var examples [nCat][]string
	addExample := func(c parityCat, s string) {
		if len(examples[c]) < 5 {
			examples[c] = append(examples[c], s)
		}
	}

	total := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 16<<20) // tolerate fat lines (HF id arrays)
	for sc.Scan() {
		var rec parityRec
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			snippet := sc.Text()
			if len(snippet) > 80 {
				snippet = snippet[:80] + "…"
			}
			t.Fatalf("decode line %d: %v\nbyte: %s", total+1, err, snippet)
		}
		total++

		// Stage 1 — normalize.
		kenNorm := tok.normalize(rec.Text)
		if kenNorm != rec.Normalized {
			counts[catNormalize]++
			addExample(catNormalize, fmt.Sprintf("text=%q\n      ken_norm=%q\n      hf_norm=%q",
				rec.Text, kenNorm, rec.Normalized))
			continue
		}

		// Stage 2 — pre-tokenize (over HF's normalized, isolating this stage).
		kenPre := tok.preTokenize(rec.Normalized)
		if !strSliceEqual(kenPre, rec.PreTokens) {
			counts[catPreTokenize]++
			addExample(catPreTokenize, fmt.Sprintf("text=%q\n      ken_pre=%v\n      hf_pre=%v",
				rec.Text, kenPre, rec.PreTokens))
			continue
		}

		// Stage 3 — wordpiece+added_tokens over HF's pre_tokens. Skip when
		// the raw text contains an added-token literal: HF's e2e Encode
		// CARVES added tokens before normalize/pre-tokenize, so its final
		// ids cannot be reproduced by per-word wordpiece-ing of the
		// pre_tokens of the non-carved normalized string. Those inputs are
		// only meaningful at the end-to-end (catOther) stage below.
		if !containsAddedToken(rec.Text, tok.addedKeys) {
			var stageIDs []int32
			for _, w := range rec.PreTokens {
				if id, ok := tok.addedTokens[w]; ok {
					stageIDs = append(stageIDs, id)
					continue
				}
				stageIDs = append(stageIDs, tok.wordPiece(w)...)
			}
			if !int32SliceEqual(stageIDs, rec.IDs) {
				counts[catWordPiece]++
				addExample(catWordPiece, fmt.Sprintf("text=%q\n      ken_wp_ids=%v\n      hf_ids=%v",
					rec.Text, stageIDs, rec.IDs))
				continue
			}
		}

		// End-to-end Encode — catches any glue bug (added_tokens priority,
		// internal stage-ordering, etc.) that wouldn't surface from running
		// the stages individually on HF's intermediates.
		gotIDs := tok.Encode(rec.Text)
		if !int32SliceEqual(gotIDs, rec.IDs) {
			counts[catOther]++
			addExample(catOther, fmt.Sprintf("text=%q\n      ken_e2e_ids=%v\n      hf_ids=%v",
				rec.Text, gotIDs, rec.IDs))
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}

	t.Logf("parity inputs: %d", total)
	t.Logf("category counts:")
	fail := false
	for c := parityCat(0); c < nCat; c++ {
		t.Logf("  %-14s %d", c, counts[c])
		if counts[c] > 0 {
			fail = true
			for i, ex := range examples[c] {
				t.Logf("    [%d] %s", i, ex)
			}
		}
	}
	if fail {
		t.Fatal("parity drift detected — see counts/examples above (acceptance bar v1: every category == 0)")
	}
}

// containsAddedToken reports whether any added-token literal occurs in s.
func containsAddedToken(s string, keys []string) bool {
	for _, k := range keys {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

// strSliceEqual treats nil and empty-slice as equal (HF and ken can each
// produce either at empty boundaries; the *content* is what matters).
func strSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func int32SliceEqual(a, b []int32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
