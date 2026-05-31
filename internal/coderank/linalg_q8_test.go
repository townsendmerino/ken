package coderank

import (
	"math"
	"math/rand/v2"
	"testing"
)

// TestMatmulBTQ8_matchesMatmulBT: int8 matmul with reconstructed
// weights must produce ~the same output as f32 matmul on the same
// reconstructed weights. The path is:
//
//	W_f32  →  quantize  →  bQ + bScales  →  dequantize  →  W_recon  ≈  W_f32
//	a · W_reconᵀ                                            ←   ground truth (f32 matmul)
//	matmulBTQ8(a, bQ, bScales)                              ←   what we're testing
//
// These two must AGREE — both compute a · W_reconᵀ, just one path
// dequantizes on-the-fly while the other materializes. Per-element
// difference is f32 reduction-order noise (the tile sizes are the
// same, so we expect very small drift).
//
// THIS IS NOT testing whether quantization is lossy (TestQuantize
// covers that). It's testing that the int8 GEMM kernel does the right
// arithmetic given quantized inputs.
func TestMatmulBTQ8_matchesMatmulBT(t *testing.T) {
	cases := []struct {
		name    string
		M, K, N int
	}{
		{"tiny", 4, 16, 4},
		{"unaligned", 33, 130, 35},
		{"L80_outproj", 80, 768, 768},
		{"L80_wqkv", 80, 768, 2304},
		{"L80_fc11", 80, 768, 3072},
		{"L80_fc2", 80, 3072, 768},
	}
	rng := rand.New(rand.NewPCG(1, 2))
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := randMat(rng, c.M*c.K)
			w := randMat(rng, c.N*c.K)
			bQ, bScales := quantizeRowsInt8(w, c.N, c.K)
			wRecon := dequantizeRowsInt8(bQ, bScales, c.N, c.K)

			refFromRecon := matmulBT(a, wRecon, c.M, c.K, c.N)
			gotFromQ8 := matmulBTQ8(a, bQ, bScales, c.M, c.K, c.N)

			if len(gotFromQ8) != len(refFromRecon) {
				t.Fatalf("len: got %d want %d", len(gotFromQ8), len(refFromRecon))
			}
			// Tolerance: same blocked reduction order on both sides
			// (matmulBT picks blocked at this scale), so differences
			// should be tiny.
			tol := float32(1e-3) * float32(c.K) / 1024.0
			if tol < 1e-4 {
				tol = 1e-4
			}
			for i := range gotFromQ8 {
				diff := float32(math.Abs(float64(gotFromQ8[i] - refFromRecon[i])))
				if diff > tol {
					t.Fatalf("idx %d: got %v want %v diff=%v tol=%v",
						i, gotFromQ8[i], refFromRecon[i], diff, tol)
				}
			}
		})
	}
}

// TestMatmulBTQ8_endToEndError: combine quantization + GEMM and check
// the RELATIVE error vs the true f32 GEMM (a · W_f32). This is the
// "how much does int8 cost us per matmul?" measurement.
// Plan §11 secondary cosine bar accepts 1e-3 to 1e-2 — we should
// land comfortably inside.
func TestMatmulBTQ8_endToEndError(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	M, K, N := 80, 768, 3072 // fc11/fc12 shape

	a := randMat(rng, M*K)
	w := randMat(rng, N*K)
	bQ, bScales := quantizeRowsInt8(w, N, K)

	truth := matmulBT(a, w, M, K, N)
	approx := matmulBTQ8(a, bQ, bScales, M, K, N)

	// Per-element relative error, averaged.
	var sumSqDiff, sumSqTruth float64
	for i := range truth {
		d := float64(approx[i] - truth[i])
		sumSqDiff += d * d
		sumSqTruth += float64(truth[i]) * float64(truth[i])
	}
	relL2 := math.Sqrt(sumSqDiff / sumSqTruth)
	t.Logf("end-to-end relative L2 error (fc11 shape): %.4f%%", relL2*100)
	if relL2 > 0.02 {
		t.Errorf("relL2 = %v > 0.02 — int8 quant introduced more error than expected", relL2)
	}
}

func benchShapeQ8(b *testing.B, M, K, N int) {
	rng := rand.New(rand.NewPCG(1, 2))
	a := randMat(rng, M*K)
	w := randMat(rng, N*K)
	bQ, bScales := quantizeRowsInt8(w, N, K)
	b.SetBytes(int64(2 * M * K * N))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = matmulBTQ8(a, bQ, bScales, M, K, N)
	}
}

func BenchmarkMatmulBTQ8_L80_wqkv(b *testing.B) { benchShapeQ8(b, 80, 768, 2304) }
func BenchmarkMatmulBTQ8_L80_fc11(b *testing.B) { benchShapeQ8(b, 80, 768, 3072) }
func BenchmarkMatmulBTQ8_L80_fc2(b *testing.B)  { benchShapeQ8(b, 80, 3072, 768) }
func BenchmarkMatmulBTQ8_L80_outproj(b *testing.B) {
	benchShapeQ8(b, 80, 768, 768)
}
func BenchmarkMatmulBTQ8_L512_fc11(b *testing.B) { benchShapeQ8(b, 512, 768, 3072) }
