package coderank

import "math"

// matmulBT computes dst = a · bᵀ where:
//
//	a: [M, K] row-major
//	b: [N, K] row-major (already transposed relative to the matmul —
//	   this matches PyTorch's [out, in] weight layout, so calls like
//	   `h · Wᵀ` don't need a transpose copy)
//	dst: [M, N] row-major, freshly allocated
//
// Accumulators are float32 (plan §7: f32 GEMM is acceptable when the
// acceptance gate is end-to-end NDCG; cosine-bar loose). For
// reductions where parity matters (LayerNorm, softmax, final L2)
// callers should use float64 explicitly.
//
// M3 dispatch: small or unaligned shapes go straight through the
// i-n-k naive path (it's hard to beat for tiny tiles and avoids the
// blocked kernel's prologue overhead); large shapes route through
// matmulBTBlocked which is ~2-3× faster at the forward-pass shapes
// (verified by BenchmarkMatmulBT_*). The threshold is the FLOP count
// per call; tuned so the layer-7 attention QKᵀ at L=80 (~3 MFLOP) is
// naive while everything bigger is blocked.
func matmulBT(a, b []float32, M, K, N int) []float32 {
	if int64(M)*int64(K)*int64(N) < 4_000_000 {
		return matmulBTNaive(a, b, M, K, N)
	}
	return matmulBTBlocked(a, b, M, K, N)
}

// matmulBTNaive is the correctness-first baseline. Loop order i-n-k:
// inner reduction reads a[i,:] sequentially and b[n,:] sequentially —
// both contiguous in row-major.
func matmulBTNaive(a, b []float32, M, K, N int) []float32 {
	dst := make([]float32, M*N)
	for i := 0; i < M; i++ {
		aRow := a[i*K : (i+1)*K]
		for n := 0; n < N; n++ {
			bRow := b[n*K : (n+1)*K]
			var s float32
			for k := 0; k < K; k++ {
				s += aRow[k] * bRow[k]
			}
			dst[i*N+n] = s
		}
	}
	return dst
}

// matmulBTBlockedInto is matmulBTBlocked writing into a caller-
// provided dst. dst MUST have len ≥ M*N. M8c addition so the
// scratch arena can reuse output buffers across the 12 layers of a
// forward pass instead of allocating M*N*4 bytes per call.
//
// The caller is responsible for zeroing dst (the k-tile accumulation
// pattern is `dst += partial`, so non-zero dst would corrupt the
// result). zeroF32Slice helper does that cheaply.
func matmulBTBlockedInto(a, b, dst []float32, M, K, N int) {
	if len(dst) < M*N {
		panic("coderank: matmulBTBlockedInto dst too small")
	}
	zeroF32Slice(dst[:M*N])
	matmulBTBlockedFillInto(a, b, dst, M, K, N)
}

// matmulBTInto dispatches by FLOP count, same as matmulBT, into a
// caller-provided dst.
func matmulBTInto(a, b, dst []float32, M, K, N int) {
	if int64(M)*int64(K)*int64(N) < 4_000_000 {
		matmulBTNaiveInto(a, b, dst, M, K, N)
		return
	}
	matmulBTBlockedInto(a, b, dst, M, K, N)
}

func matmulBTNaiveInto(a, b, dst []float32, M, K, N int) {
	if len(dst) < M*N {
		panic("coderank: matmulBTNaiveInto dst too small")
	}
	for i := 0; i < M; i++ {
		aRow := a[i*K : (i+1)*K]
		for n := 0; n < N; n++ {
			bRow := b[n*K : (n+1)*K]
			var s float32
			for k := 0; k < K; k++ {
				s += aRow[k] * bRow[k]
			}
			dst[i*N+n] = s
		}
	}
}

func zeroF32Slice(s []float32) {
	for i := range s {
		s[i] = 0
	}
}

// matmulBTBlocked is the cache-aware version: outer triple-loop over
// (M, N, K) tiles of size (mBlock, nBlock, kBlock). The 4-way-unrolled
// inner micro-kernel is routed through dotNEON4x4 on arm64 (M8b NEON
// asm, 4-6× faster than the equivalent scalar Go loop), with a Go
// fallback on other architectures.
//
// Tile sizes (32, 32, 128) fit one (mBlock×kBlock) tile of a + one
// (nBlock×kBlock) tile of b in M-series L1 (~32 KB). dst is zero-
// initialized so the k-tile accumulation is just `dst[i,n] +=
// partial`. The kBlock alignment requirement: kBlock % 4 == 0 (NEON
// 4-lane vector load); 128 satisfies that.
func matmulBTBlocked(a, b []float32, M, K, N int) []float32 {
	dst := make([]float32, M*N)
	matmulBTBlockedFillInto(a, b, dst, M, K, N)
	return dst
}

// matmulBTBlockedFillInto is the body of matmulBTBlocked, factored
// out for matmulBTBlockedInto's scratch-buffer reuse path. dst MUST
// be zero-initialized at entry (the k-tile loop accumulates).
func matmulBTBlockedFillInto(a, b, dst []float32, M, K, N int) {
	matmulBTBlockedFillIntoTiled(a, b, dst, M, K, N, mBlockDefault, nBlockDefault, kBlockDefault)
}

// Tile defaults. Tuned via the M10 sweep (outputs/m10-results.md) for
// the M8d 8x4 NEON kernel.
//
// Why 32×32×768:
//
//   - kBlock=768 matches CodeRankEmbed's hidden dimension exactly, so
//     wqkv (K=768), fc11 (K=768), and outproj (K=768) all run with
//     a SINGLE k-tile per inner iteration — zero k-loop overhead.
//     fc2 (K=3072) still wins because 4 tiles of size 768 has far
//     less overhead than 24 tiles of size 128 (the pre-M10 default).
//   - 32×768 a-row tile = 96 KB; 32×768 b-tile = 96 KB; output 32×32 =
//     4 KB. Total live data 196 KB fits in M-series P-core L1 (192 KB)
//     for the whole nBlock iteration.
//   - Larger nBlock (64+) regressed by 5-12% — the b-tile working set
//     grew past L1 fast-path, and the 8x4 kernel already saturates
//     NEON FP-pipes within an mBlock=32 row, so wider N has no
//     pipelining headroom.
//
// Cross-shape measurements (benchstat n=3, M1 Pro):
//
//	Shape                        Pre-M10        M10           Delta
//	L80 wqkv  (K=768, N=2304)    8.21 ms        6.55 ms       -20%
//	L80 fc11  (K=768, N=3072)    11.28 ms       8.83 ms       -22%
//	L80 fc2   (K=3072, N=768)    12.40 ms       9.97 ms       -20%
//	L80 outpj (K=768, N=768)     2.62 ms        2.07 ms       -21%
//
// Pre-M10 (32x32x128) was tuned for the M3-era 4x4 kernel. M8d's 8x4
// kernel processes 2× more N-columns per asm call, which lowered per-
// FLOP load-port pressure and pushed the bottleneck back to k-loop
// overhead — the lever this retune pulls.
const (
	mBlockDefault = 32
	nBlockDefault = 32
	kBlockDefault = 768
)

// matmulBTBlockedFillIntoTiled is the parametric variant exposed for
// the M10 tile-sweep benchmark. Production callers should use the
// defaults via matmulBTBlockedFillInto. kBlock MUST be a multiple of
// 4 (the NEON 4-lane vector load); nBlock benefits from being a
// multiple of 8 (the 8x4 kernel's primary unroll); mBlock has the
// loosest constraint (any positive int).
func matmulBTBlockedFillIntoTiled(a, b, dst []float32, M, K, N, mBlock, nBlock, kBlock int) {
	for i0 := 0; i0 < M; i0 += mBlock {
		iEnd := i0 + mBlock
		if iEnd > M {
			iEnd = M
		}
		for n0 := 0; n0 < N; n0 += nBlock {
			nEnd := n0 + nBlock
			if nEnd > N {
				nEnd = N
			}
			for k0 := 0; k0 < K; k0 += kBlock {
				kEnd := k0 + kBlock
				if kEnd > K {
					kEnd = K
				}
				kSpan := kEnd - k0
				// kSpan should be a multiple of 4 because kBlock=128
				// and K (model hidden / intermediate dims) is always
				// a multiple of 4 in practice (64, 768, 3072). The
				// final tile when K isn't a multiple of kBlock might
				// have an odd-sized tail, which the asm handles via
				// its n4 = kSpan/4 split + the inner scalar tail.
				k4 := kSpan / 4
				// Micro-kernel: (iEnd-i0) × (nEnd-n0) tile, K strip [k0, kEnd).
				for i := i0; i < iEnd; i++ {
					aRowPtr := &a[i*K+k0]
					dstRow := dst[i*N+n0 : i*N+nEnd]
					n := n0
					// 8-way unroll on n via the M8d NEON asm helper. One
					// a-load feeds 8 FMLAs → ~1.8× per-row throughput
					// vs the 4-way kernel (see BenchmarkDot8x4_*).
					nEndAligned8 := n0 + ((nEnd-n0)/8)*8
					var sums8 [32]float32
					for ; n < nEndAligned8; n += 8 {
						dotNEON8x4(aRowPtr,
							&b[n*K+k0], &b[(n+1)*K+k0], &b[(n+2)*K+k0], &b[(n+3)*K+k0],
							&b[(n+4)*K+k0], &b[(n+5)*K+k0], &b[(n+6)*K+k0], &b[(n+7)*K+k0],
							k4, &sums8)
						s0 := sums8[0] + sums8[1] + sums8[2] + sums8[3]
						s1 := sums8[4] + sums8[5] + sums8[6] + sums8[7]
						s2 := sums8[8] + sums8[9] + sums8[10] + sums8[11]
						s3 := sums8[12] + sums8[13] + sums8[14] + sums8[15]
						s4 := sums8[16] + sums8[17] + sums8[18] + sums8[19]
						s5 := sums8[20] + sums8[21] + sums8[22] + sums8[23]
						s6 := sums8[24] + sums8[25] + sums8[26] + sums8[27]
						s7 := sums8[28] + sums8[29] + sums8[30] + sums8[31]
						for k := k4 * 4; k < kSpan; k++ {
							av := a[i*K+k0+k]
							s0 += av * b[n*K+k0+k]
							s1 += av * b[(n+1)*K+k0+k]
							s2 += av * b[(n+2)*K+k0+k]
							s3 += av * b[(n+3)*K+k0+k]
							s4 += av * b[(n+4)*K+k0+k]
							s5 += av * b[(n+5)*K+k0+k]
							s6 += av * b[(n+6)*K+k0+k]
							s7 += av * b[(n+7)*K+k0+k]
						}
						dstRow[n-n0+0] += s0
						dstRow[n-n0+1] += s1
						dstRow[n-n0+2] += s2
						dstRow[n-n0+3] += s3
						dstRow[n-n0+4] += s4
						dstRow[n-n0+5] += s5
						dstRow[n-n0+6] += s6
						dstRow[n-n0+7] += s7
					}
					// 4-way unroll for the n-tail of 4..7 cols.
					nEndAligned4 := n0 + ((nEnd-n0)/4)*4
					var sums [16]float32
					for ; n < nEndAligned4; n += 4 {
						dotNEON4x4(aRowPtr,
							&b[n*K+k0], &b[(n+1)*K+k0], &b[(n+2)*K+k0], &b[(n+3)*K+k0],
							k4, &sums)
						s0 := sums[0] + sums[1] + sums[2] + sums[3]
						s1 := sums[4] + sums[5] + sums[6] + sums[7]
						s2 := sums[8] + sums[9] + sums[10] + sums[11]
						s3 := sums[12] + sums[13] + sums[14] + sums[15]
						for k := k4 * 4; k < kSpan; k++ {
							av := a[i*K+k0+k]
							s0 += av * b[n*K+k0+k]
							s1 += av * b[(n+1)*K+k0+k]
							s2 += av * b[(n+2)*K+k0+k]
							s3 += av * b[(n+3)*K+k0+k]
						}
						dstRow[n-n0+0] += s0
						dstRow[n-n0+1] += s1
						dstRow[n-n0+2] += s2
						dstRow[n-n0+3] += s3
					}
					// Scalar n-tail when (nEnd-n0) % 4 != 0.
					for ; n < nEnd; n++ {
						bRow := b[n*K+k0 : n*K+kEnd]
						var s float32
						for k := 0; k < kSpan; k++ {
							s += a[i*K+k0+k] * bRow[k]
						}
						dstRow[n-n0] += s
					}
				}
			}
		}
	}
}

// matmulAdd is matmulBT + an additive bias broadcast across rows:
// dst[i, n] += bias[n]. No-op when bias is nil. Kept separate from
// matmulBT so the hot, no-bias path (all CodeRankEmbed weights have
// no bias) stays tight.
func addBias(dst []float32, bias []float32, M, N int) {
	if bias == nil {
		return
	}
	for i := 0; i < M; i++ {
		row := dst[i*N : (i+1)*N]
		for n := 0; n < N; n++ {
			row[n] += bias[n]
		}
	}
}

// softmaxRow normalizes row in-place along its last axis, using
// max-subtraction for stability and float64 accumulators (plan §6:
// softmax accumulates in f64). row is modified in place.
func softmaxRow(row []float32) {
	if len(row) == 0 {
		return
	}
	// max for stability
	maxV := row[0]
	for _, v := range row[1:] {
		if v > maxV {
			maxV = v
		}
	}
	// sum of exp in f64
	var sum float64
	for i, v := range row {
		e := math.Exp(float64(v - maxV))
		row[i] = float32(e)
		sum += e
	}
	if sum == 0 {
		// degenerate — all-equal-to-zero output keeps the vector finite
		for i := range row {
			row[i] = 1.0 / float32(len(row))
		}
		return
	}
	inv := float32(1.0 / sum)
	for i := range row {
		row[i] *= inv
	}
}

// silu returns x · sigmoid(x), the SwiGLU gate activation. f64
// internally to avoid float32 saturation on large |x|.
func silu(x float32) float32 {
	xf := float64(x)
	return float32(xf / (1.0 + math.Exp(-xf)))
}
