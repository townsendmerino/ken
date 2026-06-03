package mcp

import (
	"io"
	"log"
	"os"
	"slices"
	"strings"
)

// Stdout/stderr contract reminder (see cmd/ken-mcp/main.go's package
// docstring for the full version): stdout IS the MCP JSON-RPC channel.
// Everything in this package that prints diagnostics MUST go through a
// Logger writing to stderr (or a caller-supplied non-stdout io.Writer).
// One stray fmt.Print to stdout corrupts the protocol and the agent
// disconnects with a cryptic JSON-decode error.

// LogLevel selects which Logger.Logf calls actually write.
type LogLevel int

const (
	LogDebug LogLevel = iota
	LogInfo
	LogWarn
	LogError
)

// ParseLogLevel maps a string ("debug"/"info"/"warn"/"error") to a
// LogLevel. Case-insensitive. Unknown strings return LogWarn (the
// default) and the caller can warn on the mismatch separately.
func ParseLogLevel(s string) LogLevel {
	switch strings.ToLower(s) {
	case "debug":
		return LogDebug
	case "info":
		return LogInfo
	case "error":
		return LogError
	default:
		return LogWarn
	}
}

// LogLevelNames returns the canonical log-level strings in ascending
// severity order. Callers that build env-var or CLI allowed-value lists
// should use this rather than hardcoding.
func LogLevelNames() []string { return []string{"debug", "info", "warn", "error"} }

// Logger is the level-aware logger used by mcp.Run and shared with
// cmd/ken-mcp. Writes to its underlying io.Writer (stderr by default);
// must never be wired to os.Stdout.
type Logger struct {
	Level LogLevel
	l     *log.Logger
}

// NewLogger constructs a Logger writing to w at the given level. If w is
// nil, defaults to os.Stderr. The "ken-mcp " prefix and standard
// timestamp+microsecond flags match what cmd/ken-mcp historically emitted
// so existing log filters keep working.
func NewLogger(w io.Writer, level LogLevel) *Logger {
	if w == nil {
		w = os.Stderr
	}
	return &Logger{
		Level: level,
		l:     log.New(w, "ken-mcp ", log.LstdFlags|log.Lmicroseconds),
	}
}

// Logf writes if at >= lg.Level. Format and args follow fmt.Printf.
func (lg *Logger) Logf(at LogLevel, format string, args ...any) {
	if at >= lg.Level {
		lg.l.Printf(format, args...)
	}
}

// ValidateEnum returns raw if it appears in allowed; otherwise warns and
// returns fallback. Empty raw also returns fallback (no warning). Used by
// both mcp.Run (validating Options fields against allowed enums) and
// cmd/ken-mcp's env.envEnum (which wraps this with an os.Getenv lookup
// and the same warn-then-fallback semantics).
//
// Case-sensitive on purpose: "Hybrid" should be a loud "fix your config"
// rather than a silent acceptance — matches ADR-009.
//
// Stability: best-effort (NOT part of the 1.0 public surface). This is
// a configuration-validation helper for ken-mcp's own env parsing.
// External consumers should not depend on it — the signature may
// shift if env-var validation evolves.
func ValidateEnum(name, raw string, allowed []string, fallback string, lg *Logger) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	if slices.Contains(allowed, raw) {
		return raw
	}
	lg.Logf(LogWarn, "invalid %s=%q: not in %v — using default %q", name, raw, allowed, fallback)
	return fallback
}
