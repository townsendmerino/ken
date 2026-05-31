package sql

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/townsendmerino/aikit/chunk"
)

// parse is the test convenience wrapper that asserts no error and
// returns the chunks. Pass logger=nil to discard warn messages; pass a
// bytes.Buffer to inspect them.
func parse(t *testing.T, path string, src string, logger io.Writer) []chunk.Chunk {
	t.Helper()
	chunks, err := ParseFile(path, []byte(src), logger)
	if err != nil {
		t.Fatalf("ParseFile(%q): %v", path, err)
	}
	return chunks
}

// findChunk finds the first chunk whose Text contains needle. Fails the
// test if none match.
func findChunk(t *testing.T, chunks []chunk.Chunk, needle string) chunk.Chunk {
	t.Helper()
	for _, c := range chunks {
		if strings.Contains(c.Text, needle) {
			return c
		}
	}
	t.Fatalf("no chunk contained %q; got %d chunks:\n%s", needle, len(chunks), dump(chunks))
	return chunk.Chunk{}
}

func dump(chunks []chunk.Chunk) string {
	var b strings.Builder
	for i, c := range chunks {
		b.WriteString("--- chunk[")
		b.WriteString(itoa(i))
		b.WriteString("] L")
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
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// ============================================================================
// Scenario 1: simple CREATE TABLE with PK, NOT NULL, DEFAULT
// ============================================================================

func TestScenario01_SimpleCreateTable(t *testing.T) {
	src := `CREATE TABLE users (
    id          BIGSERIAL PRIMARY KEY,
    email       VARCHAR(255) NOT NULL UNIQUE,
    role        VARCHAR(32)  NOT NULL,
    created_at  TIMESTAMP    NOT NULL DEFAULT NOW()
);`
	chunks := parse(t, "001.sql", src, nil)
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks, want 1\n%s", len(chunks), dump(chunks))
	}
	c := chunks[0]
	for _, want := range []string{"TABLE users", "id  BIGSERIAL PRIMARY KEY", "email", "VARCHAR(255) NOT NULL UNIQUE", "role", "created_at", "DEFAULT NOW()"} {
		if !strings.Contains(c.Text, want) {
			t.Errorf("chunk missing %q:\n%s", want, c.Text)
		}
	}
}

// ============================================================================
// Scenario 2: CREATE TABLE with FK references inline
// ============================================================================

func TestScenario02_CreateTableWithFK(t *testing.T) {
	src := `CREATE TABLE sessions (
    id       BIGSERIAL PRIMARY KEY,
    user_id  BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token    VARCHAR(64) NOT NULL UNIQUE
);`
	chunks := parse(t, "002.sql", src, nil)
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks, want 1\n%s", len(chunks), dump(chunks))
	}
	c := chunks[0]
	// The FK clause should land in the user_id column's rest text.
	if !strings.Contains(c.Text, "REFERENCES users(id)") {
		t.Errorf("FK reference missing:\n%s", c.Text)
	}
	if !strings.Contains(c.Text, "ON DELETE CASCADE") {
		t.Errorf("ON DELETE CASCADE missing:\n%s", c.Text)
	}
}

// ============================================================================
// Scenario 3: CREATE TABLE with CHECK constraint
// ============================================================================

func TestScenario03_CreateTableWithCheck(t *testing.T) {
	src := `CREATE TABLE products (
    id     SERIAL PRIMARY KEY,
    name   VARCHAR(100) NOT NULL,
    price  NUMERIC(10,2) NOT NULL,
    CHECK (price > 0),
    CONSTRAINT positive_id CHECK (id > 0)
);`
	chunks := parse(t, "003.sql", src, nil)
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks, want 1\n%s", len(chunks), dump(chunks))
	}
	c := chunks[0]
	// CHECK should be classified as a constraint (not a column).
	if !strings.Contains(c.Text, "CHECK (price > 0)") {
		t.Errorf("CHECK constraint missing:\n%s", c.Text)
	}
	if !strings.Contains(c.Text, "CONSTRAINT positive_id CHECK") {
		t.Errorf("named CHECK constraint missing:\n%s", c.Text)
	}
	// NUMERIC(10,2) has an internal comma — confirm the column-list
	// splitter didn't break the type by splitting on it.
	if !strings.Contains(c.Text, "NUMERIC(10,2)") {
		t.Errorf("NUMERIC(10,2) was split on internal comma:\n%s", c.Text)
	}
}

// ============================================================================
// Scenario 4: multiple tables in one file
// ============================================================================

func TestScenario04_MultipleTables(t *testing.T) {
	src := `CREATE TABLE a (x INT);
CREATE TABLE b (y INT);
CREATE TABLE c (z INT);`
	chunks := parse(t, "004.sql", src, nil)
	if len(chunks) != 3 {
		t.Fatalf("got %d chunks, want 3\n%s", len(chunks), dump(chunks))
	}
	for i, name := range []string{"TABLE a", "TABLE b", "TABLE c"} {
		if !strings.Contains(chunks[i].Text, name) {
			t.Errorf("chunk[%d] missing %q:\n%s", i, name, chunks[i].Text)
		}
	}
}

// ============================================================================
// Scenario 5: CREATE TABLE + CREATE INDEX folded
// ============================================================================

func TestScenario05_IndexFoldedIntoTable(t *testing.T) {
	src := `CREATE TABLE users (
    id    BIGSERIAL PRIMARY KEY,
    email VARCHAR(255) NOT NULL
);
CREATE INDEX users_email_idx ON users (email);`
	chunks := parse(t, "005.sql", src, nil)
	// One chunk (the table); the index is folded inside it.
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks, want 1 (index should fold)\n%s", len(chunks), dump(chunks))
	}
	c := chunks[0]
	if !strings.Contains(c.Text, "INDEX users_email_idx ON (email)") {
		t.Errorf("folded index missing:\n%s", c.Text)
	}
}

// ============================================================================
// Scenario 6: standalone CREATE INDEX (table defined elsewhere)
// ============================================================================

func TestScenario06_StandaloneIndex(t *testing.T) {
	src := `CREATE INDEX users_email_idx ON users (email);
CREATE UNIQUE INDEX users_uid ON users (id);`
	chunks := parse(t, "006.sql", src, nil)
	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want 2\n%s", len(chunks), dump(chunks))
	}
	findChunk(t, chunks, "INDEX users_email_idx ON users (email)")
	findChunk(t, chunks, "UNIQUE INDEX users_uid ON users (id)")
}

// ============================================================================
// Scenario 7: CREATE VIEW with multi-line body
// ============================================================================

func TestScenario07_CreateView(t *testing.T) {
	src := `CREATE VIEW active_users AS
    SELECT u.id, u.email, COUNT(s.id) AS session_count
    FROM users u
    LEFT JOIN sessions s ON s.user_id = u.id
    WHERE u.deleted_at IS NULL
    GROUP BY u.id, u.email;`
	chunks := parse(t, "007.sql", src, nil)
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks, want 1\n%s", len(chunks), dump(chunks))
	}
	c := chunks[0]
	for _, want := range []string{"VIEW active_users AS", "FROM users u", "LEFT JOIN sessions", "GROUP BY u.id"} {
		if !strings.Contains(c.Text, want) {
			t.Errorf("VIEW chunk missing %q:\n%s", want, c.Text)
		}
	}
}

// ============================================================================
// Scenario 8: ALTER TABLE ADD COLUMN → standalone, original table chunk unchanged
// ============================================================================

func TestScenario08_AlterTable(t *testing.T) {
	src := `CREATE TABLE users (id BIGSERIAL PRIMARY KEY);
ALTER TABLE users ADD COLUMN status VARCHAR(16) NOT NULL DEFAULT 'active';`
	chunks := parse(t, "008.sql", src, nil)
	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want 2 (table + alter)\n%s", len(chunks), dump(chunks))
	}
	tableChunk := findChunk(t, chunks, "TABLE users")
	// Original table chunk does NOT mention 'status' (no migration folding).
	if strings.Contains(tableChunk.Text, "status") {
		t.Errorf("original CREATE TABLE chunk should NOT contain ALTER-added column 'status':\n%s", tableChunk.Text)
	}
	alterChunk := findChunk(t, chunks, "ALTER TABLE users")
	if !strings.Contains(alterChunk.Text, "ADD COLUMN status") {
		t.Errorf("ALTER chunk missing ADD COLUMN status:\n%s", alterChunk.Text)
	}
	if !strings.Contains(alterChunk.Text, "DEFAULT 'active'") {
		t.Errorf("ALTER chunk missing DEFAULT 'active':\n%s", alterChunk.Text)
	}
}

// ============================================================================
// Scenario 9: weird whitespace, line/block/nested-block comments
// ============================================================================

func TestScenario09_Comments(t *testing.T) {
	src := `-- line comment before
/* block comment
   spanning multiple lines */
CREATE TABLE   foo   (
    a INT, -- inline line comment
    b INT  /* inline block */ NOT NULL,
    /* /* nested block comment */ */
    c INT
);
-- trailing line comment with semicolon ; inside it
/* trailing block ; with semicolon */`
	chunks := parse(t, "009.sql", src, nil)
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks, want 1\n%s", len(chunks), dump(chunks))
	}
	c := chunks[0]
	for _, want := range []string{"TABLE foo", "a  INT", "b  INT", "c  INT", "NOT NULL"} {
		if !strings.Contains(c.Text, want) {
			t.Errorf("chunk missing %q:\n%s", want, c.Text)
		}
	}
}

// ============================================================================
// Scenario 10: Postgres dollar-quoting
// ============================================================================

func TestScenario10_DollarQuoting(t *testing.T) {
	// A function body with $$ ... $$ and a tagged $tag$ ... $tag$ —
	// neither should affect statement splitting. The function itself is
	// stmtUnknown (we don't index function bodies in v0.7.0) so the
	// only chunk should be the CREATE TABLE after it.
	src := `CREATE OR REPLACE FUNCTION greet(name TEXT) RETURNS TEXT AS $$
BEGIN
    RETURN 'hi; ' || name;
END;
$$ LANGUAGE plpgsql;
CREATE OR REPLACE FUNCTION x() RETURNS TEXT AS $body$
    SELECT 'a; b; c'::TEXT;
$body$ LANGUAGE sql;
CREATE TABLE after_func (id INT);`
	chunks := parse(t, "010.sql", src, nil)
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks, want 1 (only CREATE TABLE)\n%s", len(chunks), dump(chunks))
	}
	if !strings.Contains(chunks[0].Text, "TABLE after_func") {
		t.Errorf("CREATE TABLE after dollar-quoted functions missing:\n%s", chunks[0].Text)
	}
}

// ============================================================================
// Scenario 11: DML / GRANT / COMMENT only — zero chunks, no error
// ============================================================================

func TestScenario11_NoDDLNoChunks(t *testing.T) {
	src := `INSERT INTO users (email) VALUES ('alice@example.com');
UPDATE users SET role = 'admin' WHERE id = 1;
DELETE FROM users WHERE deleted_at < NOW() - INTERVAL '1 year';
GRANT SELECT ON users TO readonly_role;
COMMENT ON TABLE users IS 'application users';`
	logBuf := &bytes.Buffer{}
	chunks := parse(t, "011.sql", src, logBuf)
	if len(chunks) != 0 {
		t.Errorf("got %d chunks, want 0 for DML/GRANT/COMMENT-only file\n%s", len(chunks), dump(chunks))
	}
	// No warnings should fire — these aren't malformed, they're just not DDL.
	if logBuf.Len() > 0 {
		t.Errorf("expected no warn messages for non-DDL file, got:\n%s", logBuf.String())
	}
}

// ============================================================================
// Scenario 12: malformed statement mid-file — surrounding good statements OK
// ============================================================================

func TestScenario12_MalformedMidFile(t *testing.T) {
	// The middle statement is missing the closing ); — the splitter
	// will consume bytes until the next ; that closes the parens. Since
	// the parens are unbalanced, the entire rest of the file is one
	// statement up to the final ;. The good first statement should still
	// be parsed; the malformed one should log and skip.
	src := `CREATE TABLE good_one (id INT);
CREATE TABLE bad_one (
    x INT
    -- missing closing paren!
CREATE TABLE good_two (id INT);`
	logBuf := &bytes.Buffer{}
	chunks := parse(t, "012.sql", src, logBuf)
	// At minimum the first good table is parsed. The "bad" statement
	// either fails to classify or fails to parse — either way, no chunk
	// for it. good_two may or may not be salvageable depending on how
	// the splitter recovers — we accept ≥1 chunks containing "good_one".
	if len(chunks) < 1 {
		t.Fatalf("expected at least 1 chunk for good_one, got 0\n%s", dump(chunks))
	}
	findChunk(t, chunks, "TABLE good_one")
	t.Logf("log output:\n%s", logBuf.String())
	t.Logf("got %d chunks total", len(chunks))
}

// ============================================================================
// Cross-cutting: IsSQLFile
// ============================================================================

func TestIsSQLFile(t *testing.T) {
	cases := map[string]bool{
		"foo.sql":            true,
		"migrations/001.sql": true,
		"schema.ddl":         true,
		"foo.SQL":            true,
		"foo.DDL":            true,
		"foo.go":             false,
		"foo.md":             false,
		"foo":                false,
		"sql_in_name.txt":    false,
		"":                   false,
	}
	for p, want := range cases {
		if got := IsSQLFile(p); got != want {
			t.Errorf("IsSQLFile(%q) = %v, want %v", p, got, want)
		}
	}
}

// ============================================================================
// View truncation
// ============================================================================

func TestViewTruncatedWhenLong(t *testing.T) {
	var b strings.Builder
	b.WriteString("CREATE VIEW long_view AS\n")
	for range 100 {
		b.WriteString("    SELECT 1,\n")
	}
	b.WriteString("    SELECT 2;")
	chunks := parse(t, "long_view.sql", b.String(), nil)
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks, want 1", len(chunks))
	}
	if !strings.Contains(chunks[0].Text, "view body truncated") {
		t.Errorf("expected truncation notice, got:\n%s", chunks[0].Text)
	}
}

// TestStatementStartLine_AfterMultiLineQuoted pins the H3 fix:
// `splitStatements` now counts newlines inside single-quoted strings
// and double-quoted identifiers so the NEXT statement's startLine is
// accurate. Pre-fix, the line counter only caught up after
// dollar-quoted strings; the two sibling quote forms silently skipped
// past embedded `\n`, and subsequent CREATE TABLE chunks reported a
// too-low StartLine in MCP search citations.
func TestStatementStartLine_AfterMultiLineQuoted(t *testing.T) {
	cases := []struct {
		name        string
		src         string
		needle      string
		wantStart   int
		description string
	}{
		{
			name: "single-quoted body spans 3 lines",
			src: "CREATE TABLE foo (id INT);\n" +
				"INSERT INTO foo VALUES ('a\nb\nc');\n" +
				"CREATE TABLE bar (id INT);",
			needle:      "TABLE bar",
			wantStart:   5,
			description: "line 1=foo; line 2 begins INSERT, two \\n inside the string body push through lines 3,4; line 5=bar. Pre-fix reported a too-low value.",
		},
		{
			name: "double-quoted identifier spans 2 lines",
			src: "CREATE TABLE foo (id INT);\n" +
				"CREATE TABLE \"weird\nname\" (id INT);\n" +
				"CREATE TABLE bar (id INT);",
			needle:      "TABLE bar",
			wantStart:   4,
			description: "line 1=foo; lines 2-3=quoted-ident table (one \\n inside the identifier); line 4=bar. Pre-fix reported 3.",
		},
		{
			name: "multiple multi-line single-quoted strings accumulate",
			src: "CREATE TABLE foo (id INT);\n" +
				"INSERT INTO foo VALUES ('first\nspan');\n" +
				"INSERT INTO foo VALUES ('second\nspan');\n" +
				"CREATE TABLE bar (id INT);",
			needle:      "TABLE bar",
			wantStart:   6,
			description: "line 6=bar after two multi-line INSERTs each pushing one \\n. Pre-fix reported 4 (counting only the four bare statement-separator \\n).",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chunks := parse(t, "lines.sql", tc.src, nil)
			c := findChunk(t, chunks, tc.needle)
			if c.StartLine != tc.wantStart {
				t.Errorf("StartLine = %d, want %d (%s)", c.StartLine, tc.wantStart, tc.description)
			}
		})
	}
}
