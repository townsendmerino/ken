package envcfg_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/townsendmerino/ken/internal/envcfg"
	kenmcp "github.com/townsendmerino/ken/mcp"
)

// newCapturedLogger returns a kenmcp.Logger that writes to a buffer
// (level=LogDebug so every call is captured). Used to assert that bad input
// produces the documented warn message.
func newCapturedLogger() (*kenmcp.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return kenmcp.NewLogger(buf, kenmcp.LogDebug), buf
}

// withEnv temporarily sets env vars for the duration of the test. Each test
// owns its env-var slots (unique KEN_TEST_* names) so parallel tests don't
// collide.
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
			got := envcfg.EnvInt(key, c.fallback, l)
			if got != c.want {
				t.Errorf("EnvInt(%q, fallback=%d) = %d, want %d", c.envVal, c.fallback, got, c.want)
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
			got := envcfg.EnvEnum(key, allowed, "hybrid", l)
			if got != c.want {
				t.Errorf("EnvEnum(%q) = %q, want %q", c.envVal, got, c.want)
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
		if got := envcfg.EnvPath(key, l); got != "" {
			t.Errorf("EnvPath(unset) = %q, want \"\"", got)
		}
		if buf.Len() != 0 {
			t.Errorf("unexpected log output: %q", buf.String())
		}
	})

	t.Run("valid directory returns value no warn", func(t *testing.T) {
		withEnv(t, map[string]string{key: dir})
		l, buf := newCapturedLogger()
		if got := envcfg.EnvPath(key, l); got != dir {
			t.Errorf("EnvPath(dir) = %q, want %q", got, dir)
		}
		if buf.Len() != 0 {
			t.Errorf("unexpected log output: %q", buf.String())
		}
	})

	t.Run("file path warns but keeps value", func(t *testing.T) {
		withEnv(t, map[string]string{key: file})
		l, buf := newCapturedLogger()
		if got := envcfg.EnvPath(key, l); got != file {
			t.Errorf("EnvPath(file) = %q, want %q (value must be preserved)", got, file)
		}
		if !strings.Contains(buf.String(), "not a directory") {
			t.Errorf("expected 'not a directory' warn, got: %q", buf.String())
		}
	})

	t.Run("nonexistent warns but keeps value", func(t *testing.T) {
		bogus := filepath.Join(dir, "does-not-exist")
		withEnv(t, map[string]string{key: bogus})
		l, buf := newCapturedLogger()
		if got := envcfg.EnvPath(key, l); got != bogus {
			t.Errorf("EnvPath(bogus) = %q, want %q (value must be preserved)", got, bogus)
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
			got := envcfg.EnvDuration(key, c.fallback, l)
			if got != c.want {
				t.Errorf("EnvDuration(%q, %s) = %s, want %s", c.envVal, c.fallback, got, c.want)
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
			got := envcfg.EnvBool(key, c.fallback, l)
			if got != c.want {
				t.Errorf("EnvBool(%q, fallback=%v) = %v, want %v", c.envVal, c.fallback, got, c.want)
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
		{"valid sqlite:// absolute path", "sqlite:///var/data/dev.db", "sqlite:///var/data/dev.db", false},
		{"valid sqlite3:// absolute path", "sqlite3:///var/data/dev.db", "sqlite3:///var/data/dev.db", false},
		{"valid sqlite:// relative path", "sqlite://./dev.db", "sqlite://./dev.db", false},
		{"case-insensitive sqlite scheme", "SQLITE:///var/data/dev.db", "SQLITE:///var/data/dev.db", false},
		{"valid mysql:// URL", "mysql://alice:s3cret@db.local:3306/mydb", "mysql://alice:s3cret@db.local:3306/mydb", false},
		{"mysql:// with non-default port", "mysql://alice:s3cret@db.local:33306/mydb?parseTime=true", "mysql://alice:s3cret@db.local:33306/mydb?parseTime=true", false},
		{"native MySQL tcp form", "alice:s3cret@tcp(db.local:3306)/mydb?parseTime=true", "alice:s3cret@tcp(db.local:3306)/mydb?parseTime=true", false},
		{"native MySQL unix-socket form", "alice:s3cret@unix(/var/run/mysqld/mysqld.sock)/mydb", "alice:s3cret@unix(/var/run/mysqld/mysqld.sock)/mydb", false},
		{"missing host on mysql falls back + warns", "mysql:///d", "", true},
		{"typoed sqlite falls back + warns", "sqliet:///dev.db", "", true},
		{"http scheme falls back + warns", "http://h/d", "", true},
		{"libpq key=value form falls back + warns", "host=localhost port=5432 dbname=mydb", "", true},
		{"missing host on postgres falls back + warns", "postgres:///d", "", true},
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
			got := envcfg.EnvDSN(key, l)
			if got != c.want {
				t.Errorf("EnvDSN(%q) = %q, want %q", c.envVal, got, c.want)
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
			got := envcfg.EnvPathOrURL(key, l)
			if got != c.want {
				t.Errorf("EnvPathOrURL(%q) = %q, want %q", c.envVal, got, c.want)
			}
			hasWarn := strings.Contains(buf.String(), key+"=")
			if hasWarn != c.wantWarn {
				t.Errorf("warn captured = %v, want %v\nlog output: %q", hasWarn, c.wantWarn, buf.String())
			}
		})
	}
}

// TestEnvCommaList covers KEN_DB_SCHEMAS / KEN_DB_EXCLUDE_SCHEMAS parsing:
// whitespace trimming, empty-element filtering, the "all whitespace / nothing
// left" → nil rule.
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
			got := envcfg.EnvCommaList(key)
			if !equalStringSlices(got, c.want) {
				t.Errorf("EnvCommaList(%q) = %v, want %v", c.envVal, got, c.want)
			}
		})
	}
}

// equalStringSlices treats nil and empty slice as equal (both "no entries").
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
