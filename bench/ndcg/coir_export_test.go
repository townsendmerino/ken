//go:build bench

// CoIR stage-1 shortlist exporter for the M0 rerank-ceiling experiment
// (outputs/ken-rerank-plan.md §12). Separate from coir_test.go because
// CoIR queries are multi-line Python function sources — they cannot be
// streamed to `ken bench` one-per-line, so the shortlist must be exported
// in-process with the exact same query text the published NDCG used.
//
// This is harness glue, NOT the transformer port: it only runs ken's
// existing hybrid pipeline and dumps the top-N candidate chunks (with
// their text) so scripts/m0_ceiling.py can rerank them with the real
// CodeRankEmbed model and measure the NDCG@10 lift + recall ceiling.
//
//	KEN_COIR_EXPORT=1 \
//	KEN_COIR_QUERY_LIMIT=1000 KEN_COIR_MAX_CAND=100 \
//	go test -tags=bench ./bench/ndcg/ -run TestCoIR_ExportShortlist -v -timeout 60m
//
// Writes testdata/bench/coir-csn-python/shortlist.jsonl (gitignored):
//
//	{"query_id":"q1","query_text":"...","candidates":[{"doc_id":"c1","content":"..."}]}

package ndcg

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/townsendmerino/ken/internal/search"
)

type exportCandidate struct {
	DocID   string `json:"doc_id"`
	Content string `json:"content"`
}

type exportRow struct {
	QueryID    string            `json:"query_id"`
	QueryText  string            `json:"query_text"`
	Candidates []exportCandidate `json:"candidates"`
}

func TestCoIR_ExportShortlist(t *testing.T) {
	if os.Getenv("KEN_COIR_EXPORT") != "1" {
		t.Skip("set KEN_COIR_EXPORT=1 to export the rerank shortlist")
	}
	benchDir, corpusDir, queriesPath, qrelsPath := benchPaths()
	for _, p := range []string{corpusDir, queriesPath, qrelsPath} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("missing %s — run scripts/bench_coir.py first", p)
		}
	}

	maxCand := candidatesPerQuery
	if v := os.Getenv("KEN_COIR_MAX_CAND"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			t.Fatalf("invalid KEN_COIR_MAX_CAND=%q", v)
		}
		maxCand = n
	}

	queries := loadQueries(t, queriesPath)
	qrels := loadQrels(t, qrelsPath)

	keep := make([]queryRow, 0, len(queries))
	for _, q := range queries {
		if len(qrels[q.QueryID]) > 0 {
			keep = append(keep, q)
		}
	}
	sort.Slice(keep, func(i, j int) bool { return keep[i].QueryID < keep[j].QueryID })
	if v := os.Getenv("KEN_COIR_QUERY_LIMIT"); v != "" {
		lim, err := strconv.Atoi(v)
		if err != nil || lim <= 0 {
			t.Fatalf("invalid KEN_COIR_QUERY_LIMIT=%q", v)
		}
		if lim < len(keep) {
			keep = keep[:lim]
		}
	}
	t.Logf("exporting top-%d shortlist for %d queries", maxCand, len(keep))

	modelDir := os.Getenv("KEN_MODEL_DIR")
	if modelDir == "" {
		modelDir = filepath.Join(os.Getenv("HOME"), ".ken", "model")
	}
	if _, err := os.Stat(filepath.Join(modelDir, "model.safetensors")); err != nil {
		t.Fatalf("hybrid export needs a model at %s: %v", modelDir, err)
	}

	tBuild := time.Now()
	ix, err := search.FromPath(corpusDir, search.ModeHybrid, "regex", modelDir)
	if err != nil {
		t.Fatalf("FromPath: %v", err)
	}
	t.Logf("indexed %d chunks in %.1fs", ix.Len(), time.Since(tBuild).Seconds())

	outPath := filepath.Join(benchDir, "shortlist.jsonl")
	f, err := os.Create(outPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := bufio.NewWriterSize(f, 1<<20)
	defer w.Flush()
	enc := json.NewEncoder(w)

	tQuery := time.Now()
	for _, q := range keep {
		hits := ix.Search(q.Text, maxCand)
		cands := make([]exportCandidate, 0, len(hits))
		for _, h := range hits {
			cands = append(cands, exportCandidate{
				DocID:   docIDFromFile(h.Chunk.File),
				Content: h.Chunk.Text,
			})
		}
		if err := enc.Encode(exportRow{QueryID: q.QueryID, QueryText: q.Text, Candidates: cands}); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
	t.Logf("wrote %s (%d queries) in %.1fs", outPath, len(keep), time.Since(tQuery).Seconds())
}

// docIDFromFile mirrors coir_test.go::aggregateByDoc so the export joins
// against qrels.jsonl exactly as the published-NDCG harness does.
func docIDFromFile(file string) string {
	return strings.TrimSuffix(filepath.Base(file), ".py")
}
