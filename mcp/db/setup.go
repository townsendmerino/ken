package mcpdb

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/townsendmerino/ken/internal/chunk"
	"github.com/townsendmerino/ken/internal/db"
	"github.com/townsendmerino/ken/mcp"
)

// Setup constructs the v0.8.0 Tier-2 machinery for SDK authors using
// mcp.Run: validates the Config, runs the initial IndexSchema, starts
// the periodic-refresh goroutine (if cfg.ReindexInterval > 0), starts
// the LISTEN/NOTIFY listener (if cfg.EnableListen && DSN is Postgres),
// and returns a mcp.ReindexFunc that the SDK author passes as
// mcp.Options.Reindex.
//
// Lifecycle:
//
//   - Setup runs the initial IndexSchema synchronously; the resulting
//     chunks are passed to a swap callback that's currently a no-op
//     (logged at info via cfg.LogWriter). Chunk integration into
//     mcp.Run's embedded *search.Index is deferred to v0.9.0 — see
//     the package-level docstring's caveat.
//   - The returned ReindexFunc is what the reindex_db MCP tool calls.
//     It delegates to *db.Refresher.TryRefresh and translates
//     db.ErrReindexInProgress into mcp.ReindexResult{InProgress: true}.
//   - cleanup() is currently a no-op (the periodic + listener
//     goroutines exit on ctx cancellation). Reserved as a future seam
//     for explicit connection-pool close.
//
// Empty-DSN behavior: when cfg.DSN == "", returns (nil, nil, nil).
// SDK author should treat this as "no DB; don't set opts.Reindex" —
// the safety net lets the author conditionally configure DSN without
// branching around the Setup call.
func Setup(ctx context.Context, cfg Config) (mcp.ReindexFunc, func(), error) {
	if cfg.DSN == "" {
		return nil, nil, nil
	}

	opts := cfg.toDBOptions()

	// onSwap is the swap callback Refresher invokes after each
	// successful IndexSchema. For v0.8.0 Part 3 this is a logging
	// no-op — chunks are captured but not yet unioned into mcp.Run's
	// embedded *search.Index. The log line gives the SDK author a
	// debug breadcrumb confirming the introspection ran.
	//
	// When a v0.9.0 follow-up adds *search.Index.SetExtraChunks (or
	// equivalent), the SDK author wiring will need to:
	//   - Pass an *Index pointer into Setup (or a swap callback like
	//     cmd/ken-mcp passes wix.SetExtraChunks)
	//   - Setup composes the SetExtraChunks call with the existing
	//     log line.
	// The Config struct would gain a Swap callback field, default
	// (nil → log-only); SDK authors opting into chunk integration
	// would supply one. Documented in ADR-020 Part 3 Consequences.
	onSwap := func(chunks []chunk.Chunk) {
		if cfg.LogWriter == nil {
			return
		}
		fmt.Fprintf(cfg.LogWriter,
			"mcp/db: reindex captured %d DB chunks (chunk-into-Index integration deferred to v0.9.0; see ADR-020 Part 3)\n",
			len(chunks),
		)
	}

	refresher, cleanup, err := db.SetupTier2(ctx, opts, cfg.EnableListen, onSwap)
	if err != nil {
		return nil, nil, fmt.Errorf("mcp/db.Setup: %w", err)
	}

	reindex := reindexCallback(refresher)
	return reindex, cleanup, nil
}

// reindexCallback bridges *db.Refresher's TryRefresh into the
// mcp.ReindexFunc shape mcp.Run consumes via Options.Reindex.
//
// The bridge translates db.ErrReindexInProgress into
// mcp.ReindexResult{InProgress: true} so the mcp package itself
// doesn't need to import internal/db. This is a near-duplicate of
// cmd/ken-mcp's reindexCallback — kept here (rather than extracted
// to internal/db or mcp itself) because:
//   - internal/db can't import mcp (would create a cycle: mcp tries
//     to stay DB-free, internal/db is the implementation).
//   - mcp can't import internal/db (the entire v0.6.0 binary-size
//     contract this package preserves depends on that boundary).
//
// So the bridge lives at both callsites (cmd/ken-mcp and mcp/db)
// because each is the layer that has BOTH dependencies in scope.
func reindexCallback(refresher *db.Refresher) mcp.ReindexFunc {
	if refresher == nil {
		return nil
	}
	return func(ctx context.Context) mcp.ReindexResult {
		start := time.Now()
		err := refresher.TryRefresh(ctx)
		switch {
		case errors.Is(err, db.ErrReindexInProgress):
			return mcp.ReindexResult{InProgress: true}
		case err != nil:
			return mcp.ReindexResult{Err: err}
		default:
			return mcp.ReindexResult{Elapsed: time.Since(start)}
		}
	}
}
