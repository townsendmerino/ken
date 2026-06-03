package status

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/townsendmerino/ken/internal/usage"
)

// TestBuild_CLI_NoLiveIndex confirms the zero-BuildOptions path
// (the CLI ken status invocation) populates machine-level fields
// and leaves live-server fields zero.
func TestBuild_CLI_NoLiveIndex(t *testing.T) {
	// Point everything at temp dirs that don't exist — Build should
	// gracefully report "not present" without erroring.
	emptyDir := t.TempDir()
	s := Build(BuildOptions{
		EmbedModelDir:  filepath.Join(emptyDir, "no-embed"),
		RerankModelDir: filepath.Join(emptyDir, "no-rerank"),
		SavingsPath:    filepath.Join(emptyDir, "no-savings.jsonl"),
	})

	if s.Versions.GoVersion == "" {
		t.Errorf("Versions.GoVersion should be populated from runtime.Version()")
	}
	if s.EmbedModel.Present {
		t.Errorf("EmbedModel.Present should be false for an empty dir")
	}
	if s.RerankModel.Present {
		t.Errorf("RerankModel.Present should be false for an empty dir")
	}
	if s.Savings.AllTime.Calls != 0 {
		t.Errorf("Savings should be zero with no savings file; got Calls=%d", s.Savings.AllTime.Calls)
	}
	if s.Index.FileCount != 0 {
		t.Errorf("Index.FileCount should be zero (no live index passed); got %d", s.Index.FileCount)
	}
	if !s.Enrichment.Enabled {
		t.Errorf("Enrichment should default to enabled (KEN_ENRICH unset); got %+v", s.Enrichment)
	}
}

// TestBuild_ModelPresent stats a present model file and confirms
// the size + mtime get reported.
func TestBuild_ModelPresent(t *testing.T) {
	dir := t.TempDir()
	modelPath := filepath.Join(dir, "model.safetensors")
	if err := os.WriteFile(modelPath, []byte("not really a model — just bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := Build(BuildOptions{EmbedModelDir: dir, SavingsPath: filepath.Join(dir, "no-savings.jsonl")})
	if !s.EmbedModel.Present {
		t.Fatalf("EmbedModel.Present should be true; got %+v", s.EmbedModel)
	}
	if s.EmbedModel.SizeBytes == 0 {
		t.Errorf("EmbedModel.SizeBytes should be > 0; got %d", s.EmbedModel.SizeBytes)
	}
	if s.EmbedModel.LastModified.IsZero() {
		t.Errorf("EmbedModel.LastModified should be set; got zero")
	}
}

// TestBuild_EnrichmentEnv pins the KEN_ENRICH parsing against the
// same rules the indexer uses (defaultFSOptions in
// internal/search/index.go).
func TestBuild_EnrichmentEnv(t *testing.T) {
	for _, tc := range []struct {
		envVal      string
		wantEnabled bool
	}{
		{"", true},
		{"on", true},
		{"true", true},
		{"yes", true},
		{"1", true},
		{"random", true}, // any non-disable value = enabled
		{"off", false},
		{"OFF", false},
		{"0", false},
		{"false", false},
		{"no", false},
	} {
		t.Run("KEN_ENRICH="+tc.envVal, func(t *testing.T) {
			t.Setenv("KEN_ENRICH", tc.envVal)
			s := Build(BuildOptions{SavingsPath: filepath.Join(t.TempDir(), "x")})
			if s.Enrichment.Enabled != tc.wantEnabled {
				t.Errorf("Enabled = %v, want %v for KEN_ENRICH=%q", s.Enrichment.Enabled, tc.wantEnabled, tc.envVal)
			}
			if s.Enrichment.EnvValue != tc.envVal {
				t.Errorf("EnvValue = %q, want %q (verbatim)", s.Enrichment.EnvValue, tc.envVal)
			}
		})
	}
}

// TestBuild_SavingsRoundTrip writes a synthetic savings.jsonl,
// builds, and confirms the renderer reflects the right bucket
// numbers and token estimate.
func TestBuild_SavingsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	savingsPath := filepath.Join(dir, "savings.jsonl")

	rec := usage.NewRecorder(savingsPath)
	if rec == nil {
		t.Fatal("NewRecorder returned nil for non-empty path")
	}
	for range 3 {
		rec.Record("search", 3 /* results */, 1000 /* snippetChars */, 5000 /* fileChars */)
	}

	s := Build(BuildOptions{SavingsPath: savingsPath})
	if s.Savings.AllTime.Calls != 3 {
		t.Fatalf("AllTime.Calls = %d, want 3", s.Savings.AllTime.Calls)
	}
	if s.Savings.AllTime.SnippetChars != 3000 {
		t.Errorf("AllTime.SnippetChars = %d, want 3000", s.Savings.AllTime.SnippetChars)
	}
	if s.Savings.AllTime.SavedChars != 12000 {
		t.Errorf("AllTime.SavedChars = %d, want 12000 (3 * 4000)", s.Savings.AllTime.SavedChars)
	}

	out := RenderText(s, false)
	for _, want := range []string{"Token savings", "3 calls", "all time", "saved", "tokens"} {
		if !strings.Contains(out, want) {
			t.Errorf("RenderText missing %q\n---\n%s", want, out)
		}
	}
}

// TestRender_SkipsEmptySections confirms the renderer's "honest
// about what it doesn't know" rule: zero IndexInfo / StructuralInfo
// / CacheInfo are not printed at all (no "Index (empty)" header).
func TestRender_SkipsEmptySections(t *testing.T) {
	s := Build(BuildOptions{SavingsPath: filepath.Join(t.TempDir(), "x")})
	out := RenderText(s, false)
	for _, mustNotAppear := range []string{"Index (live)", "Structural index", "Repo cache"} {
		if strings.Contains(out, mustNotAppear) {
			t.Errorf("expected section %q to be skipped for CLI build, got:\n%s", mustNotAppear, out)
		}
	}
}

// TestRender_LiveIndex confirms the MCP-side rendering when a live
// IndexInfo is threaded through.
func TestRender_LiveIndex(t *testing.T) {
	s := Build(BuildOptions{
		SavingsPath: filepath.Join(t.TempDir(), "x"),
		LiveIndex: &IndexInfo{
			Repo:        "/some/repo",
			FileCount:   1234,
			ChunkCount:  5678,
			Mode:        "hybrid",
			Chunker:     "regex",
			BuiltAt:     time.Now().Add(-2 * time.Minute),
			WatchActive: true,
		},
	})
	out := RenderText(s, false)
	for _, want := range []string{"Index (live)", "/some/repo", "1,234", "5,678", "hybrid", "regex", "watch", "active"} {
		if !strings.Contains(out, want) {
			t.Errorf("RenderText missing %q\n---\n%s", want, out)
		}
	}
}

// TestRenderJSON confirms the JSON round-trips through the standard
// encoder and contains the headline fields agents would look at.
func TestRenderJSON(t *testing.T) {
	s := Build(BuildOptions{SavingsPath: filepath.Join(t.TempDir(), "x")})
	out, err := RenderJSON(s)
	if err != nil {
		t.Fatal(err)
	}
	var rt Status
	if err := json.Unmarshal(out, &rt); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if rt.Versions.GoVersion == "" {
		t.Errorf("JSON should preserve Versions.GoVersion")
	}
}
