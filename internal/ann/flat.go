// Package ann is the dense (semantic) retriever. v1 is a flat brute-force
// cosine scan — the "vicinity" equivalent in docs/DESIGN.md §1. HNSW lands later
// behind this same Hit/Query shape; flat is exact and fine at repo scale.
//
// Invariants the rest of the codebase depends on:
//
//   - **Input vectors are L2-normalized.** embed.StaticModel.Encode
//     normalizes its output before returning, so cosine similarity is
//     just the dot product — Query computes that, not a full
//     ‖a‖‖b‖-divided cosine. Passing non-normalized vectors silently
//     produces incorrect rankings; the precision contract lives at the
//     embed boundary, not here.
//   - **Similarity, not distance.** semble's dense backend (vicinity)
//     returns cosine *distance* (1 − sim) and search.py flips it back to
//     similarity; ken skips the round-trip and scores similarity
//     directly, with "higher = better." Anything reading the Score
//     field must treat it that way.
//   - **Goroutine-safety.** A built *Flat is read-only — Query takes no
//     locks and is safe to call concurrently across goroutines. New is
//     not thread-safe (single builder); Query is.
//   - **No mutation.** There is no Add / Remove / Update API today, by
//     design. Incremental indexing is tracked in DESIGN.md §10; adding
//     it here means breaking the goroutine-safety property unless
//     guarded by a lock, which is part of the cost.
package ann

import "sort"

// Hit is one scored item, highest Score (cosine similarity) first.
type Hit struct {
	Index int
	Score float64
}

// Flat is an exhaustive cosine index over a fixed set of unit vectors.
type Flat struct {
	vecs [][]float32 // each assumed L2-normalized (embed.Encode guarantees this)
	dim  int
}

// New builds a flat index. Vectors are used by reference, not copied.
func New(vecs [][]float32) *Flat {
	d := 0
	if len(vecs) > 0 {
		d = len(vecs[0])
	}
	return &Flat{vecs: vecs, dim: d}
}

// Len is the number of indexed vectors.
func (f *Flat) Len() int { return len(f.vecs) }

// Query returns the k highest cosine-similarity vectors to q, descending,
// ties broken by ascending index for determinism. k<=0 or k>=Len returns
// all, sorted.
func (f *Flat) Query(q []float32, k int) []Hit {
	hits := make([]Hit, 0, len(f.vecs))
	for i, v := range f.vecs {
		if len(v) != len(q) {
			continue // dimension mismatch ⇒ skip rather than panic
		}
		var dot float64
		for j := range v {
			dot += float64(v[j]) * float64(q[j])
		}
		hits = append(hits, Hit{Index: i, Score: dot})
	}
	sort.Slice(hits, func(a, b int) bool {
		if hits[a].Score != hits[b].Score {
			return hits[a].Score > hits[b].Score
		}
		return hits[a].Index < hits[b].Index
	})
	if k > 0 && k < len(hits) {
		hits = hits[:k]
	}
	return hits
}
