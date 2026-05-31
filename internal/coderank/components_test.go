package coderank

import (
	"fmt"
	"math"
	"testing"
)

// Component unit tests — plan §11.4. The golden cosine test already
// proves end-to-end correctness to 6 decimal places, so these are a
// belt-and-suspenders safety net: they localize where future drift
// would come from instead of hunting it across 12 layers. Cheap, no
// fixtures, no model — they run on every `go test ./internal/coderank`.

func approxEq(t *testing.T, name string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s: got %v want %v (tol %v, diff %v)", name, got, want, tol, math.Abs(got-want))
	}
}

func TestSilu_knownValues(t *testing.T) {
	// silu(x) = x * sigmoid(x); known values from torch.nn.functional.silu.
	cases := []struct {
		x, want float64
	}{
		{0.0, 0.0},
		{1.0, 0.7310585786300049}, // 1 * sigmoid(1)
		{-1.0, -0.2689414213699951},
		{2.0, 1.7615941559557649},
		{-3.0, -0.14227761432105793},
		{10.0, 9.99954602131298}, // saturates toward x
		{-10.0, -0.00045400146867836},
	}
	for _, c := range cases {
		got := float64(silu(float32(c.x)))
		approxEq(t, fmt.Sprintf("silu(%v)", c.x), got, c.want, 1e-5)
	}
}

func TestSoftmax_uniformAndPeaked(t *testing.T) {
	// uniform input → uniform output.
	row := []float32{1, 1, 1, 1}
	softmaxRow(row)
	for i, v := range row {
		approxEq(t, fmt.Sprintf("uniform[%d]", i), float64(v), 0.25, 1e-6)
	}
	// peaked: only one position large → that position ≈ 1, others ≈ 0.
	row = []float32{0, 100, 0, 0}
	softmaxRow(row)
	approxEq(t, "peaked[1]", float64(row[1]), 1.0, 1e-6)
	for _, i := range []int{0, 2, 3} {
		if row[i] > 1e-30 {
			t.Errorf("peaked[%d]: got %v want ~0", i, row[i])
		}
	}
	// sums to 1.
	row = []float32{1.0, 2.0, 3.0, 4.0}
	softmaxRow(row)
	var sum float64
	for _, v := range row {
		sum += float64(v)
	}
	approxEq(t, "sum-to-1", sum, 1.0, 1e-6)
	// known relative ratios: softmax([1,2,3,4]) = e^[1,2,3,4] / sum
	// ratio[1]/ratio[0] should equal e^1 = 2.71828
	approxEq(t, "ratio[1/0]", float64(row[1])/float64(row[0]), math.E, 1e-5)
}

// TestLayerNorm_handComputed: tiny 4-dim row with known weight/bias,
// hand-computed expected output. Catches mean/variance/eps bugs that
// the smoke cosine would only catch as accumulated drift.
func TestLayerNorm_handComputed(t *testing.T) {
	// Row: [1, 2, 3, 4]. Mean = 2.5. Variance (divisor 4) = 1.25.
	// invStd = 1 / sqrt(1.25 + 1e-12) ≈ 0.894427.
	// normalized = ([1,2,3,4] - 2.5) * invStd = [-1.342, -0.447, 0.447, 1.342].
	// With weight=[1,1,1,1] and bias=[0,0,0,0], that's the output.
	x := []float32{1, 2, 3, 4}
	w := []float32{1, 1, 1, 1}
	b := []float32{0, 0, 0, 0}
	layerNorm(x, w, b, 1, 4, 1e-12)
	want := []float32{-1.3416407, -0.4472136, 0.4472136, 1.3416407}
	for i := range x {
		approxEq(t, fmt.Sprintf("layerNorm[%d]", i), float64(x[i]), float64(want[i]), 1e-5)
	}
	// With non-trivial weight + bias.
	x = []float32{1, 2, 3, 4}
	w = []float32{2, 0.5, 1, 1}
	b = []float32{0.1, -0.2, 0, 0.5}
	layerNorm(x, w, b, 1, 4, 1e-12)
	// expected = [-1.342*2+0.1, -0.447*0.5-0.2, 0.447*1+0, 1.342*1+0.5]
	want = []float32{-2.5832813, -0.42360678, 0.4472136, 1.8416407}
	for i := range x {
		approxEq(t, fmt.Sprintf("layerNorm-w-b[%d]", i), float64(x[i]), float64(want[i]), 1e-5)
	}
}

// TestRoPE_position0Identity: at position 0, cos=1 and sin=0, so RoPE
// is an identity. Catches any indexing / sign error in the rotate_half
// application that would only show as ~zero cosine end-to-end.
func TestRoPE_position0Identity(t *testing.T) {
	headDim := 64
	heads := 2
	// One sequence position, two heads, headDim each.
	x := make([]float32, 1*heads*headDim)
	for i := range x {
		x[i] = float32(i+1) * 0.01
	}
	xCopy := make([]float32, len(x))
	copy(xCopy, x)

	rope := newRopeTable(1, headDim, 1000)
	rope.apply(x, heads)

	for i := range x {
		if math.Abs(float64(x[i]-xCopy[i])) > 1e-6 {
			t.Errorf("idx %d: rope at pos 0 should be identity; got %v want %v", i, x[i], xCopy[i])
		}
	}
}

// TestRoPE_orthogonalityPreserved: RoPE is a rotation in each (d, d+half)
// plane, so it preserves the L2 norm of the head vector at every
// position. Catches sign-swap bugs in rotate_half.
func TestRoPE_orthogonalityPreserved(t *testing.T) {
	headDim := 64
	heads := 1
	seqLen := 5
	x := make([]float32, seqLen*heads*headDim)
	for i := range x {
		x[i] = float32((i%7)+1) * 0.1
	}
	// L2 norms per position BEFORE.
	preNorms := make([]float64, seqLen)
	for m := 0; m < seqLen; m++ {
		var s float64
		for d := 0; d < headDim; d++ {
			v := float64(x[m*headDim+d])
			s += v * v
		}
		preNorms[m] = math.Sqrt(s)
	}

	rope := newRopeTable(seqLen, headDim, 1000)
	rope.apply(x, heads)

	// L2 norms per position AFTER must equal before (rotation preserves norm).
	for m := 0; m < seqLen; m++ {
		var s float64
		for d := 0; d < headDim; d++ {
			v := float64(x[m*headDim+d])
			s += v * v
		}
		post := math.Sqrt(s)
		approxEq(t, fmt.Sprintf("norm@pos%d", m), post, preNorms[m], 1e-5)
	}
}

// TestL2Normalize_zeroSafe: empty / zero-norm rows stay zero, never NaN.
func TestL2Normalize_zeroSafe(t *testing.T) {
	x := []float32{0, 0, 0, 0}
	l2Normalize(x, 1, 4)
	for i, v := range x {
		if v != 0 {
			t.Errorf("zero-norm row mutated: x[%d]=%v", i, v)
		}
	}
	// Non-zero row: L2 norm of output should be 1.
	x = []float32{3, 4, 0, 0}
	l2Normalize(x, 1, 4)
	var sum float64
	for _, v := range x {
		sum += float64(v) * float64(v)
	}
	approxEq(t, "post-L2 norm", math.Sqrt(sum), 1.0, 1e-6)
}
