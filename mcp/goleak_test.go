package mcp

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain catches goroutine leaks across the mcp test suite. M2 of
// the second post-v0.8.3 bug scan: the mcp/ package owns the long-
// lived-goroutine surfaces (Cache builders spawn *WatchedIndex
// watchers; mcp.Run starts the SDK server + optional
// DBIntegration.Start goroutine), so a leak introduced in mcp's
// lifecycle would otherwise slip past the existing `goleak` coverage
// in internal/search + internal/db.
//
// Allowances:
//
//   - go-sql-driver/mysql's package-level logger writes to stderr,
//     not a goroutine — no allowance needed.
//   - pgx/v5 keeps a per-conn reader goroutine alive for as long as
//     the connection is open; mcp/db's integration tests close the
//     pool in defer but the reaper can race with goleak's check.
//     The integration tests are in mcp/db/ which has its own
//     goleak_test.go (sibling file); the mcp/ package itself does
//     NOT import pgx, so this TestMain doesn't need a pgx allowance.
//   - SDK server's CommandTransport-driven exec.Cmd goroutines: the
//     stdio transport spawns IO-copier goroutines that exit on
//     cmd.Wait, which the tests do (via sess.Close → ctx.Cancel →
//     server shutdown). No allowance needed.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
