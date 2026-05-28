//go:build dbintegration

// Integration tests for v0.8.0 Part 2 reindex_db tool's end-to-end
// path through Refresher.TryRefresh (ADR-020 Part 2). Reuses the
// helpers from integration_test.go (dsnOrSkip) and
// listen_integration_test.go (execDDL, uniqueTableName, safeBuffer).
package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/townsendmerino/ken/chunk"
)

// TestReindexDB_IntegrationE2E exercises the same code path the
// reindex_db MCP tool drives in production: build a Refresher, call
// TryRefresh after a DDL change, observe the freshly-indexed chunks
// in the swap callback. Mirrors listen_integration_test.go's
// table-setup pattern (per-test unique name + cleanup) so parallel
// integration runs don't collide.
//
// The chunks-after-reindex assertion uses an atomic.Pointer that the
// swap callback writes — production wires this to WatchedIndex.
// SetExtraChunks; the test wires it to a captured pointer so the
// assertion can inspect what got swapped.
func TestReindexDB_IntegrationE2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	dsn := dsnOrSkip(t)

	// Per-test table names + cleanup. The ken_test schema may or may
	// not exist depending on whether a prior loadFixture ran; using a
	// freshly-created table in the public schema means no shared-state
	// dependency between this and other integration tests.
	table := uniqueTableName("reindex_e2e")
	t.Cleanup(func() {
		execDDL(t, context.Background(), dsn, fmt.Sprintf("DROP TABLE IF EXISTS %s", table))
	})

	// Capture-the-swap shim: swap is what the Refresher calls after a
	// successful IndexSchema. The latest-pushed chunks are stored in
	// the atomic pointer so the test can inspect them post-call.
	var latest atomic.Pointer[[]chunk.Chunk]
	refresher, err := NewRefresher(
		Options{DSN: dsn, LogWriter: &noopWriter{}, IncludeSchemas: []string{"public"}},
		func(cs []chunk.Chunk) { latest.Store(&cs) },
	)
	if err != nil {
		t.Fatalf("NewRefresher: %v", err)
	}

	// Step 1: create the table and TryRefresh; the swap should fire
	// with a chunk set containing the new TABLE.
	execDDL(t, ctx, dsn, fmt.Sprintf("CREATE TABLE %s (id bigint PRIMARY KEY, email varchar(255) NOT NULL)", table))
	if err := refresher.TryRefresh(ctx); err != nil {
		t.Fatalf("first TryRefresh after CREATE: %v", err)
	}
	chunks := loadLatest(t, &latest)
	if !containsTable(chunks, table, "email", "varchar(255)") {
		t.Fatalf("post-CREATE chunks missing table %q with email varchar(255):\n%s",
			table, joinChunkText(chunks))
	}

	// Step 2: alter the table and TryRefresh; the swap should fire
	// with chunks reflecting the new column.
	execDDL(t, ctx, dsn, fmt.Sprintf(
		"ALTER TABLE %s ADD COLUMN email_verified boolean NOT NULL DEFAULT false",
		table,
	))
	if err := refresher.TryRefresh(ctx); err != nil {
		t.Fatalf("second TryRefresh after ALTER: %v", err)
	}
	chunks = loadLatest(t, &latest)
	if !containsTable(chunks, table, "email_verified", "boolean") {
		t.Fatalf("post-ALTER chunks missing email_verified column:\n%s",
			joinChunkText(chunks))
	}
}

// TestReindexDB_IntegrationInProgress confirms the in-flight lock
// holds end-to-end: a slow Refresh on one goroutine + a concurrent
// TryRefresh sees ErrReindexInProgress. This is the same shape as the
// unit test but against a real Postgres so we catch any pgx-level
// interaction with mutex-while-querying that the unit test (which
// uses an empty SQLite file) might miss.
func TestReindexDB_IntegrationInProgress(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	dsn := dsnOrSkip(t)

	releaseSwap := make(chan struct{})
	swapEntered := make(chan struct{}, 1)
	refresher, err := NewRefresher(
		Options{DSN: dsn, LogWriter: &noopWriter{}, IncludeSchemas: []string{"public"}},
		func(cs []chunk.Chunk) {
			select {
			case swapEntered <- struct{}{}:
			default:
			}
			<-releaseSwap
		},
	)
	if err != nil {
		t.Fatalf("NewRefresher: %v", err)
	}

	refreshDone := make(chan error, 1)
	go func() { refreshDone <- refresher.Refresh(ctx) }()

	select {
	case <-swapEntered:
	case <-time.After(10 * time.Second):
		close(releaseSwap)
		t.Fatal("blocking Refresh's swap callback did not run within 10s")
	}

	// Concurrent TryRefresh against the live DSN MUST fail fast with
	// ErrReindexInProgress. If the mutex behavior is somehow different
	// when pgx is involved (it shouldn't be — TryLock is a stdlib
	// primitive), this is the test that catches it.
	start := time.Now()
	tryErr := refresher.TryRefresh(ctx)
	elapsed := time.Since(start)
	close(releaseSwap)
	<-refreshDone

	if !errors.Is(tryErr, ErrReindexInProgress) {
		t.Fatalf("TryRefresh during in-flight refresh against live DB = %v, want ErrReindexInProgress", tryErr)
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("TryRefresh blocked for %v against live DB; should fail fast (≤100ms)", elapsed)
	}
}

func loadLatest(t *testing.T, p *atomic.Pointer[[]chunk.Chunk]) []chunk.Chunk {
	t.Helper()
	cs := p.Load()
	if cs == nil {
		t.Fatal("swap callback never fired (nil pointer)")
	}
	return *cs
}

// containsTable reports whether the chunk set has a TABLE chunk for
// the named table whose text mentions all the markers (typically
// "column-name + type" pairs to confirm the post-DDL schema).
func containsTable(chunks []chunk.Chunk, table string, markers ...string) bool {
	want := "TABLE " + table
	for _, c := range chunks {
		if !strings.Contains(c.Text, want) {
			continue
		}
		ok := true
		for _, m := range markers {
			if !strings.Contains(c.Text, m) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func joinChunkText(chunks []chunk.Chunk) string {
	var b strings.Builder
	for _, c := range chunks {
		b.WriteString(c.Text)
		b.WriteByte('\n')
	}
	return b.String()
}
