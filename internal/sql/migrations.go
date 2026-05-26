package sql

// Migration-directory detection (v0.7.1 / ADR-018) + per-table column /
// constraint RENAME application (v0.8.1 Part C / ADR-022).
//
// The pattern-matching half of migration folding lives here: cheap
// filename classification + the IsMigrationDir guard the caller uses
// to decide whether to invoke the more expensive FoldMigrations
// (which lives in fold.go alongside the replay machinery).
//
// v0.8.1 Part C also lands the RENAME COLUMN + RENAME CONSTRAINT
// helpers here — applied eagerly during ALTER replay (mutating the
// in-flight foldedTable directly) rather than via a lazy rename map
// resolved at chunk emission. Eager application keeps state simple
// (the existing foldedTable.columns IS the post-rename state) and
// naturally handles the "rename A→B then ADD A" pattern where the
// re-added A is a fresh column distinct from the now-B former A.
// See ADR-022 for the rationale + the rejected lazy/rename-map
// alternative + the calibration-credibility framing (Tier-1 chunk
// content fidelity, NOT recall improvement).

import (
	"io/fs"
	"regexp"
	"strings"
)

// migrationPatterns are the recognized "this is a migration directory"
// filename forms, listed MOST-SPECIFIC FIRST. Each filename is
// classified by the first pattern that matches, so a 14-digit Rails
// timestamp doesn't double-count as a generic numbered Goose file.
//
// A directory is treated as migrations iff at least `minMigrationFiles`
// files share a single classified pattern. Mixed patterns in the same
// directory pick the dominant pattern (most matches); the minority is
// logged but ignored (won't fold under a different pattern's ordering).
var migrationPatterns = []*regexp.Regexp{
	regexp.MustCompile(`^\d{14}_[\w.\-]+\.sql$`), // Rails 5+ / Alembic timestamp (must come before generic numeric)
	regexp.MustCompile(`^V\d+__[\w.\-]+\.sql$`),  // Flyway
	regexp.MustCompile(`^\d+_[\w.\-]+\.sql$`),    // Goose / dbmate / Rails 4-style
}

// classifyMigrationName returns the index of the first pattern that
// matches name, or -1 if none. Used so each file picks exactly one
// pattern slot rather than double-counting.
func classifyMigrationName(name string) int {
	for i, p := range migrationPatterns {
		if p.MatchString(name) {
			return i
		}
	}
	return -1
}

// minMigrationFiles is the floor for treating a directory as migrations.
// A single file is a one-off seed; two or more numbered files indicate
// an ordered chain.
const minMigrationFiles = 2

// IsMigrationDir reports whether dir (inside fsys) looks like an ordered
// migration chain — at least minMigrationFiles entries classified under
// the same migration pattern. Read-only; does not parse statements.
// Caller can guard a more expensive FoldMigrations call with this.
func IsMigrationDir(fsys fs.FS, dir string) bool {
	if fsys == nil || dir == "" {
		return false
	}
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return false
	}
	counts := make([]int, len(migrationPatterns))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if idx := classifyMigrationName(e.Name()); idx >= 0 {
			counts[idx]++
		}
	}
	for _, n := range counts {
		if n >= minMigrationFiles {
			return true
		}
	}
	return false
}

// ── RENAME application (v0.8.1 Part C / ADR-022) ────────────────────
//
// Helpers below are called by fold.go's applyRename branch when an
// ALTER TABLE ... RENAME COLUMN / RENAME CONSTRAINT statement fires.
// All mutate the supplied *foldedTable in-place; returns false on
// failure (source not found), which the caller turns into a warning
// + BOTH-chunks fallback (preserving the per-file ALTER chunk so the
// agent always sees the rename in the raw migration text).

// applyColumnRename renames a column in t from oldName to newName.
// Mutates the columnDef.name in place AND rewrites references to
// oldName within the first parenthesized group of every constraint
// string (PK / UNIQUE / FK source-side col list / CHECK expression).
// Returns false iff no column matching oldName exists.
//
// Cross-table FK target-side renames are NOT propagated: a constraint
// of the form "FOREIGN KEY (local) REFERENCES other(remote)" only has
// its local-side rewritten via the first-parens scope. The remote
// column is in a different table and out of scope for the per-table
// rename machinery — see ADR-022 for the rationale.
func applyColumnRename(t *foldedTable, oldName, newName string) bool {
	idx := findColumn(t, oldName)
	if idx < 0 {
		return false
	}
	t.columns[idx].name = newName

	// Rewrite source-side column references inside this table's
	// constraints. We scope the rewrite to the FIRST parenthesized
	// group so the FK target-side ("REFERENCES other(remote_col)")
	// is preserved verbatim — that's a cross-table reference the
	// per-table rename map shouldn't touch.
	for i, c := range t.constraints {
		t.constraints[i] = renameInFirstParens(c, oldName, newName)
	}
	return true
}

// applyConstraintRename renames a named constraint in t. Constraint
// strings prefixed with `CONSTRAINT <name>` get the name rewritten;
// anonymous constraints (bare `PRIMARY KEY (id)` etc.) have no name
// to match, so the rename fails (returns false) and the caller emits
// a BOTH-chunks fallback.
//
// M7 hardening: if the new name contains characters that require SQL
// identifier quoting (whitespace, punctuation, leading digit, etc.),
// emit it wrapped in double quotes. Without this, a rename of a
// previously-bare-identifier constraint TO a quoted name (e.g.
// `RENAME CONSTRAINT normal TO "weird name"`) would have produced an
// invalid `CONSTRAINT weird name ...` line in the folded chunk —
// fine for retrieval (BM25 still finds the words) but lossy if an
// agent reads the chunk and tries to act on the SQL.
func applyConstraintRename(t *foldedTable, oldName, newName string) bool {
	for i, c := range t.constraints {
		ctoks := tokenize([]byte(c))
		if len(ctoks) < 2 || !strings.EqualFold(ctoks[0].text, "CONSTRAINT") {
			continue
		}
		name := strings.Trim(ctoks[1].text, `"`)
		if !strings.EqualFold(name, oldName) {
			continue
		}
		// Rebuild with the new name, preserving everything after the
		// constraint identifier (the constraint body).
		bodyStart := ctoks[1].start + len(ctoks[1].text)
		t.constraints[i] = "CONSTRAINT " + quoteIdentIfNeeded(newName) + c[bodyStart:]
		return true
	}
	return false
}

// quoteIdentIfNeeded wraps name in SQL double-quotes when it contains
// any character that isn't a bare-identifier character (ASCII letters,
// digits after the first position, or underscore). Used by the
// constraint-rename path to preserve syntactic validity when an
// agent-supplied newName needs quoting.
//
// Conservative heuristic: doesn't track reserved-word collisions
// (would require a per-engine reserved-word table). The classic
// "needs quotes because it's a keyword like `user`" case slips
// through. Documented as a known limitation; if it surfaces in
// practice, extend with a small per-engine word list.
func quoteIdentIfNeeded(name string) string {
	if name == "" {
		return `""`
	}
	for i, c := range name {
		switch {
		case c == '_':
			// always OK
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
			// always OK
		case c >= '0' && c <= '9':
			if i == 0 {
				return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
			}
		default:
			return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
		}
	}
	return name
}

// renameInFirstParens replaces word-boundary occurrences of oldName
// with newName inside the FIRST parenthesized group of s. Nested
// parens are handled — the "first group" closes at the matching
// outer ')'. Returns s unchanged if no parens are present.
//
// Why first-parens: every constraint shape we render
// (PRIMARY KEY (cols), UNIQUE (cols), FOREIGN KEY (src) REFERENCES
// other(tgt), CHECK (expr), CONSTRAINT name <body>) puts the
// THIS-TABLE column list in the first paren group. The FK target-
// side column list ("other(tgt)") lives in a LATER paren group and
// is left alone — those columns belong to a different table.
func renameInFirstParens(s, oldName, newName string) string {
	open := strings.IndexByte(s, '(')
	if open < 0 {
		return s
	}
	close := matchingParen(s, open)
	if close < 0 {
		return s
	}
	inner := s[open+1 : close]
	rewritten := wordBoundaryReplace(inner, oldName, newName)
	return s[:open+1] + rewritten + s[close:]
}

// matchingParen returns the index of the ')' that matches the '('
// at openIdx. Returns -1 if unbalanced. Tracks nesting depth so
// `foo (a, (b, c))` correctly returns the index of the OUTER ')'.
//
// String/identifier-quote awareness is deliberately omitted: the
// constraint text fold.go stores has already passed through the
// tokenizer's quote handling, so we only see paren tokens that
// belong to the constraint structure. (Hostile inputs with quoted
// parens fall through harmlessly — first-parens just spans more
// than intended; word-boundary replace inside is still safe.)
func matchingParen(s string, openIdx int) int {
	depth := 0
	for i := openIdx; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// wordBoundaryReplace replaces every word-boundary occurrence of
// oldName with newName in s. Uses a regexp.QuoteMeta'd oldName +
// `\b` anchors so a rename of "email" doesn't accidentally match
// "email_verified" or "current_email" — the surrounding chars in
// those cases are word-chars (incl. underscore), so the boundary
// doesn't fire.
//
// Empty oldName returns s unchanged (defense-in-depth; the parsers
// should never pass empty).
func wordBoundaryReplace(s, oldName, newName string) string {
	if oldName == "" {
		return s
	}
	re := regexp.MustCompile(`\b` + regexp.QuoteMeta(oldName) + `\b`)
	return re.ReplaceAllString(s, newName)
}
