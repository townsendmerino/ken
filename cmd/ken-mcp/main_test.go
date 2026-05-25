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

	kenmcp "github.com/townsendmerino/ken/mcp"
)

// newCapturedLogger returns a kenmcp.Logger that writes to a buffer
// (level=LogDebug so every call is captured). Used by envInt/envEnum
// tests to assert that bad input produces the documented warn message.
func newCapturedLogger() (*kenmcp.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return kenmcp.NewLogger(buf, kenmcp.LogDebug), buf
}

// withEnv temporarily sets env vars for the duration of the test. Each
// test owns its env-var slots so parallel tests don't collide; we always
// use unique names like KEN_TEST_*.
func withEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

func TestEnvInt(t *testing.T) {
	cases := []struct {
		name     string
		envVal   string // "" means "not set"
		fallback int
		want     int
		wantWarn bool // expect a warn message
	}{
		{"missing", "", 99, 99, false},
		{"empty string", "", 99, 99, false},
		{"valid int", "7", 99, 7, false},
		{"zero is valid (caller decides semantics)", "0", 99, 0, false},
		{"negative is parsed (caller decides)", "-3", 99, -3, false},
		{"huge value", "999999999", 0, 999999999, false},
		{"invalid string falls back + warns", "of", 99, 99, true},
		{"trailing junk falls back + warns", "3abc", 99, 99, true},
		{"whitespace-only is treated as missing", "   ", 99, 99, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			const key = "KEN_TEST_INT"
			if c.envVal == "" {
				os.Unsetenv(key)
			} else {
				withEnv(t, map[string]string{key: c.envVal})
			}
			l, buf := newCapturedLogger()
			got := envInt(key, c.fallback, l)
			if got != c.want {
				t.Errorf("envInt(%q, fallback=%d) = %d, want %d", c.envVal, c.fallback, got, c.want)
			}
			hasWarn := strings.Contains(buf.String(), "invalid "+key)
			if hasWarn != c.wantWarn {
				t.Errorf("warn captured = %v, want %v\nlog output: %q", hasWarn, c.wantWarn, buf.String())
			}
		})
	}
}

func TestEnvEnum(t *testing.T) {
	allowed := []string{"bm25", "semantic", "hybrid"}
	cases := []struct {
		name     string
		envVal   string
		want     string
		wantWarn bool
	}{
		{"missing", "", "hybrid", false},
		{"empty string", "", "hybrid", false},
		{"whitespace-only is treated as missing", "  \t ", "hybrid", false},
		{"valid value", "semantic", "semantic", false},
		{"first allowed", "bm25", "bm25", false},
		{"case-sensitive mismatch falls back + warns", "Hybrid", "hybrid", true},
		{"all-caps mismatch", "HYBRID", "hybrid", true},
		{"junk falls back + warns", "lexical", "hybrid", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			const key = "KEN_TEST_ENUM"
			if c.envVal == "" {
				os.Unsetenv(key)
			} else {
				withEnv(t, map[string]string{key: c.envVal})
			}
			l, buf := newCapturedLogger()
			got := envEnum(key, allowed, "hybrid", l)
			if got != c.want {
				t.Errorf("envEnum(%q) = %q, want %q", c.envVal, got, c.want)
			}
			hasWarn := strings.Contains(buf.String(), "invalid "+key)
			if hasWarn != c.wantWarn {
				t.Errorf("warn captured = %v, want %v\nlog output: %q", hasWarn, c.wantWarn, buf.String())
			}
		})
	}
}

// TestEnvPath covers the "warn but keep the value" contract — downstream
// auto-downgrade logic depends on the value being passed through.
func TestEnvPath(t *testing.T) {
	const key = "KEN_TEST_PATH"
	dir := t.TempDir()
	file := filepath.Join(dir, "not-a-dir.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("missing returns empty no warn", func(t *testing.T) {
		os.Unsetenv(key)
		l, buf := newCapturedLogger()
		if got := envPath(key, l); got != "" {
			t.Errorf("envPath(unset) = %q, want \"\"", got)
		}
		if buf.Len() != 0 {
			t.Errorf("unexpected log output: %q", buf.String())
		}
	})

	t.Run("valid directory returns value no warn", func(t *testing.T) {
		withEnv(t, map[string]string{key: dir})
		l, buf := newCapturedLogger()
		if got := envPath(key, l); got != dir {
			t.Errorf("envPath(dir) = %q, want %q", got, dir)
		}
		if buf.Len() != 0 {
			t.Errorf("unexpected log output: %q", buf.String())
		}
	})

	t.Run("file path warns but keeps value", func(t *testing.T) {
		withEnv(t, map[string]string{key: file})
		l, buf := newCapturedLogger()
		if got := envPath(key, l); got != file {
			t.Errorf("envPath(file) = %q, want %q (value must be preserved)", got, file)
		}
		if !strings.Contains(buf.String(), "not a directory") {
			t.Errorf("expected 'not a directory' warn, got: %q", buf.String())
		}
	})

	t.Run("nonexistent warns but keeps value", func(t *testing.T) {
		bogus := filepath.Join(dir, "does-not-exist")
		withEnv(t, map[string]string{key: bogus})
		l, buf := newCapturedLogger()
		if got := envPath(key, l); got != bogus {
			t.Errorf("envPath(bogus) = %q, want %q (value must be preserved)", got, bogus)
		}
		if !strings.Contains(buf.String(), bogus) {
			t.Errorf("expected warn naming the path, got: %q", buf.String())
		}
	})
}

func TestEnvDuration(t *testing.T) {
	const key = "KEN_TEST_DURATION"
	cases := []struct {
		name     string
		envVal   string
		fallback time.Duration
		want     time.Duration
		wantWarn bool
	}{
		{"missing", "", 0, 0, false},
		{"empty", "", 5 * time.Minute, 5 * time.Minute, false},
		{"valid 5m", "5m", 0, 5 * time.Minute, false},
		{"valid 1h30m", "1h30m", 0, 90 * time.Minute, false},
		{"zero is valid", "0s", time.Minute, 0, false},
		{"invalid string falls back + warns", "soonish", time.Minute, time.Minute, true},
		{"trailing junk falls back + warns", "5mblah", time.Minute, time.Minute, true},
		{"whitespace-only is missing", "  \t", time.Minute, time.Minute, false},
		{"negative falls back + warns", "-5m", time.Minute, time.Minute, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.envVal == "" {
				os.Unsetenv(key)
			} else {
				withEnv(t, map[string]string{key: c.envVal})
			}
			l, buf := newCapturedLogger()
			got := envDuration(key, c.fallback, l)
			if got != c.want {
				t.Errorf("envDuration(%q, %s) = %s, want %s", c.envVal, c.fallback, got, c.want)
			}
			hasWarn := strings.Contains(buf.String(), "invalid "+key)
			if hasWarn != c.wantWarn {
				t.Errorf("warn captured = %v, want %v\nlog: %q", hasWarn, c.wantWarn, buf.String())
			}
		})
	}
}

func TestEnvDSN(t *testing.T) {
	const key = "KEN_TEST_DSN"
	cases := []struct {
		name     string
		envVal   string
		want     string
		wantWarn bool
	}{
		{"missing", "", "", false},
		{"empty", "", "", false},
		{"valid postgres://", "postgres://user:pass@host:5432/db?sslmode=disable", "postgres://user:pass@host:5432/db?sslmode=disable", false},
		{"valid postgresql://", "postgresql://h/d", "postgresql://h/d", false},
		{"case-insensitive scheme", "POSTGRES://h/d", "POSTGRES://h/d", false},
		{"wrong scheme falls back + warns", "mysql://h/d", "", true},
		{"http scheme falls back + warns", "http://h/d", "", true},
		{"libpq key=value form falls back + warns", "host=localhost port=5432 dbname=mydb", "", true},
		{"missing host falls back + warns", "postgres:///d", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.envVal == "" {
				os.Unsetenv(key)
			} else {
				withEnv(t, map[string]string{key: c.envVal})
			}
			l, buf := newCapturedLogger()
			got := envDSN(key, l)
			if got != c.want {
				t.Errorf("envDSN(%q) = %q, want %q", c.envVal, got, c.want)
			}
			hasWarn := strings.Contains(buf.String(), "invalid "+key)
			if hasWarn != c.wantWarn {
				t.Errorf("warn captured = %v, want %v\nlog: %q", hasWarn, c.wantWarn, buf.String())
			}
		})
	}
}

func TestEnvPathOrURL(t *testing.T) {
	const key = "KEN_TEST_PATH_OR_URL"
	dir := t.TempDir()

	cases := []struct {
		name     string
		envVal   string
		want     string
		wantWarn bool
	}{
		{"missing", "", "", false},
		{"valid directory", dir, dir, false},
		{"http URL passes through", "http://example.com/repo", "http://example.com/repo", false},
		{"https URL passes through", "https://github.com/foo/bar", "https://github.com/foo/bar", false},
		{"junk warns but keeps value", "neither-a-path-nor-a-url", "neither-a-path-nor-a-url", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.envVal == "" {
				os.Unsetenv(key)
			} else {
				withEnv(t, map[string]string{key: c.envVal})
			}
			l, buf := newCapturedLogger()
			got := envPathOrURL(key, l)
			if got != c.want {
				t.Errorf("envPathOrURL(%q) = %q, want %q", c.envVal, got, c.want)
			}
			hasWarn := strings.Contains(buf.String(), key+"=")
			if hasWarn != c.wantWarn {
				t.Errorf("warn captured = %v, want %v\nlog output: %q", hasWarn, c.wantWarn, buf.String())
			}
		})
	}
}

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

// TestBinary_StdoutIsCleanJSONRPC_WithDB is the v0.7.0 variant: same
// load-bearing stdout-cleanliness check, but with all KEN_DB_* env vars
// set so the Tier-2 code path (DB introspection, Refresher, SIGHUP
// handler) fires in the spawned binary. If pgx or anything in the DB
// path writes to stdout, CommandTransport can't parse the JSON-RPC
// response and this test fails loudly.
//
// Skipped when KEN_DB_TEST_DSN is unset (contributors without Postgres
// run `go test ./...` and this skips silently).
func TestBinary_StdoutIsCleanJSONRPC_WithDB(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess MCP test in -short mode")
	}
	dsn := os.Getenv("KEN_DB_TEST_DSN")
	if dsn == "" {
		t.Skip("KEN_DB_TEST_DSN not set; see internal/db/integration_test.go for setup")
	}

	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "ken-mcp")
	out, err := exec.Command("go", "build", "-o", binPath, "github.com/townsendmerino/ken/cmd/ken-mcp").CombinedOutput()
	if err != nil {
		t.Fatalf("go build ken-mcp: %v\n%s", err, out)
	}

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
		"HOME=" + os.Getenv("HOME"),
		"KEN_MCP_MODE=bm25",
		"KEN_MCP_CHUNKER=regex",
		"KEN_MCP_LOG_LEVEL=info", // verbose to catch any accidental stdout writes
		"KEN_MCP_DEFAULT_REPO=" + fixture,
		"KEN_DB_DSN=" + dsn,
		"KEN_DB_SAMPLE_ROWS=3",
		"KEN_DB_REINDEX_INTERVAL=30s",
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	cli := sdk.NewClient(&sdk.Implementation{Name: "ken-mcp-test-db", Version: "0"}, nil)
	sess, err := cli.Connect(ctx, &sdk.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("Connect (with DB env): %v\n--stderr--\n%s", err, stderr.String())
	}
	defer sess.Close()

	// Drive a tool call to make sure the JSON-RPC roundtrip is clean
	// even after the DB startup chatter (Tier-2 init logs to stderr,
	// which is fine — we only care about stdout).
	res, err := sess.CallTool(ctx, &sdk.CallToolParams{
		Name: "search",
		Arguments: map[string]any{
			"query": "validate_user",
			"mode":  "bm25",
			"top_k": 3,
		},
	})
	if err != nil {
		t.Fatalf("CallTool(search) with DB env: %v\n--stderr--\n%s", err, stderr.String())
	}
	txt := res.Content[0].(*sdk.TextContent).Text
	if !strings.Contains(txt, "Search results for:") {
		t.Errorf("search output malformed with DB env:\n%s", txt)
	}

	// stderr should mention the Tier 2 wiring (so we know the code path
	// actually ran, not silently skipped).
	stderrStr := stderr.String()
	if !strings.Contains(stderrStr, "Tier 2: indexed") {
		t.Errorf("expected 'Tier 2: indexed' in stderr (proves DB code ran), got:\n%s", stderrStr)
	}
}
