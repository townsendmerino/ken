package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/townsendmerino/ken/engine"
)

// scopeFixture writes a corpus in which the one file under zzz/ ranks far below
// position 200 for the query "needle" unfiltered: 300 other files repeat the
// term many times, the target mentions it once among filler.
func scopeFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "many"), 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range 300 {
		body := fmt.Sprintf("package many\n\n// needle needle needle needle needle needle\nfunc F%d() {}\n", i)
		if err := os.WriteFile(filepath.Join(dir, "many", fmt.Sprintf("f%03d.go", i)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "zzz"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := "package zzz\n\n" + strings.Repeat("// filler filler filler filler filler filler filler filler\n", 30) +
		"// needle\nfunc Target() {}\n"
	if err := os.WriteFile(filepath.Join(dir, "zzz", "target.go"), []byte(target), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestSearchCore_PathFilterFindsFileRankedBelowTwoHundred is F1's red test: the
// filter used to run after an over-fetch capped at 200, so a file the filter
// names but the unfiltered ranking puts deep came back as "no results".
func TestSearchCore_PathFilterFindsFileRankedBelowTwoHundred(t *testing.T) {
	dir := scopeFixture(t)
	ws, err := engine.Open(t.Context(), dir, engine.Options{Mode: "bm25"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ws.Close() }()

	resp, _, body := searchCore(ws, SearchArgs{Query: "needle", TopK: 5, PathContains: "zzz/"}, engine.ModeBM25, false, nil, dir, false)
	if len(resp.Results) != 1 || !strings.HasSuffix(resp.Results[0].File, "zzz/target.go") {
		t.Fatalf("path_contains=zzz/ should return zzz/target.go; got %+v\nbody: %s", resp.Results, body)
	}
}

// An empty scope is answered before searching, and names the filter.
func TestSearchCore_EmptyScopeNamesFilter(t *testing.T) {
	dir := scopeFixture(t)
	ws, err := engine.Open(t.Context(), dir, engine.Options{Mode: "bm25"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ws.Close() }()

	resp, _, body := searchCore(ws, SearchArgs{Query: "needle", PathContains: "nope/"}, engine.ModeBM25, false, nil, dir, false)
	if len(resp.Results) != 0 || resp.Filter == nil || resp.Filter.ChunksInScope != 0 {
		t.Fatalf("want no results and an empty scope in meta, got %+v", resp)
	}
	if !strings.Contains(body, "No files match the filters") || !strings.Contains(body, `path_contains="nope/"`) {
		t.Errorf("body should name the filter: %s", body)
	}
}

// A scope with files but no matching chunk says so, with the scope's size.
func TestSearchCore_ScopeMetaAndNoMatch(t *testing.T) {
	dir := scopeFixture(t)
	ws, err := engine.Open(t.Context(), dir, engine.Options{Mode: "bm25"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ws.Close() }()

	resp, _, _ := searchCore(ws, SearchArgs{Query: "needle", TopK: 5, PathContains: "zzz/"}, engine.ModeBM25, false, nil, dir, false)
	if resp.Filter == nil || resp.Filter.FilesInScope != 1 || resp.Filter.ChunksInScope < 1 || resp.Filter.ResultsAfterFilter != 1 {
		t.Fatalf("scope meta: %+v", resp.Filter)
	}
	_, _, body := searchCore(ws, SearchArgs{Query: "qqqzzzunmatched", PathContains: "zzz/"}, engine.ModeBM25, false, nil, dir, false)
	if !strings.Contains(body, "No chunk in the 1 file in scope") {
		t.Errorf("zero-result body: %s", body)
	}
}
