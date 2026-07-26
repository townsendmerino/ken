package db

import (
	"context"
	"strings"
	"testing"
)

// TestNewListener_UnparseableDSN_NoLeak: a DSN that pgx.ParseConfig rejects
// must yield a GENERIC error from NewListener — never one wrapping the DSN
// (audit db/mcp #7). Before the fix the raw DSN reached pgx.Connect on every
// reconnect, so a parse failure printed it — password included — into the
// stderr reconnect loop forever. This guards the third occurrence of the M5
// "no raw DSN in errors" policy (db.go, mysql.go being the first two).
func TestNewListener_UnparseableDSN_NoLeak(t *testing.T) {
	dsn := "postgres://ken_svc:hunter2@prod-pg.internal/billing?connect_timeout=abc"
	_, err := NewListener(Options{DSN: dsn}, func(context.Context) {})
	if err == nil {
		t.Fatal("expected an error for an unparseable postgres DSN")
	}
	for _, leak := range []string{"hunter2", "ken_svc", "prod-pg.internal", "billing"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("NewListener error leaked %q: %q", leak, err.Error())
		}
	}
}
