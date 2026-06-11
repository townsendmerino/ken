//go:build bench

// qgraph_test.go — Stage 8 follow-up: query-time graph expansion
// experiment.
//
// Hypothesis: take the hybrid retriever's top-K → walk the structural
// call graph one hop (callers + callee-definitions) → augment the
// candidate set → re-rank. The expansion is sparse (only from a small
// frontier, not from every chunk), so the flooding mechanism that
// killed Track 1's index-time enrichment shouldn't apply here.
//
// Reuses the same KEN_BENCH_DIR contract as TestCoIR_CSNPython. Runs
// hybrid baseline AND graph-expanded variants side-by-side and reports
// the NDCG@10 delta per setting.
//
// Gated on the bench tag; needs ~/.ken/model + the bench corpus at
// the dir KEN_BENCH_DIR points at (defaults to coir-csn-python).
//
// Run on the CoSQA dev set we built for Stage 8 Gate 1:
//
//	KEN_BENCH_DIR=$PWD/testdata/bench/cosqa-python \
//	  go test -tags=bench -run TestQGraphExpansion -v -timeout 600s ./bench/ndcg/
package ndcg

import (
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
	"time"

	_ "github.com/townsendmerino/aikit/chunk/treesitter"
	"github.com/townsendmerino/ken/internal/search"
	"github.com/townsendmerino/ken/internal/structural"
)

func TestQGraphExpansion(t *testing.T) {
	_, corpusDir, queriesPath, qrelsPath := benchPaths()
	for _, p := range []string{corpusDir, queriesPath, qrelsPath} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("missing %s — see TestCoIR_CSNPython docs", p)
		}
	}

	queries := loadQueries(t, queriesPath)
	qrelsByQuery := loadQrels(t, qrelsPath) // map[qid]map[docid]score
	keep := make([]queryRow, 0, len(queries))
	for _, q := range queries {
		if len(qrelsByQuery[q.QueryID]) > 0 {
			keep = append(keep, q)
		}
	}
	sort.Slice(keep, func(i, j int) bool { return keep[i].QueryID < keep[j].QueryID })
	if v := os.Getenv("KEN_QGRAPH_LIMIT"); v != "" {
		n, err := strconv.Atoi(v)
		if err == nil && n > 0 && n < len(keep) {
			keep = keep[:n]
		}
	}
	t.Logf("evaluating %d queries", len(keep))

	modelDir := os.Getenv("KEN_MODEL_DIR")
	if modelDir == "" {
		modelDir = filepath.Join(os.Getenv("HOME"), ".ken", "model")
	}
	if _, err := os.Stat(filepath.Join(modelDir, "model.safetensors")); err != nil {
		t.Fatalf("hybrid mode needs a model at %s: %v", modelDir, err)
	}

	chunker := "regex"

	t.Logf("building search index over %s...", corpusDir)
	tBuild := time.Now()
	ix, err := search.FromPath(corpusDir, search.ModeHybrid, chunker, modelDir)
	if err != nil {
		t.Fatalf("search.FromPath: %v", err)
	}
	t.Logf("  indexed in %.1fs", time.Since(tBuild).Seconds())

	t.Log("building structural index...")
	tStruct := time.Now()
	sx, err := structural.Build(corpusDir)
	if err != nil {
		t.Fatalf("structural.Build: %v", err)
	}
	t.Logf("  structural built in %.1fs", time.Since(tStruct).Seconds())

	// Hyper-parameters for the expansion. Picked conservatively; can
	// be overridden for sweep runs.
	overFetch := envInt("KEN_QGRAPH_K", candidatesPerQuery) // baseline over-fetch (per-chunk before file-dedup)
	frontierK := envInt("KEN_QGRAPH_FRONTIER", 3)           // how many top-K hits to expand from
	boostScale := envFloat("KEN_QGRAPH_BOOST", 0.05)        // additive score boost per graph-link hit

	t.Logf("hyperparams: K=%d frontier=%d boost=%.3f", overFetch, frontierK, boostScale)

	// Baseline hybrid pass.
	var baseSum, expSum float64
	var baseQueryWall, expQueryWall time.Duration
	movedUp, movedDown, unchanged := 0, 0, 0

	for _, q := range keep {
		t0 := time.Now()
		rawResults := ix.Search(q.Text, overFetch)
		// File-dedupe: take the best score per file.
		baseRanked := dedupeByFile(rawResults)
		baseQueryWall += time.Since(t0)
		baseNDCG := ndcgAtK(baseRanked, qrelsByQuery[q.QueryID], 10)
		baseSum += baseNDCG

		t1 := time.Now()
		expRanked := graphExpand(baseRanked, ix, sx, frontierK, boostScale)
		expQueryWall += time.Since(t1) + time.Since(t0) - time.Since(t1)
		expNDCG := ndcgAtK(expRanked, qrelsByQuery[q.QueryID], 10)
		expSum += expNDCG

		switch {
		case expNDCG > baseNDCG+1e-9:
			movedUp++
		case expNDCG < baseNDCG-1e-9:
			movedDown++
		default:
			unchanged++
		}
	}

	baseAvg := baseSum / float64(len(keep))
	expAvg := expSum / float64(len(keep))

	t.Logf("\n=== Query-time graph expansion (KEN_BENCH_DIR=%s) ===", corpusDir)
	t.Logf("queries:         %d", len(keep))
	t.Logf("baseline NDCG@10: %.4f (mean over %d queries, %.1f ms/query)",
		baseAvg, len(keep), float64(baseQueryWall.Milliseconds())/float64(len(keep)))
	t.Logf("expanded NDCG@10: %.4f (graph walk %.1f ms/query overhead)",
		expAvg, float64((expQueryWall-baseQueryWall).Milliseconds())/float64(len(keep)))
	t.Logf("delta:            %+.4f", expAvg-baseAvg)
	t.Logf("per-query moves:  up=%d  down=%d  unchanged=%d", movedUp, movedDown, unchanged)
}

// rankedHit is a file-level scored entry returned by the file-dedup
// pass + augmented by graph expansion.
type rankedHit struct {
	File  string
	Score float64
}

func dedupeByFile(results []search.Result) []rankedHit {
	best := map[string]float64{}
	for _, r := range results {
		if s, ok := best[r.Chunk.File]; !ok || r.Score > s {
			best[r.Chunk.File] = r.Score
		}
	}
	out := make([]rankedHit, 0, len(best))
	for f, s := range best {
		out = append(out, rankedHit{File: f, Score: s})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}

// graphExpand walks one structural-graph hop from the top-frontierK
// baseline hits and merges any newly-touched files back into the
// candidate set with an additive boost proportional to the number of
// graph links surfacing each new candidate.
//
// Expansion is bidirectional and file-level — the only granularity
// the index exposes:
//
//   - For each function defined in a frontier file, add all files
//     that CALL that function (Callers).
//   - For each callee mentioned in a frontier file's fs.CalleeNames(),
//     add all files that DEFINE that callee (Defs).
//
// Sparse-by-construction: walked only from the top-K frontier, not
// from every chunk in the corpus. That's the structural difference
// vs M0e Track 1's index-time enrichment (which flooded by adding
// the called-by list to every chunk).
func graphExpand(base []rankedHit, _ *search.Index, sx *structural.Index, frontierK int, boostScale float64) []rankedHit {
	if frontierK > len(base) {
		frontierK = len(base)
	}
	inBase := map[string]float64{}
	for _, h := range base {
		inBase[h.File] = h.Score
	}
	// graphHits counts how many frontier-graph paths surface each
	// candidate file — the boost amount scales with this count.
	graphHits := map[string]int{}

	for i := 0; i < frontierK; i++ {
		fileStruct := sx.File(base[i].File)
		if fileStruct == nil {
			continue
		}
		// Outbound: callers of every function this file defines.
		for _, fn := range fileStruct.Functions {
			if fn.Name == "" {
				continue
			}
			for _, site := range sx.Callers(fn.Name) {
				if site.File == base[i].File {
					continue
				}
				graphHits[site.File]++
			}
		}
		// Inbound: definitions of every name this file calls.
		for _, callee := range fileStruct.CalleeNames() {
			for _, defFile := range sx.Defs(callee) {
				if defFile == base[i].File {
					continue
				}
				graphHits[defFile]++
			}
		}
	}

	// Merge into base. Existing files get their score boosted by
	// graphHits * boostScale; new files come in at boost-only score.
	combined := map[string]float64{}
	for f, s := range inBase {
		combined[f] = s + float64(graphHits[f])*boostScale
	}
	for f, hits := range graphHits {
		if _, ok := combined[f]; !ok {
			combined[f] = float64(hits) * boostScale
		}
	}

	out := make([]rankedHit, 0, len(combined))
	for f, s := range combined {
		out = append(out, rankedHit{File: f, Score: s})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}

func ndcgAtK(ranked []rankedHit, qrels map[string]float64, k int) float64 {
	if len(qrels) == 0 {
		return 0
	}
	// Bench files are stored as `<doc_id>.py` (CoSQA convention) or
	// `<doc_id>.<ext>` (csn). Build a basename → score map for fast
	// lookup, accepting both bare and `.py`-suffixed doc_id forms.
	gold := make(map[string]float64, len(qrels)*2)
	for docID, s := range qrels {
		gold[docID] = s
		gold[docID+".py"] = s
	}
	limit := k
	if limit > len(ranked) {
		limit = len(ranked)
	}

	var dcg float64
	for i := 0; i < limit; i++ {
		if rel, ok := gold[filepath.Base(ranked[i].File)]; ok {
			dcg += rel / log2Plus1(i+1)
		}
	}

	rels := make([]float64, 0, len(qrels))
	for _, s := range qrels {
		rels = append(rels, s)
	}
	sort.Slice(rels, func(i, j int) bool { return rels[i] > rels[j] })
	var ideal float64
	for i := 0; i < limit && i < len(rels); i++ {
		ideal += rels[i] / log2Plus1(i+1)
	}
	if ideal == 0 {
		return 0
	}
	return dcg / ideal
}

func log2Plus1(rank int) float64 {
	// rank is 1-indexed.
	return math.Log2(float64(rank) + 1)
}

func envInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envFloat(name string, def float64) float64 {
	if v := os.Getenv(name); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}
