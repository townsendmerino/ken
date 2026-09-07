package main

import (
	"fmt"
	"os"
	"time"

	"github.com/townsendmerino/ken/internal/structural"
)

// defaultCLIEnrichBudgetMS is the per-file structural-enrichment parse
// budget `ken index` / `ken search` / `ken bench` / `ken perf` apply when
// the operator hasn't set KEN_ENRICH_FILE_BUDGET_MS. 4x ken-mcp's 500ms
// live-server default (defaultServerEnrichBudgetMS in
// cmd/ken-mcp/enrich_budget.go) — these commands tolerate more per-file
// latency than an interactive server, but still shouldn't be unbounded.
//
// Motivated by a real-corpus survey (see
// docs/internal/gotreesitter-c-large-init-array-slowdown.md): 504 of 37,775
// real .c/.h files in a sparse Linux kernel checkout (arch/x86, drivers, fs,
// kernel, mm, net, sound @ v6.6) exceeded a 3s budget — large designated-
// initializer arrays common in driver code (clock tables, GPU register
// tables, device-ID tables) trigger a gotreesitter `c`-grammar slowdown
// (filed upstream; not the previously-fixed GLR stack overflow — every case
// completes with ParseStopAccepted, just slowly). Deliberately NOT applied
// to `ken build-index` (see cmdBuildIndex) — that command's whole purpose is
// byte-identical, machine-load-independent output (ADR-040), and a
// wall-clock budget would reintroduce exactly the non-determinism that ADR
// closed. It's applied here because these commands make no such promise.
const defaultCLIEnrichBudgetMS = "2000"

// setupEnrichBudget defaults KEN_ENRICH_FILE_BUDGET_MS when unset and routes
// budget-exhaustion skips to stderr, so a pathological file degrades to "one
// file's structural enrichment silently skipped" instead of looking like a
// hang (the exact user-facing symptom the C# grammar fix, v1.5.1, was
// titled after). Idempotent; safe to call once per process before the first
// index build. Mirrors cmd/ken-mcp/enrich_budget.go's setupEnrichBudget —
// same mechanism, CLI-appropriate default and a plain stderr line instead of
// a leveled logger (this binary has none).
func setupEnrichBudget() {
	if os.Getenv("KEN_ENRICH_FILE_BUDGET_MS") == "" {
		_ = os.Setenv("KEN_ENRICH_FILE_BUDGET_MS", defaultCLIEnrichBudgetMS)
	}
	structural.SetParseBudgetLogf(func(path string, d time.Duration) {
		fmt.Fprintf(os.Stderr, "ken: enrichment parse budget exceeded for %s (%v) — skipping structural enrichment for this file (results are unaffected, just unenriched); set KEN_ENRICH_FILE_BUDGET_MS to change, or KEN_ENRICH=off to disable enrichment entirely\n", path, d)
	})
}
