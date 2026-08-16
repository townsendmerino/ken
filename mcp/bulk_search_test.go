package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/townsendmerino/ken/internal/search"
)

func bulkFixtureIndex(t *testing.T) *search.Index {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"auth.go":  "package p\n\nfunc validateToken(s string) bool { return s != \"\" }\n",
		"store.go": "package p\n\nfunc saveRecord(id int) error { return nil }\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ix, err := search.FromPath(dir, search.ModeBM25, "line", "")
	if err != nil {
		t.Fatalf("FromPath: %v", err)
	}
	return ix
}

func TestBulkSearch_JSONGroupsPerQuery(t *testing.T) {
	ix := bulkFixtureIndex(t)
	args := SearchArgs{Queries: []string{"validateToken", "saveRecord"}, TopK: 5, Output: "json"}
	res, _, err := runSearch(ix, args)
	if err != nil {
		t.Fatalf("runSearch: %v", err)
	}
	var bulk BulkSearchResponse
	if err := json.Unmarshal([]byte(res.Content[0].(*sdk.TextContent).Text), &bulk); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(bulk.Queries) != 2 {
		t.Fatalf("want 2 query groups, got %d", len(bulk.Queries))
	}
	if bulk.Queries[0].Query != "validateToken" || bulk.Queries[1].Query != "saveRecord" {
		t.Errorf("query order/echo wrong: %q, %q", bulk.Queries[0].Query, bulk.Queries[1].Query)
	}
	// Each query should find its own file at rank 1.
	if len(bulk.Queries[0].Results) == 0 || !strings.Contains(bulk.Queries[0].Results[0].File, "auth.go") {
		t.Errorf("query 1 top result = %+v, want auth.go", bulk.Queries[0].Results)
	}
	if len(bulk.Queries[1].Results) == 0 || !strings.Contains(bulk.Queries[1].Results[0].File, "store.go") {
		t.Errorf("query 2 top result = %+v, want store.go", bulk.Queries[1].Results)
	}
}

func TestBulkSearch_MarkdownSections(t *testing.T) {
	ix := bulkFixtureIndex(t)
	res, _, err := runSearch(ix, SearchArgs{Queries: []string{"validateToken", "saveRecord"}, TopK: 3})
	if err != nil {
		t.Fatalf("runSearch: %v", err)
	}
	md := res.Content[0].(*sdk.TextContent).Text
	if strings.Count(md, "Search results for:") != 2 {
		t.Errorf("expected two per-query sections; got:\n%s", md)
	}
	if !strings.Contains(md, "---") {
		t.Errorf("expected a separator between sections; got:\n%s", md)
	}
}

func TestBulkSearch_CapEnforced(t *testing.T) {
	ix := bulkFixtureIndex(t)
	queries := make([]string, MaxBulkQueries+5)
	for i := range queries {
		queries[i] = "validateToken"
	}
	res, _, err := runSearch(ix, SearchArgs{Queries: queries, TopK: 1, Output: "json"})
	if err != nil {
		t.Fatalf("runSearch: %v", err)
	}
	var bulk BulkSearchResponse
	if err := json.Unmarshal([]byte(res.Content[0].(*sdk.TextContent).Text), &bulk); err != nil {
		t.Fatal(err)
	}
	if len(bulk.Queries) != MaxBulkQueries {
		t.Errorf("ran %d queries, want cap %d", len(bulk.Queries), MaxBulkQueries)
	}
	if !bulk.Truncated || bulk.Dropped != 5 {
		t.Errorf("Truncated=%v Dropped=%d, want true/5", bulk.Truncated, bulk.Dropped)
	}
}

// TestSearch_RequiresQueryOrQueries pins the validation that at least one of
// query / queries must be provided (via the Cache-backed handler).
func TestSearch_RequiresQueryOrQueries(t *testing.T) {
	cfg := &Config{} // no cache needed — validation fires first
	res, _, err := handleSearch(t.Context(), cfg, SearchArgs{})
	if err != nil {
		t.Fatalf("handleSearch: %v", err)
	}
	txt := res.Content[0].(*sdk.TextContent).Text
	if !strings.Contains(txt, "query") || !strings.Contains(txt, "queries") {
		t.Errorf("empty search should ask for query or queries; got %q", txt)
	}
}
