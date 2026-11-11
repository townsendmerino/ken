package main

import (
	"time"

	"github.com/townsendmerino/ken/engine"
	"github.com/townsendmerino/ken/internal/envcfg"
	kenmcp "github.com/townsendmerino/ken/mcp"
)

// defaultServerEnrichBudgetMS is the per-file tree-sitter parse budget ken-mcp
// applies when the operator hasn't set KEN_ENRICH_FILE_BUDGET_MS. The library
// default is 0 (disabled) to preserve the cross-run determinism contract on the
// build/golden path (a timing-dependent skip would flake it — see
// internal/structural/index.go). The live server has no such contract and DOES
// want cliff-resilience: gotreesitter's own ledger records template-like files
// (e.g. bootstrap-5.blade.php) individually ~159× the median, which the 64 KiB
// size cap won't catch. 500 ms is ~100× the p99 healthy file parse (~13 ms on
// the ken tree), so only genuinely pathological files trip it.
const defaultServerEnrichBudgetMS = 500

// setupEnrichBudget applies the server-side per-file parse budget: read the
// env knob (defaulting it when unset), hand it to engine, and route budget-exhaustion skips to the leveled stderr
// logger (never stdout — the JSON-RPC channel). Server-only; the library / CLI
// layers stay at the disabled default. Must run before the first index build so
// the lazily-created gotreesitter pools observe the budget.
func setupEnrichBudget(logger *kenmcp.Logger) {
	micros, set := envcfg.EnrichFileBudgetMicros()
	if !set {
		micros = defaultServerEnrichBudgetMS * 1000
		logger.Logf(kenmcp.LogDebug,
			"enrichment: KEN_ENRICH_FILE_BUDGET_MS unset — applying ken-mcp server default %d ms (set it to override; 0 disables)",
			defaultServerEnrichBudgetMS)
	} else {
		logger.Logf(kenmcp.LogDebug, "enrichment: honoring KEN_ENRICH_FILE_BUDGET_MS=%s from env", envcfg.EnrichFileBudgetRaw())
	}
	engine.SetEnrichFileBudget(time.Duration(micros) * time.Microsecond)

	engine.SetEnrichFileBudgetLog(func(path string, d time.Duration) {
		logger.Logf(kenmcp.LogWarn,
			"enrichment parse budget exceeded for %s (%v) — skipping enrichment for this file (pathological/template-like input; results are unaffected, just unenriched)",
			path, d)
	}, func(path string, d time.Duration) {
		logger.Logf(kenmcp.LogWarn,
			"enrichment parse budget reached for %s (%v) — the file is enriched from a parse that was cut short, so its label can differ between runs (raise KEN_ENRICH_FILE_BUDGET_MS, or 0 to disable the budget)",
			path, d)
	})
}
