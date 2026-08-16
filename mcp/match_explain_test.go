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

func TestMatchInfo(t *testing.T) {
	// Verbatim term overlap → lexical, with the matched query terms.
	m := matchInfo("validateToken", "func validateToken(t string) bool { return true }")
	if m.Kind != "lexical" {
		t.Fatalf("Kind = %q, want lexical (%+v)", m.Kind, m)
	}
	// "validateToken" tokenizes to validate/token/validatetoken; the chunk
	// contains them, so at least those appear.
	joined := strings.Join(m.Terms, ",")
	if !strings.Contains(joined, "validate") || !strings.Contains(joined, "token") {
		t.Errorf("terms = %v, want validate+token", m.Terms)
	}

	// No overlap → semantic.
	if m := matchInfo("database migration", "func Add(a, b int) int { return a + b }"); m.Kind != "semantic" {
		t.Errorf("no-overlap Kind = %q, want semantic", m.Kind)
	}

	// Empty query → semantic (nothing to explain lexically).
	if m := matchInfo("", "anything"); m.Kind != "semantic" {
		t.Errorf("empty-query Kind = %q, want semantic", m.Kind)
	}

	// Dedup: a repeated query term appears once in Terms.
	m = matchInfo("token token token", "a token here")
	count := 0
	for _, tm := range m.Terms {
		if tm == "token" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 'token' once in %v", m.Terms)
	}
}

// TestSearchExplain drives the handler: explain=true populates Match on every
// row and adds a "match:" annotation to the markdown; default leaves Match nil.
func TestSearchExplain(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"),
		[]byte("package p\n\nfunc validateToken(s string) bool { return s != \"\" }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ix, err := search.FromPath(dir, search.ModeBM25, "line", "")
	if err != nil {
		t.Fatalf("FromPath: %v", err)
	}

	// JSON, explain on.
	args := SearchArgs{Query: "validateToken", TopK: 5, Explain: true, Output: "json"}
	res, _, err := runSearchWithTelemetry(ix, args, nil, false, nil, "", false)
	if err != nil {
		t.Fatalf("runSearchWithTelemetry: %v", err)
	}
	var resp SearchResponse
	if err := json.Unmarshal([]byte(res.Content[0].(*sdk.TextContent).Text), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) == 0 {
		t.Fatal("expected ≥1 result")
	}
	for _, r := range resp.Results {
		if r.Match == nil {
			t.Errorf("explain=true should populate Match on every row; got nil for %s", r.File)
		}
	}

	// markdown, explain on → per-result "match:" annotation.
	mdArgs := SearchArgs{Query: "validateToken", TopK: 5, Explain: true}
	mdRes, _, _ := runSearchWithTelemetry(ix, mdArgs, nil, false, nil, "", false)
	md := mdRes.Content[0].(*sdk.TextContent).Text
	if !strings.Contains(md, "match: terms") {
		t.Errorf("markdown should carry a 'match: terms' annotation; got:\n%s", md)
	}

	// Default (no explain) → Match nil, no annotation.
	offArgs := SearchArgs{Query: "validateToken", TopK: 5, Output: "json"}
	offRes, _, _ := runSearchWithTelemetry(ix, offArgs, nil, false, nil, "", false)
	var offResp SearchResponse
	_ = json.Unmarshal([]byte(offRes.Content[0].(*sdk.TextContent).Text), &offResp)
	for _, r := range offResp.Results {
		if r.Match != nil {
			t.Errorf("explain off should leave Match nil; got %+v", r.Match)
		}
	}
}
