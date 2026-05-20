package search

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Model-gated: the hybrid pipeline needs the Model2Vec snapshot. Skips
// cleanly without testdata/model/ (per-machine, gitignored) — same policy
// as the embed golden tests.
func modelDirOrSkip(t *testing.T) string {
	t.Helper()
	dir := filepath.Join("..", "..", "testdata", "model")
	if _, err := os.Stat(filepath.Join(dir, "model.safetensors")); err != nil {
		t.Skip("testdata/model/ not present; see testdata/README.md")
	}
	return dir
}

func TestStage4_HybridOnFixture(t *testing.T) {
	model := modelDirOrSkip(t)
	ix, err := FromPath("../../testdata/repo", ModeHybrid, "regex", model)
	if err != nil {
		t.Fatalf("FromPath(hybrid): %v", err)
	}
	if ix.Len() == 0 {
		t.Fatal("indexed 0 chunks")
	}
	cases := []struct{ query, wantFile string }{
		{"validate_user", "auth.py"},          // symbol (underscore) → definition boost
		{"makeWidget", "widget.ts"},           // symbol (camelCase) → definition boost
		{"circle area radius", "geometry.rs"}, // NL → semantic + stem
		{"store missing key", "store.go"},     // NL
	}
	for _, c := range cases {
		res := ix.Search(c.query, 5)
		if len(res) == 0 {
			t.Errorf("query %q: no results", c.query)
			continue
		}
		if !strings.HasSuffix(res[0].Chunk.File, c.wantFile) {
			t.Errorf("query %q: top = %s, want suffix %s", c.query, res[0].Chunk.File, c.wantFile)
		}
	}
}

// Demonstrates rerank actually moves results: a symbol query for a
// definition that also appears (unqualified) in a test file should rank
// the real definition above the test file under hybrid, even if BM25
// alone would not.
func TestStage4_RerankMovesDefinitionAboveTest(t *testing.T) {
	model := modelDirOrSkip(t)
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("svc.go", "package svc\n\nfunc ComputeBudget(x int) int {\n\treturn x * 2\n}\n")
	write("svc_test.go", "package svc\n\nfunc TestComputeBudget(t *T) {\n\tComputeBudget(1)\n\tComputeBudget(2)\n\tComputeBudget(3)\n}\n")

	ix, err := FromPath(root, ModeHybrid, "regex", model)
	if err != nil {
		t.Fatalf("FromPath: %v", err)
	}
	res := ix.Search("ComputeBudget", 5)
	if len(res) == 0 {
		t.Fatal("no results")
	}
	if !strings.HasSuffix(res[0].Chunk.File, "svc.go") {
		t.Errorf("top = %s, want svc.go (definition) above svc_test.go (penalised)",
			res[0].Chunk.File)
	}
}
