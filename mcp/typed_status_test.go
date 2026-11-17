package mcp

import (
	"context"
	"strings"
	"testing"
)

// F8: status says whether the go_* tools are on (go version, gopls if found)
// or off and why, so their absence is diagnosable from one call.
func TestStatus_TypedTierOn(t *testing.T) {
	r, _, err := handleStatus(context.Background(), &Config{}, StatusArgs{})
	if err != nil {
		t.Fatal(err)
	}
	got := extractText(t, r)
	if !strings.Contains(got, "Typed tools (go_*)") || !strings.Contains(got, "  on:  go") {
		t.Errorf("status lacks the typed-tier line (go is on PATH):\n%s", got)
	}
}

func TestStatus_TypedTierOffSaysWhy(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no go, no gopls
	r, _, err := handleStatus(context.Background(), &Config{}, StatusArgs{})
	if err != nil {
		t.Fatal(err)
	}
	got := extractText(t, r)
	if !strings.Contains(got, "off: no `go` command on the server's PATH") {
		t.Errorf("status doesn't say why the go_* tools are off:\n%s", got)
	}
	j, _, _ := handleStatus(context.Background(), &Config{}, StatusArgs{Output: "json"})
	if !strings.Contains(extractText(t, j), `"TypedTier"`) {
		t.Errorf("json status lacks TypedTier")
	}
}
