//go:build arm64

package coderank

// dotNEON4 fills *sums with the 4 NEON-lane partial sums of the dot
// product over the first n4*4 elements of a and b. Caller (dotF32)
// horizontal-adds the 4 lanes.
//
//go:noescape
func dotNEON4(a *float32, b *float32, n4 int, sums *[4]float32)

// dotNEON4x4 computes 4 dot products simultaneously: a·b0, a·b1,
// a·b2, a·b3. Fills *sums as 4 consecutive [4]float32 blocks: each
// block is one row's 4 NEON-lane partial sums. Caller horizontal-
// sums each block of 4 to get a single f32 per row.
//
// This is the workhorse for matmulBTBlocked's 4-way unrolled inner
// loop — one asm call replaces 4 separate dotNEON4 calls AND lets
// NEON pipe up to 4 FMLAs in parallel on cores that can dispatch
// them (M1/M2/M3 have 4× NEON FP-pipes per core).
//
//go:noescape
func dotNEON4x4(a *float32, b0, b1, b2, b3 *float32, n4 int, sums *[16]float32)

// dotNEON8x4 computes 8 dot products simultaneously: sums[0..3] =
// a·b0 lane sums, sums[4..7] = a·b1, … sums[28..31] = a·b7. Caller
// horizontal-sums each block of 4 to get one f32 per row.
//
// M8d follow-on to dotNEON4x4: each loop iter loads `a` ONCE (one
// vector register) and runs 8 FMLAs against 8 distinct accumulators.
// Halves the per-FLOP amortized cost of the a-load relative to the
// 4-row variant and gives the M-series' 4 NEON FP-pipes a longer
// FMLA chain to keep saturated.
//
//go:noescape
func dotNEON8x4(a *float32, b0, b1, b2, b3, b4, b5, b6, b7 *float32, n4 int, sums *[32]float32)

// dotNEON implements the architecture-neutral interface in dot.go:
// returns the sum over the first n4*4 elements. On arm64 this is the
// NEON path; on other arches dot_other.go's dotNEON does the same.
func dotNEON(a *float32, b *float32, n4 int) float32 {
	var sums [4]float32
	dotNEON4(a, b, n4, &sums)
	return sums[0] + sums[1] + sums[2] + sums[3]
}
