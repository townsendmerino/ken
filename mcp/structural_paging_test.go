package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestPageWindow exercises the offset/limit clamping that bounds the
// whole-repo outline/symbols dumps (audit §6).
func TestPageWindow(t *testing.T) {
	const max = 500
	cases := []struct {
		name                 string
		total, offset, limit int
		wantLo, wantHi       int
		wantTruncated        bool
	}{
		{"empty", 0, 0, 0, 0, 0, false},
		{"under cap, no args", 10, 0, 0, 0, 10, false},
		{"over cap, no args", 1200, 0, 0, 0, 500, true},
		{"explicit limit under total", 100, 0, 20, 0, 20, true},
		{"explicit limit reaches end", 20, 0, 20, 0, 20, false},
		{"offset into middle", 100, 40, 20, 40, 60, true},
		{"offset+limit past end", 100, 90, 20, 90, 100, false},
		{"offset past total clamps", 100, 200, 20, 100, 100, false},
		{"negative offset clamps to 0", 100, -5, 10, 0, 10, true},
		{"limit over max clamps to max", 1000, 0, 99999, 0, 500, true},
		{"negative limit uses cap", 1000, 0, -3, 0, 500, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lo, hi, trunc := pageWindow(tc.total, tc.offset, tc.limit, max)
			if lo != tc.wantLo || hi != tc.wantHi || trunc != tc.wantTruncated {
				t.Errorf("pageWindow(%d,%d,%d,%d) = (%d,%d,%v); want (%d,%d,%v)",
					tc.total, tc.offset, tc.limit, max, lo, hi, trunc, tc.wantLo, tc.wantHi, tc.wantTruncated)
			}
			// Invariants: 0 ≤ lo ≤ hi ≤ total, window ≤ max.
			if lo < 0 || lo > hi || hi > tc.total {
				t.Errorf("bounds out of order/range: lo=%d hi=%d total=%d", lo, hi, tc.total)
			}
			if hi-lo > max {
				t.Errorf("window %d exceeds cap %d", hi-lo, max)
			}
		})
	}
}

// TestSymbols_Truncation confirms the handler honors limit, reports the
// true total, and flags truncation with a paging notice (audit §6).
// testdata/repo (auth.py) defines several top-level symbols, so limit:1
// leaves more behind.
func TestSymbols_Truncation(t *testing.T) {
	ctx, sess, cleanup := newInMemoryServerClient(t)
	defer cleanup()

	res, err := sess.CallTool(ctx, &sdk.CallToolParams{
		Name: "symbols",
		Arguments: map[string]any{
			"limit":  1,
			"output": "json",
		},
	})
	if err != nil {
		t.Fatalf("CallTool(symbols): %v", err)
	}
	var resp SymbolsResponse
	txt := res.Content[0].(*sdk.TextContent).Text
	if err := json.Unmarshal([]byte(txt), &resp); err != nil {
		t.Fatalf("unmarshal SymbolsResponse: %v\ntext=%s", err, txt)
	}
	if resp.TotalSymbols <= 1 {
		t.Skipf("corpus has only %d symbol(s); truncation not exercised", resp.TotalSymbols)
	}
	if len(resp.Symbols) != 1 {
		t.Errorf("limit:1 returned %d symbols, want 1", len(resp.Symbols))
	}
	if !resp.Truncated {
		t.Errorf("Truncated should be true when total (%d) > limit (1)", resp.TotalSymbols)
	}

	// Markdown mode must carry the truncation notice so a markdown-only
	// agent isn't misled into thinking it saw everything.
	mdRes, err := sess.CallTool(ctx, &sdk.CallToolParams{
		Name:      "symbols",
		Arguments: map[string]any{"limit": 1},
	})
	if err != nil {
		t.Fatalf("CallTool(symbols markdown): %v", err)
	}
	md := mdRes.Content[0].(*sdk.TextContent).Text
	if !strings.Contains(md, "truncated") {
		t.Errorf("markdown missing truncation notice:\n%s", md)
	}
}
