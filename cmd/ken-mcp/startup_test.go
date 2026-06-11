package main

import (
	"io"
	"path/filepath"
	"testing"

	"github.com/townsendmerino/ken/internal/search"
	kenmcp "github.com/townsendmerino/ken/mcp"
)

func testLogger() *kenmcp.Logger { return kenmcp.NewLogger(io.Discard, kenmcp.LogWarn) }

func TestResolveStartupMode(t *testing.T) {
	lg := testLogger()
	tests := []struct {
		name                                 string
		modeStr, modelDir                    string
		modelPresent, autoFetch              bool
		wantMode                             search.Mode
		wantModeStr, wantModelDir, wantFetch string
	}{
		{"bm25 needs no model", "bm25", "/m", false, true, search.ModeBM25, "bm25", "/m", ""},
		{"hybrid with model stays hybrid", "hybrid", "/m", true, true, search.ModeHybrid, "hybrid", "/m", ""},
		{"hybrid no model + autofetch → downgrade + dest", "hybrid", "/m", false, true, search.ModeBM25, "bm25", "", "/m"},
		{"hybrid no model + no autofetch → downgrade, no dest", "hybrid", "/m", false, false, search.ModeBM25, "bm25", "", ""},
		{"hybrid no model + autofetch but empty dir → no dest", "hybrid", "", false, true, search.ModeBM25, "bm25", "", ""},
		{"semantic no model + autofetch", "semantic", "/m", false, true, search.ModeBM25, "bm25", "", "/m"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sm := resolveStartupMode(tc.modeStr, tc.modelDir, tc.modelPresent, tc.autoFetch, lg)
			if sm.mode != tc.wantMode || sm.modeStr != tc.wantModeStr {
				t.Errorf("effective mode: got %v/%q want %v/%q", sm.mode, sm.modeStr, tc.wantMode, tc.wantModeStr)
			}
			if sm.modelDir != tc.wantModelDir {
				t.Errorf("modelDir: got %q want %q", sm.modelDir, tc.wantModelDir)
			}
			if sm.autoFetchDest != tc.wantFetch {
				t.Errorf("autoFetchDest: got %q want %q", sm.autoFetchDest, tc.wantFetch)
			}
			// want* always reflects the originally REQUESTED mode, even
			// after a downgrade — that's what the auto-fetch upgrades to.
			reqMode, _ := search.ParseMode(tc.modeStr)
			if sm.wantMode != reqMode || sm.wantStr != tc.modeStr {
				t.Errorf("want*: got %v/%q want %v/%q", sm.wantMode, sm.wantStr, reqMode, tc.modeStr)
			}
		})
	}
}

func TestSetupReranker_Off(t *testing.T) {
	t.Setenv("KEN_MCP_RERANK", "") // unset/off → default false
	lazy, loader, opts, enabled := setupReranker(testLogger())
	if enabled || lazy != nil || loader != nil || opts != nil {
		t.Fatalf("rerank off: got enabled=%v lazy=%v loader=%v opts=%v", enabled, lazy, loader, opts)
	}
}

func TestSetupReranker_EnabledButNoModel(t *testing.T) {
	t.Setenv("KEN_MCP_RERANK", "1")
	t.Setenv("KEN_MCP_RERANK_MODEL_DIR", "") // empty → unavailable
	lazy, loader, opts, enabled := setupReranker(testLogger())
	if !enabled {
		t.Fatal("want enabled=true (KEN_MCP_RERANK=1)")
	}
	if lazy != nil || loader != nil || opts != nil {
		t.Fatalf("model unavailable: want nil wiring, got lazy=%v loader=%v opts=%v", lazy, loader, opts)
	}
}

func TestRerankerLoader_Load_MissingModel(t *testing.T) {
	l := &rerankerLoader{modelDir: filepath.Join(t.TempDir(), "nope"), quant: "f32", logger: testLogger()}
	r, err := l.Load()
	if err == nil || r != nil {
		t.Fatalf("loading a missing model should error: got r=%v err=%v", r, err)
	}
}

func TestParseRerankAdaptive(t *testing.T) {
	cases := []struct {
		in string
		wt float64
		wn int
	}{
		{"", 0, 0},
		{"0.30:10", 0.30, 10},
		{"garbage", 0, 0},
		{"0.30", 0, 0},     // no minN
		{"0.30:abc", 0, 0}, // bad minN
		{"0:10", 0, 0},     // zero threshold is treated as disabled
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			t.Setenv("KEN_MCP_RERANK_ADAPTIVE", c.in)
			wt, wn := parseRerankAdaptive(testLogger())
			if wt != c.wt || wn != c.wn {
				t.Errorf("%q: got %v/%d want %v/%d", c.in, wt, wn, c.wt, c.wn)
			}
		})
	}
}

func TestResolveRerankCachePath(t *testing.T) {
	t.Run("explicit empty disables", func(t *testing.T) {
		t.Setenv("KEN_MCP_RERANK_CACHE", "")
		if got := resolveRerankCachePath("f32"); got != "" {
			t.Errorf("want disabled, got %q", got)
		}
	})
	t.Run("explicit path wins", func(t *testing.T) {
		t.Setenv("KEN_MCP_RERANK_CACHE", "/tmp/x.bin")
		if got := resolveRerankCachePath("int8"); got != "/tmp/x.bin" {
			t.Errorf("got %q want /tmp/x.bin", got)
		}
	})
}
