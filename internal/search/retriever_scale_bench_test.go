//go:build bench

// Retriever scale-ramp harness — the crossover half of the A2 evaluation
// (see retriever_eval_test.go for the recall half). Real semble repos top
// out around 6-7k chunks (laravel-framework, jackson-databind), far short of
// the kernel-scale N (docs/internal/kernel-demo-feasibility.md) where the
// O(N) flat scan's memory-bandwidth wall actually bites. This harness
// measures LATENCY, BUILD TIME, and MEMORY across a synthetic N ramp at
// ken's real embedding dimension (potion-code-16M, dim=256) — recall is
// deliberately NOT measured here (that needs real semantic structure;
// TestRetrieverEval covers it at repo scale, and aikit's own
// TestFlatI8_recallReal_Model2Vec / TestHNSW_int8RecallGate / etc. cover it
// against real Model2Vec embeddings independent of N).
//
// HNSW is capped at N=200_000: a prior eval (see docs/internal/ findings)
// measured HNSW build time at 49s@50k -> 4m23s@200k on this class of
// machine, already establishing build cost as a dealbreaker for ken's
// rebuild-on-every-change indexing model; running it out to 800k would only
// extend an already-decided point at real risk of exhausting this box's 16GB
// (800k * dim256 * f32 vectors alone is ~800MB before graph overhead).
//
// Run (this is slow — HNSW@200k alone is minutes; heartbeats log per phase):
//
//	go test -tags=bench ./internal/search/ -run TestRetrieverScaleSweep -v -timeout 40m
package search

import (
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"testing"
	"time"
)

const scaleDim = 256 // potion-code-16M, ken's shipped model — see model.go Dim()

// randUnit fills dst with a uniform-random L2-normalized vector (matches the
// "each vector assumed L2-normalized" invariant every aikit/ann retriever
// documents; Model2Vec's own encode() guarantees this on real embeddings).
func randUnit(rng *rand.Rand, dim int) []float32 {
	v := make([]float32, dim)
	var norm float64
	for i := range v {
		x := rng.NormFloat64()
		v[i] = float32(x)
		norm += x * x
	}
	inv := float32(1 / (norm + 1e-12))
	for i := range v {
		v[i] *= inv
	}
	return v
}

func genScaleVecs(n, dim int, seed int64) [][]float32 {
	rng := rand.New(rand.NewSource(seed))
	vecs := make([][]float32, n)
	for i := range vecs {
		vecs[i] = randUnit(rng, dim)
	}
	return vecs
}

type scaleResult struct {
	Variant         string  `json:"variant"`
	N               int     `json:"n"`
	Dim             int     `json:"dim"`
	BuildSec        float64 `json:"build_sec"`
	P50QueryUs      float64 `json:"p50_query_us"`
	P95QueryUs      float64 `json:"p95_query_us"`
	AnalyticMB      float64 `json:"analytic_mb"`
	MeasuredDeltaMB float64 `json:"measured_heap_delta_mb"`
}

func TestRetrieverScaleSweep(t *testing.T) {
	if os.Getenv("KEN_SCALE_SWEEP") == "" {
		t.Skip("opt-in, slow (HNSW@200k is minutes) — set KEN_SCALE_SWEEP=1 to run")
	}
	ns := []int{13_000, 50_000, 200_000, 800_000}
	nQueries := 100
	var out []scaleResult
	tSuiteStart := time.Now()
	t.Logf("scale sweep started %s (dim=%d, ns=%v)", tSuiteStart.Format(time.Kitchen), scaleDim, ns)

	for _, n := range ns {
		t.Logf("=== N=%d: generating vectors (elapsed %.0fs) ===", n, time.Since(tSuiteStart).Seconds())
		vecs := genScaleVecs(n, scaleDim, int64(n))
		queries := genScaleVecs(nQueries, scaleDim, int64(n)+1)

		for _, v := range retrieverVariants {
			if v.name == "hnsw" && n > 200_000 {
				t.Logf("N=%d %s: SKIPPED (capped at 200k — see file doc comment)", n, v.name)
				continue
			}
			runtime.GC()
			debug.FreeOSMemory()
			var msBefore, msAfter runtime.MemStats
			runtime.ReadMemStats(&msBefore)

			tBuild := time.Now()
			r := v.build(vecs)
			buildSec := time.Since(tBuild).Seconds()

			runtime.ReadMemStats(&msAfter)
			deltaMB := float64(int64(msAfter.HeapAlloc)-int64(msBefore.HeapAlloc)) / 1e6

			var lats []time.Duration
			for _, q := range queries {
				t0 := time.Now()
				_ = r.Query(q, 10)
				lats = append(lats, time.Since(t0))
			}
			sort.Slice(lats, func(i, j int) bool { return lats[i] < lats[j] })
			p50 := lats[len(lats)*50/100]
			p95 := lats[min(len(lats)*95/100, len(lats)-1)]
			analyticMB := v.bytesPerVec(scaleDim) * float64(n) / 1e6

			res := scaleResult{
				Variant: v.name, N: n, Dim: scaleDim, BuildSec: buildSec,
				P50QueryUs: float64(p50.Microseconds()), P95QueryUs: float64(p95.Microseconds()),
				AnalyticMB: analyticMB, MeasuredDeltaMB: deltaMB,
			}
			out = append(out, res)
			t.Logf("N=%-7d %-16s build=%6.2fs p50=%7.0fus p95=%7.0fus analyticMB=%8.1f measuredDeltaMB=%8.1f  (total elapsed %.0fs)",
				n, v.name, buildSec, res.P50QueryUs, res.P95QueryUs, analyticMB, deltaMB, time.Since(tSuiteStart).Seconds())

			// Drop the reference so the next variant's GC/RSS reading at
			// this N isn't polluted by the previous retriever's memory.
			r = nil
			_ = r
		}
		vecs = nil
		queries = nil
	}

	t.Logf("scale sweep done in %.0fs", time.Since(tSuiteStart).Seconds())
	outPath := filepath.Join(os.TempDir(), "ken-retriever-scale.json")
	if p := os.Getenv("KEN_SCALE_SWEEP_OUT"); p != "" {
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
