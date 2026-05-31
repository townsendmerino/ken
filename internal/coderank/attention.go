package coderank

import "math"

// selfAttention runs one block's bidirectional multi-head self-attention
// and adds the output to the residual `h`. Writes the result back into
// `h` so the caller can apply the post-attention LayerNorm in place
// (post-norm structure, plan §6.2).
//
// Shapes throughout this function:
//
//	h:        [L, D]                  hidden states (D = HiddenDim = heads * headDim)
//	Wqkv:     [3D, D]                 fused Q/K/V input projection (PyTorch layout)
//	OutProj:  [D, D]                  attention output projection
//	heads, headDim: D / heads, D / heads
//	rope:     precomputed cos/sin for positions 0..L-1
//
// M8c: takes a *scratch arena so qkv / Q / K / V / ctx / qH/kH/vH /
// scores buffers are reused across the 12 layers per forward (and
// across forwards on the same worker via sync.Pool). Caller must have
// run s.ensureLayer(L, D, intermediate, heads, headDim) at the top of
// the forward.
func selfAttention(h []float32, Wqkv, OutProj []float32, heads, headDim, D, L int, rope *ropeTable, s *scratch) {
	if heads*headDim != D {
		panic("coderank: heads*headDim != D")
	}
	// 1) Project: QKV = h · Wqkvᵀ   -> [L, 3D] into scratch.
	qkv := s.qkv[:L*3*D]
	matmulBTInto(h, Wqkv, qkv, L, D, 3*D)

	// 2) Split QKV into Q, K, V — each [L, D]. Reuse scratch buffers.
	Q := s.Q[:L*D]
	K := s.K[:L*D]
	V := s.V[:L*D]
	for i := 0; i < L; i++ {
		copy(Q[i*D:(i+1)*D], qkv[i*3*D:i*3*D+D])
		copy(K[i*D:(i+1)*D], qkv[i*3*D+D:i*3*D+2*D])
		copy(V[i*D:(i+1)*D], qkv[i*3*D+2*D:i*3*D+3*D])
	}

	// 3) RoPE on Q and K per head, full headDim, rotate_half.
	rope.apply(Q, heads)
	rope.apply(K, heads)

	// 4) Scaled dot-product attention per head. Scratch holds qH/kH/vH
	// (per-head extracts) and scores ([L, L]).
	scale := float32(1.0 / math.Sqrt(float64(headDim)))
	ctx := s.ctx[:L*D]
	zeroF32Slice(ctx) // ctx accumulates per-head writes
	qH := s.qH[:L*headDim]
	kH := s.kH[:L*headDim]
	vH := s.vH[:L*headDim]
	scores := s.scores[:L*L]

	for headIdx := 0; headIdx < heads; headIdx++ {
		for i := 0; i < L; i++ {
			src := i*D + headIdx*headDim
			copy(qH[i*headDim:(i+1)*headDim], Q[src:src+headDim])
			copy(kH[i*headDim:(i+1)*headDim], K[src:src+headDim])
			copy(vH[i*headDim:(i+1)*headDim], V[src:src+headDim])
		}
		matmulBTInto(qH, kH, scores, L, headDim, L)
		for i := range scores {
			scores[i] *= scale
		}
		for i := 0; i < L; i++ {
			softmaxRow(scores[i*L : (i+1)*L])
		}
		for i := 0; i < L; i++ {
			scoresRow := scores[i*L : (i+1)*L]
			for d := 0; d < headDim; d++ {
				var s float32
				for j := 0; j < L; j++ {
					s += scoresRow[j] * vH[j*headDim+d]
				}
				ctx[i*D+headIdx*headDim+d] = s
			}
		}
	}

	// 5) Output projection into scratch.
	out := s.out[:L*D]
	matmulBTInto(ctx, OutProj, out, L, D, D)

	// 6) Residual: h += out (in place).
	for i := range h {
		h[i] += out[i]
	}
}
