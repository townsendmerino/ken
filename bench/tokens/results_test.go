//go:build bench

package tokens

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/townsendmerino/ken/bench/internal/provenance"
)

// The token-budget result file is read by scripts/plot_token_budget.py,
// which is Python and can't be compile-checked against resultDocument.
// This pins the two keys it looks for, and that provenance is really
// populated rather than an empty object that only looks like the
// contract (docs/internal/rag-thread-followups.md item 4).
func TestWriteRecords_DocumentShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.json")
	prov := provenance.Collect(provenance.Options{
		Harness: "unit", Mode: "bm25", Chunker: "regex", TopK: MaxK(),
	})
	recs := []PerQueryRecord{{Repo: "r", Query: "q", QueryClass: ClassNL}}
	if err := writeRecords(path, prov, recs); err != nil {
		t.Fatal(err)
	}

	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Provenance struct {
			Harness string `json:"harness"`
			Config  struct {
				AlphaSymbol float64 `json:"alpha_symbol"`
				TopK        int     `json:"top_k"`
			} `json:"config"`
		} `json:"provenance"`
		Records []PerQueryRecord `json:"records"`
	}
	if err := json.Unmarshal(blob, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v\n%s", path, err, blob)
	}
	if doc.Provenance.Harness != "unit" || doc.Provenance.Config.AlphaSymbol == 0 {
		t.Errorf("provenance block not populated: %+v", doc.Provenance)
	}
	if doc.Provenance.Config.TopK != MaxK() {
		t.Errorf("top_k = %d, want MaxK() = %d", doc.Provenance.Config.TopK, MaxK())
	}
	if len(doc.Records) != 1 || doc.Records[0].Query != "q" {
		t.Errorf("records round-trip lost data: %+v", doc.Records)
	}
}

func TestKsLabel(t *testing.T) {
	if got, want := KsLabel(), "1,3,5,10"; got != want {
		t.Errorf("KsLabel() = %q, want %q", got, want)
	}
	if got, want := MaxK(), 10; got != want {
		t.Errorf("MaxK() = %d, want %d", got, want)
	}
}
