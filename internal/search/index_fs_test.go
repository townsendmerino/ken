package search

import (
	"strings"
	"testing"
	"testing/fstest"
)

// TestFromFS_BM25EndToEnd exercises FromFS on an fstest.MapFS in
// ModeBM25 — no real filesystem, no model on disk required. Mirrors
// TestFromPath_BM25EndToEnd.
func TestFromFS_BM25EndToEnd(t *testing.T) {
	fsys := fstest.MapFS{
		"auth.go":   {Data: []byte("package auth\nfunc ValidateToken(tok string) error {\n\treturn nil\n}\n")},
		"db.go":     {Data: []byte("package db\nfunc OpenConnection() {}\n// database pooling logic\n")},
		"README.md": {Data: []byte("# project\nunrelated prose about widgets\n")},
	}

	ix, err := FromFS(fsys, ModeBM25, "regex", "")
	if err != nil {
		t.Fatalf("FromFS: %v", err)
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
