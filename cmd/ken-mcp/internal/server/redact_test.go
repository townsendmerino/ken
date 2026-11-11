package server

import (
	"strings"
	"testing"
)

// TestRedactDSN pins the M1 fix — redactDSN must scrub credentials
// from every DSN shape envDSN accepts, including the native
// go-sql-driver/mysql form that has no `://` scheme. Pre-fix, the
// native form's userinfo survived a no-op `url.Parse` + `u.User =
// nil` round-trip and the password landed in the startup log on
// stderr.
func TestRedactDSN(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// URL form with userinfo: scheme + path preserved, userinfo stripped.
		{"postgres URL", "postgres://alice:s3cret@host/db", "postgres://host/db"},
		{"postgres URL with port + sslmode", "postgres://alice:s3cret@host:5432/db?sslmode=disable", "postgres://host:5432/db?sslmode=disable"},
		// mysql:// + tcp(...) doesn't pass url.Parse cleanly (parens
		// in the authority), so the fail-safe <redacted> branch fires.
		// That's still safe — the password doesn't leak.
		{"mysql URL form with parens", "mysql://alice:s3cret@tcp(host:3306)/db", "<redacted>"},
		// Native go-sql-driver/mysql form: no `://`; strip up to the LAST
		// '@' before the last '/' (mirrors the driver — '@', ':' and '/'
		// are all legal in a password; audit db/mcp #2).
		{"native MySQL TCP", "alice:s3cret@tcp(host:3306)/db?parseTime=true", "tcp(host:3306)/db?parseTime=true"},
		{"native MySQL unix socket", "alice:s3cret@unix(/var/run/mysqld.sock)/db", "unix(/var/run/mysqld.sock)/db"},
		// '@' inside the password (the audit db/mcp #2 leak): the FIRST-'@'
		// cut returned `s05cret!@tcp(...)`, leaking the tail.
		{"@ in password", "svc:s3cret@!@tcp(db.internal:3306)/app", "tcp(db.internal:3306)/app"},
		{"two @ in password", "svc:a@b@c@tcp(h:3306)/app", "tcp(h:3306)/app"},
		{"@ in password, unix socket path has /", "svc:s3cret@x@unix(/var/run/m.sock)/db", "unix(/var/run/m.sock)/db"},
		{": in password", "svc:s3:cret@tcp(h:3306)/db", "tcp(h:3306)/db"},
		// SQLite: no userinfo to redact; unchanged.
		{"sqlite three-slash", "sqlite:///tmp/test.db", "sqlite:///tmp/test.db"},
		// Edge case: input contains '@' but no scheme — treat as native form.
		{"naked user@host", "user@host", "host"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactDSN(tc.in)
			if got != tc.want {
				t.Errorf("redactDSN(%q) = %q, want %q", tc.in, got, tc.want)
			}
			// Belt-and-suspenders: the canonical bad password must
			// never survive in the output.
			if strings.Contains(got, "s3cret") {
				t.Errorf("redactDSN(%q) leaked the password: %q", tc.in, got)
			}
		})
	}
}
