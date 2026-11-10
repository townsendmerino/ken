package main

import (
	"github.com/townsendmerino/ken/internal/envcfg"
	"github.com/townsendmerino/ken/internal/search"
	"github.com/townsendmerino/ken/internal/status"
)

// env.go is where the ken CLI turns the indexing environment variables into
// options. The library packages (internal/search, internal/repo,
// internal/structural, internal/status) don't read the environment; the
// CLI reads it here, through internal/envcfg, and passes the values down.
//
// Every index-building subcommand uses the same options, so KEN_ENRICH,
// KEN_ANN and the walk limits mean the same thing on the one-shot builds,
// the watched index and build-index (ADR-054).

// envFSOptions is the index-build configuration the environment asks for:
// KEN_ENRICH, KEN_ANN and the walk limits.
func envFSOptions() search.FSOptions {
	return search.FSOptions{
		DisableEnrichment: envcfg.EnrichDisabled(),
		DenseRetriever:    envcfg.DenseRetriever(),
		Walk:              envcfg.WalkLimits(),
	}
}

// envStatusOptions carries the variables `ken status` / `ken doctor` report
// on: KEN_MODEL_DIR, KEN_RERANK_MODEL_DIR, KEN_USAGE_STATS_PATH, KEN_ENRICH.
func envStatusOptions() status.BuildOptions {
	return status.BuildOptions{
		EmbedModelDir:  envcfg.ModelDir(),
		RerankModelDir: envcfg.RerankModelDir(),
		SavingsPath:    envcfg.UsageStatsPath(),
		EnrichEnv:      envcfg.EnrichRaw(),
	}
}
