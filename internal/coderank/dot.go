package coderank

// dotF32 computes Σ a[i] * b[i] with NEON acceleration on arm64 and
// a Go scalar loop elsewhere. len(a) must equal len(b).
//
// On arm64 the vector body runs in dot_arm64.s (4-lane VFMLA, ~3-5×
// the throughput of Go's scalar inner loop on Apple Silicon); the
// n%4 tail runs in Go regardless. For ken's matmul shapes K is
// always a multiple of 4 (64, 768, 3072) so the tail is empty in
// the hot path.
func dotF32(a, b []float32) float32 {
	n := len(a)
	if n != len(b) {
		panic("coderank: dotF32 length mismatch")
	}
	if n == 0 {
		return 0
	}
	n4 := n / 4
	var sum float32
	if n4 > 0 {
		sum = dotNEON(&a[0], &b[0], n4)
	}
	for k := n4 * 4; k < n; k++ {
		sum += a[k] * b[k]
	}
	return sum
}
