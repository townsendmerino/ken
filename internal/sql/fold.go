package sql

// Migration-history folding (v0.7.1 / ADR-018).
//
// Tier 1 ships one chunk per CREATE TABLE / ALTER TABLE / etc. statement.
// Projects that maintain schema through numbered migration files
// (Goose/dbmate, Rails, Flyway, Alembic) end up with N+1 chunks for one
// table: the original CREATE plus N ALTERs. Asked "what columns does
// users have?" an agent must read all N+1 chunks and replay them.
//
// FoldMigrations replays this in-memory: parse the .sql files in a
// migration directory in lexical order, apply each ALTER to the in-memory
// column list of the referenced CREATE TABLE, then emit ONE chunk per
// table reflecting the final state. Indexes inside the migration dir are
// folded into their target's table chunk; views are emitted standalone.
//
// Scope (lightweight ALTER replay):
//   - ADD COLUMN
//   - DROP COLUMN
//   - ALTER COLUMN ... TYPE / SET DATA TYPE
//   - ADD CONSTRAINT
//   - DROP CONSTRAINT
//   - RENAME COLUMN (v0.8.1 Part C / ADR-022; eager application,
//     mutates the in-flight foldedTable so subsequent ALTERs see
//     the post-rename state; rewrites column references inside
//     this-table constraints via first-parens scope; cross-table
//     FK target-side renames are NOT propagated)
//   - RENAME CONSTRAINT (v0.8.1 Part C / ADR-022; named constraints
//     only — anonymous PRIMARY KEY (id) etc. have no name to match
//     and fall back to BOTH-chunks)
//
// Out of scope (statement falls back to its per-file ALTER chunk):
//   - RENAME TO (table rename — needs per-database rename map for
//     cross-table FK target-side propagation; v0.9.0+ if requested)
//   - MySQL CHANGE old new TYPE (rename + retype in one statement;
//     v0.8.1's RENAME path doesn't decode the type-change half)
//   - Engine-specific extensions (Postgres partition syntax, MySQL
//     MODIFY COLUMN)
//
// Failure handling — emit BOTH chunks:
//   For each ALTER that can't be applied cleanly (unknown column, missing
//   CREATE TABLE, action type out of scope) we log a warn AND keep the
//   per-file ALTER chunk in the output. The folded chunk for what we
//   could resolve also goes out. Net: agent sees the union; never less
//   information than v0.7.0.
//
// API surface and execution order (called from internal/search/index.go):
//   1. IsMigrationDir(fsys, dir) — cheap detector; >= 2 files matching
//      one of the recognized naming patterns. Single-file dirs aren't
//      migrations; they're seeds or one-offs.
//   2. FoldMigrations(fsys, dir, logger) — read + parse + replay + render.
//      The caller replaces the per-file structural chunks for files in
//      `dir` with the returned slice. Line-chunked raw text from the SQL
//      files is unaffected (still emitted by the regular chunker pass).

import (
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/townsendmerino/ken/internal/chunk"
)

// Migration-directory detection (migrationPatterns, classifyMigrationName,
// minMigrationFiles, IsMigrationDir) lives in migrations.go — the cheap
// "is this a migration directory" half of the surface. fold.go retains
// the replay machinery FoldMigrations drives.

// FoldMigrations reads every .sql file in dir (inside fsys) in lexical
// order, replays CREATE TABLE / ALTER TABLE / CREATE INDEX / CREATE VIEW
// statements into an in-memory schema state, and returns the rendered
// chunks for that final state. Files in dir that don't match a
// migration naming pattern are skipped silently (test fixtures, READMEs,
// downgrade scripts named without numeric prefixes, etc.).
//
// Statements that can't be folded (out-of-scope action types, unknown
// columns, missing CREATE TABLE for an ALTER) are logged to logger and
// preserved as their original per-file chunk in the output — agent never
// sees less than v0.7.0 would have surfaced.
//
// Returns (nil, nil) when dir contains no parseable migration files.
// A read error on fs.ReadDir returns the error; per-file read errors
// are logged and the file is skipped.
func FoldMigrations(fsys fs.FS, dir string, logger io.Writer) ([]chunk.Chunk, error) {
	if logger == nil {
		logger = io.Discard
	}
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("FoldMigrations: read dir %q: %w", dir, err)
	}

	// Classify each .sql file under exactly one pattern (first match
	// wins, so 14-digit Rails timestamps don't double-count as generic
	// numbered files). Pick the dominant pattern; warn for minorities.
	counts := make([]int, len(migrationPatterns))
	classified := make(map[int][]string, len(migrationPatterns))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		idx := classifyMigrationName(name)
		if idx < 0 {
			continue
		}
		counts[idx]++
		classified[idx] = append(classified[idx], name)
	}
	dominantIdx := -1
	for i, n := range counts {
		if n >= minMigrationFiles && (dominantIdx < 0 || n > counts[dominantIdx]) {
			dominantIdx = i
		}
	}
	if dominantIdx < 0 {
		return nil, nil
	}
	for i, n := range counts {
		if i == dominantIdx || n == 0 {
			continue
		}
		fmt.Fprintf(logger, "sql.FoldMigrations: %s contains mixed migration patterns; dominant pattern picked (%d files), %d files match a minority pattern\n",
			dir, counts[dominantIdx], n)
	}
	ordered := classified[dominantIdx]
	sort.Strings(ordered)

	state := newFoldState()

	for _, name := range ordered {
		relPath := path.Join(dir, name)
		data, err := fs.ReadFile(fsys, relPath)
		if err != nil {
			fmt.Fprintf(logger, "sql.FoldMigrations: read %s: %v\n", relPath, err)
			continue
		}
		state.consumeFile(relPath, data, logger)
	}

	return state.render(), nil
}

// foldedTable is the in-memory representation of a table being assembled
// by replaying CREATE + ALTER statements. We carry both columns and
// constraints, plus the source-file pointer for the CREATE so the
// rendered chunk can point at a real path.
type foldedTable struct {
	name        string
	columns     []columnDef
	constraints []string
	indexes     []indexDef // folded indexes (target table matches)
	startLine   int
	endLine     int
	createFile  string // file path of the CREATE TABLE statement
}

// foldState accumulates the schema being replayed plus side outputs
// (unfoldable ALTER chunks, standalone indexes, standalone views).
type foldState struct {
	tables     []*foldedTable
	tableIndex map[string]int // lowercase name → index into tables
	views      []chunk.Chunk  // emitted standalone (one per CREATE VIEW)
	stdIndexes []chunk.Chunk  // standalone indexes (target table missing)
	leftovers  []chunk.Chunk  // unfoldable ALTERs preserved as per-file chunks
}

func newFoldState() *foldState {
	return &foldState{tableIndex: map[string]int{}}
}

// consumeFile parses one .sql file and folds its statements into state.
// path is the file path used for chunk headers and leftover chunks.
func (s *foldState) consumeFile(path string, content []byte, logger io.Writer) {
	statements := splitStatements(content)
	for _, st := range statements {
		switch st.kind {
		case stmtCreateTable:
			t, ok := parseCreateTable(st)
			if !ok {
				fmt.Fprintf(logger, "sql.FoldMigrations: %s:%d: could not parse CREATE TABLE\n", path, st.startLine)
				continue
			}
			ft := &foldedTable{
				name:        t.name,
				columns:     t.columns,
				constraints: t.constraints,
				startLine:   t.startLine,
				endLine:     t.endLine,
				createFile:  path,
			}
			key := strings.ToLower(t.name)
			if _, exists := s.tableIndex[key]; exists {
				// Duplicate CREATE TABLE across files — keep the first;
				// warn so an operator knows the second was ignored.
				fmt.Fprintf(logger, "sql.FoldMigrations: %s:%d: duplicate CREATE TABLE %q ignored (first definition wins)\n",
					path, st.startLine, t.name)
				continue
			}
			s.tableIndex[key] = len(s.tables)
			s.tables = append(s.tables, ft)

		case stmtAlterTable:
			a, ok := parseAlterTable(st)
			if !ok {
				fmt.Fprintf(logger, "sql.FoldMigrations: %s:%d: could not parse ALTER TABLE\n", path, st.startLine)
				continue
			}
			if !s.applyAlter(path, a, st, logger) {
				// Preserve as per-file chunk so the agent sees the action.
				s.leftovers = append(s.leftovers, renderAlterChunk(path, a))
			}

		case stmtCreateIndex:
			idx, ok := parseCreateIndex(st)
			if !ok {
				fmt.Fprintf(logger, "sql.FoldMigrations: %s:%d: could not parse CREATE INDEX\n", path, st.startLine)
				continue
			}
			if ti, ok := s.tableIndex[strings.ToLower(idx.table)]; ok {
				s.tables[ti].indexes = append(s.tables[ti].indexes, idx)
			} else {
				s.stdIndexes = append(s.stdIndexes, renderIndexChunk(path, idx))
			}

		case stmtCreateView:
			v, ok := parseCreateView(st)
			if !ok {
				fmt.Fprintf(logger, "sql.FoldMigrations: %s:%d: could not parse CREATE VIEW\n", path, st.startLine)
				continue
			}
			s.views = append(s.views, renderViewChunk(path, v))

		case stmtUnknown:
			// DML, GRANT, function bodies, etc. — silently skipped.
		}
	}
}

// applyAlter walks the actions inside one ALTER TABLE statement and
// mutates the target foldedTable. Returns true iff EVERY action in the
// statement was applied cleanly; if any action couldn't be folded, the
// caller preserves the per-file ALTER chunk and we still apply whatever
// other actions in the same statement DID match. (Partial fold +
// belt-and-suspenders chunk = agent never sees less than v0.7.0.)
func (s *foldState) applyAlter(srcFile string, a alterDef, st statement, logger io.Writer) bool {
	ti, ok := s.tableIndex[strings.ToLower(a.table)]
	if !ok {
		fmt.Fprintf(logger, "sql.FoldMigrations: %s:%d: ALTER TABLE %q has no matching CREATE TABLE in this directory; preserved as per-file chunk\n",
			srcFile, st.startLine, a.table)
		return false
	}
	t := s.tables[ti]

	allOK := true
	for _, action := range splitAlterActions(a.action) {
		action = strings.TrimSpace(action)
		if action == "" {
			continue
		}
		if !applyOneAction(t, action, srcFile, st.startLine, logger) {
			allOK = false
		}
	}
	return allOK
}

// splitAlterActions splits a multi-action ALTER body
// ("ADD COLUMN x INT, DROP COLUMN y") into per-action substrings.
// Top-level comma aware (commas inside () belong to type/constraint
// parameters, not action separators).
func splitAlterActions(action string) []string {
	body := []byte(action)
	parts := splitTopLevelCommas(body)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, string(p))
	}
	return out
}

// applyOneAction dispatches a single action to the appropriate handler.
// Returns true iff the action was fully applied; false means the action
// is out of scope or the target column/constraint wasn't found — caller
// preserves the per-file ALTER chunk so the agent always sees the
// action in the raw migration text (BOTH-chunks fallback).
//
// v0.8.1 Part C (ADR-022): the RENAME branch now folds rather than
// fail-out. RENAME COLUMN + RENAME CONSTRAINT apply eagerly via
// applyRename → applyColumnRename / applyConstraintRename in
// migrations.go. MySQL's CHANGE syntax (which renames + retypes in
// one go) remains unsupported in v0.8.1 — operators using it see the
// BOTH-chunks fallback. See ADR-022's "MySQL CHANGE syntax" entry.
func applyOneAction(t *foldedTable, action, srcFile string, line int, logger io.Writer) bool {
	tokens := tokenize([]byte(action))
	if len(tokens) == 0 {
		return true // empty action = no-op (e.g. trailing comma)
	}
	first := strings.ToUpper(tokens[0].text)
	switch first {
	case "ADD":
		return applyAdd(t, action, tokens, srcFile, line, logger)
	case "DROP":
		return applyDrop(t, tokens, srcFile, line, logger)
	case "ALTER":
		return applyAlterColumn(t, action, tokens, srcFile, line, logger)
	case "RENAME":
		return applyRename(t, tokens, srcFile, line, logger)
	default:
		fmt.Fprintf(logger, "sql.FoldMigrations: %s:%d: unsupported ALTER action %q; preserved as per-file chunk\n",
			srcFile, line, first)
		return false
	}
}

// applyRename handles ALTER TABLE ... RENAME COLUMN / RENAME CONSTRAINT.
// Eager: mutates t in place so subsequent ALTERs see the post-rename
// state. See ADR-022 for the eager-vs-lazy choice rationale.
//
// Accepted shapes (Postgres / MySQL / SQLite / MariaDB convergent
// syntax — the same identifier-level form across all four):
//
//	RENAME COLUMN old TO new
//	RENAME CONSTRAINT old TO new
//
// Anything else (e.g. MySQL CHANGE which renames+retypes in one
// statement) falls through to the per-file BOTH-chunks fallback.
func applyRename(t *foldedTable, tokens []token, srcFile string, line int, logger io.Writer) bool {
	if len(tokens) < 5 {
		fmt.Fprintf(logger, "sql.FoldMigrations: %s:%d: malformed RENAME action; preserved as per-file chunk\n",
			srcFile, line)
		return false
	}
	second := strings.ToUpper(tokens[1].text)
	switch second {
	case "COLUMN":
		// "RENAME COLUMN <old> TO <new>"
		if !strings.EqualFold(tokens[3].text, "TO") {
			fmt.Fprintf(logger, "sql.FoldMigrations: %s:%d: RENAME COLUMN expected TO; preserved as per-file chunk\n",
				srcFile, line)
			return false
		}
		oldName := strings.Trim(tokens[2].text, `"`)
		newName := strings.Trim(tokens[4].text, `"`)
		if !applyColumnRename(t, oldName, newName) {
			fmt.Fprintf(logger, "sql.FoldMigrations: %s:%d: RENAME COLUMN %q not found on %s; preserved as per-file chunk\n",
				srcFile, line, oldName, t.name)
			return false
		}
		return true
	case "CONSTRAINT":
		// "RENAME CONSTRAINT <old> TO <new>"
		if !strings.EqualFold(tokens[3].text, "TO") {
			fmt.Fprintf(logger, "sql.FoldMigrations: %s:%d: RENAME CONSTRAINT expected TO; preserved as per-file chunk\n",
				srcFile, line)
			return false
		}
		oldName := strings.Trim(tokens[2].text, `"`)
		newName := strings.Trim(tokens[4].text, `"`)
		if !applyConstraintRename(t, oldName, newName) {
			fmt.Fprintf(logger, "sql.FoldMigrations: %s:%d: RENAME CONSTRAINT %q not found on %s; preserved as per-file chunk\n",
				srcFile, line, oldName, t.name)
			return false
		}
		return true
	case "TO":
		// "RENAME TO <new>" — table rename. Out of scope for v0.8.1
		// Part C (per-database table rename map would be needed to
		// propagate to FK target references in other tables); the
		// per-file ALTER chunk preserves the action for the agent.
		fmt.Fprintf(logger, "sql.FoldMigrations: %s:%d: RENAME TABLE (rename to) not supported by lightweight fold; preserved as per-file chunk\n",
			srcFile, line)
		return false
	default:
		fmt.Fprintf(logger, "sql.FoldMigrations: %s:%d: unsupported RENAME subject %q; preserved as per-file chunk\n",
			srcFile, line, second)
		return false
	}
}

// applyAdd handles ADD COLUMN and ADD CONSTRAINT.
func applyAdd(t *foldedTable, action string, tokens []token, srcFile string, line int, logger io.Writer) bool {
	if len(tokens) < 2 {
		fmt.Fprintf(logger, "sql.FoldMigrations: %s:%d: malformed ADD action %q\n", srcFile, line, action)
		return false
	}
	second := strings.ToUpper(tokens[1].text)
	switch second {
	case "COLUMN":
		// "ADD COLUMN [IF NOT EXISTS] <name> <type> <modifiers...>"
		i := 2
		if matchSeqIgnoreCase(tokens, i, "IF", "NOT", "EXISTS") {
			i += 3
		}
		if i >= len(tokens) {
			fmt.Fprintf(logger, "sql.FoldMigrations: %s:%d: ADD COLUMN missing name\n", srcFile, line)
			return false
		}
		name := strings.Trim(tokens[i].text, `"`)
		// IF NOT EXISTS — if the column already exists, treat as no-op success.
		if findColumn(t, name) >= 0 {
			return true
		}
		// Slice the action text from the start of the type/rest token.
		rest := ""
		if i+1 < len(tokens) {
			rest = collapseWhitespace(action[tokens[i+1].start:])
		}
		t.columns = append(t.columns, columnDef{name: name, rest: rest})
		return true
	case "CONSTRAINT":
		// ADD CONSTRAINT — append the full constraint clause.
		// Strip the leading "ADD " for storage; matches the form
		// splitTableInner produces for inline constraints.
		constraint := collapseWhitespace(strings.TrimSpace(action[tokens[1].start:]))
		t.constraints = append(t.constraints, constraint)
		return true
	default:
		// Bareword PK/UNIQUE/FOREIGN/CHECK clause: "ADD PRIMARY KEY (id)".
		if isConstraintKeyword(second) {
			constraint := collapseWhitespace(strings.TrimSpace(action[tokens[1].start:]))
			t.constraints = append(t.constraints, constraint)
			return true
		}
		fmt.Fprintf(logger, "sql.FoldMigrations: %s:%d: unsupported ADD subject %q\n", srcFile, line, second)
		return false
	}
}

// applyDrop handles DROP COLUMN and DROP CONSTRAINT.
func applyDrop(t *foldedTable, tokens []token, srcFile string, line int, logger io.Writer) bool {
	if len(tokens) < 2 {
		fmt.Fprintf(logger, "sql.FoldMigrations: %s:%d: malformed DROP action\n", srcFile, line)
		return false
	}
	second := strings.ToUpper(tokens[1].text)
	switch second {
	case "COLUMN":
		i := 2
		if matchSeqIgnoreCase(tokens, i, "IF", "EXISTS") {
			i += 2
		}
		if i >= len(tokens) {
			fmt.Fprintf(logger, "sql.FoldMigrations: %s:%d: DROP COLUMN missing name\n", srcFile, line)
			return false
		}
		name := strings.Trim(tokens[i].text, `"`)
		idx := findColumn(t, name)
		if idx < 0 {
			fmt.Fprintf(logger, "sql.FoldMigrations: %s:%d: DROP COLUMN %q not found on %s; preserved as per-file chunk\n",
				srcFile, line, name, t.name)
			return false
		}
		t.columns = append(t.columns[:idx], t.columns[idx+1:]...)
		return true
	case "CONSTRAINT":
		i := 2
		if matchSeqIgnoreCase(tokens, i, "IF", "EXISTS") {
			i += 2
		}
		if i >= len(tokens) {
			fmt.Fprintf(logger, "sql.FoldMigrations: %s:%d: DROP CONSTRAINT missing name\n", srcFile, line)
			return false
		}
		name := strings.Trim(tokens[i].text, `"`)
		removed := false
		filtered := t.constraints[:0]
		for _, c := range t.constraints {
			ctoks := tokenize([]byte(c))
			// Match either "CONSTRAINT <name>" prefix or a name match in the
			// rest of the constraint text.
			if len(ctoks) >= 2 &&
				strings.EqualFold(ctoks[0].text, "CONSTRAINT") &&
				strings.EqualFold(strings.Trim(ctoks[1].text, `"`), name) {
				removed = true
				continue
			}
			filtered = append(filtered, c)
		}
		t.constraints = filtered
		if !removed {
			fmt.Fprintf(logger, "sql.FoldMigrations: %s:%d: DROP CONSTRAINT %q not found on %s; preserved as per-file chunk\n",
				srcFile, line, name, t.name)
			return false
		}
		return true
	default:
		fmt.Fprintf(logger, "sql.FoldMigrations: %s:%d: unsupported DROP subject %q\n", srcFile, line, second)
		return false
	}
}

// applyAlterColumn handles ALTER COLUMN ... TYPE / SET DATA TYPE.
func applyAlterColumn(t *foldedTable, action string, tokens []token, srcFile string, line int, logger io.Writer) bool {
	if len(tokens) < 4 || !strings.EqualFold(tokens[1].text, "COLUMN") {
		fmt.Fprintf(logger, "sql.FoldMigrations: %s:%d: unsupported ALTER subject %q\n", srcFile, line, action)
		return false
	}
	colName := strings.Trim(tokens[2].text, `"`)
	idx := findColumn(t, colName)
	if idx < 0 {
		fmt.Fprintf(logger, "sql.FoldMigrations: %s:%d: ALTER COLUMN %q not found on %s; preserved as per-file chunk\n",
			srcFile, line, colName, t.name)
		return false
	}
	// Walk tokens after the column name looking for TYPE (Postgres) or
	// SET DATA TYPE (ANSI). Everything after is the new type expression.
	i := 3
	typeStart := -1
	if strings.EqualFold(tokens[i].text, "TYPE") {
		typeStart = i + 1
	} else if matchSeqIgnoreCase(tokens, i, "SET", "DATA", "TYPE") {
		typeStart = i + 3
	}
	if typeStart < 0 || typeStart >= len(tokens) {
		fmt.Fprintf(logger, "sql.FoldMigrations: %s:%d: ALTER COLUMN %q: only TYPE/SET DATA TYPE supported (not %q); preserved as per-file chunk\n",
			srcFile, line, colName, tokens[i].text)
		return false
	}
	newRest := collapseWhitespace(strings.TrimSpace(action[tokens[typeStart].start:]))
	t.columns[idx].rest = newRest
	return true
}

// findColumn returns the index of a column by case-insensitive name, or
// -1 if not present.
func findColumn(t *foldedTable, name string) int {
	for i, c := range t.columns {
		if strings.EqualFold(c.name, name) {
			return i
		}
	}
	return -1
}

// render produces the final chunk slice: one per folded table, plus the
// preserved standalone indexes, views, and leftover ALTER chunks.
func (s *foldState) render() []chunk.Chunk {
	var out []chunk.Chunk
	for _, t := range s.tables {
		out = append(out, renderFoldedTableChunk(t))
	}
	out = append(out, s.stdIndexes...)
	out = append(out, s.views...)
	out = append(out, s.leftovers...)
	return out
}

// renderFoldedTableChunk emits the merged-state chunk for one table.
// Format mirrors renderTableChunk's output so the BM25 + Model2Vec
// retrievers see the same shape regardless of which path produced the
// chunk. The `-- file:` header points at the CREATE TABLE's source file
// (real provenance) and a second `-- folded from migrations` line marks
// the chunk as the merged view.
func renderFoldedTableChunk(t *foldedTable) chunk.Chunk {
	var b strings.Builder
	fmt.Fprintf(&b, "-- file: %s\n-- folded from migrations\nTABLE %s\n", t.createFile, t.name)
	for _, c := range t.columns {
		if c.rest == "" {
			fmt.Fprintf(&b, "  %s\n", c.name)
		} else {
			fmt.Fprintf(&b, "  %s  %s\n", c.name, c.rest)
		}
	}
	for _, con := range t.constraints {
		fmt.Fprintf(&b, "  %s\n", con)
	}
	if len(t.indexes) > 0 {
		b.WriteString("\n")
		for _, idx := range t.indexes {
			prefix := "  INDEX"
			if idx.unique {
				prefix = "  UNIQUE INDEX"
			}
			fmt.Fprintf(&b, "%s %s ON (%s)\n", prefix, idx.name, idx.columns)
		}
	}
	return chunk.Chunk{
		File:      t.createFile,
		StartLine: t.startLine,
		EndLine:   t.endLine,
		Text:      b.String(),
	}
}
