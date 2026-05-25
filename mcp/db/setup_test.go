package mcpdb

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite" // empty-sqlite-file harness for the happy-path tests
)

// TestSetup_EmptyDSN_ReturnsNil pins the documented safety net: an
// SDK author who conditionally configures DSN can call Setup
// unconditionally; an empty DSN returns (nil, nil, nil) so the caller
// just doesn't wire opts.Reindex.
func TestSetup_EmptyDSN_ReturnsNil(t *testing.T) {
	reindex, cleanup, err := Setup(context.Background(), Config{})
	if err != nil {
		t.Errorf("Setup with empty DSN should not error; got %v", err)
	}
	if reindex != nil {
		t.Errorf("Setup with empty DSN should return nil reindex; got non-nil")
	}
	if cleanup != nil {
		t.Errorf("Setup with empty DSN should return nil cleanup; got non-nil")
	}
}

// TestSetup_InvalidDSN_ReturnsError — a DSN with a scheme that doesn't
// route to any known engine fails at IndexSchema time. Setup wraps the
// error rather than panicking.
func TestSetup_InvalidDSN_ReturnsError(t *testing.T) {
	_, _, err := Setup(context.Background(), Config{
		DSN:       "redis://nope:6379/0",
		LogWriter: &bytes.Buffer{},
	})
	if err == nil {
		t.Errorf("Setup with bogus DSN scheme should error")
	}
	if !strings.Contains(err.Error(), "mcp/db.Setup") {
		t.Errorf("error should be wrapped with mcp/db.Setup prefix; got %v", err)
	}
}

// TestSetup_HappyPath — empty SQLite DSN routes through to a clean
// IndexSchema (zero chunks), Setup returns a non-nil ReindexFunc + a
// non-nil cleanup func, and the onSwap log line names the v0.9.0
// chunk-integration deferral so SDK authors / operators see the
// limitation at runtime.
func TestSetup_HappyPath(t *testing.T) {
	dsn := emptySQLiteDSN(t)
	logBuf := &bytes.Buffer{}

	ctx := t.Context()

	reindex, cleanup, err := Setup(ctx, Config{
		DSN:       dsn,
		LogWriter: logBuf,
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if reindex == nil {
		t.Fatal("Setup happy path returned nil reindex")
	}
	if cleanup == nil {
		t.Fatal("Setup happy path returned nil cleanup")
	}
	defer cleanup()

	// Onswap log line confirms the chunk-integration deferral is
	// surfaced to operators at runtime (not just in the docs).
	if !strings.Contains(logBuf.String(), "deferred to v0.9.0") {
		t.Errorf("Setup's onSwap log should name the v0.9.0 deferral; got:\n%s", logBuf.String())
	}

	// Calling the returned ReindexFunc exercises the full
	// Refresher → TryRefresh → mcp.ReindexResult bridge.
	res := reindex(ctx)
	if res.Err != nil {
		t.Errorf("reindex() returned err=%v", res.Err)
	}
	if res.InProgress {
		t.Errorf("reindex() during idle should NOT report InProgress")
	}
}

// TestSetup_NonPostgresWithEnableListen — passing EnableListen=true
// with a SQLite DSN is the "operator pre-set the flag" case from Part 1.
// Setup must NOT error; the listener silently no-ops with a warn log.
func TestSetup_NonPostgresWithEnableListen(t *testing.T) {
	dsn := emptySQLiteDSN(t)
	logBuf := &bytes.Buffer{}
	ctx := t.Context()

	_, cleanup, err := Setup(ctx, Config{
		DSN:          dsn,
		EnableListen: true, // ignored for sqlite engine
		LogWriter:    logBuf,
	})
	if err != nil {
		t.Errorf("Setup(EnableListen=true, sqlite DSN) should NOT error; got %v", err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	// The internal/db.SetupTier2 warn() helper writes the "ignored"
	// line via opts.LogWriter — which we passed as logBuf.
	if !strings.Contains(logBuf.String(), "Postgres-only") {
		t.Errorf("expected 'Postgres-only' warn in log when EnableListen=true with SQLite DSN; got:\n%s", logBuf.String())
	}
}

// TestSetup_BothSchemaListsSet — when IncludeSchemas AND ExcludeSchemas
// are both populated, the allow-list wins and the deny-list is
// silently cleared. The warning surfaces via LogWriter so SDK authors
// see the conflict at runtime. Matches cmd/ken-mcp's identical
// behavior in v0.7.2 (ADR-019).
func TestSetup_BothSchemaListsSet(t *testing.T) {
	dsn := emptySQLiteDSN(t)
	logBuf := &bytes.Buffer{}
	ctx := t.Context()

	_, cleanup, err := Setup(ctx, Config{
		DSN:            dsn,
		IncludeSchemas: []string{"public"},
		ExcludeSchemas: []string{"audit"},
		LogWriter:      logBuf,
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
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

// emptySQLiteDSN materializes a fresh empty .db file in t.TempDir()
// and returns its sqlite:// DSN. IndexSchema returns no chunks against
// it (no tables, no errors), making it a clean substrate for unit
// tests that need a working engine without a Postgres / MySQL service
// container.
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

// TestSetup_CleanupExitsOnCtxCancel — when ReindexInterval > 0, Setup
// spawns the periodic-refresh goroutine. cleanup() (or ctx cancel)
// must let it exit cleanly without leaking. We use a tiny timeout
// guard around the cleanup to catch hangs.
func TestSetup_CleanupExitsOnCtxCancel(t *testing.T) {
	dsn := emptySQLiteDSN(t)
	ctx, cancel := context.WithCancel(context.Background())

	_, cleanup, err := Setup(ctx, Config{
		DSN:             dsn,
		ReindexInterval: time.Hour, // never ticks during the test
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	cancel()
	// Cleanup is currently a no-op but reserved as a future seam;
	// invoke it to exercise the contract.
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
