package main

import (
	"testing"

	"github.com/townsendmerino/aikit/chunk"
	"github.com/townsendmerino/aikit/chunk/treesitter"
)

// TestCLI_TreesitterParseTimeoutDisabled pins that the ken CLI registers its
// treesitter chunker with the per-parse wall-clock timeout DISABLED (init()
// above), so `ken build-index` / `ken index` produce byte-identical indexes on
// the same corpus regardless of machine load (#35). A regression here — e.g.
// dropping the init() re-registration and inheriting aikit's 1s default —
// reintroduces the load-dependent chunk-count drift: a borderline file whose
// parse straddles the timeout flaps between AST and line chunking across
// rebuilds.
func TestCLI_TreesitterParseTimeoutDisabled(t *testing.T) {
	c, err := chunk.Get("treesitter")
	if err != nil {
		t.Fatalf("treesitter chunker not registered: %v", err)
	}
	ts, ok := c.(*treesitter.Chunker)
	if !ok {
		t.Fatalf("registered treesitter is %T, want *treesitter.Chunker", c)
	}
	if _, disabled := ts.ParseTimeout(); !disabled {
		t.Error("CLI treesitter must run with the parse timeout DISABLED for reproducible builds (#35); got a bounded timeout")
	}
}
