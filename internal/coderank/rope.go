package coderank

import "math"

// ropeTable holds precomputed cosθ/sinθ for every position up to seqLen
// at the model's configured base (1000 for CodeRankEmbed — NOT the
// usual 10000; see plan §1 and config.json).
//
// Layout: cos[m, d] for m ∈ [0, seqLen), d ∈ [0, headDim/2). Each entry
// covers the (d, d+halfDim) pair under rotate_half.
type ropeTable struct {
	headDim int
	halfDim int
	seqLen  int
	cos     []float32 // [seqLen, halfDim]
	sin     []float32 // [seqLen, halfDim]
}

// newRopeTable precomputes the rotary frequencies for one forward call.
// Cheap; called fresh per Forward so cache size is bounded by the
// largest seqLen ever encountered (typical 512 → 512×32 f32 each = 64 KB
// total — trivially cheaper than reusing a global cache with sync).
func newRopeTable(seqLen, headDim int, base float64) *ropeTable {
	if headDim%2 != 0 {
		panic("coderank: rope headDim must be even")
	}
	half := headDim / 2
	t := &ropeTable{
		headDim: headDim,
		halfDim: half,
		seqLen:  seqLen,
		cos:     make([]float32, seqLen*half),
		sin:     make([]float32, seqLen*half),
	}
	// inv_freq[d] = 1 / base^(2d/headDim)  for d ∈ [0, half)
	// Note: NeoX convention; matches HF rotary_emb_interleaved=false.
	invFreq := make([]float64, half)
	for d := 0; d < half; d++ {
		invFreq[d] = 1.0 / math.Pow(base, float64(2*d)/float64(headDim))
	}
	for m := 0; m < seqLen; m++ {
		row := m * half
		mf := float64(m)
		for d := 0; d < half; d++ {
			theta := mf * invFreq[d]
			t.cos[row+d] = float32(math.Cos(theta))
			t.sin[row+d] = float32(math.Sin(theta))
		}
	}
	return t
}

// applyRoPE rotates x in place. x layout: [seqLen, heads, headDim]
// flattened row-major. Rotate_half on the last axis per position m:
//
//	x1 = x[:half]; x2 = x[half:]
//	out[:half] = x1 * cos[m] - x2 * sin[m]
//	out[half:] = x2 * cos[m] + x1 * sin[m]
//
// This is the same shape PyTorch's NomicBert applies via
// rotary_emb_interleaved=false.
func (t *ropeTable) apply(x []float32, heads int) {
	half := t.halfDim
	hd := t.headDim
	stride := heads * hd // per-position stride
	for m := 0; m < t.seqLen; m++ {
		cosRow := t.cos[m*half : (m+1)*half]
		sinRow := t.sin[m*half : (m+1)*half]
		base := m * stride
		for h := 0; h < heads; h++ {
			off := base + h*hd
			for d := 0; d < half; d++ {
				x1 := x[off+d]
				x2 := x[off+half+d]
				c := cosRow[d]
				s := sinRow[d]
				x[off+d] = x1*c - x2*s
				x[off+half+d] = x2*c + x1*s
			}
		}
	}
}
