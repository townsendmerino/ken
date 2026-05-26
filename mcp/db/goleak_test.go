package mcpdb

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain catches goroutine leaks across the mcp/db test suite.
// M2 of the second post-v0.8.3 bug scan: mcp/db wires the live
// internal/db.Refresher + LISTEN/NOTIFY listener for SDK authors
// who opt into Tier-2 DB chunk integration via mcp/db.Setup. A
// `cleanup func()` returned by Refresher.Start that's not invoked
// on test shutdown would leak the interval ticker goroutine + the
// listener's reconnect-loop — exactly what goleak is for.
//
// Coverage note: most tests in this package are gated on
// `dbintegration` build tag (live service-container DSNs). The
// non-tagged unit tests don't spawn long-lived goroutines today,
// but the TestMain guard catches regressions if a future test
// forgets to call its returned cleanup.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
