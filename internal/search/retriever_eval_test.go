//go:build bench

// Retriever evaluation harness — answers "should ken wire aikit's quantized/
// approximate ANN retrievers (FlatI8, FlatBinaryI8, HNSW) into the dense arm,
// and at what scale?" (docs/internal/roadmap-2026-06.md Addendum A2,
// docs/DESIGN.md §10 "HNSW for the dense retriever").
//
// It reuses TestRecallDecomp's corpus/setup machinery (same semble checkout,
// same ~/.cache/semble-bench corpus, same repos.json) but adds a RETRIEVER
// axis instead of a penalty axis: for each repo, it builds the four
// candidate dense retrievers from the SAME vecs ix.FromPath already produced,
// then measures, per retriever:
//
//   - semantic-only candidate recall@N — does the retriever's OWN top-N
//     contain the target, with no BM25/fusion help at all. This is the
//     worst-case exposure (find_related is pure-ANN, no fusion).
//   - end-to-end pipeline recall@10 — hybridSearchWith (below) replays
//     hybridSearch's body verbatim except the semantic arm's concrete
//     *ann.Flat is swapped for the retriever interface. Because
//     aikit/fuse.RRFWeighted only consumes rank order (semOrder is built
//     from h.Index alone — see hybrid.go), an approximate retriever that
//     gets the candidate SET right costs nothing here even if its distances
//     are off, and costs little if it only shuffles order within the pool.
//   - query latency (in-process wall clock per Query call).
//
// Analytic memory (bytes/vector) is reported separately — real semble repos
// top out in the tens of thousands of chunks, too small for RSS deltas to
// separate from GC noise; the byte formulas are exact and don't need to be
// measured. The scale-ramp latency/memory crossover (where each retriever
// starts to beat Flat) is a SEPARATE harness — see retriever_scale_bench_test.go
// — because it needs N far beyond what any single real repo provides.
//
// Run:
//
//	go test -tags=bench ./internal/search/ -run TestRetrieverEval -v -timeout 30m
//
// Optional: KEN_DECOMP_REPO_LIMIT=N to iterate on a subset while developing.
package search

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/townsendmerino/aikit/ann"
	"github.com/townsendmerino/aikit/bm25"
	"github.com/townsendmerino/aikit/fuse"
)

// This harness deliberately reuses the PRODUCTION denseRetriever interface
// and buildDenseRetriever's kinds (index.go) rather than declaring its own
// — the whole point is that these numbers are measuring the exact seam
// Phase 2 wires in, not a parallel approximation of it. hnsw has no
// production denseRetrieverKind (it didn't clear the bar — see the eval
// doc) so it's built directly here for comparison only.
type retrieverVariant struct {
	name  string
	build func(vecs [][]float32) denseRetriever
	// bytesPerVec returns the analytic storage cost per indexed vector for
	// dim d, excluding the read-only chunk/text data every variant shares.
	bytesPerVec func(d int) float64
}

var retrieverVariants = []retrieverVariant{
	{
		name:        "flat-f32",
		build:       func(vecs [][]float32) denseRetriever { return buildDenseRetriever(denseFlat, vecs) },
		bytesPerVec: func(d int) float64 { return 4 * float64(d) },
	},
	{
		name:        "flat-i8",
		build:       func(vecs [][]float32) denseRetriever { return buildDenseRetriever(denseFlatI8, vecs) },
		bytesPerVec: func(d int) float64 { return float64(d) + 4 }, // int8 codes + f32 scale
	},
	{
		name:        "flat-binary-i8",
		build:       func(vecs [][]float32) denseRetriever { return buildDenseRetriever(denseFlatBinaryI8, vecs) },
		bytesPerVec: func(d int) float64 { return float64(d)/8 + float64(d) + 4 }, // binary prefilter codes + full int8 backing
	},
	{
		name:        "hnsw",
		build:       func(vecs [][]float32) denseRetriever { return ann.BuildHNSW(vecs, ann.Config{Seed: 42}) },
		bytesPerVec: func(d int) float64 { return 4*float64(d) + 16*4 + 16*4 }, // f32 vector kept (no RAM win) + graph edges (M=16, ~2 layers avg amortized)
	},
}

// retrieverAccum tallies one (variant, class) cell across all repos.
type retrieverAccum struct {
	n            int
	semHit       map[int]int // N -> #queries whose target is in the retriever's own top-N
	pipeHit10    int         // #queries with a hit in the full pipeline's top-10 (this retriever as the semantic arm)
	queryLatency []time.Duration
}

func newRetrieverAccum() *retrieverAccum {
	return &retrieverAccum{semHit: map[int]int{}}
}

var retrieverSemNs = []int{10, 50, 100}

func TestRetrieverEval(t *testing.T) {
	semblePath, corpusRoot, modelDir, repos := decompSetup(t)
	_ = semblePath

	type cellKey struct{ variant, class string }
	cells := map[cellKey]*retrieverAccum{}
	classes := []string{"all", "nl", "symbol"}
	for _, v := range retrieverVariants {
		for _, c := range classes {
			cells[cellKey{v.name, c}] = newRetrieverAccum()
		}
	}
	buildTimes := map[string]time.Duration{}
	var totalDim int
	var totalVecs int

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
		vecs := ix.Vecs()
		if len(vecs) == 0 {
			continue
		}
		totalDim = ix.model.Dim()
		totalVecs += len(vecs)

		for _, v := range retrieverVariants {
			tBuild := time.Now()
			r := v.build(vecs)
			buildTimes[v.name] += time.Since(tBuild)

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

				tQ := time.Now()
				semHits := r.Query(qVec, retrieverSemNs[len(retrieverSemNs)-1])
				lat := time.Since(tQ)

				semSorted := semHits // Query already returns descending
				pipeHit := hybridSearchAnyMatch(r, ix, qVec, task.Query, targets)

				for _, name := range []string{"all", cls} {
					a := cells[cellKey{v.name, name}]
					a.n++
					a.queryLatency = append(a.queryLatency, lat)
					if pipeHit {
						a.pipeHit10++
					}
					for _, n := range retrieverSemNs {
						nn := n
						if nn > len(semSorted) {
							nn = len(semSorted)
						}
						hit := false
						for _, h := range semSorted[:nn] {
							if decompTargetMatch(ix.chunks[h.Index].File, targets) {
								hit = true
								break
							}
						}
						if hit {
							a.semHit[n]++
						}
					}
				}
			}
		}
		t.Logf("[%d/%d %s, %s] %d chunks, dim %d, %d tasks (%.1fs)",
			ri+1, len(repos), repo.Name, repo.Language, len(vecs), totalDim, len(tasks), time.Since(tRepo).Seconds())
	}
	t.Logf("retriever eval done in %.1fs, %d total vecs across repos, dim=%d", time.Since(tStart).Seconds(), totalVecs, totalDim)

	type report struct {
		Variant       string             `json:"variant"`
		Class         string             `json:"class"`
		N             int                `json:"n_queries"`
		SemRecall     map[string]float64 `json:"sem_recall_at_n"`
		PipeRecall10  float64            `json:"pipeline_recall_at_10"`
		P50LatencyUs  float64            `json:"p50_latency_us"`
		P95LatencyUs  float64            `json:"p95_latency_us"`
		BuildTimeSec  float64            `json:"build_time_sec_total"`
		BytesPerVecMB float64            `json:"bytes_per_vec"`
	}
	var out []report
	for _, v := range retrieverVariants {
		for _, cls := range classes {
			a := cells[cellKey{v.name, cls}]
			if a.n == 0 {
				continue
			}
			sr := map[string]float64{}
			for _, n := range retrieverSemNs {
				sr[fmt.Sprintf("%d", n)] = float64(a.semHit[n]) / float64(a.n)
			}
			sort.Slice(a.queryLatency, func(i, j int) bool { return a.queryLatency[i] < a.queryLatency[j] })
			p50 := a.queryLatency[len(a.queryLatency)*50/100]
			p95 := a.queryLatency[min(len(a.queryLatency)*95/100, len(a.queryLatency)-1)]
			rep := report{
				Variant:       v.name,
				Class:         cls,
				N:             a.n,
				SemRecall:     sr,
				PipeRecall10:  float64(a.pipeHit10) / float64(a.n),
				P50LatencyUs:  float64(p50.Microseconds()),
				P95LatencyUs:  float64(p95.Microseconds()),
				BuildTimeSec:  buildTimes[v.name].Seconds(),
				BytesPerVecMB: v.bytesPerVec(totalDim),
			}
			out = append(out, rep)
			t.Logf("%-16s %-6s n=%-4d sem@10=%.3f sem@50=%.3f sem@100=%.3f pipe@10=%.3f p50=%.0fus p95=%.0fus bytes/vec=%.0f",
				v.name, cls, a.n, sr["10"], sr["50"], sr["100"], rep.PipeRecall10, rep.P50LatencyUs, rep.P95LatencyUs, rep.BytesPerVecMB)
		}
	}

	outPath := filepath.Join(os.TempDir(), "ken-retriever-eval.json")
	if p := os.Getenv("KEN_RETRIEVER_EVAL_OUT"); p != "" {
		outPath = p
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outPath, b, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s", outPath)
}

// hybridSearchAnyMatch replays hybridSearch's body verbatim (same
// candidateOverfetch, same fusion, same boosts, same rerankTopK) except the
// semantic arm is r (the retriever under test) instead of ix.flat, so the
// swap is exactly the one Phase 2 would make in production.
func hybridSearchAnyMatch(r denseRetriever, ix *Index, qVec []float32, query string, targets []string) bool {
	const topK = 10
	alpha := resolveAlpha(query, AdaptiveAlphas)
	candidateCount := topK * candidateOverfetch

	var semOrder []int
	for _, h := range r.Query(qVec, candidateCount) {
		semOrder = append(semOrder, h.Index)
	}
	var bmOrder []int
	for _, br := range ix.bm.TopK(bm25.Tokenize(query), candidateCount) {
		if br.Score > 0 {
			bmOrder = append(bmOrder, br.Doc)
		}
	}
	fused := fuse.RRFWeighted(fuse.DefaultK, []float64{alpha, 1.0 - alpha}, semOrder, bmOrder)
	combined := make(map[int]float64, len(fused))
	for _, fr := range fused {
		combined[fr.Key] = fr.Score
	}
	boostMultiChunkFiles(combined, ix.chunks)
	combined = applyQueryBoost(combined, query, ix.chunks, nil)
	ranked := rerankTopK(combined, ix.chunks, topK, alpha < 1.0)
	for _, item := range ranked {
		if decompTargetMatch(ix.chunks[item.idx].File, targets) {
			return true
		}
	}
	return false
}
