package main

import (
	"os"
	"runtime/debug"

	"github.com/townsendmerino/ken/internal/bytesize"
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
		n, ok := bytesize.Parse(raw)
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
