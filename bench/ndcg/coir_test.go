//go:build bench

// CoIR-CSN-Python harness. Build-tag gated so a normal `go test ./...`
// never tries to run it: needs ~1 GB of materialized corpus on disk,
// the Model2Vec model under ~/.ken/model for semantic / hybrid, and
// takes minutes per mode.
//
// Reproduce from scratch:
//
//	python scripts/bench_coir.py                                # ~5 min, ~1 GB on disk
//	go test -tags=bench ./bench/ndcg/ -run TestCoIR -v          # ~15–25 min
//
// Outputs a markdown table to stderr (test log) with bm25 / semantic /
// hybrid NDCG@10. The default chunker (regex) is what the published
// number uses; treesitter is an additional optional row when
// KEN_CHUNKER_TREESITTER=1 is set.
//
// Pass-by-doc-id mapping: ken's chunker emits one or more chunks per
// .py file. The harness aggregates retrieved chunks back to their
// source file (the doc_id == filename), keeping the best-ranked chunk
// as the file's rank. This is the standard "passage → document"
// aggregation BEIR-style benchmarks use when retrieval is sub-document.

package ndcg

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	// Side-effect import: register the treesitter chunker so this
	// bench can exercise it. As of v0.6.0 internal/search no longer
	// transitively pulls in optional chunkers.
	_ "github.com/townsendmerino/ken/chunk/treesitter"
	"github.com/townsendmerino/ken/internal/search"
)

const (
	benchDir    = "../../testdata/bench/coir-csn-python"
	corpusDir   = benchDir + "/corpus"
	queriesPath = benchDir + "/queries.jsonl"
	qrelsPath   = benchDir + "/qrels.jsonl"

	// Over-fetch a healthy number of chunks per query so the
	// passage→doc aggregation has room: even after dedup-by-file,
	// we want ≥10 distinct files to compute NDCG@10. Chunks beyond
	// this don't matter (a 10th-best file would already need a top-50
	// retrieval if every file is 1 chunk).
	candidatesPerQuery = 100
)

type queryRow struct {
	QueryID string `json:"query_id"`
	Text    string `json:"text"`
}

type qrelRow struct {
	QueryID string  `json:"query_id"`
	DocID   string  `json:"doc_id"`
	Score   float64 `json:"score"`
}

func TestCoIR_CSNPython(t *testing.T) {
	mustExist := []string{corpusDir, queriesPath, qrelsPath}
	for _, p := range mustExist {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("missing %s — run scripts/bench_coir.py first (see docs/BENCH.md)", p)
		}
	}

	t.Log("loading queries + qrels...")
	queries := loadQueries(t, queriesPath)
	qrelsByQuery := loadQrels(t, qrelsPath)
	t.Logf("  %d queries, %d qrel-groups", len(queries), len(qrelsByQuery))

	// Drop queries with no positive judgments; NDCG is 0 for them and
	// they'd dilute the average without providing signal.
	keep := make([]queryRow, 0, len(queries))
	for _, q := range queries {
		rels := qrelsByQuery[q.QueryID]
		if len(rels) == 0 {
			continue
		}
		keep = append(keep, q)
	}
	if len(keep) == 0 {
		t.Fatal("no queries have positive qrels; check downloader output")
	}
	t.Logf("  evaluating %d queries that have ≥1 positive qrel", len(keep))

	// Optional deterministic subsample. Without it, a full run on the
	// 14.9k test set + 280k corpus takes ~80 min on an M-series Mac
	// (bm25 alone is 23 min; semantic ~30 min; hybrid ~50 min). For the
	// public-benchmark headline number that goes in the README, a 1000-
	// query subsample is more than enough — the standard error on N=1000
	// is well under 0.005. The subsample is deterministic (sorted by
	// query_id, take the first N) so reruns are bit-identical.
	if limStr := os.Getenv("KEN_COIR_QUERY_LIMIT"); limStr != "" {
		lim, err := strconv.Atoi(limStr)
		if err != nil || lim <= 0 {
			t.Fatalf("invalid KEN_COIR_QUERY_LIMIT=%q", limStr)
		}
		sort.Slice(keep, func(i, j int) bool { return keep[i].QueryID < keep[j].QueryID })
		if lim < len(keep) {
			keep = keep[:lim]
			t.Logf("  subsampled to %d queries (KEN_COIR_QUERY_LIMIT)", len(keep))
		}
	}

	modelDir := os.Getenv("KEN_MODEL_DIR")
	if modelDir == "" {
		modelDir = filepath.Join(os.Getenv("HOME"), ".ken", "model")
	}
	if _, err := os.Stat(filepath.Join(modelDir, "model.safetensors")); err != nil {
		t.Logf("no model at %s — semantic + hybrid will be skipped (bm25 still runs)", modelDir)
	}

	chunker := "regex"
	if os.Getenv("KEN_CHUNKER_TREESITTER") == "1" {
		chunker = "treesitter"
	}
	t.Logf("chunker: %s", chunker)

	type result struct {
		Mode      string
		Avg       float64
		WallSec   float64
		NumChunks int
	}
	var results []result

	for _, mode := range []search.Mode{search.ModeBM25, search.ModeSemantic, search.ModeHybrid} {
		modeStr := modeString(mode)
		if mode != search.ModeBM25 {
			if _, err := os.Stat(filepath.Join(modelDir, "model.safetensors")); err != nil {
				t.Logf("[%s] skipped — no model available", modeStr)
				continue
			}
		}

		t.Logf("[%s] building index over %s...", modeStr, corpusDir)
		tBuild := time.Now()
		ix, err := search.FromPath(corpusDir, mode, chunker, modelDir)
		if err != nil {
			t.Fatalf("[%s] FromPath: %v", modeStr, err)
		}
		t.Logf("[%s] indexed %d chunks in %.1fs", modeStr, ix.Len(), time.Since(tBuild).Seconds())

		tQuery := time.Now()
		perQuery := make([]float64, 0, len(keep))
		for _, q := range keep {
			hits := ix.Search(q.Text, candidatesPerQuery)
			ranked := aggregateByDoc(hits)
			perQuery = append(perQuery, AtK(ranked, qrelsByQuery[q.QueryID], 10))
		}
		avg := Average(perQuery)
		results = append(results, result{
			Mode:      modeStr,
			Avg:       avg,
			WallSec:   time.Since(tBuild).Seconds(),
			NumChunks: ix.Len(),
		})
		t.Logf("[%s] NDCG@10 = %.4f (queries took %.1fs, total mode wall %.1fs)",
			modeStr, avg, time.Since(tQuery).Seconds(), time.Since(tBuild).Seconds())
	}

	// Markdown table to stderr so the harness output is grep'able for
	// inclusion in BENCH.md / README. Headlines first.
	var sb strings.Builder
	sb.WriteString("\n\nCoIR-CSN-Python (chunker=" + chunker + ")\n")
	sb.WriteString("\n| Mode                | NDCG@10 | Index wall (s) |\n")
	sb.WriteString("|---------------------|--------:|---------------:|\n")
	for _, r := range results {
		sb.WriteString(fmt.Sprintf("| %-19s | %7.4f | %14.1f |\n", r.Mode, r.Avg, r.WallSec))
	}
	t.Log(sb.String())
}

// aggregateByDoc converts a chunk-level ranking into a doc-level
// ranking by taking the highest-ranked chunk per source file. This
// matches the standard BEIR convention for passage-level retrievers
// evaluated against document-level qrels.
//
// The doc_id is derived from the chunk's File field by stripping the
// trailing .py extension (the corpus materializer in bench_coir.py
// writes corpus/<doc_id>.py, so this is the inverse).
func aggregateByDoc(hits []search.Result) []string {
	seen := make(map[string]struct{}, len(hits))
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		docID := strings.TrimSuffix(filepath.Base(h.Chunk.File), ".py")
		if _, dup := seen[docID]; dup {
			continue
		}
		seen[docID] = struct{}{}
		out = append(out, docID)
	}
	return out
}

func modeString(m search.Mode) string {
	switch m {
	case search.ModeBM25:
		return "bm25"
	case search.ModeSemantic:
		return "semantic"
	case search.ModeHybrid:
		return "hybrid"
	}
	return "?"
}

func loadQueries(t *testing.T, path string) []queryRow {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024) // some queries are long
	var out []queryRow
	for scanner.Scan() {
		var q queryRow
		if err := json.Unmarshal(scanner.Bytes(), &q); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		out = append(out, q)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// loadQrels returns query_id → (doc_id → score), the shape AtK wants.
func loadQrels(t *testing.T, path string) map[string]map[string]float64 {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	out := make(map[string]map[string]float64)
	for scanner.Scan() {
		var r qrelRow
		if err := json.Unmarshal(scanner.Bytes(), &r); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		if r.Score <= 0 {
			continue
		}
		m, ok := out[r.QueryID]
		if !ok {
			m = make(map[string]float64)
			out[r.QueryID] = m
		}
		m[r.DocID] = r.Score
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
