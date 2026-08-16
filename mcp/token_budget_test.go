package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/townsendmerino/aikit/chunk"
	"github.com/townsendmerino/ken/internal/search"
)

func TestApproxTokens(t *testing.T) {
	// Empty is zero; whitespace-only is zero.
	if got := approxTokens(""); got != 0 {
		t.Errorf("approxTokens(\"\") = %d, want 0", got)
	}
	if got := approxTokens("   \n\t "); got != 0 {
		t.Errorf("approxTokens(whitespace) = %d, want 0", got)
	}
	// A short word is ~1 token; whitespace between words doesn't add tokens.
	if got := approxTokens("foo bar"); got != 2 {
		t.Errorf("approxTokens(\"foo bar\") = %d, want 2", got)
	}
	// Punctuation counts as its own token.
	if got := approxTokens("a.b"); got != 3 { // "a" + "." + "b"
		t.Errorf("approxTokens(\"a.b\") = %d, want 3", got)
	}
	// Monotonic: more text costs more.
	if approxTokens("a short line") >= approxTokens("a considerably longer line of source code here") {
		t.Errorf("approxTokens should grow with length")
	}
}

func res(file, text string) search.Result {
	return search.Result{Chunk: chunk.Chunk{File: file, Text: text}}
}

func TestApplyTokenBudget(t *testing.T) {
	results := []search.Result{
		res("a.go", strings.Repeat("word ", 40)), // ~40 tokens + overhead
		res("b.go", strings.Repeat("word ", 40)),
		res("c.go", strings.Repeat("word ", 40)),
	}

	// maxTokens<=0 is a passthrough no-op.
	kept, dropped, used := applyTokenBudget(results, 0)
	if len(kept) != 3 || dropped != 0 || used != 0 {
		t.Errorf("budget=0 should be a no-op; got kept=%d dropped=%d used=%d", len(kept), dropped, used)
	}

	// A budget that fits ~one result keeps exactly one and drops the rest.
	oneCost := resultTokens(results[0])
	kept, dropped, used = applyTokenBudget(results, oneCost+1)
	if len(kept) != 1 || dropped != 2 {
		t.Errorf("budget≈1 result: kept=%d dropped=%d, want 1 and 2", len(kept), dropped)
	}
	if used == 0 || used > oneCost+1 {
		t.Errorf("reported used=%d, want in (0, %d]", used, oneCost+1)
	}

	// Always keep at least the top result even when it alone blows the budget.
	kept, dropped, _ = applyTokenBudget(results, 1)
	if len(kept) != 1 || dropped != 2 {
		t.Errorf("tiny budget must still keep the top hit; got kept=%d dropped=%d", len(kept), dropped)
	}

	// A generous budget keeps everything and reports Truncated=false upstream.
	kept, dropped, _ = applyTokenBudget(results, 1_000_000)
	if len(kept) != 3 || dropped != 0 {
		t.Errorf("generous budget: kept=%d dropped=%d, want 3 and 0", len(kept), dropped)
	}
}

// TestSearchMaxTokens_TruncatesAndReports drives the real search handler: a
// small max_tokens trims the result list, sets the Budget meta with
// Truncated=true, and no budget leaves Budget nil. Uses a self-contained
// fixture where the query matches every file, so ≥4 results are guaranteed.
func TestSearchMaxTokens_TruncatesAndReports(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		// Each file is one chunk containing the shared query term "alpha" plus
		// enough filler that a small budget can only hold one.
		body := "alpha " + strings.Repeat("filler word here ", 12) + "\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ix, err := search.FromPath(dir, search.ModeBM25, "line", "")
	if err != nil {
		t.Fatalf("FromPath: %v", err)
	}

	run := func(maxTokens int) SearchResponse {
		args := SearchArgs{Query: "alpha", TopK: 10, MaxTokens: maxTokens, Output: "json"}
		res, _, err := runSearchWithTelemetry(ix, args, nil, false, nil, "", false)
		if err != nil {
			t.Fatalf("runSearchWithTelemetry: %v", err)
		}
		var resp SearchResponse
		if err := json.Unmarshal([]byte(res.Content[0].(*sdk.TextContent).Text), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return resp
	}

	full := run(0)
	if full.Budget != nil {
		t.Errorf("no max_tokens → Budget should be nil, got %+v", full.Budget)
	}
	if len(full.Results) < 2 {
		t.Fatalf("fixture returned %d results; need ≥2 to test truncation", len(full.Results))
	}

	// Budget for roughly the first result only.
	budget := approxTokens(full.Results[0].Text) + approxTokens(full.Results[0].File) + rowScaffoldTokens + 1
	trimmed := run(budget)
	if trimmed.Budget == nil {
		t.Fatal("max_tokens set → Budget must be non-nil")
	}
	if !trimmed.Budget.Truncated || trimmed.Budget.Dropped == 0 {
		t.Errorf("expected truncation; got %+v", trimmed.Budget)
	}
	if len(trimmed.Results) >= len(full.Results) {
		t.Errorf("trimmed (%d) should have fewer results than full (%d)", len(trimmed.Results), len(full.Results))
	}
	if trimmed.Budget.MaxTokens != budget {
		t.Errorf("Budget.MaxTokens = %d, want %d", trimmed.Budget.MaxTokens, budget)
	}
	if len(trimmed.Results) < 1 {
		t.Error("budget must always keep at least the top result")
	}
}
