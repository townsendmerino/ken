//go:build bench

// Corpus harness for the traceability metric, plus unit tests for the
// classifier it rests on.
//
//	go test -tags=bench ./bench/chunkdiff/ -run TestTraceability_SembleCorpus -v -timeout 30m
//
// Skips itself when the semble corpus isn't synced, like every other
// corpus-backed harness here.

package chunkdiff

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	// Side-effect imports: register the chunkers under test. regex is
	// the default; treesitter is the challenger ADR-011 rejected on
	// aggregate NDCG. internal/search no longer pulls optional
	// chunkers transitively (ADR-023 binary-size seam), so a bench
	// that wants them must ask.
	_ "github.com/townsendmerino/aikit/chunk/regex"
	_ "github.com/townsendmerino/aikit/chunk/treesitter"

	"github.com/townsendmerino/aikit/chunk"
	"github.com/townsendmerino/ken/bench/internal/provenance"
	"github.com/townsendmerino/ken/internal/repo"
)

func TestTraceability_SembleCorpus(t *testing.T) {
	corpusRoot := os.Getenv("KEN_SEMBLE_CORPUS_ROOT")
	if corpusRoot == "" {
		corpusRoot = filepath.Join(os.Getenv("HOME"), ".cache", "semble-bench")
	}
	entries, err := os.ReadDir(corpusRoot)
	if err != nil {
		t.Skipf("missing corpus root %s — run semble's benchmarks/sync_repos.py first", corpusRoot)
	}

	chunkers := []string{"regex", "treesitter"}
	totals := map[string]*Totals{}
	byLang := map[string]map[string]*Totals{} // chunker -> language -> totals
	for _, name := range chunkers {
		totals[name] = &Totals{Chunker: name}
		byLang[name] = map[string]*Totals{}
	}

	// Optional repo cap for a fast first look; 0 = the whole corpus.
	limit := 0
	if v := os.Getenv("KEN_TRACE_REPO_LIMIT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			t.Fatalf("invalid KEN_TRACE_REPO_LIMIT=%q", v)
		}
		limit = n
	}

	// Progress goes to stderr, not t.Logf: the testing package buffers
	// t.Log until the test returns, which makes a 20-minute corpus
	// sweep completely silent and indistinguishable from a hang.
	start := time.Now()
	repos := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if limit > 0 && repos >= limit {
			break
		}
		root := filepath.Join(corpusRoot, e.Name())
		files, werr := repo.WalkFS(os.DirFS(root), repo.Options{})
		if werr != nil {
			t.Logf("[%s] skipped — walk: %v", e.Name(), werr)
			continue
		}
		repos++
		for _, rel := range files {
			data, rerr := os.ReadFile(filepath.Join(root, rel))
			if rerr != nil {
				continue
			}
			// Analyze under EVERY chunker or none: a file counted for
			// regex but skipped for treesitter (or vice versa) would
			// make the two columns describe different corpora.
			reports := make([]FileReport, 0, len(chunkers))
			for _, name := range chunkers {
				rep, ok := Analyze(name, rel, data)
				if !ok {
					reports = nil
					break
				}
				reports = append(reports, rep)
			}
			for _, rep := range reports {
				totals[rep.Chunker].Add(rep)
				lang := rep.Language
				if byLang[rep.Chunker][lang] == nil {
					byLang[rep.Chunker][lang] = &Totals{Chunker: rep.Chunker}
				}
				byLang[rep.Chunker][lang].Add(rep)
			}
		}
		fmt.Fprintf(os.Stderr, "[%d] %-24s files=%d defs=%d (%.0fs elapsed)\n",
			repos, e.Name(), totals["regex"].Files, totals["regex"].LeafDefs, time.Since(start).Seconds())
	}

	if totals["regex"].Files == 0 {
		t.Skip("no analyzable files found — is the corpus populated?")
	}

	var sb strings.Builder
	sb.WriteString("\n\ntraceability over " + itoa(repos) + " repos\n\n")
	sb.WriteString("| chunker | files | chunks | defs | split | split rate | mixed | mixed rate |\n")
	sb.WriteString("|---|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, name := range chunkers {
		tt := totals[name]
		sb.WriteString(sprintRow(tt))
	}
	sb.WriteString("\nby language (split rate, lower is better)\n\n")
	sb.WriteString("| language | files | regex split | treesitter split | Δ | regex mixed | treesitter mixed |\n")
	sb.WriteString("|---|---:|---:|---:|---:|---:|---:|\n")
	langs := make([]string, 0, len(byLang["regex"]))
	for lang := range byLang["regex"] {
		langs = append(langs, lang)
	}
	sort.Strings(langs)
	for _, lang := range langs {
		r, ts := byLang["regex"][lang], byLang["treesitter"][lang]
		if r == nil || ts == nil || r.LeafDefs < 50 {
			continue // too few definitions to read anything into
		}
		sb.WriteString(sprintLangRow(lang, r, ts))
	}
	t.Log(sb.String())

	out := map[string]any{
		"provenance": provenance.Collect(provenance.Options{
			Harness: "bench/chunkdiff/TestTraceability_SembleCorpus",
			Corpora: []provenance.Corpus{provenance.Detect("semble-bench", corpusRoot)},
			Mode:    "n/a (chunking only)",
			Chunker: strings.Join(chunkers, "+"),
			Extra:   map[string]string{"repos": itoa(repos)},
		}),
		"totals":      totals,
		"by_language": byLang,
	}
	if err := writeJSON(filepath.Join("results", "traceability.json"), out); err != nil {
		t.Errorf("write results: %v", err)
	}
}

// --- the classifier's own tests: synthetic files with known geometry ---

func TestAnalyze_SplitAndMixedAreCountedAsDefined(t *testing.T) {
	// Two top-level Python functions, the second one long. With a tiny
	// chunk size the long one cannot fit in a single chunk.
	var b strings.Builder
	b.WriteString("def alpha():\n    return 1\n\n")
	b.WriteString("def beta():\n")
	for i := 0; i < 400; i++ {
		b.WriteString("    x = " + itoa(i) + "\n")
	}
	src := []byte(b.String())

	rep, ok := Analyze("line", "sample.py", src)
	if !ok {
		t.Fatal("Analyze returned ok=false on a file with two definitions")
	}
	if rep.LeafDefs != 2 {
		t.Fatalf("leaf defs = %d, want 2", rep.LeafDefs)
	}
	if rep.SplitDefs == 0 {
		t.Errorf("a 400-line function fit in one chunk? split=%d chunks=%d", rep.SplitDefs, rep.Chunks)
	}
}

func TestAnalyze_WholeClassIsNotMixed(t *testing.T) {
	// The asymmetry that matters: a chunk holding one class with three
	// methods maps to ONE symbol. Counting its method starts as mixing
	// would penalize the outcome the metric is supposed to reward.
	src := []byte(`class Widget:
    def a(self):
        return 1

    def b(self):
        return 2

    def c(self):
        return 3
`)
	rep, ok := Analyze("regex", "widget.py", src)
	if !ok {
		t.Fatal("Analyze returned ok=false")
	}
	if rep.TopLevelDefs != 1 {
		t.Errorf("top-level defs = %d, want 1 (the class; its methods are leaves)", rep.TopLevelDefs)
	}
	if rep.MixedChunks != 0 {
		t.Errorf("mixed chunks = %d, want 0 — a whole class is one symbol", rep.MixedChunks)
	}
	if rep.LeafDefs != 3 {
		t.Errorf("leaf defs = %d, want 3 methods", rep.LeafDefs)
	}
}

func TestAnalyze_SkipsFilesWithNothingToTrace(t *testing.T) {
	// No registered extractor: nothing to be traceable to. Counting it
	// as perfectly traceable would dilute every rate with files that
	// never had a symbol in them.
	if _, ok := Analyze("regex", "notes.txt", []byte("just prose\n")); ok {
		t.Error("a file with no extractor should be skipped, not scored")
	}
	if _, ok := Analyze("regex", "empty.py", []byte("# only a comment\n")); ok {
		t.Error("a file with no definitions should be skipped, not scored")
	}
}

func TestContainedInSome_HonorsOverlappingWindows(t *testing.T) {
	// The line chunker's overlap exists so a definition straddling one
	// boundary still appears whole in a neighbour. Scoring it split
	// would punish the chunker for the feature that fixes the defect.
	chunks := []chunk.Chunk{
		{StartLine: 1, EndLine: 50},
		{StartLine: 46, EndLine: 95},
	}
	if !containedInSome(Span{48, 60}, chunks) {
		t.Error("a definition whole inside the SECOND window was scored as split")
	}
	if containedInSome(Span{40, 60}, chunks) {
		t.Error("a definition no single window covers was scored as contained")
	}
}

func TestDefinitionSpans_DropsUnrecordedSpans(t *testing.T) {
	// Zero spans mean "the extractor didn't capture this", per
	// FuncDef's contract. Treating a zero span as a real range would
	// manufacture splits out of missing data.
	src := []byte("def real():\n    return 1\n")
	rep, ok := Analyze("regex", "x.py", src)
	if !ok {
		t.Fatal("Analyze returned ok=false")
	}
	if rep.LeafDefs != 1 || rep.SplitDefs != 0 {
		t.Errorf("got leaf=%d split=%d, want 1 and 0", rep.LeafDefs, rep.SplitDefs)
	}
}

// --- small helpers (no fmt in the hot loop above) ---

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
