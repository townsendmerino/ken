package coderank

import "math"

// layerNorm applies y = (x - mean) / sqrt(var + eps) * weight + bias
// per row of x in-place, matching torch.nn.LayerNorm semantics over the
// last (hidden) axis. Mean and variance accumulate in float64 — this
// is the single most parity-sensitive op outside the GEMMs and the
// place where float32 accumulation visibly drifts on long sequences.
//
//	x:      [L, D] row-major, modified in place
//	weight: [D] gain (γ)
//	bias:   [D] shift (β)
//	eps:    1e-12 for CodeRankEmbed (from config.layer_norm_epsilon)
//
// The unbiased=False variance (divisor D, not D-1) matches PyTorch's
// default and is the load-bearing choice.
func layerNorm(x, weight, bias []float32, L, D int, eps float64) {
	for i := 0; i < L; i++ {
		row := x[i*D : (i+1)*D]
		// mean in f64
		var mean float64
		for _, v := range row {
			mean += float64(v)
		}
		mean /= float64(D)
		// variance in f64 (divisor D — PyTorch default unbiased=False)
		var variance float64
		for _, v := range row {
			d := float64(v) - mean
			variance += d * d
		}
		variance /= float64(D)
		invStd := 1.0 / math.Sqrt(variance+eps)
		for j, v := range row {
			row[j] = float32(((float64(v)-mean)*invStd)*float64(weight[j]) + float64(bias[j]))
		}
	}
}

// l2Normalize divides each row of x by its L2 norm in place. f64
// accumulator. Zero-norm rows (degenerate inputs like empty string)
// stay all-zero — never produce NaN.
func l2Normalize(x []float32, L, D int) {
	for i := 0; i < L; i++ {
		row := x[i*D : (i+1)*D]
		var sumSq float64
		for _, v := range row {
			sumSq += float64(v) * float64(v)
		}
		if sumSq == 0 {
			continue
		}
		inv := float32(1.0 / math.Sqrt(sumSq))
		for j := range row {
			row[j] *= inv
		}
	}
}
