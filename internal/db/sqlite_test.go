//go:build dbintegration

// SQLite integration tests for internal/db, gated by build tag
// `dbintegration`. Unlike the Postgres integration tests, these create
// their own temp .db file at runtime so no external service container
// is required.
//
// Run with:
//
//	go test -tags=dbintegration ./internal/db/ -run SQLite

package db

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// makeSQLiteFixture creates a temp .db file, applies a known schema,
// returns the file path.
func makeSQLiteFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "test.db")

	conn, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer conn.Close()

	stmts := []string{
		`CREATE TABLE users (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            email TEXT NOT NULL UNIQUE,
            created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
        )`,
		`CREATE TABLE sessions (
            id INTEGER PRIMARY KEY,
            user_id INTEGER NOT NULL REFERENCES users(id),
            token TEXT NOT NULL UNIQUE
        )`,
		`CREATE INDEX sessions_user_id_idx ON sessions(user_id)`,
		`CREATE VIEW active_users AS SELECT id, email FROM users WHERE created_at > '2024-01-01'`,
		`INSERT INTO users (email) VALUES ('alice@example.com'), ('bob@example.com')`,
		`INSERT INTO sessions (id, user_id, token) VALUES (1, 1, 't-aaa'), (2, 2, 't-bbb')`,
	}
	for _, s := range stmts {
		if _, err := conn.Exec(s); err != nil {
			t.Fatalf("exec %q: %v", s, err)
		}
	}
	return file
}

// TestSQLiteIntegration_IntrospectsKnownSchema asserts the SQLite engine
// produces the same chunk shape as Postgres for a known schema.
func TestSQLiteIntegration_IntrospectsKnownSchema(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	file := makeSQLiteFixture(t)
	logBuf := &bytes.Buffer{}
	chunks, err := IndexSchema(ctx, Options{
		DSN:       "sqlite://" + file,
		LogWriter: logBuf,
	})
	if err != nil {
		t.Fatalf("IndexSchema: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatalf("expected chunks, got 0; log: %s", logBuf.String())
	}

	var sawUsers, sawSessions, sawIndex, sawFK, sawView bool
	for _, c := range chunks {
		txt := c.Text
		if !strings.Contains(txt, "-- indexed at ") || !strings.Contains(txt, "from sqlite@") {
			t.Errorf("chunk missing freshness header:\n%s", txt)
		}
		switch {
		case strings.Contains(txt, "TABLE users"):
			sawUsers = true
			if !strings.Contains(txt, "id") || !strings.Contains(txt, "PK") {
				t.Errorf("users chunk missing id PK:\n%s", txt)
			}
			if !strings.Contains(txt, "email") {
				t.Errorf("users chunk missing email:\n%s", txt)
			}
		case strings.Contains(txt, "TABLE sessions"):
			sawSessions = true
			if strings.Contains(txt, "INDEX sessions_user_id_idx") {
				sawIndex = true
			}
			if strings.Contains(txt, "→ users(id)") {
				sawFK = true
			}
		case strings.Contains(txt, "VIEW active_users"):
			sawView = true
		}
	}
	if !sawUsers {
		t.Error("no chunk for TABLE users")
	}
	if !sawSessions {
		t.Error("no chunk for TABLE sessions")
	}
	if !sawIndex {
		t.Error("sessions chunk missing the sessions_user_id_idx index")
	}
	if !sawFK {
		t.Error("sessions chunk missing the FK → users(id) annotation")
	}
	if !sawView {
		t.Error("no chunk for VIEW active_users")
	}
}

// TestSQLiteIntegration_RowSampling — opt-in row sampling fills the
// "Sample rows" block on the table chunks.
func TestSQLiteIntegration_RowSampling(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	file := makeSQLiteFixture(t)
	chunks, err := IndexSchema(ctx, Options{
		DSN:        "sqlite://" + file,
		SampleRows: 5,
		LogWriter:  &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("IndexSchema: %v", err)
	}
	var foundSample bool
	for _, c := range chunks {
		if strings.Contains(c.Text, "TABLE users") && strings.Contains(c.Text, "Sample rows") {
			foundSample = true
			if !strings.Contains(c.Text, "alice@example.com") && !strings.Contains(c.Text, "bob@example.com") {
				t.Errorf("sample rows missing seeded data:\n%s", c.Text)
			}
		}
	}
	if !foundSample {
		t.Error("expected Sample rows block on users chunk")
	}
}

// TestSQLiteIntegration_FreshnessHeader_BasenameOnly — the header
// includes the file basename, not the full path, so chunks don't leak
// the local filesystem layout.
func TestSQLiteIntegration_FreshnessHeader_BasenameOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	file := makeSQLiteFixture(t)
	chunks, err := IndexSchema(ctx, Options{
		DSN:       "sqlite://" + file,
		LogWriter: &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("IndexSchema: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("no chunks")
	}
	for _, c := range chunks {
		if strings.Contains(c.Text, file) {
			t.Errorf("chunk text leaked full file path %q:\n%s", file, c.Text)
			return
		}
		// Basename only.
		if !strings.Contains(c.Text, "sqlite@"+filepath.Base(file)) {
			t.Errorf("chunk missing 'sqlite@<basename>' header:\n%s", c.Text)
			return
		}
	}
}

// TestSQLiteIntegration_RelativeDSNResolution — `sqlite://./test.db`
// resolves against Options.DefaultRepoPath.
func TestSQLiteIntegration_RelativeDSNResolution(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	dir := t.TempDir()
	// Build the fixture at <dir>/test.db.
	conn, err := sql.Open("sqlite", filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`CREATE TABLE only_one (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	conn.Close()

	chunks, err := IndexSchema(ctx, Options{
		DSN:             "sqlite://./test.db",
		DefaultRepoPath: dir,
		LogWriter:       &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("IndexSchema with relative DSN: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("expected chunks; got 0")
	}
	if !strings.Contains(chunks[0].Text, "TABLE only_one") {
		t.Errorf("expected TABLE only_one chunk; got:\n%s", chunks[0].Text)
	}
}
