//go:build dbintegration

// Integration tests for internal/db.Listener (v0.8.0 Part 1, ADR-020).
// Behind the dbintegration build tag — default `go test ./...` skips.
// Setup is identical to integration_test.go: KEN_DB_TEST_DSN env var
// pointing at a live Postgres (CI provides postgres:16-alpine).
package db

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// installTrigger applies the embedded ListenNotifyScript via a fresh
// connection. The script is idempotent (DROP IF EXISTS + CREATE) so
// callers can run it before every test without coordinating with
// previous runs.
func installTrigger(t *testing.T, ctx context.Context, dsn string) {
	t.Helper()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("installTrigger connect: %v", err)
	}
	defer conn.Close(context.Background())
	if _, err := conn.Exec(ctx, ListenNotifyScript); err != nil {
		t.Fatalf("installTrigger exec: %v", err)
	}
}

// dropTrigger removes the event trigger so the missing-trigger code
// path can be exercised. Mirrors what an operator who hasn't run the
// setup script would have.
func dropTrigger(t *testing.T, ctx context.Context, dsn string) {
	t.Helper()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("dropTrigger connect: %v", err)
	}
	defer conn.Close(context.Background())
	if _, err := conn.Exec(ctx, "DROP EVENT TRIGGER IF EXISTS "+triggerName); err != nil {
		t.Fatalf("dropTrigger exec: %v", err)
	}
	if _, err := conn.Exec(ctx, "DROP FUNCTION IF EXISTS ken_notify_schema_changed()"); err != nil {
		t.Fatalf("drop function: %v", err)
	}
}

// execDDL runs a DDL statement (or sequence) on a fresh connection so
// the listener's separate connection observes the resulting NOTIFY.
func execDDL(t *testing.T, ctx context.Context, dsn, sql string) {
	t.Helper()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("execDDL connect: %v", err)
	}
	defer conn.Close(context.Background())
	if _, err := conn.Exec(ctx, sql); err != nil {
		t.Fatalf("execDDL %q: %v", sql, err)
	}
}

// uniqueTableName returns a per-test table name so parallel runs of the
// integration suite don't collide on the same DROP/CREATE sequence.
func uniqueTableName(prefix string) string {
	return fmt.Sprintf("ken_listen_%s_%d", prefix, time.Now().UnixNano())
}

// safeBuffer is a small mutex-wrapped bytes.Buffer for thread-safe log
// capture in the listener goroutine. testing.T.Log isn't a *Buffer
// substitute when we want to grep the contents.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestListenIntegration_HappyPath: install trigger, start listener,
// fire a CREATE TABLE, expect onNotify within ~200ms.
func TestListenIntegration_HappyPath(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	dsn := dsnOrSkip(t)
	installTrigger(t, ctx, dsn)
	t.Cleanup(func() { dropTrigger(t, context.Background(), dsn) })

	var notifyCount atomic.Int32
	notified := make(chan struct{}, 16)
	logBuf := &safeBuffer{}

	listener, err := NewListener(
		Options{DSN: dsn, LogWriter: logBuf},
		func(context.Context) {
			notifyCount.Add(1)
			notified <- struct{}{}
		},
	)
	if err != nil {
		t.Fatalf("NewListener: %v", err)
	}

	listenCtx, listenCancel := context.WithCancel(ctx)
	t.Cleanup(listenCancel)
	go func() { _ = listener.Run(listenCtx) }()

	// Wait for "active on channel" before firing DDL — otherwise we could
	// race the notification past the listener's pre-LISTEN window.
	if !waitForLog(logBuf, "active on channel", "", 5*time.Second) {
		t.Fatalf("listener never logged 'active' line; log:\n%s", logBuf.String())
	}

	table := uniqueTableName("happy")
	execDDL(t, ctx, dsn, fmt.Sprintf("CREATE TABLE %s (id int)", table))
	t.Cleanup(func() {
		execDDL(t, context.Background(), dsn, fmt.Sprintf("DROP TABLE IF EXISTS %s", table))
	})

	select {
	case <-notified:
		// good
	case <-time.After(5 * time.Second):
		t.Fatalf("no notification within 5s after CREATE TABLE; log:\n%s", logBuf.String())
	}
	if got := notifyCount.Load(); got != 1 {
		t.Errorf("notifyCount = %d, want 1 after single CREATE TABLE", got)
	}
}

// TestListenIntegration_Debounce: one transaction with multiple DDL
// statements should collapse to exactly one onNotify call within the
// debounce window.
func TestListenIntegration_Debounce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	dsn := dsnOrSkip(t)
	installTrigger(t, ctx, dsn)
	t.Cleanup(func() { dropTrigger(t, context.Background(), dsn) })

	var notifyCount atomic.Int32
	notified := make(chan struct{}, 16)
	logBuf := &safeBuffer{}

	listener, err := NewListener(
		Options{DSN: dsn, LogWriter: logBuf},
		func(context.Context) {
			notifyCount.Add(1)
			notified <- struct{}{}
		},
	)
	if err != nil {
		t.Fatalf("NewListener: %v", err)
	}

	listenCtx, listenCancel := context.WithCancel(ctx)
	t.Cleanup(listenCancel)
	go func() { _ = listener.Run(listenCtx) }()

	if !waitForLog(logBuf, "active on channel", "", 5*time.Second) {
		t.Fatalf("listener never logged 'active' line; log:\n%s", logBuf.String())
	}

	table := uniqueTableName("debounce")
	idx := table + "_idx"
	t.Cleanup(func() {
		execDDL(t, context.Background(), dsn, fmt.Sprintf("DROP TABLE IF EXISTS %s", table))
	})

	// Three DDL statements in a single transaction — Postgres fires
	// one NOTIFY per statement (the event trigger walks
	// pg_event_trigger_ddl_commands() inside a single ddl_command_end
	// firing per statement). All arrive in fast succession.
	execDDL(t, ctx, dsn, fmt.Sprintf(`
BEGIN;
CREATE TABLE %s (id int, email text);
CREATE INDEX %s ON %s (email);
ALTER TABLE %s ADD COLUMN role text DEFAULT 'guest';
COMMIT;`, table, idx, table, table))

	// Wait for the first notification, then verify no second one
	// arrives within a window longer than the debounce window. The
	// listener should have collapsed the burst into one onNotify call.
	select {
	case <-notified:
	case <-time.After(5 * time.Second):
		t.Fatalf("no notification within 5s; log:\n%s", logBuf.String())
	}

	// Wait long enough for any spurious extra onNotify to land. The
	// debounce window is 50ms; we sample at 500ms to be conservative
	// against scheduler jitter on the CI runner.
	time.Sleep(500 * time.Millisecond)
	if got := notifyCount.Load(); got != 1 {
		t.Errorf("expected exactly 1 onNotify call (debounced); got %d; log:\n%s",
			got, logBuf.String())
	}
}

// TestListenIntegration_MissingTrigger: drop the trigger, start the
// listener, expect a warn line naming the fix command + no panic +
// no notification (listener idles until ctx canceled).
func TestListenIntegration_MissingTrigger(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	dsn := dsnOrSkip(t)
	// Ensure trigger is NOT installed — even if a previous test left it.
	dropTrigger(t, ctx, dsn)

	var notifyCount atomic.Int32
	logBuf := &safeBuffer{}

	listener, err := NewListener(
		Options{DSN: dsn, LogWriter: logBuf},
		func(context.Context) { notifyCount.Add(1) },
	)
	if err != nil {
		t.Fatalf("NewListener: %v", err)
	}

	listenCtx, listenCancel := context.WithCancel(ctx)
	defer listenCancel()
	done := make(chan struct{})
	go func() {
		_ = listener.Run(listenCtx)
		close(done)
	}()

	// Wait for the warn to appear, then check exact content.
	if !waitForLog(logBuf, "not installed", "", 5*time.Second) {
		t.Fatalf("expected 'not installed' warn within 5s; log:\n%s", logBuf.String())
	}
	log := logBuf.String()
	for _, want := range []string{
		"event trigger \"" + triggerName + "\" is not installed",
		"ken-mcp print-listen-script | psql $KEN_DB_DSN",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("warn output missing %q; got:\n%s", want, log)
		}
	}

	// Confirm the listener is actually idle (not spamming the warn).
	// Sample the log size, wait, and verify it hasn't grown materially.
	before := len(logBuf.String())
	time.Sleep(500 * time.Millisecond)
	after := len(logBuf.String())
	if after-before > 200 {
		t.Errorf("listener appears to be spamming warns; log grew by %d bytes in 500ms", after-before)
	}

	// Cancel and confirm the goroutine exits cleanly within a second.
	listenCancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("listener goroutine didn't exit within 2s of ctx cancel")
	}

	if got := notifyCount.Load(); got != 0 {
		t.Errorf("expected 0 onNotify calls (trigger missing); got %d", got)
	}
}

// TestListenIntegration_ReconnectAfterDrop: start listener happily,
// then ask Postgres to terminate the listener's backend so the next
// WaitForNotification errors → reconnect cycle kicks in and listens
// resume. After reconnect, a fresh DDL should still arrive.
func TestListenIntegration_ReconnectAfterDrop(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	dsn := dsnOrSkip(t)
	installTrigger(t, ctx, dsn)
	t.Cleanup(func() { dropTrigger(t, context.Background(), dsn) })

	notified := make(chan struct{}, 16)
	logBuf := &safeBuffer{}

	listener, err := NewListener(
		Options{DSN: dsn, LogWriter: logBuf},
		func(context.Context) { notified <- struct{}{} },
	)
	if err != nil {
		t.Fatalf("NewListener: %v", err)
	}

	listenCtx, listenCancel := context.WithCancel(ctx)
	t.Cleanup(listenCancel)
	go func() { _ = listener.Run(listenCtx) }()

	if !waitForLog(logBuf, "active on channel", "", 5*time.Second) {
		t.Fatalf("listener never logged 'active' line; log:\n%s", logBuf.String())
	}

	// Use pg_terminate_backend on the listener's PID. Find it by the
	// application_name or by filtering for LISTEN'ing connections. The
	// simplest reliable approach: kill every backend that's not us.
	killOtherBackends(t, ctx, dsn)

	// Listener should log a connection error + reconnect attempt.
	if !waitForLog(logBuf, "connection error", "reconnecting in", 10*time.Second) {
		t.Fatalf("expected reconnect warn within 10s; log:\n%s", logBuf.String())
	}

	// Wait for the SECOND "active on channel" line — the listener has
	// successfully reconnected.
	if !waitForLogCount(logBuf, "active on channel", 2, 15*time.Second) {
		t.Fatalf("listener didn't reconnect; log:\n%s", logBuf.String())
	}

	// Fire a fresh DDL post-reconnect; the new listener connection
	// should observe it.
	table := uniqueTableName("reconnect")
	t.Cleanup(func() {
		execDDL(t, context.Background(), dsn, fmt.Sprintf("DROP TABLE IF EXISTS %s", table))
	})
	execDDL(t, ctx, dsn, fmt.Sprintf("CREATE TABLE %s (id int)", table))

	select {
	case <-notified:
	case <-time.After(5 * time.Second):
		t.Fatalf("no notification after reconnect within 5s; log:\n%s", logBuf.String())
	}
}

// killOtherBackends uses pg_terminate_backend to drop every backend on
// the DB except the connection making the call. Listener's connection
// is one of these casualties — that's the point.
func killOtherBackends(t *testing.T, ctx context.Context, dsn string) {
	t.Helper()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("killOtherBackends connect: %v", err)
	}
	defer conn.Close(context.Background())
	_, err = conn.Exec(ctx,
		"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE pid <> pg_backend_pid() AND datname = current_database()",
	)
	if err != nil {
		t.Fatalf("pg_terminate_backend: %v", err)
	}
}

// waitForLog blocks up to timeout looking for a log line containing
// `primary` (and, if non-empty, ALSO containing `also`). Returns true
// when found, false on timeout. Polled because the listener writes
// from a goroutine; no channel synchronization is exposed.
func waitForLog(buf *safeBuffer, primary, also string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		s := buf.String()
		if strings.Contains(s, primary) && (also == "" || strings.Contains(s, also)) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// waitForLogCount blocks up to timeout for `substring` to appear at
// least `count` times in the captured log. Used by the reconnect test
// to detect that the listener has logged "active on channel" twice
// (once for the initial connect, once for the reconnect).
func waitForLogCount(buf *safeBuffer, substring string, count int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Count(buf.String(), substring) >= count {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}
