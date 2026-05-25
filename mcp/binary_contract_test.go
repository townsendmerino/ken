package mcp

// Binary-size contract test for v0.6.0 → v0.8.0 (ADR-016 + ADR-020 Part 3).
//
// The mcp package MUST NOT directly or transitively import any DB driver
// or internal/db. SDK authors building docs-only embedded-corpus
// binaries (the v0.6.0 use case) link against `mcp` and `mcp.Run`;
// adding a DB driver to that graph silently inflates their binaries by
// ~10MB+ (pgx + modernc.org/sqlite + go-sql-driver/mysql + transitives,
// none of which can be dead-code-eliminated because the SQL drivers
// register via package init).
//
// v0.8.0 Part 3 introduces the opt-in mcp/db package — SDK authors who
// want DB support import mcp/db, and pay the binary-size cost as a
// deliberate trade. SDK authors who DON'T import mcp/db must continue
// to get the v0.6.0 dep-tree shape.
//
// This test pins that contract by walking the mcp package's transitive
// import set via go list -deps and asserting none of the DB-driver
// import paths appear. A future commit that accidentally adds a DB
// import to mcp/ fails this test before merging.

import (
	"os/exec"
	"strings"
	"testing"
)

// dbForbiddenImports is the explicit deny list. Anything under these
// paths SHOULD NOT appear in mcp's transitive import set. Hits here
// mean a DB driver leaked into the small-binary path.
//
// Substring match (not exact) so we catch nested subpackages too:
//   - "github.com/jackc/pgx/" covers v5/, v5/pgconn, etc.
//   - "modernc.org/sqlite" covers the driver + libc + memory subpkgs
//   - "github.com/go-sql-driver/mysql" covers the driver
//   - "github.com/townsendmerino/ken/internal/db" — the internal Tier 2
//     package; mcp must not depend on it (mcp/db is the public surface
//     and it depends on internal/db, but mcp itself must not).
var dbForbiddenImports = []string{
	"github.com/jackc/pgx",
	"modernc.org/sqlite",
	"github.com/go-sql-driver/mysql",
	"github.com/townsendmerino/ken/internal/db",
}

// TestBinary_MCPPackageStaysDBFree shells out to `go list -deps` and
// asserts the mcp package's transitive import set is free of all DB
// drivers + internal/db. This is the load-bearing v0.6.0-contract
// guard.
//
// Skipped when the go tool isn't on PATH (rare; this test runs in CI
// and on developer machines that already have Go installed).
func TestBinary_MCPPackageStaysDBFree(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping go-list-deps subprocess in -short mode")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go tool not on PATH; can't verify mcp's dep tree")
	}

	cmd := exec.Command("go", "list", "-deps", "github.com/townsendmerino/ken/mcp")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps github.com/townsendmerino/ken/mcp: %v\n%s", err, out)
	}

	deps := strings.Split(strings.TrimSpace(string(out)), "\n")
	var violations []string
	for _, dep := range deps {
		dep = strings.TrimSpace(dep)
		for _, forbidden := range dbForbiddenImports {
			if strings.Contains(dep, forbidden) {
				violations = append(violations, dep+" (matches forbidden "+forbidden+")")
			}
		}
	}
	if len(violations) > 0 {
		t.Errorf("mcp package picked up DB-driver deps (v0.6.0 binary-size contract violated by v0.8.0 Part 3 / ADR-020):\n  %s",
			strings.Join(violations, "\n  "))
	}
}

// TestBinary_MCPDBPackageBringsExpectedDeps is the inverse sanity
// check: mcp/db (the opt-in package) SHOULD transitively pull in the
// DB drivers + internal/db. If a future refactor accidentally
// detaches mcp/db from internal/db, this test catches it.
func TestBinary_MCPDBPackageBringsExpectedDeps(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping go-list-deps subprocess in -short mode")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go tool not on PATH; can't verify mcp/db's dep tree")
	}

	cmd := exec.Command("go", "list", "-deps", "github.com/townsendmerino/ken/mcp/db")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps github.com/townsendmerino/ken/mcp/db: %v\n%s", err, out)
	}

	deps := strings.Split(strings.TrimSpace(string(out)), "\n")
	depSet := map[string]bool{}
	for _, d := range deps {
		depSet[strings.TrimSpace(d)] = true
	}

	// Expected: internal/db (always) + at least the SQL driver tree
	// (pgx, modernc.org/sqlite, go-sql-driver/mysql all link via
	// internal/db's engine dispatch). We assert presence of the keys
	// rather than exact set equality — a future engine bump (e.g.
	// modernc.org/sqlite v2) shouldn't fail this test.
	requireSubstring := []string{
		"github.com/townsendmerino/ken/internal/db",
		"github.com/jackc/pgx",
		"modernc.org/sqlite",
		"github.com/go-sql-driver/mysql",
	}
	for _, want := range requireSubstring {
		found := false
		for dep := range depSet {
			if strings.Contains(dep, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("mcp/db dep tree missing expected substring %q (got %d deps total)", want, len(depSet))
		}
	}
}
