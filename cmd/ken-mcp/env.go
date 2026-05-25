package main

// Env-var validation helpers for ken-mcp.
//
// Why this file exists: every KEN_MCP_* env var used to be parsed with
// a fire-and-forget pattern (`strconv.Atoi(envOr(...))` discarding the
// error; `parseLevel` falling through to "warn" on unknown input). The
// failure mode was silent: an operator typo like `KEN_MCP_CACHE_SIZE=of`
// produced size=0, the cache was effectively disabled, and the only
// symptom was "why is ken-mcp re-indexing every query?" These helpers
// validate up-front, log a stderr warning on bad input, and fall back to
// the documented default — so the operator gets the signal at startup.
//
// All warnings go to stderr via the leveled logger (LogWarn), preserving
// the stdout/stderr contract from main.go: stdout is JSON-RPC only.

import (
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	kenmcp "github.com/townsendmerino/ken/mcp"
)

// envInt parses an integer env var. Empty/unset returns fallback;
// invalid input warns and returns fallback. Negative values are
// passed through unchanged — the caller decides whether to reject (e.g.
// CACHE_SIZE rejects negatives but allows 0 for "no caching").
func envInt(name string, fallback int, l *kenmcp.Logger) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		l.Logf(kenmcp.LogWarn, "invalid %s=%q: %v — using default %d", name, raw, err, fallback)
		return fallback
	}
	return n
}

// envEnum returns the env var value if it exactly matches one of allowed
// (case-sensitive); empty/unset returns fallback; any mismatch warns and
// returns fallback. Thin wrapper around kenmcp.ValidateEnum so the warn
// format stays identical across env-var and Options.Mode validation.
func envEnum(name string, allowed []string, fallback string, l *kenmcp.Logger) string {
	return kenmcp.ValidateEnum(name, os.Getenv(name), allowed, fallback, l)
}

// envPath returns the env var unchanged but warns if it is set and not
// a readable directory. The downstream caller still gets the value so
// any existing auto-downgrade logic (e.g. KEN_MCP_MODEL_DIR missing ⇒
// downgrade to bm25) runs as before; the warn is just the early signal
// that the path is wrong.
func envPath(name string, l *kenmcp.Logger) string {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return raw
	}
	st, err := os.Stat(raw)
	if err != nil {
		l.Logf(kenmcp.LogWarn, "%s=%q: %v — value kept; downstream behavior may downgrade", name, raw, err)
		return raw
	}
	if !st.IsDir() {
		l.Logf(kenmcp.LogWarn, "%s=%q: not a directory — value kept; downstream behavior may downgrade", name, raw)
	}
	return raw
}

// envPathOrURL is envPath plus an http(s) URL escape hatch. KEN_MCP_DEFAULT_REPO
// is allowed to name either a local directory or a remote URL (cloned
// on first request); we accept either, warn on neither.
func envPathOrURL(name string, l *kenmcp.Logger) string {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return raw
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return raw
	}
	st, err := os.Stat(raw)
	if err != nil || !st.IsDir() {
		l.Logf(kenmcp.LogWarn, "%s=%q: not a directory or http(s) URL — value kept; per-request lookups may fail", name, raw)
	}
	return raw
}

// envBool parses a boolean env var. Accepted truthy values (case-
// insensitive): "1", "true", "yes", "y", "on". Accepted falsy values:
// "0", "false", "no", "n", "off". Empty/unset returns fallback; any
// other value warns and returns fallback. Matches the warn-and-fallback
// pattern the rest of this file uses.
//
// v0.7.1: introduced for KEN_SQL_NO_AUTO_MIGRATIONS.
func envBool(name string, fallback bool, l *kenmcp.Logger) bool {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	switch strings.ToLower(raw) {
	case "1", "true", "yes", "y", "on":
		return true
	case "0", "false", "no", "n", "off":
		return false
	}
	l.Logf(kenmcp.LogWarn, "invalid %s=%q: expected boolean (1/true/yes/on or 0/false/no/off) — using default %v",
		name, raw, fallback)
	return fallback
}

// envDuration parses a Go time.Duration env var (e.g. "5m", "1h30m").
// Empty/unset returns fallback; invalid input warns and returns
// fallback. Used by v0.7.0's KEN_DB_REINDEX_INTERVAL (fallback: 0 =
// disabled, no periodic reindex).
func envDuration(name string, fallback time.Duration, l *kenmcp.Logger) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		l.Logf(kenmcp.LogWarn, "invalid %s=%q: %v — using default %s", name, raw, err, fallback)
		return fallback
	}
	if d < 0 {
		l.Logf(kenmcp.LogWarn, "invalid %s=%q: must be non-negative — using default %s", name, raw, fallback)
		return fallback
	}
	return d
}

// dsnAcceptedSchemes is the allow-list for KEN_DB_DSN at v0.7.2: the
// Postgres URL forms from v0.7.0, the SQLite forms added in v0.7.1,
// and the MySQL URL form added in v0.7.2. The native go-sql-driver
// MySQL DSN (user:pass@tcp(host:port)/db) is also accepted via a
// separate substring detection — see envDSN.
var dsnAcceptedSchemes = map[string]bool{
	"postgres":   true,
	"postgresql": true,
	"sqlite":     true,
	"sqlite3":    true,
	"mysql":      true,
}

// envDSN parses a database DSN env var. Accepts the URL form for any
// engine ken supports as of v0.7.2: postgres://, postgresql://,
// sqlite://, sqlite3://, mysql://. Also accepts the native go-sql-driver
// MySQL form (user:pass@tcp(host:port)/db or @unix(/sock)/db) detected
// by the @tcp( / @unix( substring on a string with no "://" prefix.
//
// Empty/unset returns ""; invalid input warns and returns "" — Tier 2
// stays off rather than crashing the server. The libpq key=value form
// is NOT accepted at this layer (pgx supports it, but we want a loud
// rejection at startup rather than a silent connection-time failure
// later). SQLite URLs do NOT require a host (`sqlite:///abs/path.db`
// and `sqlite://./rel/path.db` are both valid).
func envDSN(name string, l *kenmcp.Logger) string {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return raw
	}
	// Native MySQL form: no scheme prefix, but @tcp( or @unix( substring.
	// Pass through if those markers are present; mysql.ParseDSN does the
	// real validation downstream.
	if !strings.Contains(raw, "://") {
		if strings.Contains(raw, "@tcp(") || strings.Contains(raw, "@unix(") {
			return raw
		}
		l.Logf(kenmcp.LogWarn,
			"invalid %s: no scheme and not a native MySQL DSN (want postgres://, postgresql://, sqlite://, sqlite3://, mysql://, or user:pass@tcp(host:port)/db) — Tier 2 (DB indexing) disabled",
			name)
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		l.Logf(kenmcp.LogWarn, "invalid %s: %v — Tier 2 (DB indexing) disabled", name, err)
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	if !dsnAcceptedSchemes[scheme] {
		l.Logf(kenmcp.LogWarn, "invalid %s scheme %q (want postgres://, postgresql://, sqlite://, sqlite3://, or mysql://) — Tier 2 (DB indexing) disabled", name, u.Scheme)
		return ""
	}
	// Postgres/postgresql/mysql REQUIRE a host. SQLite uses the path
	// component and may have an empty host (`sqlite:///abs/path` or
	// `sqlite://./rel/path` — relative paths put the leading "." in Host).
	if (scheme == "postgres" || scheme == "postgresql" || scheme == "mysql") && u.Host == "" {
		l.Logf(kenmcp.LogWarn, "invalid %s: missing host — Tier 2 (DB indexing) disabled", name)
		return ""
	}
	// Don't log the raw DSN itself — it usually contains a password.
	return raw
}

// envCommaList parses a comma-separated list env var. Empty/unset
// returns nil. Whitespace around each element is trimmed; empty
// elements (from "a,,b" or trailing commas) are dropped silently so
// operators can paste lists copy-and-paste-style without worrying
// about extra whitespace.
//
// Used by KEN_DB_SCHEMAS and KEN_DB_EXCLUDE_SCHEMAS (v0.7.2 / ADR-019).
// No warn path: a comma-separated list with weird whitespace is well-
// formed by construction; non-existent schema names are NOT errors
// per ADR-019 (operators may pre-configure for schemas that don't yet
// exist).
func envCommaList(name string) []string {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
