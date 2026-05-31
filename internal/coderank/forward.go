package coderank

// forward runs the full CodeRankEmbed transformer on a single token
// sequence and returns the raw (UN-normalized) CLS hidden state.
//
// Caller is responsible for L2-normalizing if the consumer is cosine
// (the M0 harness does), and for owning the token-id slice (forward
// does not mutate `ids` but treats it as read-only).
//
// Pipeline (plan §6, with the M1-confirmed schema):
//
//	h = WordEmb[ids] + TokenTypeEmb[0]
//	h = LayerNorm(h; EmbLN_W, EmbLN_B, eps)
//	for each of 12 layers:
//	  h = LayerNorm(h + SelfAttention(h);   norm1)   // post-norm
//	  h = LayerNorm(h + SwiGLU_MLP(h);      norm2)   // post-norm
//	return h[0]   // CLS at position 0
//
// Single-sequence only in M2 (no batching, no padding mask). M7 brings
// batched mode if needed.
func (w *Weights) forward(ids []int32) []float32 {
	L := len(ids)
	if L == 0 {
		// Degenerate input — return zero vector (matches the empty-input
		// contract of internal/embed and the model's behavior on an
		// empty string after the tokenizer wraps to just [CLS][SEP],
		// which is L=2 not 0).
		return make([]float32, w.Cfg.HiddenDim)
	}
	D := w.Cfg.HiddenDim
	heads := w.Cfg.NumHeads
	headDim := w.Cfg.HeadDim()
	intermediate := w.Cfg.IntermediateDim
	eps := w.Cfg.LayerNormEpsilon

	// M8c: borrow a scratch arena for this forward's intermediate
	// buffers. Returned to the pool on exit; the same worker's next
	// forward reuses them (typical rerank loop). ensureLayer grows
	// the per-buffer caps to fit this forward's L if needed.
	s := getScratch()
	defer putScratch(s)
	s.ensureLayer(L, D, intermediate, heads, headDim, L)

	// 1) Build the input hidden state h = word_emb[id] + token_type_emb[0].
	// Single-segment input ⇒ every row gets token_type_emb row 0 added.
	h := make([]float32, L*D)
	tte0 := w.TokenTypeEmb[:D] // row 0
	for i, id := range ids {
		if int(id) < 0 || int(id) >= w.Cfg.VocabSize {
			// Defensive — the tokenizer should never emit an OOB id,
			// but a corrupt fixture or future drop-in checkpoint could.
			// Substitute the [UNK] id (100) which is always in range.
			id = 100
		}
		src := w.WordEmb[int(id)*D : int(id)*D+D]
		dst := h[i*D : (i+1)*D]
		for j := 0; j < D; j++ {
			dst[j] = src[j] + tte0[j]
		}
	}
	// 2) Embedding LayerNorm.
	layerNorm(h, w.EmbLN_W, w.EmbLN_B, L, D, eps)

	// 3) Precompute RoPE table once for this seq length.
	rope := newRopeTable(L, headDim, w.Cfg.RoPEBase)

	// 4) Twelve transformer blocks (post-norm).
	for i := 0; i < w.Cfg.NumLayers; i++ {
		l := &w.Layers[i]
		// Attention sub-layer + residual (in place on h).
		selfAttention(h, l.Wqkv, l.OutProj, heads, headDim, D, L, rope, s)
		layerNorm(h, l.Norm1W, l.Norm1B, L, D, eps)
		// MLP sub-layer + residual.
		swigluMLP(h, l.Fc11, l.Fc12, l.Fc2, D, intermediate, L, s)
		layerNorm(h, l.Norm2W, l.Norm2B, L, D, eps)
	}

	// 5) CLS pool: return position 0 (a fresh allocation so the caller
	// can mutate / L2-normalize without aliasing into h).
	cls := make([]float32, D)
	copy(cls, h[:D])
	return cls
}
