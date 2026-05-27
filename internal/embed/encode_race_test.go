package embed

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// TestEncodeConcurrent confirms that *StaticModel.Encode is safe to call
// from multiple goroutines concurrently — the prerequisite for the
// parallelism campaign's Phase A architecture (per ADR-029 + the
// parallelism investigation plan in outputs/).
//
// The code-read audit established three load-bearing safety properties
// that this test empirically confirms with -race:
//
//   - All StaticModel fields are initialized at Load() and never
//     mutated after (read-only access from any goroutine is safe).
//   - encodeIDs allocates fresh per-call slices (rows, w) and reads
//     from m.embeddings as a slice view (shared backing, read-only).
//   - weightedMeanPoolSafe writes only to per-call locals (sum, wsum,
//     out); l2Normalize mutates only its caller-owned input slice.
//   - Tokenizer's vocab + addedTokens are map[string]int32 built at
//     Load and never written after — Go's memory model permits
//     concurrent read-only map access.
//
// If P1 (the parallelism plan's prediction that Encode IS goroutine-
// safe today) is wrong, this test fails under -race or produces a
// non-determinism between concurrent and serial output. P1's
// falsification would force a prerequisite goroutine-safety fix
// before the parallelism campaign's Phase 2 can proceed.
//
// Skip pattern mirrors parity_test.go / golden_test.go — testdata/model
// is per-machine and not committed; a fresh checkout has no model and
// this test must not fail there.
func TestEncodeConcurrent(t *testing.T) {
	modelDir := filepath.Join("..", "..", "testdata", "model")
	if _, err := os.Stat(filepath.Join(modelDir, "model.safetensors")); err != nil {
		t.Skip("testdata/model/ not present; see testdata/README.md")
	}
	m, err := Load(modelDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Realistic input set: varied lengths + vocab coverage to exercise
	// the full encode path (BertNormalizer, BertPreTokenizer, WordPiece,
	// weightedMeanPool, l2Normalize). Pulled from a real Go source file
	// for the same reason the bench tests do — exercises camelCase /
	// PascalCase / acronym / digit splits realistic identifiers
	// produce. Chunked into N=16 segments so each goroutine has its own
	// distinct input (no goroutine accidentally racing on identical
	// inputs that might cancel out a real bug).
	srcPath := filepath.Join("..", "search", "index.go")
	data, err := os.ReadFile(srcPath)
	if err != nil {
		t.Skipf("source not found at %s: %v", srcPath, err)
	}
	src := string(data)
	const numInputs = 16
	inputs := chunkInto(src, numInputs)
	if len(inputs) != numInputs {
		t.Fatalf("chunkInto produced %d inputs, want %d", len(inputs), numInputs)
	}

	// Establish baseline: each input's expected output, computed
	// serially on the same model. This is the parity reference each
	// concurrent goroutine's result must byte-match.
	baseline := make([][]float32, numInputs)
	for i, in := range inputs {
		baseline[i] = m.Encode(in)
	}

	// Run multiple rounds of concurrent encoding so the race detector
	// has multiple opportunities to surface a data race + we exercise
	// scheduler nondeterminism repeatedly. Each round: numInputs
	// goroutines, each encoding a distinct input, results compared to
	// baseline.
	const rounds = 20
	for r := 0; r < rounds; r++ {
		results := make([][]float32, numInputs)
		var wg sync.WaitGroup
		wg.Add(numInputs)
		for i := 0; i < numInputs; i++ {
			i := i // capture per-iteration
			go func() {
				defer wg.Done()
				results[i] = m.Encode(inputs[i])
			}()
		}
		wg.Wait()

		// Byte-equality check: each goroutine's output must match the
		// serial baseline. Any difference indicates either a data race
		// (caught separately by -race) or shared mutable state we
		// missed in the code-read audit.
		for i := 0; i < numInputs; i++ {
			if !reflect.DeepEqual(results[i], baseline[i]) {
				t.Errorf("round %d input %d: concurrent Encode differs from serial baseline",
					r, i)
				t.Logf("  serial   [0:8] = %v", baseline[i][:8])
				t.Logf("  parallel [0:8] = %v", results[i][:8])
				return // one failure is enough; don't spam
			}
		}
	}
}

// chunkInto splits src into n approximately-equal line-grouped pieces.
// Deterministic: same input → same chunks. Used so each goroutine gets
// a distinct input rather than racing on identical strings (which might
// mask a real concurrency bug).
func chunkInto(src string, n int) []string {
	lines := strings.Split(src, "\n")
	if n <= 0 {
		n = 1
	}
	per := len(lines) / n
	if per < 1 {
		per = 1
	}
	out := make([]string, 0, n)
	for i := 0; i < n && i*per < len(lines); i++ {
		end := (i + 1) * per
		if end > len(lines) || i == n-1 {
			end = len(lines) // last chunk takes the remainder
		}
		out = append(out, strings.Join(lines[i*per:end], "\n"))
	}
	return out
}
