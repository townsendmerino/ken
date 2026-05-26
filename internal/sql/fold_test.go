package sql

import (
	"bytes"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

// makeMigrationFS is a tiny helper for the in-memory migration fixtures
// these scenarios pass into FoldMigrations / IsMigrationDir.
func makeMigrationFS(files map[string]string) fs.FS {
	mfs := fstest.MapFS{}
	for name, body := range files {
		mfs[name] = &fstest.MapFile{Data: []byte(body)}
	}
	return mfs
}

func mustFold(t *testing.T, fsys fs.FS, dir string) ([]byte, []byte) {
	t.Helper()
	var warnBuf bytes.Buffer
	chunks, err := FoldMigrations(fsys, dir, &warnBuf)
	if err != nil {
		t.Fatalf("FoldMigrations(%q): %v", dir, err)
	}
	if len(chunks) == 0 {
		return nil, warnBuf.Bytes()
	}
	var all bytes.Buffer
	for _, c := range chunks {
		all.WriteString(c.Text)
		all.WriteString("\n---\n")
	}
	return all.Bytes(), warnBuf.Bytes()
}

func mustContain(t *testing.T, body []byte, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !bytes.Contains(body, []byte(w)) {
			t.Errorf("expected output to contain %q; got:\n%s", w, body)
		}
	}
}

func mustNotContain(t *testing.T, body []byte, unwants ...string) {
	t.Helper()
	for _, u := range unwants {
		if bytes.Contains(body, []byte(u)) {
			t.Errorf("expected output to NOT contain %q; got:\n%s", u, body)
		}
	}
}

// ============================================================================
// Scenario F01: Goose-style numbered migrations — add then drop column.
// 0001 creates users(id, role, email); 0002 drops role.
// Folded chunk has id + email, NOT role.
// ============================================================================

func TestFold01_GooseStyle(t *testing.T) {
	fsys := makeMigrationFS(map[string]string{
		"m/0001_create_users.sql": `
CREATE TABLE users (
    id    BIGSERIAL PRIMARY KEY,
    role  VARCHAR(32) NOT NULL,
    email VARCHAR(255) NOT NULL UNIQUE
);`,
		"m/0002_drop_role.sql": `ALTER TABLE users DROP COLUMN role;`,
	})
	if !IsMigrationDir(fsys, "m") {
		t.Fatal("IsMigrationDir(m) = false; want true")
	}
	body, warns := mustFold(t, fsys, "m")
	if len(warns) > 0 {
		t.Errorf("unexpected warns: %s", warns)
	}
	mustContain(t, body, "-- folded from migrations", "TABLE users", "id", "email", "VARCHAR(255) NOT NULL UNIQUE")
	mustNotContain(t, body, "role  VARCHAR")
}

// ============================================================================
// Scenario F02: Flyway-style (V<n>__<name>.sql) — add column then alter type.
// ============================================================================

func TestFold02_FlywayStyle(t *testing.T) {
	fsys := makeMigrationFS(map[string]string{
		"m/V1__init.sql":      `CREATE TABLE accounts (id INT PRIMARY KEY);`,
		"m/V2__add_email.sql": `ALTER TABLE accounts ADD COLUMN email VARCHAR(64);`,
		"m/V3__widen_email.sql": `ALTER TABLE accounts
ALTER COLUMN email TYPE VARCHAR(255);`,
	})
	if !IsMigrationDir(fsys, "m") {
		t.Fatal("IsMigrationDir(m) = false; want true")
	}
	body, warns := mustFold(t, fsys, "m")
	if len(warns) > 0 {
		t.Errorf("unexpected warns: %s", warns)
	}
	mustContain(t, body, "TABLE accounts", "email  VARCHAR(255)")
	mustNotContain(t, body, "email  VARCHAR(64)")
}

// ============================================================================
// Scenario F03: Rails 14-digit timestamp prefix.
// ============================================================================

func TestFold03_RailsTimestamp(t *testing.T) {
	fsys := makeMigrationFS(map[string]string{
		"m/20240315120000_create_posts.sql": `CREATE TABLE posts (
    id    SERIAL PRIMARY KEY,
    title TEXT NOT NULL
);`,
		"m/20240316120000_add_body.sql": `ALTER TABLE posts ADD COLUMN body TEXT;`,
	})
	if !IsMigrationDir(fsys, "m") {
		t.Fatal("IsMigrationDir(m) = false; want true")
	}
	body, warns := mustFold(t, fsys, "m")
	if len(warns) > 0 {
		t.Errorf("unexpected warns: %s", warns)
	}
	mustContain(t, body, "TABLE posts", "title  TEXT NOT NULL", "body  TEXT")
}

// ============================================================================
// Scenario F04: Mixed patterns in same dir — dominant pattern wins, warns
// about the minority.
// ============================================================================

func TestFold04_MixedPatterns(t *testing.T) {
	fsys := makeMigrationFS(map[string]string{
		"m/0001_a.sql":     `CREATE TABLE x (id INT);`,
		"m/0002_b.sql":     `ALTER TABLE x ADD COLUMN flag BOOLEAN;`,
		"m/0003_c.sql":     `ALTER TABLE x ADD COLUMN n INT;`,
		"m/V99__weird.sql": `CREATE TABLE y (id INT);`, // minority Flyway pattern
	})
	body, warns := mustFold(t, fsys, "m")
	if !strings.Contains(string(warns), "mixed migration patterns") {
		t.Errorf("expected mixed-patterns warn; got:\n%s", warns)
	}
	mustContain(t, body, "TABLE x", "flag  BOOLEAN", "n  INT")
	// y was excluded (minority pattern), so it should NOT appear in folded output.
	mustNotContain(t, body, "TABLE y")
}

// ============================================================================
// Scenario F05: multiple independent tables — each gets its own folded chunk.
// ============================================================================

func TestFold05_MultipleTables(t *testing.T) {
	fsys := makeMigrationFS(map[string]string{
		"m/0001_init.sql": `CREATE TABLE a (id INT);
CREATE TABLE b (id INT);`,
		"m/0002_evolve.sql": `ALTER TABLE a ADD COLUMN x INT;
ALTER TABLE b ADD COLUMN y INT;`,
	})
	body, _ := mustFold(t, fsys, "m")
	mustContain(t, body, "TABLE a", "x  INT", "TABLE b", "y  INT")
}

// ============================================================================
// Scenario F06: ALTER references a CREATE that lives in another (non-migration)
// directory. With no CREATE TABLE in the migration dir, the ALTER is preserved
// as a per-file chunk (warn logged).
// ============================================================================

func TestFold06_AlterWithoutCreate(t *testing.T) {
	fsys := makeMigrationFS(map[string]string{
		"m/0001_alter1.sql": `ALTER TABLE external_table ADD COLUMN extra INT;`,
		"m/0002_alter2.sql": `ALTER TABLE external_table ADD COLUMN more INT;`,
	})
	body, warns := mustFold(t, fsys, "m")
	if !strings.Contains(string(warns), "no matching CREATE TABLE") {
		t.Errorf("expected 'no matching CREATE TABLE' warn; got:\n%s", warns)
	}
	// Per-file ALTER chunk preserved.
	mustContain(t, body, "ALTER TABLE external_table", "ADD COLUMN extra")
}

// ============================================================================
// Scenario F07: DROP COLUMN on a column that doesn't exist (typo).
// Warn + per-file chunk preserved + folded chunk still emitted with what
// could be applied.
// ============================================================================

func TestFold07_DropMissingColumn(t *testing.T) {
	fsys := makeMigrationFS(map[string]string{
		"m/0001_init.sql": `CREATE TABLE items (id INT, name TEXT);`,
		"m/0002_typo.sql": `ALTER TABLE items DROP COLUMN nme;`, // typo: nme vs name
	})
	body, warns := mustFold(t, fsys, "m")
	if !strings.Contains(string(warns), "DROP COLUMN") {
		t.Errorf("expected DROP COLUMN warn; got:\n%s", warns)
	}
	// Folded chunk still emitted with original columns intact.
	mustContain(t, body, "TABLE items", "id  INT", "name  TEXT")
	// Per-file ALTER chunk also emitted.
	mustContain(t, body, "ALTER TABLE items", "DROP COLUMN nme")
}

// ============================================================================
// Scenario F08: ALTER COLUMN ... TYPE updates the column type in the folded chunk.
// ============================================================================

func TestFold08_AlterColumnType(t *testing.T) {
	fsys := makeMigrationFS(map[string]string{
		"m/0001_init.sql":  `CREATE TABLE t (id INT, name VARCHAR(10));`,
		"m/0002_widen.sql": `ALTER TABLE t ALTER COLUMN name TYPE VARCHAR(255);`,
	})
	body, warns := mustFold(t, fsys, "m")
	if len(warns) > 0 {
		t.Errorf("unexpected warns: %s", warns)
	}
	mustContain(t, body, "name  VARCHAR(255)")
	mustNotContain(t, body, "name  VARCHAR(10)")
}

// ============================================================================
// Scenario F09: ADD CONSTRAINT appears as a constraint line in folded chunk.
// ============================================================================

func TestFold09_AddConstraint(t *testing.T) {
	fsys := makeMigrationFS(map[string]string{
		"m/0001_init.sql":   `CREATE TABLE orders (id INT, ref VARCHAR(32));`,
		"m/0002_unique.sql": `ALTER TABLE orders ADD CONSTRAINT orders_ref_unique UNIQUE (ref);`,
		"m/0003_filler.sql": `-- nothing here`,
	})
	body, warns := mustFold(t, fsys, "m")
	if len(warns) > 0 {
		t.Errorf("unexpected warns: %s", warns)
	}
	mustContain(t, body, "CONSTRAINT orders_ref_unique UNIQUE (ref)")
}

// ============================================================================
// Scenario F10: DROP COLUMN immediately followed by ADD COLUMN of same name —
// folded chunk has the new column, not the old.
// ============================================================================

func TestFold10_DropThenReAdd(t *testing.T) {
	fsys := makeMigrationFS(map[string]string{
		"m/0001_init.sql":  `CREATE TABLE u (id INT, name VARCHAR(10) NOT NULL);`,
		"m/0002_drop.sql":  `ALTER TABLE u DROP COLUMN name;`,
		"m/0003_readd.sql": `ALTER TABLE u ADD COLUMN name TEXT;`,
	})
	body, warns := mustFold(t, fsys, "m")
	if len(warns) > 0 {
		t.Errorf("unexpected warns: %s", warns)
	}
	mustContain(t, body, "name  TEXT")
	mustNotContain(t, body, "name  VARCHAR(10) NOT NULL")
}

// ============================================================================
// Scenario F11: Single-file directory — NOT a migration dir.
// IsMigrationDir returns false; FoldMigrations returns (nil, nil).
// ============================================================================

func TestFold11_SingleFileDirIsNotMigrations(t *testing.T) {
	fsys := makeMigrationFS(map[string]string{
		"seeds/0001_seed.sql": `INSERT INTO foo VALUES (1);`,
	})
	if IsMigrationDir(fsys, "seeds") {
		t.Error("single-file dir should not be a migration dir")
	}
	chunks, err := FoldMigrations(fsys, "seeds", nil)
	if err != nil {
		t.Fatalf("FoldMigrations: %v", err)
	}
	if len(chunks) != 0 {
		t.Errorf("expected nil chunks, got %d", len(chunks))
	}
}

// ============================================================================
// Scenario F12: Non-migration naming patterns (current.sql, admin.sql) —
// IsMigrationDir returns false even with multiple files.
// ============================================================================

func TestFold12_NonMigrationNamesIgnored(t *testing.T) {
	fsys := makeMigrationFS(map[string]string{
		"schema/current.sql":   `CREATE TABLE a (id INT);`,
		"schema/admin.sql":     `CREATE TABLE b (id INT);`,
		"schema/auxiliary.sql": `CREATE TABLE c (id INT);`,
	})
	if IsMigrationDir(fsys, "schema") {
		t.Error("non-numbered file dir should not be a migration dir")
	}
	chunks, err := FoldMigrations(fsys, "schema", nil)
	if err != nil {
		t.Fatalf("FoldMigrations: %v", err)
	}
	if len(chunks) != 0 {
		t.Errorf("expected nil chunks (no dominant pattern), got %d", len(chunks))
	}
}

// ============================================================================
// Scenario F13: CREATE INDEX inside migration dir folds into table chunk.
// ============================================================================

func TestFold13_IndexFoldedAcrossFiles(t *testing.T) {
	fsys := makeMigrationFS(map[string]string{
		"m/0001_init.sql":  `CREATE TABLE customers (id INT, email VARCHAR(255));`,
		"m/0002_index.sql": `CREATE INDEX customers_email_idx ON customers (email);`,
	})
	body, warns := mustFold(t, fsys, "m")
	if len(warns) > 0 {
		t.Errorf("unexpected warns: %s", warns)
	}
	mustContain(t, body, "TABLE customers", "INDEX customers_email_idx ON (email)")
}

// ============================================================================
// Scenario F14: Multi-action ALTER (`ALTER TABLE x ADD COLUMN a INT, ADD COLUMN b TEXT`).
// Both columns appear in the folded chunk.
// ============================================================================

func TestFold14_MultiActionAlter(t *testing.T) {
	fsys := makeMigrationFS(map[string]string{
		"m/0001_init.sql":  `CREATE TABLE m (id INT);`,
		"m/0002_multi.sql": `ALTER TABLE m ADD COLUMN a INT, ADD COLUMN b TEXT;`,
	})
	body, warns := mustFold(t, fsys, "m")
	if len(warns) > 0 {
		t.Errorf("unexpected warns: %s", warns)
	}
	mustContain(t, body, "a  INT", "b  TEXT")
}

// ============================================================================
// Scenario F15: RENAME COLUMN now folds (v0.8.1 Part C / ADR-022). Replaces
// the v0.7.1 deferral assertion this test originally pinned. Post-rename
// column name shows in the folded chunk; no warning + no per-file ALTER
// chunk for the rename itself (the action was applied cleanly).
// ============================================================================

func TestFold15_RenameColumnFolds(t *testing.T) {
	fsys := makeMigrationFS(map[string]string{
		"m/0001_init.sql":   `CREATE TABLE x (id INT, old_name TEXT);`,
		"m/0002_rename.sql": `ALTER TABLE x RENAME COLUMN old_name TO new_name;`,
	})
	body, warns := mustFold(t, fsys, "m")
	if len(warns) > 0 {
		t.Errorf("unexpected warns from a foldable RENAME: %s", warns)
	}
	mustContain(t, body, "TABLE x", "new_name  TEXT")
	mustNotContain(t, body, "old_name  TEXT")
	// The folded chunk is the only chunk — no per-file ALTER leftover.
	mustNotContain(t, body, "ALTER TABLE x")
}

// ============================================================================
// v0.8.1 Part C (ADR-022): RENAME COLUMN + RENAME CONSTRAINT folding —
// edge-case coverage. The simple-rename case is F15 above; the cases below
// cover chain resolution, the rename-then-re-add interaction, drop-then-
// re-add-then-rename, cross-table FK source-side renames, multi-column
// constraint participation, RENAME CONSTRAINT path, the missing-source
// fallback, idempotence as a regression guard against accidental
// statefulness, and the MySQL CHANGE syntax fallback.
// ============================================================================

func TestFoldRename_Chain(t *testing.T) {
	// A→B→C across three files. Eager replay: each rename mutates the
	// in-flight foldedTable so the chain resolves naturally to C.
	fsys := makeMigrationFS(map[string]string{
		"m/0001_init.sql":   `CREATE TABLE x (id INT, col_a TEXT);`,
		"m/0002_a_to_b.sql": `ALTER TABLE x RENAME COLUMN col_a TO col_b;`,
		"m/0003_b_to_c.sql": `ALTER TABLE x RENAME COLUMN col_b TO col_c;`,
	})
	body, warns := mustFold(t, fsys, "m")
	if len(warns) > 0 {
		t.Errorf("unexpected warns: %s", warns)
	}
	mustContain(t, body, "col_c  TEXT")
	mustNotContain(t, body, "col_a  TEXT", "col_b  TEXT")
}

func TestFoldRename_CycleRoundTrip(t *testing.T) {
	// A→B→A. Eager application: column A becomes B becomes A again.
	// No cycle detection; no BOTH-chunks fallback. Final state matches
	// initial state. If the operator intended a revert, the chunk is
	// correct. If the operator intended A→B→C and typo'd C as A, the
	// raw migration files (chunked separately by the FS walker) still
	// surface the typo — the agent sees both surfaces.
	fsys := makeMigrationFS(map[string]string{
		"m/0001_init.sql":   `CREATE TABLE x (id INT, col_a TEXT);`,
		"m/0002_a_to_b.sql": `ALTER TABLE x RENAME COLUMN col_a TO col_b;`,
		"m/0003_b_to_a.sql": `ALTER TABLE x RENAME COLUMN col_b TO col_a;`,
	})
	body, warns := mustFold(t, fsys, "m")
	if len(warns) > 0 {
		t.Errorf("unexpected warns for round-trip rename: %s", warns)
	}
	mustContain(t, body, "col_a  TEXT")
	mustNotContain(t, body, "col_b  TEXT", "ALTER TABLE x")
}

func TestFoldRename_ConflictWithReAdd(t *testing.T) {
	// RENAME A→B, then ADD COLUMN A. Eager natural behavior: the
	// rename removes A (it's now B); the subsequent ADD creates a
	// fresh A. Final: two columns (B and the fresh A). This is the
	// case that lazy-rename-map resolution would mishandle without
	// per-file ordering state — see ADR-022.
	fsys := makeMigrationFS(map[string]string{
		"m/0001_init.sql":   `CREATE TABLE x (id INT, col_a TEXT);`,
		"m/0002_rename.sql": `ALTER TABLE x RENAME COLUMN col_a TO col_b;`,
		"m/0003_add_a.sql":  `ALTER TABLE x ADD COLUMN col_a INT NOT NULL DEFAULT 0;`,
	})
	body, warns := mustFold(t, fsys, "m")
	if len(warns) > 0 {
		t.Errorf("unexpected warns: %s", warns)
	}
	mustContain(t, body, "col_b  TEXT", "col_a  INT NOT NULL DEFAULT 0")
}

func TestFoldRename_DropThenReAddThenRename(t *testing.T) {
	// DROP A, ADD A back, RENAME A→B. Sequential operations; the
	// final state has column B (the rename of the freshly re-added A).
	fsys := makeMigrationFS(map[string]string{
		"m/0001_init.sql":   `CREATE TABLE x (id INT, col_a TEXT);`,
		"m/0002_drop.sql":   `ALTER TABLE x DROP COLUMN col_a;`,
		"m/0003_re_add.sql": `ALTER TABLE x ADD COLUMN col_a VARCHAR(64);`,
		"m/0004_rename.sql": `ALTER TABLE x RENAME COLUMN col_a TO col_b;`,
	})
	body, warns := mustFold(t, fsys, "m")
	if len(warns) > 0 {
		t.Errorf("unexpected warns: %s", warns)
	}
	mustContain(t, body, "col_b  VARCHAR(64)")
	mustNotContain(t, body, "col_a")
}

func TestFoldRename_CrossTableFK_SourceSideRewritten_TargetUntouched(t *testing.T) {
	// orders.user_id → orders.account_id; the FK constraint's SOURCE-
	// side column list ("(user_id)") is rewritten to "(account_id)".
	// The TARGET-side column reference ("users(id)") is the OTHER
	// table's column and is left verbatim — per-table rename map is
	// in-scope only for this table. ADR-022 documents this limitation.
	fsys := makeMigrationFS(map[string]string{
		"m/0001_users.sql": `
CREATE TABLE users (
    id BIGINT PRIMARY KEY,
    email VARCHAR(255) NOT NULL UNIQUE
);`,
		"m/0002_orders.sql": `
CREATE TABLE orders (
    id BIGINT PRIMARY KEY,
    user_id BIGINT NOT NULL,
    CONSTRAINT fk_orders_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);`,
		"m/0003_rename.sql": `ALTER TABLE orders RENAME COLUMN user_id TO account_id;`,
	})
	body, warns := mustFold(t, fsys, "m")
	if len(warns) > 0 {
		t.Errorf("unexpected warns: %s", warns)
	}
	// The source-side column reference ("(user_id)") was rewritten
	// inside the FK's first-parens group. The target side
	// ("REFERENCES users(id)") is left verbatim.
	mustContain(t, body, "FOREIGN KEY (account_id) REFERENCES users(id)")
	mustNotContain(t, body, "(user_id)")
	// Column itself renamed in the columns block too.
	mustContain(t, body, "account_id  BIGINT NOT NULL")
}

func TestFoldRename_InMultiColumnConstraint(t *testing.T) {
	// RENAME email → email_address; constraint UNIQUE (email, tenant_id)
	// must become UNIQUE (email_address, tenant_id). Word-boundary
	// rewriting inside the first-parens group of the constraint.
	fsys := makeMigrationFS(map[string]string{
		"m/0001_init.sql": `
CREATE TABLE x (
    id BIGINT PRIMARY KEY,
    email VARCHAR(255) NOT NULL,
    tenant_id INT NOT NULL,
    CONSTRAINT x_email_tenant_uniq UNIQUE (email, tenant_id)
);`,
		"m/0002_rename.sql": `ALTER TABLE x RENAME COLUMN email TO email_address;`,
	})
	body, warns := mustFold(t, fsys, "m")
	if len(warns) > 0 {
		t.Errorf("unexpected warns: %s", warns)
	}
	mustContain(t, body, "UNIQUE (email_address, tenant_id)", "email_address  VARCHAR(255) NOT NULL")
	// The bare word "email" should no longer appear as a standalone
	// identifier — but identifiers containing "email" as a substring
	// (like a hypothetical "email_address_index" or constraint name)
	// would remain via the word-boundary anchor. Confirm the standalone
	// "email" reference is gone.
	if strings.Contains(string(body), "(email,") || strings.Contains(string(body), "(email)") || strings.Contains(string(body), " email ") {
		t.Errorf("standalone 'email' identifier should have been renamed:\n%s", body)
	}
}

func TestFoldRename_WordBoundary_DoesNotMatchSubstrings(t *testing.T) {
	// Regression guard for the word-boundary regex: a rename of
	// "email" must not touch "email_verified" or "current_email".
	// If \b anchors fail (e.g. switched to a substring replace), this
	// test catches the regression.
	fsys := makeMigrationFS(map[string]string{
		"m/0001_init.sql": `
CREATE TABLE x (
    id BIGINT PRIMARY KEY,
    email VARCHAR(255) NOT NULL,
    email_verified BOOLEAN NOT NULL DEFAULT FALSE,
    current_email VARCHAR(255),
    CONSTRAINT email_uniq UNIQUE (email)
);`,
		"m/0002_rename.sql": `ALTER TABLE x RENAME COLUMN email TO email_address;`,
	})
	body, warns := mustFold(t, fsys, "m")
	if len(warns) > 0 {
		t.Errorf("unexpected warns: %s", warns)
	}
	// The boundary-anchored rewrite hits only the standalone "email".
	mustContain(t, body,
		"email_address  VARCHAR(255) NOT NULL",
		"email_verified  BOOLEAN NOT NULL DEFAULT FALSE",
		"current_email  VARCHAR(255)",
		"UNIQUE (email_address)",
	)
}

func TestFoldRename_Constraint_Simple(t *testing.T) {
	// RENAME CONSTRAINT old_name TO new_name. Named constraints only;
	// the constraint string's leading "CONSTRAINT <name>" gets the
	// name rewritten in place.
	fsys := makeMigrationFS(map[string]string{
		"m/0001_init.sql": `
CREATE TABLE x (
    id BIGINT PRIMARY KEY,
    email VARCHAR(255) NOT NULL,
    CONSTRAINT users_email_unique UNIQUE (email)
);`,
		"m/0002_rename.sql": `ALTER TABLE x RENAME CONSTRAINT users_email_unique TO users_email_uniq;`,
	})
	body, warns := mustFold(t, fsys, "m")
	if len(warns) > 0 {
		t.Errorf("unexpected warns: %s", warns)
	}
	mustContain(t, body, "CONSTRAINT users_email_uniq UNIQUE (email)")
	mustNotContain(t, body, "users_email_unique")
}

func TestFoldRename_Constraint_AnonymousFallsBackToBothChunks(t *testing.T) {
	// Anonymous constraint ("PRIMARY KEY (id)" with no leading
	// CONSTRAINT <name>) has no name to match; RENAME CONSTRAINT fails
	// and the per-file ALTER chunk is preserved as the BOTH-chunks
	// fallback (the agent sees the action in the raw text).
	fsys := makeMigrationFS(map[string]string{
		"m/0001_init.sql":   `CREATE TABLE x (id INT PRIMARY KEY, email TEXT);`,
		"m/0002_rename.sql": `ALTER TABLE x RENAME CONSTRAINT x_pkey TO x_primary_key;`,
	})
	body, warns := mustFold(t, fsys, "m")
	if !strings.Contains(string(warns), "RENAME CONSTRAINT") || !strings.Contains(string(warns), "not found") {
		t.Errorf("expected 'RENAME CONSTRAINT ... not found' warn; got:\n%s", warns)
	}
	// Both chunks present: folded table + the per-file ALTER.
	mustContain(t, body, "TABLE x", "ALTER TABLE x", "RENAME CONSTRAINT x_pkey TO x_primary_key")
}

func TestFoldRename_MissingSourceColumn_FallsBackToBothChunks(t *testing.T) {
	// RENAME COLUMN where the source column doesn't exist (operator
	// typo) → warn + per-file ALTER chunk preserved. Folded table
	// chunk still emitted with whatever did parse cleanly.
	fsys := makeMigrationFS(map[string]string{
		"m/0001_init.sql":   `CREATE TABLE x (id INT, email TEXT);`,
		"m/0002_rename.sql": `ALTER TABLE x RENAME COLUMN nonexistent TO replacement;`,
	})
	body, warns := mustFold(t, fsys, "m")
	if !strings.Contains(string(warns), "RENAME COLUMN") || !strings.Contains(string(warns), "not found") {
		t.Errorf("expected 'RENAME COLUMN ... not found' warn; got:\n%s", warns)
	}
	mustContain(t, body, "TABLE x", "id  INT", "email  TEXT")
	mustContain(t, body, "ALTER TABLE x", "RENAME COLUMN nonexistent TO replacement")
}

func TestFoldRename_Idempotent(t *testing.T) {
	// Regression guard against accidental statefulness in the rename
	// machinery: running FoldMigrations twice on the same fixture
	// yields byte-identical chunks. If applyColumnRename / renameIn-
	// FirstParens accidentally leaked global state, the second run
	// would diverge.
	fsys := makeMigrationFS(map[string]string{
		"m/0001_init.sql": `
CREATE TABLE x (
    id BIGINT PRIMARY KEY,
    email VARCHAR(255) NOT NULL,
    CONSTRAINT x_email_uniq UNIQUE (email)
);`,
		"m/0002_rename_col.sql": `ALTER TABLE x RENAME COLUMN email TO email_address;`,
		"m/0003_rename_con.sql": `ALTER TABLE x RENAME CONSTRAINT x_email_uniq TO x_email_address_uniq;`,
	})
	firstBody, _ := mustFold(t, fsys, "m")
	secondBody, _ := mustFold(t, fsys, "m")
	if !bytes.Equal(firstBody, secondBody) {
		t.Errorf("non-deterministic fold output across two runs:\n--first--\n%s\n--second--\n%s", firstBody, secondBody)
	}
}

func TestFoldRename_SQLiteSyntax(t *testing.T) {
	// SQLite has supported "RENAME COLUMN old TO new" since 3.25.0
	// (2018). Same syntax as Postgres / MySQL / MariaDB. The fold
	// path doesn't engine-dispatch — it parses the canonical form.
	fsys := makeMigrationFS(map[string]string{
		"m/0001_init.sql":   `CREATE TABLE x (id INTEGER PRIMARY KEY, old_col TEXT);`,
		"m/0002_rename.sql": `ALTER TABLE x RENAME COLUMN old_col TO new_col;`,
	})
	body, warns := mustFold(t, fsys, "m")
	if len(warns) > 0 {
		t.Errorf("unexpected warns: %s", warns)
	}
	mustContain(t, body, "new_col  TEXT")
}

func TestFoldRename_MySQLChangeSyntax_FallsBackToBothChunks(t *testing.T) {
	// MySQL's CHANGE syntax renames + retypes in one statement. v0.8.1
	// Part C doesn't decode CHANGE; falls through to BOTH-chunks (the
	// per-file ALTER chunk preserves the action; the folded chunk
	// retains the pre-CHANGE column name + type). ADR-022 records this
	// as a known deferral.
	fsys := makeMigrationFS(map[string]string{
		"m/0001_init.sql":   `CREATE TABLE x (id INT, old_name VARCHAR(64) NOT NULL);`,
		"m/0002_change.sql": `ALTER TABLE x CHANGE old_name new_name VARCHAR(128) NOT NULL;`,
	})
	body, warns := mustFold(t, fsys, "m")
	if !strings.Contains(string(warns), "unsupported") {
		t.Errorf("expected 'unsupported ALTER action' warn for CHANGE; got:\n%s", warns)
	}
	// Folded chunk retains pre-CHANGE state; raw migration file's
	// CHANGE action is preserved via the per-file ALTER chunk.
	mustContain(t, body, "old_name  VARCHAR(64) NOT NULL")
	mustContain(t, body, "CHANGE old_name new_name VARCHAR(128) NOT NULL")
}
