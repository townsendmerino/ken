package coderank

import "math"

// Per-row symmetric int8 weight quantization. Plan §7 / §M8 int8 path.
//
// Why per-row symmetric:
//
//   - SYMMETRIC: zero stays at zero (no zero-point bookkeeping). Code-
//     embedding weights are roughly mean-zero per output channel, so
//     symmetric loses very little precision vs asymmetric.
//   - PER-ROW (= per-output-channel): each weight row gets its own scale.
//     The dynamic range of W[i,:] varies a lot across output channels
//     (e.g., embedding rows for rare tokens have small max, common ones
//     large); a single global scale would force the rare-row weights to
//     round to 0. Per-row is the standard "per-channel" quantization
//     bitsandbytes / GPTQ / etc. use.
//   - INT8 (range [-127, 127]): the standard. We never quantize to -128
//     because then -(-128) overflows; clamping to [-127, 127] avoids the
//     edge case for ~0% accuracy cost.
//
// Reconstruction quality: TestQuantize_roundTrip pins relative L2 error
// per row to ≤ 1e-2 on uniform-random and normal-random weights, which
// the model card's weights comfortably satisfy.

// quantizeRowsInt8 quantizes a [rows, cols] f32 matrix (row-major) to
// int8 weights + per-row f32 scales. Returns:
//
//	q       — [rows*cols] int8, same row-major layout
//	scales  — [rows] f32, scale[i] = max(|W[i,:]|) / 127
//
// To reconstruct: W_approx[i,j] = float32(q[i*cols+j]) * scales[i].
//
// Empty rows (all-zero) get scale=1 — the encoded values are all zero
// so reconstruction is exactly zero; the scale value doesn't matter.
func quantizeRowsInt8(w []float32, rows, cols int) (q []int8, scales []float32) {
	if rows*cols != len(w) {
		panic("coderank: quantizeRowsInt8 shape mismatch")
	}
	q = make([]int8, rows*cols)
	scales = make([]float32, rows)
	for i := 0; i < rows; i++ {
		row := w[i*cols : (i+1)*cols]
		// max(|row|) — find the dynamic range of this row.
		var maxAbs float32
		for _, v := range row {
			a := v
			if a < 0 {
				a = -a
			}
			if a > maxAbs {
				maxAbs = a
			}
		}
		if maxAbs == 0 {
			scales[i] = 1
			// q row stays all-zero
			continue
		}
		s := maxAbs / 127.0
		scales[i] = s
		inv := 1.0 / s
		off := i * cols
		for j, v := range row {
			x := math.Round(float64(v * inv))
			if x > 127 {
				x = 127
			} else if x < -127 {
				x = -127
			}
			q[off+j] = int8(x)
		}
	}
	return q, scales
}

// dequantizeRowsInt8 is the reconstruction. Mostly for tests and
// debugging; the production forward pass dequantizes on-the-fly inside
// matmulBTQ8 so we never materialize the full f32 weight back into
// memory (defeats the M8 storage win).
func dequantizeRowsInt8(q []int8, scales []float32, rows, cols int) []float32 {
	if rows*cols != len(q) || rows != len(scales) {
		panic("coderank: dequantizeRowsInt8 shape mismatch")
	}
	w := make([]float32, rows*cols)
	for i := 0; i < rows; i++ {
		s := scales[i]
		off := i * cols
		for j := 0; j < cols; j++ {
			w[off+j] = float32(q[off+j]) * s
		}
	}
	return w
}
