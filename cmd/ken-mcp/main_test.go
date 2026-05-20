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

// TestBinary_StdoutIsCleanJSONRPC is the load-bearing test for the
// stdout/stderr contract documented in main.go. It builds the actual
// ken-mcp binary, exec's it under the SDK's CommandTransport (which
// pipes stdin/stdout), and drives a real protocol session. If anything
// in main.go (or any imported library at startup) writes to stdout, the
// protocol stream is corrupted and this test fails loudly — the same
// failure agents would see.
func TestBinary_StdoutIsCleanJSONRPC(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess MCP test in -short mode")
	}

	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "ken-mcp")
	out, err := exec.Command("go", "build", "-o", binPath, "github.com/townsendmerino/ken/cmd/ken-mcp").CombinedOutput()
	if err != nil {
		t.Fatalf("go build ken-mcp: %v\n%s", err, out)
	}

	// Repo root is two levels up from cmd/ken-mcp/.
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(repoRoot, "testdata", "repo")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binPath)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"), // go-git reads ~/.gitconfig for some setups
		"KEN_MCP_MODE=bm25",
		"KEN_MCP_CHUNKER=regex",
		"KEN_MCP_LOG_LEVEL=error",
		"KEN_MCP_DEFAULT_REPO=" + fixture,
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	cli := sdk.NewClient(&sdk.Implementation{Name: "ken-mcp-test", Version: "0"}, nil)
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
	for _, tl := range tl.Tools {
		have[tl.Name] = true
		t.Logf("tool: %s — %s", tl.Name, tl.Description)
	}
	for _, name := range []string{"search", "find_related"} {
		if !have[name] {
			t.Errorf("missing tool %q (have %v)", name, have)
		}
	}

	res, err := sess.CallTool(ctx, &sdk.CallToolParams{
		Name: "search",
		Arguments: map[string]any{
			"query": "validate_user",
			"mode":  "bm25",
			"top_k": 3,
		},
	})
	if err != nil {
		t.Fatalf("CallTool(search): %v\n--stderr--\n%s", err, stderr.String())
	}
	txt := res.Content[0].(*sdk.TextContent).Text
	t.Logf("=== search 'validate_user' top-3 response ===\n%s", txt)
	for _, want := range []string{"Search results for:", "auth.py"} {
		if !strings.Contains(txt, want) {
			t.Errorf("search output missing %q\n--- got ---\n%s", want, txt)
		}
	}
}
