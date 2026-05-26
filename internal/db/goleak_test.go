package db

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain catches goroutine leaks across the internal/db test
// suite. Step 4 of the post-v0.8.3 hardening sequence.
//
// The Refresher (db.go) + Listener (listen.go) code paths are the
// load-bearing concern: each Refresher.Start spawns an interval-
// ticker goroutine that must stop on cleanup-func call; the
// LISTEN/NOTIFY Listener spawns a reconnect-loop goroutine on a
// dedicated pgx.Conn. A test that forgets to call the cleanup
// function (or whose cleanup race-conditions against the ticker /
// reconnect loop) would leak — goleak.VerifyTestMain catches that
// class of regression here.
//
// Integration tests (dbintegration build tag) compile into the
// same package, so this TestMain also guards the live-DB paths
// when CI runs with -tags=dbintegration. The non-tagged tests run
// it on every checkout's `go test ./internal/db/`.
//
// If a test legitimately needs to spawn a non-test-owned goroutine
// (e.g. an OS-level pgx-pool thread that the Go runtime treats as a
// goroutine), add an IgnoreTopFunction / IgnoreAnyFunction
// allowance here rather than disabling goleak globally.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
