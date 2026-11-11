package server

import (
	"bytes"
	"strings"
	"testing"

	kenmcp "github.com/townsendmerino/ken/mcp"
)

// TestPrebuiltLogWriter pins the two translations and the pass-through: a
// pre-built repo's structural lines keep the wording ken-mcp has always
// logged, whatever characters the repo path holds.
func TestPrebuiltLogWriter(t *testing.T) {
	var logged, passed bytes.Buffer
	w := prebuiltLogWriter{logger: kenmcp.NewLogger(&logged, kenmcp.LogInfo), dir: "/r/my repo", passthrough: &passed}

	lines := []string{
		"engine: structural index built for /r/my repo: 12 files, 340 symbols, 56 callees in 1.5ms\n",
		"engine: structural index build for /r/my repo failed (boom); structural calls report no index and retry after a cooldown\n",
		"engine: something else\n",
	}
	for _, l := range lines {
		if n, err := w.Write([]byte(l)); err != nil || n != len(l) {
			t.Fatalf("Write(%q) = %d, %v", l, n, err)
		}
	}
	got := logged.String()
	for _, want := range []string{
		"structural index built (lazy, first use) for /r/my repo: 12 files, 340 symbols, 56 unique callees",
		"structural index build failed for /r/my repo: boom (track 2 tools will report no structure available; will retry on next call)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("log is missing %q; got:\n%s", want, got)
		}
	}
	if passed.String() != lines[2] {
		t.Errorf("pass-through = %q, want %q", passed.String(), lines[2])
	}
}
