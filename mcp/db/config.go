// Package mcpdb is the opt-in v0.8.0 Part 3 helper for SDK authors
// using mcp.Run who want Tier 2 DB support — schema introspection,
// LISTEN/NOTIFY push notifications, interval reindex, and the
// reindex_db MCP tool from Part 2.
//
// ── Why opt-in by import ────────────────────────────────────────────
// v0.6.0's mcp.Run shipped with a deliberate property: SDK authors
// building docs-only embedded-corpus binaries get a small binary (no
// pgx / sqlite / mysql in the dep tree). v0.8.0 Part 3 preserves
// that contract by putting Tier 2 wiring in this SEPARATE package.
// SDK authors who don't need DB support never import mcp/db; their
// binary is identical in dep-tree shape to v0.7.2's mcp.Run use case.
//
// SDK authors who DO want DB support add one import line and one
// function call:
//
//	import mcpdb "github.com/townsendmerino/ken/mcp/db"
//
//	reindex, cleanup, err := mcpdb.Setup(ctx, mcpdb.Config{
//	    DSN:             os.Getenv("MY_DB_DSN"),
//	    SampleRows:      0,
//	    ReindexInterval: 5 * time.Minute,
//	    EnableListen:    true,
//	})
//	if err != nil { log.Fatal(err) }
//	defer cleanup()
//
//	mcp.Run(ctx, corpus, mcp.Options{Reindex: reindex, ...})
//
// ── v0.8.0 Part 3 caveat: chunk visibility deferred ─────────────────
// In v0.8.0, Setup runs introspection on each refresh (validates the
// DSN, captures freshness, fires LISTEN handlers, exercises the
// interval-ticker path) and the reindex_db tool returns the standard
// "Reindexed in Nms." response. HOWEVER: the chunks IndexSchema
// produces are NOT yet unioned into the embedded *search.Index that
// mcp.Run serves. Chunk integration into the static Index requires a
// SetExtraChunks-style mechanism on *search.Index that isn't yet
// implemented (cmd/ken-mcp uses *search.WatchedIndex.SetExtraChunks,
// which doesn't generalize to mcp.Run's fs.FS-rooted static Index).
// Chunk integration is deferred to v0.9.0. See ADR-020 Part 3.
//
// ── Engine routing ──────────────────────────────────────────────────
// Same shape as cmd/ken-mcp: DSN scheme dispatch through
// internal/db.IndexSchema. Supports postgres:// / postgresql:// /
// sqlite:// / sqlite3:// / mysql:// + the native MySQL DSN form. The
// LISTEN/NOTIFY listener is Postgres-only; non-Postgres DSNs with
// EnableListen=true emit a debug log and ignore the flag, matching
// v0.8.0 Part 1's KEN_DB_LISTEN behavior.
package mcpdb

import (
	"io"
	"os"
	"time"

	"github.com/townsendmerino/ken/internal/db"
)

// Config is the SDK author's configuration for Tier 2 DB support.
// Mirrors the v0.7.x env-var surface from cmd/ken-mcp; SDK authors
// typically populate fields from their own env vars / CLI flags /
// config file.
//
// The zero value is a valid "no DB" sentinel: Setup with Config{}
// returns (nil, nil, nil) so the caller can unconditionally call
// Setup and let nil DSN gate the behavior.
type Config struct {
	// DSN is the database connection string. Same shape as
	// cmd/ken-mcp's KEN_DB_DSN. Empty disables DB integration
	// (Setup returns nil-nil-nil). Supports:
	//
	//   - postgres:// or postgresql:// (pgx)
	//   - sqlite:// or sqlite3:// (modernc.org/sqlite, pure Go)
	//   - mysql:// or the native go-sql-driver form
	//     (user:pass@tcp(host:port)/db, user:pass@unix(/sock)/db)
	DSN string

	// SampleRows is the number of rows per table to include in each
	// table's chunk text. 0 (default) means schema-only — the
	// row-sampling code path doesn't even open a transaction. See
	// internal/db/sample.go for the determinism guarantee and the
	// PII stance (ADR-017).
	SampleRows int

	// ReindexInterval, when > 0, configures the periodic-refresh
	// goroutine's poll cadence. 0 (default) means no background
	// polling — refresh runs only on Setup's initial call and on
	// reindex_db tool invocations.
	ReindexInterval time.Duration

	// EnableListen activates the v0.8.0 Part 1 LISTEN/NOTIFY listener.
	// Postgres-only — non-Postgres DSNs with EnableListen=true emit
	// a debug log and ignore the flag. Requires the one-time setup
	// script (mcpdb.ListenNotifyScript piped to psql); see the
	// package's ListenNotifyScript var for SDK authors building
	// their own "print-listen-script" CLI subcommand.
	EnableListen bool

	// IncludeSchemas is the allow-list of schema names. Same shape
	// as KEN_DB_SCHEMAS. Empty means "no allow-list filter; index
	// everything except engine system schemas." Default exclusions
	// (pg_catalog, information_schema, mysql, performance_schema,
	// sys) are always applied regardless of this field — operators
	// who genuinely need to index system schemas should not point
	// ken at the DB.
	IncludeSchemas []string

	// ExcludeSchemas is the deny-list extending the default
	// exclusions. Same shape as KEN_DB_EXCLUDE_SCHEMAS. When
	// IncludeSchemas is also non-empty, ExcludeSchemas is ignored
	// (allow-list wins; matches v0.7.2 + cmd/ken-mcp behavior).
	ExcludeSchemas []string

	// LogWriter is the destination for diagnostic messages. nil
	// defaults to os.Stderr. MUST NOT be os.Stdout — that's the
	// JSON-RPC channel for stdio MCP servers.
	LogWriter io.Writer
}

// toDBOptions translates an mcpdb.Config into the internal/db.Options
// shape SetupTier2 consumes. Applies the documented defaults
// (LogWriter falls back to os.Stderr; both-schema-lists honored as
// "allow-list wins" matching cmd/ken-mcp).
func (cfg Config) toDBOptions() db.Options {
	logWriter := cfg.LogWriter
	if logWriter == nil {
		logWriter = os.Stderr
	}
	include := cfg.IncludeSchemas
	exclude := cfg.ExcludeSchemas
	if len(include) > 0 && len(exclude) > 0 {
		// Both set → allow-list wins. cmd/ken-mcp logs a stderr warn
		// here; mcp/db.Setup logs the same line via logWriter so SDK
		// authors see the same signal.
		_, _ = logWriter.Write([]byte("mcp/db: KEN_DB_SCHEMAS-equivalent and KEN_DB_EXCLUDE_SCHEMAS-equivalent both set; allow-list wins, deny-list ignored\n"))
		exclude = nil
	}
	return db.Options{
		DSN:             cfg.DSN,
		SampleRows:      cfg.SampleRows,
		ReindexInterval: cfg.ReindexInterval,
		LogWriter:       logWriter,
		IncludeSchemas:  include,
		ExcludeSchemas:  exclude,
		// DefaultRepoPath is a cmd/ken-mcp concept (sqlite relative-DSN
		// anchoring). SDK authors using mcp.Run can supply absolute
		// SQLite paths or use the postgres / mysql engines; we leave
		// DefaultRepoPath empty for now. If a real-world SDK-author
		// signal asks for relative-SQLite-DSN support in mcp.Run, add
		// a Config.DefaultRepoPath field then.
	}
}
