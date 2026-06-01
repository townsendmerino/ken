package usage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRecorder_writeAppendsLine: each Record call appends one
// well-formed JSON line; field names match semble's exactly so an
// external aggregator (or a future ken read of a semble log) works.
func TestRecorder_writeAppendsLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "savings.jsonl")
	r := NewRecorder(path)

	r.Record("search", 5, 2480, 31200)
	r.Record("find_related", 3, 1200, 8400)
	r.Record("search", 1, 600, 600)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3 (file=%q)", len(lines), data)
	}

	// First line should parse as a Record with matching values.
	var rec Record
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("unmarshal line 0: %v", err)
	}
	if rec.Call != "search" {
		t.Errorf("call: got %q want search", rec.Call)
	}
	if rec.Results != 5 || rec.SnippetChars != 2480 || rec.FileChars != 31200 {
		t.Errorf("record fields: got %+v", rec)
	}
	if rec.TS <= 0 {
		t.Errorf("ts: got %v, want positive Unix time", rec.TS)
	}
}

// TestRecorder_privacy_noQueryTextOrPaths: this is the contract the
// privacy promise rests on — no query string, no file path, no chunk
// content ever lands in the jsonl. If a future Record() change adds
// a field, this test fails until the new field is verified safe.
func TestRecorder_privacy_noQueryTextOrPaths(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "savings.jsonl")
	r := NewRecorder(path)
	r.Record("search", 1, 100, 500)
	data, _ := os.ReadFile(path)
	asString := string(data)
	// The 5 documented fields are exactly what should appear, no more.
	// Whitelist check on JSON keys.
	var anyRec map[string]any
	if err := json.Unmarshal(data[:len(data)-1], &anyRec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	allowed := map[string]bool{
		"ts": true, "call": true, "results": true,
		"snippet_chars": true, "file_chars": true,
	}
	for k := range anyRec {
		if !allowed[k] {
			t.Errorf("UNEXPECTED key %q in record (privacy violation candidate); full line: %s", k, asString)
		}
	}
}

// TestRecorder_nilIsNoOp: passing nil through the call chain must
// silently work — that's how callers opt out without conditionals.
func TestRecorder_nilIsNoOp(t *testing.T) {
	var r *Recorder
	// Should not panic.
	r.Record("search", 5, 1000, 5000)
}

// TestRecorder_emptyPathReturnsNil: NewRecorder("") returns the nil
// recorder which means subsequent .Record calls are no-ops.
func TestRecorder_emptyPathReturnsNil(t *testing.T) {
	if r := NewRecorder(""); r != nil {
		t.Errorf("NewRecorder(\"\") = %v, want nil", r)
	}
}

// TestBuildSummary_buckets: write records spanning today / 2 days ago
// / 30 days ago, verify each bucket counts the right ones.
func TestBuildSummary_buckets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "savings.jsonl")

	now := time.Now()
	writeRecord(t, path, "search", 1, 100, 1000, now)                        // today
	writeRecord(t, path, "search", 1, 200, 2000, now.AddDate(0, 0, -2))      // 2 days ago (in 7d)
	writeRecord(t, path, "find_related", 1, 50, 500, now.AddDate(0, 0, -10)) // 10 days ago (in all)

	s, err := BuildSummary(path)
	if err != nil {
		t.Fatalf("BuildSummary: %v", err)
	}
	if s.Today.Calls != 1 {
		t.Errorf("Today.Calls=%d, want 1", s.Today.Calls)
	}
	if s.Last7Days.Calls != 2 {
		t.Errorf("Last7Days.Calls=%d, want 2 (today + 2-days-ago)", s.Last7Days.Calls)
	}
	if s.AllTime.Calls != 3 {
		t.Errorf("AllTime.Calls=%d, want 3", s.AllTime.Calls)
	}
	if s.CallTypeCounts["search"] != 2 {
		t.Errorf("CallTypeCounts[search]=%d, want 2", s.CallTypeCounts["search"])
	}
	if s.CallTypeCounts["find_related"] != 1 {
		t.Errorf("CallTypeCounts[find_related]=%d, want 1", s.CallTypeCounts["find_related"])
	}
	// SavedChars math: max(0, file - snippet) per record.
	wantTodaySaved := 1000 - 100
	if s.Today.SavedChars != wantTodaySaved {
		t.Errorf("Today.SavedChars=%d, want %d", s.Today.SavedChars, wantTodaySaved)
	}
	wantAllSaved := (1000 - 100) + (2000 - 200) + (500 - 50)
	if s.AllTime.SavedChars != wantAllSaved {
		t.Errorf("AllTime.SavedChars=%d, want %d", s.AllTime.SavedChars, wantAllSaved)
	}
}

// TestBuildSummary_missingFile_noError: first-run UX — the file
// doesn't exist yet, BuildSummary returns an empty Summary and nil
// error so the renderer prints "no stats yet."
func TestBuildSummary_missingFile_noError(t *testing.T) {
	s, err := BuildSummary("/nonexistent/savings.jsonl")
	if err != nil {
		t.Fatalf("err=%v, want nil", err)
	}
	if s.AllTime.Calls != 0 {
		t.Errorf("AllTime.Calls=%d, want 0", s.AllTime.Calls)
	}
}

// TestBuildSummary_skipsMalformedLines: a corrupt JSON line shouldn't
// kill the whole aggregation — just skip it.
func TestBuildSummary_skipsMalformedLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "savings.jsonl")
	now := time.Now()
	writeRecord(t, path, "search", 1, 100, 1000, now)
	// Inject a corrupt line.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if _, err := f.WriteString("this is not json\n"); err != nil {
		t.Fatalf("inject corrupt line: %v", err)
	}
	f.Close()
	writeRecord(t, path, "search", 1, 200, 2000, now)

	s, err := BuildSummary(path)
	if err != nil {
		t.Fatalf("BuildSummary: %v", err)
	}
	if s.AllTime.Calls != 2 {
		t.Errorf("AllTime.Calls=%d, want 2 (corrupt line skipped)", s.AllTime.Calls)
	}
}

// TestFormatReport_noRecords: empty summary renders the no-stats
// message verbatim (matches semble's string).
func TestFormatReport_noRecords(t *testing.T) {
	out := FormatReport(Summary{CallTypeCounts: map[string]int{}}, false)
	if out != "No stats yet. Run a search first." {
		t.Errorf("got %q, want exact no-stats message", out)
	}
}

// TestFormatReport_basicShape: the headline ("Ken Token Savings"),
// the bucket labels, and the bar-chart character should all appear.
// Not a golden-byte comparison (rendering may evolve) but the
// load-bearing content has to be present.
func TestFormatReport_basicShape(t *testing.T) {
	s := Summary{
		Today:     Bucket{Calls: 42, FileChars: 60000, SnippetChars: 3000, SavedChars: 57000},
		Last7Days: Bucket{Calls: 287, FileChars: 350000, SnippetChars: 35000, SavedChars: 315000},
		AllTime:   Bucket{Calls: 1400, FileChars: 1300000, SnippetChars: 100000, SavedChars: 1200000},
		CallTypeCounts: map[string]int{
			"search": 1300, "find_related": 100,
		},
	}
	out := FormatReport(s, true)
	for _, want := range []string{
		"Ken Token Savings",
		"Today",
		"Last 7 days",
		"All time",
		"█", // bar character
		"~14.2k tokens",
		"~78.8k tokens",
		"~300.0k tokens",
		"Usage Breakdown",
		"find_related",
		"search",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in report:\n%s", want, out)
		}
	}
}

// writeRecord appends a Record to path using a caller-supplied
// timestamp. Test helper — production code always uses time.Now in
// the Recorder, but we need historical timestamps for bucket-edge
// tests.
func writeRecord(t *testing.T, path, call string, results, snippet, file int, ts time.Time) {
	t.Helper()
	rec := Record{
		TS:           float64(ts.UnixNano()) / 1e9,
		Call:         call,
		Results:      results,
		SnippetChars: snippet,
		FileChars:    file,
	}
	line, _ := json.Marshal(rec)
	line = append(line, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(line); err != nil {
		t.Fatalf("write savings record: %v", err)
	}
}
