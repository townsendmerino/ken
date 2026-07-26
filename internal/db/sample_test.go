package db

import (
	"bytes"
	"strings"
	"testing"
)

// TestWithSampleRecover confirms a panic in fn is caught + logged (audit
// §26) rather than propagating (which would crash ken-mcp), and that the
// happy path runs fn normally.
func TestWithSampleRecover(t *testing.T) {
	buf := &bytes.Buffer{}
	opts := Options{LogWriter: buf}

	ran := false
	withSampleRecover(opts, "ok_table", func() { ran = true })
	if !ran {
		t.Error("withSampleRecover should run fn on the happy path")
	}

	// A panic must be swallowed and logged, not propagated.
	withSampleRecover(opts, "bad_table", func() { panic("boom") })
	if !strings.Contains(buf.String(), "bad_table") || !strings.Contains(buf.String(), "boom") {
		t.Errorf("panic should be logged with table label + value; got %q", buf.String())
	}
}
