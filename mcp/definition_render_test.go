package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestRenderDefinitionMarkdown_Line checks the definition render surfaces
// file:line (so an agent can copy it straight into find_related) and omits the
// :line suffix when the span wasn't recorded (Line == 0).
func TestRenderDefinitionMarkdown_Line(t *testing.T) {
	r := DefinitionResponse{
		Symbol: "Login",
		Definitions: []DefinitionRowOut{
			{File: "auth/user.go", Line: 42, Kind: "method", QName: "User.Login"},
			{File: "auth/admin.go", Line: 0, Kind: "function"}, // no span recorded
		},
	}
	md := renderDefinitionMarkdown(r)
	if !strings.Contains(md, "auth/user.go:42") {
		t.Errorf("render should include file:line for a recorded span; got:\n%s", md)
	}
	if strings.Contains(md, "auth/admin.go:0") {
		t.Errorf("render should NOT append :0 when the line is unrecorded; got:\n%s", md)
	}
	if !strings.Contains(md, "**auth/admin.go** — function") {
		t.Errorf("render should show bare file when line is 0; got:\n%s", md)
	}
}

// TestDefinitionRowOut_LineJSON confirms the JSON surface carries `line` and
// omits it at zero (omitempty), so adding the field stays additive for parsers.
func TestDefinitionRowOut_LineJSON(t *testing.T) {
	withLine, _ := json.Marshal(DefinitionRowOut{File: "a.go", Line: 7, Kind: "function"})
	if !strings.Contains(string(withLine), `"line":7`) {
		t.Errorf(`want "line":7 in %s`, withLine)
	}
	noLine, _ := json.Marshal(DefinitionRowOut{File: "a.go", Line: 0, Kind: "function"})
	if strings.Contains(string(noLine), `"line"`) {
		t.Errorf(`line should be omitted at zero; got %s`, noLine)
	}
}
