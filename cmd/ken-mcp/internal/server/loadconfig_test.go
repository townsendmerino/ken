package server

import (
	"strings"
	"testing"
	"time"
)

// TestLoadConfig_LateVariables covers the settings that used to be read
// inside Run: they are in Config now, and an invalid value is reported by
// LoadConfig, at startup.
func TestLoadConfig_LateVariables(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KEN_MCP_MODE", "bm25")
	t.Setenv("KEN_MCP_AUTO_FETCH", "0")
	t.Setenv("KEN_DB_DSN", "sqlite:///tmp/x.db")
	t.Setenv("KEN_DB_SAMPLE_ROWS", "-3")
	t.Setenv("KEN_DB_REINDEX_INTERVAL", "90s")
	t.Setenv("KEN_DB_STARTUP_TIMEOUT", "soon")
	t.Setenv("KEN_DB_SCHEMAS", "public, app")
	t.Setenv("KEN_DB_EXCLUDE_SCHEMAS", "audit")
	t.Setenv("KEN_DB_LISTEN", "1")
	t.Setenv("KEN_MCP_RERANK_TELEMETRY", "1")
	t.Setenv("KEN_NO_USAGE_STATS", "perhaps")
	t.Setenv("KEN_USAGE_STATS_PATH", " /tmp/stats.jsonl ")
	t.Setenv("KEN_MCP_SHUTDOWN_GRACE", "2s")
	t.Setenv("KEN_ENRICH", "off")
	t.Setenv("KEN_MAX_FILES", "7")

	logger, buf := newCapturedLogger()
	cfg := LoadConfig(logger)

	db := cfg.DB
	if db.DSN != "sqlite:///tmp/x.db" || db.SampleRows != 0 || db.ReindexInterval != 90*time.Second ||
		db.StartupTimeout != 30*time.Second || !db.EnableListen {
		t.Errorf("DB = %+v", db)
	}
	if got := strings.Join(db.IncludeSchemas, ","); got != "public,app" || db.ExcludeSchemas != nil {
		t.Errorf("schemas: include=%q exclude=%v, want the allow-list to win", got, db.ExcludeSchemas)
	}
	if !cfg.RerankTelemetryInResponse || cfg.UsageStatsDisabled || cfg.UsageStatsPath != "/tmp/stats.jsonl" || cfg.ShutdownGrace != 2*time.Second {
		t.Errorf("telemetry=%v usageDisabled=%v usagePath=%q grace=%v", cfg.RerankTelemetryInResponse, cfg.UsageStatsDisabled, cfg.UsageStatsPath, cfg.ShutdownGrace)
	}
	if !cfg.Index.DisableEnrichment || cfg.Index.MaxFiles != 7 {
		t.Errorf("Index = %+v", cfg.Index)
	}
	log := buf.String()
	for _, want := range []string{
		"KEN_DB_SAMPLE_ROWS=-3: must be non-negative",
		`invalid KEN_DB_STARTUP_TIMEOUT="soon"`,
		"KEN_DB_SCHEMAS and KEN_DB_EXCLUDE_SCHEMAS both set",
		`invalid KEN_NO_USAGE_STATS="perhaps"`,
	} {
		if !strings.Contains(log, want) {
			t.Errorf("LoadConfig did not warn %q; log:\n%s", want, log)
		}
	}
}

// With no DSN the rest of the KEN_DB_* family is not read, so it is not
// validated either: a stray KEN_DB_SAMPLE_ROWS says nothing.
func TestLoadConfig_NoDSNSkipsDBValidation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KEN_MCP_MODE", "bm25")
	t.Setenv("KEN_MCP_AUTO_FETCH", "0")
	t.Setenv("KEN_DB_DSN", "")
	t.Setenv("KEN_DB_SAMPLE_ROWS", "lots")
	logger, buf := newCapturedLogger()
	cfg := LoadConfig(logger)
	if cfg.DB.DSN != "" || cfg.DB.SampleRows != 0 {
		t.Errorf("DB = %+v, want zero", cfg.DB)
	}
	if strings.Contains(buf.String(), "KEN_DB_SAMPLE_ROWS") {
		t.Errorf("KEN_DB_SAMPLE_ROWS was validated without a DSN:\n%s", buf.String())
	}
}

// ken-mcp by default remembers up to 32 MB of Go typed-tier answers, keeps the type-checked programs loaded, and
// answers in process with go/types; each is settable, and 0 turns the cache or the resident programs off (ADR-060,
// ADR-061).
func TestLoadConfig_GoTypedDefaults(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KEN_MCP_MODE", "bm25")
	t.Setenv("KEN_MCP_AUTO_FETCH", "0")
	logger, _ := newCapturedLogger()
	cfg := LoadConfig(logger)
	if cfg.GoTyped.CacheBytes != 32<<20 || cfg.GoTyped.MaxResident != 0 || cfg.GoTyped.IdleTimeout != 5*time.Minute ||
		cfg.GoTyped.Engine != "types" || cfg.GoTyped.Parallel != 1 {
		t.Errorf("defaults = %+v, want a 32 MB cache, programs kept and sized automatically (MaxResident 0), 5m idle, the types engine, one configuration at a time", cfg.GoTyped)
	}
	t.Setenv("KEN_MCP_GO_RESIDENT", "0") // 0 opts out: nothing kept loaded
	if cfg = LoadConfig(logger); cfg.GoTyped.MaxResident >= 0 {
		t.Errorf("KEN_MCP_GO_RESIDENT=0 gave MaxResident %d, want a negative value (keep nothing loaded)", cfg.GoTyped.MaxResident)
	}
	t.Setenv("KEN_MCP_GO_CACHE_MB", "0")
	t.Setenv("KEN_MCP_GO_RESIDENT", "3")
	t.Setenv("KEN_MCP_GO_ENGINE", "lsp")
	t.Setenv("KEN_MCP_GO_PARALLEL", "2")
	cfg = LoadConfig(logger)
	if cfg.GoTyped.CacheBytes != 0 || cfg.GoTyped.MaxResident != 3 || cfg.GoTyped.Engine != "lsp" || cfg.GoTyped.Parallel != 2 {
		t.Errorf("with the four variables set: %+v", cfg.GoTyped)
	}
}
