package search

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestFromFS_ParityWithFromPath pins that FromFS over os.DirFS(root)
// and the deprecated FromPath(root) produce byte-identical indexes and
// ranking on the same on-disk corpus. Since FromPath is now implemented
// in terms of FromFS, this also pins the os.DirFS adapter contract for
// the indexer side.
func TestFromFS_ParityWithFromPath(t *testing.T) {
	tmp := t.TempDir()
	mk := func(rel, body string) {
		p := filepath.Join(tmp, rel)
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

	a, err := FromPath(tmp, ModeBM25, "regex", "")
	if err != nil {
		t.Fatalf("FromPath: %v", err)
	}
	b, err := FromFS(os.DirFS(tmp), ModeBM25, "regex", "")
	if err != nil {
		t.Fatalf("FromFS: %v", err)
	}
	if a.Len() != b.Len() {
		t.Fatalf("Len mismatch: FromPath=%d FromFS=%d", a.Len(), b.Len())
	}
	for _, q := range []string{"validate token", "database pooling", "widgets"} {
		ra := a.Search(q, 5)
		rb := b.Search(q, 5)
		if !reflect.DeepEqual(ra, rb) {
			t.Fatalf("Search(%q) diverged:\n  FromPath: %#v\n  FromFS:   %#v", q, ra, rb)
		}
	}
}
