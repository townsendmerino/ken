package status

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// advise.go — the `ken doctor` advisory layer.
//
// status.Build already gathers the raw facts (model presence, rerank model,
// enrichment state, token-savings summary). `status`, `internal/usage`, and the
// rerank cache each report a slice of that separately, leaving an operator to
// cross-reference three surfaces by hand. Advise folds them into one
// recommendation pass: it reasons over a Status snapshot plus a couple of extra
// probes and emits prioritized, actionable findings ("no model — run
// download-model", "caching disabled — raise KEN_MCP_CACHE_SIZE").
//
// Advise is a pure function of AdviseInputs so it unit-tests without touching
// the filesystem or env; the CLI (cmd/ken) does the gathering.

// Severity orders findings: Warn (likely degrading results/perf) > Info
// (optional improvement) > OK (healthy, shown for reassurance).
type Severity int

const (
	SeverityOK Severity = iota
	SeverityInfo
	SeverityWarn
)

func (s Severity) String() string {
	switch s {
	case SeverityWarn:
		return "warn"
	case SeverityInfo:
		return "info"
	default:
		return "ok"
	}
}

// Finding is one advisory result. Action is the command/knob that resolves it
// (empty for an OK/healthy finding).
type Finding struct {
	Severity Severity `json:"severity"`
	Title    string   `json:"title"`
	Detail   string   `json:"detail,omitempty"`
	Action   string   `json:"action,omitempty"`
}

// AdviseInputs carries the facts Advise reasons over. Status is the shared
// snapshot from Build; the rest are extra probes/config the caller gathers so
// Advise stays pure.
type AdviseInputs struct {
	Status Status

	// RerankCacheEntries is the entry count of the on-disk rerank cache, or -1
	// when it wasn't probed / the file is absent.
	RerankCacheEntries int

	// MCPMode is the raw KEN_MCP_MODE value ("" if unset) — used to warn when a
	// model-needing mode is configured but no model is present.
	MCPMode string

	// CachingDisabled is true when KEN_MCP_CACHE_SIZE == "0".
	CachingDisabled bool

	// AutoFetchDisabled is true when KEN_MCP_AUTO_FETCH is explicitly falsey.
	AutoFetchDisabled bool

	// CorpusChunks is the chunk count of the repo's existing snapshot
	// (search.PeekSnapshotChunks on <cwd>/.ken/snapshot.bin), or 0 when no
	// snapshot is present / it wasn't probed. Drives the large-corpus advice.
	CorpusChunks int
}

// largeCorpusChunks is the chunk count above which a cold hybrid build's
// embedding pass is slow enough (multi-second) that KEN_MCP_STAGED's
// serve-bm25-first pays off. The largest corpus measured (Linux kernel, hybrid)
// is ~826k chunks; 100k is a comfortably-large repo where the suggestion helps.
const largeCorpusChunks = 100_000

// Advise analyzes the inputs and returns findings ordered most-severe-first.
func Advise(in AdviseInputs) []Finding {
	var f []Finding
	st := in.Status

	// 1. Embedding model — the single biggest retrieval-quality lever.
	if st.EmbedModel.Present {
		f = append(f, Finding{SeverityOK, "Embedding model present",
			fmt.Sprintf("Found model.safetensors in %s — semantic/hybrid search is available.", st.EmbedModel.Dir), ""})
	} else {
		f = append(f, Finding{SeverityWarn, "No embedding model",
			fmt.Sprintf("No model.safetensors under %s — search falls back to BM25-only (~14pp lower recall@10 than the hybrid default).", st.EmbedModel.Dir),
			"ken download-model"})
	}

	// 2. Config cross-reference: a model-needing mode configured with no model.
	if !st.EmbedModel.Present && modeNeedsModel(in.MCPMode) {
		detail := fmt.Sprintf("KEN_MCP_MODE=%s needs an embedding model, but none is present.", in.MCPMode)
		action := "ken download-model (or let ken-mcp auto-fetch it on first run)"
		if in.AutoFetchDisabled {
			detail += " Auto-fetch is off (KEN_MCP_AUTO_FETCH=0), so ken-mcp will serve BM25 indefinitely."
			action = "ken download-model, or unset KEN_MCP_AUTO_FETCH so ken-mcp fetches it"
		} else {
			detail += " ken-mcp serves BM25 until the background auto-fetch lands, then upgrades to hybrid."
		}
		f = append(f, Finding{SeverityWarn, "Configured mode needs a model", detail, action})
	}

	// 3. Rerank model + persistent cache warmth.
	switch {
	case !st.RerankModel.Present:
		f = append(f, Finding{SeverityInfo, "No rerank model",
			"The optional neural reranker (hybrid-rerank mode) isn't installed.",
			"ken download-model --rerank"})
	case in.RerankCacheEntries > 0:
		f = append(f, Finding{SeverityOK, "Rerank cache warm",
			fmt.Sprintf("%s entries on disk — hybrid-rerank first queries start fast.", formatInt(in.RerankCacheEntries)), ""})
	default:
		f = append(f, Finding{SeverityInfo, "Rerank cache cold",
			"The persistent rerank cache is empty — the first hybrid-rerank queries are slow (~5s cold vs ~0.6s warm). It warms automatically as you query.", ""})
	}

	// 4. Arm B enrichment.
	if !st.Enrichment.Enabled {
		f = append(f, Finding{SeverityInfo, "Arm B enrichment is off",
			fmt.Sprintf("KEN_ENRICH=%q disables structural enrichment labels — retrieval quality is slightly lower on supported languages.", st.Enrichment.EnvValue),
			"unset KEN_ENRICH to re-enable (it's on by default)"})
	}

	// 5. Repo cache disabled.
	if in.CachingDisabled {
		f = append(f, Finding{SeverityWarn, "Repo cache disabled",
			"KEN_MCP_CACHE_SIZE=0 turns off the repo→index cache, so ken-mcp re-indexes on every request.",
			"raise KEN_MCP_CACHE_SIZE (default 16)"})
	}

	// 6. Repo shape — a large corpus benefits from a staged cold start
	// (docs/USERS.md "Tuning for your repo shape"). Sized from the existing
	// snapshot's chunk count (peeked, no rebuild) when one is present.
	if in.CorpusChunks >= largeCorpusChunks {
		f = append(f, Finding{SeverityInfo, "Large corpus",
			fmt.Sprintf("This repo's snapshot holds %s chunks; on a cold start, hybrid embedding of that many chunks is the slow step.", formatInt(in.CorpusChunks)),
			"set KEN_MCP_STAGED=1 — ken-mcp serves BM25 lexical instantly and upgrades to hybrid in the background (see docs/USERS.md \"Tuning for your repo shape\")"})
	}

	// 7. Usage / token-savings tracking.
	if st.SavingsPath == "" || st.Savings.AllTime.Calls == 0 {
		f = append(f, Finding{SeverityInfo, "No usage recorded yet",
			"Token-savings tracking has no entries yet — it populates as agents call ken.", ""})
	} else {
		at := st.Savings.AllTime
		f = append(f, Finding{SeverityOK, "Usage tracked",
			fmt.Sprintf("%s calls recorded; ~%s characters saved vs grep+Read (see `ken savings`).",
				formatInt(at.Calls), formatInt(at.SavedChars)), ""})
	}

	sort.SliceStable(f, func(i, j int) bool { return f[i].Severity > f[j].Severity })
	return f
}

// modeNeedsModel reports whether a KEN_MCP_MODE value requires an embedding
// model. Case-sensitive, matching ken-mcp's validated enum.
func modeNeedsModel(mode string) bool {
	switch strings.TrimSpace(mode) {
	case "semantic", "hybrid", "hybrid-rerank":
		return true
	}
	return false
}

// RenderAdvice formats findings as human-readable text. OK findings are shown
// (as ✓ reassurance) only under verbose; warnings and suggestions always show.
func RenderAdvice(findings []Finding, verbose bool) string {
	var warn, info int
	for _, f := range findings {
		switch f.Severity {
		case SeverityWarn:
			warn++
		case SeverityInfo:
			info++
		}
	}

	var b strings.Builder
	b.WriteString("ken doctor\n")
	switch {
	case warn == 0 && info == 0:
		b.WriteString("Everything looks healthy.\n")
	default:
		fmt.Fprintf(&b, "%s, %s\n",
			pluralize(warn, "warning"), pluralize(info, "suggestion"))
	}
	b.WriteString("\n")

	for _, f := range findings {
		if f.Severity == SeverityOK && !verbose {
			continue
		}
		var marker string
		switch f.Severity {
		case SeverityWarn:
			marker = "⚠"
		case SeverityInfo:
			marker = "•"
		default:
			marker = "✓"
		}
		fmt.Fprintf(&b, "%s %s\n", marker, f.Title)
		if f.Detail != "" {
			fmt.Fprintf(&b, "    %s\n", f.Detail)
		}
		if f.Action != "" {
			fmt.Fprintf(&b, "    → %s\n", f.Action)
		}
	}
	if !verbose {
		ok := 0
		for _, f := range findings {
			if f.Severity == SeverityOK {
				ok++
			}
		}
		if ok > 0 {
			fmt.Fprintf(&b, "\n%s passed (run with --verbose to list them).\n", pluralize(ok, "healthy check"))
		}
	}
	return b.String()
}

// RenderAdviceJSON serializes the findings for machine consumption.
func RenderAdviceJSON(findings []Finding) ([]byte, error) {
	// Emit severity as its string form, not the int, for a stable JSON surface.
	type row struct {
		Severity string `json:"severity"`
		Title    string `json:"title"`
		Detail   string `json:"detail,omitempty"`
		Action   string `json:"action,omitempty"`
	}
	rows := make([]row, 0, len(findings))
	for _, f := range findings {
		rows = append(rows, row{f.Severity.String(), f.Title, f.Detail, f.Action})
	}
	return json.MarshalIndent(map[string]any{"findings": rows}, "", "  ")
}

func pluralize(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
