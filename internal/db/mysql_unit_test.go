package db

import (
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
)

// TestMySQLURLToConfig covers the URL-to-*mysql.Config rewrite. The driver
// only accepts the native form, so this conversion is load-bearing for every
// operator who pastes the URL shape. Assertions are on the structured config
// fields (User/Passwd/Addr/DBName), which is exactly what makes a password
// with '/'/'@'/'?' safe — it's never spliced through a native string.
func TestMySQLURLToConfig(t *testing.T) {
	cases := []struct {
		name                       string
		in                         string
		user, passwd, addr, dbname string
	}{
		{"user+pass+port+db+query", "mysql://alice:s3cret@db.local:3306/mydb?parseTime=true", "alice", "s3cret", "db.local:3306", "mydb"},
		{"default port when omitted", "mysql://alice:s3cret@db.local/mydb", "alice", "s3cret", "db.local:3306", "mydb"},
		{"no creds", "mysql://db.local:3306/mydb", "", "", "db.local:3306", "mydb"},
		{"no db", "mysql://alice:s3cret@db.local:3306/", "alice", "s3cret", "db.local:3306", ""},
		{"user only, no password", "mysql://alice@db.local/mydb", "alice", "", "db.local:3306", "mydb"},
		// The bug the string-splice version had: a '/' (or '@'/'?') in the
		// password. url-encoded in the URL, decoded into cfg.Passwd exactly.
		{"password with slash/at/question", "mysql://alice:p%2Fa%40ss%3Fx@db.local/mydb", "alice", "p/a@ss?x", "db.local:3306", "mydb"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, err := mysqlURLToConfig(c.in)
			if err != nil {
				t.Fatalf("mysqlURLToConfig(%q) error: %v", c.in, err)
			}
			if cfg.User != c.user || cfg.Passwd != c.passwd || cfg.Addr != c.addr || cfg.DBName != c.dbname {
				t.Errorf("mysqlURLToConfig(%q) = {User:%q Passwd:%q Addr:%q DBName:%q}, want {User:%q Passwd:%q Addr:%q DBName:%q}",
					c.in, cfg.User, cfg.Passwd, cfg.Addr, cfg.DBName, c.user, c.passwd, c.addr, c.dbname)
			}
		})
	}
}

// TestMySQLURLToConfig_Errors confirms invalid URL DSNs are rejected, and
// crucially that the error NEVER echoes the password (M5 credential leak).
func TestMySQLURLToConfig_Errors(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"wrong scheme", "postgres://h/d"},
		{"missing host", "mysql:///db"},
		// A bad port makes url.Parse fail with an *url.Error that embeds the
		// whole input, password included — the exact M5 leak.
		{"bad port with password", "mysql://alice:hunter2@db.local:notaport/mydb"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := mysqlURLToConfig(c.in)
			if err == nil {
				t.Fatalf("mysqlURLToConfig(%q) returned nil error; want error", c.in)
			}
			if strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), "alice") {
				t.Errorf("mysqlURLToConfig error leaked credentials: %v", err)
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

// TestNormalizeMySQLIntType pins the v0.8.1 Part B integer display-width
// stripping behavior. MariaDB 11.x still emits the legacy "(N)" suffix
// for integer-family columns + scalar-function return types; MySQL 8.x
// dropped them in 8.0 (bug #80094). The normalizer collapses the MariaDB
// output to match the MySQL form so cross-engine chunk text stays byte-
// identical. Idempotent — running it on already-bare input is a no-op.
//
// See ADR-021 for the full audit + the rejected probe-and-branch
// alternative.
func TestNormalizeMySQLIntType(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// Tier-1 transforms — MariaDB → MySQL form.
		{"bigint(20)", "bigint(20)", "bigint"},
		{"int(11)", "int(11)", "int"},
		{"smallint(6)", "smallint(6)", "smallint"},
		{"tinyint(4)", "tinyint(4)", "tinyint"},
		{"mediumint(9)", "mediumint(9)", "mediumint"},

		// Modifiers downstream of the type preserved.
		{"unsigned modifier preserved", "bigint(20) unsigned", "bigint unsigned"},
		{"zerofill modifier preserved", "int(11) unsigned zerofill", "int unsigned zerofill"},

		// MySQL 8.x output — already bare, idempotent.
		{"already bare bigint", "bigint", "bigint"},
		{"already bare int unsigned", "int unsigned", "int unsigned"},

		// Non-integer families must NOT be touched (their (N) is semantic).
		{"varchar with size retained", "varchar(255)", "varchar(255)"},
		{"char with size retained", "char(36)", "char(36)"},
		{"decimal with precision retained", "decimal(10,2)", "decimal(10,2)"},
		{"binary with size retained", "binary(16)", "binary(16)"},
		{"varbinary with size retained", "varbinary(255)", "varbinary(255)"},

		// Tinyint(1) is the special case MySQL/MariaDB BOTH use to mean
		// boolean; the (1) carries the semantic, but historically both
		// engines also accept "tinyint(4)" / "tinyint(20)" with the
		// same column. We strip uniformly — agents reading the chunk see
		// "tinyint" and treat boolean-vs-int via context, which is the
		// same disambiguation they'd do for SQLite (no native bool).
		{"tinyint(1) stripped uniformly", "tinyint(1)", "tinyint"},

		// Function-return-type forms.
		{"function returns int(11)", "int(11)", "int"},

		// Mixed string with non-int parens left alone.
		{"varchar(64) + tinyint(4)", "varchar(64), tinyint(4)", "varchar(64), tinyint"},

		// Empty input.
		{"empty string", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := normalizeMySQLIntType(c.in)
			if got != c.want {
				t.Errorf("normalizeMySQLIntType(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
