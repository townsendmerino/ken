package db

// Pure-Tier-2-mechanics orchestration shared by cmd/ken-mcp's wireDBTier2
// (env-var-driven CLI wiring) and mcp/db.Setup (config-struct-driven
// SDK author wiring). Extracted in v0.8.0 Part 3 (ADR-020) so the same
// Refresher + interval + listener lifecycle serves both surfaces; the
// CLI- and SDK-specific concerns (env-var parsing, SIGHUP, swap-target
// selection, logger interface) stay in their respective callers.
//
// The extraction is deliberately minimal: SetupTier2 takes a parsed
// Options struct + an OnSwap callback + an EnableListen flag, and
// orchestrates the lifecycle. Callers do their own logging on top of
// (or via) Options.LogWriter — SetupTier2 itself writes only the warn()
// diagnostics the rest of internal/db already uses.

import (
	"context"
	"errors"
	"fmt"

	"github.com/townsendmerino/ken/chunk"
)

// SetupTier2 orchestrates the full Tier-2 lifecycle:
//
//  1. Runs the initial IndexSchema (synchronously) and invokes onSwap
//     with the result so the caller's snapshot target sees the freshest
//     chunks before SetupTier2 returns.
//  2. Constructs the Refresher with onSwap as its swap callback.
//  3. Starts the periodic-reindex goroutine if opts.ReindexInterval > 0.
//  4. If enableListen is true AND the DSN routes to Postgres, starts
//     the LISTEN/NOTIFY listener goroutine. Non-Postgres DSNs with
//     enableListen=true emit a debug log and continue without the
//     listener — matches the cmd/ken-mcp env-var-validator pattern.
//
// Returns the *Refresher (for the caller's SIGHUP / reindex_db / etc.
// wiring) + a cleanup func + any startup error. The current cleanup is
// a no-op (goroutines exit on ctx cancellation); reserved as a future
// seam for explicit connection-pool close if the implementation ever
// needs one.
//
// Caller's responsibilities (NOT inside SetupTier2):
//   - Env-var parsing / config validation (cmd/ken-mcp) or Config-struct
//     validation (mcp/db).
//   - DefaultRepo validation (cmd/ken-mcp only — mcp.Run has no
//     analogue).
//   - SIGHUP signal handler installation (cmd/ken-mcp only — SDK
//     authors using mcp.Run don't typically need SIGHUP).
//   - Wrapping onSwap with the caller's preferred logger (e.g.
//     cmd/ken-mcp logs "Tier 2: indexed N chunks" after each swap; the
//     count is computed from the caller's callback wrapper, not inside
//     SetupTier2).
//   - Bridging *Refresher.TryRefresh → mcp.ReindexFunc for the
//     reindex_db MCP tool (Part 2).
func SetupTier2(ctx context.Context, opts Options, enableListen bool, onSwap func([]chunk.Chunk)) (*Refresher, func(), error) {
	opts = opts.validate()
	if opts.DSN == "" {
		return nil, nil, errors.New("db.SetupTier2: Options.DSN is empty; caller should not invoke SetupTier2 when Tier 2 is disabled")
	}
	if onSwap == nil {
		return nil, nil, errors.New("db.SetupTier2: onSwap callback is required")
	}

	// Initial introspection. The caller's onSwap callback fires with
	// the initial chunk set BEFORE we construct the Refresher — this
	// matches v0.7.0's wireDBTier2 ordering so the caller's snapshot
	// target sees DB chunks before any agent search is served.
	initial, err := IndexSchema(ctx, opts)
	if err != nil {
		return nil, nil, fmt.Errorf("initial IndexSchema: %w", err)
	}
	onSwap(initial)

	refresher, err := NewRefresher(opts, onSwap)
	if err != nil {
		return nil, nil, fmt.Errorf("NewRefresher: %w", err)
	}

	// Periodic reindex goroutine. Refresher.Run is a no-op when
	// ReindexInterval == 0, so we can call it unconditionally — but
	// gating on the interval keeps the goroutine-count down for the
	// "interval polling disabled" case and avoids stray goroutines in
	// tests that don't want them.
	if opts.ReindexInterval > 0 {
		go func() {
			if err := refresher.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				warn(opts, "SetupTier2: periodic refresher exit: %v", err)
			}
		}()
	}

	// LISTEN/NOTIFY listener (Postgres only). Non-Postgres engines
	// return ErrListenNotSupported from NewListener — we debug-log and
	// move on, consistent with the cmd/ken-mcp KEN_DB_LISTEN=1 +
	// non-Postgres-DSN behavior from Part 1.
	if enableListen {
		listener, lerr := NewListener(opts, func(ctx context.Context) {
			if err := refresher.Refresh(ctx); err != nil {
				warn(opts, "SetupTier2: LISTEN-driven refresh failed: %v", err)
			}
		})
		switch {
		case errors.Is(lerr, ErrListenNotSupported):
			warn(opts, "SetupTier2: enableListen=true ignored — LISTEN/NOTIFY is Postgres-only")
		case lerr != nil:
			warn(opts, "SetupTier2: NewListener: %v — listener disabled; interval polling continues", lerr)
		default:
			go func() {
				if err := listener.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
					warn(opts, "SetupTier2: listener exited: %v", err)
				}
			}()
		}
	}

	return refresher, func() {}, nil
}
