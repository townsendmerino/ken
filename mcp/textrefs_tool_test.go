package mcp

import (
	"context"
	"strings"
	"testing"
)

func TestTextRefsTool(t *testing.T) {
	cfg, _ := testsConfig(t)
	// the fixture's a_test.go names no file; add the readers through a fresh repo is heavier than this check needs
	res, _, err := handleTextRefs(context.Background(), cfg, TextRefsArgs{Path: "a/a.go"})
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(t, res)
	if !strings.Contains(text, "No string literal") || !strings.Contains(text, "grep") {
		t.Errorf("empty answer should say none found and point at grep:\n%s", text)
	}
	res, _, _ = handleTextRefs(context.Background(), cfg, TextRefsArgs{})
	if !strings.Contains(resultText(t, res), "path is required") {
		t.Errorf("missing path not rejected")
	}
}
