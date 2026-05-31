package coderank

import (
	"math"
	"math/rand/v2"
	"testing"
)

// TestDotF32_matchesScalar: dotF32 (NEON on arm64) must produce
// numerically-close results to a plain Go scalar reduction, within
// f32 reduction-order tolerance. The NEON path accumulates in 4 lanes
// then horizontally sums; Go scalar accumulates in 1 register. These
// orders differ by O(log N) so a small drift (~1e-4 × magnitude) is
// expected on long vectors.
func TestDotF32_matchesScalar(t *testing.T) {
	cases := []int{0, 1, 3, 4, 7, 8, 64, 65, 67, 768, 769, 3072, 3075}
	rng := rand.New(rand.NewPCG(1, 2))
	for _, n := range cases {
		a := make([]float32, n)
		b := make([]float32, n)
		for i := 0; i < n; i++ {
			a[i] = float32(rng.NormFloat64() * 0.1)
			b[i] = float32(rng.NormFloat64() * 0.1)
		}
		var ref float32
		for i := 0; i < n; i++ {
			ref += a[i] * b[i]
		}
		got := dotF32(a, b)
		tol := float32(1e-4)*absF32(ref) + 1e-6
		diff := absF32(got - ref)
		if diff > tol {
			t.Errorf("n=%d: got %v want %v (tol %v)", n, got, ref, tol)
		}
	}
}

func absF32(x float32) float32 {
	if x < 0 {
		return -x
	}
	return x
}

// Cosine sanity: dotF32 of orthogonal unit vectors should be ~0,
// parallel unit vectors should be ~1. Catches a sign/order bug that
// the random-vector test might miss.
func TestDotF32_orthogonalAndParallel(t *testing.T) {
	K := 64 // multiple of 4
	a := make([]float32, K)
	b := make([]float32, K)
	a[0] = 1
	b[1] = 1
	if got := dotF32(a, b); got != 0 {
		t.Errorf("orthogonal: got %v want 0", got)
	}
	a[1] = 1 // a = [1,1,0,...]
	b[0] = 1 // b = [1,1,0,...]
	got := dotF32(a, b)
	if math.Abs(float64(got)-2.0) > 1e-6 {
		t.Errorf("parallel: got %v want 2", got)
	}
}

// Benchmark NEON-vs-scalar on the K dimensions that show up in
// matmulBT's inner loop (K=64 for per-head attention, K=768/3072
// for the big linear layers).
func benchDot(b *testing.B, n int) {
	rng := rand.New(rand.NewPCG(1, 2))
	a := make([]float32, n)
	bs := make([]float32, n)
	for i := 0; i < n; i++ {
		a[i] = float32(rng.NormFloat64() * 0.1)
		bs[i] = float32(rng.NormFloat64() * 0.1)
	}
	b.SetBytes(int64(2 * n)) // 2 FLOPs per element; SetBytes report = FLOP/s
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = dotF32(a, bs)
	}
}

func BenchmarkDotF32_K64(b *testing.B)   { benchDot(b, 64) }
func BenchmarkDotF32_K768(b *testing.B)  { benchDot(b, 768) }
func BenchmarkDotF32_K3072(b *testing.B) { benchDot(b, 3072) }

func benchDotGo(b *testing.B, n int) {
	rng := rand.New(rand.NewPCG(1, 2))
	a := make([]float32, n)
	bs := make([]float32, n)
	for i := 0; i < n; i++ {
		a[i] = float32(rng.NormFloat64() * 0.1)
		bs[i] = float32(rng.NormFloat64() * 0.1)
	}
	b.SetBytes(int64(2 * n))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var s float32
		for k := 0; k < n; k++ {
			s += a[k] * bs[k]
		}
		_ = s
	}
}

func BenchmarkDotGo_K64(b *testing.B)   { benchDotGo(b, 64) }
func BenchmarkDotGo_K768(b *testing.B)  { benchDotGo(b, 768) }
func BenchmarkDotGo_K3072(b *testing.B) { benchDotGo(b, 3072) }

// TestDot4x4_matchesScalar: NEON 4-row kernel must produce the same
// 4 row sums as separate scalar dot products on the same data.
// Catches off-by-one indexing, wrong VST1 layout, register clobbering.
func TestDot4x4_matchesScalar(t *testing.T) {
	cases := []int{0, 4, 8, 64, 256, 768, 3072}
	rng := rand.New(rand.NewPCG(5, 7))
	for _, n := range cases {
		a := make([]float32, n)
		b0 := make([]float32, n)
		b1 := make([]float32, n)
		b2 := make([]float32, n)
		b3 := make([]float32, n)
		for i := 0; i < n; i++ {
			a[i] = float32(rng.NormFloat64() * 0.1)
			b0[i] = float32(rng.NormFloat64() * 0.1)
			b1[i] = float32(rng.NormFloat64() * 0.1)
			b2[i] = float32(rng.NormFloat64() * 0.1)
			b3[i] = float32(rng.NormFloat64() * 0.1)
		}
		// References: scalar dot products.
		var ref [4]float32
		for i := 0; i < n; i++ {
			ref[0] += a[i] * b0[i]
			ref[1] += a[i] * b1[i]
			ref[2] += a[i] * b2[i]
			ref[3] += a[i] * b3[i]
		}

		var sums [16]float32
		if n > 0 {
			var aPtr, b0Ptr, b1Ptr, b2Ptr, b3Ptr *float32
			aPtr, b0Ptr, b1Ptr, b2Ptr, b3Ptr = &a[0], &b0[0], &b1[0], &b2[0], &b3[0]
			dotNEON4x4(aPtr, b0Ptr, b1Ptr, b2Ptr, b3Ptr, n/4, &sums)
		}
		// Each row's full sum is the horizontal sum of its 4 lanes.
		got := [4]float32{
			sums[0] + sums[1] + sums[2] + sums[3],
			sums[4] + sums[5] + sums[6] + sums[7],
			sums[8] + sums[9] + sums[10] + sums[11],
			sums[12] + sums[13] + sums[14] + sums[15],
		}
		for r := 0; r < 4; r++ {
			tol := float32(1e-4)*absF32(ref[r]) + 1e-6
			if absF32(got[r]-ref[r]) > tol {
				t.Errorf("n=%d row=%d: got %v want %v (tol %v)", n, r, got[r], ref[r], tol)
			}
		}
	}
}

func benchDot4x4(b *testing.B, n int) {
	rng := rand.New(rand.NewPCG(1, 2))
	a := make([]float32, n)
	b0 := make([]float32, n)
	b1 := make([]float32, n)
	b2 := make([]float32, n)
	b3 := make([]float32, n)
	for i := 0; i < n; i++ {
		a[i] = float32(rng.NormFloat64() * 0.1)
		b0[i] = float32(rng.NormFloat64() * 0.1)
		b1[i] = float32(rng.NormFloat64() * 0.1)
		b2[i] = float32(rng.NormFloat64() * 0.1)
		b3[i] = float32(rng.NormFloat64() * 0.1)
	}
	var sums [16]float32
	b.SetBytes(int64(8 * n)) // 4 rows × (2 FLOPs per element)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dotNEON4x4(&a[0], &b0[0], &b1[0], &b2[0], &b3[0], n/4, &sums)
	}
}

func BenchmarkDot4x4_K64(b *testing.B)   { benchDot4x4(b, 64) }
func BenchmarkDot4x4_K768(b *testing.B)  { benchDot4x4(b, 768) }
func BenchmarkDot4x4_K3072(b *testing.B) { benchDot4x4(b, 3072) }

// TestDot8x4_matchesScalar: NEON 8-row kernel correctness check.
// Same shape as the 4x4 test, scaled to 8 rows.
func TestDot8x4_matchesScalar(t *testing.T) {
	cases := []int{0, 4, 8, 64, 256, 768, 3072}
	rng := rand.New(rand.NewPCG(11, 13))
	for _, n := range cases {
		a := make([]float32, n)
		bs := [8][]float32{}
		for r := 0; r < 8; r++ {
			bs[r] = make([]float32, n)
		}
		for i := 0; i < n; i++ {
			a[i] = float32(rng.NormFloat64() * 0.1)
			for r := 0; r < 8; r++ {
				bs[r][i] = float32(rng.NormFloat64() * 0.1)
			}
		}
		var ref [8]float32
		for i := 0; i < n; i++ {
			for r := 0; r < 8; r++ {
				ref[r] += a[i] * bs[r][i]
			}
		}
		var sums [32]float32
		if n > 0 {
			dotNEON8x4(&a[0],
				&bs[0][0], &bs[1][0], &bs[2][0], &bs[3][0],
				&bs[4][0], &bs[5][0], &bs[6][0], &bs[7][0],
				n/4, &sums)
		}
		for r := 0; r < 8; r++ {
			got := sums[r*4] + sums[r*4+1] + sums[r*4+2] + sums[r*4+3]
			tol := float32(1e-4)*absF32(ref[r]) + 1e-6
			if absF32(got-ref[r]) > tol {
				t.Errorf("n=%d row=%d: got %v want %v (tol %v)", n, r, got, ref[r], tol)
			}
		}
	}
}

func benchDot8x4(b *testing.B, n int) {
	rng := rand.New(rand.NewPCG(3, 7))
	a := make([]float32, n)
	bs := [8][]float32{}
	for r := 0; r < 8; r++ {
		bs[r] = make([]float32, n)
	}
	for i := 0; i < n; i++ {
		a[i] = float32(rng.NormFloat64() * 0.1)
		for r := 0; r < 8; r++ {
			bs[r][i] = float32(rng.NormFloat64() * 0.1)
		}
	}
	var sums [32]float32
	b.SetBytes(int64(16 * n)) // 8 rows × (2 FLOPs per element)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dotNEON8x4(&a[0],
			&bs[0][0], &bs[1][0], &bs[2][0], &bs[3][0],
			&bs[4][0], &bs[5][0], &bs[6][0], &bs[7][0],
			n/4, &sums)
	}
}

func BenchmarkDot8x4_K64(b *testing.B)   { benchDot8x4(b, 64) }
func BenchmarkDot8x4_K768(b *testing.B)  { benchDot8x4(b, 768) }
func BenchmarkDot8x4_K3072(b *testing.B) { benchDot8x4(b, 3072) }
