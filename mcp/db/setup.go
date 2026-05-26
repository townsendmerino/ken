package mcpdb

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/townsendmerino/ken/internal/chunk"
	"github.com/townsendmerino/ken/internal/db"
	"github.com/townsendmerino/ken/mcp"
)

// Refresher is the SDK author's Tier-2 handle. Returned by Setup;
// passed to mcp.Options.DB (or mcp.Config.DB for the cache-backed
// NewServer path). Implements mcp.DBIntegration so mcp.Run can call
// Start (registers the chunk-integration callback + spawns the
// interval/listener goroutines) and TryRefresh (invoked by the
// reindex_db MCP tool).
//
// Lifecycle:
//
//   - Setup constructs and validates the Refresher; no goroutines
//     run yet, no DB connection is opened. The DSN is parsed at
//     Start time, not Setup time.
//   - Start opens the DB connection, runs the initial IndexSchema
//     (firing onExtras synchronously with the initial chunks before
//     returning), constructs the inner *internal/db.Refresher, and
//     starts the interval-ticker + LISTEN/NOTIFY goroutines. Returns
//     a cleanup func the caller defers.
//   - TryRefresh delegates to the inner Refresher; translates
//     db.ErrReindexInProgress into mcp.ReindexResult{InProgress: true}.
//   - Refresh (the blocking variant) is exposed for cmd/ken-mcp's
//     SIGHUP handler. SDK authors using mcp.Run typically don't need
//     it.
//
// Empty-DSN behavior: Setup with Config{DSN: ""} returns (nil, nil),
// matching the v0.8.0 Part 3 safety-net contract — SDK authors with
// conditional DB configuration can call Setup unconditionally and
// pass the nil *Refresher to mcp.Options.DB; the mcp package's nil-DB
// path is byte-identical to the v0.6.0/v0.7.x docs-only behavior.
type Refresher struct {
	cfg Config

	mu      sync.Mutex
	inner   *db.Refresher // populated by Start; nil before
	started bool
}

// Setup validates Config and returns a *Refresher ready for Start.
// Empty Config.DSN returns (nil, nil) — the documented safety net.
// Other validation errors (TBD as new fields land) return non-nil
// error. v0.8.0 Part 3 addendum (ADR-020) merges Part 3's original
// Setup signature (which returned a ReindexFunc + cleanup) with the
// chunk-integration seam from the revised Prompt C: the Refresher
// now owns its own Start, taking the onExtras swap target from the
// caller (mcp.Run or cmd/ken-mcp) at Start time.
func Setup(_ context.Context, cfg Config) (*Refresher, error) {
	if cfg.DSN == "" {
		return nil, nil
	}
	// Config validation (TBD as new fields land). Currently no
	// validation needed beyond the engine-dispatch which happens
	// inside internal/db.IndexSchema at Start time.
	return &Refresher{cfg: cfg}, nil
}

// Start wires onExtras as the swap callback and orchestrates the full
// Tier-2 lifecycle (initial IndexSchema → Refresher → interval ticker
// → LISTEN/NOTIFY listener). Returns a cleanup func the caller MUST
// defer; canceling ctx is also honored.
//
// onExtras MUST be non-nil. mcp.Run provides an
// ixPtr.Store(baseIx.WithExtraChunks(extras)) closure; cmd/ken-mcp
// provides a wix.SetExtraChunks wrapper composed with its logger.
//
// Calling Start twice on the same Refresher returns an error — Start
// is one-shot. SDK authors typically construct one Refresher per
// process and pass it once.
//
// Receiver nil-safety: Start on a nil *Refresher returns a no-op
// cleanup and nil error, so SDK authors can pass the nil-result-from-
// Setup directly to mcp.Options.DB without branching.
func (r *Refresher) Start(ctx context.Context, onExtras func([]chunk.Chunk)) (func(), error) {
	if r == nil {
		return func() {}, nil
	}
	if onExtras == nil {
		return nil, errors.New("mcp/db.Refresher.Start: onExtras callback is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started {
		return nil, errors.New("mcp/db.Refresher.Start: already started (Refresher is one-shot)")
	}

	opts := r.cfg.toDBOptions()
	inner, cleanup, err := db.SetupTier2(ctx, opts, r.cfg.EnableListen, onExtras)
	if err != nil {
		return nil, fmt.Errorf("mcp/db.Refresher.Start: %w", err)
	}
	r.inner = inner
	r.started = true
	return cleanup, nil
}

// TryRefresh implements mcp.DBIntegration. Bridges
// *internal/db.Refresher.TryRefresh into mcp.ReindexResult; translates
// db.ErrReindexInProgress into ReindexResult{InProgress: true} so the
// mcp package itself doesn't need to import internal/db.
//
// Returns ReindexResult{Err: ...} if Start hasn't been called yet —
// this is a programming error on the caller's side (mcp.Run calls
// Start before serving the first request) but we surface it via the
// existing Err channel rather than panicking. handleReindexDB's "Err
// != nil" branch renders this as "Reindex failed: ..." to the agent.
//
// Receiver nil-safety: TryRefresh on a nil *Refresher returns
// ReindexResult{Err: ...} — defense-in-depth for callers that route
// past mcp's nil-DB-tool-not-registered gate.
func (r *Refresher) TryRefresh(ctx context.Context) mcp.ReindexResult {
	if r == nil {
		return mcp.ReindexResult{Err: errors.New("DB integration not configured")}
	}
	r.mu.Lock()
	inner := r.inner
	r.mu.Unlock()
	if inner == nil {
		return mcp.ReindexResult{Err: errors.New("DB integration not started (call Refresher.Start before serving requests)")}
	}
	start := time.Now()
	err := inner.TryRefresh(ctx)
	switch {
	case errors.Is(err, db.ErrReindexInProgress):
		return mcp.ReindexResult{InProgress: true}
	case err != nil:
		return mcp.ReindexResult{Err: err}
	default:
		return mcp.ReindexResult{Elapsed: time.Since(start)}
	}
}

// Refresh is the BLOCKING variant of TryRefresh. Exposed for
// cmd/ken-mcp's SIGHUP handler — interval-tick / SIGHUP / LISTEN
// semantics genuinely want to serialize (their callers know they're
// blocking), unlike the agent-callable reindex_db tool which uses
// the fail-fast TryRefresh.
//
// Returns nil on success, the underlying error otherwise (NOT
// translated through ReindexResult). SDK authors using mcp.Run
// typically don't need this method; it's a cmd/ken-mcp-specific
// surface.
func (r *Refresher) Refresh(ctx context.Context) error {
	if r == nil {
		return errors.New("mcp/db.Refresher.Refresh: nil receiver")
	}
	r.mu.Lock()
	inner := r.inner
	r.mu.Unlock()
	if inner == nil {
		return errors.New("mcp/db.Refresher.Refresh: not started (call Start before Refresh)")
	}
	return inner.Refresh(ctx)
}
