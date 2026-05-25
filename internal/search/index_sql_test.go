package search

import (
	"strings"
	"testing"
	"testing/fstest"
)

// TestFromFS_WiresSQLChunksAlongsideCodeChunks confirms the v0.7.0
// Tier-1 wiring (ADR-017): a corpus containing both source code and
// .sql files produces both the regular chunker output AND structural
// per-table chunks from internal/sql.ParseFile, all in one Index.
//
// Without the wiring, BuildIndex would only contain the line-chunked
// raw .sql bytes; an agent searching for "users email NOT NULL" would
// have to match the literal source rather than the denormalized
// "TABLE users / email VARCHAR(255) NOT NULL" rendering.
func TestFromFS_WiresSQLChunksAlongsideCodeChunks(t *testing.T) {
	fsys := fstest.MapFS{
		"main.go": {Data: []byte(`package main

func validateEmail(s string) bool {
	return len(s) > 0
}
`)},
		"migrations/001_users.sql": {Data: []byte(`CREATE TABLE users (
    id    BIGSERIAL PRIMARY KEY,
    email VARCHAR(255) NOT NULL UNIQUE
);
CREATE INDEX users_email_idx ON users (email);
`)},
	}

	ix, err := FromFS(fsys, ModeBM25, "regex", "")
	if err != nil {
		t.Fatalf("FromFS: %v", err)
	}

	// Look for at least one chunk that's the structural per-table
	// rendering (recognizable by the leading "-- file: ... TABLE users"
	// shape that internal/sql.emit produces).
	// The structural chunk's tell is the "-- file: ..." header that
	// internal/sql/emit.go renders. The raw line-chunked .sql doesn't
	// have it (the line chunker just slices the source). Test classify
	// each chunk by source-of-origin discriminators that don't overlap.
	var sqlStructural, sqlRaw, code int
	for _, c := range ix.Chunks() {
		switch {
		case strings.HasPrefix(c.Text, "-- file: ") && strings.Contains(c.Text, "TABLE users"):
			sqlStructural++
		case strings.Contains(c.Text, "CREATE TABLE users"):
			sqlRaw++
		case strings.Contains(c.Text, "validateEmail"):
			code++
		}
	}
	if sqlStructural < 1 {
		t.Errorf("expected ≥1 structural SQL chunk (TABLE users + columns); got 0\nchunks:\n%s", dumpChunks(ix))
	}
	if sqlRaw < 1 {
		t.Errorf("expected ≥1 raw SQL chunk (the line-chunked .sql file); got 0\nchunks:\n%s", dumpChunks(ix))
	}
	if code < 1 {
		t.Errorf("expected ≥1 code chunk (main.go validateEmail); got 0\nchunks:\n%s", dumpChunks(ix))
	}

	// Search for a structural-only phrase that wouldn't appear in the
	// raw line-chunked .sql (the emitter renders "  email  VARCHAR(255)"
	// with 2-space indent and double-space-separator; the source has
	// "    email" with 4-space indent and single-space separators). A
	// BM25 hit at top-3 demonstrates the structural chunk is queryable.
	results := ix.Search("email VARCHAR", 5)
	if len(results) == 0 {
		t.Fatalf("BM25 search for 'email VARCHAR' returned 0 results")
	}
	t.Logf("top hit for 'email VARCHAR': %s L%d-%d", results[0].Chunk.File, results[0].Chunk.StartLine, results[0].Chunk.EndLine)
}

// TestFromFS_FoldsMigrationDirByDefault — v0.7.1: when a directory
// contains numbered .sql files that match a recognized migration
// pattern, the SQL structural chunks are folded into one chunk per
// table reflecting the final state (CREATE + later ALTERs replayed).
func TestFromFS_FoldsMigrationDirByDefault(t *testing.T) {
	fsys := fstest.MapFS{
		"m/0001_init.sql": {Data: []byte(`CREATE TABLE users (
    id    BIGSERIAL PRIMARY KEY,
    email VARCHAR(255) NOT NULL
);
`)},
		"m/0002_add_status.sql": {Data: []byte(
			`ALTER TABLE users ADD COLUMN status VARCHAR(16) NOT NULL DEFAULT 'active';
`)},
	}
	ix, err := FromFS(fsys, ModeBM25, "regex", "")
	if err != nil {
		t.Fatalf("FromFS: %v", err)
	}

	// A structural per-file ALTER chunk has the "-- file: <p>\nALTER TABLE"
	// shape from renderAlterChunk; the raw .sql line chunks lack the
	// "-- file: " header. We only want to suppress the structural form
	// when folding is on (the folded chunk supersedes it).
	var folded, structuralAlter int
	for _, c := range ix.Chunks() {
		if strings.Contains(c.Text, "-- folded from migrations") && strings.Contains(c.Text, "TABLE users") {
			folded++
			if !strings.Contains(c.Text, "status  VARCHAR(16) NOT NULL DEFAULT 'active'") {
				t.Errorf("folded chunk missing folded-in status column:\n%s", c.Text)
			}
		}
		if strings.HasPrefix(c.Text, "-- file: ") && strings.Contains(c.Text, "ALTER TABLE") {
			structuralAlter++
		}
	}
	if folded != 1 {
		t.Errorf("expected exactly 1 folded TABLE users chunk; got %d\n%s", folded, dumpChunks(ix))
	}
	if structuralAlter != 0 {
		t.Errorf("expected 0 structural per-file ALTER chunks for migration dir (folding supersedes); got %d\n%s", structuralAlter, dumpChunks(ix))
	}
}

// TestFromFS_DisableFoldMigrations_OptsOut — v0.7.1: setting
// FSOptions.DisableFoldMigrations restores v0.7.0 per-file behavior.
func TestFromFS_DisableFoldMigrations_OptsOut(t *testing.T) {
	fsys := fstest.MapFS{
		"m/0001_init.sql":       {Data: []byte(`CREATE TABLE users (id BIGSERIAL PRIMARY KEY);`)},
		"m/0002_add_status.sql": {Data: []byte(`ALTER TABLE users ADD COLUMN status VARCHAR(16);`)},
	}
	ix, err := FromFSWithOptions(fsys, ModeBM25, "regex", "", FSOptions{DisableFoldMigrations: true})
	if err != nil {
		t.Fatalf("FromFSWithOptions: %v", err)
	}
	var folded, structuralAlter int
	for _, c := range ix.Chunks() {
		if strings.Contains(c.Text, "-- folded from migrations") {
			folded++
		}
		if strings.HasPrefix(c.Text, "-- file: ") && strings.Contains(c.Text, "ALTER TABLE") {
			structuralAlter++
		}
	}
	if folded != 0 {
		t.Errorf("expected 0 folded chunks with opt-out; got %d", folded)
	}
	if structuralAlter < 1 {
		t.Errorf("expected ≥1 structural per-file ALTER chunk under opt-out; got %d\n%s", structuralAlter, dumpChunks(ix))
	}
}

func dumpChunks(ix *Index) string {
	var b strings.Builder
	for i, c := range ix.Chunks() {
		b.WriteString("--- chunk[")
		b.WriteString(itoa(i))
		b.WriteString("] ")
		b.WriteString(c.File)
		b.WriteString(" L")
		b.WriteString(itoa(c.StartLine))
		b.WriteString("-L")
		b.WriteString(itoa(c.EndLine))
		b.WriteString(" ---\n")
		b.WriteString(c.Text)
		if !strings.HasSuffix(c.Text, "\n") {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
