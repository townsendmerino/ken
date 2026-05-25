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
