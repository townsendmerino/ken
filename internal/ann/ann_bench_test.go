package ann

import (
	"math"
	"math/rand/v2"
	"testing"
)

// dimForBench matches Model2Vec's potion-code-16M output dimension.
// Synthetic vectors at this width keep the benchmark realistic relative
// to what ken's embed pipeline actually feeds Flat in production.
const dimForBench = 128

// makeUnitVectors generates n L2-normalized random vectors of dim d
// using a seeded PRNG. Determinism: same (n, d, seed) → identical
// vectors, so benchstat compares across runs cleanly.
//
// The Flat type's invariant (vectors must be L2-normalized) is enforced
// at the embed boundary in production; we honour it here so the
// benchmark exercises the same code path the production retriever does.
func makeUnitVectors(n, d int, seed uint64) [][]float32 {
	rng := rand.New(rand.NewPCG(seed, seed^0xdeadbeef))
	out := make([][]float32, n)
	for i := range out {
		v := make([]float32, d)
		var sumsq float64
		for j := range v {
			x := float32(rng.NormFloat64())
			v[j] = x
			sumsq += float64(x) * float64(x)
		}
		inv := float32(1.0 / math.Sqrt(sumsq))
		for j := range v {
			v[j] *= inv
		}
		out[i] = v
	}
	return out
}

func BenchmarkFlatQuery(b *testing.B) {
	// N values per the briefing: prediction #3 says flat search becomes
	// intractable at chromium-scale (~millions of chunks). The per-N
	// curve here lets a Phase-1 benchstat run trace the O(N·D) line and
	// pick a hand-off threshold for HNSW (DESIGN.md §10 future work).
	//
	// Max N is 50k rather than the briefing's 100k — judgment call #3,
	// dropping to keep a single bench iteration under a couple seconds
	// on this laptop. Phase 1 can bump back to 100k if the harness lives
	// on better hardware.
	cases := []struct {
		name string
		n    int
	}{
		{"N1k", 1_000},
		{"N10k", 10_000},
		{"N50k", 50_000},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			corpus := makeUnitVectors(tc.n, dimForBench, 0xfeed)
			query := makeUnitVectors(1, dimForBench, 0xdade)[0]
			f := New(corpus)
			// SetBytes is the per-iteration work: N vectors × dim × 4 B
			// (float32 dot products). benchstat MB/s then reads as
			// flat-scan memory bandwidth.
			b.SetBytes(int64(tc.n) * int64(dimForBench) * 4)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = f.Query(query, 10)
			}
		})
	}
}
