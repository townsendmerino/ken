package search

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/townsendmerino/ken/internal/embed"
)

// TestBuildDeterminism_CrossRun is the in-tree regression net for the
// parallelism campaign's determinism contract (ADR-030 / Phase A). It
// builds the same corpus N times via the production parallel
// walkAndChunkFSWithModel and asserts the serialized index bytes are
// byte-identical across all N runs.
//
// CRITICAL CAVEAT: this is a SMOKE test, not the determinism proof.
// testdata/repo/ is 3 files — determinism bugs in parallel merges
// often only manifest under contention (many files, many workers
// racing), which 3 files at 8 workers barely exercises. The
// load-bearing determinism evidence is the N=20 builds at semble
// medium scale (378,524 chunks) captured in ADR-030 — that's where
// contention is real. This test is here to catch obvious regressions
// in the parallel path (e.g., someone accidentally introducing a
// non-deterministic map iteration in the collector) cheaply enough
// to run on every `go test ./...`.
//
// The pre-Phase-2 history of this test (when it was named
// TestBuildParity_ParallelVsSerial) compared the parallel impl
// against the serial impl via an env-var gate. Phase 2 made parallel
// the default and removed the env var; the serial impl no longer
// exists in tree. So this test now proves only cross-run determinism
// of the parallel path — not parity with a serial reference. The
// historical serial parity is preserved by the one-time N=20
// medium-scale comparison documented in ADR-030.
//
// Skip pattern: hybrid variant skips if testdata/model/ is absent
// (mirrors parity_test.go); bm25 variant runs without a model.
func TestBuildDeterminism_CrossRun(t *testing.T) {
	cases := []struct {
		name    string
		fixture string
		mode    Mode
	}{
		// Small fixture (3 files): tiny smoke test, exercises minimal
		// worker contention. Catches obvious regressions (e.g., a
		// non-deterministic map iteration in the collector).
		{"smoke-bm25", filepath.Join("..", "..", "testdata", "repo"), ModeBM25},
		{"smoke-hybrid", filepath.Join("..", "..", "testdata", "repo"), ModeHybrid},

		// Bigger fixture (the internal/ tree — hundreds of Go files at
		// varied paths). Exercises real worker contention: with N=8
		// workers on hundreds of files, the per-file collector slot
		// writes happen concurrently. This is the in-tree contention
		// stress that the 3-file smoke can't deliver. Still not the
		// load-bearing determinism proof (that's the N=20 at semble
		// medium, ~378k chunks, captured in ADR-030), but a meaningful
		// step up in contention surface.
		{"contention-bm25", filepath.Join("..", ".."), ModeBM25},
	}

	const runs = 5 // smoke; the load-bearing N=20 lives in ADR-030

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := os.Stat(tc.fixture); err != nil {
				t.Skipf("fixture %q not found", tc.fixture)
			}
			opts := BuildOptions{
				Mode:    tc.mode,
				Chunker: "regex",
			}
			if tc.mode.needsModel() {
				modelDir := filepath.Join("..", "..", "testdata", "model")
				if _, err := os.Stat(filepath.Join(modelDir, "model.safetensors")); err != nil {
					t.Skip("testdata/model/ not present; see testdata/README.md")
				}
				m, err := embed.LoadFromFS(os.DirFS(modelDir), ".")
				if err != nil {
					t.Fatalf("embed.LoadFromFS: %v", err)
				}
				opts.Model = m
			}

			reference, err := BuildAndSerializeIndex(os.DirFS(tc.fixture), opts)
			if err != nil {
				t.Fatalf("reference build: %v", err)
			}

			for i := 1; i < runs; i++ {
				got, err := BuildAndSerializeIndex(os.DirFS(tc.fixture), opts)
				if err != nil {
					t.Fatalf("run %d build: %v", i, err)
				}
				if !bytes.Equal(reference, got) {
					t.Fatalf("run %d serialized bytes differ from reference (%d vs %d bytes)\n"+
						"This breaks the determinism contract Phase A's parallel build is "+
						"supposed to preserve. The collector's file-index-ordered reassembly "+
						"may have regressed.",
						i, len(reference), len(got))
				}
			}
		})
	}
}
