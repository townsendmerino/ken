//go:build !arm64

package coderank

import "unsafe"

// dotNEON Go fallback for non-arm64 architectures. Same contract as
// the arm64 asm version (operates on n4 four-element strides, returns
// f32 sum) so dotF32 in dot.go is architecture-agnostic. Performance
// matches Go's previous scalar inner loop — on amd64 you don't get
// the NEON win, just the M7-era throughput unchanged.
func dotNEON(a *float32, b *float32, n4 int) float32 {
	n := n4 * 4
	aSlice := unsafe.Slice(a, n)
	bSlice := unsafe.Slice(b, n)
	var sum float32
	for i := 0; i < n; i++ {
		sum += aSlice[i] * bSlice[i]
	}
	return sum
}

// dotNEON4x4 Go fallback. Computes 4 dot products and stores their
// FULL sums into the first lane of each block; remaining 3 lanes per
// block are zero so the caller's horizontal sum produces the right
// value either way.
func dotNEON4x4(a *float32, b0, b1, b2, b3 *float32, n4 int, sums *[16]float32) {
	n := n4 * 4
	aS := unsafe.Slice(a, n)
	b0S := unsafe.Slice(b0, n)
	b1S := unsafe.Slice(b1, n)
	b2S := unsafe.Slice(b2, n)
	b3S := unsafe.Slice(b3, n)
	var s0, s1, s2, s3 float32
	for i := 0; i < n; i++ {
		s0 += aS[i] * b0S[i]
		s1 += aS[i] * b1S[i]
		s2 += aS[i] * b2S[i]
		s3 += aS[i] * b3S[i]
	}
	*sums = [16]float32{}
	sums[0] = s0
	sums[4] = s1
	sums[8] = s2
	sums[12] = s3
}

// dotNEON8x4 Go fallback. Same lane-layout convention as the 4x4
// fallback (full sum in the first lane of each row's 4-lane block).
func dotNEON8x4(a *float32, b0, b1, b2, b3, b4, b5, b6, b7 *float32, n4 int, sums *[32]float32) {
	n := n4 * 4
	aS := unsafe.Slice(a, n)
	bS := [8][]float32{
		unsafe.Slice(b0, n),
		unsafe.Slice(b1, n),
		unsafe.Slice(b2, n),
		unsafe.Slice(b3, n),
		unsafe.Slice(b4, n),
		unsafe.Slice(b5, n),
		unsafe.Slice(b6, n),
		unsafe.Slice(b7, n),
	}
	var s [8]float32
	for i := 0; i < n; i++ {
		av := aS[i]
		for r := 0; r < 8; r++ {
			s[r] += av * bS[r][i]
		}
	}
	*sums = [32]float32{}
	for r := 0; r < 8; r++ {
		sums[r*4] = s[r]
	}
}
