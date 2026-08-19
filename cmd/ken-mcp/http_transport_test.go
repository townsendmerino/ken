package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// runKenMCPExpectExit runs the real binary with env and asserts it exits with
// the given code, returning combined stderr. Used for the fail-loud startup
// guards of the remote HTTP transport (#15 / ADR-041).
func runKenMCPExpectExit(t *testing.T, wantCode int, env ...string) string {
	t.Helper()
	binPath := buildKenMCPOnce(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binPath)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}, env...)
	var stderr safeBuf
	cmd.Stderr = &stderr
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case wantCode == 0:
		if err != nil {
			t.Fatalf("expected clean exit, got %v\n--stderr--\n%s", err, stderr.String())
		}
	case errors.As(err, &ee):
		if ee.ExitCode() != wantCode {
			t.Fatalf("exit code = %d, want %d\n--stderr--\n%s", ee.ExitCode(), wantCode, stderr.String())
		}
	default:
		t.Fatalf("expected exit code %d, got err %v\n--stderr--\n%s", wantCode, err, stderr.String())
	}
	return stderr.String()
}

func TestHTTP_FailsLoud_NoAuthToken(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess test in -short mode")
	}
	out := runKenMCPExpectExit(t, 1, "KEN_MCP_TRANSPORT=http", "KEN_MCP_LOG_LEVEL=error")
	if !strings.Contains(out, "requires an auth token") {
		t.Errorf("stderr should explain the missing-token failure; got:\n%s", out)
	}
}

func TestHTTP_FailsLoud_SampleRows(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess test in -short mode")
	}
	out := runKenMCPExpectExit(t, 1,
		"KEN_MCP_TRANSPORT=http", "KEN_MCP_AUTH_TOKEN=tok", "KEN_DB_SAMPLE_ROWS=5", "KEN_MCP_LOG_LEVEL=error")
	if !strings.Contains(out, "KEN_DB_SAMPLE_ROWS") {
		t.Errorf("stderr should explain the PII/sampling rejection; got:\n%s", out)
	}
}

func TestHTTP_FailsLoud_UnreadableTokenFile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess test in -short mode")
	}
	out := runKenMCPExpectExit(t, 1,
		"KEN_MCP_TRANSPORT=http", "KEN_MCP_AUTH_TOKEN_FILE=/nonexistent/ken-token", "KEN_MCP_LOG_LEVEL=error")
	if !strings.Contains(out, "cannot read auth token") {
		t.Errorf("stderr should explain the unreadable token file; got:\n%s", out)
	}
}

// bearerRoundTripper injects the Authorization header (the SDK client transport
// has no header hook).
type bearerRoundTripper struct {
	token string
	base  http.RoundTripper
}

func (b bearerRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if b.token != "" {
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	return b.base.RoundTrip(r)
}

func httpBearerTransport(url, token string) *sdk.StreamableClientTransport {
	return &sdk.StreamableClientTransport{
		Endpoint:             url,
		DisableStandaloneSSE: true, // server is stateless
		HTTPClient:           &http.Client{Transport: bearerRoundTripper{token: token, base: http.DefaultTransport}},
	}
}

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// TestHTTP_Binary_ServesAuthedSession runs the REAL binary in http transport
// mode and drives a full authenticated MCP session over the network socket:
// the search tool round-trips with a valid token, and an unauthenticated
// connect is rejected. This closes the gap the in-process mcp-package test can't
// cover — the binary's KEN_MCP_TRANSPORT=http wiring end to end.
func TestHTTP_Binary_ServesAuthedSession(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess test in -short mode")
	}
	binPath := buildKenMCPOnce(t)
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(repoRoot, "testdata", "repo")
	addr := freePort(t)
	const token = "test-secret-token"

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binPath)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"),
		"KEN_MCP_TRANSPORT=http", "KEN_MCP_ADDR=" + addr, "KEN_MCP_AUTH_TOKEN=" + token,
		"KEN_MCP_MODE=bm25", "KEN_MCP_CHUNKER=regex", "KEN_MCP_LOG_LEVEL=error",
		"KEN_MCP_SNAPSHOT=0", "KEN_MCP_DEFAULT_REPO=" + fixture,
	}
	var stderr safeBuf
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	url := "http://" + addr

	// Wait for the server to build its index and start listening.
	var sess *sdk.ClientSession
	for range 150 {
		cctx, ccancel := context.WithTimeout(ctx, 2*time.Second)
		s, cerr := sdk.NewClient(&sdk.Implementation{Name: "t", Version: "0"}, nil).
			Connect(cctx, httpBearerTransport(url, token), nil)
		ccancel()
		if cerr == nil {
			sess = s
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if sess == nil {
		t.Fatalf("HTTP server never became ready on %s\n--stderr--\n%s", addr, stderr.String())
	}
	defer sess.Close()

	res, err := sess.CallTool(ctx, &sdk.CallToolParams{
		Name:      "search",
		Arguments: map[string]any{"query": "validate_user", "mode": "bm25", "top_k": 3},
	})
	if err != nil {
		t.Fatalf("CallTool(search) over HTTP: %v\n--stderr--\n%s", err, stderr.String())
	}
	txt := res.Content[0].(*sdk.TextContent).Text
	if !strings.Contains(txt, "Search results for:") {
		t.Errorf("search over HTTP missing expected output; got:\n%s", txt)
	}

	// An unauthenticated client must be rejected.
	badCtx, badCancel := context.WithTimeout(ctx, 5*time.Second)
	defer badCancel()
	if bad, badErr := sdk.NewClient(&sdk.Implementation{Name: "t", Version: "0"}, nil).
		Connect(badCtx, httpBearerTransport(url, ""), nil); badErr == nil {
		_ = bad.Close()
		t.Error("unauthenticated connect over HTTP should be rejected")
	}
}
