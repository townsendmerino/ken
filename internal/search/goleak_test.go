package search

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain catches goroutine leaks across the whole internal/search
// test suite. Step 4 of the post-v0.8.3 hardening sequence.
//
// The WatchedIndex code path (watch.go) is the load-bearing concern:
// each Watched* test spawns an fsnotify watcher + a debounce
// goroutine + a flush goroutine, all of which MUST shut down on
// wix.Close(). A test that forgets to call Close (or whose Close
// race-conditions against the debouncer) would leak — goleak.
// VerifyTestMain catches that class of regression here.
//
// If a test legitimately needs to spawn a non-test-owned goroutine
// (e.g. an OS-level inotify thread that the Go runtime treats as a
// goroutine), add an IgnoreTopFunction / IgnoreAnyFunction
// allowance here rather than disabling goleak globally.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
