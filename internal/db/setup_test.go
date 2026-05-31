package db

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/townsendmerino/aikit/chunk"
)

// TestSetupTier2_RequiresDSN — Tier 2 is disabled when DSN is empty;
// SetupTier2 callers should be the ones checking that and skipping
// the call entirely. Returning an error (rather than nil-nil-nil)
// catches the programming-error case where a caller invokes
// SetupTier2 with an empty Options.
func TestSetupTier2_RequiresDSN(t *testing.T) {
	_, _, err := SetupTier2(context.Background(), Options{}, false, func([]chunk.Chunk) {})
	if err == nil {
		t.Errorf("SetupTier2 with empty DSN should error")
	}
}

// TestSetupTier2_RequiresOnSwap — onSwap is required; passing nil is
// a programming error (the caller forgot to wire their snapshot
// target). Catch early.
func TestSetupTier2_RequiresOnSwap(t *testing.T) {
	_, _, err := SetupTier2(context.Background(), Options{DSN: "sqlite:///tmp/x.db"}, false, nil)
	if err == nil {
		t.Errorf("SetupTier2 with nil onSwap should error")
	}
}

// TestSetupTier2_HappyPath — empty SQLite DSN routes through to a
// successful IndexSchema (zero chunks), onSwap fires with the empty
// slice, Refresher constructed. Cleanup returns immediately on ctx
// cancel.
func TestSetupTier2_HappyPath(t *testing.T) {
	dsn := emptySQLiteDSN(t)

	var swapCalls atomic.Int32
	onSwap := func([]chunk.Chunk) { swapCalls.Add(1) }

	ctx := t.Context()
	refresher, cleanup, err := SetupTier2(ctx, Options{DSN: dsn}, false, onSwap)
	if err != nil {
		t.Fatalf("SetupTier2: %v", err)
	}
	if refresher == nil {
		t.Fatal("SetupTier2 returned nil refresher")
	}
	if cleanup == nil {
		t.Fatal("SetupTier2 returned nil cleanup")
	}
	if got := swapCalls.Load(); got != 1 {
		t.Errorf("onSwap call count after SetupTier2 = %d, want 1 (initial IndexSchema swap)", got)
	}
	cleanup()
}

// TestSetupTier2_IndexSchemaFailureReturnsError — when the DSN routes
// to an engine that can't connect (here: a non-existent SQLite file
// path), IndexSchema fails and SetupTier2 surfaces the error rather
// than constructing a half-initialized Refresher. Caller skips Tier 2.
func TestSetupTier2_IndexSchemaFailureReturnsError(t *testing.T) {
	// Pointing at a path that doesn't exist — sqlite engine's os.Stat
	// will fail upfront.
	bogus := "sqlite:///nonexistent-dir-for-setuptier2-test/missing.db"
	_, _, err := SetupTier2(context.Background(), Options{DSN: bogus}, false, func([]chunk.Chunk) {})
	if err == nil {
		t.Errorf("SetupTier2 with unreachable DSN should return error")
	}
	if !strings.Contains(err.Error(), "IndexSchema") {
		t.Errorf("error should name IndexSchema as the failure point; got %v", err)
	}
}

// TestSetupTier2_EnableListenSilentlyNoOpsForNonPostgres — passing
// enableListen=true with a SQLite DSN logs a debug message but doesn't
// fail; matches cmd/ken-mcp's KEN_DB_LISTEN=1 + sqlite DSN behavior.
// SetupTier2 returns successfully; no listener goroutine starts.
func TestSetupTier2_EnableListenSilentlyNoOpsForNonPostgres(t *testing.T) {
	dsn := emptySQLiteDSN(t)
	ctx := t.Context()

	refresher, _, err := SetupTier2(ctx, Options{DSN: dsn}, true /* enableListen */, func([]chunk.Chunk) {})
	if err != nil {
		t.Fatalf("SetupTier2(enableListen=true, sqlite DSN) should NOT error; got %v", err)
	}
	if refresher == nil {
		t.Fatal("expected non-nil refresher")
	}
	// We can't directly observe "listener didn't start" (goroutines are
	// opaque), but if a goroutine was spawned and panicked on a non-
	// Postgres DSN, the test would crash. Reaching here cleanly is the
	// implicit pass condition.
}

// TestSetupTier2_IntervalGoroutineExitsOnCtxCancel — when
// ReindexInterval > 0, the periodic-refresh goroutine starts. Canceling
// ctx must cause it to exit cleanly without panic or leak.
func TestSetupTier2_IntervalGoroutineExitsOnCtxCancel(t *testing.T) {
	dsn := emptySQLiteDSN(t)
	ctx, cancel := context.WithCancel(context.Background())

	_, _, err := SetupTier2(ctx,
		Options{DSN: dsn, ReindexInterval: time.Hour}, // never actually ticks during the test
		false,
		func([]chunk.Chunk) {},
	)
	if err != nil {
		t.Fatalf("SetupTier2: %v", err)
	}
	// Cancel; give the goroutine a beat to notice.
	cancel()
	time.Sleep(50 * time.Millisecond)
	// goroutine leak detection would catch a hung Run() — we can't
	// goroutine-leak-test from here cheaply, but the t.Cleanup default
	// catches obvious deadlocks.
}
