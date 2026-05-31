// NEON-accelerated f32 dot products for arm64 (Apple Silicon and
// other ARMv8.0+). Two entry points:
//
//   dotNEON4     — single-row 4-lane dot product, fills *[4]float32
//   dotNEON4x4   — FOUR rows of the dot product simultaneously, fills
//                  *[16]float32 with 4 rows × 4 lanes each
//
// The 4x4 variant is the M8b workhorse: matmulBTBlocked's inner loop
// does 4 multiply-add rows in flight (4-way N unroll). Doing all 4 in
// one asm call keeps loop / call overhead amortized AND lets NEON
// pipe 4 FMLAs in parallel (Apple M1 has 4× NEON FP-pipes per core).
//
// The horizontal reduction is left to the Go caller — Plan 9 ARM64
// asm doesn't expose FADDP/FADDV for floats (TODO in
// cmd/asm/internal/asm/testdata/arm64enc.s as of Go 1.26). The Go-
// side reduction is 4 fadds per row = ~1 ns, negligible vs the
// vector body.

#include "textflag.h"

// func dotNEON4(a *float32, b *float32, n4 int, sums *[4]float32)
//   a, b:  pointers to f32 slices (≥ n4*4 elements).
//   n4:    number of 4-lane iterations.
//   sums:  caller-provided [4]float32 — asm writes the 4 lane sums.
TEXT ·dotNEON4(SB), NOSPLIT, $0-32
	MOVD	a+0(FP), R0          // R0 = &a[0]
	MOVD	b+8(FP), R1          // R1 = &b[0]
	MOVD	n4+16(FP), R2        // R2 = n4
	MOVD	sums+24(FP), R3      // R3 = sums

	// V0 = [0,0,0,0] accumulator.
	VMOVI	$0, V0.B16

	CBZ	R2, store

loop:
	VLD1.P	16(R0), [V1.S4]      // V1 = a[k..k+4], R0 += 16
	VLD1.P	16(R1), [V2.S4]      // V2 = b[k..k+4], R1 += 16
	VFMLA	V1.S4, V2.S4, V0.S4  // V0 += V1 * V2 (lane-wise)
	SUBS	$1, R2, R2
	BNE	loop

store:
	VST1	[V0.S4], (R3)         // *sums = V0 (4 floats)
	RET

// func dotNEON4x4(a *float32, b0, b1, b2, b3 *float32, n4 int, sums *[16]float32)
//   Computes 4 dot products simultaneously: sums[0..3] = a·b0 lanes,
//   sums[4..7] = a·b1 lanes, etc. Caller horizontal-sums each block
//   of 4 to get the 4 row sums. n4 = number of 4-lane iterations
//   (rows must all be at least n4*4 elements long).
//
// Arg layout: a=0, b0=8, b1=16, b2=24, b3=32, n4=40, sums=48, total=56.
TEXT ·dotNEON4x4(SB), NOSPLIT, $0-56
	MOVD	a+0(FP), R0
	MOVD	b0+8(FP), R1
	MOVD	b1+16(FP), R2
	MOVD	b2+24(FP), R3
	MOVD	b3+32(FP), R4
	MOVD	n4+40(FP), R5
	MOVD	sums+48(FP), R6

	// V0..V3 = 4 accumulators (one per row).
	VMOVI	$0, V0.B16
	VMOVI	$0, V1.B16
	VMOVI	$0, V2.B16
	VMOVI	$0, V3.B16

	CBZ	R5, store4x4

loop4x4:
	VLD1.P	16(R0), [V4.S4]      // V4 = a[k..k+4] (broadcast)
	VLD1.P	16(R1), [V5.S4]      // V5 = b0[k..k+4]
	VFMLA	V4.S4, V5.S4, V0.S4  // V0 += V4 * V5
	VLD1.P	16(R2), [V5.S4]      // V5 = b1[k..k+4]
	VFMLA	V4.S4, V5.S4, V1.S4
	VLD1.P	16(R3), [V5.S4]      // V5 = b2[k..k+4]
	VFMLA	V4.S4, V5.S4, V2.S4
	VLD1.P	16(R4), [V5.S4]      // V5 = b3[k..k+4]
	VFMLA	V4.S4, V5.S4, V3.S4
	SUBS	$1, R5, R5
	BNE	loop4x4

store4x4:
	// Write V0..V3 sequentially: 16 floats total at *sums.
	VST1	[V0.S4], (R6)
	ADD	$16, R6, R6
	VST1	[V1.S4], (R6)
	ADD	$16, R6, R6
	VST1	[V2.S4], (R6)
	ADD	$16, R6, R6
	VST1	[V3.S4], (R6)
	RET

// func dotNEON8x4(a *float32, b0,b1,b2,b3,b4,b5,b6,b7 *float32, n4 int, sums *[32]float32)
//   8 dot products in flight: sums[0..3]   = a·b0 lane sums,
//                             sums[4..7]   = a·b1 lane sums,
//                             ...
//                             sums[28..31] = a·b7 lane sums.
//
// M8d follow-on to the 4x4 kernel. The key insight: each loop iteration
// loads `a` ONCE (V8) then runs 8 FMLAs against 8 distinct accumulators
// (V0..V7) by rotating b-row loads through V9. Halves the per-FLOP
// amortized cost of the a-load relative to the 4x4 kernel, AND keeps
// the M-series' 4 NEON FP-pipes saturated through the longer FMLA
// dependency chain.
//
// Register usage:
//   R0     a pointer
//   R1..R8 b0..b7 pointers
//   R9     n4 counter
//   R10    sums dst
//   V0..V7 8 accumulators
//   V8     a broadcast register
//   V9     rotating b-load register
//
// Arg layout: a=0, b0=8, b1=16, b2=24, b3=32, b4=40, b5=48, b6=56,
// b7=64, n4=72, sums=80; total=88.
TEXT ·dotNEON8x4(SB), NOSPLIT, $0-88
	MOVD	a+0(FP), R0
	MOVD	b0+8(FP), R1
	MOVD	b1+16(FP), R2
	MOVD	b2+24(FP), R3
	MOVD	b3+32(FP), R4
	MOVD	b4+40(FP), R5
	MOVD	b5+48(FP), R6
	MOVD	b6+56(FP), R7
	MOVD	b7+64(FP), R8
	MOVD	n4+72(FP), R9
	MOVD	sums+80(FP), R10

	// V0..V7 = 8 accumulators (one per row).
	VMOVI	$0, V0.B16
	VMOVI	$0, V1.B16
	VMOVI	$0, V2.B16
	VMOVI	$0, V3.B16
	VMOVI	$0, V4.B16
	VMOVI	$0, V5.B16
	VMOVI	$0, V6.B16
	VMOVI	$0, V7.B16

	CBZ	R9, store8x4

loop8x4:
	VLD1.P	16(R0), [V8.S4]      // V8 = a[k..k+4] (broadcast across all 8 FMLAs)
	VLD1.P	16(R1), [V9.S4]
	VFMLA	V8.S4, V9.S4, V0.S4
	VLD1.P	16(R2), [V9.S4]
	VFMLA	V8.S4, V9.S4, V1.S4
	VLD1.P	16(R3), [V9.S4]
	VFMLA	V8.S4, V9.S4, V2.S4
	VLD1.P	16(R4), [V9.S4]
	VFMLA	V8.S4, V9.S4, V3.S4
	VLD1.P	16(R5), [V9.S4]
	VFMLA	V8.S4, V9.S4, V4.S4
	VLD1.P	16(R6), [V9.S4]
	VFMLA	V8.S4, V9.S4, V5.S4
	VLD1.P	16(R7), [V9.S4]
	VFMLA	V8.S4, V9.S4, V6.S4
	VLD1.P	16(R8), [V9.S4]
	VFMLA	V8.S4, V9.S4, V7.S4
	SUBS	$1, R9, R9
	BNE	loop8x4

store8x4:
	// Write V0..V7 sequentially: 32 floats total at *sums.
	VST1	[V0.S4], (R10)
	ADD	$16, R10, R10
	VST1	[V1.S4], (R10)
	ADD	$16, R10, R10
	VST1	[V2.S4], (R10)
	ADD	$16, R10, R10
	VST1	[V3.S4], (R10)
	ADD	$16, R10, R10
	VST1	[V4.S4], (R10)
	ADD	$16, R10, R10
	VST1	[V5.S4], (R10)
	ADD	$16, R10, R10
	VST1	[V6.S4], (R10)
	ADD	$16, R10, R10
	VST1	[V7.S4], (R10)
	RET
