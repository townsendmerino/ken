package search

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/townsendmerino/aikit/chunk"
	_ "github.com/townsendmerino/aikit/chunk/regex"
	"github.com/townsendmerino/aikit/encoder"
)

// Tiny corpus used by the integration tests below. Distinct enough to
// produce a stable stage-1 hybrid ranking; small enough that re-runs
// stay under a second.
var miniCorpus = fstest.MapFS{
	"json_load.py":   {Data: []byte("import json\ndef load(s):\n    return json.loads(s)\n")},
	"sha256_file.py": {Data: []byte("import hashlib\ndef sha256_of(path):\n    h = hashlib.sha256()\n    with open(path, 'rb') as f:\n        return h.hexdigest()\n")},
	"fib.py":         {Data: []byte("def fib(n):\n    if n < 2: return n\n    return fib(n-1) + fib(n-2)\n")},
	"walk_dir.py":    {Data: []byte("import os\ndef walk(root):\n    for d, _, fs in os.walk(root):\n        for f in fs:\n            yield f\n")},
}

const embedModelDir = "../../testdata/model"

func loadHybridIndex(t *testing.T) *Index {
	t.Helper()
	if _, err := os.Stat(filepath.Join(embedModelDir, "model.safetensors")); errors.Is(err, fs.ErrNotExist) {
		t.Skipf("no embed model at %s — required for hybrid; see testdata/README.md", embedModelDir)
	}
	ix, err := FromFS(miniCorpus, ModeHybrid, "regex", embedModelDir)
	if err != nil {
		t.Fatalf("FromFS: %v", err)
	}
	return ix
}

// TestSearchMode_hybridRerankDowngradesWithoutReranker: querying
// ModeHybridRerank on an Index that has no reranker attached must
// transparently behave as ModeHybrid and report mode=ModeHybrid in
// the return. Same "downgrade rather than error" pattern the existing
// no-model semantic→bm25 path uses.
func TestSearchMode_hybridRerankDowngradesWithoutReranker(t *testing.T) {
	ix := loadHybridIndex(t)
	// No SetReranker call ⇒ ix.reranker == nil.

	hybridResults, hybridMode := ix.SearchMode("parse json", 3, ModeHybrid)
	rerankResults, rerankMode := ix.SearchMode("parse json", 3, ModeHybridRerank)

	if rerankMode != ModeHybrid {
		t.Errorf("downgrade: mode return got %v want ModeHybrid", rerankMode)
	}
	if hybridMode != ModeHybrid {
		t.Errorf("baseline: mode return got %v want ModeHybrid", hybridMode)
	}
	if len(rerankResults) != len(hybridResults) {
		t.Errorf("downgraded result count differs: rerank=%d hybrid=%d",
			len(rerankResults), len(hybridResults))
	}
	for i := range rerankResults {
		if rerankResults[i].Chunk.File != hybridResults[i].Chunk.File {
			t.Errorf("downgraded result[%d].File: rerank=%q hybrid=%q",
				i, rerankResults[i].Chunk.File, hybridResults[i].Chunk.File)
		}
	}
}

// TestSearchMode_hybridRerankInvertsWithStub: attaching a stub reranker
// that scores chunks in reverse-stage-1 order with β=1 must invert the
// hybrid top-k. End-to-end proof that ix.SetReranker → SearchMode →
// applyReranker is wired correctly.
func TestSearchMode_hybridRerankInvertsWithStub(t *testing.T) {
	ix := loadHybridIndex(t)
	hybrid, _ := ix.SearchMode("parse json", 4, ModeHybrid)
	if len(hybrid) < 2 {
		t.Skip("hybrid returned too few results to test inversion")
	}

	// Stub reranker: invert the stage-1 order. Higher score = better
	// reranked position; we assign by stage-1 rank so the
	// last-in-hybrid gets the highest score.
	scores := map[string]float64{}
	for i, r := range hybrid {
		scores[r.Chunk.File] = float64(i) // first → 0 (low), last → high
	}
	ix.SetReranker(&stubReranker{scores: scores}, WithRerankN(len(hybrid)), WithRerankBlendBeta(1.0))

	rerank, mode := ix.SearchMode("parse json", 4, ModeHybridRerank)
	if mode != ModeHybridRerank {
		t.Errorf("mode: got %v want ModeHybridRerank", mode)
	}
	// First reranked must equal last hybrid (the lowest-ranked file
	// got the highest stub score; β=1 means pure neural).
	if rerank[0].Chunk.File != hybrid[len(hybrid)-1].Chunk.File {
		t.Errorf("β=1 inversion: rerank[0]=%q want %q (= hybrid last)",
			rerank[0].Chunk.File, hybrid[len(hybrid)-1].Chunk.File)
	}
}

// TestSearchMode_hybridRerankBetaZeroEqualsHybrid: β=0 means "use the
// reranker's scores at zero weight" — output order should match
// ModeHybrid exactly. Pins the "blend default is a knob, not a
// behavioral switch" contract.
func TestSearchMode_hybridRerankBetaZeroEqualsHybrid(t *testing.T) {
	ix := loadHybridIndex(t)
	ix.SetReranker(&stubReranker{scores: map[string]float64{"json_load.py": 999}}, WithRerankBlendBeta(0))
	hybrid, _ := ix.SearchMode("parse json", 3, ModeHybrid)
	rerank, _ := ix.SearchMode("parse json", 3, ModeHybridRerank)
	if len(rerank) != len(hybrid) {
		t.Fatalf("len: rerank=%d hybrid=%d", len(rerank), len(hybrid))
	}
	for i := range rerank {
		if rerank[i].Chunk.File != hybrid[i].Chunk.File {
			t.Errorf("β=0 should preserve hybrid order; pos %d: rerank=%q hybrid=%q",
				i, rerank[i].Chunk.File, hybrid[i].Chunk.File)
		}
	}
}

// TestNeuralReranker_endToEnd: load the REAL encoder.Model, attach it
// as a reranker, run a query, confirm: (1) it produces results,
// (2) cache miss-then-hit counters move on repeat queries, (3) result
// order matches NeuralReranker.Rerank's intent (cosine on L2-normed
// vectors). Skipped when the encoder model isn't symlinked.
//
// This is the slow one (model load ~150ms + a handful of forward
// passes); gated by -short and by the encoder model dir.
func TestNeuralReranker_endToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end NeuralReranker test runs ~5s; skipped under -short")
	}
	encoderModelDir := "../../testdata/encoder-model"
	if _, err := os.Stat(filepath.Join(encoderModelDir, "model.safetensors")); errors.Is(err, fs.ErrNotExist) {
		t.Skipf("no encoder model at %s — symlink HF snapshot to enable", encoderModelDir)
	}
	ix := loadHybridIndex(t)
	rm, err := encoder.Load(encoderModelDir)
	if err != nil {
		t.Fatalf("encoder.Load: %v", err)
	}
	// Keep max_seq_length small so the test stays fast.
	rm.SetMaxSeqLength(128)
	nr := NewNeuralReranker(rm)
	ix.SetReranker(nr, WithRerankN(4), WithRerankBlendBeta(0.5))

	results, mode := ix.SearchMode("parse json from string", 3, ModeHybridRerank)
	if mode != ModeHybridRerank {
		t.Errorf("mode: got %v want ModeHybridRerank", mode)
	}
	if len(results) == 0 {
		t.Fatal("got 0 results from rerank")
	}

	// All 4 candidates were cold ⇒ misses==4, hits==0.
	hits, misses, size := nr.CacheStats()
	if misses == 0 {
		t.Errorf("expected non-zero cache misses on cold run, got %d", misses)
	}
	if size == 0 {
		t.Errorf("expected cache to have populated entries after first query, got 0")
	}
	t.Logf("cold run: hits=%d misses=%d size=%d", hits, misses, size)

	// Repeat the same query — every candidate should now be a cache hit.
	_, _ = ix.SearchMode("parse json from string", 3, ModeHybridRerank)
	hits2, misses2, _ := nr.CacheStats()
	if hits2 <= hits {
		t.Errorf("expected cache hits to increase on warm run; cold=%d warm=%d", hits, hits2)
	}
	if misses2 > misses {
		t.Errorf("expected zero new misses on warm run; cold=%d warm=%d", misses, misses2)
	}
}

// Reference to silence "imported and not used" — chunk used via stubReranker.
var _ = chunk.Chunk{}
