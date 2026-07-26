package mcp

import (
	"encoding/json"
	"path/filepath"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/townsendmerino/ken/internal/search"
)

// TestSearchResponse_WarmingField: the M4 staged-readiness signal appears in the
// search response's `semantic` field exactly when warming=true, and is absent
// otherwise.
func TestSearchResponse_WarmingField(t *testing.T) {
	ix, err := search.FromPath(filepath.Join("..", "testdata", "repo"), search.ModeBM25, "line", "")
	if err != nil {
		t.Fatalf("FromPath: %v", err)
	}
	args := SearchArgs{Query: "user", Output: "json"}

	semantic := func(warming bool) string {
		res, _, err := runSearchWithTelemetry(ix, args, nil, false, nil, "", warming)
		if err != nil {
			t.Fatalf("runSearchWithTelemetry: %v", err)
		}
		var resp SearchResponse
		txt := res.Content[0].(*sdk.TextContent).Text
		if err := json.Unmarshal([]byte(txt), &resp); err != nil {
			t.Fatalf("unmarshal SearchResponse: %v\ntext=%s", err, txt)
		}
		return resp.Semantic
	}

	if got := semantic(true); got != "warming" {
		t.Errorf("warming=true → semantic=%q, want %q", got, "warming")
	}
	if got := semantic(false); got != "" {
		t.Errorf("warming=false → semantic=%q, want empty (omitempty)", got)
	}
}
