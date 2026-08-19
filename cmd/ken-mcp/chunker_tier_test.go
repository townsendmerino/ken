package main

import (
	"testing"

	"github.com/townsendmerino/aikit/chunk"
	"github.com/townsendmerino/aikit/chunk/treesitter"
)

// TestServer_TreesitterParseTimeoutBounded pins that ken-mcp registers its
// treesitter chunker with a BOUNDED per-parse wall-clock timeout (init()
// above), the opposite tier from the `ken` CLI (#35). The live multi-repo
// server keeps the latency guard so one pathological file can't stall a watch
// flush; it deliberately does not take the CLI's disabled-timeout
// reproducibility guarantee.
func TestServer_TreesitterParseTimeoutBounded(t *testing.T) {
	c, err := chunk.Get("treesitter")
	if err != nil {
		t.Fatalf("treesitter chunker not registered: %v", err)
	}
	ts, ok := c.(*treesitter.Chunker)
	if !ok {
		t.Fatalf("registered treesitter is %T, want *treesitter.Chunker", c)
	}
	micros, disabled := ts.ParseTimeout()
	if disabled {
		t.Error("server treesitter must keep a bounded parse timeout for latency isolation (#35); got disabled")
	}
	if micros != 1_000_000 {
		t.Errorf("server treesitter timeout = %d µs, want 1_000_000 (1s)", micros)
	}
}
