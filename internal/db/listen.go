package db

// Postgres LISTEN/NOTIFY push-based schema-change detection (v0.8.0
// Part 1, ADR-020).
//
// Listener owns a dedicated pgx.Conn separate from the introspection
// path. A long-running WaitForNotification call must not tie up a
// connection that IndexSchema needs. The listener calls onNotify (a
// debounced callback into Refresher.Refresh) for each batch of
// notifications.
//
// ── Driver audit (stdout cleanliness) ───────────────────────────────
// The listener's pgx.Conn is opened via pgx.Connect, which (like the
// introspection path's pgx.ConnectConfig) defaults Tracer to nil. No
// protocol-level logging to stdout. Diagnostic lines (reconnect,
// trigger-missing) go to opts.LogWriter — wired to os.Stderr by
// cmd/ken-mcp. TestBinary_StdoutIsCleanJSONRPC_WithListen pins this.
//
// ── Engine scope ────────────────────────────────────────────────────
// Postgres only. NewListener returns ErrListenNotSupported for non-
// Postgres DSNs; cmd/ken-mcp logs the case at debug level and skips
// listener startup. MySQL has no native LISTEN/NOTIFY and SQLite is
// in-process by design; an interval-polling backstop is the only
// sensible alternative for those engines.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrListenNotSupported is returned by NewListener when opts.DSN is not
// a Postgres DSN. Callers should distinguish this from real connection
// errors (debug log + skip vs warn log + retry).
var ErrListenNotSupported = errors.New("db: LISTEN/NOTIFY is Postgres-only; current DSN is not Postgres")

// notifyChannel is the single Postgres NOTIFY channel ken uses. Pinned
// across the SQL script and the listener so they always agree.
const notifyChannel = "ken_schema_changed"

// triggerName is the event-trigger object the SQL script installs. The
// listener queries pg_event_trigger to verify it exists before issuing
// LISTEN, so an operator who set KEN_DB_LISTEN=1 without running the
// setup script gets a clear "trigger missing" warn instead of a silent
// no-op.
const triggerName = "ken_schema_changed_trigger"

// defaultDebounceWindow is the time the listener waits for additional
// notifications after the first one in a burst before invoking
// onNotify. A migration file with `CREATE TABLE foo; CREATE INDEX ...`
// emits multiple notifications in fast succession; collecting them
// into one refresh halves DB load. 50ms is short enough that even
// "live during agent conversation" feels instant.
const defaultDebounceWindow = 50 * time.Millisecond

// defaultInitialBackoff is the first reconnect-after-error delay.
// Doubles each subsequent failure up to maxBackoff.
const defaultInitialBackoff = 100 * time.Millisecond

// maxBackoff caps the reconnect delay so a long-running outage doesn't
// drift to multi-minute reconnect intervals.
const maxBackoff = 30 * time.Second

// Listener manages a dedicated pgx connection that LISTENs for
// ken_schema_changed notifications and calls onNotify (debounced) for
// each batch of events.
//
// The connection is separate from IndexSchema's so a blocking
// WaitForNotification call doesn't starve introspection — both can
// fire concurrently.
type Listener struct {
	dsn      string
	onNotify func(context.Context)
	logger   io.Writer

	// debounceWindow is exposed via the zero-value mechanism for tests
	// that want to use a shorter window without exporting a setter.
	debounceWindow time.Duration
}

// NewListener constructs a Listener. Returns ErrListenNotSupported when
// opts.DSN is not Postgres (caller should debug-log and skip).
// onNotify must be non-nil — typically a closure that calls
// (*Refresher).Refresh.
func NewListener(opts Options, onNotify func(context.Context)) (*Listener, error) {
	if dsnEngine(opts.DSN) != "postgres" {
		return nil, ErrListenNotSupported
	}
	if onNotify == nil {
		return nil, errors.New("db.NewListener: onNotify callback is required")
	}
	logger := opts.LogWriter
	if logger == nil {
		// Never default to os.Stdout — that's the JSON-RPC channel for
		// cmd/ken-mcp. Discard is the safe fallback; cmd/ken-mcp wires
		// os.Stderr explicitly.
		logger = io.Discard
	}
	return &Listener{
		dsn:            opts.DSN,
		onNotify:       onNotify,
		logger:         logger,
		debounceWindow: defaultDebounceWindow,
	}, nil
}

// Run blocks until ctx is canceled. Internally it loops over runOnce,
// reconnecting with exponential backoff (100ms → 30s cap) after any
// connection-level error. Returns ctx.Err() on cancellation.
//
// Per-error backoff resets on a successful (re-)connect, so a flaky
// network that disconnects every ~minute doesn't drift to 30s waits.
// (Implementation detail: a "successful connect" here means runOnce
// returned an error AFTER LISTEN was issued, not before — see runOnce.)
func (l *Listener) Run(ctx context.Context) error {
	backoff := defaultInitialBackoff

	for ctx.Err() == nil {
		listened, err := l.runOnce(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Reset backoff if the previous attempt actually got as far as
		// LISTEN active — that signals the DB is healthy and the error
		// is a fresh, separate transient.
		if listened {
			backoff = defaultInitialBackoff
		}
		fmt.Fprintf(l.logger, "ken-mcp listen: connection error: %v; reconnecting in %s\n", err, backoff)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}

		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
	return ctx.Err()
}

// runOnce opens a dedicated connection, verifies the event trigger is
// installed, issues LISTEN, and runs the notification loop. Returns
// when the connection drops or ctx is canceled.
//
// The bool return is "did we get as far as LISTEN active" — used by
// Run to decide whether to reset the backoff (mid-loop disconnects are
// healthier signals than connect-time failures).
func (l *Listener) runOnce(ctx context.Context) (listened bool, err error) {
	conn, err := pgx.Connect(ctx, l.dsn)
	if err != nil {
		return false, fmt.Errorf("connect: %w", err)
	}
	defer conn.Close(context.Background())

	// Trigger-existence check: if missing, log the fix command once
	// per (re)connect and idle until ctx done. We do NOT spam stderr —
	// the next reconnect (which fires when the connection eventually
	// drops for any reason) will re-check, so operators see the warn
	// at startup, fix it, and on the next reconnect listening resumes.
	exists, err := triggerExists(ctx, conn)
	if err != nil {
		return false, fmt.Errorf("checking event trigger: %w", err)
	}
	if !exists {
		fmt.Fprintf(l.logger, "ken-mcp listen: LISTEN/NOTIFY enabled but event trigger %q is not installed.\n", triggerName)
		fmt.Fprintln(l.logger, "ken-mcp listen: run `ken-mcp print-listen-script | psql $KEN_DB_DSN` to install it.")
		fmt.Fprintln(l.logger, "ken-mcp listen: listener idle until next reconnect (interval polling continues).")
		<-ctx.Done()
		return false, ctx.Err()
	}

	if _, err := conn.Exec(ctx, "LISTEN "+notifyChannel); err != nil {
		return false, fmt.Errorf("LISTEN: %w", err)
	}
	listened = true
	fmt.Fprintf(l.logger, "ken-mcp listen: active on channel %q\n", notifyChannel)

	// Notification loop. WaitForNotification blocks until a NOTIFY
	// arrives, the connection drops, or ctx is canceled. Errors here
	// are connection-level (the loop exits and Run reconnects).
	for {
		notif, waitErr := conn.WaitForNotification(ctx)
		if waitErr != nil {
			return listened, fmt.Errorf("WaitForNotification: %w", waitErr)
		}
		_ = notif // payload is informational; we always re-introspect everything

		// Debounce: drain any additional notifications that arrive
		// within the debounce window into the same refresh batch. A
		// migration file with multiple DDL statements fires multiple
		// NOTIFYs in fast succession; coalescing halves DB load with
		// no observable agent-side latency change.
		l.drainDebounce(ctx, conn)

		// Detach from the listener's context so a cancellation between
		// "notification received" and "refresh starts" doesn't cause
		// the refresh to be skipped. The refresh itself respects ctx
		// internally for its own queries; this just guarantees we run
		// it once per notification batch even if the parent is shutting
		// down. We pass ctx so the refresh itself can short-circuit on
		// cancellation — refresh just runs harmlessly.
		l.onNotify(ctx)
	}
}

// drainDebounce consumes any additional notifications that arrive
// within debounceWindow. Returns when the window closes (timeout) or
// the connection drops (the outer loop will then propagate the error
// and trigger reconnect).
func (l *Listener) drainDebounce(ctx context.Context, conn *pgx.Conn) {
	deadline := time.Now().Add(l.debounceWindow)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return
		}
		drainCtx, cancel := context.WithTimeout(ctx, remaining)
		_, drainErr := conn.WaitForNotification(drainCtx)
		cancel()
		if drainErr == nil {
			// Got another notification within the window; keep draining
			// until the window closes.
			continue
		}
		// Either the debounce window closed (DeadlineExceeded — expected)
		// or the connection dropped (real error). Either way we're done
		// draining for this batch; the outer loop will handle a real
		// connection error on the next WaitForNotification.
		return
	}
}

// triggerExists reports whether the ken_schema_changed_trigger event
// trigger is installed. We query pg_event_trigger directly rather than
// information_schema (which doesn't list event triggers as of
// Postgres 16).
func triggerExists(ctx context.Context, conn *pgx.Conn) (bool, error) {
	var exists bool
	err := conn.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM pg_event_trigger WHERE evtname = $1)",
		triggerName,
	).Scan(&exists)
	return exists, err
}
