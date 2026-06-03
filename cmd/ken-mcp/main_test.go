package main

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	_ "modernc.org/sqlite"

	kenmcp "github.com/townsendmerino/ken/mcp"
)

// safeBuf is a goroutine-safe wrapper around bytes.Buffer used by the
// stdout-cleanliness tests below. The exec.Cmd's stderr-copy
// goroutine (launched by sdk.CommandTransport) writes to it while
// the test goroutine reads stderr.String() after sess.CallTool
// returns — bytes.Buffer is not goroutine-safe, so a plain
// `var stderr bytes.Buffer` produces a real data race under
// `go test -race`. Caught as M1 in the post-v0.8.3 bug review. The
// mutex serializes Write/String; the print-listen-script test (which
// uses cmd.Run, a blocking call that includes io-pipe drain) doesn't
// need this and continues to use plain bytes.Buffer.
type safeBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *safeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *safeBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// sqliteOpenForTest is a tiny helper used by the SQLite stdout-clean
// test to materialize a fixture .db file. Lives here (not in
// internal/db/sqlite_test.go) because the stdout-clean test runs
// without the dbintegration build tag — every checkout's `go test ./...`
// hits it.
func sqliteOpenForTest(path string) (*sql.DB, error) {
	return sql.Open("sqlite", path)
}

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

func TestEnvBool(t *testing.T) {
	const key = "KEN_TEST_BOOL"
	cases := []struct {
		name     string
		envVal   string
		fallback bool
		want     bool
		wantWarn bool
	}{
		{"missing returns fallback", "", true, true, false},
		{"missing returns fallback false", "", false, false, false},
		{"1 is true", "1", false, true, false},
		{"true is true", "true", false, true, false},
		{"TRUE case-insensitive", "TRUE", false, true, false},
		{"yes is true", "yes", false, true, false},
		{"on is true", "on", false, true, false},
		{"0 is false", "0", true, false, false},
		{"false is false", "false", true, false, false},
		{"no is false", "no", true, false, false},
		{"off is false", "off", true, false, false},
		{"junk warns + returns fallback", "maybe", true, true, true},
		{"empty is fallback", "", false, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.envVal == "" {
				os.Unsetenv(key)
			} else {
				withEnv(t, map[string]string{key: c.envVal})
			}
			l, buf := newCapturedLogger()
			got := envBool(key, c.fallback, l)
			if got != c.want {
				t.Errorf("envBool(%q, fallback=%v) = %v, want %v", c.envVal, c.fallback, got, c.want)
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
		// v0.7.1: SQLite schemes accepted.
		{"valid sqlite:// absolute path", "sqlite:///var/data/dev.db", "sqlite:///var/data/dev.db", false},
		{"valid sqlite3:// absolute path", "sqlite3:///var/data/dev.db", "sqlite3:///var/data/dev.db", false},
		{"valid sqlite:// relative path", "sqlite://./dev.db", "sqlite://./dev.db", false},
		{"case-insensitive sqlite scheme", "SQLITE:///var/data/dev.db", "SQLITE:///var/data/dev.db", false},
		// v0.7.2: MySQL URL form + native form accepted.
		{"valid mysql:// URL", "mysql://alice:s3cret@db.local:3306/mydb", "mysql://alice:s3cret@db.local:3306/mydb", false},
		{"mysql:// with non-default port", "mysql://alice:s3cret@db.local:33306/mydb?parseTime=true", "mysql://alice:s3cret@db.local:33306/mydb?parseTime=true", false},
		{"native MySQL tcp form", "alice:s3cret@tcp(db.local:3306)/mydb?parseTime=true", "alice:s3cret@tcp(db.local:3306)/mydb?parseTime=true", false},
		{"native MySQL unix-socket form", "alice:s3cret@unix(/var/run/mysqld/mysqld.sock)/mydb", "alice:s3cret@unix(/var/run/mysqld/mysqld.sock)/mydb", false},
		{"missing host on mysql falls back + warns", "mysql:///d", "", true},
		// Typoed schemes / non-DB schemes fall back.
		{"typoed sqlite falls back + warns", "sqliet:///dev.db", "", true},
		{"http scheme falls back + warns", "http://h/d", "", true},
		{"libpq key=value form falls back + warns", "host=localhost port=5432 dbname=mydb", "", true},
		{"missing host on postgres falls back + warns", "postgres:///d", "", true},
		// SQLite without host is fine (path-only).
		{"sqlite without host is OK", "sqlite:///d.db", "sqlite:///d.db", false},
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

// TestEnvCommaList covers KEN_DB_SCHEMAS / KEN_DB_EXCLUDE_SCHEMAS
// parsing: whitespace trimming, empty-element filtering, the "all
// whitespace / nothing left" → nil rule.
func TestEnvCommaList(t *testing.T) {
	const key = "KEN_TEST_COMMA_LIST"
	cases := []struct {
		name   string
		envVal string
		want   []string
	}{
		{"missing", "", nil},
		{"empty", "", nil},
		{"whitespace-only", "  \t ", nil},
		{"single value", "public", []string{"public"}},
		{"two values", "public,billing", []string{"public", "billing"}},
		{"whitespace around commas", " public , billing ", []string{"public", "billing"}},
		{"trailing comma", "public,billing,", []string{"public", "billing"}},
		{"empty element in middle", "public,,billing", []string{"public", "billing"}},
		{"all empty (commas only)", " , , , ", nil},
		{"three values realistic", "audit,cron,legacy", []string{"audit", "cron", "legacy"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.envVal == "" {
				os.Unsetenv(key)
			} else {
				withEnv(t, map[string]string{key: c.envVal})
			}
			got := envCommaList(key)
			if !equalStringSlices(got, c.want) {
				t.Errorf("envCommaList(%q) = %v, want %v", c.envVal, got, c.want)
			}
		})
	}
}

// equalStringSlices is a small local helper; nil and empty slice are
// treated as equal (both represent "no entries").
func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestBinary_StdoutIsCleanJSONRPC is the load-bearing test for the
// stdout/stderr contract documented in main.go. It builds the actual
// ken-mcp binary, exec's it under the SDK's CommandTransport (which
// pipes stdin/stdout), and drives a real protocol session. If anything
// in main.go (or any imported library at startup) writes to stdout, the
// protocol stream is corrupted and this test fails loudly — the same
// failure agents would see.
// TestRedactDSN pins the M1 fix — redactDSN must scrub credentials
// from every DSN shape envDSN accepts, including the native
// go-sql-driver/mysql form that has no `://` scheme. Pre-fix, the
// native form's userinfo survived a no-op `url.Parse` + `u.User =
// nil` round-trip and the password landed in the startup log on
// stderr.
func TestRedactDSN(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// URL form with userinfo: scheme + path preserved, userinfo stripped.
		{"postgres URL", "postgres://alice:s3cret@host/db", "postgres://host/db"},
		{"postgres URL with port + sslmode", "postgres://alice:s3cret@host:5432/db?sslmode=disable", "postgres://host:5432/db?sslmode=disable"},
		// mysql:// + tcp(...) doesn't pass url.Parse cleanly (parens
		// in the authority), so the fail-safe <redacted> branch fires.
		// That's still safe — the password doesn't leak.
		{"mysql URL form with parens", "mysql://alice:s3cret@tcp(host:3306)/db", "<redacted>"},
		// Native go-sql-driver/mysql form: no `://`; strip up to first '@'.
		{"native MySQL TCP", "alice:s3cret@tcp(host:3306)/db?parseTime=true", "tcp(host:3306)/db?parseTime=true"},
		{"native MySQL unix socket", "alice:s3cret@unix(/var/run/mysqld.sock)/db", "unix(/var/run/mysqld.sock)/db"},
		// SQLite: no userinfo to redact; unchanged.
		{"sqlite three-slash", "sqlite:///tmp/test.db", "sqlite:///tmp/test.db"},
		// Edge case: input contains '@' but no scheme — treat as native form.
		{"naked user@host", "user@host", "host"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactDSN(tc.in)
			if got != tc.want {
				t.Errorf("redactDSN(%q) = %q, want %q", tc.in, got, tc.want)
			}
			// Belt-and-suspenders: the canonical bad password must
			// never survive in the output.
			if strings.Contains(got, "s3cret") {
				t.Errorf("redactDSN(%q) leaked the password: %q", tc.in, got)
			}
		})
	}
}

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
	var stderr safeBuf
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
	// Stage 8 Track 2 + 1.0 utilities: all nine tools must be
	// registered (search + find_related from v0.6; definition,
	// references, callers, outline, symbols from Stage 8;
	// status + recently_changed from the 1.0 ship-list).
	// reindex_db remains hidden when DB is unset
	// (separate ADR-020 invariant).
	for _, name := range []string{"search", "find_related", "definition", "references", "callers", "outline", "symbols", "status", "recently_changed"} {
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

	// Stage 8 Track 2 structural-tool invocations. Each call should
	// (a) succeed, (b) NOT pollute stdout (the SDK session would
	// fail to parse the next JSON-RPC reply otherwise — that's
	// what makes the stdout-clean contract self-enforcing). The
	// testdata/repo fixture has one Python file (auth.py); the
	// structural index picks it up and these calls land non-empty
	// responses.
	for _, tc := range []struct {
		name string
		args map[string]any
		want string // a substring present in either the success or empty-result response shape
	}{
		// `symbols`: testdata/repo has one .py file (auth.py)
		// defining validate_user, User, etc. — the header always
		// appears.
		{"symbols", map[string]any{}, "Symbols"},
		// `definition`: validate_user is a top-level def in auth.py.
		{"definition", map[string]any{"symbol": "validate_user"}, "Definition"},
		// `references`: validate_user is defined but not called in
		// this fixture, so the response is the "No references found
		// for..." text. Both shapes contain the symbol name; check
		// for that as the catch-all.
		{"references", map[string]any{"symbol": "validate_user"}, "validate_user"},
		// `callers`: same situation as references — validate_user is
		// defined but not called in the fixture, so the response is
		// the "No callers found for..." text. Both shapes contain
		// the symbol name.
		{"callers", map[string]any{"symbol": "validate_user"}, "validate_user"},
		// `outline`: auth.py defines class User and func
		// validate_user, so the outline is non-empty.
		{"outline", map[string]any{"path": "auth.py"}, "Outline"},
	} {
		t.Run("structural_"+tc.name, func(t *testing.T) {
			res, err := sess.CallTool(ctx, &sdk.CallToolParams{
				Name:      tc.name,
				Arguments: tc.args,
			})
			if err != nil {
				t.Fatalf("CallTool(%s): %v\n--stderr--\n%s", tc.name, err, stderr.String())
			}
			if len(res.Content) == 0 {
				t.Fatalf("CallTool(%s): empty content", tc.name)
			}
			txt := res.Content[0].(*sdk.TextContent).Text
			t.Logf("=== %s response ===\n%s", tc.name, txt)
			if !strings.Contains(txt, tc.want) {
				t.Errorf("%s output missing %q\n--- got ---\n%s", tc.name, tc.want, txt)
			}
		})
	}
}

// TestBinary_StdoutIsCleanJSONRPC_WithRerank is the M5 variant: boots
// ken-mcp with KEN_MCP_RERANK=on and a valid CodeRankEmbed snapshot,
// drives a hybrid-rerank query, and asserts the stdout/stderr contract
// still holds — the M4 NeuralReranker's model load and forward passes
// must NEVER write to stdout, otherwise the JSON-RPC channel gets
// corrupted.
//
// Skipped without both models symlinked: testdata/model (Model2Vec for
// stage-1 hybrid) AND testdata/encoder-model (for the reranker).
func TestBinary_StdoutIsCleanJSONRPC_WithRerank(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess MCP rerank test in -short mode")
	}

	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	hybridModel := filepath.Join(repoRoot, "testdata", "model")
	rerankModel := filepath.Join(repoRoot, "testdata", "encoder-model")
	for _, p := range []string{
		filepath.Join(hybridModel, "model.safetensors"),
		filepath.Join(rerankModel, "model.safetensors"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("missing %s — both testdata/model + testdata/encoder-model symlinks required", p)
		}
	}

	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "ken-mcp")
	out, err := exec.Command("go", "build", "-o", binPath, "github.com/townsendmerino/ken/cmd/ken-mcp").CombinedOutput()
	if err != nil {
		t.Fatalf("go build ken-mcp: %v\n%s", err, out)
	}

	fixture := filepath.Join(repoRoot, "testdata", "repo")
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binPath)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"KEN_MCP_MODE=hybrid-rerank",
		"KEN_MCP_CHUNKER=regex",
		"KEN_MCP_LOG_LEVEL=info", // so we can verify rerank=on lands on stderr
		"KEN_MCP_DEFAULT_REPO=" + fixture,
		"KEN_MCP_MODEL_DIR=" + hybridModel,
		"KEN_MCP_RERANK=on",
		"KEN_MCP_RERANK_MODEL_DIR=" + rerankModel,
		"KEN_MCP_RERANK_TOP_N=10", // small head keeps the test fast
		"KEN_MCP_RERANK_BETA=0.25",
	}
	var stderr safeBuf
	cmd.Stderr = &stderr

	cli := sdk.NewClient(&sdk.Implementation{Name: "ken-mcp-rerank-test", Version: "0"}, nil)
	sess, err := cli.Connect(ctx, &sdk.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("Connect: %v\n--stderr--\n%s", err, stderr.String())
	}
	defer sess.Close()

	// Drive a hybrid-rerank query — exercises the M4 NeuralReranker
	// forward pass end-to-end through the MCP JSON-RPC channel.
	res, err := sess.CallTool(ctx, &sdk.CallToolParams{
		Name: "search",
		Arguments: map[string]any{
			"query": "validate user credentials",
			"mode":  "hybrid-rerank",
			"top_k": 3,
		},
	})
	if err != nil {
		t.Fatalf("CallTool(search, mode=hybrid-rerank): %v\n--stderr--\n%s", err, stderr.String())
	}
	txt := res.Content[0].(*sdk.TextContent).Text
	t.Logf("=== hybrid-rerank 'validate user credentials' top-3 ===\n%s", txt)
	if !strings.Contains(txt, "Search results for:") {
		t.Errorf("search output missing header\n--- got ---\n%s", txt)
	}

	// Pin the M5 startup-log discipline: the rerank=on status MUST
	// appear on stderr (proves the model loaded), and the load line
	// MUST mention the snapshot dir.
	errStr := stderr.String()
	if !strings.Contains(errStr, "rerank=on") {
		t.Errorf("startup log missing rerank=on; stderr:\n%s", errStr)
	}
	if !strings.Contains(errStr, "rerank: loaded") {
		t.Errorf("startup log missing 'rerank: loaded' line; stderr:\n%s", errStr)
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
	var stderr safeBuf
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

// TestBinary_PrintListenScript_StdoutIsScript confirms the v0.8.0
// `ken-mcp print-listen-script` subcommand emits the SQL setup script
// to stdout (and only the script — no startup chatter, no JSON-RPC
// preamble). The subcommand short-circuits main() before the MCP
// server starts, so writing to stdout here is intentional and safe.
//
// Re-runnability: the script is idempotent (`DROP IF EXISTS` pair),
// so a second pipe to psql doesn't error. We verify the markers that
// must be present for the trigger to install.
func TestBinary_PrintListenScript_StdoutIsScript(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess subcommand test in -short mode")
	}
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "ken-mcp")
	out, err := exec.Command("go", "build", "-o", binPath, "github.com/townsendmerino/ken/cmd/ken-mcp").CombinedOutput()
	if err != nil {
		t.Fatalf("go build ken-mcp: %v\n%s", err, out)
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.Command(binPath, "print-listen-script")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("run print-listen-script: %v\n--stderr--\n%s", err, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("print-listen-script wrote to stderr (should be silent):\n%s", stderr.String())
	}
	script := stdout.String()
	for _, want := range []string{
		"CREATE EVENT TRIGGER ken_schema_changed_trigger",
		"pg_notify('ken_schema_changed'",
		"DROP EVENT TRIGGER IF EXISTS ken_schema_changed_trigger",
		"COMMIT;",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("print-listen-script output missing %q", want)
		}
	}
}

// TestBinary_StdoutIsCleanJSONRPC_WithListen is the v0.8.0 sibling of
// _WithDB / _WithSQLite / _WithMySQL: spawns the real ken-mcp binary
// with Postgres DSN + KEN_DB_LISTEN=1 set so the listener goroutine
// fires. Confirms the listener's dedicated pgx.Conn doesn't leak
// anything to stdout — which would break the JSON-RPC channel.
//
// The listener may log "event trigger not installed" if the test
// Postgres doesn't have it (and that's fine — we're testing stdout
// cleanliness, not listener happy-path; that's the job of
// internal/db/listen_integration_test.go). Either way: nothing goes
// to stdout.
//
// Skipped when KEN_DB_TEST_DSN is unset; CI sets it via the
// postgres:16-alpine service container.
func TestBinary_StdoutIsCleanJSONRPC_WithListen(t *testing.T) {
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
		"KEN_MCP_LOG_LEVEL=info",
		"KEN_MCP_DEFAULT_REPO=" + fixture,
		"KEN_DB_DSN=" + dsn,
		"KEN_DB_LISTEN=1",
	}
	var stderr safeBuf
	cmd.Stderr = &stderr

	cli := sdk.NewClient(&sdk.Implementation{Name: "ken-mcp-test-listen", Version: "0"}, nil)
	sess, err := cli.Connect(ctx, &sdk.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("Connect (with LISTEN env): %v\n--stderr--\n%s", err, stderr.String())
	}
	defer sess.Close()

	// Drive a tool call so any post-startup stdout pollution surfaces.
	res, err := sess.CallTool(ctx, &sdk.CallToolParams{
		Name: "search",
		Arguments: map[string]any{
			"query": "validate_user",
			"mode":  "bm25",
			"top_k": 3,
		},
	})
	if err != nil {
		t.Fatalf("CallTool(search) with LISTEN env: %v\n--stderr--\n%s", err, stderr.String())
	}
	txt := res.Content[0].(*sdk.TextContent).Text
	if !strings.Contains(txt, "Search results for:") {
		t.Errorf("search output malformed with LISTEN env:\n%s", txt)
	}

	// stderr should mention the listener startup (proves the code path
	// actually ran, not silently skipped via ErrListenNotSupported).
	// v0.8.0 Part 3 refactor: SetupTier2 owns the listener-start
	// path now (the old "Tier 2: starting LISTEN/NOTIFY listener"
	// log line was inside the old wireDBTier2 body that's been
	// folded into SetupTier2). The CLI-side log line that proves
	// the listener code ran is the "KEN_DB_LISTEN=1" announcement
	// emitted in wireDBTier2 just before calling SetupTier2 with
	// enableListen=true. We accept either that line OR the
	// listener's own "active on channel" line (which fires once the
	// listener goroutine inside SetupTier2 connects + LISTENs).
	stderrStr := stderr.String()
	listenerPathRan := strings.Contains(stderrStr, "KEN_DB_LISTEN=1") ||
		strings.Contains(stderrStr, "active on channel")
	if !listenerPathRan {
		t.Errorf("expected a listener-path indicator ('KEN_DB_LISTEN=1' or 'active on channel') in stderr (proves listener code ran), got:\n%s", stderrStr)
	}
}

// TestBinary_StdoutIsCleanJSONRPC_WithReindexDB is the v0.8.0 Part 2
// sibling of _WithDB / _WithListen. Spawns ken-mcp with KEN_DB_DSN
// set, then drives a `reindex_db` tool call through sdk.CommandTransport
// — the existing _WithDB test only calls `search`, so the reindex_db
// tool's full code path (registration, handler, callback into the
// Refresher) wasn't audited for stdout cleanliness until this test.
//
// The new code path doesn't obviously introduce stdout writes
// (handler uses the same textResult shim search/find_related use; the
// reindexCallback in main.go is just errors.Is + time.Now + a
// closure), so this test is defense-in-depth — it would catch a
// future regression where, say, someone adds a log.Print to the
// callback without thinking about which writer it goes to.
//
// Skipped when KEN_DB_TEST_DSN is unset; CI sets it via the
// postgres:16-alpine service container.
func TestBinary_StdoutIsCleanJSONRPC_WithReindexDB(t *testing.T) {
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
		"KEN_MCP_LOG_LEVEL=info",
		"KEN_MCP_DEFAULT_REPO=" + fixture,
		"KEN_DB_DSN=" + dsn,
	}
	var stderr safeBuf
	cmd.Stderr = &stderr

	cli := sdk.NewClient(&sdk.Implementation{Name: "ken-mcp-test-reindex", Version: "0"}, nil)
	sess, err := cli.Connect(ctx, &sdk.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("Connect (with reindex_db env): %v\n--stderr--\n%s", err, stderr.String())
	}
	defer sess.Close()

	// reindex_db should appear in the tools/list response (proves the
	// Reindex callback was wired into Config — without it, the tool
	// isn't registered).
	tl, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v\n--stderr--\n%s", err, stderr.String())
	}
	have := map[string]bool{}
	for _, t := range tl.Tools {
		have[t.Name] = true
	}
	if !have["reindex_db"] {
		t.Fatalf("reindex_db not in tools/list (have %v); Config.Reindex wasn't wired",
			have)
	}

	// Call the tool. Successful response shape: "Reindexed in Nms."
	// (or "Reindex already in progress" if the startup IndexSchema is
	// still holding the mutex — either case is stdout-clean).
	res, err := sess.CallTool(ctx, &sdk.CallToolParams{
		Name:      "reindex_db",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool(reindex_db): %v\n--stderr--\n%s", err, stderr.String())
	}
	txt := res.Content[0].(*sdk.TextContent).Text
	if !strings.Contains(txt, "Reindexed in") && !strings.Contains(txt, "Reindex already in progress") {
		t.Errorf("unexpected reindex_db response: %s", txt)
	}
}

// TestBinary_StdoutIsCleanJSONRPC_WithMySQL is the v0.7.2 sibling of
// _WithDB / _WithSQLite: spawns the real ken-mcp binary with a MySQL
// DSN set via KEN_DB_MYSQL_TEST_DSN. Confirms go-sql-driver/mysql's
// default logger stays on stderr and never leaks to stdout — which
// would break the JSON-RPC channel.
//
// Skipped when KEN_DB_MYSQL_TEST_DSN is unset. CI sets it via the
// mysql:8 service container in .github/workflows/ci.yml.
func TestBinary_StdoutIsCleanJSONRPC_WithMySQL(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess MCP test in -short mode")
	}
	dsn := os.Getenv("KEN_DB_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("KEN_DB_MYSQL_TEST_DSN not set; see internal/db/mysql_integration_test.go for setup")
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

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binPath)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"KEN_MCP_MODE=bm25",
		"KEN_MCP_CHUNKER=regex",
		"KEN_MCP_LOG_LEVEL=info",
		"KEN_MCP_DEFAULT_REPO=" + fixture,
		"KEN_DB_DSN=" + dsn,
		"KEN_DB_SAMPLE_ROWS=2",
		// Exercise schema filtering too — confirms both env vars feed
		// through wireDBTier2 without disturbing the stdout contract.
		"KEN_DB_SCHEMAS=ken_test",
	}
	var stderr safeBuf
	cmd.Stderr = &stderr

	cli := sdk.NewClient(&sdk.Implementation{Name: "ken-mcp-test-mysql", Version: "0"}, nil)
	sess, err := cli.Connect(ctx, &sdk.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("Connect (with MySQL DSN): %v\n--stderr--\n%s", err, stderr.String())
	}
	defer sess.Close()

	res, err := sess.CallTool(ctx, &sdk.CallToolParams{
		Name: "search",
		Arguments: map[string]any{
			"query": "validate_user",
			"mode":  "bm25",
			"top_k": 3,
		},
	})
	if err != nil {
		t.Fatalf("CallTool(search) with MySQL DSN: %v\n--stderr--\n%s", err, stderr.String())
	}
	txt := res.Content[0].(*sdk.TextContent).Text
	if !strings.Contains(txt, "Search results for:") {
		t.Errorf("search output malformed with MySQL DSN:\n%s", txt)
	}

	stderrStr := stderr.String()
	if !strings.Contains(stderrStr, "Tier 2: indexed") {
		t.Errorf("expected 'Tier 2: indexed' in stderr (proves MySQL code ran), got:\n%s", stderrStr)
	}
}

// TestBinary_StdoutIsCleanJSONRPC_WithMariaDB is the v0.8.1 Part B
// sibling of _WithMySQL: spawns the real ken-mcp binary with a MariaDB
// DSN set via KEN_DB_MARIADB_TEST_DSN. MariaDB shares the
// go-sql-driver/mysql driver with MySQL (wire-compatible), so the
// stdout-contract surface is the same; this test exists to (1) pin the
// contract against the live MariaDB server version output, and (2)
// exercise the v0.8.1 integer-display-width normalization path against
// real MariaDB chunks. ADR-021 records the divergence-audit findings.
//
// Skipped when KEN_DB_MARIADB_TEST_DSN is unset. CI sets it via the
// mariadb:11-jammy service container in .github/workflows/ci.yml.
func TestBinary_StdoutIsCleanJSONRPC_WithMariaDB(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess MCP test in -short mode")
	}
	dsn := os.Getenv("KEN_DB_MARIADB_TEST_DSN")
	if dsn == "" {
		t.Skip("KEN_DB_MARIADB_TEST_DSN not set; see internal/db/mysql_integration_test.go for setup")
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

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binPath)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"KEN_MCP_MODE=bm25",
		"KEN_MCP_CHUNKER=regex",
		"KEN_MCP_LOG_LEVEL=info",
		"KEN_MCP_DEFAULT_REPO=" + fixture,
		"KEN_DB_DSN=" + dsn,
		"KEN_DB_SAMPLE_ROWS=2",
		"KEN_DB_SCHEMAS=ken_test",
	}
	var stderr safeBuf
	cmd.Stderr = &stderr

	cli := sdk.NewClient(&sdk.Implementation{Name: "ken-mcp-test-mariadb", Version: "0"}, nil)
	sess, err := cli.Connect(ctx, &sdk.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("Connect (with MariaDB DSN): %v\n--stderr--\n%s", err, stderr.String())
	}
	defer sess.Close()

	res, err := sess.CallTool(ctx, &sdk.CallToolParams{
		Name: "search",
		Arguments: map[string]any{
			"query": "validate_user",
			"mode":  "bm25",
			"top_k": 3,
		},
	})
	if err != nil {
		t.Fatalf("CallTool(search) with MariaDB DSN: %v\n--stderr--\n%s", err, stderr.String())
	}
	txt := res.Content[0].(*sdk.TextContent).Text
	if !strings.Contains(txt, "Search results for:") {
		t.Errorf("search output malformed with MariaDB DSN:\n%s", txt)
	}

	stderrStr := stderr.String()
	if !strings.Contains(stderrStr, "Tier 2: indexed") {
		t.Errorf("expected 'Tier 2: indexed' in stderr (proves MariaDB code ran), got:\n%s", stderrStr)
	}
}

// TestBinary_StdoutIsCleanJSONRPC_WithSQLite is the v0.7.1 sibling of
// _WithDB: spawns the real ken-mcp binary with KEN_DB_DSN pointing at a
// local SQLite file (created by this test). Confirms modernc.org/sqlite
// doesn't write anything to stdout that would break the JSON-RPC channel.
//
// Unlike _WithDB this requires no external service — the SQLite file is
// created in t.TempDir(). Auto-runs in stock CI.
func TestBinary_StdoutIsCleanJSONRPC_WithSQLite(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess MCP test in -short mode")
	}

	// Build a temp SQLite file with a small schema.
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	{
		conn, err := sqliteOpenForTest(dbPath)
		if err != nil {
			t.Fatalf("open sqlite: %v", err)
		}
		if _, err := conn.Exec(`CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT NOT NULL UNIQUE)`); err != nil {
			t.Fatalf("create table: %v", err)
		}
		_ = conn.Close()
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
		"KEN_MCP_LOG_LEVEL=info",
		"KEN_MCP_DEFAULT_REPO=" + fixture,
		"KEN_DB_DSN=sqlite://" + dbPath,
		"KEN_DB_SAMPLE_ROWS=2",
	}
	var stderr safeBuf
	cmd.Stderr = &stderr

	cli := sdk.NewClient(&sdk.Implementation{Name: "ken-mcp-test-sqlite", Version: "0"}, nil)
	sess, err := cli.Connect(ctx, &sdk.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("Connect (with SQLite DSN): %v\n--stderr--\n%s", err, stderr.String())
	}
	defer sess.Close()

	res, err := sess.CallTool(ctx, &sdk.CallToolParams{
		Name: "search",
		Arguments: map[string]any{
			"query": "validate_user",
			"mode":  "bm25",
			"top_k": 3,
		},
	})
	if err != nil {
		t.Fatalf("CallTool(search) with SQLite DSN: %v\n--stderr--\n%s", err, stderr.String())
	}
	txt := res.Content[0].(*sdk.TextContent).Text
	if !strings.Contains(txt, "Search results for:") {
		t.Errorf("search output malformed with SQLite DSN:\n%s", txt)
	}

	stderrStr := stderr.String()
	if !strings.Contains(stderrStr, "Tier 2: indexed") {
		t.Errorf("expected 'Tier 2: indexed' in stderr (proves SQLite code ran), got:\n%s", stderrStr)
	}
}
