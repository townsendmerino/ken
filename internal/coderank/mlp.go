package coderank

// swigluMLP runs one block's SwiGLU MLP and adds the output to the
// residual `h` (in place). Caller applies the post-MLP LayerNorm.
//
// SwiGLU is (per the reference NomciBertGatedMLP.forward — the
// variable naming in the reference is confusingly inverted vs. typical
// SwiGLU papers):
//
//	val  = h · Fc11ᵀ                  // [L, intermediate]   <- unmodified
//	gate = h · Fc12ᵀ                  // [L, intermediate]   <- activated
//	mid  = val ⊙ SiLU(gate)           // [L, intermediate]
//	out  = mid · Fc2ᵀ                 // [L, D]
//
// CodeRankEmbed's checkpoint stores fc11 and fc12 as TWO separate
// tensors — the plan §6.4 footnote noted "confirm the split convention
// and fc1 width from the checkpoint shapes"; the M1 dump confirmed
// there is no fused fc1, so this is two matmuls (not one + split), and
// the SiLU goes on fc12 (not fc11). The first cut of this file had the
// gate/value naming swapped — caught by the smoke cosine going to
// -0.06; the reference's NomciBertGatedMLP.forward is the source of
// truth, not the SwiGLU paper.
//
// No biases on any of fc11/fc12/fc2 (config: mlp_fc1_bias=false,
// mlp_fc2_bias=false).
//
// M8c: takes a *scratch arena so val / gate / mid buffers are reused
// across the 12 layers per forward.
func swigluMLP(h []float32, Fc11, Fc12, Fc2 []float32, D, intermediate, L int, s *scratch) {
	val := s.val[:L*intermediate]
	gate := s.gate[:L*intermediate]
	matmulBTInto(h, Fc11, val, L, D, intermediate)
	matmulBTInto(h, Fc12, gate, L, D, intermediate)
	// mid = val ⊙ SiLU(gate), reuse val's storage
	for i, v := range val {
		val[i] = v * silu(gate[i])
	}
	mid := s.mid[:L*D]
	matmulBTInto(val, Fc2, mid, L, intermediate, D)
	for i := range h {
		h[i] += mid[i]
	}
}
