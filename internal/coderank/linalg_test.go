package coderank

import (
	"math"
	"math/rand/v2"
	"testing"
)

// TestMatmulBT_blockedMatchesNaive: the blocked kernel must produce
// bit-equivalent (modulo float32 reduction order) output to the naive
// triple loop on the exact shapes the forward pass uses. Slight
// non-bitwise differences are expected because the blocked kernel
// accumulates per k-tile (different summation order); we allow a small
// absolute tolerance proportional to K.
func TestMatmulBT_blockedMatchesNaive(t *testing.T) {
	cases := []struct {
		name    string
		M, K, N int
	}{
		{"tiny", 4, 8, 4},             // sub-block, exercises tail
		{"one_block_mn", 32, 64, 32},  // exactly one (BM, BN) tile
		{"unaligned", 33, 130, 35},    // mid-block tail handling on all 3 axes
		{"L80_outproj", 80, 768, 768}, // forward shape: out_proj
		{"L80_wqkv", 80, 768, 2304},   // forward shape: Wqkv
		{"L80_fc11", 80, 768, 3072},   // forward shape: fc11/fc12
		{"L80_fc2", 80, 3072, 768},    // forward shape: fc2
		{"L512_fc11", 512, 768, 3072}, // longest forward shape
	}
	rng := rand.New(rand.NewPCG(1, 2))
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := randMat(rng, c.M*c.K)
			b := randMat(rng, c.N*c.K)
			naive := matmulBTNaive(a, b, c.M, c.K, c.N)
			blocked := matmulBTBlocked(a, b, c.M, c.K, c.N)
			// Tolerance scales with K (reduction length) — float32
			// summation order differs between naive and blocked.
			tol := float32(1e-3) * float32(c.K) / 1024.0
			if tol < 1e-4 {
				tol = 1e-4
			}
			for i := range naive {
				diff := float32(math.Abs(float64(naive[i] - blocked[i])))
				if diff > tol {
					t.Fatalf("idx %d: naive=%v blocked=%v diff=%v (tol=%v)",
						i, naive[i], blocked[i], diff, tol)
				}
			}
		})
	}
}

func randMat(rng *rand.Rand, n int) []float32 {
	out := make([]float32, n)
	for i := range out {
		out[i] = float32(rng.NormFloat64() * 0.1)
	}
	return out
}

// Benchmarks on real forward shapes. SetBytes reports the GEMM size
// so `go test -bench=. -benchmem` prints throughput in MB/s
// (multiply by 2 for FLOP/s — each multiply-add is 2 ops).
func benchShape(b *testing.B, M, K, N int, fn func(a, w []float32, M, K, N int) []float32) {
	rng := rand.New(rand.NewPCG(1, 2))
	a := randMat(rng, M*K)
	w := randMat(rng, N*K)
	// Bytes "moved" = 4 * (M*K + N*K + M*N); but we report FLOPs as
	// 2*M*K*N below via SetBytes(2*M*K*N) so the printed MB/s == GFLOP/s.
	b.SetBytes(int64(2 * M * K * N))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = fn(a, w, M, K, N)
	}
}

func BenchmarkMatmulBT_naive_L80_wqkv(b *testing.B)    { benchShape(b, 80, 768, 2304, matmulBTNaive) }
func BenchmarkMatmulBT_blocked_L80_wqkv(b *testing.B)  { benchShape(b, 80, 768, 2304, matmulBTBlocked) }
func BenchmarkMatmulBT_naive_L80_fc11(b *testing.B)    { benchShape(b, 80, 768, 3072, matmulBTNaive) }
func BenchmarkMatmulBT_blocked_L80_fc11(b *testing.B)  { benchShape(b, 80, 768, 3072, matmulBTBlocked) }
func BenchmarkMatmulBT_naive_L80_fc2(b *testing.B)     { benchShape(b, 80, 3072, 768, matmulBTNaive) }
func BenchmarkMatmulBT_blocked_L80_fc2(b *testing.B)   { benchShape(b, 80, 3072, 768, matmulBTBlocked) }
func BenchmarkMatmulBT_naive_L80_outproj(b *testing.B) { benchShape(b, 80, 768, 768, matmulBTNaive) }
func BenchmarkMatmulBT_blocked_L80_outproj(b *testing.B) {
	benchShape(b, 80, 768, 768, matmulBTBlocked)
}
func BenchmarkMatmulBT_naive_L512_fc11(b *testing.B) { benchShape(b, 512, 768, 3072, matmulBTNaive) }
func BenchmarkMatmulBT_blocked_L512_fc11(b *testing.B) {
	benchShape(b, 512, 768, 3072, matmulBTBlocked)
}

// M10 tile sweep: parameterized matmul bench so we can search for the
// post-8x4-kernel optimum without recompiling between runs. The shape
// is L80_fc11 (M=80, K=768, N=3072) — the largest forward-pass GEMM
// and the one most likely to benefit from larger tiles.
func benchShapeTiled(b *testing.B, M, K, N, mB, nB, kB int) {
	rng := rand.New(rand.NewPCG(1, 2))
	a := randMat(rng, M*K)
	w := randMat(rng, N*K)
	dst := make([]float32, M*N)
	b.SetBytes(int64(2 * M * K * N))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := range dst {
			dst[j] = 0
		}
		matmulBTBlockedFillIntoTiled(a, w, dst, M, K, N, mB, nB, kB)
	}
}

// Named sweep cells. Naming: <mBlock>x<nBlock>x<kBlock>.
func BenchmarkMatmulTile_32x32x128(b *testing.B)  { benchShapeTiled(b, 80, 768, 3072, 32, 32, 128) }
func BenchmarkMatmulTile_32x64x128(b *testing.B)  { benchShapeTiled(b, 80, 768, 3072, 32, 64, 128) }
func BenchmarkMatmulTile_64x32x128(b *testing.B)  { benchShapeTiled(b, 80, 768, 3072, 64, 32, 128) }
func BenchmarkMatmulTile_64x64x128(b *testing.B)  { benchShapeTiled(b, 80, 768, 3072, 64, 64, 128) }
func BenchmarkMatmulTile_32x32x256(b *testing.B)  { benchShapeTiled(b, 80, 768, 3072, 32, 32, 256) }
func BenchmarkMatmulTile_64x64x64(b *testing.B)   { benchShapeTiled(b, 80, 768, 3072, 64, 64, 64) }
func BenchmarkMatmulTile_16x32x128(b *testing.B)  { benchShapeTiled(b, 80, 768, 3072, 16, 32, 128) }
func BenchmarkMatmulTile_32x128x128(b *testing.B) { benchShapeTiled(b, 80, 768, 3072, 32, 128, 128) }
func BenchmarkMatmulTile_64x128x128(b *testing.B) { benchShapeTiled(b, 80, 768, 3072, 64, 128, 128) }
func BenchmarkMatmulTile_32x256x128(b *testing.B) { benchShapeTiled(b, 80, 768, 3072, 32, 256, 128) }

// Larger kBlock variants — the 32x32x256 winner suggested kBlock is
// the lever, not mBlock/nBlock. Test K=768 (full K in one tile, no
// k-loop overhead) and intermediate sizes.
func BenchmarkMatmulTile_32x32x384(b *testing.B) { benchShapeTiled(b, 80, 768, 3072, 32, 32, 384) }
func BenchmarkMatmulTile_32x32x768(b *testing.B) { benchShapeTiled(b, 80, 768, 3072, 32, 32, 768) }
func BenchmarkMatmulTile_32x64x256(b *testing.B) { benchShapeTiled(b, 80, 768, 3072, 32, 64, 256) }
func BenchmarkMatmulTile_32x64x768(b *testing.B) { benchShapeTiled(b, 80, 768, 3072, 32, 64, 768) }
func BenchmarkMatmulTile_16x32x256(b *testing.B) { benchShapeTiled(b, 80, 768, 3072, 16, 32, 256) }
func BenchmarkMatmulTile_64x32x256(b *testing.B) { benchShapeTiled(b, 80, 768, 3072, 64, 32, 256) }

// Cross-shape check of the 32x32x768 winner against all 4 forward-pass
// shapes — the fc2 case has K=3072 so 768 splits it into 4 tiles
// (vs 24 with kBlock=128). If any shape regresses, the default needs
// to compromise. Also a 32x32x3072 cell for fc2's K-full case.
func BenchmarkMatmulTile_wqkv_32x32x768(b *testing.B) { benchShapeTiled(b, 80, 768, 2304, 32, 32, 768) }
func BenchmarkMatmulTile_fc2_32x32x768(b *testing.B)  { benchShapeTiled(b, 80, 3072, 768, 32, 32, 768) }
func BenchmarkMatmulTile_fc2_32x32x3072(b *testing.B) {
	benchShapeTiled(b, 80, 3072, 768, 32, 32, 3072)
}
func BenchmarkMatmulTile_outproj_32x32x768(b *testing.B) {
	benchShapeTiled(b, 80, 768, 768, 32, 32, 768)
}
func BenchmarkMatmulTile_L512_fc11_32x32x768(b *testing.B) {
	benchShapeTiled(b, 512, 768, 3072, 32, 32, 768)
}
