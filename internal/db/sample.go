package db

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// maxSampleCellChars caps the displayed width of any single cell in a
// sample row. Long values (JSON blobs, base64, text columns) are
// truncated with U+2026 so the rendered table stays readable. The
// underlying data is not modified — only the displayed slice.
const maxSampleCellChars = 80

func init() {
	// Replace the no-op stub in introspect.go with the real sampler.
	// Package-init wiring (rather than direct assignment in IndexSchema)
	// keeps sample.go optional: build a binary without it and you get
	// schema-only by default at no code cost.
	appendRowSamples = sampleRowsImpl
}

// sampleRowsImpl pulls Options.SampleRows rows from every table in snap
// and attaches them to tableInfo.sampleRows / sampleColumns. Also
// populates approxRowCount from pg_class.reltuples (free to query, no
// extra COUNT(*) needed).
//
// Failure handling: per-table errors are swallowed with a warn message
// to opts.LogWriter. The reasoning: a corpus may contain one weird
// table (PostGIS geometry, custom domain, vendor-extension type) that
// won't decode cleanly via pgx.Rows.Values; the rest of the schema
// should still benefit from row sampling. A connection-level error
// would have already failed earlier in introspection.
//
// Determinism: rows are ordered by the table's first primary-key column
// when one exists, else by ORDER BY 1. Across two reindexes of an
// unchanged table the same rows come out in the same order (modulo
// concurrent writes to the underlying table).
func sampleRowsImpl(ctx context.Context, conn *pgx.Conn, snap *schemaSnapshot, opts Options) {
	if opts.SampleRows <= 0 {
		return
	}

	// Fetch all reltuples in one query, then sample per table.
	approx, err := queryApproxRowCounts(ctx, conn)
	if err != nil {
		warn(opts, "row-count query failed: %v", err)
		// Continue without approx counts; the sample text just omits "of ~N".
	}

	for i := range snap.tables {
		t := &snap.tables[i]
		if c, ok := approx[t.schema+"."+t.name]; ok {
			t.approxRowCount = c
		}
		sampleOne(ctx, conn, t, opts)
	}
}

// sampleOne samples one table. Wraps everything in a recovered panic
// (defense against pgx returning weird types) and a per-table error
// swallow.
func sampleOne(ctx context.Context, conn *pgx.Conn, t *tableInfo, opts Options) {
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

	fds := rows.FieldDescriptions()
	colNames := make([]string, len(fds))
	for i, fd := range fds {
		colNames[i] = string(fd.Name)
	}

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
	t.sampleColumns = colNames
	t.sampleRows = collected
}

// orderByClauseFor picks the ORDER BY clause that maximizes determinism.
// Prefers the table's first PK column; falls back to "ORDER BY 1" which
// orders by the first column (whatever it is) for stable cross-run
// output even on tables without a PK.
func orderByClauseFor(t *tableInfo) string {
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
func queryApproxRowCounts(ctx context.Context, conn *pgx.Conn) (map[string]float64, error) {
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
