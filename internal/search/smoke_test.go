package search

import (
	"strings"
	"testing"
)

// TestRegexChunkerSmoke indexes the polyglot fixture testdata/repo with
// the regex chunker (which also exercises the markdown→line fallback) and
// checks that end-to-end search returns the obviously-relevant file.
func TestRegexChunkerSmoke(t *testing.T) {
	ix, err := FromPath("../../testdata/repo", ModeBM25, "regex", "")
	if err != nil {
		t.Fatalf("FromPath(regex): %v", err)
	}
	if ix.Len() == 0 {
		t.Fatal("indexed 0 chunks from testdata/repo")
	}

	cases := []struct {
		query    string
		wantFile string
	}{
		{"validate user token", "auth.py"},
		{"store get missing key", "store.go"},
		{"make widget service", "widget.ts"},
		{"circle area radius", "geometry.rs"},
	}
	for _, c := range cases {
		res := ix.Search(c.query, 5)
		if len(res) == 0 {
			t.Errorf("query %q: no results", c.query)
			continue
		}
		if !strings.HasSuffix(res[0].Chunk.File, c.wantFile) {
			t.Errorf("query %q: top hit %s, want %s", c.query, res[0].Chunk.File, c.wantFile)
		}
	}
}
