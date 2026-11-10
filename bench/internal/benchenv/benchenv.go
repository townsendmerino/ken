//go:build bench

// Package benchenv is the bench harnesses' environment edge. ken's library
// packages don't read the environment; the harnesses still take their
// configuration from the KEN_* variables (docs/BENCH.md's commands set
// KEN_ENRICH, KEN_ANN, KEN_ENRICH_FILE_BUDGET_MS and the walk limits, and
// bench/internal/provenance records them as part of a run's config), so
// each harness reads them here and passes the values down.
package benchenv

import (
	"os"
	"time"

	"github.com/townsendmerino/ken/engine"
	"github.com/townsendmerino/ken/internal/envcfg"
	"github.com/townsendmerino/ken/internal/search"
	"github.com/townsendmerino/ken/internal/structural"
)

// FSOptions is the index-build configuration the environment asks for:
// KEN_ENRICH, KEN_ANN and the walk limits.
func FSOptions() search.FSOptions {
	return search.FSOptions{
		DisableEnrichment: envcfg.EnrichDisabled(),
		DenseRetriever:    envcfg.DenseRetriever(),
		Walk:              envcfg.WalkLimits(),
	}
}

// FromPath is search.FromPath with FSOptions applied, and with
// KEN_ENRICH_FILE_BUDGET_MS handed to the parser first when it is set.
func FromPath(root string, mode search.Mode, chunker, modelDir string) (*search.Index, error) {
	ApplyParseBudget(0)
	return search.FromFSWithOptions(os.DirFS(root), mode, chunker, modelDir, FSOptions())
}

// ApplyParseBudget sets the structural parse budget from
// KEN_ENRICH_FILE_BUDGET_MS, or to defaultMS milliseconds when the variable
// is unset and defaultMS > 0. With the variable unset and defaultMS 0 it
// leaves the budget as it is.
func ApplyParseBudget(defaultMS uint64) {
	micros, set := envcfg.EnrichFileBudgetMicros()
	if !set {
		if defaultMS == 0 {
			return
		}
		micros = defaultMS * 1000
	}
	structural.SetParseBudget(time.Duration(micros) * time.Microsecond)
}

// EngineOptions fills the engine.Options fields the environment used to
// reach engine.Open implicitly: the walk limits and
// KEN_STRUCTURAL_MAX_FILE_BYTES. Call ApplyParseBudget for the parse budget.
func EngineOptions(o engine.Options) engine.Options {
	walk := envcfg.WalkLimits()
	o.MaxFileBytes = walk.MaxFileBytes
	o.MaxAvgLineBytes = walk.MaxAvgLineBytes
	o.MaxFiles = walk.MaxFiles
	o.SkipGenerated = walk.SkipGenerated
	o.StructuralMaxFileBytes = envcfg.StructuralMaxFileBytes()
	return o
}
