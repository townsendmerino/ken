package db

// SQLite engine for Tier 2 (v0.7.1 / ADR-018).
//
// Pure-Go, no cgo — modernc.org/sqlite is the C SQLite engine
// transpiled to Go, so it preserves ken's "single static cross-compiled
// binary" property. Default behavior is silent (no protocol-level
// logger that would write to stdout), preserving the cmd/ken-mcp
// JSON-RPC stdout-cleanliness contract; TestBinary_StdoutIsCleanJSONRPC_WithSQLite
// pins this.
//
// Engine routing happens in IndexSchema based on the DSN scheme. This
// file mirrors introspect.go's tableDef / viewDef / functionDef
// shape so the renderers in emit.go can be reused unchanged — the
// SQLite path produces the same chunk shape as Postgres modulo the
// "engine@host" portion of the freshness header.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (sql.Open("sqlite", ...))

	"github.com/townsendmerino/aikit/chunk"
)

// sqliteDB is the engine label used in the freshness header
// ("-- indexed at ... from sqlite@<basename>"). Distinct from the
// driver name passed to sql.Open ("sqlite").
const sqliteDB = "sqlite"

// indexSchemaSQLite is the SQLite arm of IndexSchema's engine dispatch.
// Connects to the file pointed at by opts.DSN, introspects schema +
// (optionally) samples rows, and returns the rendered chunks.
//
// opts.DSN is one of:
//   - sqlite:///abs/path.db   → absolute file path
//   - sqlite3:///abs/path.db  → same; sqlite3 scheme accepted for convention
//   - sqlite://./rel/path.db  → relative to KEN_MCP_DEFAULT_REPO
//   - sqlite://rel/path.db    → relative form WITHOUT leading "./" (rare)
//
// The DSN may also carry query parameters which we pass through to the
// driver verbatim (e.g. `?_pragma=foreign_keys(1)`).
//
// defaultRepo is the operator's KEN_MCP_DEFAULT_REPO — used to resolve
// relative DSN paths. Empty means "treat the path as-is" (best-effort).
func indexSchemaSQLite(ctx context.Context, opts Options, defaultRepo string) ([]chunk.Chunk, error) {
	opts = opts.validate()
	if opts.DSN == "" {
		return nil, nil
	}

	filePath, driverDSN, err := resolveSQLiteDSN(opts.DSN, defaultRepo)
	if err != nil {
		return nil, fmt.Errorf("db: sqlite DSN: %w", err)
	}

	// The driver opens the file lazily on first query. Stat upfront so
	// we surface "file missing" as a clean connection-level error
	// rather than the first query failing with the same.
	if _, err := os.Stat(filePath); err != nil {
		return nil, fmt.Errorf("db: sqlite file %q: %w", filePath, err)
	}

	conn, err := sql.Open("sqlite", driverDSN)
	if err != nil {
		return nil, fmt.Errorf("db: sqlite open: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if err := conn.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("db: sqlite ping: %w", err)
	}

	snap, err := introspectSQLite(ctx, conn, opts)
	if err != nil {
		return nil, fmt.Errorf("db: sqlite introspect: %w", err)
	}

	engineHost := sqliteEngineHost(filePath)
	header := freshnessHeader(engineHost, time.Now().UTC())
	pathPrefix := "db://" + engineHost

	var chunks []chunk.Chunk
	for _, t := range snap.tables {
		chunks = append(chunks, renderTableChunk(t, header, pathPrefix))
	}
	for _, v := range snap.views {
		chunks = append(chunks, renderViewChunk(v, header, pathPrefix))
	}
	// SQLite doesn't have stored functions in the SQL standard sense;
	// triggers exist but are scoped to a table and folded into the
	// table's CREATE TABLE rendering (out of scope for v0.7.1 — keep
	// the function path zero-filled for parity with the Postgres shape).
	return chunks, nil
}

// resolveSQLiteDSN parses opts.DSN, returns the absolute file path the
// engine should open and the DSN string to hand to the modernc.org
// driver (which is just the file path, optionally followed by ?query).
//
// Relative paths (`sqlite://./dev.db` or `sqlite://dev.db` with a
// non-empty Host) are resolved against defaultRepo so operators don't
// have to type the absolute path of the repo's checkout dir.
func resolveSQLiteDSN(dsn, defaultRepo string) (filePath, driverDSN string, err error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", "", fmt.Errorf("parse DSN: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "sqlite" && scheme != "sqlite3" {
		return "", "", fmt.Errorf("unsupported scheme %q (want sqlite:// or sqlite3://)", u.Scheme)
	}

	// Reconstruct the path: absolute when Host is empty
	// (sqlite:///abs/path → Host="", Path="/abs/path"),
	// relative when Host has the leading "." or anything else
	// (sqlite://./rel → Host=".", Path="/rel").
	path := u.Path
	if u.Host != "" {
		path = u.Host + u.Path
	}
	// Windows file URLs: `sqlite:///C:/path` parses to Path="/C:/path".
	// Drop the leading slash before the drive letter so the result is the
	// real absolute path "C:/path" — filepath.IsAbs("/C:/...") is false on
	// Windows, so without this the path would be mis-anchored at defaultRepo.
	if runtime.GOOS == "windows" && len(path) >= 3 && path[0] == '/' && path[2] == ':' &&
		((path[1] >= 'A' && path[1] <= 'Z') || (path[1] >= 'a' && path[1] <= 'z')) {
		path = path[1:]
	}
	if path == "" {
		return "", "", errors.New("DSN missing file path")
	}

	// Absolute path: keep as-is.
	if filepath.IsAbs(path) {
		filePath = path
	} else {
		// Relative path: anchor at defaultRepo when present, else cwd.
		if defaultRepo != "" {
			filePath = filepath.Join(defaultRepo, path)
		} else {
			filePath = path
		}
	}

	driverDSN = filePath
	if u.RawQuery != "" {
		driverDSN = filePath + "?" + u.RawQuery
	}
	return filePath, driverDSN, nil
}

// sqliteEngineHost is the SQLite analogue of engineHostFromConfig. It
// renders the engine label as "sqlite@<basename>" — basename only so
// the rendered chunk doesn't leak the full local filesystem path into
// every chunk's text. Operators who need full provenance can grep
// stderr logs.
func sqliteEngineHost(filePath string) string {
	return sqliteDB + "@" + filepath.Base(filePath)
}

// introspectSQLite is the SQLite analogue of introspect(). Produces the
// same schemaSnapshot shape so the renderers in emit.go don't need
// engine-specific branches.
func introspectSQLite(ctx context.Context, conn *sql.DB, opts Options) (*schemaSnapshot, error) {
	tables, err := sqliteListTables(ctx, conn)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	tableMap := make(map[string]*tableDef, len(tables))
	for i := range tables {
		t := &tables[i]
		tableMap[qualifiedKey(t.schema, t.name)] = t
	}

	// Pass 1: columns + indexes for EVERY table first, so every table's PK
	// is known before any FK is resolved (audit §13 — an implicit
	// `REFERENCES parent` FK needs to look up parent's real PK, which may
	// be a later table in this slice).
	for i := range tables {
		t := &tables[i]
		if err := sqliteFillColumns(ctx, conn, t); err != nil {
			warn(opts, "sqlite: columns for %s: %v", t.name, err)
		}
		if err := sqliteFillIndexes(ctx, conn, t); err != nil {
			warn(opts, "sqlite: indexes for %s: %v", t.name, err)
		}
	}
	// Pass 2: FKs, now that tableMap columns are fully populated.
	for i := range tables {
		t := &tables[i]
		if err := sqliteFillForeignKeys(ctx, conn, t, tableMap); err != nil {
			warn(opts, "sqlite: foreign keys for %s: %v", t.name, err)
		}
	}

	views, err := sqliteListViews(ctx, conn)
	if err != nil {
		return nil, fmt.Errorf("list views: %w", err)
	}

	// Stable order.
	slices.SortFunc(tables, compareTable)

	snap := &schemaSnapshot{tables: tables, views: views}

	if opts.SampleRows > 0 {
		sqliteAppendSamples(ctx, conn, snap, opts)
	}

	return snap, nil
}

// sqliteListTables returns every user table. SQLite has no schemas in
// the Postgres sense — the schema field is left empty ("public"-style
// rendering omits it via qualifiedName) and only the table name is
// meaningful.
func sqliteListTables(ctx context.Context, conn *sql.DB) ([]tableDef, error) {
	const q = `
SELECT name FROM sqlite_schema
WHERE type = 'table'
  AND name NOT LIKE 'sqlite_%'
ORDER BY name;`
	rows, err := conn.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []tableDef
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, tableDef{name: name})
	}
	return out, rows.Err()
}

// sqliteFillColumns populates tableDef.columns from PRAGMA table_info.
// pg_get_expr's analogue is PRAGMA table_info itself (returns
// dflt_value, notnull, pk, type — everything we need for column-level
// modifiers).
func sqliteFillColumns(ctx context.Context, conn *sql.DB, t *tableDef) error {
	// PRAGMA arguments are unsafe with parameters; identifier injection
	// is the concern, but `name` came from sqlite_schema (trusted source)
	// and is quoted for safety.
	q := fmt.Sprintf("PRAGMA table_info(%s)", sqliteQuoteIdent(t.name))
	rows, err := conn.QueryContext(ctx, q)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid     int
			colName string
			colType string
			notnull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &colName, &colType, &notnull, &dflt, &pk); err != nil {
			return err
		}
		col := columnDef{
			name:         colName,
			dataType:     strings.ToLower(strings.TrimSpace(colType)),
			notNull:      notnull != 0,
			isPrimaryKey: pk != 0,
		}
		if dflt.Valid {
			col.defaultExpr = dflt.String
		}
		t.columns = append(t.columns, col)
	}
	return rows.Err()
}

// sqliteFillIndexes populates tableDef.indexes from PRAGMA index_list
// + PRAGMA index_info. We render the index columns into the
// indexdef-shaped string emit.go consumes via extractIndexColumns.
func sqliteFillIndexes(ctx context.Context, conn *sql.DB, t *tableDef) error {
	q1 := fmt.Sprintf("PRAGMA index_list(%s)", sqliteQuoteIdent(t.name))
	rows, err := conn.QueryContext(ctx, q1)
	if err != nil {
		return err
	}
	defer rows.Close()
	type idxMeta struct {
		name   string
		unique bool
		origin string
	}
	var metas []idxMeta
	for rows.Next() {
		var (
			seq     int
			name    string
			unique  int
			origin  string
			partial int
		)
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			return err
		}
		// Skip auto-indexes created by UNIQUE/PK constraints; they
		// duplicate the column-level UNIQUE marker emit.go renders.
		if origin == "pk" || strings.HasPrefix(name, "sqlite_autoindex_") {
			continue
		}
		metas = append(metas, idxMeta{name: name, unique: unique != 0, origin: origin})
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, m := range metas {
		q2 := fmt.Sprintf("PRAGMA index_info(%s)", sqliteQuoteIdent(m.name))
		// Scoped closure so colRows.Close is deferred per-iteration (not
		// leaked until function return) and rows.Err() is checked.
		cols, err := func() ([]string, error) {
			colRows, err := conn.QueryContext(ctx, q2)
			if err != nil {
				return nil, err
			}
			defer colRows.Close()
			var cols []string
			for colRows.Next() {
				var seqno, cid int
				var name sql.NullString
				if err := colRows.Scan(&seqno, &cid, &name); err != nil {
					return nil, err
				}
				if name.Valid {
					cols = append(cols, name.String)
				}
			}
			return cols, colRows.Err()
		}()
		if err != nil {
			return err
		}
		// Render in the same indexdef shape extractIndexColumns understands.
		uniqueKW := ""
		if m.unique {
			uniqueKW = " UNIQUE"
		}
		indexdef := fmt.Sprintf("CREATE%s INDEX %s ON %s (%s)",
			uniqueKW, m.name, t.name, strings.Join(cols, ", "))
		t.indexes = append(t.indexes, indexDef{
			name:     m.name,
			unique:   m.unique,
			indexdef: indexdef,
		})
	}
	return nil
}

// sqliteFillForeignKeys populates the column-level fkTarget AND the
// inverse fkReferenced list on the referenced table, mirroring what
// annotateConstraints + annotateFKReferences do for Postgres.
func sqliteFillForeignKeys(ctx context.Context, conn *sql.DB, t *tableDef, tableMap map[string]*tableDef) error {
	q := fmt.Sprintf("PRAGMA foreign_key_list(%s)", sqliteQuoteIdent(t.name))
	rows, err := conn.QueryContext(ctx, q)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id       int
			seq      int
			refTable string
			fromCol  string
			toCol    sql.NullString
			onUpdate string
			onDelete string
			match    string
		)
		if err := rows.Scan(&id, &seq, &refTable, &fromCol, &toCol, &onUpdate, &onDelete, &match); err != nil {
			return err
		}
		toColStr := toCol.String
		if !toCol.Valid || toColStr == "" {
			// PRAGMA returns NULL for `to` when the FK references the parent's
			// PK without naming the column. Resolve the parent's REAL primary
			// key from tableMap rather than guessing "id" (audit §13 —
			// fabricating a column name the parent doesn't have is wrong data
			// presented confidently, which is worse than omission).
			toColStr = ""
			if rt, ok := tableMap[qualifiedKey("", refTable)]; ok {
				for i := range rt.columns {
					if rt.columns[i].isPrimaryKey {
						toColStr = rt.columns[i].name
						break
					}
				}
			}
			if toColStr == "" {
				// No explicit PK column — SQLite's implicit key is rowid.
				toColStr = "rowid"
			}
		}
		// Column-level: forward FK pointer.
		for i := range t.columns {
			if strings.EqualFold(t.columns[i].name, fromCol) {
				t.columns[i].fkTarget = fmt.Sprintf("%s(%s)", refTable, toColStr)
			}
		}
		// Inverse: ref table sees "FK referenced by: <fromTable>(<fromCol>)".
		if rt, ok := tableMap[qualifiedKey("", refTable)]; ok {
			rt.fkReferenced = append(rt.fkReferenced, fkRef{
				fromSchema: "",
				fromTable:  t.name,
				fromColumn: fromCol,
			})
		}
	}
	return rows.Err()
}

// sqliteListViews returns user views with their definitions.
func sqliteListViews(ctx context.Context, conn *sql.DB) ([]viewDef, error) {
	const q = `
SELECT name, sql FROM sqlite_schema
WHERE type = 'view'
ORDER BY name;`
	rows, err := conn.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []viewDef
	for rows.Next() {
		var name, body sql.NullString
		if err := rows.Scan(&name, &body); err != nil {
			return nil, err
		}
		v := viewDef{name: name.String}
		if body.Valid {
			// Strip the leading "CREATE VIEW <name> AS " preamble so the
			// rendered chunk shows just the body — matches the Postgres
			// information_schema.views shape.
			def := strings.TrimSpace(body.String)
			lower := strings.ToLower(def)
			if i := strings.Index(lower, " as "); i >= 0 {
				def = strings.TrimSpace(def[i+4:])
			}
			v.definition = def
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// sqliteAppendSamples is the SQLite analogue of sample.go's
// sampleRowsImpl. Same per-table determinism (ORDER BY first PK or
// ORDER BY 1) and same cell-truncation policy via the shared
// truncateCell/formatCell helpers.
//
// Approx row counts: SQLite has no pg_class.reltuples equivalent. A BOUNDED
// COUNT(*) over a capped subquery (audit R4): MAX(rowid) is the largest
// primary key, NOT a count — an INTEGER PRIMARY KEY *is* the rowid, so a
// 2-row table with epoch/snowflake ids rendered as "~1.7 trillion rows",
// confidently wrong data handed to the model. Capping the inner scan at
// sqliteRowCountCap+1 keeps it O(cap) not O(n) (the §28 perf worry that
// motivated the MAX(rowid) change) while staying ACCURATE up to the cap;
// above the cap we omit the count rather than present an understated one.
// COUNT(*) also works on WITHOUT ROWID tables, which MAX(rowid) errored on.
func sqliteAppendSamples(ctx context.Context, conn *sql.DB, snap *schemaSnapshot, opts Options) {
	const sqliteRowCountCap = 1_000_000
	for i := range snap.tables {
		t := &snap.tables[i]
		// Per-table recover (audit §26): one exotic driver conversion panic
		// skips the table instead of crashing the process, matching Postgres.
		withSampleRecover(opts, t.name, func() {
			// Bounded approximate row count.
			var n int64
			countQ := fmt.Sprintf("SELECT COUNT(*) FROM (SELECT 1 FROM %s LIMIT %d)",
				sqliteQuoteIdent(t.name), sqliteRowCountCap+1)
			if err := conn.QueryRowContext(ctx, countQ).Scan(&n); err == nil && n <= sqliteRowCountCap {
				t.approxRowCount = float64(n)
			}

			orderClause := "ORDER BY 1"
			for _, c := range t.columns {
				if c.isPrimaryKey {
					orderClause = "ORDER BY " + sqliteQuoteIdent(c.name)
					break
				}
			}
			// LIMIT must be an int literal in SQLite's PRAGMA-free query path,
			// but passing it as a parameter is supported by the driver.
			q := fmt.Sprintf("SELECT * FROM %s %s LIMIT ?", sqliteQuoteIdent(t.name), orderClause)
			rows, err := conn.QueryContext(ctx, q, opts.SampleRows)
			if err != nil {
				warn(opts, "sqlite: sample query failed for %s: %v", t.name, err)
				return
			}
			defer rows.Close()
			cols, err := rows.Columns()
			if err != nil {
				warn(opts, "sqlite: column metadata for %s: %v", t.name, err)
				return
			}
			var collected [][]string
			for rows.Next() {
				vals := make([]any, len(cols))
				ptrs := make([]any, len(cols))
				for j := range vals {
					ptrs[j] = &vals[j]
				}
				if err := rows.Scan(ptrs...); err != nil {
					warn(opts, "sqlite: sample scan failed for %s: %v", t.name, err)
					continue
				}
				cells := make([]string, len(vals))
				for j, v := range vals {
					cells[j] = truncateCell(formatCell(v))
				}
				collected = append(collected, cells)
			}
			// Surface a mid-iteration read error (code review §4): without this,
			// rows.Next() stopping early on an error silently accepts a partial
			// sample. The Postgres path already checks rows.Err().
			if err := rows.Err(); err != nil {
				warn(opts, "sqlite: sample row iteration for %s ended early: %v", t.name, err)
			}
			t.sampleRows = collected
		})
	}
}

// sqliteQuoteIdent double-quotes an identifier for safe SQL embedding.
// SQLite accepts both `"name"` and “ `name` “ quoting; we use the
// SQL-standard form. Doubles any embedded `"` per the standard.
func sqliteQuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// qualifiedKey returns a map key that's unique per (schema, name)
// pair. SQLite always has empty schema; Postgres uses "public"/etc.
// Same shape so the same renderers can reuse the lookup.
func qualifiedKey(schema, name string) string {
	return schema + "." + name
}
