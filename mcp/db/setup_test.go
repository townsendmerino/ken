package mcpdb

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite" // empty-sqlite-file harness for the happy-path tests

	"github.com/townsendmerino/ken/internal/chunk"
)

// TestSetup_EmptyDSN_ReturnsNil pins the documented safety net: an
// SDK author who conditionally configures DSN can call Setup
// unconditionally; an empty DSN returns (nil, nil) so the caller
// passes the nil *Refresher to mcp.Options.DB and the tool isn't
// registered.
func TestSetup_EmptyDSN_ReturnsNil(t *testing.T) {
	refresher, err := Setup(context.Background(), Config{})
	if err != nil {
		t.Errorf("Setup with empty DSN should not error; got %v", err)
	}
	if refresher != nil {
		t.Errorf("Setup with empty DSN should return nil refresher; got non-nil")
	}
}

// TestSetup_StartInvalidDSN_ReturnsError — Setup validates Config
// only; the DSN routing happens at Start time. A bogus DSN surfaces
// as a Start error.
func TestSetup_StartInvalidDSN_ReturnsError(t *testing.T) {
	refresher, err := Setup(context.Background(), Config{
		DSN:       "redis://nope:6379/0",
		LogWriter: &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("Setup should not error for unsupported scheme until Start; got %v", err)
	}
	if refresher == nil {
		t.Fatal("Setup with non-empty DSN should return non-nil refresher")
	}
	_, err = refresher.Start(context.Background(), func([]chunk.Chunk) {})
	if err == nil {
		t.Errorf("Refresher.Start with bogus DSN scheme should error")
	}
	if !strings.Contains(err.Error(), "mcp/db.Refresher.Start") {
		t.Errorf("error should be wrapped with mcp/db.Refresher.Start prefix; got %v", err)
	}
}

// TestSetup_HappyPath — empty SQLite DSN routes through to a clean
// IndexSchema (zero chunks). Setup returns a non-nil Refresher;
// Start fires the onExtras callback with the initial chunks before
// returning. TryRefresh wires through the inner Refresher.
func TestSetup_HappyPath(t *testing.T) {
	dsn := emptySQLiteDSN(t)
	logBuf := &bytes.Buffer{}

	ctx := t.Context()

	refresher, err := Setup(ctx, Config{
		DSN:       dsn,
		LogWriter: logBuf,
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if refresher == nil {
		t.Fatal("Setup happy path returned nil refresher")
	}

	var swapCalls atomic.Int32
	onExtras := func([]chunk.Chunk) { swapCalls.Add(1) }
	cleanup, err := refresher.Start(ctx, onExtras)
	if err != nil {
		t.Fatalf("Refresher.Start: %v", err)
	}
	if cleanup == nil {
		t.Fatal("Refresher.Start returned nil cleanup")
	}
	defer cleanup()

	// Start fires onExtras synchronously with the initial chunks
	// before returning — the swap callback has run at least once by
	// now.
	if got := swapCalls.Load(); got != 1 {
		t.Errorf("onExtras call count after Start = %d, want 1 (initial swap)", got)
	}

	// TryRefresh exercises the full Refresher → TryRefresh → mcp.ReindexResult bridge.
	res := refresher.TryRefresh(ctx)
	if res.Err != nil {
		t.Errorf("TryRefresh returned err=%v", res.Err)
	}
	if res.InProgress {
		t.Errorf("TryRefresh during idle should NOT report InProgress")
	}
}

// TestSetup_NonPostgresWithEnableListen — EnableListen=true with a
// SQLite DSN is the "operator pre-set the flag" case. Start must
// NOT error; the listener silently no-ops with a warn log.
func TestSetup_NonPostgresWithEnableListen(t *testing.T) {
	dsn := emptySQLiteDSN(t)
	logBuf := &bytes.Buffer{}
	ctx := t.Context()

	refresher, err := Setup(ctx, Config{
		DSN:          dsn,
		EnableListen: true, // ignored for sqlite engine
		LogWriter:    logBuf,
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	cleanup, err := refresher.Start(ctx, func([]chunk.Chunk) {})
	if err != nil {
		t.Errorf("Start(EnableListen=true, sqlite DSN) should NOT error; got %v", err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	if !strings.Contains(logBuf.String(), "Postgres-only") {
		t.Errorf("expected 'Postgres-only' warn when EnableListen=true with SQLite DSN; got:\n%s", logBuf.String())
	}
}

// TestSetup_BothSchemaListsSet — when IncludeSchemas AND ExcludeSchemas
// are both populated, the allow-list wins and the deny-list is
// silently cleared. The warning surfaces via LogWriter. Matches
// cmd/ken-mcp's v0.7.2 behavior (ADR-019).
func TestSetup_BothSchemaListsSet(t *testing.T) {
	dsn := emptySQLiteDSN(t)
	logBuf := &bytes.Buffer{}
	ctx := t.Context()

	refresher, err := Setup(ctx, Config{
		DSN:            dsn,
		IncludeSchemas: []string{"public"},
		ExcludeSchemas: []string{"audit"},
		LogWriter:      logBuf,
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	cleanup, err := refresher.Start(ctx, func([]chunk.Chunk) {})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	if !strings.Contains(logBuf.String(), "allow-list wins") {
		t.Errorf("expected 'allow-list wins' warn when both schema lists are set; got:\n%s", logBuf.String())
	}
}

// TestListenNotifyScript_ReExports confirms mcp/db.ListenNotifyScript
// is the same bytes as internal/db.ListenNotifyScript (re-export
// invariant; catches a future refactor that accidentally changes one
// without the other).
func TestListenNotifyScript_ReExports(t *testing.T) {
	if ListenNotifyScript == "" {
		t.Fatal("mcpdb.ListenNotifyScript is empty — the re-export didn't load")
	}
	for _, want := range []string{
		"CREATE EVENT TRIGGER ken_schema_changed_trigger",
		"pg_notify('ken_schema_changed'",
		"DROP EVENT TRIGGER IF EXISTS",
		"COMMIT;",
	} {
		if !strings.Contains(ListenNotifyScript, want) {
			t.Errorf("mcpdb.ListenNotifyScript missing %q", want)
		}
	}
}

// TestRefresher_StartTwice rejects the second call (Refresher is
// one-shot per process). SDK authors who accidentally call Start
// twice see the error rather than silently leaking goroutines.
func TestRefresher_StartTwice(t *testing.T) {
	dsn := emptySQLiteDSN(t)
	ctx := t.Context()
	refresher, err := Setup(ctx, Config{DSN: dsn})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	cleanup, err := refresher.Start(ctx, func([]chunk.Chunk) {})
	if err != nil {
		t.Fatalf("first Start: %v", err)
	}
	defer cleanup()

	_, err = refresher.Start(ctx, func([]chunk.Chunk) {})
	if err == nil {
		t.Errorf("second Start should error (Refresher is one-shot); got nil")
	}
}

// TestRefresher_TryRefreshBeforeStart — calling TryRefresh on a
// Refresher that hasn't been Started yet returns ReindexResult.Err
// rather than panicking. mcp.Run's lifecycle calls Start before
// serving requests so this shouldn't fire in practice; defense-in-
// depth.
func TestRefresher_TryRefreshBeforeStart(t *testing.T) {
	refresher, err := Setup(context.Background(), Config{DSN: emptySQLiteDSN(t)})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	res := refresher.TryRefresh(context.Background())
	if res.Err == nil {
		t.Errorf("TryRefresh before Start should return ReindexResult.Err; got nil")
	}
}

// TestRefresher_NilReceiver — calling methods on a nil *Refresher
// (the "no DB" return from Setup) is safe. Start returns a no-op
// cleanup; TryRefresh returns ReindexResult.Err; Refresh returns
// an error. Same safety-net intent as the empty-DSN gate.
func TestRefresher_NilReceiver(t *testing.T) {
	var r *Refresher

	cleanup, err := r.Start(context.Background(), func([]chunk.Chunk) {})
	if err != nil {
		t.Errorf("(*Refresher)(nil).Start should not error; got %v", err)
	}
	if cleanup == nil {
		t.Errorf("(*Refresher)(nil).Start should return a non-nil cleanup; got nil")
	}

	res := r.TryRefresh(context.Background())
	if res.Err == nil {
		t.Errorf("(*Refresher)(nil).TryRefresh should return ReindexResult.Err")
	}

	if err := r.Refresh(context.Background()); err == nil {
		t.Errorf("(*Refresher)(nil).Refresh should error")
	}
}

// emptySQLiteDSN materializes a fresh empty .db file in t.TempDir()
// and returns its sqlite:// DSN.
func emptySQLiteDSN(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mcpdb-test.db")
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open temp sqlite: %v", err)
	}
	if err := conn.Ping(); err != nil {
		_ = conn.Close()
		t.Fatalf("ping temp sqlite: %v", err)
	}
	_ = conn.Close()
	return "sqlite://" + path
}

// TestRefresher_CleanupExitsOnCtxCancel — when ReindexInterval > 0,
// Start spawns the periodic-refresh goroutine. cleanup() (or ctx
// cancel) must let it exit cleanly. Tiny timeout guard catches hangs.
func TestRefresher_CleanupExitsOnCtxCancel(t *testing.T) {
	dsn := emptySQLiteDSN(t)
	ctx, cancel := context.WithCancel(context.Background())

	refresher, err := Setup(ctx, Config{
		DSN:             dsn,
		ReindexInterval: time.Hour, // never ticks during the test
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	cleanup, err := refresher.Start(ctx, func([]chunk.Chunk) {})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	cancel()
	done := make(chan struct{})
	go func() {
		cleanup()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup() hung for >2s; should be a fast no-op")
	}
}
