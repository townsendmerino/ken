//go:build bench

// Recall decomposition harness — answers "where is recall@10 lost?"
//
// The 82% (NL) / 87% (symbol) recall@10 in docs/BENCH.md is a stage-1
// hybrid number (no neural reranker). This harness decomposes the loss
// into the two places it can happen, because the fix differs completely:
//
//   1. candidate-generation loss — the target chunk never enters the
//      fused candidate pool (semOrder ∪ bmOrder). Reranking / fusion /
//      penalty tuning CANNOT recover this. Only wider retrieval, better
//      embeddings, or query expansion can. Measured by POOL RECALL@N.
//
//   2. ranking loss — the target IS in the pool but ranked below K, so
//      it misses the final top-K. Reranking, fusion weights, and
//      penalties directly control this. Measured by the gap between
//      POOL RECALL and PIPELINE RECALL@K.
//
// It also ablates the path penalties (penalties.go strongPenalty=0.3 for
// tests/examples, file-saturation decay) — those actively DEMOTE chunks
// out of top-10, trading recall for precision. PENALTY-OFF RECALL@10
// reads out exactly how much recall that costs.
//
// White-box (package search) so it can call hybridSearch's internals
// directly — no production code changes. Build-tag gated like the other
// bench harnesses.
//
// Prereqs (same as bench/tokens/semble_test.go):
//   - semble checkout at /tmp/semble (or $SEMBLE_CHECKOUT) for repos.json
//     + annotations.
//   - corpus synced at ~/.cache/semble-bench (or $KEN_SEMBLE_CORPUS_ROOT).
//   - potion-code-16M model at ~/.ken/model (or $KEN_MODEL_DIR).
//
// Run:
//   go test -tags=bench ./internal/search/ -run TestRecallDecomp -v -timeout 30m
//
// Optional slow rerank arm (separate test, opt-in — the neural reranker
// is ~30s/query COLD before the M9 on-disk cache warms):
//   KEN_RERANK=1 go test -tags=bench ./internal/search/ \
//     -run TestRecallDecomp_Rerank -v -timeout 60m

package search

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

	"github.com/townsendmerino/aikit/bm25"
	"github.com/townsendmerino/aikit/encoder"
	"github.com/townsendmerino/aikit/fuse"
)

// Cutoffs for the pipeline-recall curve. 1..10 is the user-facing range;
// 20/50/100 expose how much of the miss is recoverable by surfacing
// deeper (and, since hybridSearch over-fetches k*5, a wider pool too).
var decompPipeKs = []int{1, 3, 5, 10, 20, 50, 100}

// Pool sizes for the candidate-generation ceiling. N is the per-arm
// fetch depth; the pool is the union of the top-N semantic and top-N
// BM25 candidates. recall@10 search uses N=50 internally (k*5), so
// decompPoolNs[0]=50 is the ceiling the DEFAULT pipeline actually draws
// from; 100/200/500 show what widening the net would buy.
var decompPoolNs = []int{50, 100, 200, 500}

type decompRepo struct {
	Name          string `json:"name"`
	Language      string `json:"language"`
	BenchmarkRoot string `json:"benchmark_root"`
}

// Relevant is []json.RawMessage because semble's annotations mix two
// qrel shapes within the same file: bare strings ("lib/core/x.js",
// file-level) and objects ({path,start_line,end_line}, line-level). For
// file-level recall we only need the path; decompTargets normalizes both.
type decompTask struct {
	Query    string            `json:"query"`
	Relevant []json.RawMessage `json:"relevant"`
	Category string            `json:"category"`
}

// decompTargets flattens semble's mixed string/object qrel elements to
// the set of target file paths.
func decompTargets(raws []json.RawMessage) []string {
	var out []string
	for _, r := range raws {
		var s string
		if json.Unmarshal(r, &s) == nil {
			out = append(out, s)
			continue
		}
		var o struct {
			Path string `json:"path"`
		}
		if json.Unmarshal(r, &o) == nil && o.Path != "" {
			out = append(out, o.Path)
		}
	}
	return out
}

// decompAccum tallies hits for one query class (symbol / nl / all).
type decompAccum struct {
	n         int
	pipeHit   map[int]int // K -> #queries with a hit in pipeline top-K
	poolHit   map[int]int // N -> #queries with a hit in the N-deep pool
	penOffHit int         // #queries with a hit in penalty-OFF top-10
}

func newDecompAccum() *decompAccum {
	return &decompAccum{pipeHit: map[int]int{}, poolHit: map[int]int{}}
}

func TestRecallDecomp(t *testing.T) {
	semblePath, corpusRoot, modelDir, repos := decompSetup(t)
	_ = semblePath

	classes := map[string]*decompAccum{
		"all":    newDecompAccum(),
		"nl":     newDecompAccum(),
		"symbol": newDecompAccum(),
	}

	tStart := time.Now()
	for ri, repo := range repos {
		repoDir := filepath.Join(corpusRoot, repo.Name)
		if _, err := os.Stat(repoDir); err != nil {
			t.Logf("[%s] skip — corpus dir missing (%s)", repo.Name, repoDir)
			continue
		}
		benchDir := repoDir
		if repo.BenchmarkRoot != "" {
			benchDir = filepath.Join(repoDir, filepath.FromSlash(repo.BenchmarkRoot))
		}
		tasks, err := decompLoadTasks(filepath.Join(semblePath, "benchmarks", "annotations", repo.Name+".json"))
		if err != nil {
			t.Logf("[%s] skip — annotations: %v", repo.Name, err)
			continue
		}
		if len(tasks) == 0 {
			continue
		}

		tRepo := time.Now()
		ix, err := FromPath(benchDir, ModeHybrid, "regex", modelDir)
		if err != nil {
			t.Logf("[%s] skip — index build: %v", repo.Name, err)
			continue
		}
		if ix.model == nil || ix.flat == nil {
			t.Fatalf("[%s] expected hybrid index but model/flat nil — model dir %q not usable", repo.Name, modelDir)
		}

		for _, task := range tasks {
			targets := decompTargets(task.Relevant)
			if len(targets) == 0 {
				continue
			}
			cls := "nl"
			if isSymbolQuery(task.Query) {
				cls = "symbol"
			}
			qVec := ix.model.Encode(task.Query)

			// Pipeline recall@K — the real, full pipeline at each K.
			pipe := map[int]bool{}
			for _, k := range decompPipeKs {
				res, _ := ix.SearchMode(task.Query, k, ModeHybrid)
				pipe[k] = decompAnyMatch(res, targets)
			}
			// Pool recall@N — candidate-generation ceiling, no penalties,
			// no boosts, no truncation: is the target anywhere in the
			// union of the top-N semantic and top-N BM25 candidates?
			pool := map[int]bool{}
			for _, n := range decompPoolNs {
				pool[n] = decompPoolHit(ix, qVec, task.Query, targets, n)
			}
			// Penalty-OFF recall@10 — identical pipeline to pipe[10]
			// except path penalties + file-saturation are disabled.
			penOff := decompPenaltyOffTop10Hit(ix, task.Query, qVec, targets)

			for _, name := range []string{"all", cls} {
				a := classes[name]
				a.n++
				for _, k := range decompPipeKs {
					if pipe[k] {
						a.pipeHit[k]++
					}
				}
				for _, n := range decompPoolNs {
					if pool[n] {
						a.poolHit[n]++
					}
				}
				if penOff {
					a.penOffHit++
				}
			}
		}
		t.Logf("[%d/%d %s, %s] %d chunks, %d tasks (%.1fs)",
			ri+1, len(repos), repo.Name, repo.Language, ix.Len(), len(tasks), time.Since(tRepo).Seconds())
	}

	t.Logf("decomposition done in %.1fs", time.Since(tStart).Seconds())
	for _, name := range []string{"all", "nl", "symbol"} {
		decompReport(t, name, classes[name])
	}
	decompWriteJSON(t, classes)
}

// decompPoolHit reports whether any target file appears in the union of
// the top-N semantic candidates and the top-N (positive-score) BM25
// candidates — the raw candidate pool, before any fusion/boost/penalty.
func decompPoolHit(ix *Index, qVec []float32, query string, targets []string, n int) bool {
	for _, h := range ix.flat.Query(qVec, n) {
		if decompTargetMatch(ix.chunks[h.Index].File, targets) {
			return true
		}
	}
	for _, r := range ix.bm.TopK(bm25.Tokenize(query), n) {
		if r.Score > 0 && decompTargetMatch(ix.chunks[r.Doc].File, targets) {
			return true
		}
	}
	return false
}

// decompPenaltyOffTop10Hit replays hybridSearch's body verbatim with the
// SAME k*5 candidate pool, fusion, and boosts — flipping only the final
// rerankTopK's penalisePaths flag to false. The delta vs pipeline
// recall@10 is purely the path-penalty + file-saturation cost.
func decompPenaltyOffTop10Hit(ix *Index, query string, qVec []float32, targets []string) bool {
	const topK = 10
	alpha := resolveAlpha(query, -1)
	candidateCount := topK * 5

	var semOrder []int
	for _, h := range ix.flat.Query(qVec, candidateCount) {
		semOrder = append(semOrder, h.Index)
	}
	var bmOrder []int
	for _, r := range ix.bm.TopK(bm25.Tokenize(query), candidateCount) {
		if r.Score > 0 {
			bmOrder = append(bmOrder, r.Doc)
		}
	}
	fused := fuse.RRFWeighted(fuse.DefaultK, []float64{alpha, 1.0 - alpha}, semOrder, bmOrder)
	combined := make(map[int]float64, len(fused))
	for _, r := range fused {
		combined[r.Key] = r.Score
	}
	boostMultiChunkFiles(combined, ix.chunks)
	combined = applyQueryBoost(combined, query, ix.chunks, nil)
	ranked := rerankTopK(combined, ix.chunks, topK, false) // penalties OFF
	for _, r := range ranked {
		if decompTargetMatch(ix.chunks[r.idx].File, targets) {
			return true
		}
	}
	return false
}

func decompAnyMatch(results []Result, targets []string) bool {
	for _, r := range results {
		if decompTargetMatch(r.Chunk.File, targets) {
			return true
		}
	}
	return false
}

// decompTargetMatch mirrors semble benchmarks/data.py path_matches:
// suffix-aware either-direction match (qrel targets are repo-rooted,
// ken's chunk.File is benchmark-root-relative).
func decompTargetMatch(filePath string, targets []string) bool {
	f := strings.ReplaceAll(filePath, "\\", "/")
	for _, t := range targets {
		tt := strings.ReplaceAll(t, "\\", "/")
		if f == tt || strings.HasSuffix(f, "/"+tt) || strings.HasSuffix(tt, "/"+f) {
			return true
		}
	}
	return false
}

func decompReport(t *testing.T, name string, a *decompAccum) {
	if a.n == 0 {
		t.Logf("=== %s: no queries ===", name)
		return
	}
	frac := func(c int) float64 { return float64(c) / float64(a.n) }
	maxN := decompPoolNs[len(decompPoolNs)-1]

	var pipe strings.Builder
	for _, k := range decompPipeKs {
		fmt.Fprintf(&pipe, " @%d=%.3f", k, frac(a.pipeHit[k]))
	}
	var pool strings.Builder
	for _, n := range decompPoolNs {
		fmt.Fprintf(&pool, " N=%d:%.3f", n, frac(a.poolHit[n]))
	}

	pipe10 := frac(a.pipeHit[10])
	pool50 := frac(a.poolHit[50])    // pool the DEFAULT k=10 pipeline draws from
	poolMax := frac(a.poolHit[maxN]) // widest net measured
	penOff10 := frac(a.penOffHit)

	t.Logf("================ %s  (n=%d) ================", name, a.n)
	t.Logf("  pipeline recall:%s", pipe.String())
	t.Logf("  pool recall:    %s", pool.String())
	t.Logf("  penalty-OFF recall@10: %.3f   (pipeline @10: %.3f)", penOff10, pipe10)
	t.Logf("  --- decomposition of the recall@10 miss (1 - %.3f = %.3f) ---", pipe10, 1-pipe10)
	t.Logf("    candidate-gen loss (1 - pool@50)        = %.3f   [target never in the k=10 pool; needs wider retrieval/embeddings/HyDE]", 1-pool50)
	t.Logf("    ranking loss within k=10 pool (pool@50 - pipe@10) = %.3f   [in pool, ranked >10; recoverable by rerank/fusion]", pool50-pipe10)
	t.Logf("    of which penalties cost (penOff@10 - pipe@10)     = %.3f   [demoted out of top-10 by path penalties/saturation]", penOff10-pipe10)
	t.Logf("    headroom from widening pool 50->%d (pool@%d - pool@50) = %.3f   [extra targets a wider net would expose]", maxN, maxN, poolMax-pool50)
	t.Logf("    hard ceiling at N=%d (pool@%d)           = %.3f   [(1-this)=%.3f needs better retrieval, full stop]", maxN, maxN, poolMax, 1-poolMax)
}

// TestRecallDecomp_BM25Baseline reproduces the docs/BENCH.md token-budget
// recall@10 (NL ~0.82) to confirm it is a BM25-ONLY number — the
// token-budget harness builds search.ModeBM25 — and quantify how much
// the semantic arm adds. Pipeline recall only (BM25 has no flat/model
// for the pool/penalty arms).
func TestRecallDecomp_BM25Baseline(t *testing.T) {
	semblePath, corpusRoot, modelDir, repos := decompSetup(t)
	_ = modelDir

	classes := map[string]*decompAccum{
		"all": newDecompAccum(), "nl": newDecompAccum(), "symbol": newDecompAccum(),
	}
	for _, repo := range repos {
		repoDir := filepath.Join(corpusRoot, repo.Name)
		if _, err := os.Stat(repoDir); err != nil {
			continue
		}
		benchDir := repoDir
		if repo.BenchmarkRoot != "" {
			benchDir = filepath.Join(repoDir, filepath.FromSlash(repo.BenchmarkRoot))
		}
		tasks, err := decompLoadTasks(filepath.Join(semblePath, "benchmarks", "annotations", repo.Name+".json"))
		if err != nil || len(tasks) == 0 {
			continue
		}
		ix, err := FromPath(benchDir, ModeBM25, "regex", "")
		if err != nil {
			continue
		}
		for _, task := range tasks {
			targets := decompTargets(task.Relevant)
			if len(targets) == 0 {
				continue
			}
			cls := "nl"
			if isSymbolQuery(task.Query) {
				cls = "symbol"
			}
			for _, name := range []string{"all", cls} {
				a := classes[name]
				a.n++
				for _, k := range decompPipeKs {
					res, _ := ix.SearchMode(task.Query, k, ModeBM25)
					if decompAnyMatch(res, targets) {
						a.pipeHit[k]++
					}
				}
			}
		}
	}
	for _, name := range []string{"all", "nl", "symbol"} {
		a := classes[name]
		if a.n == 0 {
			continue
		}
		var b strings.Builder
		for _, k := range decompPipeKs {
			fmt.Fprintf(&b, " @%d=%.3f", k, float64(a.pipeHit[k])/float64(a.n))
		}
		t.Logf("BM25-only %s (n=%d) pipeline recall:%s", name, a.n, b.String())
	}
}

// ---- the slow, opt-in neural-rerank arm -----------------------------------

func TestRecallDecomp_Rerank(t *testing.T) {
	if os.Getenv("KEN_RERANK") != "1" {
		t.Skip("set KEN_RERANK=1 to run the slow neural-rerank arm (~30s/query COLD before the M9 cache warms)")
	}
	semblePath, corpusRoot, modelDir, repos := decompSetup(t)

	rerankModelDir := os.Getenv("KEN_RERANK_MODEL_DIR")
	if rerankModelDir == "" {
		rerankModelDir = filepath.Join(os.Getenv("HOME"), ".ken", "rerank-model")
	}
	if _, err := os.Stat(filepath.Join(rerankModelDir, "model.safetensors")); err != nil {
		t.Skipf("no rerank model at %s (run `ken download-model --rerank`)", rerankModelDir)
	}

	// Keep the cost bounded: a few repos, a global query cap. The
	// reranker REORDERS the stage-1 top-N, so it can only recover
	// ranking loss — and only for targets already inside rerankN.
	repoLimit := decompEnvInt("KEN_DECOMP_RERANK_REPO_LIMIT", 3)
	queryCap := decompEnvInt("KEN_DECOMP_RERANK_QUERY_LIMIT", 30)
	rerankN := decompEnvInt("KEN_DECOMP_RERANK_N", 50)
	betas := []float64{0.25, 0.5, 1.0}

	rm, err := encoder.Load(rerankModelDir)
	if err != nil {
		t.Fatalf("encoder.Load: %v", err)
	}

	if repoLimit < len(repos) {
		repos = repos[:repoLimit]
	}

	// hits[beta] = #queries with a hit in rerank top-10; plus baselines.
	hitsRerank := map[float64]int{}
	var n, hitHybrid10, hitPoolN int
	tStart := time.Now()

	for _, repo := range repos {
		if n >= queryCap {
			break
		}
		repoDir := filepath.Join(corpusRoot, repo.Name)
		if _, err := os.Stat(repoDir); err != nil {
			continue
		}
		benchDir := repoDir
		if repo.BenchmarkRoot != "" {
			benchDir = filepath.Join(repoDir, filepath.FromSlash(repo.BenchmarkRoot))
		}
		tasks, err := decompLoadTasks(filepath.Join(semblePath, "benchmarks", "annotations", repo.Name+".json"))
		if err != nil || len(tasks) == 0 {
			continue
		}
		ix, err := FromPath(benchDir, ModeHybrid, "regex", modelDir)
		if err != nil {
			continue
		}

		for _, task := range tasks {
			if n >= queryCap {
				break
			}
			targets := decompTargets(task.Relevant)
			if len(targets) == 0 {
				n--
				continue
			}
			qVec := ix.model.Encode(task.Query)

			// Baselines: stage-1 hybrid top-10, and "is the target even
			// inside the rerankN window the reranker can see?"
			base, _ := ix.SearchMode(task.Query, 10, ModeHybrid)
			if decompAnyMatch(base, targets) {
				hitHybrid10++
			}
			if decompPoolHit(ix, qVec, task.Query, targets, rerankN) {
				hitPoolN++
			}

			for _, beta := range betas {
				ix.SetReranker(NewNeuralReranker(rm), WithRerankN(rerankN), WithRerankBlendBeta(beta))
				res, _ := ix.SearchMode(task.Query, 10, ModeHybridRerank)
				if decompAnyMatch(res, targets) {
					hitsRerank[beta]++
				}
			}
			if n%5 == 0 {
				t.Logf("  ...%d/%d queries (%.0fs elapsed)", n, queryCap, time.Since(tStart).Seconds())
			}
		}
	}

	if n == 0 {
		t.Fatal("no queries measured")
	}
	frac := func(c int) float64 { return float64(c) / float64(n) }
	t.Logf("================ neural-rerank arm (n=%d, rerankN=%d, %.0fs) ================", n, rerankN, time.Since(tStart).Seconds())
	t.Logf("  stage-1 hybrid recall@10:        %.3f", frac(hitHybrid10))
	t.Logf("  target-in-rerankN-window (pool@%d): %.3f   [ceiling the reranker can possibly reach]", rerankN, frac(hitPoolN))
	for _, beta := range betas {
		t.Logf("  hybrid-rerank recall@10 β=%.2f:   %.3f   (Δ vs stage-1 = %+.3f)", beta, frac(hitsRerank[beta]), frac(hitsRerank[beta])-frac(hitHybrid10))
	}
}

// ---- shared setup + loaders -----------------------------------------------

func decompSetup(t *testing.T) (semblePath, corpusRoot, modelDir string, repos []decompRepo) {
	t.Helper()
	semblePath = os.Getenv("SEMBLE_CHECKOUT")
	if semblePath == "" {
		semblePath = "/tmp/semble"
	}
	reposPath := filepath.Join(semblePath, "benchmarks", "repos.json")
	if _, err := os.Stat(reposPath); err != nil {
		t.Skipf("missing %s — clone semble to /tmp/semble or set SEMBLE_CHECKOUT", reposPath)
	}
	corpusRoot = os.Getenv("KEN_SEMBLE_CORPUS_ROOT")
	if corpusRoot == "" {
		corpusRoot = filepath.Join(os.Getenv("HOME"), ".cache", "semble-bench")
	}
	if _, err := os.Stat(corpusRoot); err != nil {
		t.Skipf("missing corpus root %s — run `python %s/benchmarks/sync_repos.py`", corpusRoot, semblePath)
	}
	modelDir = os.Getenv("KEN_MODEL_DIR")
	if modelDir == "" {
		if d := filepath.Join(os.Getenv("HOME"), ".ken", "model"); decompHasModel(d) {
			modelDir = d
		} else if decompHasModel("testdata/model") {
			modelDir = "testdata/model"
		}
	}
	if !decompHasModel(modelDir) {
		t.Skipf("no potion-code-16M model found (set KEN_MODEL_DIR; tried ~/.ken/model and testdata/model)")
	}

	data, err := os.ReadFile(reposPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &repos); err != nil {
		t.Fatalf("parse repos.json: %v", err)
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].Name < repos[j].Name })
	if lim := decompEnvInt("KEN_DECOMP_REPO_LIMIT", 0); lim > 0 && lim < len(repos) {
		repos = repos[:lim]
	}
	t.Logf("decomp: %d repos, corpus=%s, model=%s", len(repos), corpusRoot, modelDir)
	return semblePath, corpusRoot, modelDir, repos
}

func decompHasModel(dir string) bool {
	if dir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(dir, "model.safetensors"))
	return err == nil
}

// decompLoadTasks keeps only tasks with at least one positive qrel
// target — file-level recall is undefined without one.
func decompLoadTasks(path string) ([]decompTask, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var all []decompTask
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	keep := all[:0]
	for _, tk := range all {
		if len(tk.Relevant) > 0 {
			keep = append(keep, tk)
		}
	}
	return keep, nil
}

func decompEnvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func decompWriteJSON(t *testing.T, classes map[string]*decompAccum) {
	type out struct {
		N         int         `json:"n"`
		PipeHit   map[int]int `json:"pipe_hit"`
		PoolHit   map[int]int `json:"pool_hit"`
		PenOffHit int         `json:"penalty_off_hit_at10"`
		PipeKs    []int       `json:"pipe_ks"`
		PoolNs    []int       `json:"pool_ns"`
	}
	payload := map[string]out{}
	for name, a := range classes {
		payload[name] = out{N: a.n, PipeHit: a.pipeHit, PoolHit: a.poolHit, PenOffHit: a.penOffHit, PipeKs: decompPipeKs, PoolNs: decompPoolNs}
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		t.Logf("marshal results: %v", err)
		return
	}
	dst := filepath.Join(os.TempDir(), "ken-recall-decomp.json")
	if err := os.WriteFile(dst, b, 0o644); err != nil {
		t.Logf("write results: %v", err)
		return
	}
	t.Logf("wrote raw decomposition counts to %s", dst)
}
