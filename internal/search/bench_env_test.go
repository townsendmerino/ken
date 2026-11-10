//go:build bench

package search

import (
	"os"
	"time"

	"github.com/townsendmerino/ken/internal/envcfg"
	"github.com/townsendmerino/ken/internal/structural"
)

// fromPathEnv is FromPath configured from the environment, for the
// bench-tagged harnesses in this package. docs/BENCH.md runs them with
// KEN_ENRICH=off (and, optionally, KEN_ANN, the walk limits and
// KEN_ENRICH_FILE_BUDGET_MS); the package itself no longer reads those
// variables, so the harness, which is the program's edge here, does.
func fromPathEnv(root string, mode Mode, chunkerName, modelDir string) (*Index, error) {
	if micros, set := envcfg.EnrichFileBudgetMicros(); set {
		structural.SetParseBudget(time.Duration(micros) * time.Microsecond)
	}
	return FromFSWithOptions(os.DirFS(root), mode, chunkerName, modelDir, FSOptions{
		DisableEnrichment: envcfg.EnrichDisabled(),
		DenseRetriever:    envcfg.DenseRetriever(),
		Walk:              envcfg.WalkLimits(),
	})
}
