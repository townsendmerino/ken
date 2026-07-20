package main

import (
	"os"
	"runtime/debug"
	"strconv"
	"strings"

	kenmcp "github.com/townsendmerino/ken/mcp"
)

// GC hygiene for the long-lived ken-mcp server (M2 of the memory campaign).
//
// ken-mcp idles between queries but holds a full in-RAM index, so under the
// runtime's default GOGC=100 its steady-state RSS sits at ~2× the live heap
// and it rarely returns freed pages to the OS. These knobs trim that. They
// live in the BINARY, not internal/search or aikit: aggressive GC is the
// right policy for a background server and the wrong one for a batch CLI or
// an SDK embedder, so the library layer stays untuned (debug.FreeOSMemory
// is likewise called only from cmd/ken-mcp's flush/build hooks).

// defaultServerGOGC is the GOGC ken-mcp applies when the operator hasn't set
// GOGC. 50 halves the heap-growth headroom vs the runtime default of 100 —
// more frequent GC, lower steady-state heap/RSS. A deliberate server trade.
const defaultServerGOGC = 50

// setupGCHygiene applies ken-mcp's server-tuned GC policy. Both settings
// defer to explicit operator config: an existing GOGC env var is honored
// (SetGCPercent is only called when GOGC is unset), and KEN_MEMLIMIT is a
// convenience alias for debug.SetMemoryLimit (a soft heap limit) that, when
// set, takes precedence over GOMEMLIMIT. GOMEMLIMIT alone is honored
// natively by the runtime — nothing to do here.
func setupGCHygiene(l *kenmcp.Logger) {
	if os.Getenv("GOGC") == "" {
		debug.SetGCPercent(defaultServerGOGC)
		l.Logf(kenmcp.LogDebug, "GC: GOGC unset — applying ken-mcp server default GOGC=%d (set GOGC to override)", defaultServerGOGC)
	} else {
		l.Logf(kenmcp.LogDebug, "GC: honoring GOGC=%s from env", os.Getenv("GOGC"))
	}

	if raw := os.Getenv("KEN_MEMLIMIT"); raw != "" {
		n, ok := parseByteSize(raw)
		if !ok || n <= 0 {
			l.Logf(kenmcp.LogWarn, "KEN_MEMLIMIT=%q: not a positive byte size (e.g. 1GiB, 512MiB, or a plain byte count) — ignoring", raw)
			return
		}
		debug.SetMemoryLimit(n)
		note := ""
		if os.Getenv("GOMEMLIMIT") != "" {
			note = " (overrides GOMEMLIMIT)"
		}
		l.Logf(kenmcp.LogInfo, "GC: soft memory limit set to %d bytes via KEN_MEMLIMIT=%q%s", n, raw, note)
	}
}

// parseByteSize parses a byte count with an optional binary (KiB/MiB/GiB) or
// decimal (KB/MB/GB) suffix, or a bare integer of bytes. Case-insensitive;
// whitespace around the number and unit is tolerated. Mirrors the suffixes
// the Go runtime accepts for GOMEMLIMIT so KEN_MEMLIMIT reads the same way.
func parseByteSize(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	numPart := s[:i]
	unit := strings.ToLower(strings.TrimSpace(s[i:]))
	if numPart == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(numPart, 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	var mult int64
	switch unit {
	case "", "b":
		mult = 1
	case "kib":
		mult = 1 << 10
	case "mib":
		mult = 1 << 20
	case "gib":
		mult = 1 << 30
	case "kb":
		mult = 1_000
	case "mb":
		mult = 1_000_000
	case "gb":
		mult = 1_000_000_000
	default:
		return 0, false
	}
	// Guard against int64 overflow on absurd inputs.
	if mult != 0 && n > (1<<62)/mult {
		return 0, false
	}
	return n * mult, true
}
