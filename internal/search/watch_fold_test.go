package search

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestWatchedIndex_RefoldsOnAlterAdded — when a new ALTER lands in a
// migration directory mid-session, the folded chunk for the affected
// table reflects the new column on the next debounce flush.
func TestWatchedIndex_RefoldsOnAlterAdded(t *testing.T) {
	root := makeTempRepo(t, map[string]string{
		"m/0001_init.sql": `CREATE TABLE users (
    id    BIGSERIAL PRIMARY KEY,
    email VARCHAR(255) NOT NULL
);`,
		"m/0002_seed.sql": `-- placeholder so dir qualifies as migrations (>=2 numbered files)
SELECT 1;`,
	})
	wi := withShortDebounce(t, root, true)

	// Confirm the build-time folded chunk has only id + email.
	initial := findFoldedTable(t, wi, "users")
	if strings.Contains(initial, "status") {
		t.Fatalf("initial folded chunk should NOT contain status:\n%s", initial)
	}

	swaps := make(chan struct{}, 4)
	wi.SetOnSwap(swaps)
	drainSwaps(swaps)

	// Add a new migration that adds a status column.
	newMigration := filepath.Join(root, "m", "0003_add_status.sql")
	if err := os.WriteFile(newMigration, []byte(
		"ALTER TABLE users ADD COLUMN status VARCHAR(16) NOT NULL DEFAULT 'active';\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	if !waitForSwap(t, swaps, 5*time.Second) {
		t.Fatal("no swap after writing new migration")
	}
	// Give the goroutine a beat after the swap to settle.
	time.Sleep(20 * time.Millisecond)

	after := findFoldedTable(t, wi, "users")
	if !strings.Contains(after, "status  VARCHAR(16) NOT NULL DEFAULT 'active'") {
		t.Errorf("post-flush folded chunk missing status column:\n%s", after)
	}
}

// findFoldedTable scans the current snapshot for the folded chunk
// describing the given table; fails the test if none is found.
func findFoldedTable(t *testing.T, wi *WatchedIndex, tableName string) string {
	t.Helper()
	ix := wi.Load()
	for _, c := range ix.Chunks() {
		if c.Tombstoned {
			continue
		}
		if strings.Contains(c.Text, "-- folded from migrations") &&
			strings.Contains(c.Text, "TABLE "+tableName) {
			return c.Text
		}
	}
	t.Fatalf("no folded chunk for table %q in snapshot of %d chunks", tableName, ix.Len())
	return ""
}
