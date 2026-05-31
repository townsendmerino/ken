//go:build dbintegration

// Integration tests for internal/db, gated by build tag `dbintegration`.
// Default `go test ./...` skips this file entirely so contributors
// without Postgres aren't slowed down.
//
// Run with:
//
//	docker run --rm -d --name ken-test-pg -e POSTGRES_PASSWORD=test \
//	  -p 55432:5432 postgres:16-alpine
//	export KEN_DB_TEST_DSN="postgres://postgres:test@127.0.0.1:55432/postgres?sslmode=disable"
//	go test -tags=dbintegration ./internal/db/
//
// The tests load testdata/init.sql to create a known schema, then
// introspect and assert chunk shapes. Each test re-runs init.sql so
// they're independent.
package db

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/townsendmerino/aikit/chunk"
)

func dsnOrSkip(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("KEN_DB_TEST_DSN")
	if dsn == "" {
		t.Skip("KEN_DB_TEST_DSN not set; see internal/db/integration_test.go for setup")
	}
	return dsn
}

func loadFixture(t *testing.T, ctx context.Context, dsn string) {
	t.Helper()
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer conn.Close(ctx)

	data, err := os.ReadFile("testdata/init.sql")
	if err != nil {
		t.Fatalf("read init.sql: %v", err)
	}
	// pgx.Exec doesn't multi-statement; use the simple protocol via Exec
	// of the full script (Postgres accepts multi-statement strings via
	// the simple protocol). pgx's Conn.Exec uses the extended protocol
	// which forbids multiple statements; sending via a raw query through
	// the simple protocol works with conn.Exec when no parameters are
	// involved.
	if _, err := conn.Exec(ctx, string(data)); err != nil {
		t.Fatalf("init.sql exec: %v", err)
	}
}

// TestIntegration_IntrospectsKnownSchema is the load-bearing test:
// after loading init.sql, IndexSchema must produce chunks covering
// every shape (table with PK + FK + UNIQUE + DEFAULT + INDEX, view,
// function).
func TestIntegration_IntrospectsKnownSchema(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	dsn := dsnOrSkip(t)
	loadFixture(t, ctx, dsn)

	logBuf := &bytes.Buffer{}
	chunks, err := IndexSchema(ctx, Options{
		DSN:       dsn,
		LogWriter: logBuf,
	})
	if err != nil {
		t.Fatalf("IndexSchema: %v\n--log--\n%s", err, logBuf.String())
	}
	if len(chunks) == 0 {
		t.Fatalf("IndexSchema produced 0 chunks")
	}

	// Locate the expected chunks by their distinctive markers.
	tableUsers := mustFindChunk(t, chunks, "TABLE ken_test.users")
	tableSessions := mustFindChunk(t, chunks, "TABLE ken_test.sessions")
	view := mustFindChunk(t, chunks, "VIEW ken_test.active_users")
	fn := mustFindChunk(t, chunks, "FUNCTION ken_test.greet")

	// users table assertions.
	for _, want := range []string{
		"email  varchar(255)  NOT NULL UNIQUE",
		"role  varchar(32)  NOT NULL DEFAULT",
		"INDEX users_email_idx ON (email)",
		"FK referenced by: ken_test.sessions(user_id)",
	} {
		if !strings.Contains(tableUsers.Text, want) {
			t.Errorf("users chunk missing %q:\n%s", want, tableUsers.Text)
		}
	}

	// sessions table assertions — confirms FK forward-direction (→) is rendered.
	if !strings.Contains(tableSessions.Text, "user_id  bigint  NOT NULL → ken_test.users(id)") {
		t.Errorf("sessions chunk missing forward FK arrow:\n%s", tableSessions.Text)
	}

	// View body shows up (truncation should NOT trigger for a 5-line view).
	if !strings.Contains(view.Text, "LEFT JOIN ken_test.sessions") {
		t.Errorf("view chunk missing body content:\n%s", view.Text)
	}
	if strings.Contains(view.Text, "view body truncated") {
		t.Errorf("view body unexpectedly truncated:\n%s", view.Text)
	}

	// Function signature only — no body.
	if !strings.Contains(fn.Text, "FUNCTION ken_test.greet(name text) RETURNS text") {
		t.Errorf("function chunk missing signature:\n%s", fn.Text)
	}
	if strings.Contains(fn.Text, "SELECT 'hello'") {
		t.Errorf("function chunk should NOT contain body:\n%s", fn.Text)
	}

	// Freshness header on every chunk.
	for _, c := range chunks {
		if !strings.HasPrefix(c.Text, "-- indexed at ") {
			t.Errorf("chunk missing freshness header (path=%s):\n%s", c.File, c.Text)
		}
		// Defense-in-depth: never any credential bytes.
		for _, danger := range []string{"postgres:test", "password=", "pass:"} {
			if strings.Contains(c.Text, danger) {
				t.Errorf("chunk leaked credential %q (path=%s)", danger, c.File)
			}
		}
	}

	// Synthetic DB path shape.
	for _, c := range chunks {
		if !strings.HasPrefix(c.File, "db://postgres@") {
			t.Errorf("chunk path should start with db://postgres@; got %s", c.File)
		}
	}
}

// TestIntegration_RowSamplingDeterministic confirms two consecutive
// IndexSchema runs (with SampleRows=3) produce identical sample-row
// content. Determinism comes from ORDER BY first-PK-column; the
// fixture's deterministic INSERTs make the expected rows known.
func TestIntegration_RowSamplingDeterministic(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	dsn := dsnOrSkip(t)
	loadFixture(t, ctx, dsn)

	opts := Options{DSN: dsn, SampleRows: 3, LogWriter: &bytes.Buffer{}}
	chunksA, err := IndexSchema(ctx, opts)
	if err != nil {
		t.Fatalf("first IndexSchema: %v", err)
	}
	chunksB, err := IndexSchema(ctx, opts)
	if err != nil {
		t.Fatalf("second IndexSchema: %v", err)
	}

	users := mustFindChunk(t, chunksA, "TABLE ken_test.users")
	// First, confirm rows showed up at all.
	if !strings.Contains(users.Text, "Sample rows (3") {
		t.Fatalf("expected 'Sample rows (3' in users chunk; got:\n%s", users.Text)
	}
	for _, want := range []string{"alice@example.com", "bob@example.com", "claire@example.com"} {
		if !strings.Contains(users.Text, want) {
			t.Errorf("expected %q in sampled rows:\n%s", want, users.Text)
		}
	}

	// Compare two consecutive runs body-for-body except for the
	// freshness header (which is the only thing allowed to vary).
	stripHeader := func(s string) string {
		// Drop the first line if it matches the freshness header pattern.
		if i := strings.Index(s, "\n"); i >= 0 && strings.HasPrefix(s, "-- indexed at ") {
			return s[i+1:]
		}
		return s
	}
	usersA := stripHeader(mustFindChunk(t, chunksA, "TABLE ken_test.users").Text)
	usersB := stripHeader(mustFindChunk(t, chunksB, "TABLE ken_test.users").Text)
	if usersA != usersB {
		t.Errorf("non-deterministic users chunk between two consecutive runs:\n--A--\n%s\n--B--\n%s", usersA, usersB)
	}
}

// TestIntegration_SchemaOnlyWhenSampleRowsZero confirms the default
// (SampleRows=0) path NEVER emits row data.
func TestIntegration_SchemaOnlyWhenSampleRowsZero(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	dsn := dsnOrSkip(t)
	loadFixture(t, ctx, dsn)

	chunks, err := IndexSchema(ctx, Options{DSN: dsn})
	if err != nil {
		t.Fatalf("IndexSchema: %v", err)
	}
	users := mustFindChunk(t, chunks, "TABLE ken_test.users")
	if strings.Contains(users.Text, "Sample rows") {
		t.Errorf("SampleRows=0 should not emit any sample rows, got:\n%s", users.Text)
	}
	// And no row data should appear anywhere.
	for _, danger := range []string{"alice@example.com", "bob@example.com", "claire@example.com"} {
		if strings.Contains(users.Text, danger) {
			t.Errorf("row value %q leaked into schema-only chunk", danger)
		}
	}
}

// mustFindChunk returns the first chunk whose Text contains needle;
// fails the test if none match.
func mustFindChunk(t *testing.T, chunks []chunk.Chunk, needle string) chunk.Chunk {
	t.Helper()
	for _, c := range chunks {
		if strings.Contains(c.Text, needle) {
			return c
		}
	}
	t.Fatalf("no chunk contained %q; got %d chunks", needle, len(chunks))
	return chunk.Chunk{}
}
