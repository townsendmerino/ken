package mcp

import (
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// resultText extracts the text payload from a CallToolResult.
func resultText(t *testing.T, r *sdk.CallToolResult) string {
	t.Helper()
	if len(r.Content) == 0 {
		t.Fatal("result has no content")
	}
	tc, ok := r.Content[0].(*sdk.TextContent)
	if !ok {
		t.Fatalf("content is not text: %T", r.Content[0])
	}
	return tc.Text
}

// TestErrorResult_UnknownOutputMode_KeepsRealError is the audit §23
// regression: when the agent typos `output`, the real error must NOT be
// swallowed by the mode complaint — both survive.
func TestErrorResult_UnknownOutputMode_KeepsRealError(t *testing.T) {
	const realErr = "repo /nope does not exist"
	got := resultText(t, errorResult("jsom", realErr))
	if !strings.Contains(got, realErr) {
		t.Errorf("real error dropped on bad output mode; got:\n%s", got)
	}
	if !strings.Contains(got, "jsom") {
		t.Errorf("mode complaint missing; got:\n%s", got)
	}
}

// TestErrorResult_JSONMode wraps the error as {"error": ...} so a
// json-parsing agent doesn't choke on markdown.
func TestErrorResult_JSONMode(t *testing.T) {
	got := resultText(t, errorResult("json", "boom"))
	if !strings.Contains(got, `"error"`) || !strings.Contains(got, "boom") {
		t.Errorf("json errorResult should be {\"error\":\"boom\"}; got %s", got)
	}
}
