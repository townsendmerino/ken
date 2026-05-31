package coderank

// matmulBTQ8 is the M8 int8-weight variant of matmulBT. Same shape:
// dst = a · bᵀ where a is [M, K] f32 (activations) and b is logically
// [N, K] f32 stored as int8 quantized rows + per-row f32 scales:
//
//	a       — [M, K] f32 row-major (activations, NOT quantized)
//	bQ      — [N*K] int8 row-major (the quantized [N, K] matrix)
//	bScales — [N] f32 (per-row scale: row n's f32 value ≈ float32(bQ[n,k]) * bScales[n])
//	dst     — [M, N] f32 row-major, freshly allocated
//
// The kernel is the M3 blocked-matmul with one twist: weight reads come
// from a tightly-packed int8 array (4× less memory bandwidth than f32),
// the multiply-accumulate happens in f32 (each int8 weight gets
// converted to f32 inside the inner loop), and the final accumulator
// is scaled by the row's bScale once per (i, n) tile cell at write-back.
//
// Why this saves time: at M3's blocked-kernel measurement, the GEMM
// was bandwidth-bound on weight reads at ~6.5 GFLOP/s. Reducing weight
// bytes 4× pushes the bound out by ~3× (memory subsystem can deliver
// 4× more weights per cycle, but per-multiply work is unchanged; the
// f32×f32 multiply itself doesn't get faster). Empirically the win
// lands closer to 2× than 4× because Go's compiler doesn't auto-SIMD
// the int8-to-f32 conversion as tightly as the pure-f32 inner loop.
//
// Dispatch: matmulBTQ8 is always blocked (the M8 path is only
// triggered for the big linear layers Wqkv/OutProj/fc11/fc12/fc2,
// all of which have M*K*N ≫ the matmulBT small-shape threshold).
func matmulBTQ8(a []float32, bQ []int8, bScales []float32, M, K, N int) []float32 {
	if len(a) != M*K || len(bQ) != N*K || len(bScales) != N {
		panic("coderank: matmulBTQ8 shape mismatch")
	}
	const (
		mBlock = 32
		nBlock = 32
		kBlock = 128
	)
	dst := make([]float32, M*N)
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
				// Micro-kernel: (iEnd-i0) × (nEnd-n0) tile, K strip [k0, kEnd).
				for i := i0; i < iEnd; i++ {
					aRow := a[i*K+k0 : i*K+kEnd]
					dstRow := dst[i*N+n0 : i*N+nEnd]
					n := n0
					nEndAligned := n0 + ((nEnd-n0)/4)*4
					for ; n < nEndAligned; n += 4 {
						bq0 := bQ[n*K+k0 : n*K+kEnd]
						bq1 := bQ[(n+1)*K+k0 : (n+1)*K+kEnd]
						bq2 := bQ[(n+2)*K+k0 : (n+2)*K+kEnd]
						bq3 := bQ[(n+3)*K+k0 : (n+3)*K+kEnd]
						var s0, s1, s2, s3 float32
						for k := 0; k < kEnd-k0; k++ {
							av := aRow[k]
							s0 += av * float32(bq0[k])
							s1 += av * float32(bq1[k])
							s2 += av * float32(bq2[k])
							s3 += av * float32(bq3[k])
						}
						// Scale the partial sums by the row scales
						// and add into dst (k-tile accumulation).
						dstRow[n-n0+0] += s0 * bScales[n]
						dstRow[n-n0+1] += s1 * bScales[n+1]
						dstRow[n-n0+2] += s2 * bScales[n+2]
						dstRow[n-n0+3] += s3 * bScales[n+3]
					}
					for ; n < nEnd; n++ {
						bqRow := bQ[n*K+k0 : n*K+kEnd]
						var s float32
						for k := 0; k < kEnd-k0; k++ {
							s += aRow[k] * float32(bqRow[k])
						}
						dstRow[n-n0] += s * bScales[n]
					}
				}
			}
		}
	}
	return dst
}
