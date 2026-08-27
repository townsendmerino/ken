package mcpdb

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"
)

// TestConfig_toDBOptions locks the Config → db.Options field mapping. It's a
// pure mapping function with a couple of documented defaults/overrides that
// were only exercised indirectly (via Setup's DSN path); a silent field-drop
// or a broken allow-list/deny-list rule would otherwise pass unnoticed.
func TestConfig_toDBOptions(t *testing.T) {
	var buf bytes.Buffer
	cfg := Config{
		DSN:             "postgres://x",
		SampleRows:      5,
		ReindexInterval: 30 * time.Second,
		StartupTimeout:  10 * time.Second,
		IncludeSchemas:  []string{"app"},
		ExcludeSchemas:  []string{"legacy"}, // both lists set → allow-list wins
		LogWriter:       &buf,
	}
	opts := cfg.toDBOptions()

	if opts.DSN != "postgres://x" {
		t.Errorf("DSN = %q, want postgres://x", opts.DSN)
	}
	if opts.SampleRows != 5 {
		t.Errorf("SampleRows = %d, want 5", opts.SampleRows)
	}
	if opts.ReindexInterval != 30*time.Second {
		t.Errorf("ReindexInterval = %v, want 30s", opts.ReindexInterval)
	}
	if opts.StartupTimeout != 10*time.Second {
		t.Errorf("StartupTimeout = %v, want 10s", opts.StartupTimeout)
	}
	if len(opts.IncludeSchemas) != 1 || opts.IncludeSchemas[0] != "app" {
		t.Errorf("IncludeSchemas = %v, want [app]", opts.IncludeSchemas)
	}
	if opts.LogWriter != &buf {
		t.Error("LogWriter should pass through unchanged")
	}

	// Both schema lists set → allow-list wins: ExcludeSchemas is dropped and a
	// warn is written to the LogWriter (matching cmd/ken-mcp).
	if opts.ExcludeSchemas != nil {
		t.Errorf("ExcludeSchemas should be nil when both lists are set (allow-list wins); got %v", opts.ExcludeSchemas)
	}
	if !strings.Contains(buf.String(), "allow-list wins") {
		t.Errorf("expected an allow-list-wins warning on LogWriter; got %q", buf.String())
	}
}

func TestConfig_toDBOptions_Defaults(t *testing.T) {
	// nil LogWriter defaults to os.Stderr (never os.Stdout — the JSON-RPC channel).
	if opts := (Config{DSN: "sqlite://x"}).toDBOptions(); opts.LogWriter != os.Stderr {
		t.Errorf("nil LogWriter should default to os.Stderr; got %v", opts.LogWriter)
	}

	// Only a deny-list (no allow-list) → it passes through untouched, no override.
	var buf bytes.Buffer
	opts := (Config{ExcludeSchemas: []string{"tmp"}, LogWriter: &buf}).toDBOptions()
	if len(opts.ExcludeSchemas) != 1 || opts.ExcludeSchemas[0] != "tmp" {
		t.Errorf("ExcludeSchemas should pass through when IncludeSchemas is empty; got %v", opts.ExcludeSchemas)
	}
	if len(opts.IncludeSchemas) != 0 {
		t.Errorf("IncludeSchemas should be empty; got %v", opts.IncludeSchemas)
	}
	if buf.Len() != 0 {
		t.Errorf("no warning expected when only the deny-list is set; got %q", buf.String())
	}
}
