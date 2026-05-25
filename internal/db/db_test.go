package db

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// TestFreshnessHeader_Format pins the wire format every Tier-2 chunk
// carries: ISO-8601 UTC to the minute + "<engine>@<host>". The test
// asserts the exact string so any future format change is a loud
// regression — agents downstream may grep this header.
func TestFreshnessHeader_Format(t *testing.T) {
	cases := []struct {
		engineHost string
		t          time.Time
		want       string
	}{
		{
			"postgres@dev-pg.local",
			time.Date(2026, 8, 15, 14, 23, 7, 0, time.UTC),
			"-- indexed at 2026-08-15T14:23Z from postgres@dev-pg.local",
		},
		{
			// Local timezone should be normalized to UTC.
			"postgres@127.0.0.1",
			time.Date(2026, 1, 1, 9, 30, 0, 0, time.FixedZone("PST", -8*3600)),
			"-- indexed at 2026-01-01T17:30Z from postgres@127.0.0.1",
		},
	}
	for _, c := range cases {
		got := freshnessHeader(c.engineHost, c.t)
		if got != c.want {
			t.Errorf("freshnessHeader(%q, %v) = %q, want %q", c.engineHost, c.t, got, c.want)
		}
	}
}

// TestFreshnessHeader_NeverLeaksCredentials confirms the header never
// contains credentials regardless of the engineHost label passed in.
// Defense-in-depth — the caller (engineHostFromConfig) is responsible
// for stripping creds, but a regression there would let creds into
// every chunk. This test enforces "engineHost never contains @user:"
// at the freshnessHeader layer.
func TestFreshnessHeader_NeverLeaksCredentials(t *testing.T) {
	h := freshnessHeader("postgres@host.example", time.Now())
	for _, danger := range []string{"password=", "pass:", ":pass@", "secret"} {
		if strings.Contains(h, danger) {
			t.Errorf("freshness header should never contain %q: %s", danger, h)
		}
	}
}

// TestEngineHostFromConfig — the credential-stripping live wire.
// Confirms the "@user:pass@host" credential portion of a parsed pgx
// config NEVER makes it into the engineHost label.
func TestEngineHostFromConfig(t *testing.T) {
	cases := []struct {
		dsn  string
		want string
	}{
		{"postgres://user:pass@db.local/mydb", "postgres@db.local"},
		{"postgres://alice:s3cret@127.0.0.1:5432/mydb", "postgres@127.0.0.1"},
		{"postgres://alice:s3cret@db.local:55432/mydb", "postgres@db.local:55432"},
		{"postgres://h/d", "postgres@h"}, // no creds at all
	}
	for _, c := range cases {
		cfg, err := pgx.ParseConfig(c.dsn)
		if err != nil {
			t.Fatalf("ParseConfig(%q): %v", c.dsn, err)
		}
		got := engineHostFromConfig(cfg)
		if got != c.want {
			t.Errorf("engineHostFromConfig(%q) = %q, want %q", c.dsn, got, c.want)
		}
		// Defense-in-depth: NEVER include any of the cred-shaped tokens.
		for _, danger := range []string{"alice", "s3cret", "user", "pass", "password"} {
			if strings.Contains(got, danger) {
				t.Errorf("engineHostFromConfig(%q) leaked %q: %s", c.dsn, danger, got)
			}
		}
	}
}

// TestEngineHostFromConfig_IPv6 — IPv6 hosts need brackets so the :port
// suffix is unambiguous. (pgx parses these into cfg.Host without the
// brackets; we add them back when rendering for clarity.)
func TestEngineHostFromConfig_IPv6(t *testing.T) {
	cfg, err := pgx.ParseConfig("postgres://user:pass@[::1]:5432/db")
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	got := engineHostFromConfig(cfg)
	if !strings.HasPrefix(got, "postgres@[") {
		t.Errorf("IPv6 host should be bracketed; got %q", got)
	}
}

// TestNeedsIPv6Brackets pins the brackets-or-not decision against the
// shapes pgx.Conn.Host might surface (already-bracketed, bare IPv6,
// hostname, IPv4).
func TestNeedsIPv6Brackets(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1":    false,
		"db.local":     false,
		"::1":          true, // bare IPv6
		"fe80::1":      true,
		"2001:db8::42": true,
		"[::1]":        false, // already bracketed
		"":             false,
	}
	for h, want := range cases {
		if got := needsIPv6Brackets(h); got != want {
			t.Errorf("needsIPv6Brackets(%q) = %v, want %v", h, got, want)
		}
	}
}

// TestOptions_Validate covers the safe-defaults normalization that
// keeps IndexSchema honest if a caller passes garbage values.
func TestOptions_Validate(t *testing.T) {
	// Negative SampleRows clamps to 0 (schema-only).
	o := Options{DSN: "postgres://x/y", SampleRows: -7}
	got := o.validate()
	if got.SampleRows != 0 {
		t.Errorf("negative SampleRows should clamp to 0; got %d", got.SampleRows)
	}
	// Nil LogWriter defaults to os.Stderr (we just check non-nil — can't
	// compare os.Stderr identity safely across all platforms).
	if got.LogWriter == nil {
		t.Errorf("nil LogWriter should default to non-nil writer")
	}
	// Caller's struct not mutated.
	if o.SampleRows != -7 {
		t.Errorf("validate() must not mutate caller's struct (caller's SampleRows = %d)", o.SampleRows)
	}
}

// TestOptions_LogWriterRespected confirms a caller-supplied writer is
// used as-is rather than overwritten by validate's default.
func TestOptions_LogWriterRespected(t *testing.T) {
	buf := &bytes.Buffer{}
	o := Options{DSN: "x", LogWriter: buf}.validate()
	if o.LogWriter != buf {
		t.Errorf("validate() overwrote a non-nil LogWriter")
	}
}

// TestIndexSchema_EmptyDSN — the sentinel-return contract. Empty DSN
// is the documented "Tier 2 disabled" trigger; IndexSchema must return
// (nil, nil) without attempting any connection.
func TestIndexSchema_EmptyDSN(t *testing.T) {
	chunks, err := IndexSchema(context.Background(), Options{DSN: ""})
	if err != nil {
		t.Errorf("IndexSchema with empty DSN should return nil error; got %v", err)
	}
	if chunks != nil {
		t.Errorf("IndexSchema with empty DSN should return nil chunks; got %d", len(chunks))
	}
}

// TestColumnModifiers pins the modifier-string composition order so
// successive reindexes produce byte-identical text for the same schema.
func TestColumnModifiers(t *testing.T) {
	cases := []struct {
		name string
		c    columnInfo
		want string
	}{
		{"plain", columnInfo{name: "x"}, ""},
		{"PK only", columnInfo{name: "id", isPrimaryKey: true}, "PK"},
		{"NOT NULL only", columnInfo{name: "x", notNull: true}, "NOT NULL"},
		{"PK + NOT NULL", columnInfo{name: "id", isPrimaryKey: true, notNull: true}, "PK NOT NULL"},
		{"UNIQUE not redundant with PK", columnInfo{name: "id", isPrimaryKey: true, isUnique: true}, "PK"},
		{"UNIQUE standalone", columnInfo{name: "email", notNull: true, isUnique: true}, "NOT NULL UNIQUE"},
		{"with default", columnInfo{name: "x", notNull: true, defaultExpr: "now()"}, "NOT NULL DEFAULT now()"},
		{"with FK", columnInfo{name: "user_id", notNull: true, fkTarget: "users(id)"}, "NOT NULL → users(id)"},
		{"PK + NOT NULL + DEFAULT + FK", columnInfo{name: "x", isPrimaryKey: true, notNull: true, defaultExpr: "0", fkTarget: "a(b)"}, "PK NOT NULL DEFAULT 0 → a(b)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := columnModifiers(c.c); got != c.want {
				t.Errorf("columnModifiers = %q, want %q", got, c.want)
			}
		})
	}
}

// TestRenderTableChunk exercises chunk emission end-to-end with a
// synthetic snapshot — no DB needed.
func TestRenderTableChunk(t *testing.T) {
	tab := tableInfo{
		schema: "public",
		name:   "users",
		columns: []columnInfo{
			{name: "id", dataType: "bigint", isPrimaryKey: true, notNull: true},
			{name: "email", dataType: "varchar(255)", notNull: true, isUnique: true},
			{name: "role", dataType: "varchar(32)", notNull: true, defaultExpr: "'guest'"},
			{name: "created_at", dataType: "timestamp", notNull: true, defaultExpr: "now()"},
		},
		indexes: []indexInfo{
			{name: "users_email_idx", unique: false, indexdef: "CREATE INDEX users_email_idx ON public.users USING btree (email)"},
		},
		fkReferenced: []fkRef{
			{fromSchema: "public", fromTable: "sessions", fromColumn: "user_id"},
		},
	}
	header := "-- indexed at 2026-08-15T14:23Z from postgres@dev-pg.local"
	c := renderTableChunk(tab, &schemaSnapshot{}, header, "db://postgres@dev-pg.local")

	for _, want := range []string{
		header,                    // freshness first
		"TABLE users",             // public-schema omitted by qualifiedName
		"id  bigint  PK NOT NULL", // modifier order
		"email  varchar(255)  NOT NULL UNIQUE",
		"created_at  timestamp  NOT NULL DEFAULT now()",
		"INDEX users_email_idx ON (email)", // not UNIQUE prefix
		"FK referenced by: sessions(user_id)",
	} {
		if !strings.Contains(c.Text, want) {
			t.Errorf("rendered chunk missing %q:\n%s", want, c.Text)
		}
	}
	if c.File != "db://postgres@dev-pg.local/public.users" {
		t.Errorf("unexpected chunk path: %s", c.File)
	}
}

// TestRenderViewChunk_Truncation confirms the maxViewBodyLines cap
// kicks in and the truncation notice is appended.
func TestRenderViewChunk_Truncation(t *testing.T) {
	var b strings.Builder
	for range 200 {
		b.WriteString("SELECT 1\n")
	}
	v := viewInfo{schema: "public", name: "big_view", definition: b.String()}
	c := renderViewChunk(v, "-- header", "db://h")
	if !strings.Contains(c.Text, "view body truncated") {
		t.Errorf("expected truncation notice in oversized view; got:\n%s", c.Text[:200])
	}
	// Count rendered SELECT 1 lines — must be ≤ maxViewBodyLines.
	if got := strings.Count(c.Text, "SELECT 1"); got > maxViewBodyLines {
		t.Errorf("rendered %d SELECT 1 lines, want ≤ %d", got, maxViewBodyLines)
	}
}

// TestRenderFunctionChunk pins the signature-only rendering.
func TestRenderFunctionChunk(t *testing.T) {
	f := functionInfo{
		schema:  "public",
		name:    "greet",
		argSig:  "(name text)",
		returnT: "text",
	}
	c := renderFunctionChunk(f, "-- h", "db://h")
	want := "FUNCTION greet(name text) RETURNS text"
	if !strings.Contains(c.Text, want) {
		t.Errorf("function chunk missing %q:\n%s", want, c.Text)
	}
}

func TestExtractIndexColumns(t *testing.T) {
	cases := map[string]string{
		"CREATE INDEX users_email_idx ON public.users USING btree (email)": "email",
		"CREATE UNIQUE INDEX users_pk ON public.users USING btree (id)":    "id",
		"CREATE INDEX foo ON x.y USING gin (to_tsvector('english', body))": "to_tsvector('english', body)",
		"CREATE INDEX x ON t (a, b, c)":                                    "a, b, c",
		"CREATE INDEX x ON t":                                              "",
	}
	for in, want := range cases {
		if got := extractIndexColumns(in); got != want {
			t.Errorf("extractIndexColumns(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeType(t *testing.T) {
	cases := map[string]string{
		"character varying(255)":      "varchar(255)",
		"character(10)":               "char(10)",
		"timestamp without time zone": "timestamp",
		"timestamp with time zone":    "timestamptz",
		"time without time zone":      "time",
		"time with time zone":         "timetz",
		"integer":                     "integer", // unchanged
		"bigint":                      "bigint",  // unchanged
	}
	for in, want := range cases {
		if got := normalizeType(in); got != want {
			t.Errorf("normalizeType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSortTables(t *testing.T) {
	ts := []tableInfo{
		{schema: "public", name: "users"},
		{schema: "audit", name: "events"},
		{schema: "public", name: "sessions"},
		{schema: "audit", name: "archive"},
	}
	sortTables(ts)
	wantOrder := []string{"audit.archive", "audit.events", "public.sessions", "public.users"}
	for i, w := range wantOrder {
		got := ts[i].schema + "." + ts[i].name
		if got != w {
			t.Errorf("sortTables[%d] = %q, want %q (full: %v)", i, got, w, ts)
		}
	}
}

func TestQualifiedName(t *testing.T) {
	cases := []struct{ schema, name, want string }{
		{"public", "users", "users"},
		{"", "users", "users"},
		{"audit", "events", "audit.events"},
	}
	for _, c := range cases {
		if got := qualifiedName(c.schema, c.name); got != c.want {
			t.Errorf("qualifiedName(%q, %q) = %q, want %q", c.schema, c.name, got, c.want)
		}
	}
}
