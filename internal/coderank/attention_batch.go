package coderank

import "math"

// selfAttentionBatched is the M7 batched variant of selfAttention.
// Layout: h is [B, Lmax, D] flattened row-major to [B*Lmax, D]. The
// linear projections (Wqkv, OutProj) run as one big matmul over
// M=B*Lmax — that's the M7 win on the bandwidth-bound kernels.
// Attention itself stays per-sequence-per-head; realLen[b] bounds the
// inner softmax + ctx loops so padded positions never enter the score
// matrix (no mask tensor needed).
//
// M8c: takes a *scratch sized for B*Lmax rows. qkv/Q/K/V/ctx/out
// reuse scratch buffers across the 12 layers. Per-head qH/kH/vH and
// per-(b,head) scores ARE still allocated inside this function — they
// need different sizes per sequence (realLen[b] varies), and the
// scratch buffers are sized for B*Lmax which is too big. The per-
// (b,head) allocations stay; the BIG allocations (qkv at 3*B*Lmax*D,
// Q/K/V/ctx at B*Lmax*D each, out at B*Lmax*D) are eliminated.
//
// In-place on h: writes h += attentionOutput, leaving the caller to
// apply LayerNorm (post-norm structure, plan §6.2).
func selfAttentionBatched(h []float32, Wqkv, OutProj []float32, heads, headDim, D, B, Lmax int, realLen []int, rope *ropeTable, s *scratch) {
	if heads*headDim != D {
		panic("coderank: heads*headDim != D")
	}
	BL := B * Lmax

	// 1) Project: QKV = h · Wqkvᵀ → [BL, 3D] into scratch.
	qkv := s.qkv[:BL*3*D]
	matmulBTInto(h, Wqkv, qkv, BL, D, 3*D)

	// 2) Split into Q, K, V — each [BL, D] in scratch.
	Q := s.Q[:BL*D]
	K := s.K[:BL*D]
	V := s.V[:BL*D]
	for i := 0; i < BL; i++ {
		copy(Q[i*D:(i+1)*D], qkv[i*3*D:i*3*D+D])
		copy(K[i*D:(i+1)*D], qkv[i*3*D+D:i*3*D+2*D])
		copy(V[i*D:(i+1)*D], qkv[i*3*D+2*D:i*3*D+3*D])
	}

	// 3) RoPE per sequence's [Lmax, D] slice.
	for b := 0; b < B; b++ {
		off := b * Lmax * D
		rope.apply(Q[off:off+Lmax*D], heads)
		rope.apply(K[off:off+Lmax*D], heads)
	}

	scale := float32(1.0 / math.Sqrt(float64(headDim)))
	ctx := s.ctx[:BL*D]
	zeroF32Slice(ctx) // ctx accumulates per-head writes; padded positions stay 0

	// 4) Per-sequence, per-head attention. M11 alloc fix: hoist the
	// four per-(b,head) buffers (qH/kH/vH + scores) to per-CALL
	// reusables. Sized once to (Lmax * headDim) and (Lmax * Lmax) —
	// the upper bound across all (b, head) iterations — then sliced
	// down to the (L * headDim) / (L * L) actual shape each iter.
	// Pre-M11 this loop allocated 4 fresh slices per iteration:
	// at heads=12, B=50 cands, 12 layers = 28,800 allocs per forward.
	// Profile-confirmed as the dominant alloc hotspot (~50% of total).
	qH := s.qH[:Lmax*headDim]
	kH := s.kH[:Lmax*headDim]
	vH := s.vH[:Lmax*headDim]
	scores := s.scores[:Lmax*Lmax]
	for b := 0; b < B; b++ {
		L := realLen[b]
		if L == 0 {
			continue
		}
		seqOff := b * Lmax * D
		for headIdx := 0; headIdx < heads; headIdx++ {
			qH = qH[:L*headDim]
			kH = kH[:L*headDim]
			vH = vH[:L*headDim]
			for i := 0; i < L; i++ {
				src := seqOff + i*D + headIdx*headDim
				copy(qH[i*headDim:(i+1)*headDim], Q[src:src+headDim])
				copy(kH[i*headDim:(i+1)*headDim], K[src:src+headDim])
				copy(vH[i*headDim:(i+1)*headDim], V[src:src+headDim])
			}

			scores = scores[:L*L]
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
					var sV float32
					for j := 0; j < L; j++ {
						sV += scoresRow[j] * vH[j*headDim+d]
					}
					ctx[seqOff+i*D+headIdx*headDim+d] = sV
				}
			}
		}
	}

	// 5) Output projection into scratch.
	out := s.out[:BL*D]
	matmulBTInto(ctx, OutProj, out, BL, D, D)
	// 6) Residual.
	for i := range h {
		h[i] += out[i]
	}
}
