//go:build bench

// CoIR-CSN-Python token-budget harness. Build-tag gated; needs the
// same downloaded corpus the bench/ndcg CoIR harness uses (run
// scripts/bench_coir.py first; see docs/BENCH.md).
//
//	go test -tags=bench ./bench/tokens/ -run TestTokens_CoIR -v -timeout 60m
//	KEN_COIR_QUERY_LIMIT=200 go test -tags=bench ./bench/tokens/ -run TestTokens_CoIR -v
//
// Output: bench/tokens/results/coir-tokens.json (one record per query).
//
// Why subsample: the grep baseline pre-tokenizes ~280k corpus files
// once (~5 min on an M-series Mac) then per-query iterates that cache
// substring-scanning every file. With 1000 queries that's ~50 min;
// KEN_COIR_QUERY_LIMIT=200 keeps the full run under 15 min.

package tokens

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

	"github.com/townsendmerino/ken/internal/search"
)

const (
	coirBenchDir    = "../../testdata/bench/coir-csn-python"
	coirCorpusDir   = coirBenchDir + "/corpus"
	coirQueriesPath = coirBenchDir + "/queries.jsonl"
	coirQrelsPath   = coirBenchDir + "/qrels.jsonl"

	coirHeader = "Search results for: "
)

type coirQuery struct {
	QueryID string `json:"query_id"`
	Text    string `json:"text"`
}

type coirQrel struct {
	QueryID string  `json:"query_id"`
	DocID   string  `json:"doc_id"`
	Score   float64 `json:"score"`
}

// PerQueryRecord is what each harness writes per query — schema kept
// stable across coir + semble so scripts/plot_token_budget.py can
// merge them.
type PerQueryRecord struct {
	Repo          string      `json:"repo"`
	Query         string      `json:"query"`
	QueryClass    QueryClass  `json:"query_class"`
	QrelTargets   []string    `json:"qrel_targets"`
	Ken           []KenAtK    `json:"ken"`
	GrepTokenized GrepResult  `json:"grep_tokenized"`
	GrepLiteral   *GrepResult `json:"grep_literal,omitempty"` // symbol queries only
}

func TestTokens_CoIR(t *testing.T) {
	for _, p := range []string{coirCorpusDir, coirQueriesPath, coirQrelsPath} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("missing %s — run scripts/bench_coir.py first (see docs/BENCH.md)", p)
		}
	}

	t.Log("loading queries + qrels...")
	queries := loadCoirQueries(t, coirQueriesPath)
	qrelsByQuery := loadCoirQrels(t, coirQrelsPath)
	t.Logf("  %d queries, %d qrel groups", len(queries), len(qrelsByQuery))

	// Drop queries with no positive judgments (same convention as the
	// ndcg harness — they'd contribute zero signal).
	keep := make([]coirQuery, 0, len(queries))
	for _, q := range queries {
		if len(qrelsByQuery[q.QueryID]) > 0 {
			keep = append(keep, q)
		}
	}
	if len(keep) == 0 {
		t.Fatal("no queries have positive qrels; check downloader output")
	}

	// Optional deterministic subsample. Default behavior: small (200)
	// so the bench finishes in ~15 min. Set KEN_COIR_QUERY_LIMIT=0 to
	// run the full set (~50 min).
	limStr := os.Getenv("KEN_COIR_QUERY_LIMIT")
	lim := 200
	if limStr != "" {
		n, err := strconv.Atoi(limStr)
		if err != nil {
			t.Fatalf("invalid KEN_COIR_QUERY_LIMIT=%q", limStr)
		}
		lim = n
	}
	sort.Slice(keep, func(i, j int) bool { return keep[i].QueryID < keep[j].QueryID })
	if lim > 0 && lim < len(keep) {
		keep = keep[:lim]
	}
	t.Logf("  evaluating %d queries (subsample: KEN_COIR_QUERY_LIMIT=%v)", len(keep), limStr)

	t.Log("building corpus cache (pre-tokenizing every file once)...")
	tBuild := time.Now()
	corpus, err := NewCorpusCache(coirCorpusDir)
	if err != nil {
		t.Fatalf("NewCorpusCache: %v", err)
	}
	t.Logf("  cached %d files in %.1fs", corpus.Len(), time.Since(tBuild).Seconds())

	t.Log("building ken index (hybrid would need model; bm25 doesn't)...")
	tIndex := time.Now()
	ix, err := search.FromPath(coirCorpusDir, search.ModeBM25, "regex", "")
	if err != nil {
		t.Fatalf("FromPath: %v", err)
	}
	t.Logf("  indexed %d chunks in %.1fs", ix.Len(), time.Since(tIndex).Seconds())

	records := make([]PerQueryRecord, 0, len(keep))
	tEval := time.Now()
	for i, q := range keep {
		// Build target file list from qrels. CoIR doc IDs were
		// materialized as `<doc_id>.py` files; recover the file rel-
		// path. Same convention as bench/ndcg/coir_test.go.
		targets := make([]string, 0, len(qrelsByQuery[q.QueryID]))
		for docID := range qrelsByQuery[q.QueryID] {
			targets = append(targets, docID+".py")
		}

		cls := ClassifyQuery(q.Text)
		rec := PerQueryRecord{
			Repo:          "coir-csn-python",
			Query:         q.Text,
			QueryClass:    cls,
			QrelTargets:   targets,
			Ken:           MeasureKen(ix, q.Text, coirHeader+q.Text, targets),
			GrepTokenized: corpus.MeasureGrepTokenized(q.Text, targets),
		}
		if cls == ClassSymbol {
			grepLit := corpus.MeasureGrepLiteral(q.Text, targets)
			rec.GrepLiteral = &grepLit
		}
		records = append(records, rec)

		if (i+1)%50 == 0 {
			t.Logf("  %d/%d queries processed (%.1fs elapsed)", i+1, len(keep), time.Since(tEval).Seconds())
		}
	}

	t.Logf("eval done in %.1fs total", time.Since(tEval).Seconds())

	// Write JSONL output.
	outDir := "results"
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(outDir, "coir-tokens.json")
	if err := writeRecords(outPath, records); err != nil {
		t.Fatalf("write results: %v", err)
	}
	t.Logf("wrote %d records to %s", len(records), outPath)

	// Print aggregate summary to test log.
	printAggregate(t, records, "CoIR-CSN-Python (regex chunker, subsample="+strconv.Itoa(len(keep))+")")
}

// --- helpers ---

func loadCoirQueries(t *testing.T, path string) []coirQuery {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var out []coirQuery
	for sc.Scan() {
		var q coirQuery
		if err := json.Unmarshal(sc.Bytes(), &q); err != nil {
			t.Fatal(err)
		}
		out = append(out, q)
	}
	return out
}

func loadCoirQrels(t *testing.T, path string) map[string]map[string]float64 {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	out := map[string]map[string]float64{}
	for sc.Scan() {
		var r coirQrel
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
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
	return out
}

func writeRecords(path string, recs []PerQueryRecord) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(recs)
}

// printAggregate emits a per-query-class summary table to the test log
// — same shape as the BENCH.md target table.
func printAggregate(t *testing.T, recs []PerQueryRecord, title string) {
	t.Helper()
	t.Logf("\n\n%s — %d records\n", title, len(recs))

	var sb strings.Builder
	sb.WriteString("| Class  | K  | ken med tokens | ken recall@K | grep med tokens | grep recall |\n")
	sb.WriteString("|--------|----|---------------:|-------------:|----------------:|------------:|\n")

	for _, cls := range []QueryClass{ClassSymbol, ClassNL} {
		filtered := filterByClass(recs, cls)
		if len(filtered) == 0 {
			continue
		}
		// Pick grep variant: literal for symbol queries (it's the realistic
		// best case for grep on symbol lookups), tokenized for NL.
		grepLabel := "tokenized"
		grepTokens, grepRecalls := getGrepStats(filtered, cls == ClassSymbol)
		if cls == ClassSymbol {
			grepLabel = "literal"
		}
		_ = grepLabel
		gMed := median(grepTokens)
		gRec := mean(grepRecalls)

		for _, k := range Ks {
			toks, recs := getKenStats(filtered, k)
			fmt.Fprintf(&sb, "| %-6s | %2d | %14d | %12.3f | %15d | %11.3f |\n",
				cls, k, median(toks), mean(recs), gMed, gRec)
		}
	}
	t.Log(sb.String())
}

func filterByClass(recs []PerQueryRecord, cls QueryClass) []PerQueryRecord {
	out := []PerQueryRecord{}
	for _, r := range recs {
		if r.QueryClass == cls {
			out = append(out, r)
		}
	}
	return out
}

func getKenStats(recs []PerQueryRecord, k int) (tokens []int, recalls []float64) {
	for _, r := range recs {
		for _, ka := range r.Ken {
			if ka.K == k {
				tokens = append(tokens, ka.Tokens)
				recalls = append(recalls, boolFloat(ka.Recall))
			}
		}
	}
	return
}

func getGrepStats(recs []PerQueryRecord, useLiteral bool) (tokens []int, recalls []float64) {
	for _, r := range recs {
		g := &r.GrepTokenized
		if useLiteral && r.GrepLiteral != nil {
			g = r.GrepLiteral
		}
		tokens = append(tokens, g.Tokens)
		recalls = append(recalls, boolFloat(g.Recall))
	}
	return
}

func median(xs []int) int {
	if len(xs) == 0 {
		return 0
	}
	cp := append([]int(nil), xs...)
	sort.Ints(cp)
	return cp[len(cp)/2]
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var sum float64
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}

func boolFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
