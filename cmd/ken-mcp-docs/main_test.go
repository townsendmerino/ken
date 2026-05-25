//go:build integration

// Integration smoke test for the v0.6.0 ken-mcp-docs demo binary.
//
// Gated by build tag `integration` because it:
//  1. Requires the Model2Vec model at ~/.ken/model (per-machine; see
//     testdata/README.md). Skipped if absent.
//  2. Invokes scripts/build-docs-mcp.sh to rebuild the binary fresh.
//  3. Spawns the binary as a subprocess and drives a real MCP session
//     via the SDK's CommandTransport.
//
// Run with:
//
//	go test -tags=integration ./cmd/ken-mcp-docs/
//
// Default `go test ./...` skips this entirely (the file has no
// buildable Go content without the tag).
package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestEmbeddedCorpus_SmokeTest builds the demo binary via the canonical
// script, then drives a real MCP session over CommandTransport to
// confirm that an end-to-end query against the embedded docs corpus
// returns the expected hit. Also confirms that `mcp.Run`'s
// repo-arg-is-ignored contract holds when crossed by the agent (a
// bogus repo arg must produce the same hit as no repo arg).
func TestEmbeddedCorpus_SmokeTest(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	modelPath := filepath.Join(home, ".ken", "model", "model.safetensors")
	if _, err := os.Stat(modelPath); err != nil {
		t.Skip("~/.ken/model/model.safetensors not present — run `ken download-model` first")
	}

	// Find repo root: cmd/ken-mcp-docs/ is two levels under it.
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("abs(..): %v", err)
	}
	script := filepath.Join(repoRoot, "scripts", "build-docs-mcp.sh")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("missing build script %q: %v", script, err)
	}

	// Build the binary. The script stages docs + model into the package
	// directory then runs `go build -tags=embed_corpus`.
	out, err := exec.Command(script).CombinedOutput()
	if err != nil {
		t.Fatalf("build-docs-mcp.sh failed: %v\n%s", err, out)
	}
	t.Logf("build output:\n%s", out)

	binPath := filepath.Join(repoRoot, "bin", "ken-mcp-docs")
	if st, err := os.Stat(binPath); err != nil {
		t.Fatalf("binary missing after build: %v", err)
	} else {
		t.Logf("bin/ken-mcp-docs is %.1f MB", float64(st.Size())/1024/1024)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binPath)
	cmd.Env = append(os.Environ(), "KEN_MCP_LOG_LEVEL=error")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	cli := sdk.NewClient(&sdk.Implementation{Name: "ken-mcp-docs-test", Version: "0"}, nil)
	sess, err := cli.Connect(ctx, &sdk.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("Connect: %v\n--stderr--\n%s", err, stderr.String())
	}
	defer sess.Close()

	tl, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v\n--stderr--\n%s", err, stderr.String())
	}
	have := map[string]bool{}
	for _, tool := range tl.Tools {
		have[tool.Name] = true
	}
	for _, want := range []string{"search", "find_related"} {
		if !have[want] {
			t.Errorf("missing tool %q (have %v)", want, have)
		}
	}

	// Query 1: search for a term we know is in docs/DESIGN.md ("Model2Vec"
	// appears prominently throughout the file). The expected hit is
	// DESIGN.md.
	res, err := sess.CallTool(ctx, &sdk.CallToolParams{
		Name: "search",
		Arguments: map[string]any{
			"query": "Model2Vec embedding",
			"mode":  "hybrid",
			"top_k": 5,
		},
	})
	if err != nil {
		t.Fatalf("CallTool(search): %v\n--stderr--\n%s", err, stderr.String())
	}
	txtA := res.Content[0].(*sdk.TextContent).Text
	t.Logf("=== embedded-corpus search hit ===\n%s", truncate(txtA, 1500))
	if !strings.Contains(txtA, "DESIGN.md") {
		t.Errorf("expected DESIGN.md in search results, got:\n%s", truncate(txtA, 1500))
	}

	// Query 2: same query with a bogus repo arg. mcp.Run ignores the
	// repo arg (single fixed corpus); the result must be identical.
	res, err = sess.CallTool(ctx, &sdk.CallToolParams{
		Name: "search",
		Arguments: map[string]any{
			"query": "Model2Vec embedding",
			"mode":  "hybrid",
			"top_k": 5,
			"repo":  "/totally/bogus/repo/path/that/never/existed",
		},
	})
	if err != nil {
		t.Fatalf("CallTool(search with bogus repo): %v\n--stderr--\n%s", err, stderr.String())
	}
	txtB := res.Content[0].(*sdk.TextContent).Text
	if txtA != txtB {
		t.Errorf("bogus repo arg should be ignored (single-corpus contract)\n--without repo--\n%s\n\n--with bogus repo--\n%s",
			truncate(txtA, 800), truncate(txtB, 800))
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n…[truncated]"
}
