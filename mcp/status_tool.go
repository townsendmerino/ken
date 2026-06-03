package mcp

import (
	"context"
	"path/filepath"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/townsendmerino/ken/internal/search"
	"github.com/townsendmerino/ken/internal/status"
	"github.com/townsendmerino/ken/internal/structural"
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

	// If a repo is provided, resolve it via the same cache path
	// search.go uses and populate the live IndexInfo + StructuralInfo.
	if args.Repo != "" && cfg.Cache != nil {
		source, err := resolveRepo(cfg, args.Repo)
		if err == nil {
			if bundle, err := cfg.Cache.GetBundle(ctx, source); err == nil {
				if bundle.Index != nil {
					if ix := bundle.Index.Load(); ix != nil {
						opts.LiveIndex = liveIndexInfo(source, ix)
					}
				}
				if bundle.Structural != nil {
					opts.LiveStructural = buildStructuralInfo(bundle.Structural)
				}
			}
		}
	}

	return renderStatusResult(status.Build(opts), args)
}

// handleStatusRun is the mcp.Run path's status handler. Embedded-
// corpus servers don't have a Cache (no on-demand repos) but DO have
// a single live Index via the atomic pointer. Surface that as the
// IndexInfo so users still see "what's loaded right now" without
// having to pass a repo argument.
func handleStatusRun(ix *search.Index, args StatusArgs) (*sdk.CallToolResult, any, error) {
	opts := status.BuildOptions{}
	if ix != nil {
		opts.LiveIndex = liveIndexInfo("(embedded corpus)", ix)
	}
	return renderStatusResult(status.Build(opts), args)
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

func liveIndexInfo(repo string, ix *search.Index) *status.IndexInfo {
	return &status.IndexInfo{
		Repo:        repo,
		ChunkCount:  ix.Len(),
		Mode:        search.ModeNames()[int(ix.Mode())],
		FileCount:   countUniqueFiles(ix),
		WatchActive: true, // ken-mcp v0.3+ always watches; embedded-corpus pointer is reloaded on rebuild
	}
}

// countUniqueFiles walks the index's chunks once and counts distinct
// file paths. The index doesn't expose this directly; for the status
// surface we accept the O(N) cost.
func countUniqueFiles(ix *search.Index) int {
	seen := make(map[string]struct{})
	for _, c := range ix.Chunks() {
		if c.Tombstoned {
			continue
		}
		seen[c.File] = struct{}{}
	}
	return len(seen)
}

// buildStructuralInfo summarizes the structural index into the
// status.StructuralInfo shape. Per-language file count is bucketed by
// extension (lowercase; leading dot kept so the table matches what
// `ken status --verbose` prints).
func buildStructuralInfo(sx *structural.Index) *status.StructuralInfo {
	info := &status.StructuralInfo{PerLanguageFiles: map[string]int{}}
	for path, fs := range sx.Files() {
		ext := filepath.Ext(path)
		info.PerLanguageFiles[ext]++
		for _, fn := range fs.Functions {
			if fn.IsMethod {
				info.Methods++
			} else {
				info.TopLevelSymbols++
			}
		}
		info.TopLevelSymbols += len(fs.Classes)
	}
	return info
}
