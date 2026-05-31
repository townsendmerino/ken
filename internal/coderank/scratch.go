package coderank

import "sync"

// scratchPool gives every forward-pass goroutine a private reusable
// scratch arena. EncodeBatch's static-partition design (M3 + M7)
// runs one forward at a time per worker, so a Get/Put pair around
// each forward keeps the scratch private to that goroutine for the
// duration of the pass. Subsequent forwards on the same worker
// (typical for warm-cache rerank workloads) reuse the same buffers,
// avoiding the per-layer / per-head allocations that M3+M7 profiles
// showed dominating GC time.
var scratchPool = sync.Pool{
	New: func() any { return &scratch{} },
}

func getScratch() *scratch  { return scratchPool.Get().(*scratch) }
func putScratch(s *scratch) { scratchPool.Put(s) }

// scratch is a per-forward scratchpad of reusable float32 buffers. The
// alternative (allocating inside every selfAttention call) burns ~432
// small mallocs per single-sequence forward (12 layers × 12 heads × 3
// buffers: qH, kH, vH) and a handful of larger ones (qkv, Q, K, V,
// ctx, out, scores). At rerankN=50 that's ~20k mallocs per query —
// real GC pressure visible in M3/M7 traces.
//
// One scratch is held per goroutine. EncodeBatch's static-partition
// design (M3 + M7) means each worker runs one forward at a time, so
// a scratch on the goroutine's stack-allocated *scratch is private.
// Per-call we ENSURE each buffer is at least the size we need, then
// reuse. The buffer slices grow monotonically across the 12 layers
// (within a single forward they stay the same size); the next
// forward on the same worker reuses them.
//
// Buffers are sized for the maximum L and D the forward will see.
// For single-sequence: cap=L; for batched: cap=B*Lmax. Caller passes
// the right cap via ensure*.
type scratch struct {
	// Linear-layer scratch (sized to L*3*D = QKV output, or L*D for
	// projections). qkv is reused across the 12 selfAttention calls;
	// Q/K/V/ctx are split-out per-call buffers.
	qkv []float32 // [L*3*D]
	Q   []float32 // [L*D]
	K   []float32 // [L*D]
	V   []float32 // [L*D]
	ctx []float32 // [L*D]
	out []float32 // [L*D] — attention out_proj output
	// MLP scratch.
	val  []float32 // [L*intermediate]
	gate []float32 // [L*intermediate]
	mid  []float32 // [L*D] — MLP fc2 output
	// Per-head extracts (sized L*headDim each).
	qH []float32
	kH []float32
	vH []float32
	// Attention scores [L, L].
	scores []float32
}

// ensureF32 grows b to capacity n (returning a slice of length n).
// Reuses the underlying array when n ≤ cap(b); allocates a new one
// 25% bigger than n otherwise so subsequent calls with similar
// sizes don't reallocate.
func ensureF32(b []float32, n int) []float32 {
	if cap(b) >= n {
		return b[:n]
	}
	return make([]float32, n, n+n/4)
}

// ensureLayer sizes the scratch buffers for one forward pass.
//
//   - L is the per-row count for the BATCHED scratch slices
//     (qkv/Q/K/V/ctx/out/val/gate/mid) — pass B*Lmax in the batched
//     path, L in the single-seq path.
//   - perHeadLen is the per-(b, head) inner-loop bound, used to size
//     qH/kH/vH/scores. Pass Lmax in the batched path (the longest
//     sequence in the batch) and L in the single-seq path. Pre-M11
//     this was conflated with L which over-sized scores to (B*Lmax)²
//     in the batched path — wasted memory the M11 attention alloc
//     fix surfaced.
func (s *scratch) ensureLayer(L, D, intermediate, heads, headDim, perHeadLen int) {
	s.qkv = ensureF32(s.qkv, L*3*D)
	s.Q = ensureF32(s.Q, L*D)
	s.K = ensureF32(s.K, L*D)
	s.V = ensureF32(s.V, L*D)
	s.ctx = ensureF32(s.ctx, L*D)
	s.out = ensureF32(s.out, L*D)
	s.val = ensureF32(s.val, L*intermediate)
	s.gate = ensureF32(s.gate, L*intermediate)
	s.mid = ensureF32(s.mid, L*D)
	s.qH = ensureF32(s.qH, perHeadLen*headDim)
	s.kH = ensureF32(s.kH, perHeadLen*headDim)
	s.vH = ensureF32(s.vH, perHeadLen*headDim)
	s.scores = ensureF32(s.scores, perHeadLen*perHeadLen)
}
