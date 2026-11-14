package mcp

import (
	"context"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/townsendmerino/ken/engine"
	"github.com/townsendmerino/ken/internal/envcfg"
	"github.com/townsendmerino/ken/internal/status"
)

// handleStatus implements the `status` MCP tool for the cmd/ken-mcp
// path (full server with a Cache and on-demand repo bundles). Always
// surfaces machine-level info plus the server's cache state. With
// args.Repo set, ALSO populates the live IndexInfo + StructuralInfo
// for that repo.
//
// Output: markdown by default, JSON if args.Output == "json".
//
// No mutation. No network. Safe to call concurrently.
func handleStatus(ctx context.Context, cfg *Config, args StatusArgs) (*sdk.CallToolResult, any, error) {
	opts := status.BuildOptions{}

	// Always include the cache state when we have one — it's a
	// server-level concern that doesn't depend on a specific repo.
	if cfg.Cache != nil {
		opts.LiveCache = &status.CacheInfo{
			Capacity: cfg.Cache.Capacity(),
			InUse:    cfg.Cache.Len(),
		}
	}

	if cfg.GoTyped != nil {
		if st := cfg.GoTyped.Status(); st.Enabled {
			opts.GoTyped = goTypedInfo(st)
		}
	}

	// If a repo is provided, resolve it via the same cache path
	// search.go uses and populate the live IndexInfo + StructuralInfo.
	if args.Repo != "" && cfg.Cache != nil {
		source, err := resolveRepo(cfg, args.Repo)
		if err == nil {
			if bundle, err := cfg.Cache.GetBundle(ctx, source); err == nil {
				ws := bundle.workspace()
				if bundle.watchedIndex() != nil {
					opts.LiveIndex = liveIndexInfo(source, ws.IndexStatus())
				}
				// Peek only — never trigger the lazy structural build just
				// to report status (cold-start M0). If no structural tool
				// has been called yet, the index isn't built and status
				// omits it, same as a repo with no structural support.
				if st, ok := ws.StructuralStatus(); ok {
					opts.LiveStructural = &status.StructuralInfo{
						TopLevelSymbols:  st.TopLevelSymbols,
						Methods:          st.Methods,
						PerLanguageFiles: st.FilesByExtension,
					}
				}
			}
		}
	}

	return renderStatusResult(status.Build(withStatusEnv(opts)), args)
}

// runStatus is the mcp.Run path's status handler. Embedded-
// corpus servers don't have a Cache (no on-demand repos) but DO have
// a single live Index via the atomic pointer. Surface that as the
// IndexInfo so users still see "what's loaded right now" without
// having to pass a repo argument.
func runStatus(ix interface{ IndexStatus() engine.IndexStatus }, args StatusArgs) (*sdk.CallToolResult, any, error) {
	opts := status.BuildOptions{}
	if ix != nil {
		opts.LiveIndex = liveIndexInfo("(embedded corpus)", ix.IndexStatus())
	}
	return renderStatusResult(status.Build(withStatusEnv(opts)), args)
}

// withStatusEnv fills the fields the status report reads from the
// environment: KEN_MODEL_DIR, KEN_RERANK_MODEL_DIR, KEN_USAGE_STATS_PATH
// and KEN_ENRICH. internal/status no longer reads them itself; this
// package is a server edge (see env.go), so it does, through
// internal/envcfg.
func withStatusEnv(opts status.BuildOptions) status.BuildOptions {
	opts.EmbedModelDir = envcfg.ModelDir()
	opts.RerankModelDir = envcfg.RerankModelDir()
	opts.SavingsPath = envcfg.UsageStatsPath()
	opts.EnrichEnv = envcfg.EnrichRaw()
	// Process-wide counts: every index this server has built so far.
	skipped, reached := engine.EnrichFileBudgetCounts()
	opts.ParseBudget = &status.ParseBudgetInfo{Skipped: skipped, Reached: reached}
	return opts
}

func renderStatusResult(s status.Status, args StatusArgs) (*sdk.CallToolResult, any, error) {
	if args.Output == "json" {
		j, err := status.RenderJSON(s)
		if err != nil {
			return textResult("status: " + err.Error()), nil, nil
		}
		return textResult(string(j)), nil, nil
	}
	return textResult(status.RenderMarkdown(s, args.Verbose)), nil, nil
}

// liveIndexInfo is the status report's view of one loaded index, read
// from the index itself (engine.IndexStatus) rather than from KEN_ENRICH /
// KEN_ANN.
func liveIndexInfo(repo string, st engine.IndexStatus) *status.IndexInfo {
	dense := st.DenseRetriever
	switch {
	case st.Mode == engine.ModeBM25:
		dense = "none"
	case dense == "":
		dense = "flat"
	}
	return &status.IndexInfo{
		Repo:           repo,
		ChunkCount:     st.ChunkCount,
		Mode:           engine.ModeNames()[int(st.Mode)],
		FileCount:      st.FileCount,
		WatchActive:    true, // ken-mcp v0.3+ always watches; embedded-corpus pointer is reloaded on rebuild
		EnrichedChunks: st.EnrichedChunks,
		DenseRetriever: dense,
	}
}

// goTypedInfo is the status report's view of what the Go typed tier holds between queries.
func goTypedInfo(st engine.GoTypedStatus) *status.GoTypedInfo {
	g := &status.GoTypedInfo{
		MaxResident: st.Pool.MaxSessions, IdleSeconds: st.Pool.IdleSeconds, HeldBytes: st.HeldBytes,
		CacheEntries: st.Cache.Entries, CacheBytes: st.Cache.Bytes, CacheMaxBytes: st.Cache.MaxBytes,
		CacheHits: st.Cache.Hits, CacheMisses: st.Cache.Misses, CacheStale: st.Cache.Stale, CacheDropped: st.Cache.Dropped,
		Starts: st.Pool.Starts, Reuses: st.Pool.Reuses,
		ProgramLoads: st.Programs.Loads, ProgramRefreshes: st.Programs.Refreshes, ProgramReuses: st.Programs.Reuses, ProgramStale: st.Programs.Stale, ProgramDropped: st.Programs.Dropped,
	}
	if g.MaxResident == 0 {
		g.MaxResident, g.IdleSeconds = st.Programs.MaxPrograms, st.Programs.IdleSeconds
	}
	for _, pr := range st.Programs.Programs {
		g.Programs = append(g.Programs, status.GoTypedProgram{Repo: pr.Repo, Config: pr.Config, Bytes: pr.Bytes, Packages: pr.Packages, IdleSec: pr.IdleSec, Queries: pr.Queries})
	}
	for _, s := range st.Pool.Sessions {
		g.Sessions = append(g.Sessions, status.GoTypedSession{Repo: s.Repo, Config: s.Config, PID: s.PID, RSSBytes: s.RSSBytes, IdleSec: s.IdleSec, Requests: s.Requests})
	}
	return g
}
