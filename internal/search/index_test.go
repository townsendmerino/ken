package search

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFromPath_BM25EndToEnd(t *testing.T) {
	root := t.TempDir()
	mk := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("auth.go", "package auth\nfunc ValidateToken(tok string) error {\n\treturn nil\n}\n")
	mk("db.go", "package db\nfunc OpenConnection() {}\n// database pooling logic\n")
	mk("README.md", "# project\nunrelated prose about widgets\n")

	ix, err := FromPath(root, ModeBM25, "regex", "")
	if err != nil {
		t.Fatalf("FromPath: %v", err)
	}
	if ix.Len() != 3 {
		t.Fatalf("indexed %d chunks, want 3 (one per small file)", ix.Len())
	}

	res := ix.Search("validate token", 5)
	if len(res) == 0 {
		t.Fatal("search returned no results for 'validate token'")
	}
	if !strings.HasPrefix(res[0].Chunk.File, "auth.go") {
		t.Errorf("top hit = %s, want auth.go (contains ValidateToken)", res[0].Chunk.File)
	}
}

func TestFromPath_RejectsNonBM25Mode(t *testing.T) {
	if _, err := FromPath(t.TempDir(), Mode(99), "regex", ""); err == nil {
		t.Error("FromPath with unsupported mode should error in Stage 1")
	}
}
