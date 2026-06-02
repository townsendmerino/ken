//go:build bench

// Stage-7a HyDE M0 ceiling experiment.
//
// Reads NL queries + qrels from the inverted CSN-Python bench
// (see scripts/bench_csn_nl.py) and HyDE snippets from
// scripts/m0_hyde.py, then runs the existing hybrid retriever with
// a *fused* query vector:
//
//	q' = normalize( w·potion(snippet) + (1−w)·potion(query) )
//
// and reports NDCG@10 + Recall@10 + Recall@100 across:
//
//   - w ∈ {0.0 baseline, 0.3, 0.5, 0.7}    (kickoff §M0)
//   - mode ∈ {ModeHybrid, ModeHybridRerank} (kickoff Q2 answer: both)
//
// Reproduce:
//
//	python scripts/bench_coir.py                   # one-time
//	python scripts/bench_csn_nl.py                 # one-time
//	ANTHROPIC_API_KEY=... python scripts/m0_hyde.py
//	go test -tags=bench ./bench/ndcg/ -run TestHyDE_M0 -v -timeout 90m
//
// Knobs (env):
//
//	KEN_HYDE_QUERY_LIMIT  evaluate the first N queries (sorted by id)
//	                      so the Sonnet bill and the bench wall stay
//	                      reasonable (default 1000, matching the
//	                      published CoIR subsample)
//	KEN_HYDE_MODEL        model id used in the cache filename
//	                      (default claude-sonnet-4-6); matches the
//	                      KEN_M0_HYDE_MODEL knob in scripts/m0_hyde.py
//	KEN_RERANK            "1" to also include the +rerank rows
//	                      (requires KEN_RERANK_MODEL_DIR or
//	                      ~/.ken/rerank-model to be populated)
//	KEN_RERANK_MODEL_DIR  CodeRankEmbed snapshot dir
//	KEN_RERANK_TOP_N      override rerankN (default 50; bench-friendly)
//	KEN_RERANK_BETA       override blend β (default 1.0 — same as
//	                      TestCoIR_CSNPython's published M0 config)
//	KEN_HYDE_WEIGHTS      comma-separated override for the w sweep
//	                      (default "0.0,0.3,0.5,0.7"). Use a 2-value
//	                      list like "0.0,0.7" for focused rerank-stack
//	                      experiments where each rerank cell is
//	                      expensive.
//	KEN_HYDE_RERANK_ONLY  "1" to SKIP the plain-hybrid sweep and run
//	                      only the hybrid+rerank mode. Implies
//	                      KEN_RERANK=1. Use when the plain-hybrid
//	                      numbers are already on file and you only
//	                      want the stacking question answered.
//	KEN_HYDE_DUMP_CSV     path to write a per-(qid, mode, w) CSV of
//	                      ndcg10/r10/r100 (default: no CSV). Enables a
//	                      paired per-query Δ analysis at the end of
//	                      the test: even when aggregate NDCG is
//	                      identical across w values, this reveals
//	                      whether HyDE helps some queries and hurts
//	                      others by an equal amount (the "tail-help
//	                      vs zero-sum" question).

package ndcg

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/townsendmerino/aikit/encoder"
	"github.com/townsendmerino/ken/internal/search"
)

// symbolQueryREBench mirrors internal/search/adaptive.go::symbolQueryRE
// (the regex is unexported there). Verbatim copy so the bench can
// bucket queries by class without exposing the production regex.
var symbolQueryREBench = regexp.MustCompile(
	`^(?:` +
		`[A-Za-z_][A-Za-z0-9_]*(?:(?:::|\\|->|\.)[A-Za-z_][A-Za-z0-9_]*)+` +
		`|_[A-Za-z0-9_]*` +
		`|[A-Za-z][A-Za-z0-9]*[A-Z_][A-Za-z0-9_]*` +
		`|[A-Z][A-Za-z0-9]*` +
		`)$`,
)

const (
	defaultNLBenchDir = "../../testdata/bench/csn-python-nl"
	// candidatesPerQuery from coir_test.go (100) is reused: deep enough
	// over-fetch for the passage→doc aggregation to find ≥10 distinct
	// files at NDCG@10. We additionally want Recall@100, which is just
	// "did the qrel doc appear in the top 100?" — same shortlist.
)

// rerankNForGate is the stage-1 over-fetch the recall@rerankN gate
// measures against. Matches the production reranker default
// (rerankN=50). The Phase A "leak-free" memo (outputs/m0b-results.md)
// front-loads recall@50 as THE decision variable: it's the fraction
// of queries where stage-1 already places the relevant doc inside
// the shortlist the reranker would see. HyDE can only act through
// this window — if it's already ~1.0 at w=0, HyDE has no surface.
const rerankNForGate = 50

type hydeSnippet struct {
	QueryID string `json:"query_id"`
	Snippet string `json:"snippet"`
}

func TestHyDE_M0(t *testing.T) {
	benchDir := os.Getenv("KEN_HYDE_BENCH_DIR")
	if benchDir == "" {
		benchDir = defaultNLBenchDir
	}
	queriesPath := filepath.Join(benchDir, "queries.jsonl")
	qrelsPath := filepath.Join(benchDir, "qrels.jsonl")
	corpusDir := filepath.Join(benchDir, "corpus")
	for _, p := range []string{queriesPath, qrelsPath, corpusDir} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("missing %s — run scripts/bench_csn_nl.py (and bench_csn_nl_stripped.py for the leak-free variant) first", p)
		}
	}

	modelID := os.Getenv("KEN_HYDE_MODEL")
	if modelID == "" {
		modelID = "claude-sonnet-4-6"
	}
	snippetsPath := filepath.Join(benchDir, "hyde-snippets-"+modelID+".jsonl")
	if _, err := os.Stat(snippetsPath); err != nil {
		t.Skipf("missing %s — run `python scripts/m0_hyde.py` first", snippetsPath)
	}

	limit := 1000
	if v := os.Getenv("KEN_HYDE_QUERY_LIMIT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			t.Fatalf("invalid KEN_HYDE_QUERY_LIMIT=%q", v)
		}
		limit = n
	}

	t.Log("loading queries + qrels + snippets...")
	queries := loadQueries(t, queriesPath)
	qrelsByQuery := loadQrels(t, qrelsPath)
	snippets := loadSnippets(t, snippetsPath)

	keep := make([]queryRow, 0, limit)
	skippedNoSnippet, skippedNoQrels := 0, 0
	sort.Slice(queries, func(i, j int) bool { return queries[i].QueryID < queries[j].QueryID })
	for _, q := range queries {
		if len(qrelsByQuery[q.QueryID]) == 0 {
			skippedNoQrels++
			continue
		}
		if _, ok := snippets[q.QueryID]; !ok {
			skippedNoSnippet++
			continue
		}
		keep = append(keep, q)
		if len(keep) >= limit {
			break
		}
	}
	t.Logf("evaluating %d queries (limit=%d, skipped: no-qrels=%d, no-snippet=%d)",
		len(keep), limit, skippedNoQrels, skippedNoSnippet)
	if len(keep) == 0 {
		t.Fatal("no evaluable queries — did the snippet generator finish?")
	}

	modelDir := os.Getenv("KEN_MODEL_DIR")
	if modelDir == "" {
		modelDir = filepath.Join(os.Getenv("HOME"), ".ken", "model")
	}
	if _, err := os.Stat(filepath.Join(modelDir, "model.safetensors")); err != nil {
		t.Fatalf("hybrid mode needs a model at %s: %v", modelDir, err)
	}

	chunker := "regex"
	if os.Getenv("KEN_CHUNKER_TREESITTER") == "1" {
		chunker = "treesitter"
	}

	t.Logf("building index over %s (chunker=%s)...", corpusDir, chunker)
	tBuild := time.Now()
	ix, err := search.FromPath(corpusDir, search.ModeHybrid, chunker, modelDir)
	if err != nil {
		t.Fatalf("FromPath: %v", err)
	}
	t.Logf("indexed %d chunks in %.1fs", ix.Len(), time.Since(tBuild).Seconds())
	m := ix.Model()
	if m == nil {
		t.Fatal("ix.Model() returned nil — hybrid mode should have one")
	}

	// Stage 8 body:label ratio report. Walks the corpus dir once and
	// computes the mean/median/p99 of label-chars-as-fraction-of-
	// chunk-chars for chunks whose first line starts with `# func:`.
	// Catches the heur-only flooding mode early (a ratio > 25%
	// foreshadows the catastrophic precision collapse M0d's
	// heur-only ablation showed). Empty/no-label corpora print
	// "no label prefix detected" so the bench still runs.
	reportBodyLabelRatio(t, corpusDir)

	// Optional Stage-6 rerank arm.
	rerankEnabled := os.Getenv("KEN_RERANK") == "1"
	if rerankEnabled {
		rerankDir := os.Getenv("KEN_RERANK_MODEL_DIR")
		if rerankDir == "" {
			rerankDir = filepath.Join(os.Getenv("HOME"), ".ken", "rerank-model")
		}
		if _, err := os.Stat(filepath.Join(rerankDir, "model.safetensors")); err != nil {
			t.Fatalf("KEN_RERANK=1 but no rerank model at %s: %v", rerankDir, err)
		}
		tLoad := time.Now()
		rm, err := encoder.Load(rerankDir)
		if err != nil {
			t.Fatalf("encoder.Load: %v", err)
		}
		rerankN := 50
		if v := os.Getenv("KEN_RERANK_TOP_N"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				rerankN = n
			}
		}
		rerankBeta := 1.0
		if v := os.Getenv("KEN_RERANK_BETA"); v != "" {
			if b, err := strconv.ParseFloat(v, 64); err == nil && b >= 0 && b <= 1 {
				rerankBeta = b
			}
		}
		t.Logf("loaded rerank model in %.1fs (rerankN=%d β=%v)", time.Since(tLoad).Seconds(), rerankN, rerankBeta)
		ix.SetReranker(
			search.NewNeuralReranker(rm),
			search.WithRerankN(rerankN),
			search.WithRerankBlendBeta(rerankBeta),
		)
	}

	// Precompute the per-query embeddings ONCE: encode the NL query
	// text and its HyDE snippet exactly once apiece. The 4-w sweep
	// (× 2 modes) is then pure linear-combine + retrieval, no
	// repeated transformer work.
	t.Log("encoding query + snippet vectors...")
	tEnc := time.Now()
	type qVecs struct {
		q []float32
		s []float32
	}
	enc := make(map[string]qVecs, len(keep))
	for _, q := range keep {
		enc[q.QueryID] = qVecs{
			q: m.Encode(q.Text),
			s: m.Encode(snippets[q.QueryID]),
		}
	}
	t.Logf("encoded in %.1fs", time.Since(tEnc).Seconds())

	type cell struct {
		Name        string  // "w=0.3" (M0) or "oracle-max" (M0c)
		W           float64 // HyDE blend weight (0.0 for M0c)
		Mode        string
		NDCG10      float64
		R10         float64
		R100        float64
		RerankNGate float64 // Recall@rerankN — the Phase A leak-free gate.
		Symbol      float64 // NDCG@10 over symbol-class queries (typically empty here)
		NL          float64 // NDCG@10 over NL-class queries
		NLCount     int
		SymCnt      int
		Wall        float64
	}

	// cellSpec drives one row of the bench: a (cellName, w, predictor)
	// triple. The HyDE M0 path produces cells like w=0.3 with no
	// predictor; the M0c path produces cells like arm=oracle-max with
	// w=0.0 and a real predictor. A single inner loop handles both.
	type cellSpec struct {
		name      string
		w         float64
		predictor search.Predictor
	}

	// Build the cell driver. KEN_M0C_ARMS=<csv> selects transform-#2
	// arms (M0c mode); otherwise the harness falls back to the HyDE
	// w-sweep (M0 mode).
	var cells []cellSpec
	m0cArms := os.Getenv("KEN_M0C_ARMS")
	if m0cArms != "" {
		// M0c: transform-#2 arms at w=0.0 only. The arm list always
		// begins with "baseline" so flip-set analysis has a stable
		// reference even when the operator omits it from the env.
		k := 8
		if v := os.Getenv("KEN_M0C_K"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				k = n
			}
		}
		dfFloor := 5
		if v := os.Getenv("KEN_M0C_DF_FLOOR"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				dfFloor = n
			}
		}
		// DF ceiling: drop near-corpus-mean tokens whose centroids
		// point nowhere. max(50, N/100) ≈ 150 on a 14.7k-doc corpus.
		dfCeil := ix.Len() / 100
		if dfCeil < 50 {
			dfCeil = 50
		}
		if v := os.Getenv("KEN_M0C_DF_CEIL"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				dfCeil = n
			}
		}
		encoderThresh := 0.30
		if v := os.Getenv("KEN_M0C_ENCODER_THRESH"); v != "" {
			if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 && f <= 1 {
				encoderThresh = f
			}
		}
		prfTopN := 10
		if v := os.Getenv("KEN_M0C_PRF_TOPN"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				prfTopN = n
			}
		}
		var oracleMax, oracleDF *oraclePredictor
		var prf *prfPredictor
		var encoder *encoderPredictor
		hasBaseline := false
		for _, name := range strings.Split(m0cArms, ",") {
			name = strings.TrimSpace(name)
			switch name {
			case "baseline":
				cells = append(cells, cellSpec{name: "baseline", w: 0.0, predictor: nil})
				hasBaseline = true
			case "oracle-max":
				if oracleMax == nil {
					oracleMax = newOraclePredictor(keep, qrelsByQuery, ix.Chunks(), ix.BM25(), corpusDir, k, 0, "oracle-max")
				}
				cells = append(cells, cellSpec{name: "oracle-max", w: 0.0, predictor: oracleMax})
			case "oracle-df":
				if oracleDF == nil {
					oracleDF = newOraclePredictor(keep, qrelsByQuery, ix.Chunks(), ix.BM25(), corpusDir, k, dfFloor, fmt.Sprintf("oracle-df%d", dfFloor))
				}
				cells = append(cells, cellSpec{name: fmt.Sprintf("oracle-df%d", dfFloor), w: 0.0, predictor: oracleDF})
			case "prf":
				if prf == nil {
					prf = newPRFPredictor(ix, m, prfTopN, k, "prf")
				}
				cells = append(cells, cellSpec{name: fmt.Sprintf("prf-top%d-k%d", prfTopN, k), w: 0.0, predictor: prf})
			case "encoder":
				if encoder == nil {
					tBuild := time.Now()
					encoder = newEncoderPredictor(ix, m, k, encoderThresh, dfFloor, dfCeil, "encoder")
					t.Logf("encoder centroid matrix built in %.1fs (DF in [%d, %d], %d identifiers)",
						time.Since(tBuild).Seconds(), dfFloor, dfCeil, len(encoder.tokens))
				}
				cells = append(cells, cellSpec{name: fmt.Sprintf("encoder-thr%.2f-k%d", encoderThresh, k), w: 0.0, predictor: encoder})
			default:
				t.Fatalf("unknown KEN_M0C_ARMS entry %q (want baseline|oracle-max|oracle-df|prf|encoder)", name)
			}
		}
		if !hasBaseline {
			// Prepend baseline so flip-set analysis has a reference.
			cells = append([]cellSpec{{name: "baseline", w: 0.0, predictor: nil}}, cells...)
		}
		t.Logf("M0c mode: %d arms (k=%d, df_floor=%d, df_ceil=%d, prf_topN=%d, encoder_thresh=%.2f)",
			len(cells), k, dfFloor, dfCeil, prfTopN, encoderThresh)
	} else {
		// M0: HyDE w-sweep, no predictors.
		weights := []float64{0.0, 0.3, 0.5, 0.7}
		if v := os.Getenv("KEN_HYDE_WEIGHTS"); v != "" {
			weights = nil
			for _, p := range strings.Split(v, ",") {
				f, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
				if err != nil {
					t.Fatalf("invalid KEN_HYDE_WEIGHTS=%q: %v", v, err)
				}
				weights = append(weights, f)
			}
		}
		for _, w := range weights {
			cells = append(cells, cellSpec{name: fmt.Sprintf("w=%.1f", w), w: w, predictor: nil})
		}
	}

	modes := []search.Mode{search.ModeHybrid}
	modeStr := map[search.Mode]string{search.ModeHybrid: "hybrid", search.ModeHybridRerank: "hybrid+rerank"}
	if rerankEnabled {
		modes = append(modes, search.ModeHybridRerank)
	}
	if os.Getenv("KEN_HYDE_RERANK_ONLY") == "1" {
		if !rerankEnabled {
			t.Fatal("KEN_HYDE_RERANK_ONLY=1 requires KEN_RERANK=1")
		}
		modes = []search.Mode{search.ModeHybridRerank}
	}

	// Per-query timing instrumentation, gated on KEN_HYDE_TRACE=1.
	// When on, the bench uses SearchWithQVecTelemetry, records per-
	// query wall + telemetry stage-breakdowns, and prints a progress
	// line every progressEvery queries. At end of each cell it prints
	// the top-10 slowest queries with their text length and the cache
	// hit rate. This catches the pathological-tail-latency case we
	// hit during the 3h-timeout run (no progress signal, no idea
	// whether some queries were doing 10× more work than others).
	trace := os.Getenv("KEN_HYDE_TRACE") == "1"
	progressEvery := 25

	// Per-query bookkeeping. perQueryNDCG drives the paired Δ analysis
	// (M0). perQueryR50 + perQueryPos drive Phase B's flip-set
	// analysis: which queries did the treatment pull into the rerank
	// shortlist at stage-1, and where did the reranker then place them?
	//
	//   perQueryNDCG[modeStr][cellName][qid] = ndcg10
	//   perQueryR50 [modeStr][cellName][qid] = stage-1 recall@50
	//   perQueryPos [modeStr][cellName][qid] = 1-indexed qrel position
	//                                          in the final ranking,
	//                                          or 0 if not in top-100
	//
	// The cellName key carries both M0 ("w=0.0", "w=0.3", ...) and M0c
	// ("baseline", "oracle-max", "prf-top10-k8", ...) variants so the
	// flip-set logic doesn't need to know which mode it's in.
	perQueryNDCG := make(map[string]map[string]map[string]float64)
	perQueryR50 := make(map[string]map[string]map[string]float64)
	perQueryPos := make(map[string]map[string]map[string]int)
	dumpCSV := os.Getenv("KEN_HYDE_DUMP_CSV")
	var csvFile *os.File
	if dumpCSV != "" {
		var err error
		csvFile, err = os.Create(dumpCSV)
		if err != nil {
			t.Fatalf("create dump csv: %v", err)
		}
		defer csvFile.Close()
		if _, err := fmt.Fprintf(csvFile, "qid,mode,cell,ndcg10,recall10,recall100,recall%d,qrel_pos\n", rerankNForGate); err != nil {
			t.Fatalf("write csv header: %v", err)
		}
	}

	results := make([]cell, 0, len(cells)*len(modes))
	for _, mode := range modes {
		for _, c := range cells {
			tw := time.Now()
			perNDCG := make([]float64, 0, len(keep))
			perR10 := make([]float64, 0, len(keep))
			perR100 := make([]float64, 0, len(keep))
			perRRerankN := make([]float64, 0, len(keep))
			var symNDCG, nlNDCG []float64

			// Per-query trace bookkeeping (only populated when trace).
			type qStat struct {
				qid             string
				textLen         int
				total           time.Duration
				stage1          time.Duration
				rerank          time.Duration
				rerankQEncode   time.Duration
				rerankCEncode   time.Duration
				rerankCacheHits int
				rerankCacheMiss int
			}
			var stats []qStat
			if trace {
				stats = make([]qStat, 0, len(keep))
			}

			cellStart := time.Now()
			lastProgress := time.Now()
			for i, q := range keep {
				vs := enc[q.QueryID]
				qV := blend(vs.q, vs.s, c.w)
				var predicted []string
				if c.predictor != nil {
					predicted = c.predictor.Predict(q.Text)
				}

				var hits []search.Result
				var perQWall time.Duration
				if trace {
					qt0 := time.Now()
					hs, _, tel := ix.SearchWithQVecPredictedTelemetry(q.Text, qV, predicted, candidatesPerQuery, mode)
					perQWall = time.Since(qt0)
					hits = hs
					stats = append(stats, qStat{
						qid:             q.QueryID,
						textLen:         len(q.Text),
						total:           perQWall,
						stage1:          tel.Stage1Wall,
						rerank:          tel.RerankWall,
						rerankQEncode:   tel.RerankerQueryEncode,
						rerankCEncode:   tel.RerankerCandidateEncode,
						rerankCacheHits: tel.RerankerCacheHits,
						rerankCacheMiss: tel.RerankerCacheMisses,
					})
				} else {
					hits, _ = ix.SearchWithQVecPredicted(q.Text, qV, predicted, candidatesPerQuery, mode)
				}

				ranked := aggregateByDoc(hits)
				rels := qrelsByQuery[q.QueryID]
				nd := AtK(ranked, rels, 10)
				r10 := recallAt(ranked, rels, 10)
				r100 := recallAt(ranked, rels, 100)
				rRerankN := recallAt(ranked, rels, rerankNForGate)
				qpos := qrelPosition(ranked, rels)
				perNDCG = append(perNDCG, nd)
				perR10 = append(perR10, r10)
				perR100 = append(perR100, r100)
				perRRerankN = append(perRRerankN, rRerankN)

				// Per-query bookkeeping.
				mKey := modeStr[mode]
				if _, ok := perQueryNDCG[mKey]; !ok {
					perQueryNDCG[mKey] = make(map[string]map[string]float64)
					perQueryR50[mKey] = make(map[string]map[string]float64)
					perQueryPos[mKey] = make(map[string]map[string]int)
				}
				if _, ok := perQueryNDCG[mKey][c.name]; !ok {
					perQueryNDCG[mKey][c.name] = make(map[string]float64, len(keep))
					perQueryR50[mKey][c.name] = make(map[string]float64, len(keep))
					perQueryPos[mKey][c.name] = make(map[string]int, len(keep))
				}
				perQueryNDCG[mKey][c.name][q.QueryID] = nd
				perQueryR50[mKey][c.name][q.QueryID] = rRerankN
				perQueryPos[mKey][c.name][q.QueryID] = qpos
				if csvFile != nil {
					if _, err := fmt.Fprintf(csvFile, "%s,%s,%s,%.6f,%.6f,%.6f,%.6f,%d\n",
						q.QueryID, mKey, c.name, nd, r10, r100, rRerankN, qpos); err != nil {
						t.Fatalf("dump csv row: %v", err)
					}
				}
				if isSymbolQuery(q.Text) {
					symNDCG = append(symNDCG, nd)
				} else {
					nlNDCG = append(nlNDCG, nd)
				}

				if trace && ((i+1)%progressEvery == 0 || i == 0) {
					recent := stats
					if len(recent) > progressEvery {
						recent = recent[len(recent)-progressEvery:]
					}
					var sumTotal, sumS1, sumRR, sumCEnc time.Duration
					var hits, miss int
					for _, s := range recent {
						sumTotal += s.total
						sumS1 += s.stage1
						sumRR += s.rerank
						sumCEnc += s.rerankCEncode
						hits += s.rerankCacheHits
						miss += s.rerankCacheMiss
					}
					n := len(recent)
					hitRate := 0.0
					if hits+miss > 0 {
						hitRate = float64(hits) / float64(hits+miss)
					}
					t.Logf(
						"[%-13s %-12s] %3d/%d last=%s tot=%6.0fms s1=%5.1fms rr=%5.0fms enc=%5.0fms hit=%4.1f%% (since=%5.1fs)",
						modeStr[mode], c.name, i+1, len(keep), q.QueryID,
						float64(sumTotal.Microseconds())/float64(n)/1000.0,
						float64(sumS1.Microseconds())/float64(n)/1000.0,
						float64(sumRR.Microseconds())/float64(n)/1000.0,
						float64(sumCEnc.Microseconds())/float64(n)/1000.0,
						hitRate*100,
						time.Since(lastProgress).Seconds(),
					)
					lastProgress = time.Now()
				}
			}

			results = append(results, cell{
				W: c.w, Mode: modeStr[mode], Name: c.name,
				NDCG10:      Average(perNDCG),
				R10:         Average(perR10),
				R100:        Average(perR100),
				RerankNGate: Average(perRRerankN),
				Symbol:      Average(symNDCG),
				NL:          Average(nlNDCG),
				NLCount:     len(nlNDCG),
				SymCnt:      len(symNDCG),
				Wall:        time.Since(tw).Seconds(),
			})
			t.Logf("[%-13s %-12s] NDCG@10=%.4f R@10=%.4f R@%d=%.4f R@100=%.4f (NL n=%d ndcg=%.4f / sym n=%d ndcg=%.4f) wall=%.1fs",
				modeStr[mode], c.name,
				results[len(results)-1].NDCG10, results[len(results)-1].R10,
				rerankNForGate, results[len(results)-1].RerankNGate,
				results[len(results)-1].R100,
				results[len(results)-1].NLCount, results[len(results)-1].NL,
				results[len(results)-1].SymCnt, results[len(results)-1].Symbol,
				results[len(results)-1].Wall)

			// Encoder-arm diagnostic: how many identifiers cleared
			// the cosine threshold per query. Disambiguates "weak
			// predictor" (most queries return 0, OR mean top-1 is
			// very low) from "threshold too high" (mean top-1 is
			// 0.25 but threshold is 0.30).
			if enc, ok := c.predictor.(*encoderPredictor); ok {
				dist, meanTop1, nq := enc.Histogram()
				var hb strings.Builder
				hb.WriteString(fmt.Sprintf("  encoder threshold-pass histogram (n=%d, mean top-1 cosine=%.3f, threshold=%.2f):",
					nq, meanTop1, enc.threshold))
				for k, count := range dist {
					if count > 0 {
						hb.WriteString(fmt.Sprintf(" [k=%d:%d]", k, count))
					}
				}
				t.Log(hb.String())
			}

			if trace && len(stats) > 0 {
				// Slowest-10 report for this cell. Sort by total wall.
				sorted := make([]qStat, len(stats))
				copy(sorted, stats)
				sort.Slice(sorted, func(a, b int) bool { return sorted[a].total > sorted[b].total })
				dump := sorted
				if len(dump) > 10 {
					dump = dump[:10]
				}
				t.Logf("[%-13s %-12s] top-10 slowest queries (cell wall %.1fs):", modeStr[mode], c.name, time.Since(cellStart).Seconds())
				for _, s := range dump {
					t.Logf("  %s textlen=%5d tot=%7.0fms s1=%6.1fms rr=%7.0fms enc=%7.0fms hit/miss=%d/%d",
						s.qid, s.textLen,
						float64(s.total.Microseconds())/1000,
						float64(s.stage1.Microseconds())/1000,
						float64(s.rerank.Microseconds())/1000,
						float64(s.rerankCEncode.Microseconds())/1000,
						s.rerankCacheHits, s.rerankCacheMiss,
					)
				}
			}
		}
	}

	// Markdown table to stderr, grep-able for the M0/M0b/M0c memo. The
	// leftmost data column is R@rerankN (the Phase A gate) — it is THE
	// decision variable when the bench is run leak-free; NDCG/R@10 are
	// secondary signal. The "Cell" column carries either a HyDE weight
	// (M0 mode: "w=0.3") or a transform-#2 arm name (M0c mode:
	// "oracle-max", "prf-top10-k8", ...).
	var sb strings.Builder
	sb.WriteString("\n\nHyDE bench=" + benchDir + " (chunker=" + chunker + ", snippets=" + modelID + ")\n")
	sb.WriteString(fmt.Sprintf("queries=%d  rerank=%v\n", len(keep), rerankEnabled))
	sb.WriteString(fmt.Sprintf("\n| Mode          | Cell         |   R@%d | NDCG@10 | NDCG NL | NDCG sym | R@10  | R@100 |\n", rerankNForGate))
	sb.WriteString("|---------------|--------------|------:|--------:|--------:|---------:|------:|------:|\n")
	for _, r := range results {
		sym := fmt.Sprintf("%.4f", r.Symbol)
		if r.SymCnt == 0 {
			sym = "n/a"
		}
		sb.WriteString(fmt.Sprintf("| %-13s | %-12s | %5.4f | %7.4f | %7.4f | %8s | %5.4f | %5.4f |\n",
			r.Mode, r.Name, r.RerankNGate, r.NDCG10, r.NL, sym, r.R10, r.R100))
	}
	t.Log(sb.String())

	// Baseline cell is cells[0] by construction (M0: "w=0.0"; M0c:
	// "baseline"). Δ analysis + flip-set both compare every other
	// cell against this anchor. If only one cell ran, both analyses
	// short-circuit.
	if len(cells) < 2 {
		return
	}
	baselineName := cells[0].name

	// Paired per-query Δ NDCG@10 analysis: which treatment cells
	// helped/hurt how many queries vs the baseline cell. Aggregate
	// NDCG numbers at N≤500 live in the noise floor (~6pp SE); the
	// per-query helped/hurt counts and the sumΔ split are the
	// signal-bearing reads.
	for _, mode := range modes {
		mStr := modeStr[mode]
		base, hasBase := perQueryNDCG[mStr][baselineName]
		if !hasBase {
			continue
		}
		for _, c := range cells[1:] {
			treatment, ok := perQueryNDCG[mStr][c.name]
			if !ok {
				continue
			}
			type qd struct {
				qid   string
				delta float64
			}
			deltas := make([]qd, 0, len(base))
			helped, hurt, tie := 0, 0, 0
			var sumPos, sumNeg float64
			for qid, b := range base {
				tt, ok := treatment[qid]
				if !ok {
					continue
				}
				d := tt - b
				deltas = append(deltas, qd{qid, d})
				switch {
				case d > 1e-9:
					helped++
					sumPos += d
				case d < -1e-9:
					hurt++
					sumNeg += d
				default:
					tie++
				}
			}
			sort.Slice(deltas, func(i, j int) bool { return deltas[i].delta > deltas[j].delta })
			t.Logf("[Δ %s %s vs %s] helped=%d hurt=%d tie=%d  "+
				"sumΔ_pos=%+.4f sumΔ_neg=%+.4f  mean_Δ=%+.6f",
				mStr, c.name, baselineName, helped, hurt, tie,
				sumPos, sumNeg, (sumPos+sumNeg)/float64(len(deltas)))
			if helped > 0 || hurt > 0 {
				t.Logf("  top-5 HELPED:")
				for i := 0; i < 5 && i < len(deltas); i++ {
					if deltas[i].delta <= 1e-9 {
						break
					}
					t.Logf("    %s Δ=%+.4f", deltas[i].qid, deltas[i].delta)
				}
				t.Logf("  top-5 HURT:")
				for i := 0; i < 5 && i < len(deltas); i++ {
					idx := len(deltas) - 1 - i
					if idx < 0 || deltas[idx].delta >= -1e-9 {
						break
					}
					t.Logf("    %s Δ=%+.4f", deltas[idx].qid, deltas[idx].delta)
				}
			}
		}
	}

	// Phase B / M0c flip-set analysis. For each mode that has a
	// baseline and a treatment cell, identify queries whose stage-1
	// recall@rerankN flipped (0→1 means the treatment surfaced the
	// relevant doc into the rerank shortlist; 1→0 means it pushed
	// the doc out). For each flipped query, report the final qrel
	// position under both cells. The reranker re-scores stage-1
	// candidates from scratch, so only the recall flip can propagate
	// through it — within-shortlist repositioning at stage-1 is
	// overwritten. The build-relevant signal is right here, not in
	// the aggregate NDCG delta.
	type flipSet struct {
		mode   string
		cell   string
		gained map[string]struct{}
	}
	var allFlips []flipSet
	for _, mode := range modes {
		mStr := modeStr[mode]
		baseR50, hasBase := perQueryR50[mStr][baselineName]
		if !hasBase {
			continue
		}
		for _, c := range cells[1:] {
			treatR50, ok := perQueryR50[mStr][c.name]
			if !ok {
				continue
			}
			type flipRow struct {
				qid     string
				basePos int
				trtPos  int
			}
			var gained, lost []flipRow
			gainedSet := make(map[string]struct{})
			for qid, br := range baseR50 {
				tr, ok := treatR50[qid]
				if !ok {
					continue
				}
				if br < 0.5 && tr >= 0.5 {
					gained = append(gained, flipRow{
						qid:     qid,
						basePos: perQueryPos[mStr][baselineName][qid],
						trtPos:  perQueryPos[mStr][c.name][qid],
					})
					gainedSet[qid] = struct{}{}
				} else if br >= 0.5 && tr < 0.5 {
					lost = append(lost, flipRow{
						qid:     qid,
						basePos: perQueryPos[mStr][baselineName][qid],
						trtPos:  perQueryPos[mStr][c.name][qid],
					})
				}
			}
			sort.Slice(gained, func(i, j int) bool { return gained[i].qid < gained[j].qid })
			sort.Slice(lost, func(i, j int) bool { return lost[i].qid < lost[j].qid })
			t.Logf("[flip-set %s %s vs %s] gained=%d (surfaced qrel into top-%d)  lost=%d (pushed it out)",
				mStr, c.name, baselineName, len(gained), rerankNForGate, len(lost))
			if len(gained) > 0 {
				t.Logf("  GAINED queries (qid | base_pos→trt_pos):")
				for _, r := range gained {
					t.Logf("    %s | %s → %s", r.qid, fmtPos(r.basePos), fmtPos(r.trtPos))
				}
			}
			if len(lost) > 0 {
				t.Logf("  LOST queries (qid | base_pos→trt_pos):")
				for _, r := range lost {
					t.Logf("    %s | %s → %s", r.qid, fmtPos(r.basePos), fmtPos(r.trtPos))
				}
			}
			allFlips = append(allFlips, flipSet{mode: mStr, cell: c.name, gained: gainedSet})
		}
	}

	// M0c-only: assert the baseline cell is byte-identical to Phase
	// B's hybrid+rerank w=0.0 per-query qrel_pos at the file
	// referenced by KEN_M0C_PHASEB_CSV (or default below). The HyDE-
	// overlap reference (the 7 qids from Phase B) is only valid if
	// this holds. Mismatch is t.Fatal — the harness wedged something
	// silently in between runs.
	//
	// Skipped when no CSV is given OR no hybrid+rerank baseline ran
	// (e.g. M0 mode, or KEN_RERANK=0 in M0c mode).
	if m0cArms != "" {
		csvPath := os.Getenv("KEN_M0C_PHASEB_CSV")
		if csvPath == "" {
			csvPath = "/tmp/m0b-phase-b-perq.csv"
		}
		basePosByQid := perQueryPos[modeStr[search.ModeHybridRerank]][baselineName]
		if len(basePosByQid) > 0 {
			if mismatches, n := verifyPhaseBByteIdentity(t, csvPath, basePosByQid); n > 0 {
				if mismatches > 0 {
					t.Fatalf("Phase B byte-identity check failed: %d/%d qids disagree on qrel_pos — HyDE overlap reference is invalid",
						mismatches, n)
				}
				t.Logf("Phase B byte-identity check: PASS (%d qids match)", n)
			}
		}
	}

	// M0c-only: overlap table vs HyDE Phase B's 7 gained queries.
	// For each treatment cell, intersect its gained-qid set with
	// hydePhaseBGained. Disjoint → the transform stacks with HyDE;
	// equal → redundant; subset → covers part of the same recall
	// surface.
	if m0cArms != "" && len(allFlips) > 0 {
		t.Logf("\nHyDE Phase B overlap (vs the 7 qids HyDE rescued in outputs/m0b-phase-b-results.md):")
		t.Logf("HyDE-gained set: %v", sortedKeysSet(hydePhaseBGained))
		for _, f := range allFlips {
			overlap := make([]string, 0)
			only := make([]string, 0)
			for qid := range f.gained {
				if _, in := hydePhaseBGained[qid]; in {
					overlap = append(overlap, qid)
				} else {
					only = append(only, qid)
				}
			}
			sort.Strings(overlap)
			sort.Strings(only)
			t.Logf("  %s %s: |gained|=%d  overlap-with-HyDE=%d  arm-only=%d",
				f.mode, f.cell, len(f.gained), len(overlap), len(only))
			if len(overlap) > 0 {
				t.Logf("    overlap qids: %v", overlap)
			}
			if len(only) > 0 {
				t.Logf("    arm-only qids: %v", only)
			}
		}

		// Stage 8 M0c-30 capture: which of the 30 qids that M0d
		// Arm B did NOT rescue does THIS arm rescue? That's the
		// per-addition decision metric — gained-from-30 has to
		// be > 0 with lost-vs-baseline not blowing up for an
		// additive arm to ship. The HyDE-overlap above is for
		// understanding stacking with HyDE; this is the bake-off
		// decision.
		t.Logf("\nM0c-30 capture (vs the 30 qids M0d Arm B did NOT rescue):")
		t.Logf("M0c-30 set: %d qids (the M0d Arm B failure surface)", len(m0cUnreached30))
		for _, f := range allFlips {
			capturedFromUnreached := make([]string, 0)
			for qid := range f.gained {
				if _, in := m0cUnreached30[qid]; in {
					capturedFromUnreached = append(capturedFromUnreached, qid)
				}
			}
			sort.Strings(capturedFromUnreached)
			t.Logf("  %s %s: captured %d of M0c-30 unreached", f.mode, f.cell, len(capturedFromUnreached))
			if len(capturedFromUnreached) > 0 {
				t.Logf("    qids: %v", capturedFromUnreached)
			}
		}
	}
}

// hydePhaseBGained pins the 7 queries HyDE Phase B rescued at w=0.3
// on hybrid+rerank (outputs/m0b-phase-b-results.md). The M0c bench
// uses this as the reference for the HyDE-overlap analysis. Hardcoded
// because Phase B is a fixed historical result; any future re-run
// updates this set explicitly so the comparison is intentional.
var hydePhaseBGained = map[string]struct{}{
	"c134055": {},
	"c200465": {},
	"c265644": {},
	"c265774": {},
	"c265803": {},
	"c265879": {},
	"c265900": {},
}

// m0cUnreached30 is the M0c-40 minus the 10 qids M0d's Arm B
// (heur-full) captured. Derived from /tmp/m0d-stripped.csv ∪
// /tmp/m0d-stripped-heur.csv by the procedure in scripts/merge_m0d.py
// and pinned here for the Stage 8 bench so each invocation reports
// "of-30 captured" without rebuilding the M0c-40 from scratch.
//
// These are the queries the Stage 8 Track 1 additive arms (callers /
// imports / signature / siblings + the union) are racing to rescue:
// queries where oracle-df5 puts the qrel in top-50 but Arm B did not.
// Reaching a higher fraction of these 30 is the headline metric.
var m0cUnreached30 = map[string]struct{}{
	"c109483": {}, "c128025": {}, "c133741": {}, "c133839": {}, "c134045": {},
	"c134392": {}, "c135455": {}, "c135654": {}, "c173658": {}, "c182790": {},
	"c200465": {}, "c209177": {}, "c212467": {}, "c21908": {}, "c220222": {},
	"c265614": {}, "c265616": {}, "c265621": {}, "c265628": {}, "c265670": {},
	"c265686": {}, "c265690": {}, "c265721": {}, "c265766": {}, "c265774": {},
	"c265785": {}, "c265831": {}, "c265840": {}, "c265890": {}, "c265940": {},
}

// reportBodyLabelRatio walks the corpus directory, computes the
// label-line-length / chunk-length ratio for every chunk that starts
// with `# func:` (the Stage 8 / M0d Arm B label prefix), and logs
// mean / median / p99. The early-warning signal for M0d's heur-only
// flooding mode: a corpus whose median label is >25% of its chunk
// size is likely to drag NDCG down (the label dominates BM25 + the
// dense embedding, the body's signal gets diluted).
func reportBodyLabelRatio(t *testing.T, corpusDir string) {
	t.Helper()
	var ratios []float64
	withLabel, withoutLabel := 0, 0
	_ = filepath.WalkDir(corpusDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".py") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if !bytesHasPrefix(body, "# func:") {
			withoutLabel++
			return nil
		}
		// Label is everything up to the first newline (Enrich
		// emits `# ... \n` as a single prefix line). Body is the
		// rest. Ratio = label / total. Zero-length body shouldn't
		// happen on this corpus but guard anyway.
		nl := bytesIndexOf(body, '\n')
		if nl <= 0 {
			withoutLabel++
			return nil
		}
		labelLen := nl + 1 // include the newline
		total := len(body)
		if total == 0 {
			return nil
		}
		ratios = append(ratios, float64(labelLen)/float64(total))
		withLabel++
		return nil
	})
	if len(ratios) == 0 {
		t.Logf("body:label ratio: no `# func:` prefix detected on any chunk (corpus %s — likely the unaugmented baseline)", corpusDir)
		return
	}
	sort.Float64s(ratios)
	mean := 0.0
	for _, r := range ratios {
		mean += r
	}
	mean /= float64(len(ratios))
	median := ratios[len(ratios)/2]
	p99 := ratios[len(ratios)*99/100]
	t.Logf("body:label ratio (corpus %s, n_with_label=%d, n_without=%d): mean=%.3f median=%.3f p99=%.3f",
		corpusDir, withLabel, withoutLabel, mean, median, p99)
	if median > 0.25 {
		t.Logf("⚠ WARNING: median label:body ratio > 25%% — expect flooding (M0d heur-only failure mode)")
	}
}

// bytesHasPrefix is a tiny helper to avoid pulling in `bytes` for
// just this. Identical to bytes.HasPrefix(body, []byte(prefix)) but
// works on string prefixes directly.
func bytesHasPrefix(body []byte, prefix string) bool {
	if len(body) < len(prefix) {
		return false
	}
	for i := 0; i < len(prefix); i++ {
		if body[i] != prefix[i] {
			return false
		}
	}
	return true
}

// bytesIndexOf returns the index of the first occurrence of b in
// body, or -1 if absent.
func bytesIndexOf(body []byte, b byte) int {
	for i, c := range body {
		if c == b {
			return i
		}
	}
	return -1
}

// sortedKeysSet returns the keys of a string-set, sorted, for stable
// log output.
func sortedKeysSet(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// verifyPhaseBByteIdentity loads Phase B's per-query CSV and diffs
// the current baseline cell's qrel_pos against it on the
// `hybrid+rerank,w=0.00` rows. Returns (mismatches, matched-qids).
// Returns (0, 0) and skips with a stderr note when the CSV file is
// missing or empty — the M0c bench can still run; only the HyDE-
// overlap claim is downgraded.
func verifyPhaseBByteIdentity(t *testing.T, csvPath string, basePos map[string]int) (int, int) {
	t.Helper()
	f, err := os.Open(csvPath)
	if err != nil {
		t.Logf("Phase B CSV %s not found (%v) — skipping byte-identity check", csvPath, err)
		return 0, 0
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	mismatches, matched := 0, 0
	header := true
	for scanner.Scan() {
		if header {
			header = false
			continue
		}
		line := scanner.Text()
		// Phase B CSV schema: qid,mode,w,ndcg10,recall10,recall100,recall50,qrel_pos
		// M0c CSV schema:     qid,mode,cell,ndcg10,recall10,recall100,recall50,qrel_pos
		// Both have qrel_pos as the last field; phase-B used w as a
		// float ("0.00"), so we identify the baseline rows by mode +
		// w=="0.00".
		parts := strings.Split(line, ",")
		if len(parts) < 8 {
			continue
		}
		qid, mode, w := parts[0], parts[1], parts[2]
		if mode != "hybrid+rerank" {
			continue
		}
		// Match either the historical "0.00" weight or a "baseline"
		// cell name in case the CSV was regenerated with this
		// harness.
		if w != "0.00" && w != "baseline" {
			continue
		}
		want, err := strconv.Atoi(parts[7])
		if err != nil {
			continue
		}
		got, ok := basePos[qid]
		if !ok {
			continue
		}
		matched++
		if got != want {
			mismatches++
			if mismatches <= 5 {
				t.Logf("  byte-identity mismatch qid=%s: got #%d want #%d", qid, got, want)
			}
		}
	}
	return mismatches, matched
}

// fmtPos formats a 1-indexed rank position; 0 means "not in top-100".
func fmtPos(p int) string {
	if p == 0 {
		return "miss"
	}
	return fmt.Sprintf("#%d", p)
}

// blend returns L2-normalize(w*s + (1-w)*q). When w==0 it returns q
// unchanged (after L2 — the model output is already unit-norm but the
// blend re-normalize keeps the API simple). When w==1 it returns s.
func blend(q, s []float32, w float64) []float32 {
	if len(q) != len(s) {
		panic(fmt.Sprintf("blend: dim mismatch q=%d s=%d", len(q), len(s)))
	}
	out := make([]float32, len(q))
	for i := range q {
		out[i] = float32((1-w)*float64(q[i]) + w*float64(s[i]))
	}
	var sumSq float64
	for _, v := range out {
		sumSq += float64(v) * float64(v)
	}
	if sumSq == 0 {
		return out
	}
	inv := float32(1.0 / math.Sqrt(sumSq))
	for i := range out {
		out[i] *= inv
	}
	return out
}

// qrelPosition returns the 1-indexed position of the first qrel-
// positive doc in ranked, or 0 if no positive doc appears. Used by
// Phase B's flip-set tracker: for a query whose stage-1 recall@50
// flipped from 0 (miss) at w=0 to 1 (hit) at w=0.3, we want to know
// where the reranker ended up placing the now-visible doc — was it
// at position 1 (full credit), buried at position 47 (hit but no
// NDCG@10), or somewhere in between?
func qrelPosition(ranked []string, rels map[string]float64) int {
	for i, doc := range ranked {
		if r, ok := rels[doc]; ok && r > 0 {
			return i + 1
		}
	}
	return 0
}

// recallAt: fraction of qrel-positive docs the retriever surfaced in
// the top-k. With one relevant doc per query (the CSN convention) it's
// effectively hit@k.
func recallAt(ranked []string, rels map[string]float64, k int) float64 {
	if len(rels) == 0 {
		return 0
	}
	total := 0
	for _, r := range rels {
		if r > 0 {
			total++
		}
	}
	if total == 0 {
		return 0
	}
	hit := 0
	for i, doc := range ranked {
		if i >= k {
			break
		}
		if r, ok := rels[doc]; ok && r > 0 {
			hit++
		}
	}
	return float64(hit) / float64(total)
}

// isSymbolQuery: trivial re-implementation of the (unexported) check
// in internal/search/adaptive.go so the bench harness can bucket
// queries without depending on the package's private API. Exported via
// a thin alias would be cleaner long-term, but copying the regex keeps
// the bench self-contained and bench is build-tag isolated anyway.
func isSymbolQuery(q string) bool {
	return symbolQueryREBench.MatchString(strings.TrimSpace(q))
}

func loadSnippets(t *testing.T, path string) map[string]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	out := make(map[string]string)
	for scanner.Scan() {
		var r hydeSnippet
		if err := json.Unmarshal(scanner.Bytes(), &r); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		if r.Snippet != "" {
			out[r.QueryID] = r.Snippet
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
