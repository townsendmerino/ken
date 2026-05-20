package embed

import "math"

// l2NormEps avoids div-by-zero on degenerate (all-zero) vectors. Matches the
// convention used by sentence-transformers and a typical eps for float32.
const l2NormEps = 1e-12

// weightedMeanPool computes Σ rows[i]·weights[i] / Σ weights[i], the
// verified Model2Vec runtime pooling algorithm.
//
// `rows` is a logical [N, dim] view: rows[i] is a slice of length dim into
// the embeddings tensor (F32). `weights` is length N (F64 from the model).
//
// PRECISION CONTRACT (required for golden-test parity):
//   - All accumulators (sum[dim], wsum, the dot product inside L2 norm) are
//     float64. Each `rows[i][j]` (float32) is widened to float64 BEFORE
//     multiplying by w[i] and accumulating.
//   - Output is float32 (cast at the end), matching numpy's
//     `embeddings[...].astype(np.float64) ... .astype(np.float32)` shape
//     in pin_inference.py.
//
// Doing the accumulation in float32 will silently drift cosine below the
// 1 − 1e-5 parity bar on longer inputs. This is the single most likely
// silent failure mode for a re-implementer; do not "optimize" it away.
//
// Returns a zero vector if N==0 or Σweights == 0 (an all-pad / all-zero-weight
// degenerate case).
func weightedMeanPool(rows [][]float32, weights []float64, dim int) []float32 {
	out := make([]float32, dim)
	if len(rows) == 0 {
		return out
	}
	if len(rows) != len(weights) {
		panic("weightedMeanPool: rows and weights length mismatch")
	}

	sum := make([]float64, dim)
	var wsum float64
	for i, row := range rows {
		w := weights[i]
		for j := range dim {
			sum[j] += float64(row[j]) * w
		}
		wsum += w
	}

	if wsum == 0 {
		return out
	}
	for j := range dim {
		out[j] = float32(sum[j] / wsum)
	}
	return out
}

// l2Normalize divides v by its L2 norm in-place, returning v. If the L2 norm
// is below l2NormEps (degenerate / all-UNK input), v is zeroed and returned
// as a stable, safe value rather than NaN.
//
// The Python reference returns NaN for the all-UNK case; ken returns zero to
// keep downstream cosine-similarity computations well-behaved.
//
// Precision: the sum-of-squares accumulator is float64. Float32 accumulation
// here would compound the drift introduced by float32 accumulation in
// weightedMeanPool.
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
