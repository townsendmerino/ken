//go:build bench

// semble token-budget harness. Build-tag gated; needs:
//   1. The semble corpus synced via `python /tmp/semble/benchmarks/sync_repos.py`
//      (see docs/BENCH.md "Bootstrap the corpus" — same prereq the
//      bench/semble run_ken.py harness uses).
//   2. A semble checkout (default /tmp/semble; override with $SEMBLE_CHECKOUT)
//      for the annotations JSON files + repos.json.
//
//	go test -tags=bench ./bench/tokens/ -run TestTokens_Semble -v -timeout 30m
//
// Output: bench/tokens/results/semble-tokens.json.
//
// Per-repo memory: we hold one CorpusCache at a time and drop it
// before moving to the next repo. RSS stays bounded by the largest
// single repo's corpus footprint.

package tokens

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

	"github.com/townsendmerino/ken/internal/search"
)

const sembleHeader = "Search results for: "

type sembleRepo struct {
	Name          string `json:"name"`
	Language      string `json:"language"`
	URL           string `json:"url"`
	Revision      string `json:"revision"`
	BenchmarkRoot string `json:"benchmark_root"`
}

type sembleTask struct {
	Query     string   `json:"query"`
	Relevant  []string `json:"relevant"`
	Secondary []string `json:"secondary"`
	Category  string   `json:"category"`
}

func TestTokens_Semble(t *testing.T) {
	semblePath := os.Getenv("SEMBLE_CHECKOUT")
	if semblePath == "" {
		semblePath = "/tmp/semble"
	}
	reposPath := filepath.Join(semblePath, "benchmarks", "repos.json")
	if _, err := os.Stat(reposPath); err != nil {
		t.Skipf("missing %s — clone semble to /tmp/semble or set SEMBLE_CHECKOUT (see docs/BENCH.md)", reposPath)
	}

	corpusRoot := os.Getenv("KEN_SEMBLE_CORPUS_ROOT")
	if corpusRoot == "" {
		// Same default semble's sync_repos.py writes to.
		corpusRoot = filepath.Join(os.Getenv("HOME"), ".cache", "semble-bench")
	}
	if _, err := os.Stat(corpusRoot); err != nil {
		t.Skipf("missing corpus root %s — run `python %s/benchmarks/sync_repos.py` first", corpusRoot, semblePath)
	}

	// KEN_TOKENS_MODE selects the retrieval mode the token bench measures.
	// Default bm25 (no model needed — the historical token-budget table).
	// hybrid/semantic resolve a model dir (KEN_MODEL_DIR → ~/.ken/model)
	// so the median-token + recall numbers reflect the default product mode.
	tokMode, tokModelDir := search.ModeBM25, ""
	switch os.Getenv("KEN_TOKENS_MODE") {
	case "hybrid":
		tokMode = search.ModeHybrid
	case "semantic":
		tokMode = search.ModeSemantic
	}
	if tokMode != search.ModeBM25 {
		if tokModelDir = os.Getenv("KEN_MODEL_DIR"); tokModelDir == "" {
			tokModelDir = filepath.Join(os.Getenv("HOME"), ".ken", "model")
		}
		if _, serr := os.Stat(filepath.Join(tokModelDir, "model.safetensors")); serr != nil {
			t.Skipf("KEN_TOKENS_MODE=%s needs a model; none at %s", os.Getenv("KEN_TOKENS_MODE"), tokModelDir)
		}
	}

	// Load repos.json.
	repos, err := loadSembleRepos(reposPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("loaded %d repos (token mode=%v)", len(repos), tokMode)

	// Optional subsample: KEN_SEMBLE_REPO_LIMIT=N runs the first N
	// repos (alphabetic by name). Default is all.
	if lim, _ := strconv.Atoi(os.Getenv("KEN_SEMBLE_REPO_LIMIT")); lim > 0 && lim < len(repos) {
		sort.Slice(repos, func(i, j int) bool { return repos[i].Name < repos[j].Name })
		repos = repos[:lim]
		t.Logf("  subsampled to %d repos", len(repos))
	}

	records := make([]PerQueryRecord, 0, 1500)
	tStart := time.Now()

	for ri, repo := range repos {
		repoDir := filepath.Join(corpusRoot, repo.Name)
		if _, err := os.Stat(repoDir); err != nil {
			t.Logf("[%s] skipped — corpus dir missing (%s); did sync_repos.py run?", repo.Name, repoDir)
			continue
		}
		benchDir := repoDir
		if repo.BenchmarkRoot != "" {
			benchDir = filepath.Join(repoDir, filepath.FromSlash(repo.BenchmarkRoot))
		}

		annotPath := filepath.Join(semblePath, "benchmarks", "annotations", repo.Name+".json")
		tasks, err := loadSembleTasks(annotPath)
		if err != nil {
			t.Logf("[%s] skipped — annotations: %v", repo.Name, err)
			continue
		}

		tRepo := time.Now()
		corpus, err := NewCorpusCache(benchDir)
		if err != nil {
			t.Logf("[%s] skipped — corpus cache: %v", repo.Name, err)
			continue
		}

		ix, err := search.FromPath(benchDir, tokMode, "regex", tokModelDir)
		if err != nil {
			t.Logf("[%s] skipped — index build: %v", repo.Name, err)
			continue
		}

		for _, task := range tasks {
			targets := append([]string(nil), task.Relevant...)

			cls := ClassifyQuery(task.Query)
			rec := PerQueryRecord{
				Repo:          repo.Name,
				Query:         task.Query,
				QueryClass:    cls,
				QrelTargets:   targets,
				Ken:           MeasureKen(ix, task.Query, sembleHeader+task.Query, targets),
				GrepTokenized: corpus.MeasureGrepTokenized(task.Query, targets),
			}
			if cls == ClassSymbol {
				gl := corpus.MeasureGrepLiteral(task.Query, targets)
				rec.GrepLiteral = &gl
			}
			records = append(records, rec)
		}

		t.Logf("[%d/%d %s, %s, %d tasks] cached %d files, %d chunks (%.1fs)",
			ri+1, len(repos), repo.Name, repo.Language, len(tasks),
			corpus.Len(), ix.Len(), time.Since(tRepo).Seconds())
	}

	t.Logf("all repos done in %.1fs; %d total query records", time.Since(tStart).Seconds(), len(records))

	if len(records) == 0 {
		t.Fatal("no query records produced — check semble checkout + corpus root")
	}

	outDir := "results"
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(outDir, "semble-tokens.json")
	if err := writeRecords(outPath, records); err != nil {
		t.Fatalf("write results: %v", err)
	}
	t.Logf("wrote %d records to %s", len(records), outPath)

	printAggregate(t, records, fmt.Sprintf("semble bench (%d repos)", len(repos)))
}

func loadSembleRepos(path string) ([]sembleRepo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []sembleRepo
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return out, nil
}

func loadSembleTasks(path string) ([]sembleTask, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []sembleTask
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	// Drop tasks without any positive qrel target — file-level recall
	// is impossible to satisfy on those, and they'd skew aggregate
	// recall toward zero.
	keep := out[:0]
	for _, t := range out {
		if len(t.Relevant) > 0 || len(t.Secondary) > 0 {
			keep = append(keep, t)
		}
	}
	return keep, nil
}

// Silence the unused-strings-import warning if a future edit removes
// the strings.TrimSpace path. (No-op when strings is in use.)
var _ = strings.TrimSpace
