package main

import (
	"context"
	"errors"
	"io/fs"
	"os"

	"github.com/townsendmerino/ken/engine"
	"github.com/townsendmerino/ken/internal/envcfg"
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

// envEngineOptions is envFSOptions as engine.Options: KEN_ENRICH, KEN_ANN
// and the walk limits. engine does not read the environment, so the CLI
// passes them.
func envEngineOptions() engine.Options {
	walk := envcfg.WalkLimits()
	return engine.Options{
		DisableEnrichment: envcfg.EnrichDisabled(),
		DenseRetriever:    envcfg.DenseRetriever(),
		MaxFileBytes:      walk.MaxFileBytes,
		MaxAvgLineBytes:   walk.MaxAvgLineBytes,
		MaxFiles:          walk.MaxFiles,
		SkipGenerated:     walk.SkipGenerated,
	}
}

// openWorkspace opens root through engine for a CLI subcommand, with the
// environment's options. Errors come back in the words the CLI has always
// printed: a missing or non-directory root as the filesystem's own stat
// error, and a failed build as its cause (unknown chunker, model not
// found, too many files) rather than engine's wrapper.
func openWorkspace(root, mode, chunker, modelDir string, watch bool) (*engine.Workspace, error) {
	if _, err := fs.Stat(os.DirFS(root), "."); err != nil {
		return nil, err
	}
	opts := envEngineOptions()
	opts.Mode, opts.Chunker, opts.ModelDir, opts.Watch = mode, chunker, modelDir, watch
	ws, err := engine.Open(context.Background(), root, opts)
	var be *engine.BuildError
	if errors.As(err, &be) {
		return nil, be.Err
	}
	return ws, err
}
