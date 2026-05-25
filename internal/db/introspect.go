package db

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// schemaSnapshot is the materialized result of introspection: every
// table, view, function we found, plus the per-table columns,
// constraints, indexes, and FK relationships. The renderers in emit.go
// consume this struct to produce chunks.
type schemaSnapshot struct {
	tables    []tableInfo
	views     []viewInfo
	functions []functionInfo
}

type tableInfo struct {
	schema       string
	name         string
	columns      []columnInfo
	indexes      []indexInfo
	fkReferenced []fkRef // FKs from other tables pointing AT this one

	// approxRowCount is pg_class.reltuples — Postgres' best estimate of
	// how many rows are in the table, refreshed by ANALYZE. Free to
	// query; zero on a freshly-created or never-analyzed table.
	approxRowCount float64
	// sampleRows is N rows of the table rendered as string cells.
	// Populated only when Options.SampleRows > 0. Each inner slice is
	// one row; values are already truncated for display. Nil otherwise.
	sampleRows [][]string
	// sampleColumns is the column-name header that aligns with
	// sampleRows. Same length as each sampleRows row. Nil when no rows
	// are sampled.
	sampleColumns []string
}

type columnInfo struct {
	name         string
	dataType     string // human-friendly, e.g. "varchar(255)" not "character varying"
	notNull      bool
	isPrimaryKey bool
	isUnique     bool
	defaultExpr  string // empty if no default
	fkTarget     string // empty if not a FK; "<schema>.<table>(<col>)" otherwise
}

type indexInfo struct {
	name     string
	unique   bool
	indexdef string // raw pg_indexes.indexdef (the CREATE INDEX statement Postgres would emit to recreate it)
}

type fkRef struct {
	fromSchema string
	fromTable  string
	fromColumn string
}

type viewInfo struct {
	schema     string
	name       string
	definition string // truncated by renderer if too long
}

type functionInfo struct {
	schema  string
	name    string
	argSig  string // "(arg_t1, arg_t2)" — argument types only, no names; body NOT indexed in v0.7.0
	returnT string // return type or "void"
}

// introspect runs the per-category queries and assembles a
// schemaSnapshot. The queries are intentionally portable across modern
// Postgres versions (≥12) and avoid pg_catalog-only fields where
// information_schema suffices.
//
// Errors from individual queries propagate up; a connection-level
// failure ends introspection (caller returns the error to its caller,
// which logs and continues with Tier 1 only). Per-row data issues are
// tolerated — if pg_indexes contains a row we can't parse, we skip it
// and continue rather than failing the whole introspection.
//
// v0.7.2: rows are filtered through filterSchema(name, "postgres", opts)
// so KEN_DB_SCHEMAS / KEN_DB_EXCLUDE_SCHEMAS are respected. The annotate
// queries below don't need their own filter — they look up tables in
// the map populated here, and filtered-away tables simply aren't found.
func introspect(ctx context.Context, conn *pgx.Conn, opts Options) (*schemaSnapshot, error) {
	tables, err := queryTablesAndColumns(ctx, conn, opts)
	if err != nil {
		return nil, fmt.Errorf("queryTablesAndColumns: %w", err)
	}
	if err := annotateConstraints(ctx, conn, tables); err != nil {
		return nil, fmt.Errorf("annotateConstraints: %w", err)
	}
	if err := annotateIndexes(ctx, conn, tables); err != nil {
		return nil, fmt.Errorf("annotateIndexes: %w", err)
	}
	if err := annotateFKReferences(ctx, conn, tables, opts); err != nil {
		return nil, fmt.Errorf("annotateFKReferences: %w", err)
	}

	// Materialize tables map into a sorted slice for stable output.
	tableList := make([]tableInfo, 0, len(tables))
	for _, t := range tables {
		tableList = append(tableList, *t)
	}
	sortTables(tableList)

	views, err := queryViews(ctx, conn, opts)
	if err != nil {
		return nil, fmt.Errorf("queryViews: %w", err)
	}
	functions, err := queryFunctions(ctx, conn, opts)
	if err != nil {
		return nil, fmt.Errorf("queryFunctions: %w", err)
	}

	snap := &schemaSnapshot{
		tables:    tableList,
		views:     views,
		functions: functions,
	}

	// Row sampling (opt-in; SampleRows > 0).
	if opts.SampleRows > 0 {
		appendRowSamples(ctx, conn, snap, opts)
	}

	return snap, nil
}

// queryTablesAndColumns lists every user table and its columns in one
// pass. The schema-exclusion filter is the canonical "skip Postgres
// internals" set; v0.7.2's KEN_DB_SCHEMAS / KEN_DB_EXCLUDE_SCHEMAS are
// applied per-row via filterSchema so the SQL stays simple (one
// canonical filter source instead of dynamic IN-list parameterization).
//
// Returns a map keyed by "schema.name" for O(1) annotation by the
// follow-on queries (which join back to tables by their natural keys).
func queryTablesAndColumns(ctx context.Context, conn *pgx.Conn, opts Options) (map[string]*tableInfo, error) {
	const q = `
SELECT
    t.table_schema,
    t.table_name,
    c.column_name,
    -- format_type produces the human-friendly form ("character varying(255)" → keep)
    -- but we further normalize "character varying" → "varchar" in renderer for brevity.
    pg_catalog.format_type(a.atttypid, a.atttypmod) AS data_type,
    a.attnotnull AS not_null,
    pg_get_expr(d.adbin, d.adrelid) AS default_expr
FROM information_schema.tables t
JOIN pg_catalog.pg_class cl
     ON cl.relname = t.table_name
JOIN pg_catalog.pg_namespace n
     ON n.oid = cl.relnamespace AND n.nspname = t.table_schema
JOIN information_schema.columns c
     ON c.table_schema = t.table_schema AND c.table_name = t.table_name
JOIN pg_catalog.pg_attribute a
     ON a.attrelid = cl.oid AND a.attname = c.column_name AND a.attnum > 0
LEFT JOIN pg_catalog.pg_attrdef d
     ON d.adrelid = cl.oid AND d.adnum = a.attnum
WHERE t.table_type = 'BASE TABLE'
  AND t.table_schema NOT IN ('pg_catalog', 'information_schema')
  AND t.table_schema NOT LIKE 'pg_%'
ORDER BY t.table_schema, t.table_name, c.ordinal_position;
`
	rows, err := conn.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]*tableInfo{}
	for rows.Next() {
		var schema, name, colName, dataType string
		var notNull bool
		var defaultExpr *string
		if err := rows.Scan(&schema, &name, &colName, &dataType, &notNull, &defaultExpr); err != nil {
			return nil, err
		}
		if !filterSchema(schema, "postgres", opts) {
			continue
		}
		key := schema + "." + name
		t, ok := out[key]
		if !ok {
			t = &tableInfo{schema: schema, name: name}
			out[key] = t
		}
		col := columnInfo{
			name:     colName,
			dataType: normalizeType(dataType),
			notNull:  notNull,
		}
		if defaultExpr != nil {
			col.defaultExpr = *defaultExpr
		}
		t.columns = append(t.columns, col)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// annotateConstraints attaches PK / UNIQUE / FK markers + FK targets to
// the columns of tables collected by queryTablesAndColumns.
func annotateConstraints(ctx context.Context, conn *pgx.Conn, tables map[string]*tableInfo) error {
	// PK + UNIQUE: information_schema.table_constraints joined to
	// key_column_usage gives us (table, column, constraint_type).
	const q1 = `
SELECT
    tc.table_schema,
    tc.table_name,
    kcu.column_name,
    tc.constraint_type
FROM information_schema.table_constraints tc
JOIN information_schema.key_column_usage kcu
  ON kcu.constraint_schema = tc.constraint_schema
 AND kcu.constraint_name   = tc.constraint_name
 AND kcu.table_schema      = tc.table_schema
 AND kcu.table_name        = tc.table_name
WHERE tc.table_schema NOT IN ('pg_catalog', 'information_schema')
  AND tc.constraint_type IN ('PRIMARY KEY', 'UNIQUE');
`
	rows, err := conn.Query(ctx, q1)
	if err != nil {
		return err
	}
	for rows.Next() {
		var schema, name, col, ctype string
		if err := rows.Scan(&schema, &name, &col, &ctype); err != nil {
			rows.Close()
			return err
		}
		t, ok := tables[schema+"."+name]
		if !ok {
			continue
		}
		for i := range t.columns {
			if t.columns[i].name == col {
				if ctype == "PRIMARY KEY" {
					t.columns[i].isPrimaryKey = true
				} else if ctype == "UNIQUE" {
					t.columns[i].isUnique = true
				}
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// FK targets: same joins but constraint_type='FOREIGN KEY', and
	// pull the referenced (schema, table, column).
	const q2 = `
SELECT
    tc.table_schema,
    tc.table_name,
    kcu.column_name,
    ccu.table_schema AS ref_schema,
    ccu.table_name   AS ref_table,
    ccu.column_name  AS ref_column
FROM information_schema.table_constraints tc
JOIN information_schema.key_column_usage kcu
  ON kcu.constraint_schema = tc.constraint_schema
 AND kcu.constraint_name   = tc.constraint_name
JOIN information_schema.constraint_column_usage ccu
  ON ccu.constraint_schema = tc.constraint_schema
 AND ccu.constraint_name   = tc.constraint_name
WHERE tc.constraint_type = 'FOREIGN KEY'
  AND tc.table_schema NOT IN ('pg_catalog', 'information_schema');
`
	rows2, err := conn.Query(ctx, q2)
	if err != nil {
		return err
	}
	defer rows2.Close()
	for rows2.Next() {
		var fromSchema, fromTable, fromCol, refSchema, refTable, refCol string
		if err := rows2.Scan(&fromSchema, &fromTable, &fromCol, &refSchema, &refTable, &refCol); err != nil {
			return err
		}
		t, ok := tables[fromSchema+"."+fromTable]
		if !ok {
			continue
		}
		for i := range t.columns {
			if t.columns[i].name == fromCol {
				t.columns[i].fkTarget = fmt.Sprintf("%s(%s)", qualifiedName(refSchema, refTable), refCol)
			}
		}
	}
	return rows2.Err()
}

// annotateIndexes attaches pg_indexes.indexdef rows to each table.
// We let Postgres tell us how the index was created (raw indexdef) so we
// don't have to re-render it.
func annotateIndexes(ctx context.Context, conn *pgx.Conn, tables map[string]*tableInfo) error {
	const q = `
SELECT
    schemaname,
    tablename,
    indexname,
    indexdef
FROM pg_indexes
WHERE schemaname NOT IN ('pg_catalog', 'information_schema')
  AND schemaname NOT LIKE 'pg_%';
`
	rows, err := conn.Query(ctx, q)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var schema, table, name, indexdef string
		if err := rows.Scan(&schema, &table, &name, &indexdef); err != nil {
			return err
		}
		t, ok := tables[schema+"."+table]
		if !ok {
			continue
		}
		// "CREATE UNIQUE INDEX ..." vs "CREATE INDEX ..." discriminates uniqueness.
		unique := strings.Contains(strings.ToUpper(indexdef), "CREATE UNIQUE INDEX")
		t.indexes = append(t.indexes, indexInfo{
			name:     name,
			unique:   unique,
			indexdef: indexdef,
		})
	}
	return rows.Err()
}

// annotateFKReferences populates tableInfo.fkReferenced with the
// "this table is FK-referenced BY <other>(col)" relationships, the
// inverse of what annotateConstraints captured per-column. Surfacing
// both directions is the prompt's stated requirement.
//
// v0.7.2: skips inverse-FK entries from filtered-away schemas so the
// "FK referenced by:" rendered list never names a schema the operator
// explicitly excluded via KEN_DB_SCHEMAS / KEN_DB_EXCLUDE_SCHEMAS. The
// referenced-side filter is already enforced by table-map membership
// (tables map only contains schemas that passed filterSchema), so we
// only need to filter the from-side here.
func annotateFKReferences(ctx context.Context, conn *pgx.Conn, tables map[string]*tableInfo, opts Options) error {
	const q = `
SELECT
    ccu.table_schema  AS ref_schema,
    ccu.table_name    AS ref_table,
    tc.table_schema   AS from_schema,
    tc.table_name     AS from_table,
    kcu.column_name   AS from_column
FROM information_schema.table_constraints tc
JOIN information_schema.key_column_usage kcu
  ON kcu.constraint_schema = tc.constraint_schema
 AND kcu.constraint_name   = tc.constraint_name
JOIN information_schema.constraint_column_usage ccu
  ON ccu.constraint_schema = tc.constraint_schema
 AND ccu.constraint_name   = tc.constraint_name
WHERE tc.constraint_type = 'FOREIGN KEY'
  AND tc.table_schema NOT IN ('pg_catalog', 'information_schema');
`
	rows, err := conn.Query(ctx, q)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var refSchema, refTable, fromSchema, fromTable, fromCol string
		if err := rows.Scan(&refSchema, &refTable, &fromSchema, &fromTable, &fromCol); err != nil {
			return err
		}
		t, ok := tables[refSchema+"."+refTable]
		if !ok {
			continue
		}
		if !filterSchema(fromSchema, "postgres", opts) {
			continue
		}
		t.fkReferenced = append(t.fkReferenced, fkRef{
			fromSchema: fromSchema,
			fromTable:  fromTable,
			fromColumn: fromCol,
		})
	}
	return rows.Err()
}

// queryViews lists every user view with its definition. The definition
// may be long (analytics CTEs etc.); the renderer truncates if needed.
// v0.7.2: respects opts.IncludeSchemas / ExcludeSchemas via filterSchema.
func queryViews(ctx context.Context, conn *pgx.Conn, opts Options) ([]viewInfo, error) {
	const q = `
SELECT
    table_schema,
    table_name,
    view_definition
FROM information_schema.views
WHERE table_schema NOT IN ('pg_catalog', 'information_schema')
  AND table_schema NOT LIKE 'pg_%'
ORDER BY table_schema, table_name;
`
	rows, err := conn.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []viewInfo
	for rows.Next() {
		var v viewInfo
		var def *string
		if err := rows.Scan(&v.schema, &v.name, &def); err != nil {
			return nil, err
		}
		if !filterSchema(v.schema, "postgres", opts) {
			continue
		}
		if def != nil {
			v.definition = *def
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// queryFunctions returns user-defined functions/procedures by their
// signatures only. Bodies are deliberately NOT indexed in v0.7.0 —
// function bodies vary wildly in language (PL/pgSQL, SQL, C-callable,
// Python via plpython3u) and the signature is the high-signal indexing
// target. Body indexing is a v0.x+ refinement.
// v0.7.2: respects opts.IncludeSchemas / ExcludeSchemas via filterSchema.
func queryFunctions(ctx context.Context, conn *pgx.Conn, opts Options) ([]functionInfo, error) {
	const q = `
SELECT
    n.nspname AS schema,
    p.proname AS name,
    pg_catalog.pg_get_function_arguments(p.oid) AS args,
    pg_catalog.pg_get_function_result(p.oid) AS result
FROM pg_catalog.pg_proc p
JOIN pg_catalog.pg_namespace n
  ON n.oid = p.pronamespace
WHERE n.nspname NOT IN ('pg_catalog', 'information_schema')
  AND n.nspname NOT LIKE 'pg_%'
  -- Exclude functions implementing operators / aggregates / window funcs:
  AND p.prokind = 'f'
ORDER BY n.nspname, p.proname;
`
	rows, err := conn.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []functionInfo
	for rows.Next() {
		var f functionInfo
		var result *string
		if err := rows.Scan(&f.schema, &f.name, &f.argSig, &result); err != nil {
			return nil, err
		}
		if !filterSchema(f.schema, "postgres", opts) {
			continue
		}
		// Wrap args in parens for the rendered signature.
		f.argSig = "(" + f.argSig + ")"
		if result != nil {
			f.returnT = *result
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// qualifiedName returns "schema.name", omitting the schema when it's
// "public" (the Postgres default) for a cleaner display.
func qualifiedName(schema, name string) string {
	if schema == "" || schema == "public" {
		return name
	}
	return schema + "." + name
}

// normalizeType maps Postgres canonical type names to their commonly-
// written shorthand: "character varying(255)" → "varchar(255)",
// "timestamp without time zone" → "timestamp", etc. Pure cosmetics —
// makes the rendered chunks read like hand-written DDL.
func normalizeType(t string) string {
	t = strings.ReplaceAll(t, "character varying", "varchar")
	t = strings.ReplaceAll(t, "character(", "char(")
	t = strings.ReplaceAll(t, "timestamp without time zone", "timestamp")
	t = strings.ReplaceAll(t, "timestamp with time zone", "timestamptz")
	t = strings.ReplaceAll(t, "time without time zone", "time")
	t = strings.ReplaceAll(t, "time with time zone", "timetz")
	return t
}

// sortTables orders tables by (schema, name) so chunk emission is
// deterministic across runs.
func sortTables(ts []tableInfo) {
	for i := 1; i < len(ts); i++ {
		for j := i; j > 0; j-- {
			if compareTable(ts[j], ts[j-1]) < 0 {
				ts[j], ts[j-1] = ts[j-1], ts[j]
			} else {
				break
			}
		}
	}
}

func compareTable(a, b tableInfo) int {
	if a.schema != b.schema {
		if a.schema < b.schema {
			return -1
		}
		return 1
	}
	if a.name < b.name {
		return -1
	}
	if a.name > b.name {
		return 1
	}
	return 0
}

// appendRowSamples is a forward declaration — implementation lives in
// sample.go. Stubbed here so introspect.go compiles ahead of sample.go.
// The real version pulls SampleRows rows per table and attaches them.
var appendRowSamples = func(ctx context.Context, conn *pgx.Conn, snap *schemaSnapshot, opts Options) {
	// no-op by default; sample.go's init() overwrites this with the
	// real sampler.
}
