package mcp

import (
	"time"

	"github.com/townsendmerino/ken/engine"
	"github.com/townsendmerino/ken/internal/envcfg"
)

// env.go: this package is an environment edge.
//
// ken's library packages (engine and everything under internal/ except
// internal/envcfg) do not read the environment; a program's edge reads the
// KEN_* variables and passes the values down as options. For servers built
// on this package the edge is the package itself, so that a third-party
// mcp.Run or mcp.NewServer binary keeps honoring the documented variables
// without wiring each one. What mcp reads:
//
//   - clone.go: KEN_ALLOW_PRIVATE_CLONE_TARGETS, KEN_MAX_CLONE_BYTES
//   - confine.go: KEN_MCP_ALLOWED_REPO_ROOTS
//   - status_tool.go: KEN_MODEL_DIR, KEN_RERANK_MODEL_DIR,
//     KEN_USAGE_STATS_PATH, KEN_ENRICH (reported by the status tool)
//   - run.go (mcp.Run only): KEN_ENRICH, KEN_ANN, KEN_MAX_FILE_BYTES,
//     KEN_MAX_AVG_LINE_BYTES, KEN_MAX_FILES, KEN_SKIP_GENERATED,
//     KEN_ENRICH_FILE_BUDGET_MS
//
// internal/buildchecks' TestEnvReadsStayAtTheEdge enforces the rule and
// names this package as one of the allowed readers.

// applyParseBudgetEnv hands KEN_ENRICH_FILE_BUDGET_MS to the structural
// parser for an mcp.Run server. When the variable is unset it leaves the
// budget alone (0 unless the program set one itself).
func applyParseBudgetEnv() {
	if micros, set := envcfg.EnrichFileBudgetMicros(); set {
		engine.SetEnrichFileBudget(time.Duration(micros) * time.Microsecond)
	}
}
