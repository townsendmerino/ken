package db

// MySQL engine for Tier 2 (v0.7.2 / ADR-019).
//
// Pure-Go via github.com/go-sql-driver/mysql — no cgo. Compatible with
// MySQL 5.7+, MySQL 8.x, and MariaDB 10.x+ (wire-compatible via the
// same driver; MariaDB-specific INFORMATION_SCHEMA differences are
// documented compatibility, not first-class testing).
//
// ── Driver audit (stdout cleanliness) ───────────────────────────────
// go-sql-driver/mysql's package-level logger defaults to
// log.New(os.Stderr, "[mysql] ", log.Ldate|log.Ltime) — stderr already.
// cmd/ken-mcp's init() also reroutes the stdlib `log` package to
// stderr as belt-and-suspenders. TestBinary_StdoutIsCleanJSONRPC_WithMySQL
// pins this — any future driver upgrade that silently switches to
// stdout would fail that test.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"golang.org/x/sync/errgroup"

	"github.com/townsendmerino/aikit/chunk"
)

// mysqlIntDisplayWidth matches the legacy integer display-width suffix
// (e.g. "bigint(20)", "int(11)") MariaDB 10.x / 11.x still emits for
// integer-family columns + scalar-function return types. MySQL 8.0
// deprecated and removed these widths (MySQL bug #80094, "Deprecate the
// display width of integer types"); MySQL 8.4 now returns the bare
// "bigint" / "int" form. ADR-019's v0.7.2 chunks were captured against
// MySQL 8.x and use the bare form; v0.8.1 Part B normalizes MariaDB
// output to match so cross-engine chunk text stays identical. See
// ADR-021 for the full divergence audit + the rejected probe-and-branch
// alternative.
var mysqlIntDisplayWidth = regexp.MustCompile(`\b(bigint|int|mediumint|smallint|tinyint)\(\d+\)`)

// normalizeMySQLIntType strips the legacy integer display-width suffix
// from a MySQL/MariaDB type expression. Idempotent: MySQL 8.x output
// already lacks the suffix, so the regex matches nothing and the
// string is returned unchanged. The transform preserves modifiers
// downstream of the type ("bigint(20) unsigned" → "bigint unsigned").
//
// Limited to integer families because:
//   - char/varchar/binary/varbinary genuinely need their (N) (the size
//     IS the type's semantic) and both engines emit them identically.
//   - decimal/numeric have (precision,scale) which is also semantic.
//   - enum/set have ('a','b','c') member lists — wholly different shape.
//
// Only the integer family lost its display-width parameter in MySQL 8.0;
// only the integer family needs normalization.
func normalizeMySQLIntType(s string) string {
	return mysqlIntDisplayWidth.ReplaceAllString(s, "$1")
}

// mysqlDB is the engine label used in the freshness header
// ("-- indexed at ... from mysql@<host>"). Distinct from the driver
// name passed to sql.Open ("mysql").
const mysqlDB = "mysql"

// mysqlDefaultPort matches the documented default. Non-default ports
// are surfaced in the freshness header so operators with multiple
// instances can disambiguate.
const mysqlDefaultPort = "3306"

// indexSchemaMySQL is the MySQL arm of IndexSchema's engine dispatch.
// Connects to opts.DSN (URL or native go-sql-driver form), introspects,
// optionally samples rows, and returns the rendered chunks.
//
// opts.DSN accepts:
//   - mysql://user:pass@host:3306/db?parseTime=true   (URL form)
//   - user:pass@tcp(host:3306)/db?parseTime=true       (native form)
//   - user:pass@unix(/path/to/socket)/db?...           (native unix-socket form)
//
// parseTime=true is forced if absent — without it, DATE/DATETIME/TIMESTAMP
// columns return []byte, which doesn't render cleanly via formatCell.
// Documented in the README as a footgun for operators who omit it.
func indexSchemaMySQL(ctx context.Context, opts Options) ([]chunk.Chunk, error) {
	opts = opts.validate()
	if opts.DSN == "" {
		return nil, nil
	}

	cfg, err := parseMySQLDSN(opts.DSN)
	if err != nil {
		return nil, fmt.Errorf("db: mysql DSN: %w", err)
	}

	conn, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, fmt.Errorf("db: mysql open: %w", err)
	}
	// Cap the pool at sampleWorkers() so the parallel sample fetch in
	// mysqlAppendSamples never exceeds the configured worker count.
	// Without this cap, *sql.DB defaults to unlimited connections and a
	// burst of 8+ goroutines could exhaust shared MySQL max_connections.
	// ADR-031.
	conn.SetMaxOpenConns(sampleWorkers())
	defer func() { _ = conn.Close() }()

	if err := conn.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("db: mysql ping: %w", err)
	}

	snap, err := introspectMySQL(ctx, conn, opts)
	if err != nil {
		return nil, fmt.Errorf("db: mysql introspect: %w", err)
	}

	engineHost := mysqlEngineHost(cfg)
	header := freshnessHeader(engineHost, time.Now().UTC())
	pathPrefix := "db://" + engineHost

	var chunks []chunk.Chunk
	for _, t := range snap.tables {
		chunks = append(chunks, renderTableChunk(t, snap, header, pathPrefix))
	}
	for _, v := range snap.views {
		chunks = append(chunks, renderViewChunk(v, header, pathPrefix))
	}
	for _, f := range snap.functions {
		chunks = append(chunks, renderFunctionChunk(f, header, pathPrefix))
	}
	return chunks, nil
}

// parseMySQLDSN normalizes a DSN to a *mysql.Config the driver
// understands. URL form is rewritten to the native form first, then
// handed to mysql.ParseDSN — the native form is the engine's canonical
// shape. parseTime=true is always set.
func parseMySQLDSN(dsn string) (*mysql.Config, error) {
	native := dsn
	if strings.Contains(dsn, "://") {
		converted, err := mysqlURLToNative(dsn)
		if err != nil {
			return nil, fmt.Errorf("parse URL DSN: %w", err)
		}
		native = converted
	}
	cfg, err := mysql.ParseDSN(native)
	if err != nil {
		return nil, fmt.Errorf("mysql.ParseDSN: %w", err)
	}
	// Force parseTime=true for time.Time columns. Operators paste DSNs
	// from .env files that often omit this and end up with []byte
	// cells in sample-row rendering.
	cfg.ParseTime = true
	return cfg, nil
}

// mysqlURLToNative converts mysql://user:pass@host:port/db?param=value
// to the driver's native user:pass@tcp(host:port)/db?param=value. The
// driver doesn't accept the URL form directly — every wrapper library
// that documents URL-style DSNs does this conversion internally.
func mysqlURLToNative(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(u.Scheme, "mysql") {
		return "", fmt.Errorf("unsupported scheme %q (want mysql://)", u.Scheme)
	}
	if u.Host == "" {
		return "", errors.New("URL DSN missing host")
	}
	user := ""
	pass := ""
	if u.User != nil {
		user = u.User.Username()
		if p, ok := u.User.Password(); ok {
			pass = p
		}
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = mysqlDefaultPort
	}
	db := strings.TrimPrefix(u.Path, "/")

	var b strings.Builder
	if user != "" {
		b.WriteString(user)
		if pass != "" {
			b.WriteByte(':')
			b.WriteString(pass)
		}
		b.WriteByte('@')
	}
	fmt.Fprintf(&b, "tcp(%s:%s)/%s", host, port, db)
	if u.RawQuery != "" {
		b.WriteByte('?')
		b.WriteString(u.RawQuery)
	}
	return b.String(), nil
}

// mysqlEngineHost renders the "mysql@host" portion of the freshness
// header from a parsed *mysql.Config. Mirrors engineHostFromConfig's
// credential discipline: NEVER includes user / password.
//
// Network DSNs render as mysql@host[:port] (port only when non-default).
// Unix-socket DSNs render as mysql@unix-<basename> so chunks don't leak
// the full filesystem path of the socket.
func mysqlEngineHost(cfg *mysql.Config) string {
	switch cfg.Net {
	case "unix":
		// Addr is the socket path; show the basename only to stay
		// consistent with the SQLite engine's basename-only policy.
		base := cfg.Addr
		if i := strings.LastIndex(base, "/"); i >= 0 {
			base = base[i+1:]
		}
		if base == "" {
			base = "socket"
		}
		return mysqlDB + "@unix-" + base
	default:
		// tcp (default) or any future network — show host[:port].
		host, port, ok := splitHostPort(cfg.Addr)
		if !ok {
			// Defensive: if Addr isn't host:port, render it verbatim.
			return mysqlDB + "@" + cfg.Addr
		}
		if needsIPv6Brackets(host) {
			host = "[" + host + "]"
		}
		if port != "" && port != mysqlDefaultPort {
			return mysqlDB + "@" + host + ":" + port
		}
		return mysqlDB + "@" + host
	}
}

// splitHostPort splits "host:port" into its components. Returns false
// if no colon (host without port). Handles bracketed IPv6 like
// "[::1]:3306" → ("::1", "3306"). Standalone IPv6 like "::1" (no
// brackets, no trailing port) falls through as (full string, "", false).
func splitHostPort(addr string) (host, port string, ok bool) {
	if addr == "" {
		return "", "", false
	}
	if addr[0] == '[' {
		end := strings.Index(addr, "]")
		if end < 0 {
			return "", "", false
		}
		host = addr[1:end]
		rest := addr[end+1:]
		if strings.HasPrefix(rest, ":") {
			port = rest[1:]
		}
		return host, port, true
	}
	// Simple "host:port" (single colon).
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		// Reject bare IPv6 (more than one colon, no brackets).
		if strings.Count(addr, ":") == 1 {
			return addr[:i], addr[i+1:], true
		}
	}
	return addr, "", false
}

// introspectMySQL is the MySQL analogue of introspect(). Produces the
// same schemaSnapshot shape so emit.go's renderers don't need engine-
// specific branches.
func introspectMySQL(ctx context.Context, conn *sql.DB, opts Options) (*schemaSnapshot, error) {
	tables, err := mysqlListTablesAndColumns(ctx, conn, opts)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}

	// Build a lookup keyed by (schema, table) so the FK annotation pass
	// can find the referenced table when populating fkReferenced. Same
	// shape as introspect.go's `tables` map.
	tableMap := make(map[string]*tableInfo, len(tables))
	for i := range tables {
		t := &tables[i]
		tableMap[qualifiedKey(t.schema, t.name)] = t
	}

	if err := mysqlAnnotateConstraints(ctx, conn, tableMap, opts); err != nil {
		return nil, fmt.Errorf("annotateConstraints: %w", err)
	}
	if err := mysqlAnnotateIndexes(ctx, conn, tableMap, opts); err != nil {
		return nil, fmt.Errorf("annotateIndexes: %w", err)
	}
	if err := mysqlAnnotateFKReferences(ctx, conn, tableMap, opts); err != nil {
		return nil, fmt.Errorf("annotateFKReferences: %w", err)
	}

	sortTables(tables)

	views, err := mysqlListViews(ctx, conn, opts)
	if err != nil {
		return nil, fmt.Errorf("list views: %w", err)
	}

	functions, err := mysqlListRoutines(ctx, conn, opts)
	if err != nil {
		return nil, fmt.Errorf("list routines: %w", err)
	}

	snap := &schemaSnapshot{tables: tables, views: views, functions: functions}

	if opts.SampleRows > 0 {
		mysqlAppendSamples(ctx, conn, snap, opts)
	}

	return snap, nil
}

// mysqlListTablesAndColumns lists base tables and their columns from
// information_schema. Filters non-system rows via filterSchema; the
// SQL excludes the system-schema set up front as a defense-in-depth
// (the helper rejects the same names but the WHERE clause keeps the
// network traffic small on big servers).
func mysqlListTablesAndColumns(ctx context.Context, conn *sql.DB, opts Options) ([]tableInfo, error) {
	const q = `
SELECT
    t.table_schema,
    t.table_name,
    c.column_name,
    c.column_type,
    c.is_nullable,
    c.column_default,
    c.extra
FROM information_schema.tables t
JOIN information_schema.columns c
  ON c.table_schema = t.table_schema
 AND c.table_name   = t.table_name
WHERE t.table_type = 'BASE TABLE'
  AND t.table_schema NOT IN ('information_schema', 'mysql', 'performance_schema', 'sys')
ORDER BY t.table_schema, t.table_name, c.ordinal_position;
`
	rows, err := conn.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tmp := map[string]*tableInfo{}
	var order []string
	for rows.Next() {
		var (
			schema, name, colName, colType, isNullable, extra string
			defaultExpr                                       sql.NullString
		)
		if err := rows.Scan(&schema, &name, &colName, &colType, &isNullable, &defaultExpr, &extra); err != nil {
			return nil, err
		}
		if !filterSchema(schema, "mysql", opts) {
			continue
		}
		key := schema + "." + name
		t, ok := tmp[key]
		if !ok {
			t = &tableInfo{schema: schema, name: name}
			tmp[key] = t
			order = append(order, key)
		}
		col := columnInfo{
			name:     colName,
			dataType: normalizeMySQLIntType(strings.ToLower(colType)),
			notNull:  !strings.EqualFold(isNullable, "YES"),
		}
		if defaultExpr.Valid {
			col.defaultExpr = defaultExpr.String
		}
		// MySQL surfaces AUTO_INCREMENT and stored-generated markers in
		// the `extra` column. We render the AUTO_INCREMENT marker into
		// defaultExpr so it shows up via columnModifiers' DEFAULT slot.
		if strings.Contains(strings.ToUpper(extra), "AUTO_INCREMENT") {
			if col.defaultExpr == "" {
				col.defaultExpr = "AUTO_INCREMENT"
			} else {
				col.defaultExpr = col.defaultExpr + " AUTO_INCREMENT"
			}
		}
		t.columns = append(t.columns, col)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]tableInfo, 0, len(order))
	for _, key := range order {
		out = append(out, *tmp[key])
	}
	return out, nil
}

// mysqlAnnotateConstraints attaches PK / UNIQUE markers and FK targets
// to columns. Two passes: PK/UNIQUE from table_constraints, FK from
// key_column_usage + referential_constraints.
func mysqlAnnotateConstraints(ctx context.Context, conn *sql.DB, tables map[string]*tableInfo, opts Options) error {
	// PK + UNIQUE markers.
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
WHERE tc.table_schema NOT IN ('information_schema', 'mysql', 'performance_schema', 'sys')
  AND tc.constraint_type IN ('PRIMARY KEY', 'UNIQUE');
`
	rows, err := conn.QueryContext(ctx, q1)
	if err != nil {
		return err
	}
	for rows.Next() {
		var schema, name, col, ctype string
		if err := rows.Scan(&schema, &name, &col, &ctype); err != nil {
			rows.Close()
			return err
		}
		if !filterSchema(schema, "mysql", opts) {
			continue
		}
		t, ok := tables[schema+"."+name]
		if !ok {
			continue
		}
		for i := range t.columns {
			if t.columns[i].name == col {
				switch ctype {
				case "PRIMARY KEY":
					t.columns[i].isPrimaryKey = true
				case "UNIQUE":
					t.columns[i].isUnique = true
				}
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// FK targets.
	const q2 = `
SELECT
    kcu.table_schema,
    kcu.table_name,
    kcu.column_name,
    kcu.referenced_table_schema,
    kcu.referenced_table_name,
    kcu.referenced_column_name
FROM information_schema.key_column_usage kcu
WHERE kcu.referenced_table_name IS NOT NULL
  AND kcu.table_schema NOT IN ('information_schema', 'mysql', 'performance_schema', 'sys');
`
	rows2, err := conn.QueryContext(ctx, q2)
	if err != nil {
		return err
	}
	defer rows2.Close()
	for rows2.Next() {
		var fromSchema, fromTable, fromCol, refSchema, refTable, refCol string
		if err := rows2.Scan(&fromSchema, &fromTable, &fromCol, &refSchema, &refTable, &refCol); err != nil {
			return err
		}
		if !filterSchema(fromSchema, "mysql", opts) {
			continue
		}
		t, ok := tables[fromSchema+"."+fromTable]
		if !ok {
			continue
		}
		for i := range t.columns {
			if t.columns[i].name == fromCol {
				t.columns[i].fkTarget = fmt.Sprintf("%s(%s)", mysqlQualifiedName(refSchema, refTable), refCol)
			}
		}
	}
	return rows2.Err()
}

// mysqlAnnotateIndexes attaches per-index metadata via
// information_schema.statistics, which has one row per (index, column).
// We aggregate to one indexInfo per index name.
func mysqlAnnotateIndexes(ctx context.Context, conn *sql.DB, tables map[string]*tableInfo, opts Options) error {
	// non_unique = 0 → UNIQUE, = 1 → not unique. PRIMARY index name is
	// skipped (the PK columns already carry the marker).
	const q = `
SELECT
    table_schema,
    table_name,
    index_name,
    non_unique,
    column_name,
    seq_in_index
FROM information_schema.statistics
WHERE table_schema NOT IN ('information_schema', 'mysql', 'performance_schema', 'sys')
  AND index_name <> 'PRIMARY'
ORDER BY table_schema, table_name, index_name, seq_in_index;
`
	rows, err := conn.QueryContext(ctx, q)
	if err != nil {
		return err
	}
	defer rows.Close()

	type idxKey struct{ schema, table, name string }
	type idxAcc struct {
		unique bool
		cols   []string
	}
	acc := map[idxKey]*idxAcc{}
	var order []idxKey

	for rows.Next() {
		var (
			schema, table, name, colName string
			nonUnique                    int
			seq                          int
		)
		if err := rows.Scan(&schema, &table, &name, &nonUnique, &colName, &seq); err != nil {
			return err
		}
		if !filterSchema(schema, "mysql", opts) {
			continue
		}
		if _, ok := tables[schema+"."+table]; !ok {
			continue
		}
		k := idxKey{schema, table, name}
		a, ok := acc[k]
		if !ok {
			a = &idxAcc{unique: nonUnique == 0}
			acc[k] = a
			order = append(order, k)
		}
		a.cols = append(a.cols, colName)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, k := range order {
		a := acc[k]
		t := tables[k.schema+"."+k.table]
		uniqueKW := ""
		if a.unique {
			uniqueKW = " UNIQUE"
		}
		// Render in the same shape extractIndexColumns understands
		// (parens around the column list); the renderer will pull cols
		// back out via extractIndexColumns + emit "INDEX <name> ON (<cols>)".
		indexdef := fmt.Sprintf("CREATE%s INDEX %s ON %s (%s)",
			uniqueKW, k.name, k.table, strings.Join(a.cols, ", "))
		t.indexes = append(t.indexes, indexInfo{
			name:     k.name,
			unique:   a.unique,
			indexdef: indexdef,
		})
	}
	return nil
}

// mysqlAnnotateFKReferences populates the inverse FK list ("this table
// is FK-referenced BY <other>(col)"). The MySQL key_column_usage view
// already names both sides, so this is one query.
func mysqlAnnotateFKReferences(ctx context.Context, conn *sql.DB, tables map[string]*tableInfo, opts Options) error {
	const q = `
SELECT
    kcu.referenced_table_schema,
    kcu.referenced_table_name,
    kcu.table_schema,
    kcu.table_name,
    kcu.column_name
FROM information_schema.key_column_usage kcu
WHERE kcu.referenced_table_name IS NOT NULL
  AND kcu.table_schema NOT IN ('information_schema', 'mysql', 'performance_schema', 'sys');
`
	rows, err := conn.QueryContext(ctx, q)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var refSchema, refTable, fromSchema, fromTable, fromCol string
		if err := rows.Scan(&refSchema, &refTable, &fromSchema, &fromTable, &fromCol); err != nil {
			return err
		}
		if !filterSchema(fromSchema, "mysql", opts) {
			continue
		}
		t, ok := tables[refSchema+"."+refTable]
		if !ok {
			// Referenced table itself is filtered out — drop the entry.
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

// mysqlListViews lists views with their definitions. Truncation happens
// at render time (maxViewBodyLines).
func mysqlListViews(ctx context.Context, conn *sql.DB, opts Options) ([]viewInfo, error) {
	const q = `
SELECT
    table_schema,
    table_name,
    view_definition
FROM information_schema.views
WHERE table_schema NOT IN ('information_schema', 'mysql', 'performance_schema', 'sys')
ORDER BY table_schema, table_name;
`
	rows, err := conn.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []viewInfo
	for rows.Next() {
		var schema, name string
		var def sql.NullString
		if err := rows.Scan(&schema, &name, &def); err != nil {
			return nil, err
		}
		if !filterSchema(schema, "mysql", opts) {
			continue
		}
		v := viewInfo{schema: schema, name: name}
		if def.Valid {
			v.definition = def.String
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// mysqlListRoutines lists stored procedures + functions with their
// parameter / return signatures. Body deliberately NOT indexed (parallel
// to the Postgres policy: signature is the high-signal target).
func mysqlListRoutines(ctx context.Context, conn *sql.DB, opts Options) ([]functionInfo, error) {
	// Routines (header rows) — one per procedure/function.
	const qRoutines = `
SELECT
    routine_schema,
    routine_name,
    routine_type,
    dtd_identifier
FROM information_schema.routines
WHERE routine_schema NOT IN ('information_schema', 'mysql', 'performance_schema', 'sys')
ORDER BY routine_schema, routine_name;
`
	rows, err := conn.QueryContext(ctx, qRoutines)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type key struct{ schema, name string }
	type routineMeta struct {
		schema, name, routineType string
		returnT                   string
	}
	var metas []routineMeta
	for rows.Next() {
		var schema, name, rtype string
		var ret sql.NullString
		if err := rows.Scan(&schema, &name, &rtype, &ret); err != nil {
			return nil, err
		}
		if !filterSchema(schema, "mysql", opts) {
			continue
		}
		m := routineMeta{schema: schema, name: name, routineType: rtype}
		if strings.EqualFold(rtype, "FUNCTION") && ret.Valid {
			m.returnT = normalizeMySQLIntType(strings.ToLower(ret.String))
		}
		metas = append(metas, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Params — one row per parameter; ordinal_position 0 is the FUNCTION's
	// implicit return slot (which we ignore — we already have dtd_identifier
	// for that). Aggregated into the routine's argument signature.
	const qParams = `
SELECT
    specific_schema,
    specific_name,
    parameter_name,
    dtd_identifier,
    ordinal_position
FROM information_schema.parameters
WHERE specific_schema NOT IN ('information_schema', 'mysql', 'performance_schema', 'sys')
  AND ordinal_position > 0
ORDER BY specific_schema, specific_name, ordinal_position;
`
	paramRows, err := conn.QueryContext(ctx, qParams)
	if err != nil {
		return nil, err
	}
	defer paramRows.Close()

	argSigs := map[key][]string{}
	for paramRows.Next() {
		var schema, name string
		var paramName sql.NullString
		var dataType string
		var pos int
		if err := paramRows.Scan(&schema, &name, &paramName, &dataType, &pos); err != nil {
			return nil, err
		}
		if !filterSchema(schema, "mysql", opts) {
			continue
		}
		k := key{schema, name}
		// "name type" if the parameter has a name, else just "type".
		entry := normalizeMySQLIntType(strings.ToLower(dataType))
		if paramName.Valid && paramName.String != "" {
			entry = paramName.String + " " + entry
		}
		argSigs[k] = append(argSigs[k], entry)
	}
	if err := paramRows.Err(); err != nil {
		return nil, err
	}

	out := make([]functionInfo, 0, len(metas))
	for _, m := range metas {
		sig := "(" + strings.Join(argSigs[key{m.schema, m.name}], ", ") + ")"
		out = append(out, functionInfo{
			schema:  m.schema,
			name:    m.name,
			argSig:  sig,
			returnT: m.returnT,
		})
	}
	return out, nil
}

// mysqlAppendSamples is the MySQL analogue of sample.go's
// sampleRowsImpl. Same per-table determinism (ORDER BY first PK, fall
// back to ORDER BY 1) and same cell-truncation policy.
//
// Approximate row counts come from information_schema.tables.table_rows
// (refreshed by MySQL's stats infrastructure; cheap, approximate). The
// rendered chunk shows "of ~N" matching Postgres.
func mysqlAppendSamples(ctx context.Context, conn *sql.DB, snap *schemaSnapshot, opts Options) {
	// Bulk-fetch approximate row counts for every table we care about.
	approx, err := mysqlApproxRowCounts(ctx, conn)
	if err != nil {
		warn(opts, "mysql: row-count query failed: %v", err)
	}

	// Approx-count attach + per-table sample fetch.
	//
	// Sample fetches are fanned out across up to sampleWorkers goroutines
	// via golang.org/x/sync/errgroup. *sql.DB is safe for concurrent use,
	// and each worker writes to a distinct slice element (&snap.tables[i])
	// so no cross-goroutine synchronization is needed for the per-table
	// results. v0.8.4 / ADR-031: introspection sample loop is ~57% of
	// total wall time on localhost (and dominates on remote DBs where
	// per-query latency is RTT-bound); parallelism cashes the Amdahl
	// ceiling.
	//
	// Workers return nil unconditionally — per-table failures are warned
	// and swallowed (matches the pre-parallel warn+continue semantics).
	// Returning err would cause errgroup.WithContext to cancel siblings
	// on the first failure, which is the wrong shape for a best-effort
	// sample pass.
	for i := range snap.tables {
		t := &snap.tables[i]
		if c, ok := approx[t.schema+"."+t.name]; ok {
			t.approxRowCount = c
		}
	}

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(sampleWorkers())
	for i := range snap.tables {
		t := &snap.tables[i]
		g.Go(func() error {
			orderClause := "ORDER BY 1"
			for _, c := range t.columns {
				if c.isPrimaryKey {
					orderClause = "ORDER BY " + mysqlQuoteIdent(c.name)
					break
				}
			}
			// Fully-qualified schema.table; MySQL backticks for identifier safety.
			q := fmt.Sprintf("SELECT * FROM %s.%s %s LIMIT ?",
				mysqlQuoteIdent(t.schema), mysqlQuoteIdent(t.name), orderClause)
			rows, err := conn.QueryContext(gctx, q, opts.SampleRows)
			if err != nil {
				warn(opts, "mysql: sample query failed for %s.%s: %v", t.schema, t.name, err)
				return nil
			}
			cols, err := rows.Columns()
			if err != nil {
				warn(opts, "mysql: column metadata for %s.%s: %v", t.schema, t.name, err)
				rows.Close()
				return nil
			}
			t.sampleColumns = cols
			var collected [][]string
			for rows.Next() {
				vals := make([]any, len(cols))
				ptrs := make([]any, len(cols))
				for j := range vals {
					ptrs[j] = &vals[j]
				}
				if err := rows.Scan(ptrs...); err != nil {
					warn(opts, "mysql: sample scan failed for %s.%s: %v", t.schema, t.name, err)
					continue
				}
				cells := make([]string, len(vals))
				for j, v := range vals {
					// go-sql-driver/mysql returns VARCHAR / CHAR / TEXT / JSON
					// as []byte rather than string (default driver behavior,
					// unlike pgx which returns string). Convert at the
					// driver boundary so formatCell renders them as the
					// string they actually are. Genuine binary columns
					// (BLOB / BINARY / VARBINARY) on MySQL also come back
					// as []byte, but we let those render as strings too —
					// formatCell's BYTEA path on Postgres still applies to
					// pgx's []byte, where pgx only returns it for actual
					// binary types.
					if b, ok := v.([]byte); ok {
						v = string(b)
					}
					cells[j] = truncateCell(formatCell(v))
				}
				collected = append(collected, cells)
			}
			rows.Close()
			t.sampleRows = collected
			return nil
		})
	}
	_ = g.Wait()
}

// sampleWorkers is the concurrency cap for parallel sample-row fetches
// in both engines. Capped at 8 to avoid exhausting MySQL/Postgres
// max_connections in shared dev/staging environments (commonly 50-150);
// the matching SetMaxOpenConns(8) on the introspection *sql.DB further
// guarantees we never exceed this. The min() with runtime.NumCPU() is
// defensive — on a 2-core box, 8 workers would still serialize at the
// scheduler anyway. ADR-031 documents the rationale.
func sampleWorkers() int {
	n := min(runtime.NumCPU(), 8)
	if n < 1 {
		n = 1
	}
	return n
}

// mysqlApproxRowCounts returns a (schema.table → table_rows) map. Free
// over a single information_schema.tables query; approximate (refreshed
// by MySQL's stats infrastructure / `ANALYZE TABLE`). Same shape as the
// Postgres queryApproxRowCounts helper.
func mysqlApproxRowCounts(ctx context.Context, conn *sql.DB) (map[string]float64, error) {
	const q = `
SELECT table_schema, table_name, COALESCE(table_rows, 0)
FROM information_schema.tables
WHERE table_type = 'BASE TABLE'
  AND table_schema NOT IN ('information_schema', 'mysql', 'performance_schema', 'sys');
`
	rows, err := conn.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var schema, name string
		var n int64
		if err := rows.Scan(&schema, &name, &n); err != nil {
			return nil, err
		}
		out[schema+"."+name] = float64(n)
	}
	return out, rows.Err()
}

// mysqlQuoteIdent backtick-quotes an identifier for safe SQL embedding.
// MySQL uses backticks (or `"name"` only with ANSI_QUOTES SQL mode);
// backticks work universally. Embedded backticks are doubled per
// MySQL's convention.
func mysqlQuoteIdent(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

// mysqlQualifiedName mirrors qualifiedName for Postgres: rendered
// "schema.table" — MySQL has no equivalent of Postgres's `public`
// schema (which qualifiedName elides), so we always show the schema.
// Operators reading the chunk get unambiguous database names, which
// matters for multi-db dev setups.
func mysqlQualifiedName(schema, name string) string {
	if schema == "" {
		return name
	}
	return schema + "." + name
}
