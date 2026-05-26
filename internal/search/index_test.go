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

// TestSearchMode_DowngradesSemanticOnBM25Index pins the H4 fix: when
// a BM25-only index is asked to run a semantic/hybrid search via the
// per-call override, it transparently downgrades to BM25 (matching
// mcp.Run's build-time downgrade semantics) rather than panicking on
// nil flat/model. The reported effective mode is BM25.
func TestSearchMode_DowngradesSemanticOnBM25Index(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "auth.go"),
		[]byte("package auth\nfunc ValidateToken(tok string) error { return nil }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ix, err := FromPath(root, ModeBM25, "regex", "")
	if err != nil {
		t.Fatalf("FromPath: %v", err)
	}

	// Sanity: BM25 index has no flat/model.
	if ix.Mode() != ModeBM25 {
		t.Fatalf("Mode() = %v, want ModeBM25", ix.Mode())
	}

	for _, requested := range []Mode{ModeSemantic, ModeHybrid} {
		results, effective := ix.SearchMode("ValidateToken", 5, requested)
		if effective != ModeBM25 {
			t.Errorf("requested=%v: effective = %v, want ModeBM25 (downgrade)", requested, effective)
		}
		if len(results) == 0 {
			t.Errorf("requested=%v: empty results after downgrade", requested)
		}
	}
}

// TestSearchMode_HonorsRequestedModeOnCapableIndex pins that
// SearchMode respects the per-call override when the index has the
// required capabilities. We can't fully test semantic/hybrid here
// without a model (gated on testdata/model), so this exercises the
// "BM25 override on a BM25 index" path explicitly + the "Search
// equivalence" invariant.
func TestSearchMode_HonorsRequestedModeOnCapableIndex(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "auth.go"),
		[]byte("package auth\nfunc ValidateToken(tok string) error { return nil }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ix, err := FromPath(root, ModeBM25, "regex", "")
	if err != nil {
		t.Fatalf("FromPath: %v", err)
	}
	// Search(query, k) must produce the same results as SearchMode
	// with the index's build-time mode.
	want := ix.Search("ValidateToken", 5)
	got, effective := ix.SearchMode("ValidateToken", 5, ix.Mode())
	if effective != ix.Mode() {
		t.Errorf("effective = %v, want %v (no downgrade for capable index)", effective, ix.Mode())
	}
	if len(got) != len(want) {
		t.Fatalf("result count: SearchMode=%d Search=%d", len(got), len(want))
	}
	for i := range want {
		if got[i].Chunk.File != want[i].Chunk.File ||
			got[i].Score != want[i].Score {
			t.Errorf("result[%d] diverges: SearchMode=(%s,%.3f) Search=(%s,%.3f)",
				i, got[i].Chunk.File, got[i].Score, want[i].Chunk.File, want[i].Score)
		}
	}
}
