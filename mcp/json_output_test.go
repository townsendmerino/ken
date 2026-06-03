package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestJSONOutput_StructuralTools exercises output:"json" mode across
// the five Stage 8 structural tools. Uses the newInMemoryServerClient
// fixture which builds both a search index AND a structural index
// over testdata/repo (auth.py defines validate_user + hash_password
// + a User class), so the JSON paths hit populated responses rather
// than the "no structural index" branch tested separately in
// server_test.go.
func TestJSONOutput_StructuralTools(t *testing.T) {
	ctx, sess, cleanup := newInMemoryServerClient(t)
	defer cleanup()

	// definition → DefinitionResponse with at least one site
	t.Run("definition", func(t *testing.T) {
		res, err := sess.CallTool(ctx, &sdk.CallToolParams{
			Name: "definition",
			Arguments: map[string]any{
				"symbol": "validate_user",
				"output": "json",
			},
		})
		if err != nil {
			t.Fatalf("CallTool(definition): %v", err)
		}
		var resp DefinitionResponse
		txt := res.Content[0].(*sdk.TextContent).Text
		if err := json.Unmarshal([]byte(txt), &resp); err != nil {
			t.Fatalf("unmarshal DefinitionResponse: %v\ntext=%s", err, txt)
		}
		if resp.Symbol != "validate_user" {
			t.Errorf("Symbol echo = %q", resp.Symbol)
		}
		if len(resp.Definitions) == 0 {
			t.Errorf("expected ≥1 definition; got %+v", resp)
		} else if resp.Definitions[0].File == "" || resp.Definitions[0].Kind == "" {
			t.Errorf("first def missing File/Kind: %+v", resp.Definitions[0])
		}
	})

	// references → ReferencesResponse with totals echoed
	t.Run("references", func(t *testing.T) {
		res, err := sess.CallTool(ctx, &sdk.CallToolParams{
			Name: "references",
			Arguments: map[string]any{
				"symbol": "validate_user",
				"output": "json",
			},
		})
		if err != nil {
			t.Fatalf("CallTool(references): %v", err)
		}
		var resp ReferencesResponse
		txt := res.Content[0].(*sdk.TextContent).Text
		if err := json.Unmarshal([]byte(txt), &resp); err != nil {
			t.Fatalf("unmarshal ReferencesResponse: %v\ntext=%s", err, txt)
		}
		if resp.Symbol != "validate_user" {
			t.Errorf("Symbol echo = %q", resp.Symbol)
		}
		// References slice can be empty (no callers in fixture);
		// totals echo regardless.
	})

	// callers → CallersResponse with Files initialized
	t.Run("callers", func(t *testing.T) {
		res, err := sess.CallTool(ctx, &sdk.CallToolParams{
			Name: "callers",
			Arguments: map[string]any{
				"symbol": "validate_user",
				"output": "json",
			},
		})
		if err != nil {
			t.Fatalf("CallTool(callers): %v", err)
		}
		var resp CallersResponse
		txt := res.Content[0].(*sdk.TextContent).Text
		if err := json.Unmarshal([]byte(txt), &resp); err != nil {
			t.Fatalf("unmarshal CallersResponse: %v\ntext=%s", err, txt)
		}
		if resp.Symbol != "validate_user" {
			t.Errorf("Symbol echo = %q", resp.Symbol)
		}
		if resp.Files == nil {
			t.Errorf("Files should be [] (initialized) even when empty")
		}
	})

	// outline → OutlineResponse with Entries
	t.Run("outline", func(t *testing.T) {
		res, err := sess.CallTool(ctx, &sdk.CallToolParams{
			Name: "outline",
			Arguments: map[string]any{
				"path":   "auth.py",
				"output": "json",
			},
		})
		if err != nil {
			t.Fatalf("CallTool(outline): %v", err)
		}
		var resp OutlineResponse
		txt := res.Content[0].(*sdk.TextContent).Text
		if err := json.Unmarshal([]byte(txt), &resp); err != nil {
			t.Fatalf("unmarshal OutlineResponse: %v\ntext=%s", err, txt)
		}
		if resp.Path != "auth.py" {
			t.Errorf("Path echo = %q, want auth.py", resp.Path)
		}
		if len(resp.Entries) == 0 {
			t.Errorf("Entries should be non-empty for auth.py: %+v", resp)
		}
		// Entries should include the function names; spot-check.
		seen := map[string]bool{}
		for _, e := range resp.Entries {
			seen[e.Name] = true
		}
		if !seen["validate_user"] {
			t.Errorf("expected validate_user in outline; got names %v", seen)
		}
	})

	// symbols → SymbolsResponse with Symbols slice
	t.Run("symbols", func(t *testing.T) {
		res, err := sess.CallTool(ctx, &sdk.CallToolParams{
			Name: "symbols",
			Arguments: map[string]any{
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
		if resp.Symbols == nil {
			t.Errorf("Symbols should be initialized")
		}
		if len(resp.Symbols) == 0 {
			t.Errorf("expected ≥1 symbol from auth.py; got empty list")
		}
	})

	// Default still markdown — sanity that adding the JSON path
	// didn't flip the default.
	t.Run("default_still_markdown", func(t *testing.T) {
		res, err := sess.CallTool(ctx, &sdk.CallToolParams{
			Name: "callers",
			Arguments: map[string]any{
				"symbol": "validate_user",
			},
		})
		if err != nil {
			t.Fatalf("CallTool(callers): %v", err)
		}
		txt := res.Content[0].(*sdk.TextContent).Text
		if len(txt) > 0 && txt[0] == '{' {
			t.Errorf("default should be markdown, got JSON-looking text:\n%s", txt)
		}
	})

	// Unknown output mode error is consistent across tools.
	t.Run("unknown_output_errors", func(t *testing.T) {
		res, err := sess.CallTool(ctx, &sdk.CallToolParams{
			Name: "symbols",
			Arguments: map[string]any{
				"output": "yaml",
			},
		})
		if err != nil {
			t.Fatalf("CallTool(symbols): %v", err)
		}
		txt := res.Content[0].(*sdk.TextContent).Text
		if !strings.Contains(txt, "unknown output mode") {
			t.Errorf("expected 'unknown output mode' error, got:\n%s", txt)
		}
	})
}
