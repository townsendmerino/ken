// Package usage records per-call ken usage to an append-only jsonl
// file (~/.ken/savings.jsonl by default) and renders a "tokens saved"
// summary via the same shape semble uses.
//
// Privacy note: only timestamps, call type, result counts, and
// character counts are persisted. The query text, file paths, and
// chunk content are NEVER written. Opt out entirely by setting
// KEN_NO_USAGE_STATS=1 (cmd/ken-mcp) or --no-stats (cmd/ken search).
//
// Format: each line is a JSON object with the same field names as
// semble's savings.jsonl, so the renderer is portable across either
// tool's log:
//
//	{"ts":1717024800.12,"call":"search","results":5,"snippet_chars":2480,"file_chars":31200}
//
// Aggregation: ken savings reads the whole file (typically tens of
// MB for a heavy user — fast enough that lazy aggregation isn't worth
// the index complexity), buckets by Today / Last 7 days / All time,
// and renders a bar + percentage per bucket. Optional --verbose
// adds a breakdown by call type. saved_chars = max(0, file_chars -
// snippet_chars) per record; saved_tokens = saved_chars / 4 (the
// standard 4 chars/token approximation also used by semble).
package usage

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Record is one row of the savings.jsonl file. Field names match
// semble's stats.py exactly so a renderer on either tool's log
// produces the same output.
type Record struct {
	TS           float64 `json:"ts"`
	Call         string  `json:"call"`
	Results      int     `json:"results"`
	SnippetChars int     `json:"snippet_chars"`
	FileChars    int     `json:"file_chars"`
}

// Recorder is an append-only writer for a savings.jsonl file. Safe
// for concurrent use across goroutines (a sync.Mutex serializes the
// write); cross-process safety relies on POSIX append being atomic
// for writes ≤ PIPE_BUF (4096 bytes on Linux; our records are
// ≤ 150 bytes).
//
// A nil Recorder is a no-op — every method silently does nothing.
// This lets callers use the pattern `r.Record(...)` without nil
// checking; passing nil disables tracking with no code change.
type Recorder struct {
	mu   sync.Mutex
	path string
}

// NewRecorder returns a Recorder that appends to path. path is NOT
// opened until the first Record call (so a Recorder for a path that
// doesn't exist yet doesn't crash construction).
//
// An empty path returns nil, which the caller can pass around
// safely — nil Recorder is the documented no-op state. Errors during
// Record are silently swallowed (tracking failures must never break
// the user-facing tool surface; that's the semble convention too).
func NewRecorder(path string) *Recorder {
	if path == "" {
		return nil
	}
	return &Recorder{path: path}
}

// DefaultPath returns the canonical savings.jsonl path
// (~/.ken/savings.jsonl) or "" if HOME is unset.
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".ken", "savings.jsonl")
}

// Record appends a usage row. snippetChars is the total length of
// returned chunk text; fileChars is the total size of all unique
// files that contributed those chunks (so saved_chars =
// max(0, fileChars - snippetChars) approximates how much the agent
// didn't have to read).
//
// callType is a free-form string ("search", "find_related",
// "reindex_db" today; future tools should add their own). No
// validation — semble does the same.
//
// Errors are swallowed silently. Best-effort persistence is the
// contract (the alternative — surfacing errors to the agent — would
// add noise to every tool call for a feature that's strictly
// informational).
func (r *Recorder) Record(callType string, results, snippetChars, fileChars int) {
	if r == nil {
		return
	}
	rec := Record{
		TS:           float64(time.Now().UnixNano()) / 1e9,
		Call:         callType,
		Results:      results,
		SnippetChars: snippetChars,
		FileChars:    fileChars,
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return // shouldn't happen for fixed-shape struct, but defensive
	}
	line = append(line, '\n')

	r.mu.Lock()
	defer r.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(r.path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(r.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(line)
}

// ── Aggregation + rendering (port of semble's stats.py) ─────────────

// Bucket aggregates Record counts for one time window (today /
// last 7 days / all time). saved_chars accumulates as the
// per-record max(0, fileChars - snippetChars) so a record where the
// agent received MORE text than the source file (degenerate edge:
// duplicate-chunk corpus) contributes 0 rather than negative.
type Bucket struct {
	Calls        int
	SnippetChars int
	FileChars    int
	SavedChars   int
}

func (b *Bucket) add(rec Record) {
	b.Calls++
	b.SnippetChars += rec.SnippetChars
	b.FileChars += rec.FileChars
	if d := rec.FileChars - rec.SnippetChars; d > 0 {
		b.SavedChars += d
	}
}

// Summary is the output of BuildSummary: three time-window buckets
// plus the per-call-type counter that powers `--verbose`'s usage
// breakdown.
type Summary struct {
	Today          Bucket
	Last7Days      Bucket
	AllTime        Bucket
	CallTypeCounts map[string]int
}

// BuildSummary reads the jsonl file at path and aggregates by the
// three time windows. Missing file returns an empty Summary (zero
// values), NOT an error — the renderer surfaces "no stats yet."
// Malformed JSON lines are skipped silently.
//
// "Today" and "Last 7 days" use the system's local timezone so
// boundaries match the user's expectation (semble does the same
// with UTC; we honor local since interactive users live in their
// own tz).
func BuildSummary(path string) (Summary, error) {
	s := Summary{CallTypeCounts: map[string]int{}}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return s, err
	}
	defer f.Close()

	now := time.Now()
	today := now.Truncate(24 * time.Hour)
	sevenDaysAgo := today.AddDate(0, 0, -6) // include today + previous 6 days

	scan := bufio.NewScanner(f)
	// 64 KB default buffer is fine for our line shape (~150 bytes), but
	// bump explicitly so a corrupt long line doesn't kill the scan.
	scan.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scan.Scan() {
		var rec Record
		if err := json.Unmarshal(scan.Bytes(), &rec); err != nil {
			continue
		}
		s.CallTypeCounts[rec.Call]++
		s.AllTime.add(rec)
		ts := time.Unix(0, int64(rec.TS*1e9))
		if ts.After(sevenDaysAgo) || ts.Equal(sevenDaysAgo) {
			s.Last7Days.add(rec)
		}
		if ts.After(today) || ts.Equal(today) {
			s.Today.add(rec)
		}
	}
	if err := scan.Err(); err != nil && err != io.EOF {
		return s, err
	}
	return s, nil
}

// FormatReport renders a Summary as the human-readable table semble
// emits. Width-matched to semble's so an operator who uses both tools
// gets visually identical output (modulo the "Ken" / "Semble" label).
//
// verbose adds a "Usage Breakdown" section listing each call type's
// count. Matches semble's --verbose flag semantics.
//
// Empty summaries (no records yet) return "No stats yet. Run a
// search first." — same string semble uses.
func FormatReport(s Summary, verbose bool) string {
	if s.AllTime.Calls == 0 {
		return "No stats yet. Run a search first."
	}
	const barWidth = 16
	heavy := "  " + strings.Repeat("═", 64)
	light := "  " + strings.Repeat("─", 64)

	var b strings.Builder
	b.WriteString("\n")
	b.WriteString("  Ken Token Savings\n")
	b.WriteString(heavy + "\n")
	fmt.Fprintf(&b, "  %-12s  %-6s  Savings\n", "Period", "Calls")
	b.WriteString(light + "\n")

	for _, row := range []struct {
		label  string
		bucket Bucket
	}{
		{"Today", s.Today},
		{"Last 7 days", s.Last7Days},
		{"All time", s.AllTime},
	} {
		savedTokens := row.bucket.SavedChars / 4
		var savedStr string
		switch {
		case savedTokens >= 1_000_000:
			savedStr = fmt.Sprintf("~%.1fM", float64(savedTokens)/1_000_000)
		case savedTokens >= 1000:
			savedStr = fmt.Sprintf("~%.1fk", float64(savedTokens)/1000)
		default:
			savedStr = fmt.Sprintf("~%d", savedTokens)
		}
		var callsStr string
		if row.bucket.Calls >= 1000 {
			callsStr = fmt.Sprintf("%.1fk", float64(row.bucket.Calls)/1000)
		} else {
			callsStr = fmt.Sprintf("%d", row.bucket.Calls)
		}
		if row.bucket.FileChars > 0 {
			ratio := float64(row.bucket.SavedChars) / float64(row.bucket.FileChars)
			filled := int(ratio*float64(barWidth) + 0.5)
			if filled < 0 {
				filled = 0
			} else if filled > barWidth {
				filled = barWidth
			}
			bar := strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)
			pct := int(ratio*100 + 0.5)
			fmt.Fprintf(&b, "  %-12s  %-6s  [%s]  %s tokens (%d%%)\n",
				row.label, callsStr, bar, savedStr, pct)
		} else {
			bar := strings.Repeat("░", barWidth)
			fmt.Fprintf(&b, "  %-12s  %-6s  [%s]  %s tokens\n",
				row.label, callsStr, bar, savedStr)
		}
	}

	if verbose && len(s.CallTypeCounts) > 0 {
		b.WriteString("\n")
		b.WriteString("  Usage Breakdown\n")
		b.WriteString(light + "\n")
		fmt.Fprintf(&b, "  %-16s  Calls\n", "Call type")
		// Stable order for deterministic output (tests + diff-friendly).
		keys := make([]string, 0, len(s.CallTypeCounts))
		for k := range s.CallTypeCounts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			n := s.CallTypeCounts[k]
			var nStr string
			if n >= 1000 {
				nStr = fmt.Sprintf("%.1fk", float64(n)/1000)
			} else {
				nStr = fmt.Sprintf("%d", n)
			}
			fmt.Fprintf(&b, "  %-16s  %s\n", k, nStr)
		}
		b.WriteString(heavy + "\n")
	}
	b.WriteString("\n")
	return b.String()
}
