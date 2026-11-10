package engine

import (
	"time"

	"github.com/townsendmerino/ken/internal/structural"
)

// SetEnrichFileBudget sets the per-file wall-clock budget for the
// tree-sitter parse behind Arm B enrichment and the structural index. A
// file whose parse runs past it is indexed without its structural label
// (and left out of the structural index); search results for it are
// otherwise unaffected. 0, the default, means no budget, which keeps
// builds deterministic across runs.
//
// The budget is process-wide, not per Workspace: every Workspace shares
// one parser pool per language, built on first use with the budget in
// force at that moment. Call this once, before the first Open. It is what
// the ken and ken-mcp binaries do with KEN_ENRICH_FILE_BUDGET_MS (2000 and
// 500 ms by default); engine itself does not read that variable.
func SetEnrichFileBudget(d time.Duration) { structural.SetParseBudget(d) }
