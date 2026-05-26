package sql

// Migration-directory detection (v0.7.1 / ADR-018).
//
// The pattern-matching half of migration folding: cheap filename
// classification + the IsMigrationDir guard the caller uses to
// decide whether to invoke the more expensive FoldMigrations
// (which lives in fold.go alongside the replay machinery).
//
// Split off from fold.go in v0.8.1 (Part A) so v0.8.1 Part C's
// RENAME COLUMN / RENAME CONSTRAINT support has an obvious home
// for the new state — the per-table rename map + chain resolution
// helpers — without further bloating fold.go.

import (
	"io/fs"
	"regexp"
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
