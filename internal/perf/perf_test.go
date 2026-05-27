package perf

import (
	"encoding/json"
	"testing"
	"time"
)

func TestLatencyOf_Empty(t *testing.T) {
	got := LatencyOf(nil)
	if got != (LatencyStats{}) {
		t.Fatalf("LatencyOf(nil) = %+v, want zero value", got)
	}
	got = LatencyOf([]time.Duration{})
	if got != (LatencyStats{}) {
		t.Fatalf("LatencyOf([]) = %+v, want zero value", got)
	}
}

func TestLatencyOf_Single(t *testing.T) {
	got := LatencyOf([]time.Duration{5 * time.Millisecond})
	want := LatencyStats{N: 1, MinMs: 5, MaxMs: 5, MeanMs: 5, P50Ms: 5, P95Ms: 5, P99Ms: 5}
	if got != want {
		t.Fatalf("LatencyOf({5ms}) = %+v, want %+v", got, want)
	}
}

func TestLatencyOf_AllSame(t *testing.T) {
	sample := make([]time.Duration, 100)
	for i := range sample {
		sample[i] = 7 * time.Millisecond
	}
	got := LatencyOf(sample)
	if got.MinMs != 7 || got.MaxMs != 7 || got.P50Ms != 7 || got.P95Ms != 7 || got.P99Ms != 7 || got.MeanMs != 7 {
		t.Fatalf("LatencyOf(all-7ms) = %+v, want all 7ms", got)
	}
	if got.N != 100 {
		t.Fatalf("N = %d, want 100", got.N)
	}
}

func TestLatencyOf_Percentiles(t *testing.T) {
	// Sorted sample 1..100 ms. nearest-rank 1-indexed:
	//   P50 of 100 = index 49 = 50ms
	//   P95 of 100 = index 94 = 95ms
	//   P99 of 100 = index 98 = 99ms
	sample := make([]time.Duration, 100)
	for i := range sample {
		sample[i] = time.Duration(i+1) * time.Millisecond
	}
	got := LatencyOf(sample)
	if got.P50Ms != 50 {
		t.Errorf("P50 = %v, want 50ms", got.P50Ms)
	}
	if got.P95Ms != 95 {
		t.Errorf("P95 = %v, want 95ms", got.P95Ms)
	}
	if got.P99Ms != 99 {
		t.Errorf("P99 = %v, want 99ms", got.P99Ms)
	}
	if got.MinMs != 1 || got.MaxMs != 100 {
		t.Errorf("min/max = %v/%v, want 1/100", got.MinMs, got.MaxMs)
	}
	if got.MeanMs != 50.5 {
		t.Errorf("mean = %v, want 50.5", got.MeanMs)
	}
}

func TestLatencyOf_Unsorted(t *testing.T) {
	// LatencyOf sorts in place; pass an out-of-order sample and check the
	// percentiles still come out right.
	sample := []time.Duration{
		10 * time.Millisecond,
		1 * time.Millisecond,
		5 * time.Millisecond,
		3 * time.Millisecond,
		7 * time.Millisecond,
	}
	got := LatencyOf(sample)
	if got.MinMs != 1 || got.MaxMs != 10 {
		t.Errorf("min/max = %v/%v, want 1/10", got.MinMs, got.MaxMs)
	}
	// P50 of 5: nearest-rank index ceil(0.5*5)-1 = 2 → sorted[2] = 5ms.
	if got.P50Ms != 5 {
		t.Errorf("P50 = %v, want 5ms", got.P50Ms)
	}
}

func TestLatencyStats_JSON(t *testing.T) {
	s := LatencyStats{N: 3, MinMs: 1, MaxMs: 3, MeanMs: 2, P50Ms: 2, P95Ms: 3, P99Ms: 3}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var back LatencyStats
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if back != s {
		t.Fatalf("round-trip = %+v, want %+v", back, s)
	}
}

func TestAllocSnapshot_Delta(t *testing.T) {
	start := StartAlloc()
	// Allocate ~1 MiB in known-sized objects so the delta is non-zero.
	const n = 1024
	keep := make([][]byte, n)
	for i := range keep {
		keep[i] = make([]byte, 1024)
	}
	d := start.Delta()
	if d.BytesAllocated == 0 {
		t.Fatalf("BytesAllocated = 0, want > 0")
	}
	if d.ObjectsAllocated == 0 {
		t.Fatalf("ObjectsAllocated = 0, want > 0")
	}
	// Keep `keep` reachable so the compiler doesn't elide the allocs.
	if len(keep) != n {
		t.Fatal("unreachable")
	}
}

func TestAllocDelta_JSON(t *testing.T) {
	d := AllocDelta{BytesAllocated: 4096, ObjectsAllocated: 16}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(b) != `{"bytes_allocated":4096,"objects_allocated":16}` {
		t.Fatalf("Marshal = %s", b)
	}
}

func TestHeapSnapshot_NonZero(t *testing.T) {
	h := HeapSnapshot()
	// A running Go program always has non-zero HeapInuse / HeapSys / Sys.
	if h.HeapInuseBytes == 0 {
		t.Errorf("HeapInuseBytes = 0")
	}
	if h.HeapSysBytes == 0 {
		t.Errorf("HeapSysBytes = 0")
	}
	if h.SysBytes == 0 {
		t.Errorf("SysBytes = 0")
	}
	// Sys is the total runtime reservation; should be >= HeapSys.
	if h.SysBytes < h.HeapSysBytes {
		t.Errorf("SysBytes (%d) < HeapSysBytes (%d)", h.SysBytes, h.HeapSysBytes)
	}
}

func TestHeapStats_JSON(t *testing.T) {
	h := HeapStats{HeapInuseBytes: 1 << 20, HeapSysBytes: 2 << 20, SysBytes: 4 << 20}
	b, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var back HeapStats
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if back != h {
		t.Fatalf("round-trip = %+v, want %+v", back, h)
	}
}
