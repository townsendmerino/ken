package coderank

import (
	"math"
	"math/rand/v2"
	"testing"
)

// TestQuantizeRoundTrip: per-row symmetric int8 quantization is
// lossy by ~1/127 = 0.78% of the row's dynamic range on truly random
// data. Bound the per-row relative L2 error to 1.5e-2 (loose enough
// to absorb tail-effect noise; tight enough to catch a logic bug).
//
// We also assert the SHAPE invariants — len(q) == rows*cols, len(scales) ==
// rows — and the small-input edge cases (all-zero row → scale=1, all-zero
// q; single-row matrix; rectangular non-square).
func TestQuantizeRoundTrip(t *testing.T) {
	cases := []struct {
		name       string
		rows, cols int
	}{
		{"tiny_square", 4, 4},
		{"rect_wide", 3, 64},
		{"rect_tall", 64, 3},
		{"forward_wqkv", 2304, 768}, // real model shape
		{"forward_fc11", 3072, 768}, // real model shape
		{"forward_fc2", 768, 3072},  // real model shape
	}
	rng := rand.New(rand.NewPCG(7, 11))
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := make([]float32, c.rows*c.cols)
			for i := range w {
				w[i] = float32(rng.NormFloat64() * 0.1)
			}
			q, scales := quantizeRowsInt8(w, c.rows, c.cols)
			if len(q) != c.rows*c.cols {
				t.Fatalf("q len: got %d want %d", len(q), c.rows*c.cols)
			}
			if len(scales) != c.rows {
				t.Fatalf("scales len: got %d want %d", len(scales), c.rows)
			}
			rec := dequantizeRowsInt8(q, scales, c.rows, c.cols)

			// Per-row relative L2 error.
			for i := 0; i < c.rows; i++ {
				var num, den float64
				for j := 0; j < c.cols; j++ {
					d := float64(rec[i*c.cols+j] - w[i*c.cols+j])
					num += d * d
					den += float64(w[i*c.cols+j]) * float64(w[i*c.cols+j])
				}
				if den == 0 {
					continue
				}
				relErr := math.Sqrt(num / den)
				if relErr > 1.5e-2 {
					t.Errorf("row %d: relErr=%v > 1.5e-2 (max-abs row=%v)", i, relErr, scales[i]*127)
					break
				}
			}
		})
	}
}

// TestQuantize_zeroRow: an all-zero row must produce all-zero q with
// some non-zero scale (we use 1 — value doesn't matter since q is 0).
// Catches a divide-by-zero regression if a future loosened scale calc
// drops the maxAbs==0 guard.
func TestQuantize_zeroRow(t *testing.T) {
	w := make([]float32, 4*8) // 4 rows × 8 cols; row 2 stays all-zero
	for i := 0; i < 4*8; i++ {
		if i/8 != 2 {
			w[i] = float32(i) * 0.01
		}
	}
	q, scales := quantizeRowsInt8(w, 4, 8)
	for j := 0; j < 8; j++ {
		if q[2*8+j] != 0 {
			t.Errorf("zero-row q[%d]: got %d want 0", j, q[2*8+j])
		}
	}
	if scales[2] == 0 {
		t.Errorf("zero-row scale should be non-zero (defaults to 1 for safety)")
	}
}

// TestQuantize_extremeMaxRow: a row whose max is +1 / -1 exactly maps
// to q ∈ {-127, 127} (not 128). Catches the clamp-to-127 bug — if the
// quantizer let -128 through, then sym-multiplying by scale would
// recover -1.008 instead of -1.0 (off by 0.008 = 0.6%).
func TestQuantize_extremeMaxRow(t *testing.T) {
	// Row with max +1.0 at one position, max -1.0 at another, zeros elsewhere.
	w := []float32{1.0, -1.0, 0, 0}
	q, scales := quantizeRowsInt8(w, 1, 4)
	if scales[0] != 1.0/127.0 {
		t.Errorf("scale: got %v want %v", scales[0], 1.0/127.0)
	}
	if q[0] != 127 {
		t.Errorf("q[0]: got %d want 127", q[0])
	}
	if q[1] != -127 {
		t.Errorf("q[1]: got %d want -127", q[1])
	}
}
