package mcp

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestStructuralTools_Lines checks the line fields ADR-048 added to the
// structural tools' JSON (end_line/doc_line, outline spans, reference call
// lines, callers' sites) and their markdown, over testdata/repo.
func TestStructuralTools_Lines(t *testing.T) {
	ctx, sess, cleanup := newInMemoryServerClient(t)
	defer cleanup()

	call := func(tool string, args map[string]any) string {
		t.Helper()
		res, err := sess.CallTool(ctx, &sdk.CallToolParams{Name: tool, Arguments: args})
		if err != nil {
			t.Fatalf("CallTool(%s): %v", tool, err)
		}
		return res.Content[0].(*sdk.TextContent).Text
	}

	// definition: auth.py:16-17; NewStore's Go doc comment is on line 12.
	var def DefinitionResponse
	if err := json.Unmarshal([]byte(call("definition", map[string]any{"symbol": "validate_user", "output": "json"})), &def); err != nil {
		t.Fatal(err)
	}
	if len(def.Definitions) != 1 || def.Definitions[0].Line != 16 || def.Definitions[0].EndLine != 17 {
		t.Errorf("definition validate_user = %+v, want auth.py lines 16-17", def.Definitions)
	}
	if err := json.Unmarshal([]byte(call("definition", map[string]any{"symbol": "NewStore", "output": "json"})), &def); err != nil {
		t.Fatal(err)
	}
	if len(def.Definitions) != 1 || def.Definitions[0].Line != 13 || def.Definitions[0].EndLine != 15 || def.Definitions[0].DocLine != 12 {
		t.Errorf("definition NewStore = %+v, want 13-15 with doc_line 12", def.Definitions)
	}
	if md := call("definition", map[string]any{"symbol": "validate_user"}); !strings.Contains(md, "auth.py:16-17") {
		t.Errorf("definition markdown lacks the span:\n%s", md)
	}

	// outline: every entry carries its span; the decorated class starts
	// after its decorator.
	var out OutlineResponse
	if err := json.Unmarshal([]byte(call("outline", map[string]any{"path": "auth.py", "output": "json"})), &out); err != nil {
		t.Fatal(err)
	}
	spans := map[string][2]int{}
	for _, e := range out.Entries {
		spans[e.Container+"."+e.Name] = [2]int{e.StartLine, e.EndLine}
	}
	for name, want := range map[string][2]int{".validate_user": {16, 17}, ".User": {7, 12}, "User.is_valid": {11, 12}} {
		if spans[name] != want {
			t.Errorf("outline %s span = %v, want %v (all: %v)", name, spans[name], want, spans)
		}
	}
	if md := call("outline", map[string]any{"path": "auth.py"}); !strings.Contains(md, "func **validate_user**(user, token) — 16-17") {
		t.Errorf("outline markdown lacks spans:\n%s", md)
	}

	// callers: errors.New at store.go's file scope (line 6).
	var callers CallersResponse
	if err := json.Unmarshal([]byte(call("callers", map[string]any{"symbol": "New", "output": "json"})), &callers); err != nil {
		t.Fatal(err)
	}
	wantSites := []CallerSiteOut{{File: "store.go", Calls: []CallOut{{Line: 6}}}}
	if !reflect.DeepEqual(callers.Files, []string{"store.go"}) || !reflect.DeepEqual(callers.Sites, wantSites) {
		t.Errorf("callers New = files %v sites %+v, want store.go with one file-scope call on line 6", callers.Files, callers.Sites)
	}
	if md := call("callers", map[string]any{"symbol": "New"}); !strings.Contains(md, "**store.go** — 6 (file scope)") {
		t.Errorf("callers markdown lacks the call site:\n%s", md)
	}

	// references: the call lines ride on the file's row.
	var refs ReferencesResponse
	if err := json.Unmarshal([]byte(call("references", map[string]any{"symbol": "New", "output": "json"})), &refs); err != nil {
		t.Fatal(err)
	}
	if len(refs.References) != 1 || !reflect.DeepEqual(refs.References[0].Lines, []int{6}) {
		t.Errorf("references New = %+v, want store.go with call line 6", refs.References)
	}
}
