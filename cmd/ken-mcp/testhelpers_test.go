package main

import (
	"bytes"
	"io"
	"os"

	kenmcp "github.com/townsendmerino/ken/mcp"
)

// Shared test-logger constructors for package main's tests. Three variants,
// one home (previously scattered across startup_test.go, loadorbuild_test.go,
// and main_test.go, each with minor differences). Pick by what the test needs:
//
//   - testLogger:        discards output (io.Discard) at warn level — for tests
//     that exercise a code path but don't inspect logs.
//   - quietLogger:       stderr at error level — silent unless something really
//     goes wrong, so failures still surface a message.
//   - newCapturedLogger: captures to a returned buffer at debug level — for
//     tests that assert on the emitted warn/log text.
func testLogger() *kenmcp.Logger { return kenmcp.NewLogger(io.Discard, kenmcp.LogWarn) }

func quietLogger() *kenmcp.Logger { return kenmcp.NewLogger(os.Stderr, kenmcp.LogError) }

func newCapturedLogger() (*kenmcp.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return kenmcp.NewLogger(buf, kenmcp.LogDebug), buf
}
