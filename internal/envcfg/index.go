package envcfg

import (
	"os"
	"strconv"
	"strings"

	"github.com/townsendmerino/ken/internal/bytesize"
	"github.com/townsendmerino/ken/internal/repo"
)

// index.go holds the readers for the KEN_* variables that tune indexing
// itself rather than the ken-mcp server. The library packages they
// configure (internal/repo, internal/search, internal/structural,
// internal/status, and the public engine package) do not read the
// environment: a program's edge (cmd/ken, cmd/ken-mcp, and the mcp server
// package) calls these and passes the values down as options.
//
// Unlike the KEN_MCP_* readers in envcfg.go, these are silent: an invalid
// value falls back to the default without a warning. That is how the
// library packages parsed them before the reads moved here, and both
// binaries must keep behaving exactly as they did.

// WalkLimits reads the walker's limits into the repo.Options fields that
// carry them (Root is left empty):
//
//   - KEN_MAX_FILE_BYTES: a byte count or KiB/MiB/GiB-suffixed size;
//     invalid or non-positive values are ignored.
//   - KEN_MAX_AVG_LINE_BYTES: a plain count; 0 switches the minified-file
//     heuristic off (Options.MaxAvgLineBytes = -1).
//   - KEN_MAX_FILES: a plain count; 0 means unlimited (Options.MaxFiles =
//     -1).
//   - KEN_SKIP_GENERATED: 1/true/yes/on (case-insensitive) turns the
//     generated-code skip on.
//
// A zero field means "unset": the walker applies its default.
func WalkLimits() repo.Options {
	var o repo.Options
	if raw := os.Getenv("KEN_MAX_FILE_BYTES"); raw != "" {
		if n, ok := bytesize.Parse(raw); ok && n > 0 {
			o.MaxFileBytes = n
		}
	}
	o.MaxAvgLineBytes = countOrOff("KEN_MAX_AVG_LINE_BYTES")
	o.MaxFiles = countOrOff("KEN_MAX_FILES")
	switch strings.ToLower(strings.TrimSpace(os.Getenv("KEN_SKIP_GENERATED"))) {
	case "1", "true", "yes", "on":
		o.SkipGenerated = true
	}
	return o
}

// countOrOff parses a non-negative count variable whose 0 means "off" into
// the option encoding where 0 means "default": unset or invalid → 0, "0" →
// -1, n → n.
func countOrOff(name string) int {
	raw := os.Getenv(name)
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 0 {
		return 0
	}
	if n == 0 {
		return -1
	}
	return n
}

// EnrichRaw returns KEN_ENRICH exactly as set, for status reporting.
func EnrichRaw() string { return os.Getenv("KEN_ENRICH") }

// EnrichDisabled reports whether KEN_ENRICH switches Arm B enrichment off:
// any of 0/off/false/no (case-insensitive). Unset and every other value
// leave it on.
func EnrichDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("KEN_ENRICH"))) {
	case "0", "off", "false", "no":
		return true
	}
	return false
}

// DenseRetriever reads KEN_ANN: "flat-i8" or "flat-binary-i8"
// (case-insensitive), else "" (the default exact retriever). An
// unrecognized value falls back to "" without a warning.
func DenseRetriever() string {
	switch v := strings.ToLower(strings.TrimSpace(os.Getenv("KEN_ANN"))); v {
	case "flat-i8", "flat-binary-i8":
		return v
	}
	return ""
}

// StructuralMaxFileBytes reads KEN_STRUCTURAL_MAX_FILE_BYTES (a byte count
// or suffixed size). It returns 0, meaning "use the default", when the
// variable is unset, invalid or non-positive.
func StructuralMaxFileBytes() int64 {
	if raw := os.Getenv("KEN_STRUCTURAL_MAX_FILE_BYTES"); raw != "" {
		if n, ok := bytesize.Parse(raw); ok && n > 0 {
			return n
		}
	}
	return 0
}

// EnrichFileBudgetMicros reads KEN_ENRICH_FILE_BUDGET_MS, the per-file
// parse budget in whole milliseconds, as microseconds. set is false when
// the variable is unset (empty), which is when a binary applies its own
// default. A set but invalid value is 0, meaning no budget.
func EnrichFileBudgetMicros() (micros uint64, set bool) {
	raw := os.Getenv("KEN_ENRICH_FILE_BUDGET_MS")
	if raw == "" {
		return 0, false
	}
	ms, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0, true
	}
	return ms * 1000, true
}

// EnrichFileBudgetRaw returns KEN_ENRICH_FILE_BUDGET_MS exactly as set,
// for log lines.
func EnrichFileBudgetRaw() string { return os.Getenv("KEN_ENRICH_FILE_BUDGET_MS") }

// ModelDir returns KEN_MODEL_DIR exactly as set ("" when unset).
func ModelDir() string { return os.Getenv("KEN_MODEL_DIR") }

// RerankModelDir returns KEN_RERANK_MODEL_DIR exactly as set.
func RerankModelDir() string { return os.Getenv("KEN_RERANK_MODEL_DIR") }

// UsageStatsPath returns KEN_USAGE_STATS_PATH exactly as set.
func UsageStatsPath() string { return os.Getenv("KEN_USAGE_STATS_PATH") }
