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
// All warnings go to stderr via the leveled logger (lvlWarn), preserving
// the stdout/stderr contract from main.go: stdout is JSON-RPC only.

import (
	"os"
	"slices"
	"strconv"
	"strings"
)

// envInt parses an integer env var. Empty/unset returns fallback;
// invalid input warns and returns fallback. Negative values are
// passed through unchanged — the caller decides whether to reject (e.g.
// CACHE_SIZE rejects negatives but allows 0 for "no caching").
func envInt(name string, fallback int, l *leveledLogger) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		l.logf(lvlWarn, "invalid %s=%q: %v — using default %d", name, raw, err, fallback)
		return fallback
	}
	return n
}

// envEnum returns the env var value if it exactly matches one of allowed
// (case-sensitive); empty/unset returns fallback; any mismatch warns and
// returns fallback. Case-sensitivity is deliberate: agents and config
// files pass these verbatim, and "Hybrid" vs "hybrid" should be a loud
// "fix your config" rather than a silent acceptance.
func envEnum(name string, allowed []string, fallback string, l *leveledLogger) string {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	if slices.Contains(allowed, raw) {
		return raw
	}
	l.logf(lvlWarn, "invalid %s=%q: not in %v — using default %q", name, raw, allowed, fallback)
	return fallback
}

// envPath returns the env var unchanged but warns if it is set and not
// a readable directory. The downstream caller still gets the value so
// any existing auto-downgrade logic (e.g. KEN_MCP_MODEL_DIR missing ⇒
// downgrade to bm25) runs as before; the warn is just the early signal
// that the path is wrong.
func envPath(name string, l *leveledLogger) string {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return raw
	}
	st, err := os.Stat(raw)
	if err != nil {
		l.logf(lvlWarn, "%s=%q: %v — value kept; downstream behavior may downgrade", name, raw, err)
		return raw
	}
	if !st.IsDir() {
		l.logf(lvlWarn, "%s=%q: not a directory — value kept; downstream behavior may downgrade", name, raw)
	}
	return raw
}

// envPathOrURL is envPath plus an http(s) URL escape hatch. KEN_MCP_DEFAULT_REPO
// is allowed to name either a local directory or a remote URL (cloned
// on first request); we accept either, warn on neither.
func envPathOrURL(name string, l *leveledLogger) string {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return raw
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return raw
	}
	st, err := os.Stat(raw)
	if err != nil || !st.IsDir() {
		l.logf(lvlWarn, "%s=%q: not a directory or http(s) URL — value kept; per-request lookups may fail", name, raw)
	}
	return raw
}
