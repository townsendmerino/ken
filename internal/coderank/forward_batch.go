package coderank

// forwardBatch runs the transformer on B token sequences as a single
// batched pass, returning B CLS hidden-state vectors. This is the M7
// optimization the plan §7 §M7 calls for: one matmul per layer over
// [B*Lmax, D] instead of B separate [L_b, D] forwards, so the dominant
// linear layers (Wqkv, OutProj, fc11, fc12, fc2 — together ~75× more
// FLOPs than attention at L=80) amortize per-call overhead and stay
// in cache across the 12 blocks.
//
// Design choices:
//
//   - Padding strategy: rectangular pad-to-max(L_b) using id=0 [PAD].
//     A length-bucketing pass would cut wasted attention compute on
//     very ragged batches, but rerank batches are typically uniform
//     (M3 measured 80-token candidates ± a small spread), so the
//     marginal win is small relative to bucket-bookkeeping complexity.
//     Filed as a future optimization; current code-path is correct on
//     any batch shape.
//
//   - Attention stays per-sequence-per-head (the existing 12-head loop)
//     rather than a 4D batched-attention kernel. Attention is O(L²)
//     and typical L=80 makes it ~1% of the layer's FLOPs; batching it
//     for a 1% saving doesn't justify the indexing complexity. The
//     ragged-length attention loop reads each sequence's real L from
//     realLen[b], so padded positions never enter softmax (no mask
//     bookkeeping needed — we just stop at L).
//
//   - Empty sequences (L=0) and B=0 short-circuit to zero outputs.
//     B=1 falls through to the single-sequence forward (avoids paying
//     the batched-buffer allocation when there's no batching benefit).
//
// Outputs match forward(ids) per-element to within float32 reduction
// order noise (the matmul tile sizes change with M, so the f32
// accumulation differs by ULPs; cosine vs reference still > 0.997
// per the M2 contract — pinned by TestForwardBatch_matchesSingle).
func (w *Weights) forwardBatch(idsList [][]int32) [][]float32 {
	B := len(idsList)
	D := w.Cfg.HiddenDim
	if B == 0 {
		return nil
	}
	if B == 1 {
		// Fast path: no batching benefit, skip the padding overhead.
		return [][]float32{w.forward(idsList[0])}
	}

	// Find max L for padding. Track per-sequence real lengths so the
	// attention loop can stop at L instead of needing a mask tensor.
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

	// M8c: scratch arena sized to B*Lmax (the row count the batched
	// forward sees through every linear layer + LayerNorm).
	s := getScratch()
	defer putScratch(s)
	s.ensureLayer(B*Lmax, D, intermediate, heads, headDim, Lmax)

	// 1) Build padded input [B, Lmax, D] flattened to [B*Lmax, D].
	// Pad positions stay all-zero — they get LN'd along with real
	// positions (LN of zero row → bias only, all-equal across pad
	// rows). Since attention skips pad positions (realLen-bounded),
	// downstream layers' pad outputs are never read in the CLS pool.
	h := make([]float32, B*Lmax*D)
	tte0 := w.TokenTypeEmb[:D]
	for b, ids := range idsList {
		base := b * Lmax * D
		for i, id := range ids {
			if int(id) < 0 || int(id) >= w.Cfg.VocabSize {
				id = 100 // [UNK] — defensive (same as forward())
			}
			src := w.WordEmb[int(id)*D : int(id)*D+D]
			dst := h[base+i*D : base+(i+1)*D]
			for j := 0; j < D; j++ {
				dst[j] = src[j] + tte0[j]
			}
		}
	}

	// 2) Embedding LayerNorm — treats every row independently, so
	// [B*Lmax, D] is the natural shape (padded rows get a meaningless
	// but bounded value; never read after attention).
	layerNorm(h, w.EmbLN_W, w.EmbLN_B, B*Lmax, D, eps)

	// 3) Precompute RoPE for Lmax positions. Same table for every
	// sequence — RoPE depends only on position, not content.
	rope := newRopeTable(Lmax, headDim, w.Cfg.RoPEBase)

	// 4) Twelve batched transformer blocks (post-norm).
	for li := 0; li < w.Cfg.NumLayers; li++ {
		l := &w.Layers[li]
		selfAttentionBatched(h, l.Wqkv, l.OutProj, heads, headDim, D, B, Lmax, realLen, rope, s)
		layerNorm(h, l.Norm1W, l.Norm1B, B*Lmax, D, eps)
		swigluMLP(h, l.Fc11, l.Fc12, l.Fc2, D, intermediate, B*Lmax, s)
		layerNorm(h, l.Norm2W, l.Norm2B, B*Lmax, D, eps)
	}

	// 5) CLS pool: extract position 0 of each sequence. Pad positions
	// (i > 0 past realLen[b]) are discarded; we only ever read i=0.
	out := make([][]float32, B)
	for b := 0; b < B; b++ {
		out[b] = make([]float32, D)
		copy(out[b], h[b*Lmax*D:b*Lmax*D+D])
	}
	return out
}
