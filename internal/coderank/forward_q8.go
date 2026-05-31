package coderank

import "math"

// (*WeightsQ8).forward runs the int8 forward pass on a single sequence.
// Same pipeline as Weights.forward; the only difference is the 5 big
// linear layers route through matmulBTQ8 instead of matmulBT. LN,
// softmax, RoPE, residual adds, and CLS pool all stay f32 — those are
// either tiny ops or parity-sensitive reductions where f64 accumulators
// matter.
func (w *WeightsQ8) forward(ids []int32) []float32 {
	L := len(ids)
	D := w.Cfg.HiddenDim
	if L == 0 {
		return make([]float32, D)
	}
	heads := w.Cfg.NumHeads
	headDim := w.Cfg.HeadDim()
	intermediate := w.Cfg.IntermediateDim
	eps := w.Cfg.LayerNormEpsilon

	h := make([]float32, L*D)
	tte0 := w.TokenTypeEmb[:D]
	for i, id := range ids {
		if int(id) < 0 || int(id) >= w.Cfg.VocabSize {
			id = 100
		}
		src := w.WordEmb[int(id)*D : int(id)*D+D]
		dst := h[i*D : (i+1)*D]
		for j := 0; j < D; j++ {
			dst[j] = src[j] + tte0[j]
		}
	}
	layerNorm(h, w.EmbLN_W, w.EmbLN_B, L, D, eps)
	rope := newRopeTable(L, headDim, w.Cfg.RoPEBase)
	for i := 0; i < w.Cfg.NumLayers; i++ {
		l := &w.Layers[i]
		selfAttentionQ8(h, l.WqkvQ, l.WqkvScales, l.OutProjQ, l.OutProjScales,
			heads, headDim, D, L, rope)
		layerNorm(h, l.Norm1W, l.Norm1B, L, D, eps)
		swigluMLPQ8(h, l.Fc11Q, l.Fc11Scales, l.Fc12Q, l.Fc12Scales,
			l.Fc2Q, l.Fc2Scales, D, intermediate, L)
		layerNorm(h, l.Norm2W, l.Norm2B, L, D, eps)
	}
	cls := make([]float32, D)
	copy(cls, h[:D])
	return cls
}

// (*WeightsQ8).forwardBatch is the int8 + batched (M7) combination.
// Single-thread runs B sequences as one big batch through the 12
// layers, with linear projections in int8 and attention per-sequence-
// per-head bounded by realLen[b].
func (w *WeightsQ8) forwardBatch(idsList [][]int32) [][]float32 {
	B := len(idsList)
	D := w.Cfg.HiddenDim
	if B == 0 {
		return nil
	}
	if B == 1 {
		return [][]float32{w.forward(idsList[0])}
	}
	Lmax := 0
	realLen := make([]int, B)
	for b, ids := range idsList {
		realLen[b] = len(ids)
		if len(ids) > Lmax {
			Lmax = len(ids)
		}
	}
	if Lmax == 0 {
		out := make([][]float32, B)
		for i := range out {
			out[i] = make([]float32, D)
		}
		return out
	}
	heads := w.Cfg.NumHeads
	headDim := w.Cfg.HeadDim()
	intermediate := w.Cfg.IntermediateDim
	eps := w.Cfg.LayerNormEpsilon

	h := make([]float32, B*Lmax*D)
	tte0 := w.TokenTypeEmb[:D]
	for b, ids := range idsList {
		base := b * Lmax * D
		for i, id := range ids {
			if int(id) < 0 || int(id) >= w.Cfg.VocabSize {
				id = 100
			}
			src := w.WordEmb[int(id)*D : int(id)*D+D]
			dst := h[base+i*D : base+(i+1)*D]
			for j := 0; j < D; j++ {
				dst[j] = src[j] + tte0[j]
			}
		}
	}
	layerNorm(h, w.EmbLN_W, w.EmbLN_B, B*Lmax, D, eps)
	rope := newRopeTable(Lmax, headDim, w.Cfg.RoPEBase)
	for li := 0; li < w.Cfg.NumLayers; li++ {
		l := &w.Layers[li]
		selfAttentionQ8Batched(h, l.WqkvQ, l.WqkvScales, l.OutProjQ, l.OutProjScales,
			heads, headDim, D, B, Lmax, realLen, rope)
		layerNorm(h, l.Norm1W, l.Norm1B, B*Lmax, D, eps)
		swigluMLPQ8(h, l.Fc11Q, l.Fc11Scales, l.Fc12Q, l.Fc12Scales,
			l.Fc2Q, l.Fc2Scales, D, intermediate, B*Lmax)
		layerNorm(h, l.Norm2W, l.Norm2B, B*Lmax, D, eps)
	}
	out := make([][]float32, B)
	for b := 0; b < B; b++ {
		out[b] = make([]float32, D)
		copy(out[b], h[b*Lmax*D:b*Lmax*D+D])
	}
	return out
}

// selfAttentionQ8 is selfAttention's M8 sibling: identical structure,
// Wqkv and OutProj routed through matmulBTQ8. Attention itself stays
// f32 (the small per-head matmuls are tiny — quantizing them would
// add scale-multiply overhead for no gain).
func selfAttentionQ8(h []float32, WqkvQ []int8, WqkvScales []float32,
	OutProjQ []int8, OutProjScales []float32,
	heads, headDim, D, L int, rope *ropeTable) {
	qkv := matmulBTQ8(h, WqkvQ, WqkvScales, L, D, 3*D)
	Q := make([]float32, L*D)
	K := make([]float32, L*D)
	V := make([]float32, L*D)
	for i := 0; i < L; i++ {
		copy(Q[i*D:(i+1)*D], qkv[i*3*D:i*3*D+D])
		copy(K[i*D:(i+1)*D], qkv[i*3*D+D:i*3*D+2*D])
		copy(V[i*D:(i+1)*D], qkv[i*3*D+2*D:i*3*D+3*D])
	}
	rope.apply(Q, heads)
	rope.apply(K, heads)

	scale := float32(1.0 / math.Sqrt(float64(headDim)))
	ctx := make([]float32, L*D)
	for headIdx := 0; headIdx < heads; headIdx++ {
		qH := make([]float32, L*headDim)
		kH := make([]float32, L*headDim)
		vH := make([]float32, L*headDim)
		for i := 0; i < L; i++ {
			src := i*D + headIdx*headDim
			copy(qH[i*headDim:(i+1)*headDim], Q[src:src+headDim])
			copy(kH[i*headDim:(i+1)*headDim], K[src:src+headDim])
			copy(vH[i*headDim:(i+1)*headDim], V[src:src+headDim])
		}
		raw := matmulBT(qH, kH, L, headDim, L)
		for i := range raw {
			raw[i] *= scale
		}
		for i := 0; i < L; i++ {
			softmaxRow(raw[i*L : (i+1)*L])
		}
		for i := 0; i < L; i++ {
			scoresRow := raw[i*L : (i+1)*L]
			for d := 0; d < headDim; d++ {
				var s float32
				for j := 0; j < L; j++ {
					s += scoresRow[j] * vH[j*headDim+d]
				}
				ctx[i*D+headIdx*headDim+d] = s
			}
		}
	}
	out := matmulBTQ8(ctx, OutProjQ, OutProjScales, L, D, D)
	for i := range h {
		h[i] += out[i]
	}
}

// selfAttentionQ8Batched is the int8 + batched attention sibling
// (combines M7 batched-linear and M8 int8 paths).
func selfAttentionQ8Batched(h []float32, WqkvQ []int8, WqkvScales []float32,
	OutProjQ []int8, OutProjScales []float32,
	heads, headDim, D, B, Lmax int, realLen []int, rope *ropeTable) {
	BL := B * Lmax
	qkv := matmulBTQ8(h, WqkvQ, WqkvScales, BL, D, 3*D)
	Q := make([]float32, BL*D)
	K := make([]float32, BL*D)
	V := make([]float32, BL*D)
	for i := 0; i < BL; i++ {
		copy(Q[i*D:(i+1)*D], qkv[i*3*D:i*3*D+D])
		copy(K[i*D:(i+1)*D], qkv[i*3*D+D:i*3*D+2*D])
		copy(V[i*D:(i+1)*D], qkv[i*3*D+2*D:i*3*D+3*D])
	}
	for b := 0; b < B; b++ {
		off := b * Lmax * D
		rope.apply(Q[off:off+Lmax*D], heads)
		rope.apply(K[off:off+Lmax*D], heads)
	}
	scale := float32(1.0 / math.Sqrt(float64(headDim)))
	ctx := make([]float32, BL*D)
	for b := 0; b < B; b++ {
		L := realLen[b]
		if L == 0 {
			continue
		}
		seqOff := b * Lmax * D
		for headIdx := 0; headIdx < heads; headIdx++ {
			qH := make([]float32, L*headDim)
			kH := make([]float32, L*headDim)
			vH := make([]float32, L*headDim)
			for i := 0; i < L; i++ {
				src := seqOff + i*D + headIdx*headDim
				copy(qH[i*headDim:(i+1)*headDim], Q[src:src+headDim])
				copy(kH[i*headDim:(i+1)*headDim], K[src:src+headDim])
				copy(vH[i*headDim:(i+1)*headDim], V[src:src+headDim])
			}
			scores := matmulBT(qH, kH, L, headDim, L)
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
					ctx[seqOff+i*D+headIdx*headDim+d] = s
				}
			}
		}
	}
	out := matmulBTQ8(ctx, OutProjQ, OutProjScales, BL, D, D)
	for i := range h {
		h[i] += out[i]
	}
}

// swigluMLPQ8 is swigluMLP with fc11/fc12/fc2 routed through matmulBTQ8.
// L here is the row count for batched callers (BL = B*Lmax); the
// single-sequence path passes L directly. fc1 element-wise SiLU and
// the multiplicative gate stay f32.
func swigluMLPQ8(h []float32, Fc11Q []int8, Fc11Scales []float32,
	Fc12Q []int8, Fc12Scales []float32,
	Fc2Q []int8, Fc2Scales []float32,
	D, intermediate, L int) {
	val := matmulBTQ8(h, Fc11Q, Fc11Scales, L, D, intermediate)
	gate := matmulBTQ8(h, Fc12Q, Fc12Scales, L, D, intermediate)
	for i, v := range val {
		val[i] = v * silu(gate[i])
	}
	out := matmulBTQ8(val, Fc2Q, Fc2Scales, L, intermediate, D)
	for i := range h {
		h[i] += out[i]
	}
}
