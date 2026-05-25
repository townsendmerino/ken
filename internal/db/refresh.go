package db

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/townsendmerino/ken/internal/chunk"
)

// Refresher orchestrates the three reindex paths Tier 2 supports
// (ADR-017): build-once-at-startup (caller drives), periodic via
// Options.ReindexInterval > 0 (Run starts a ticker), and SIGHUP-driven
// manual (caller calls Refresh from its signal handler). All three
// share one IndexSchema implementation and one atomic-swap callback;
// the Refresher just sequences them.
//
// Refresher does NOT install signal handlers itself — separation of
// concerns: signal-handler registration is cmd/ken-mcp's job (it has
// to install SIGHUP unix-only and no-op on Windows). The caller's
// handler calls (*Refresher).Refresh(ctx).
//
// Concurrent Refresh calls and ticker firings are serialized via mu;
// at most one IndexSchema call is in flight at a time. This keeps the
// swap callback from being called twice for the same logical refresh
// (with potentially different chunk sets if the second introspection
// raced ahead).
type Refresher struct {
	opts Options
	swap func([]chunk.Chunk)
	mu   sync.Mutex
}

// NewRefresher constructs a Refresher with the given options and swap
// callback. Returns an error if opts.DSN is empty — caller should not
// construct a Refresher when Tier 2 is disabled (just skip the whole
// code path). swap is the user's atomic-snapshot mutator (typically
// WatchedIndex.SetExtraChunks wrapped to do the union with FS chunks).
func NewRefresher(opts Options, swap func([]chunk.Chunk)) (*Refresher, error) {
	if opts.DSN == "" {
		return nil, errors.New("db.NewRefresher: Options.DSN is empty; Tier 2 is disabled, do not construct a Refresher")
	}
	if swap == nil {
		return nil, errors.New("db.NewRefresher: swap callback is required")
	}
	opts = opts.validate()
	return &Refresher{opts: opts, swap: swap}, nil
}

// Run starts the periodic-refresh loop if Options.ReindexInterval > 0.
// Returns nil immediately if interval is 0 or negative (no-op — caller
// uses Refresh manually). Otherwise blocks until ctx is canceled, then
// returns ctx.Err() (typically context.Canceled).
//
// Tick-time refresh failures are logged via Options.LogWriter at warn
// level and do NOT exit Run — agents tolerate stale schema better than
// "DB chunks vanished mid-session" when a transient query failure hits.
func (r *Refresher) Run(ctx context.Context) error {
	if r.opts.ReindexInterval <= 0 {
		return nil
	}
	ticker := time.NewTicker(r.opts.ReindexInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := r.Refresh(ctx); err != nil {
				warn(r.opts, "periodic refresh failed: %v", err)
			}
		}
	}
}

// Refresh triggers an immediate IndexSchema rebuild and swap. Safe to
// call concurrently with Run (or other Refresh callers) — the mutex
// serializes so at most one refresh is in flight at any time.
//
// Returns the underlying IndexSchema error if any, so the caller can
// log differentiate transient-vs-fatal at its discretion. The chunks
// are still swapped on success.
func (r *Refresher) Refresh(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	chunks, err := IndexSchema(ctx, r.opts)
	if err != nil {
		return fmt.Errorf("Refresh: %w", err)
	}
	// Always call swap, even with nil chunks — that's the
	// "DB unreachable, clear the DB chunks" path. The orchestrator's
	// composition (FS ∪ DB) handles a nil DB-chunks slot.
	r.swap(chunks)
	return nil
}
