package db

import (
	"fmt"
	"strings"

	"github.com/townsendmerino/aikit/chunk"
)

// maxViewBodyLines mirrors internal/sql/emit.go: caps a view body so a
// huge analytics CTE doesn't dominate the index as one chunk.
const maxViewBodyLines = 50

// renderTableChunk produces the denormalized "TABLE / columns / indexes
// / FK-referenced-by" chunk for one table. Header is the freshness line
// (always first). Path is `db://<engine-host>/<schema>.<table>`.
//
// Column rendering: "  <name>  <type>  <modifiers>" — modifiers are
// PK / NOT NULL / UNIQUE / DEFAULT / FK keywords composed in a stable
// order so successive reindexes produce byte-identical output for the
// same schema (modulo freshness header).
func renderTableChunk(t tableInfo, snap *schemaSnapshot, header, pathPrefix string) chunk.Chunk {
	_ = snap // reserved for cross-table lookups (composite FK rendering, etc.)
	var b strings.Builder
	b.WriteString(header)
	b.WriteByte('\n')
	fmt.Fprintf(&b, "TABLE %s\n", qualifiedName(t.schema, t.name))
	for _, c := range t.columns {
		mods := columnModifiers(c)
		if mods == "" {
			fmt.Fprintf(&b, "  %s  %s\n", c.name, c.dataType)
		} else {
			fmt.Fprintf(&b, "  %s  %s  %s\n", c.name, c.dataType, mods)
		}
	}
	if len(t.indexes) > 0 {
		b.WriteByte('\n')
		for _, idx := range t.indexes {
			// pg_indexes.indexdef looks like:
			//   CREATE [UNIQUE] INDEX <name> ON <schema>.<table> USING <method> (<cols>)
			// Render the trailing "(<cols>)" portion plus the index name
			// for compactness; full indexdef is overkill in the chunk.
			cols := extractIndexColumns(idx.indexdef)
			prefix := "  INDEX"
			if idx.unique {
				prefix = "  UNIQUE INDEX"
			}
			if cols == "" {
				fmt.Fprintf(&b, "%s %s\n", prefix, idx.name)
			} else {
				fmt.Fprintf(&b, "%s %s ON (%s)\n", prefix, idx.name, cols)
			}
		}
	}
	if len(t.fkReferenced) > 0 {
		// "FK referenced by: sessions(user_id), audit_log(actor_id)"
		// — gives the agent the inverse navigation that's normally
		// expensive to compute from the source code alone.
		parts := make([]string, 0, len(t.fkReferenced))
		for _, ref := range t.fkReferenced {
			parts = append(parts, fmt.Sprintf("%s(%s)",
				qualifiedName(ref.fromSchema, ref.fromTable), ref.fromColumn))
		}
		fmt.Fprintf(&b, "  FK referenced by: %s\n", strings.Join(parts, ", "))
	}
	if len(t.sampleRows) > 0 {
		// "Sample rows (3 of ~12,847):" then one parenthesized row per line.
		// The "of ~N" is omitted when approxRowCount is 0 (table never analyzed).
		b.WriteByte('\n')
		if t.approxRowCount > 0 {
			fmt.Fprintf(&b, "  Sample rows (%d of ~%s):\n", len(t.sampleRows), formatApproxCount(t.approxRowCount))
		} else {
			fmt.Fprintf(&b, "  Sample rows (%d):\n", len(t.sampleRows))
		}
		for _, row := range t.sampleRows {
			fmt.Fprintf(&b, "    (%s)\n", strings.Join(row, ", "))
		}
	}
	return chunk.Chunk{
		File: fmt.Sprintf("%s/%s.%s", pathPrefix, t.schema, t.name),
		// StartLine/EndLine are 1 — DB chunks have no source byte range.
		// We use 1/N where N is the rendered line count so any
		// downstream UI that expects monotonic positive lines doesn't
		// trip on zero.
		StartLine: 1,
		EndLine:   strings.Count(b.String(), "\n"),
		Text:      b.String(),
	}
}

// formatApproxCount renders a float-ish reltuples count as a comma-
// separated integer string for display (12847 → "12,847"). Reltuples
// can be fractional in older Postgres but we always show as integer —
// agents don't care about decimals of an approximation.
func formatApproxCount(n float64) string {
	if n < 0 {
		return "0"
	}
	i := int64(n)
	s := fmt.Sprintf("%d", i)
	// Insert commas every 3 digits from the right.
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	for k, c := range s {
		if k > 0 && (len(s)-k)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}

// columnModifiers composes "PK NOT NULL UNIQUE DEFAULT x FK→target"
// in a stable order. Order chosen for readability: PK first (visually
// distinct), then nullability, then uniqueness, then default, then FK
// arrow.
func columnModifiers(c columnInfo) string {
	var parts []string
	if c.isPrimaryKey {
		parts = append(parts, "PK")
	}
	if c.notNull {
		parts = append(parts, "NOT NULL")
	}
	if c.isUnique && !c.isPrimaryKey {
		// PK implies UNIQUE; don't double-print.
		parts = append(parts, "UNIQUE")
	}
	if c.defaultExpr != "" {
		parts = append(parts, "DEFAULT "+c.defaultExpr)
	}
	if c.fkTarget != "" {
		parts = append(parts, "→ "+c.fkTarget)
	}
	return strings.Join(parts, " ")
}

// extractIndexColumns pulls the column list out of a pg_indexes.indexdef
// string. The indexdef is a literal CREATE INDEX statement Postgres
// would emit; we grab the substring inside the outermost parentheses.
// Returns "" if no parentheses are found (shouldn't happen for valid
// indexes, but defensively avoid panicking).
func extractIndexColumns(indexdef string) string {
	open := strings.Index(indexdef, "(")
	if open < 0 {
		return ""
	}
	close := strings.LastIndex(indexdef, ")")
	if close < 0 || close <= open {
		return ""
	}
	return strings.TrimSpace(indexdef[open+1 : close])
}

// renderViewChunk produces a VIEW chunk with the body truncated at
// maxViewBodyLines.
func renderViewChunk(v viewInfo, header, pathPrefix string) chunk.Chunk {
	body := strings.TrimSpace(v.definition)
	lines := strings.Split(body, "\n")
	truncated := false
	if len(lines) > maxViewBodyLines {
		lines = lines[:maxViewBodyLines]
		truncated = true
	}
	var b strings.Builder
	b.WriteString(header)
	b.WriteByte('\n')
	fmt.Fprintf(&b, "VIEW %s AS\n", qualifiedName(v.schema, v.name))
	for _, ln := range lines {
		fmt.Fprintf(&b, "  %s\n", ln)
	}
	if truncated {
		fmt.Fprintf(&b, "  -- ... (view body truncated at %d lines)\n", maxViewBodyLines)
	}
	return chunk.Chunk{
		File:      fmt.Sprintf("%s/%s.%s", pathPrefix, v.schema, v.name),
		StartLine: 1,
		EndLine:   strings.Count(b.String(), "\n"),
		Text:      b.String(),
	}
}

// renderFunctionChunk produces a one-line-ish FUNCTION chunk: signature
// only, no body. The signature is the high-signal target for an agent
// searching for "function that authenticates users by token".
func renderFunctionChunk(f functionInfo, header, pathPrefix string) chunk.Chunk {
	var b strings.Builder
	b.WriteString(header)
	b.WriteByte('\n')
	if f.returnT != "" {
		fmt.Fprintf(&b, "FUNCTION %s%s RETURNS %s\n",
			qualifiedName(f.schema, f.name), f.argSig, f.returnT)
	} else {
		fmt.Fprintf(&b, "FUNCTION %s%s\n",
			qualifiedName(f.schema, f.name), f.argSig)
	}
	return chunk.Chunk{
		File:      fmt.Sprintf("%s/%s.%s", pathPrefix, f.schema, f.name),
		StartLine: 1,
		EndLine:   strings.Count(b.String(), "\n"),
		Text:      b.String(),
	}
}
