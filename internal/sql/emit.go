package sql

import (
	"fmt"
	"io"
	"strings"

	"github.com/townsendmerino/ken/chunk"
)

// maxViewBodyLines caps the rendered length of a CREATE VIEW body. Long
// views (multi-CTE analytics queries) would otherwise produce one very
// large chunk; truncating after this many lines keeps the BM25 doc
// length reasonable while preserving the head of the body where the
// SELECT list lives.
const maxViewBodyLines = 50

// ParseFile is the package's public entry: given a .sql source, return
// one chunk per CREATE TABLE / INDEX / VIEW / ALTER TABLE statement.
// Statements that don't parse cleanly are logged to logger at warn
// level (one line per skip: "sql: skipped statement in <path>:<line>: <reason>")
// and the rest of the file continues.
//
// CREATE INDEX statements whose target table also has a CREATE TABLE in
// the SAME file are folded into the table's chunk (the index appears as
// an extra line after the column list). Indexes whose target table
// lives elsewhere become standalone chunks.
//
// Returns nil, nil for files containing zero parseable DDL (pure-DML
// files, GRANT-only files, files with only comments) — empty result is
// not an error.
//
// path is the file path used for the chunk header and Chunk.File. logger
// receives best-effort warn messages; pass io.Discard if not interested.
func ParseFile(path string, content []byte, logger io.Writer) ([]chunk.Chunk, error) {
	if logger == nil {
		logger = io.Discard
	}
	statements := splitStatements(content)
	if len(statements) == 0 {
		return nil, nil
	}

	// Two-pass: build the tables map first so we can fold indexes.
	var tables []tableDef
	tableByName := map[string]int{} // lowercase name → index into tables

	var indexes []indexDef
	var views []viewDef
	var alters []alterDef

	for _, s := range statements {
		switch s.kind {
		case stmtCreateTable:
			t, ok := parseCreateTable(s)
			if !ok {
				warn(logger, path, s.startLine, "could not parse CREATE TABLE")
				continue
			}
			tableByName[strings.ToLower(t.name)] = len(tables)
			tables = append(tables, t)
		case stmtCreateIndex:
			idx, ok := parseCreateIndex(s)
			if !ok {
				warn(logger, path, s.startLine, "could not parse CREATE INDEX")
				continue
			}
			indexes = append(indexes, idx)
		case stmtCreateView:
			v, ok := parseCreateView(s)
			if !ok {
				warn(logger, path, s.startLine, "could not parse CREATE VIEW")
				continue
			}
			views = append(views, v)
		case stmtAlterTable:
			a, ok := parseAlterTable(s)
			if !ok {
				warn(logger, path, s.startLine, "could not parse ALTER TABLE")
				continue
			}
			alters = append(alters, a)
		case stmtUnknown:
			// Silently skip — DML, GRANT, COMMENT ON, etc.
		}
	}

	// Group indexes by folding-target. Indexes with a matching table in
	// this file attach to the table's chunk; the rest are standalone.
	foldedIndexes := map[int][]indexDef{} // table-index → indexes attached
	var standaloneIndexes []indexDef
	for _, idx := range indexes {
		if ti, ok := tableByName[strings.ToLower(idx.table)]; ok {
			foldedIndexes[ti] = append(foldedIndexes[ti], idx)
		} else {
			standaloneIndexes = append(standaloneIndexes, idx)
		}
	}

	var out []chunk.Chunk
	for i, t := range tables {
		out = append(out, renderTableChunk(path, t, foldedIndexes[i]))
	}
	for _, idx := range standaloneIndexes {
		out = append(out, renderIndexChunk(path, idx))
	}
	for _, v := range views {
		out = append(out, renderViewChunk(path, v))
	}
	for _, a := range alters {
		out = append(out, renderAlterChunk(path, a))
	}
	return out, nil
}

// renderTableChunk produces the denormalized "TABLE <name> + columns +
// constraints + folded indexes" form. The header line `-- file: <path>`
// keeps source provenance discoverable from the chunk text alone.
func renderTableChunk(path string, t tableDef, folded []indexDef) chunk.Chunk {
	var b strings.Builder
	fmt.Fprintf(&b, "-- file: %s\nTABLE %s\n", path, t.name)
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
	if len(folded) > 0 {
		b.WriteString("\n")
		for _, idx := range folded {
			prefix := "  INDEX"
			if idx.unique {
				prefix = "  UNIQUE INDEX"
			}
			fmt.Fprintf(&b, "%s %s ON (%s)\n", prefix, idx.name, idx.columns)
		}
	}
	return chunk.Chunk{
		File:      path,
		StartLine: t.startLine,
		EndLine:   t.endLine,
		Text:      b.String(),
	}
}

// renderIndexChunk produces a standalone CREATE INDEX chunk for cases
// where the target table is defined in a different file.
func renderIndexChunk(path string, idx indexDef) chunk.Chunk {
	var b strings.Builder
	fmt.Fprintf(&b, "-- file: %s\n", path)
	if idx.unique {
		fmt.Fprintf(&b, "UNIQUE INDEX %s ON %s (%s)\n", idx.name, idx.table, idx.columns)
	} else {
		fmt.Fprintf(&b, "INDEX %s ON %s (%s)\n", idx.name, idx.table, idx.columns)
	}
	return chunk.Chunk{
		File:      path,
		StartLine: idx.startLine,
		EndLine:   idx.endLine,
		Text:      b.String(),
	}
}

// renderViewChunk produces a VIEW chunk with body truncated at
// maxViewBodyLines so a huge analytics view doesn't dominate the index.
func renderViewChunk(path string, v viewDef) chunk.Chunk {
	body := v.body
	lines := strings.Split(body, "\n")
	truncated := false
	if len(lines) > maxViewBodyLines {
		lines = lines[:maxViewBodyLines]
		truncated = true
	}
	var b strings.Builder
	fmt.Fprintf(&b, "-- file: %s\nVIEW %s AS\n", path, v.name)
	for _, ln := range lines {
		fmt.Fprintf(&b, "  %s\n", ln)
	}
	if truncated {
		fmt.Fprintf(&b, "  -- ... (view body truncated at %d lines)\n", maxViewBodyLines)
	}
	return chunk.Chunk{
		File:      path,
		StartLine: v.startLine,
		EndLine:   v.endLine,
		Text:      b.String(),
	}
}

// renderAlterChunk produces one chunk per ALTER TABLE statement. We do
// NOT fold across files into the original CREATE TABLE — agents see the
// historical mutation as its own retrievable unit (ADR-017 rejected
// migration-history folding for v0.7.0).
func renderAlterChunk(path string, a alterDef) chunk.Chunk {
	var b strings.Builder
	fmt.Fprintf(&b, "-- file: %s\nALTER TABLE %s\n  %s\n", path, a.table, a.action)
	return chunk.Chunk{
		File:      path,
		StartLine: a.startLine,
		EndLine:   a.endLine,
		Text:      b.String(),
	}
}

// warn writes a one-line skip notice to logger. Format mirrors
// cmd/ken-mcp's existing leveled-logger emission so operators can grep
// for "sql: skipped" the same way they grep for other ken-mcp
// diagnostics.
func warn(logger io.Writer, path string, line int, reason string) {
	fmt.Fprintf(logger, "sql: skipped statement in %s:%d: %s\n", path, line, reason)
}
