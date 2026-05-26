package embed

import "math"

// l2NormEps avoids div-by-zero on degenerate (all-zero) vectors. Matches the
// convention used by sentence-transformers and a typical eps for float32.
const l2NormEps = 1e-12

// l2Normalize divides v by its L2 norm in-place, returning v. If the L2 norm
// is below l2NormEps (degenerate / all-UNK input), v is zeroed and returned
// as a stable, safe value rather than NaN.
//
// The Python reference returns NaN for the all-UNK case; ken returns zero to
// keep downstream cosine-similarity computations well-behaved.
//
// Precision: the sum-of-squares accumulator is float64. Float32 accumulation
// here would compound the drift introduced by float32 accumulation in
// weightedMeanPoolSafe.
func l2Normalize(v []float32) []float32 {
	var sq float64
	for _, x := range v {
		sq += float64(x) * float64(x)
	}
	norm := math.Sqrt(sq)
	if norm < l2NormEps {
		for i := range v {
			v[i] = 0
		}
		return v
	}
	for i, x := range v {
		v[i] = float32(float64(x) / norm)
	}
	return v
}
