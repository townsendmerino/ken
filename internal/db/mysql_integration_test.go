//go:build dbintegration

// MySQL + MariaDB integration tests for internal/db, gated by
// `dbintegration` build tag (parallel to integration_test.go for
// Postgres and the sqlite_test.go file-based path). Each test in this
// file runs as a subtest per configured engine — same assertions, same
// fixture, byte-identical chunk text (the v0.8.1 Part B normalization
// makes MariaDB and MySQL produce identical chunks for this fixture).
//
// Run against MySQL only:
//
//	docker run --rm -d --name ken-test-mysql -e MYSQL_ROOT_PASSWORD=test \
//	  -p 53306:3306 mysql:8
//	# wait for the container to finish initializing (~20s)
//	export KEN_DB_MYSQL_TEST_DSN="root:test@tcp(127.0.0.1:53306)/?parseTime=true"
//	go test -tags=dbintegration ./internal/db/ -run TestMySQLIntegration -v
//
// Run against MariaDB as well (v0.8.1 Part B, ADR-021):
//
//	docker run --rm -d --name ken-test-mariadb -e MARIADB_ROOT_PASSWORD=test \
//	  -p 53307:3306 mariadb:11-jammy
//	export KEN_DB_MARIADB_TEST_DSN="root:test@tcp(127.0.0.1:53307)/?parseTime=true"
//	go test -tags=dbintegration ./internal/db/ -run TestMySQLIntegration -v
//
// Either env var being unset skips its engine's subtests cleanly; the
// other engine still runs. Both unset skips the whole test.
//
// The mysql_init.sql fixture creates database `ken_test` and seeds
// deterministic rows. Tests run multi-statement DDL via the driver's
// `multiStatements=true` connection flag.
package db

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

// engineDSN pairs a subtest label (engine variant name) with its DSN.
type engineDSN struct {
	label string
	dsn   string
}

// mysqlEnginesOrSkip returns the engine-DSN pairs configured via env
// vars. Empty when no engine is configured — caller t.Skip()s. The
// v0.8.1 Part B normalization makes both engines produce byte-identical
// chunks against this fixture, so every test body assertion holds for
// every configured engine.
func mysqlEnginesOrSkip(t *testing.T) []engineDSN {
	t.Helper()
	var engines []engineDSN
	if dsn := os.Getenv("KEN_DB_MYSQL_TEST_DSN"); dsn != "" {
		engines = append(engines, engineDSN{label: "mysql", dsn: dsn})
	}
	if dsn := os.Getenv("KEN_DB_MARIADB_TEST_DSN"); dsn != "" {
		engines = append(engines, engineDSN{label: "mariadb", dsn: dsn})
	}
	if len(engines) == 0 {
		t.Skip("set KEN_DB_MYSQL_TEST_DSN and/or KEN_DB_MARIADB_TEST_DSN; see mysql_integration_test.go for setup")
	}
	return engines
}

// withMultiStatements rewrites a DSN to enable multi-statement queries
// (needed for loading the fixture, which is a multi-statement SQL file).
// The connection used for IndexSchema does NOT need this — production
// introspection is single-statement queries.
func withMultiStatements(t *testing.T, dsn string) string {
	t.Helper()
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("ParseDSN: %v", err)
	}
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	cfg.Params["multiStatements"] = "true"
	cfg.ParseTime = true
	return cfg.FormatDSN()
}

func loadMySQLFixture(t *testing.T, ctx context.Context, dsn string) {
	t.Helper()
	conn, err := sql.Open("mysql", withMultiStatements(t, dsn))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer conn.Close()
	if err := conn.PingContext(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	data, err := os.ReadFile("testdata/mysql_init.sql")
	if err != nil {
		t.Fatalf("read mysql_init.sql: %v", err)
	}
	if _, err := conn.ExecContext(ctx, string(data)); err != nil {
		t.Fatalf("mysql_init.sql exec: %v", err)
	}
}

// TestMySQLIntegration_IntrospectsKnownSchema mirrors
// TestIntegration_IntrospectsKnownSchema (Postgres) — every shape
// (table with PK + FK + UNIQUE + DEFAULT + INDEX, view, AUTO_INCREMENT)
// produces a chunk with the expected markers. v0.8.1 Part B: parameterized
// to run identically against MySQL and MariaDB.
func TestMySQLIntegration_IntrospectsKnownSchema(t *testing.T) {
	for _, eng := range mysqlEnginesOrSkip(t) {
		t.Run(eng.label, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			loadMySQLFixture(t, ctx, eng.dsn)

			logBuf := &bytes.Buffer{}
			chunks, err := IndexSchema(ctx, Options{
				DSN:            eng.dsn,
				LogWriter:      logBuf,
				IncludeSchemas: []string{"ken_test"}, // isolate from any other DBs on the host
			})
			if err != nil {
				t.Fatalf("IndexSchema: %v\n--log--\n%s", err, logBuf.String())
			}
			if len(chunks) == 0 {
				t.Fatalf("IndexSchema produced 0 chunks")
			}

			tableUsers := mustFindChunk(t, chunks, "TABLE ken_test.users")
			tableSessions := mustFindChunk(t, chunks, "TABLE ken_test.sessions")
			view := mustFindChunk(t, chunks, "VIEW ken_test.active_users")

			// users table assertions. The v0.8.1 integer-display-width
			// normalization makes "id bigint PK NOT NULL DEFAULT
			// AUTO_INCREMENT" the cross-engine-stable form — MariaDB's
			// raw "bigint(20)" gets stripped to "bigint" before chunk
			// rendering. See ADR-021.
			for _, want := range []string{
				"email  varchar(255)  NOT NULL UNIQUE",
				"id  bigint  PK NOT NULL DEFAULT AUTO_INCREMENT",
				"INDEX users_email_idx ON (email)",
				"FK referenced by: ken_test.sessions(user_id)",
			} {
				if !strings.Contains(tableUsers.Text, want) {
					t.Errorf("users chunk missing %q:\n%s", want, tableUsers.Text)
				}
			}

			// sessions table assertions — confirms forward-direction FK arrow.
			if !strings.Contains(tableSessions.Text, "user_id  bigint  NOT NULL → ken_test.users(id)") {
				t.Errorf("sessions chunk missing forward FK arrow:\n%s", tableSessions.Text)
			}

			// MySQL stores view definitions in normalized lowercase + backticked
			// form, so the rendered chunk contains `left join` (not LEFT JOIN)
			// and `` `ken_test`.`sessions` `` (backticked + qualified). Match
			// case-insensitively to keep the assertion robust against the
			// cross-engine view-body parenthesization difference documented
			// in ADR-021.
			lower := strings.ToLower(view.Text)
			if !strings.Contains(lower, "left join") || !strings.Contains(lower, "sessions") {
				t.Errorf("view chunk missing body content:\n%s", view.Text)
			}

			for _, c := range chunks {
				if !strings.HasPrefix(c.Text, "-- indexed at ") {
					t.Errorf("chunk missing freshness header (path=%s):\n%s", c.File, c.Text)
				}
				// Defense-in-depth: never any credential bytes.
				for _, danger := range []string{"root:test", "password=", ":test@"} {
					if strings.Contains(c.Text, danger) {
						t.Errorf("chunk leaked credential %q (path=%s)", danger, c.File)
					}
				}
				// The engine label is always "mysql" — both MySQL and
				// MariaDB route through the same code path and report
				// the same engine name. ADR-021 records the rationale.
				if !strings.HasPrefix(c.File, "db://mysql@") {
					t.Errorf("chunk path should start with db://mysql@; got %s", c.File)
				}
			}
		})
	}
}

// TestMySQLIntegration_RowSamplingDeterministic — two consecutive runs
// with SampleRows=3 produce identical sample content modulo the
// freshness header (the same contract Postgres + SQLite have). v0.8.1
// Part B: same contract held against MariaDB.
func TestMySQLIntegration_RowSamplingDeterministic(t *testing.T) {
	for _, eng := range mysqlEnginesOrSkip(t) {
		t.Run(eng.label, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			loadMySQLFixture(t, ctx, eng.dsn)

			opts := Options{
				DSN:            eng.dsn,
				SampleRows:     3,
				LogWriter:      &bytes.Buffer{},
				IncludeSchemas: []string{"ken_test"},
			}
			chunksA, err := IndexSchema(ctx, opts)
			if err != nil {
				t.Fatalf("first IndexSchema: %v", err)
			}
			chunksB, err := IndexSchema(ctx, opts)
			if err != nil {
				t.Fatalf("second IndexSchema: %v", err)
			}

			users := mustFindChunk(t, chunksA, "TABLE ken_test.users")
			if !strings.Contains(users.Text, "Sample rows (3") {
				t.Fatalf("expected 'Sample rows (3' in users chunk; got:\n%s", users.Text)
			}
			for _, want := range []string{"alice@example.com", "bob@example.com", "claire@example.com"} {
				if !strings.Contains(users.Text, want) {
					t.Errorf("expected %q in sampled rows:\n%s", want, users.Text)
				}
			}

			stripHeader := func(s string) string {
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
		})
	}
}

// TestMySQLIntegration_SchemaOnlyWhenSampleRowsZero confirms SampleRows=0
// never emits row data.
func TestMySQLIntegration_SchemaOnlyWhenSampleRowsZero(t *testing.T) {
	for _, eng := range mysqlEnginesOrSkip(t) {
		t.Run(eng.label, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			loadMySQLFixture(t, ctx, eng.dsn)

			chunks, err := IndexSchema(ctx, Options{
				DSN:            eng.dsn,
				IncludeSchemas: []string{"ken_test"},
			})
			if err != nil {
				t.Fatalf("IndexSchema: %v", err)
			}
			users := mustFindChunk(t, chunks, "TABLE ken_test.users")
			if strings.Contains(users.Text, "Sample rows") {
				t.Errorf("SampleRows=0 should not emit any sample rows, got:\n%s", users.Text)
			}
			for _, danger := range []string{"alice@example.com", "bob@example.com", "claire@example.com"} {
				if strings.Contains(users.Text, danger) {
					t.Errorf("row value %q leaked into schema-only chunk", danger)
				}
			}
		})
	}
}

// TestMySQLIntegration_ExcludeSchemas_DenyList confirms a deny-list
// excludes the named schema from the chunk set.
func TestMySQLIntegration_ExcludeSchemas_DenyList(t *testing.T) {
	for _, eng := range mysqlEnginesOrSkip(t) {
		t.Run(eng.label, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			loadMySQLFixture(t, ctx, eng.dsn)

			chunks, err := IndexSchema(ctx, Options{
				DSN:            eng.dsn,
				ExcludeSchemas: []string{"ken_test"},
			})
			if err != nil {
				t.Fatalf("IndexSchema: %v", err)
			}
			for _, c := range chunks {
				if strings.Contains(c.Text, "ken_test") {
					t.Errorf("ExcludeSchemas=[ken_test] should produce no chunks naming ken_test; got:\n%s", c.Text)
				}
			}
		})
	}
}

// TestMySQLIntegration_SystemSchemasNeverIncluded confirms that even
// with an aggressive allow-list including a system schema, the system
// schema is rejected by filterSchema.
func TestMySQLIntegration_SystemSchemasNeverIncluded(t *testing.T) {
	for _, eng := range mysqlEnginesOrSkip(t) {
		t.Run(eng.label, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			loadMySQLFixture(t, ctx, eng.dsn)

			chunks, err := IndexSchema(ctx, Options{
				DSN:            eng.dsn,
				IncludeSchemas: []string{"ken_test", "information_schema", "mysql"},
			})
			if err != nil {
				t.Fatalf("IndexSchema: %v", err)
			}
			for _, c := range chunks {
				for _, system := range []string{"information_schema.", "mysql.user", "performance_schema", "sys."} {
					if strings.Contains(c.Text, system) {
						t.Errorf("system schema content %q leaked into chunk despite default exclusions:\n%s", system, c.Text)
					}
				}
			}
		})
	}
}
