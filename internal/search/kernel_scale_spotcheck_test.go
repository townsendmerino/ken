//go:build bench

// Kernel-scale recall spot-check — closes the one gap ADR-043 /
// docs/internal/dense-retriever-adoption-2026-09.md left open: does
// FlatI8/FlatBinaryI8's approximation hold on REAL, semantically clustered
// code embeddings at real large-corpus scale, not just the synthetic
// near-orthogonal vectors retriever_scale_bench_test.go sweeps? Binary
// Hamming prefiltering is exactly where real near-duplicate/boilerplate
// clustering (common in a codebase the size of the Linux kernel) could
// plausibly behave differently than it does on repo-scale real corpora or
// synthetic random vectors — this test is the direct check, not an
// extrapolation.
//
// Ground truth is exact ann.Flat over the SAME real embeddings this test
// builds — no external relevance judgments are needed, because the
// question is narrower than "does ken's ranking match human relevance"
// (TestRetrieverEval already answers that with real qrels, at repo scale):
// it's "does the approximate retriever agree with exact cosine on real,
// large-scale, clustered data." Two query sets probe that from different
// angles: representative kernel-domain NL/symbol queries (realistic
// usage, encoded fresh via the model), and a random sample of the
// corpus's OWN chunk embeddings used as queries (self-retrieval) — the
// sharper test, since a chunk is its own nearest neighbor and near-exact
// duplicates are precisely what a Hamming prefilter could miss.
//
// Opt-in: set KEN_KERNEL_CORPUS to a real large local checkout (e.g. a
// sparse Linux kernel clone — see scripts/kernel_demo_bench.sh, which
// clones torvalds/linux to $WORK and sets a sparse-checkout scale group).
// Skips cleanly without it; not part of the default `-tags=bench` run.
//
// Run:
//
//	KEN_KERNEL_CORPUS=/tmp/ken-kernel-bench go test -tags=bench ./internal/search/ \
//	    -run TestKernelScaleRecallSpotCheck -v -timeout 30m
package search

import (
	"math/rand"
	"os"
	"runtime"
	"runtime/debug"
	"sort"
	"testing"
	"time"
)

// kernelDemoQueries mirrors scripts/kernel_demo_bench.sh's representative
// query set (NL + symbol-shaped) — the realistic-usage probe.
var kernelDemoQueries = []string{
	"allocate a new inode",
	"how are dirty pages written back to disk",
	"read ahead pages into the page cache",
	"acquire a spinlock with interrupts disabled",
	"TCP retransmission timeout handling",
	"where is the slab allocator freelist managed",
	"mount a filesystem and parse its superblock",
	"copy data from user space safely",
	"schedule the next runnable task",
	"journal commit and checkpoint",
	"ext4_map_blocks",
	"kmem_cache_alloc",
	"tcp_sendmsg",
	"folio_mark_dirty",
	"__vfs_read",
	"try_to_wake_up",
}

func TestKernelScaleRecallSpotCheck(t *testing.T) {
	root := os.Getenv("KEN_KERNEL_CORPUS")
	if root == "" {
		t.Skip("set KEN_KERNEL_CORPUS to a real large corpus checkout to run this spot-check (see file doc comment)")
	}
	modelDir := os.Getenv("KEN_MODEL_DIR")
	if modelDir == "" {
		if d := os.Getenv("HOME") + "/.ken/model"; decompHasModel(d) {
			modelDir = d
		} else if decompHasModel("testdata/model") {
			modelDir = "testdata/model"
		}
	}
	if !decompHasModel(modelDir) {
		t.Skip("no potion-code-16M model found (set KEN_MODEL_DIR; tried ~/.ken/model and testdata/model)")
	}

	tStart := time.Now()
	t.Logf("building hybrid index over %s (started %s)…", root, tStart.Format(time.Kitchen))
	ix, err := FromPath(root, ModeHybrid, "regex", modelDir)
	if err != nil {
		t.Fatalf("FromPath: %v", err)
	}
	vecs := ix.Vecs()
	dim := 0
	if len(vecs) > 0 {
		dim = len(vecs[0])
	}
	t.Logf("indexed %d chunks, dim=%d, in %.1fs", len(vecs), dim, time.Since(tStart).Seconds())
	if len(vecs) < 10 {
		t.Fatalf("too few vectors (%d) for a meaningful spot-check", len(vecs))
	}

	type variant struct {
		name string
		r    denseRetriever
	}
	buildOne := func(name string, kind denseRetrieverKind) variant {
		runtime.GC()
		debug.FreeOSMemory()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		tB := time.Now()
		r := buildDenseRetriever(kind, vecs)
		buildSec := time.Since(tB).Seconds()
		runtime.ReadMemStats(&after)
		deltaMB := float64(int64(after.HeapAlloc)-int64(before.HeapAlloc)) / 1e6
		t.Logf("%-16s build=%.2fs heapDeltaMB=%.1f", name, buildSec, deltaMB)
		return variant{name: name, r: r}
	}

	flatV := buildOne("flat-f32 (ground truth)", denseFlat)
	i8V := buildOne("flat-i8", denseFlatI8)
	bi8V := buildOne("flat-binary-i8", denseFlatBinaryI8)

	const k = 10
	agreeAt := func(candidate denseRetriever, queries [][]float32) (mean float64, p50Us, p95Us float64) {
		var sum float64
		lats := make([]time.Duration, 0, len(queries))
		for _, q := range queries {
			truth := flatV.r.Query(q, k)
			truthSet := make(map[int]bool, k)
			for _, h := range truth {
				truthSet[h.Index] = true
			}
			t0 := time.Now()
			got := candidate.Query(q, k)
			lats = append(lats, time.Since(t0))
			hit := 0
			for _, h := range got {
				if truthSet[h.Index] {
					hit++
				}
			}
			denom := len(truth)
			if denom == 0 {
				continue
			}
			sum += float64(hit) / float64(denom)
		}
		sort.Slice(lats, func(i, j int) bool { return lats[i] < lats[j] })
		p50 := lats[len(lats)*50/100]
		p95 := lats[min(len(lats)*95/100, len(lats)-1)]
		return sum / float64(len(queries)), float64(p50.Microseconds()), float64(p95.Microseconds())
	}

	// Query set 1: realistic kernel-domain NL/symbol queries, freshly encoded.
	realQueries := make([][]float32, 0, len(kernelDemoQueries))
	for _, q := range kernelDemoQueries {
		realQueries = append(realQueries, ix.model.Encode(q))
	}

	// Query set 2: a random sample of the corpus's OWN embeddings as
	// queries — self-retrieval, the sharpest near-duplicate-cluster probe.
	const nSelf = 300
	rng := rand.New(rand.NewSource(42))
	selfQueries := make([][]float32, 0, nSelf)
	for range nSelf {
		selfQueries = append(selfQueries, vecs[rng.Intn(len(vecs))])
	}

	for _, v := range []variant{i8V, bi8V} {
		rMean, rP50, rP95 := agreeAt(v.r, realQueries)
		sMean, sP50, sP95 := agreeAt(v.r, selfQueries)
		t.Logf("%-16s agree@%d(kernel-nl/symbol queries, n=%d)=%.4f p50=%.0fus p95=%.0fus",
			v.name, k, len(realQueries), rMean, rP50, rP95)
		t.Logf("%-16s agree@%d(self-retrieval, n=%d)=%.4f p50=%.0fus p95=%.0fus",
			v.name, k, len(selfQueries), sMean, sP50, sP95)
	}

	// Ground-truth Flat's own query latency for the same two sets, so the
	// agree@k numbers above have a latency baseline to compare against —
	// mirrors retriever_scale_bench_test.go's columns at this real N.
	_, fRP50, fRP95 := agreeAt(flatV.r, realQueries) // trivially agree@k=1.0 vs itself; latency is the point
	_, fSP50, fSP95 := agreeAt(flatV.r, selfQueries)
	t.Logf("%-16s (self-agree, latency baseline) real p50=%.0fus/p95=%.0fus  self p50=%.0fus/p95=%.0fus",
		flatV.name, fRP50, fRP95, fSP50, fSP95)
}
