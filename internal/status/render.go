package status

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/townsendmerino/ken/internal/usage"
)

// RenderText returns the default human-readable rendering of a
// Status. verbose=true prints the additional sections (per-language
// extractor coverage, per-call-type counts, process info).
//
// Empty sections — e.g. a CLI invocation that has no live index —
// are skipped rather than printed as "(empty)" or "0", matching the
// philosophy that surfaces should be honest about what they don't
// know.
func RenderText(s Status, verbose bool) string {
	var b strings.Builder

	// ---------- Header ----------
	fmt.Fprintln(&b, "ken status")
	fmt.Fprintln(&b, "==========")
	fmt.Fprintln(&b)

	// ---------- Versions ----------
	fmt.Fprintln(&b, "Build")
	v := s.Versions
	if v.VcsRevision != "" {
		commit := v.VcsRevision
		if len(commit) > 12 {
			commit = commit[:12]
		}
		dirty := ""
		if v.VcsDirty {
			dirty = " (dirty)"
		}
		fmt.Fprintf(&b, "  commit:        %s%s\n", commit, dirty)
	}
	if v.AikitVersion != "" {
		fmt.Fprintf(&b, "  aikit:         %s\n", v.AikitVersion)
	}
	if v.GotreesitterVersion != "" {
		fmt.Fprintf(&b, "  gotreesitter:  %s\n", v.GotreesitterVersion)
	}
	fmt.Fprintf(&b, "  go:            %s\n", v.GoVersion)
	fmt.Fprintln(&b)

	// ---------- Models ----------
	fmt.Fprintln(&b, "Models")
	renderModel(&b, "embedding", s.EmbedModel)
	renderModel(&b, "rerank   ", s.RerankModel)
	fmt.Fprintln(&b)

	// ---------- Enrichment ----------
	fmt.Fprintln(&b, "Arm B enrichment")
	if s.Enrichment.Enabled {
		fmt.Fprintln(&b, "  enabled (default; structural prefix prepended at index time)")
	} else {
		raw := s.Enrichment.EnvValue
		if raw == "" {
			raw = "(empty)"
		}
		fmt.Fprintf(&b, "  disabled — KEN_ENRICH=%s\n", raw)
	}
	fmt.Fprintln(&b)

	// ---------- Live index (MCP only) ----------
	if s.Index.FileCount > 0 || s.Index.ChunkCount > 0 || s.Index.Repo != "" {
		fmt.Fprintln(&b, "Index (live)")
		if s.Index.Repo != "" {
			fmt.Fprintf(&b, "  repo:        %s\n", s.Index.Repo)
		}
		if s.Index.FileCount > 0 {
			fmt.Fprintf(&b, "  files:       %s\n", formatInt(s.Index.FileCount))
		}
		if s.Index.ChunkCount > 0 {
			fmt.Fprintf(&b, "  chunks:      %s\n", formatInt(s.Index.ChunkCount))
		}
		if s.Index.Mode != "" {
			fmt.Fprintf(&b, "  mode:        %s\n", s.Index.Mode)
		}
		if s.Index.Chunker != "" {
			fmt.Fprintf(&b, "  chunker:     %s\n", s.Index.Chunker)
		}
		if !s.Index.BuiltAt.IsZero() {
			age := s.Process.StartedAt.Sub(s.Index.BuiltAt)
			fmt.Fprintf(&b, "  built:       %s ago\n", formatDuration(age))
		}
		fmt.Fprintf(&b, "  watch:       %s\n", boolWord(s.Index.WatchActive, "active", "inactive"))
		fmt.Fprintln(&b)
	}

	// ---------- Live structural (MCP only) ----------
	if s.Structural.TopLevelSymbols > 0 || s.Structural.Methods > 0 || len(s.Structural.PerLanguageFiles) > 0 {
		fmt.Fprintln(&b, "Structural index")
		if s.Structural.TopLevelSymbols > 0 {
			fmt.Fprintf(&b, "  top-level symbols: %s\n", formatInt(s.Structural.TopLevelSymbols))
		}
		if s.Structural.Methods > 0 {
			fmt.Fprintf(&b, "  methods:           %s\n", formatInt(s.Structural.Methods))
		}
		if verbose && len(s.Structural.PerLanguageFiles) > 0 {
			fmt.Fprintln(&b, "  per-language file count:")
			for _, kv := range sortLangCounts(s.Structural.PerLanguageFiles) {
				fmt.Fprintf(&b, "    %-6s %s\n", kv.lang, formatInt(kv.count))
			}
		}
		fmt.Fprintln(&b)
	}

	// ---------- Live cache (MCP only) ----------
	if s.Cache.Capacity > 0 || s.Cache.InUse > 0 {
		fmt.Fprintln(&b, "Repo cache (MCP)")
		fmt.Fprintf(&b, "  in use / cap:  %d / %d\n", s.Cache.InUse, s.Cache.Capacity)
		if verbose && len(s.Cache.RepoLabels) > 0 {
			fmt.Fprintln(&b, "  cached repos:")
			for _, r := range s.Cache.RepoLabels {
				fmt.Fprintf(&b, "    - %s\n", r)
			}
		}
		fmt.Fprintln(&b)
	}

	// ---------- Token savings ----------
	renderSavings(&b, s, verbose)

	// ---------- Process (verbose only) ----------
	if verbose {
		fmt.Fprintln(&b, "Process")
		fmt.Fprintf(&b, "  goos / goarch:  %s / %s\n", s.Process.GOOS, s.Process.GOARCH)
		fmt.Fprintf(&b, "  GOMAXPROCS:     %d\n", s.Process.GOMAXPROCS)
		fmt.Fprintln(&b)
	}

	return strings.TrimRight(b.String(), "\n") + "\n"
}

// RenderJSON marshals the Status with stable field order. Used by
// `ken status --json` and by the MCP tool when the agent asks for
// structured output.
func RenderJSON(s Status) ([]byte, error) {
	return json.MarshalIndent(s, "", "  ")
}

// RenderMarkdown is the rendering used by the MCP `status` tool.
// Same content as RenderText with markdown headings + code-fenced
// numeric blocks so the agent sees it cleanly.
func RenderMarkdown(s Status, verbose bool) string {
	// For Pass 1 the text format already reads cleanly through a
	// markdown viewer (indented sections, plain field names).
	// Wrap it in a ```text``` fence so MCP clients that strictly
	// honor markdown render the whitespace-aligned columns.
	return "```text\n" + RenderText(s, verbose) + "```\n"
}

func renderModel(b *strings.Builder, label string, m ModelInfo) {
	if m.Dir == "" {
		fmt.Fprintf(b, "  %s: (no dir resolved)\n", label)
		return
	}
	if !m.Present {
		fmt.Fprintf(b, "  %s: missing  — dir=%s\n", label, m.Dir)
		return
	}
	fmt.Fprintf(b, "  %s: present (%s) at %s\n", label, formatBytes(m.SizeBytes), m.Dir)
}

func renderSavings(b *strings.Builder, s Status, verbose bool) {
	if s.SavingsPath == "" {
		return
	}
	fmt.Fprintln(b, "Token savings")
	if s.Savings.AllTime.Calls == 0 {
		fmt.Fprintln(b, "  no recorded calls yet (the persistent log lives at "+s.SavingsPath+")")
		fmt.Fprintln(b)
		return
	}
	fmt.Fprintf(b, "  store: %s\n", s.SavingsPath)
	fmt.Fprintf(b, "  saved chars ≈ tokens (chars / %d):\n", CharsPerToken)
	renderBucket(b, "today        ", s.Savings.Today)
	renderBucket(b, "last 7 days  ", s.Savings.Last7Days)
	renderBucket(b, "all time     ", s.Savings.AllTime)
	if verbose && len(s.Savings.CallTypeCounts) > 0 {
		fmt.Fprintln(b, "  all-time per-call-type counts:")
		for _, kv := range sortCallCounts(s.Savings.CallTypeCounts) {
			fmt.Fprintf(b, "    %-15s %s\n", kv.name, formatInt(kv.count))
		}
	}
	fmt.Fprintln(b)
	fmt.Fprintln(b, "  (\"saved\" = chars in the matched files that the agent")
	fmt.Fprintln(b, "  did NOT have to read whole-file because ken returned a snippet")
	fmt.Fprintln(b, "  instead. Upper-bound estimate vs reading every matched file in full.)")
	fmt.Fprintln(b)
}

func renderBucket(b *strings.Builder, label string, bk usage.Bucket) {
	if bk.Calls == 0 {
		fmt.Fprintf(b, "    %s   %s\n", label, "(no calls)")
		return
	}
	snippet := formatBytes(int64(bk.SnippetChars))
	saved := formatBytes(int64(bk.SavedChars))
	fmt.Fprintf(b, "    %s   %d calls, %s returned, ~%s saved (≈ %s tokens)\n",
		label, bk.Calls, snippet, saved, formatInt(bk.SavedChars/CharsPerToken))
}

type langCount struct {
	lang  string
	count int
}

func sortLangCounts(m map[string]int) []langCount {
	out := make([]langCount, 0, len(m))
	for k, v := range m {
		out = append(out, langCount{k, v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].count != out[j].count {
			return out[i].count > out[j].count
		}
		return out[i].lang < out[j].lang
	})
	return out
}

type callCount struct {
	name  string
	count int
}

func sortCallCounts(m map[string]int) []callCount {
	out := make([]callCount, 0, len(m))
	for k, v := range m {
		out = append(out, callCount{k, v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].count != out[j].count {
			return out[i].count > out[j].count
		}
		return out[i].name < out[j].name
	})
	return out
}

func formatBytes(n int64) string {
	const (
		KB = 1024
		MB = 1024 * 1024
		GB = 1024 * 1024 * 1024
	)
	switch {
	case n >= GB:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(GB))
	case n >= MB:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(MB))
	case n >= KB:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(KB))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func formatInt(n int) string {
	// Insert thousands separators ("1,234,567"). Pure-stdlib so we
	// don't pull a humanize dep for one number-formatting use.
	s := fmt.Sprintf("%d", n)
	if n < 0 {
		return "-" + formatInt(-n)
	}
	parts := []byte(s)
	if len(parts) <= 3 {
		return s
	}
	var b strings.Builder
	rem := len(parts) % 3
	if rem > 0 {
		b.Write(parts[:rem])
		if len(parts) > rem {
			b.WriteByte(',')
		}
	}
	for i := rem; i < len(parts); i += 3 {
		b.Write(parts[i : i+3])
		if i+3 < len(parts) {
			b.WriteByte(',')
		}
	}
	return b.String()
}

func formatDuration(d any) string {
	// Accept time.Duration via interface so callers that build the
	// label string from a future absolute clock difference can
	// still feed it. Currently only time.Duration is supported;
	// fall back to default formatting otherwise.
	switch v := d.(type) {
	case interface{ String() string }:
		return v.String()
	default:
		return fmt.Sprintf("%v", v)
	}
}

func boolWord(cond bool, t, f string) string {
	if cond {
		return t
	}
	return f
}
