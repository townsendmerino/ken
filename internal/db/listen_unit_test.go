package db

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

// TestNewListener_NonPostgresRejected pins the engine-gate contract:
// NewListener returns ErrListenNotSupported (not a generic error) for
// SQLite, MySQL, and unknown-engine DSNs. cmd/ken-mcp distinguishes
// this case from real connection failures to choose between
// debug-and-skip vs warn-and-retry.
func TestNewListener_NonPostgresRejected(t *testing.T) {
	cases := []struct {
		name string
		dsn  string
	}{
		{"sqlite scheme", "sqlite:///tmp/dev.db"},
		{"sqlite3 scheme", "sqlite3:///tmp/dev.db"},
		{"mysql URL", "mysql://alice:s3cret@db.local:3306/mydb"},
		{"native MySQL tcp", "alice:s3cret@tcp(db.local:3306)/mydb"},
		{"native MySQL unix", "alice:s3cret@unix(/var/sock)/mydb"},
		{"unknown scheme", "redis://h:6379/0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := NewListener(Options{DSN: c.dsn}, func(context.Context) {})
			if !errors.Is(err, ErrListenNotSupported) {
				t.Errorf("NewListener(%q) error = %v, want ErrListenNotSupported", c.dsn, err)
			}
		})
	}
}

// TestNewListener_PostgresAccepted confirms postgres:// and
// postgresql:// schemes pass NewListener's gate. We do NOT connect
// (that's the integration test's job) — just verify the constructor
// returns a non-nil Listener.
func TestNewListener_PostgresAccepted(t *testing.T) {
	for _, dsn := range []string{
		"postgres://h/d",
		"postgresql://h/d",
		"postgres://alice:pass@db.local:5432/mydb?sslmode=disable",
	} {
		l, err := NewListener(Options{DSN: dsn}, func(context.Context) {})
		if err != nil {
			t.Errorf("NewListener(%q): %v", dsn, err)
			continue
		}
		if l == nil {
			t.Errorf("NewListener(%q) returned nil listener with nil error", dsn)
		}
	}
}

// TestNewListener_NilCallback rejects a nil onNotify — there's no
// sensible default behavior (refresh? log? nothing?), and the failure
// mode of "listener spinning and silently dropping notifications" is
// the worst possible silent surprise.
func TestNewListener_NilCallback(t *testing.T) {
	_, err := NewListener(Options{DSN: "postgres://h/d"}, nil)
	if err == nil {
		t.Errorf("NewListener with nil onNotify should error; got nil")
	}
}

// TestNewListener_NilLogWriterDoesNotPanic confirms a missing
// LogWriter falls back to io.Discard (NOT os.Stdout — that's the
// JSON-RPC channel for cmd/ken-mcp). Defense-in-depth against a caller
// who forgets to wire a writer.
func TestNewListener_NilLogWriterDoesNotPanic(t *testing.T) {
	l, err := NewListener(Options{DSN: "postgres://h/d", LogWriter: nil}, func(context.Context) {})
	if err != nil {
		t.Fatalf("NewListener: %v", err)
	}
	if l.logger == nil {
		t.Errorf("listener.logger is nil; should default to io.Discard")
	}
}

// TestListenNotifyScript_ContainsEssentials confirms the embedded SQL
// script names the channel + trigger objects the runtime code expects.
// Catches drift between the script and the listener's queries (the
// trigger name + channel name are pinned in listen.go as constants).
func TestListenNotifyScript_ContainsEssentials(t *testing.T) {
	if ListenNotifyScript == "" {
		t.Fatal("ListenNotifyScript is empty — go:embed didn't load the file")
	}
	for _, want := range []string{
		"CREATE EVENT TRIGGER " + triggerName,
		"pg_notify('" + notifyChannel + "'",
		"ddl_command_end",
		"CREATE TABLE", "ALTER TABLE", "DROP TABLE", // representative DDL coverage
	} {
		if !bytes.Contains([]byte(ListenNotifyScript), []byte(want)) {
			t.Errorf("ListenNotifyScript missing %q", want)
		}
	}
}
