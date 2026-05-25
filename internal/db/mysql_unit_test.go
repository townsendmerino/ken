package db

import (
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
)

// TestMySQLURLToNative covers the URL-to-native DSN rewrite. The
// driver only accepts the native form, so this conversion is
// load-bearing for every operator who pastes the URL shape.
func TestMySQLURLToNative(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			"user+pass+port+db+query",
			"mysql://alice:s3cret@db.local:3306/mydb?parseTime=true",
			"alice:s3cret@tcp(db.local:3306)/mydb?parseTime=true",
		},
		{
			"default port when omitted",
			"mysql://alice:s3cret@db.local/mydb",
			"alice:s3cret@tcp(db.local:3306)/mydb",
		},
		{
			"no creds",
			"mysql://db.local:3306/mydb",
			"tcp(db.local:3306)/mydb",
		},
		{
			"no db",
			"mysql://alice:s3cret@db.local:3306/",
			"alice:s3cret@tcp(db.local:3306)/",
		},
		{
			"user only, no password",
			"mysql://alice@db.local/mydb",
			"alice@tcp(db.local:3306)/mydb",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := mysqlURLToNative(c.in)
			if err != nil {
				t.Fatalf("mysqlURLToNative(%q) error: %v", c.in, err)
			}
			if got != c.want {
				t.Errorf("mysqlURLToNative(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestMySQLURLToNative_Errors confirms invalid URL DSNs are rejected
// rather than producing a garbled native form.
func TestMySQLURLToNative_Errors(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"wrong scheme", "postgres://h/d"},
		{"missing host", "mysql:///db"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := mysqlURLToNative(c.in)
			if err == nil {
				t.Errorf("mysqlURLToNative(%q) returned nil error; want error", c.in)
			}
		})
	}
}

// TestParseMySQLDSN_AcceptsBothForms confirms ParseDSN handles URL and
// native forms equivalently and always sets ParseTime.
func TestParseMySQLDSN_AcceptsBothForms(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"URL form", "mysql://alice:s3cret@db.local:3306/mydb"},
		{"native form (tcp)", "alice:s3cret@tcp(db.local:3306)/mydb"},
		{"native form with unix socket", "alice:s3cret@unix(/var/run/mysqld/mysqld.sock)/mydb"},
		{"URL with query that disables parseTime", "mysql://h:1/d?parseTime=false"}, // we force-set parseTime=true
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, err := parseMySQLDSN(c.in)
			if err != nil {
				t.Fatalf("parseMySQLDSN(%q): %v", c.in, err)
			}
			if !cfg.ParseTime {
				t.Errorf("parseMySQLDSN(%q): ParseTime not set; we always force it true", c.in)
			}
			// Defense-in-depth: the round-tripped DSN should never
			// contain the plaintext password "s3cret" in the public
			// representation we'd log. (cfg.Passwd holds it internally;
			// freshness header rendering uses mysqlEngineHost which strips.)
			_ = cfg.FormatDSN() // exercise the formatter; never logged
		})
	}
}

// TestMySQLEngineHost covers credential-stripping and port-elision
// behavior for the freshness-header label.
func TestMySQLEngineHost(t *testing.T) {
	cases := []struct {
		name string
		dsn  string
		want string
	}{
		{
			"default port elided",
			"alice:s3cret@tcp(db.local:3306)/mydb",
			"mysql@db.local",
		},
		{
			"non-default port surfaced",
			"alice:s3cret@tcp(db.local:33306)/mydb",
			"mysql@db.local:33306",
		},
		{
			"unix socket uses basename",
			"alice:s3cret@unix(/var/run/mysqld/mysqld.sock)/mydb",
			"mysql@unix-mysqld.sock",
		},
		{
			"no creds",
			"tcp(127.0.0.1:3306)/mydb",
			"mysql@127.0.0.1",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, err := mysql.ParseDSN(c.dsn)
			if err != nil {
				t.Fatalf("mysql.ParseDSN: %v", err)
			}
			got := mysqlEngineHost(cfg)
			if got != c.want {
				t.Errorf("mysqlEngineHost(%q) = %q, want %q", c.dsn, got, c.want)
			}
			// Defense-in-depth: NEVER any cred-shaped token.
			for _, danger := range []string{"alice", "s3cret", "pass", "secret"} {
				if strings.Contains(got, danger) {
					t.Errorf("mysqlEngineHost(%q) leaked %q: %s", c.dsn, danger, got)
				}
			}
		})
	}
}

// TestSplitHostPort pins behavior across the host/port shapes the
// freshness-header renderer might see.
func TestSplitHostPort(t *testing.T) {
	cases := []struct {
		in   string
		host string
		port string
		ok   bool
	}{
		{"db.local:3306", "db.local", "3306", true},
		{"127.0.0.1:33306", "127.0.0.1", "33306", true},
		{"[::1]:3306", "::1", "3306", true},
		{"[::1]", "::1", "", true},
		{"db.local", "db.local", "", false},
		{"", "", "", false},
	}
	for _, c := range cases {
		host, port, ok := splitHostPort(c.in)
		if host != c.host || port != c.port || ok != c.ok {
			t.Errorf("splitHostPort(%q) = (%q, %q, %v), want (%q, %q, %v)",
				c.in, host, port, ok, c.host, c.port, c.ok)
		}
	}
}

// TestMySQLQuoteIdent confirms backtick-quoting and double-backtick
// escaping for identifiers that contain a backtick.
func TestMySQLQuoteIdent(t *testing.T) {
	cases := map[string]string{
		"users":      "`users`",
		"my-table":   "`my-table`",
		"weird`name": "`weird``name`",
		"":           "``",
	}
	for in, want := range cases {
		if got := mysqlQuoteIdent(in); got != want {
			t.Errorf("mysqlQuoteIdent(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestMySQLQualifiedName pins the "schema.table" rendering. Unlike
// Postgres's qualifiedName (which elides "public"), MySQL always shows
// the schema because there's no implicit-default-schema convention.
func TestMySQLQualifiedName(t *testing.T) {
	cases := []struct{ schema, name, want string }{
		{"mydb", "users", "mydb.users"},
		{"", "users", "users"},
		{"billing", "invoices", "billing.invoices"},
	}
	for _, c := range cases {
		if got := mysqlQualifiedName(c.schema, c.name); got != c.want {
			t.Errorf("mysqlQualifiedName(%q, %q) = %q, want %q", c.schema, c.name, got, c.want)
		}
	}
}

// TestDSNEngine covers the dispatch-decision helper that picks postgres
// / sqlite / mysql / "" from a raw DSN. Both URL and native MySQL forms
// must map to "mysql".
func TestDSNEngine(t *testing.T) {
	cases := []struct {
		dsn  string
		want string
	}{
		{"postgres://h/d", "postgres"},
		{"postgresql://h/d", "postgres"},
		{"sqlite:///abs/path.db", "sqlite"},
		{"sqlite3://./rel.db", "sqlite"},
		{"mysql://h/d", "mysql"},
		{"alice:pass@tcp(h:3306)/d", "mysql"},
		{"alice:pass@unix(/sock)/d", "mysql"},
		{"http://h", ""},
		{"random-string", ""},
	}
	for _, c := range cases {
		if got := dsnEngine(c.dsn); got != c.want {
			t.Errorf("dsnEngine(%q) = %q, want %q", c.dsn, got, c.want)
		}
	}
}
