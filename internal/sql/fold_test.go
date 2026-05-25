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
// Scenario F15: RENAME COLUMN is out of scope — preserve per-file chunk + warn.
// ============================================================================

func TestFold15_RenameColumnOutOfScope(t *testing.T) {
	fsys := makeMigrationFS(map[string]string{
		"m/0001_init.sql":   `CREATE TABLE x (id INT, old_name TEXT);`,
		"m/0002_rename.sql": `ALTER TABLE x RENAME COLUMN old_name TO new_name;`,
	})
	body, warns := mustFold(t, fsys, "m")
	if !strings.Contains(string(warns), "RENAME") {
		t.Errorf("expected RENAME warn; got:\n%s", warns)
	}
	// Folded chunk still emitted; RENAME was preserved as per-file chunk.
	mustContain(t, body, "TABLE x", "old_name  TEXT")
	mustContain(t, body, "ALTER TABLE x", "RENAME COLUMN old_name TO new_name")
}
