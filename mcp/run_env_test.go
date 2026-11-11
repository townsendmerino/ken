package mcp

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/townsendmerino/ken/internal/status"
)

// mcp.Run honors KEN_ENRICH and KEN_ANN through the same options the ken
// CLI's one-shot builds use (ADR-054). Before it, the embedded-corpus build
// ignored both.

func TestRun_KenEnrichEnv(t *testing.T) {
	for _, tc := range []struct {
		env          string
		wantEnriched bool
	}{{"", true}, {"on", true}, {"off", false}, {"0", false}} {
		t.Setenv("KEN_ENRICH", tc.env)
		ix, _, err := buildEmbeddedIndex(embeddedCorpus(), Options{Mode: "bm25", ChunkerName: "regex", LogWriter: &bytes.Buffer{}})
		if err != nil {
			t.Fatal(err)
		}
		if got := ix.EnrichedChunks() > 0; got != tc.wantEnriched {
			t.Errorf("KEN_ENRICH=%q: index enriched = %v (%d chunks labeled), want %v", tc.env, got, ix.EnrichedChunks(), tc.wantEnriched)
		}
		// status reports the index, not the variable.
		info := liveIndexInfo("(embedded corpus)", ix)
		if (info.EnrichedChunks > 0) != tc.wantEnriched || info.DenseRetriever != "none" {
			t.Errorf("KEN_ENRICH=%q: status reports EnrichedChunks=%d DenseRetriever=%q", tc.env, info.EnrichedChunks, info.DenseRetriever)
		}
	}
}

func TestRun_KenAnnEnv(t *testing.T) {
	modelDir := filepath.Join("..", "testdata", "model")
	if _, err := os.Stat(filepath.Join(modelDir, "model.safetensors")); err != nil {
		t.Skip("testdata/model/ not present; see testdata/README.md")
	}
	for env, want := range map[string]string{"": "flat", "flat-i8": "flat-i8", "flat-binary-i8": "flat-binary-i8", "hnsw": "flat"} {
		t.Setenv("KEN_ANN", env)
		ix, _, err := buildEmbeddedIndex(embeddedCorpus(), Options{Mode: "hybrid", ChunkerName: "regex", ModelDir: modelDir, LogWriter: &bytes.Buffer{}})
		if err != nil {
			t.Fatal(err)
		}
		if got := liveIndexInfo("(embedded corpus)", ix).DenseRetriever; got != want {
			t.Errorf("KEN_ANN=%q: mcp.Run index serves %q, want %q", env, got, want)
		}
	}
}

// TestStatus_ReportsLoadedIndexNotEnv is the mismatch the fix closes: an
// index that was built enriched keeps its labels whatever KEN_ENRICH says
// later (a pre-built or snapshot-loaded index), and status must say so.
func TestStatus_ReportsLoadedIndexNotEnv(t *testing.T) {
	t.Setenv("KEN_ENRICH", "")
	ix, _, err := buildEmbeddedIndex(embeddedCorpus(), Options{Mode: "bm25", ChunkerName: "regex", LogWriter: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("KEN_ENRICH", "off") // changed after the index was built
	opts := withStatusEnv(status.BuildOptions{LiveIndex: liveIndexInfo("repo", ix)})
	text := status.RenderText(status.Build(opts), false)
	if !strings.Contains(text, "disabled — KEN_ENRICH=off") {
		t.Errorf("status should still show the configured setting; got:\n%s", text)
	}
	if !strings.Contains(text, "chunks labeled") || strings.Contains(text, "enrichment:  none") {
		t.Errorf("status must report the loaded index as enriched; got:\n%s", text)
	}
}

// TestStatus_ShowsParseBudgetCounts: the MCP status report carries this
// process's parse-budget counts (skipped and reached), even when both are 0.
func TestStatus_ShowsParseBudgetCounts(t *testing.T) {
	st := status.Build(withStatusEnv(status.BuildOptions{}))
	if st.ParseBudget == nil {
		t.Fatal("MCP status has no ParseBudget block")
	}
	text := status.RenderText(st, false)
	for _, want := range []string{"Parse budget", "skipped:", "reached:"} {
		if !strings.Contains(text, want) {
			t.Errorf("status text lacks %q:\n%s", want, text)
		}
	}
	// The CLI surface passes no counts and shows no block.
	if cli := status.RenderText(status.Build(status.BuildOptions{}), false); strings.Contains(cli, "Parse budget") {
		t.Errorf("CLI status shows a Parse budget block:\n%s", cli)
	}
}
