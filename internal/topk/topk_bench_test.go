package topk

import (
	"math/rand/v2"
	"testing"
)

// BenchmarkSelector confirms the O(N log K) scaling claim at the unit
// level before the two integrators (internal/ann, internal/bm25) take
// dependencies. Table-driven across N to make the per-N curve visible
// in benchstat output; K=10 matches ken's production search K.
//
// Deterministic input: seeded PCG so the same (N, K, seed) always
// produces the same input sequence — benchstat diffs across runs stay
// signal, not noise.
func BenchmarkSelector(b *testing.B) {
	const k = 10
	cases := []struct {
		name string
		n    int
	}{
		{"N1k", 1_000},
		{"N10k", 10_000},
		{"N100k", 100_000},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			// Pre-generate the score stream so the per-iteration work
			// is just the heap operations, not the PRNG.
			rng := rand.New(rand.NewPCG(0xc0ffee, 0xfeedface))
			scores := make([]float64, tc.n)
			for i := range scores {
				scores[i] = rng.Float64()
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				sel := New[int](k)
				for j, sc := range scores {
					sel.Push(j, sc)
				}
				_ = sel.Result()
			}
		})
	}
}
