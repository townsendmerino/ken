// Package db is ken's v0.7.0 Tier-2 chunker: it connects to a live
// database, introspects its schema via information_schema / pg_catalog,
// and emits one ken chunk per table / view / index / procedure. The
// emitted chunks share the denormalized "for retrieval" shape of
// internal/sql (Tier 1), so agents searching for "users email NOT NULL"
// retrieve the live schema and the static .sql migration in one ranked
// list.
//
// Scope:
//
//   - Postgres only for v0.7.0 (github.com/jackc/pgx/v5). MySQL and
//     SQLite share this package; one engine cleanly first (ADR-017).
//   - Schema-only by default. Row sampling opt-in via Options.SampleRows
//     (see sample.go). PII responsibility lives with the operator;
//     defaults are conservative (schema-only, freshness metadata in
//     every chunk) and the README is explicit about not pointing this
//     at production data.
//   - Build-once-at-startup is the default. Periodic refresh and
//     SIGHUP-triggered manual refresh are layered on top via the
//     Refresher type (refresh.go) which the binary wires.
//
// Every chunk Tier 2 emits carries a one-line freshness header
//
//	-- indexed at 2026-08-15T14:23Z from postgres@dev-pg.local
//
// so an agent reading the chunk knows when the data was captured and
// from which engine/host. No credentials, no DSN-leaking.
//
// Chunk.File for DB chunks is a synthetic URL-ish path like
// `db://postgres@dev-pg.local/public.users` so DB chunks are
// distinguishable from filesystem chunks in any UI / ranked-result
// view that displays the path.
package db

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/townsendmerino/ken/internal/chunk"
)

// Options configures Tier-2 DB indexing. The zero value is a valid
// "Tier 2 disabled" sentinel: IndexSchema returns (nil, nil) when
// Options.DSN == "".
type Options struct {
	// DSN is the Postgres connection string (postgres:// or
	// postgresql:// URL form). Empty disables Tier 2 entirely. Format
	// must be parseable by pgx.ParseConfig (rejected at startup if
	// not — see cmd/ken-mcp/env.go's envDSN).
	DSN string

	// SampleRows is the number of rows-per-table to include in each
	// table's chunk text. 0 (default) means schema-only — the row-
	// sampling code path doesn't even open a transaction. See
	// sample.go for the sampling implementation and the deterministic-
	// ordering guarantee.
	SampleRows int

	// ReindexInterval, when > 0, configures the Refresher's periodic
	// poll cadence. 0 (default) means no background polling — refresh
	// only at startup or via Refresher.Refresh (typically SIGHUP).
	ReindexInterval time.Duration

	// LogWriter is the destination for diagnostic messages (skipped
	// tables, transient query errors). nil defaults to os.Stderr. Must
	// not be wired to os.Stdout — that's the JSON-RPC channel for
	// cmd/ken-mcp. Tier 2 logs at "warn"-ish level: skipped tables,
	// connection failures, sampling errors per table.
	LogWriter io.Writer

	// DefaultRepoPath (v0.7.1) is the operator's KEN_MCP_DEFAULT_REPO
	// used by the SQLite engine to resolve relative DSN paths
	// ("sqlite://./dev.db" → join(DefaultRepoPath, "dev.db")). Empty is
	// fine for absolute SQLite paths and for the Postgres path (which
	// ignores this field entirely).
	DefaultRepoPath string
}

// validate normalizes Options for safety: clamps negative SampleRows to
// 0 and sets the default LogWriter. Returns a copy; never mutates the
// caller's struct.
func (o Options) validate() Options {
	if o.SampleRows < 0 {
		o.SampleRows = 0
	}
	if o.LogWriter == nil {
		o.LogWriter = os.Stderr
	}
	return o
}

// IndexSchema connects to opts.DSN, introspects, and returns one chunk
// per database object. Returns (nil, nil) when opts.DSN is empty
// (Tier 2 disabled — the documented sentinel). Closes the connection
// before return; no long-lived handle.
//
// As of v0.7.1 IndexSchema dispatches on the DSN scheme:
//   - postgres:// / postgresql://  → indexSchemaPostgres (the v0.7.0 path)
//   - sqlite://   / sqlite3://     → indexSchemaSQLite   (ADR-018)
//
// Per-table errors during introspection (a query failing on one weird
// table type, for example) are logged to opts.LogWriter at warn level
// and the table is skipped — the goal is best-effort indexing of mixed-
// quality schemas, not all-or-nothing.
//
// A connection-level error (DSN unreachable, auth failed) is returned
// as-is: caller (cmd/ken-mcp) logs and continues startup with the FS
// index alone. Tier 2 going dark is not fatal.
func IndexSchema(ctx context.Context, opts Options) ([]chunk.Chunk, error) {
	opts = opts.validate()
	if opts.DSN == "" {
		return nil, nil
	}

	scheme := strings.ToLower(schemeOf(opts.DSN))
	switch scheme {
	case "postgres", "postgresql":
		return indexSchemaPostgres(ctx, opts)
	case "sqlite", "sqlite3":
		return indexSchemaSQLite(ctx, opts, opts.DefaultRepoPath)
	default:
		return nil, fmt.Errorf("db: unsupported DSN scheme %q (want postgres://, postgresql://, sqlite://, or sqlite3://)", scheme)
	}
}

// schemeOf returns the lowercased URL scheme from a DSN, or "" if
// unparseable. Used by IndexSchema's engine dispatch.
func schemeOf(dsn string) string {
	if i := strings.Index(dsn, "://"); i > 0 {
		return strings.ToLower(dsn[:i])
	}
	return ""
}

// indexSchemaPostgres is the v0.7.0 implementation lifted into its own
// function so the dispatch above can pick between it and the SQLite
// arm cleanly. Behavior unchanged from v0.7.0.
func indexSchemaPostgres(ctx context.Context, opts Options) ([]chunk.Chunk, error) {
	cfg, err := pgx.ParseConfig(opts.DSN)
	if err != nil {
		return nil, fmt.Errorf("db: parse DSN: %w", err)
	}
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("db: connect: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	schema, err := introspect(ctx, conn, opts)
	if err != nil {
		return nil, fmt.Errorf("db: introspect: %w", err)
	}

	header := freshnessHeader(engineHostFromConfig(cfg), time.Now().UTC())
	pathPrefix := dbPathPrefix(cfg)

	var chunks []chunk.Chunk
	for _, t := range schema.tables {
		chunks = append(chunks, renderTableChunk(t, schema, header, pathPrefix))
	}
	for _, v := range schema.views {
		chunks = append(chunks, renderViewChunk(v, header, pathPrefix))
	}
	for _, f := range schema.functions {
		chunks = append(chunks, renderFunctionChunk(f, header, pathPrefix))
	}
	return chunks, nil
}

// engineHostFromConfig formats the "engine@host" portion of the
// freshness header. Always engine=postgres for v0.7.0. The host is the
// hostname (or IPv6 in brackets) plus port only if non-default.
//
// Crucially: NEVER returns the username or password. Passwords would
// leak into every chunk text; usernames would leak into agent output.
// Operators who need full provenance can grep `pg_stat_activity` on
// their server.
func engineHostFromConfig(cfg *pgx.ConnConfig) string {
	host := cfg.Host
	if host == "" {
		host = "localhost"
	}
	// IPv6 hosts need brackets to disambiguate from the :port suffix.
	if needsIPv6Brackets(host) {
		host = "[" + host + "]"
	}
	if cfg.Port != 0 && cfg.Port != 5432 {
		host = fmt.Sprintf("%s:%d", host, cfg.Port)
	}
	return "postgres@" + host
}

// needsIPv6Brackets reports whether host contains a colon (so it must
// be IPv6) and isn't already bracketed.
func needsIPv6Brackets(host string) bool {
	if len(host) == 0 {
		return false
	}
	if host[0] == '[' {
		return false
	}
	colons := 0
	for i := range host {
		if host[i] == ':' {
			colons++
			if colons >= 2 {
				return true
			}
		}
	}
	return false
}

// dbPathPrefix returns the synthetic URL-ish prefix that DB chunks use
// for Chunk.File: "db://postgres@host[:port]". The full path appends
// "/<schema>.<object>" per chunk so filesystem chunks (relative paths)
// and DB chunks (absolute-ish URLs) are unambiguously distinguishable in
// any UI or ranked-result display.
func dbPathPrefix(cfg *pgx.ConnConfig) string {
	return "db://" + engineHostFromConfig(cfg)
}

// freshnessHeader formats the one-line header every Tier-2 chunk
// carries. UTC ISO-8601, minute resolution (seconds add noise without
// signal at indexing cadence). Format pinned by tests.
func freshnessHeader(engineHost string, t time.Time) string {
	return fmt.Sprintf("-- indexed at %s from %s",
		t.UTC().Format("2006-01-02T15:04Z"),
		engineHost,
	)
}
