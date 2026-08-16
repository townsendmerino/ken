package db

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"
)

// maxSampleCellChars caps the displayed width of any single cell in a
// sample row. Long values (JSON blobs, base64, text columns) are
// truncated with U+2026 so the rendered table stays readable. The
// underlying data is not modified — only the displayed slice.
const maxSampleCellChars = 80

// withSampleRecover runs fn, converting a panic into a warn log so one
// pathological table (an exotic driver type conversion — the hazard the
// Postgres sampleOne recover was written for) skips instead of crashing the
// whole ken-mcp process. Applied symmetrically across all three engines
// (audit §26): a panic inside the MySQL errgroup goroutine or the SQLite
// loop was previously fatal, where the Postgres equivalent warned and
// continued.
func withSampleRecover(opts Options, label string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			warn(opts, "panic sampling %s: %v", label, r)
		}
	}()
	fn()
}

// pgxQuerier is the subset of the pgx API the sampler needs. Both
// *pgx.Conn and *pgxpool.Pool satisfy it; the pool hands each concurrent
// Query its own connection, which is what makes the fan-out safe.
type pgxQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// samplePostgresParallel opens a bounded pgxpool and fans the per-table
// sample queries out across sampleWorkers() goroutines (audit §11 — the
// serial N+1 loop was the flagship engine's un-parallelized headline cost;
// MySQL/SQLite already fan out). sampleOne writes only to &snap.tables[i],
// so no locking is needed. Best-effort per table via the recover +
// error-swallow inside sampleOne.
func samplePostgresParallel(ctx context.Context, opts Options, snap *schemaSnapshot) error {
	if opts.SampleRows <= 0 || len(snap.tables) == 0 {
		return nil
	}
	poolCfg, err := pgxpool.ParseConfig(opts.DSN)
	if err != nil {
		// opts.DSN already parsed OK in indexSchemaPostgres; stay generic
		// anyway so a *url.Error can never echo the password (M5).
		return errors.New("db: unparseable postgres DSN for sample pool")
	}
	poolCfg.MaxConns = int32(sampleWorkers())
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return fmt.Errorf("sample pool: %w", err)
	}
	defer pool.Close()

	// reltuples for every table in one query.
	if approx, aerr := queryApproxRowCounts(ctx, pool); aerr != nil {
		warn(opts, "row-count query failed: %v", aerr)
	} else {
		for i := range snap.tables {
			t := &snap.tables[i]
			if c, ok := approx[t.schema+"."+t.name]; ok {
				t.approxRowCount = c
			}
		}
	}

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(sampleWorkers())
	for i := range snap.tables {
		t := &snap.tables[i]
		g.Go(func() error {
			sampleOne(gctx, pool, t, opts)
			return nil
		})
	}
	return g.Wait()
}

// sampleOne samples one table. Wraps everything in a recovered panic
// (defense against pgx returning weird types) and a per-table error
// swallow. The querier is a *pgxpool.Pool under the parallel sampler, so
// each call runs on its own pooled connection.
func sampleOne(ctx context.Context, conn pgxQuerier, t *tableDef, opts Options) {
	defer func() {
		if r := recover(); r != nil {
			warn(opts, "panic sampling %s: %v", t.schema+"."+t.name, r)
		}
	}()

	orderClause := orderByClauseFor(t)
	// quoteIdent uses double-quoting to safely handle reserved
	// identifiers and mixed-case names; the table and schema came from
	// system catalogs (trusted source) but quoting is the right
	// defensive shape.
	query := fmt.Sprintf(
		"SELECT * FROM %s.%s %s LIMIT $1",
		quoteIdent(t.schema), quoteIdent(t.name), orderClause,
	)
	rows, err := conn.Query(ctx, query, opts.SampleRows)
	if err != nil {
		warn(opts, "sample query failed for %s: %v", t.schema+"."+t.name, err)
		return
	}
	defer rows.Close()

	var collected [][]string
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			warn(opts, "sample decode failed for %s: %v", t.schema+"."+t.name, err)
			return
		}
		cells := make([]string, len(vals))
		for i, v := range vals {
			cells[i] = truncateCell(formatCell(v))
		}
		collected = append(collected, cells)
	}
	if err := rows.Err(); err != nil {
		warn(opts, "sample read failed for %s: %v", t.schema+"."+t.name, err)
		return
	}
	t.sampleRows = collected
}

// orderByClauseFor picks the ORDER BY clause that maximizes determinism.
// Prefers the table's first PK column; falls back to "ORDER BY 1" which
// orders by the first column (whatever it is) for stable cross-run
// output even on tables without a PK.
func orderByClauseFor(t *tableDef) string {
	for _, c := range t.columns {
		if c.isPrimaryKey {
			return "ORDER BY " + quoteIdent(c.name)
		}
	}
	return "ORDER BY 1"
}

// queryApproxRowCounts returns a (schema.table → reltuples) map. Free
// over a single pg_class query; far cheaper than per-table COUNT(*) on
// large tables. The value is approximate (refreshed by ANALYZE / autovac)
// — we render it as "~N" in the chunk so agents know not to take it
// as exact.
func queryApproxRowCounts(ctx context.Context, conn pgxQuerier) (map[string]float64, error) {
	const q = `
SELECT
    n.nspname AS schema,
    c.relname AS name,
    c.reltuples AS approx
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n
  ON n.oid = c.relnamespace
WHERE c.relkind = 'r'
  AND n.nspname NOT IN ('pg_catalog', 'information_schema')
  AND n.nspname NOT LIKE 'pg_%';
`
	rows, err := conn.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var schema, name string
		var approx float64
		if err := rows.Scan(&schema, &name, &approx); err != nil {
			return nil, err
		}
		out[schema+"."+name] = approx
	}
	return out, rows.Err()
}

// formatCell renders one row's cell value as a string suitable for
// embedding in the chunk text. Time values get an ISO-8601 surface,
// nil becomes "NULL", everything else falls through to fmt.Sprintf("%v",
// ...) which is good enough for the common scalar types.
//
// Bytes get a small lossy fingerprint ("<N bytes>") rather than the
// raw content — embedding base64 of an arbitrary BLOB in every chunk
// would inflate the index for no retrieval benefit.
func formatCell(v any) string {
	if v == nil {
		return "NULL"
	}
	switch x := v.(type) {
	case []byte:
		return fmt.Sprintf("<%d bytes>", len(x))
	case string:
		return x
	default:
		return fmt.Sprintf("%v", v)
	}
}

// truncateCell shortens a cell to maxSampleCellChars runes (not bytes —
// non-ASCII shouldn't get clipped mid-rune), appending U+2026 if it
// was actually shortened. Rune-aware so a CJK column doesn't render
// garbled.
func truncateCell(s string) string {
	if len(s) <= maxSampleCellChars {
		return s
	}
	runes := []rune(s)
	if len(runes) <= maxSampleCellChars {
		return s
	}
	return string(runes[:maxSampleCellChars]) + "…"
}

// quoteIdent double-quotes an identifier for safe SQL embedding. Doubles
// any embedded `"` per the SQL standard. Used for schema and table
// names inside the dynamically-built SELECT — values come from system
// catalogs (trusted) but quoting is the right defensive form.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// warn emits a one-line diagnostic to opts.LogWriter. We don't go
// through cmd/ken-mcp's leveled logger here — sampling failures are
// best-effort by design and a single io.Writer is enough.
func warn(opts Options, format string, args ...any) {
	if opts.LogWriter == nil {
		return
	}
	fmt.Fprintf(opts.LogWriter, "db: "+format+"\n", args...)
}
