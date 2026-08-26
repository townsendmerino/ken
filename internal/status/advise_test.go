package status

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/townsendmerino/ken/internal/usage"
)

func findTitle(fs []Finding, title string) *Finding {
	for i := range fs {
		if fs[i].Title == title {
			return &fs[i]
		}
	}
	return nil
}

// healthyInputs is a baseline where everything is present/warm, so each test can
// perturb one axis.
func healthyInputs() AdviseInputs {
	return AdviseInputs{
		Status: Status{
			EmbedModel:  ModelInfo{Present: true, Dir: "/m"},
			RerankModel: ModelInfo{Present: true, Dir: "/r"},
			Enrichment:  EnrichmentInfo{Enabled: true},
			SavingsPath: "/s",
			Savings:     usage.Summary{AllTime: usage.Bucket{Calls: 10, SavedChars: 1234}},
		},
		RerankCacheEntries: 500,
	}
}

func TestAdvise_LargeCorpus(t *testing.T) {
	// Above the threshold → a SeverityInfo "Large corpus" finding suggesting STAGED.
	in := healthyInputs()
	in.CorpusChunks = 200_000
	f := Advise(in)
	got := findTitle(f, "Large corpus")
	if got == nil {
		t.Fatal("expected a 'Large corpus' finding at 200k chunks")
	}
	if got.Severity != SeverityInfo {
		t.Errorf("Large corpus severity = %v, want Info", got.Severity)
	}
	if !strings.Contains(got.Action, "KEN_MCP_STAGED=1") {
		t.Errorf("action should suggest KEN_MCP_STAGED=1; got %q", got.Action)
	}

	// Below the threshold (and unset, 0) → no finding.
	in.CorpusChunks = 50_000
	if findTitle(Advise(in), "Large corpus") != nil {
		t.Error("50k chunks is under the threshold — should not fire")
	}
	if findTitle(Advise(healthyInputs()), "Large corpus") != nil {
		t.Error("unset CorpusChunks (0) should not fire")
	}
}

func TestAdvise_HealthyIsAllOK(t *testing.T) {
	f := Advise(healthyInputs())
	for _, x := range f {
		if x.Severity != SeverityOK {
			t.Errorf("healthy inputs produced a non-OK finding: %+v", x)
		}
	}
	if findTitle(f, "Embedding model present") == nil {
		t.Error("expected an 'Embedding model present' OK finding")
	}
}

func TestAdvise_NoModelWarns(t *testing.T) {
	in := healthyInputs()
	in.Status.EmbedModel = ModelInfo{Present: false, Dir: "/nope"}
	f := Advise(in)
	m := findTitle(f, "No embedding model")
	if m == nil {
		t.Fatal("expected a 'No embedding model' finding")
	}
	if m.Severity != SeverityWarn {
		t.Errorf("severity = %v, want warn", m.Severity)
	}
	if !strings.Contains(m.Action, "download-model") {
		t.Errorf("action should point at download-model; got %q", m.Action)
	}
}

func TestAdvise_ModeNeedsModel(t *testing.T) {
	in := healthyInputs()
	in.Status.EmbedModel.Present = false
	in.MCPMode = "hybrid"

	// With auto-fetch on (default), the finding notes ken-mcp will upgrade.
	f := Advise(in)
	m := findTitle(f, "Configured mode needs a model")
	if m == nil || m.Severity != SeverityWarn {
		t.Fatalf("expected a warn 'Configured mode needs a model'; got %+v", m)
	}
	if strings.Contains(m.Detail, "indefinitely") {
		t.Errorf("auto-fetch on should NOT say 'indefinitely'; got %q", m.Detail)
	}

	// With auto-fetch disabled, it must warn that BM25 is served indefinitely.
	in.AutoFetchDisabled = true
	m = findTitle(Advise(in), "Configured mode needs a model")
	if m == nil || !strings.Contains(m.Detail, "indefinitely") {
		t.Errorf("auto-fetch off should warn 'indefinitely'; got %+v", m)
	}

	// bm25 mode with no model is fine — no mismatch finding.
	in2 := healthyInputs()
	in2.Status.EmbedModel.Present = false
	in2.MCPMode = "bm25"
	if findTitle(Advise(in2), "Configured mode needs a model") != nil {
		t.Error("bm25 mode should not trigger the mode/model mismatch warning")
	}
}

func TestAdvise_RerankCacheStates(t *testing.T) {
	// Warm.
	if findTitle(Advise(healthyInputs()), "Rerank cache warm") == nil {
		t.Error("entries>0 should report 'Rerank cache warm'")
	}
	// Cold (model present, no entries).
	cold := healthyInputs()
	cold.RerankCacheEntries = 0
	if m := findTitle(Advise(cold), "Rerank cache cold"); m == nil || m.Severity != SeverityInfo {
		t.Errorf("entries=0 should report an info 'Rerank cache cold'; got %+v", m)
	}
	// No rerank model at all.
	noModel := healthyInputs()
	noModel.Status.RerankModel.Present = false
	if m := findTitle(Advise(noModel), "No rerank model"); m == nil || !strings.Contains(m.Action, "--rerank") {
		t.Errorf("missing rerank model should suggest download-model --rerank; got %+v", m)
	}
}

func TestAdvise_EnrichmentAndCache(t *testing.T) {
	in := healthyInputs()
	in.Status.Enrichment = EnrichmentInfo{Enabled: false, EnvValue: "off"}
	in.CachingDisabled = true
	f := Advise(in)
	if m := findTitle(f, "Arm B enrichment is off"); m == nil || m.Severity != SeverityInfo {
		t.Errorf("enrichment off should be an info finding; got %+v", m)
	}
	if m := findTitle(f, "Repo cache disabled"); m == nil || m.Severity != SeverityWarn {
		t.Errorf("cache disabled should be a warn finding; got %+v", m)
	}
}

func TestAdvise_OrderedMostSevereFirst(t *testing.T) {
	in := healthyInputs()
	in.Status.EmbedModel.Present = false // adds a warn
	in.CachingDisabled = true            // adds a warn
	f := Advise(in)
	lastSev := SeverityWarn + 1
	for _, x := range f {
		if x.Severity > lastSev {
			t.Errorf("findings not ordered most-severe-first: %v after %v", x.Severity, lastSev)
		}
		lastSev = x.Severity
	}
}

func TestRenderAdviceJSON(t *testing.T) {
	in := healthyInputs()
	in.Status.EmbedModel.Present = false
	out, err := RenderAdviceJSON(Advise(in))
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Findings []struct {
			Severity, Title, Action string
		} `json:"findings"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("JSON invalid: %v\n%s", err, out)
	}
	if len(parsed.Findings) == 0 || parsed.Findings[0].Severity != "warn" {
		t.Errorf("expected the most-severe (warn) finding first; got %+v", parsed.Findings)
	}
}
