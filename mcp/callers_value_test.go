package mcp

import (
	"strings"
	"testing"
)

// With no direct calls but value uses, the markdown must say the name is
// used, never read as "unused".
func TestRenderCallersMarkdown_ValueUsesOnly(t *testing.T) {
	md := renderCallersMarkdown(CallersResponse{
		Symbol: "emitTurn",
		Files:  []string{},
		ValueUses: []CallerSiteOut{
			{File: "srv.go", Calls: []CallOut{{Line: 14, Caller: "Server.wire"}}},
		},
	})
	for _, want := range []string{"No direct calls", "not unused", "Passed as a value", "srv.go", "14 (in Server.wire)", "not traced"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q:\n%s", want, md)
		}
	}
}
