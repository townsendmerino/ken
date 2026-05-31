package coderank

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestMmapLoad_cosineMatchesHeap pins the M8 invariant: the mmap-loaded
// model produces bit-identical outputs to the heap-loaded model. Tensor
// data is the SAME safetensors blob just accessed via the page cache
// instead of a heap-resident []byte — so this isn't really testing
// inference correctness, it's testing that the unsafe-slice aliasing
// to the mapped region works correctly.
//
// Skipped when the model isn't available (same gate as other tests).
func TestMmapLoad_cosineMatchesHeap(t *testing.T) {
	if _, err := os.Stat(modelDir); errors.Is(err, fs.ErrNotExist) {
		t.Skipf("no model at %s", modelDir)
	}

	heap, err := LoadFromFS(os.DirFS(modelDir), ".")
	if err != nil {
		t.Fatalf("heap Load: %v", err)
	}
	mmaped, err := Load(modelDir)
	if err != nil {
		t.Fatalf("mmap Load: %v", err)
	}
	defer mmaped.weights.st.Close()

	// Spot-check: a small sample of tensors must be byte-identical.
	heapWE := heap.weights.WordEmb
	mmapWE := mmaped.weights.WordEmb
	if len(heapWE) != len(mmapWE) {
		t.Fatalf("WordEmb len: heap=%d mmap=%d", len(heapWE), len(mmapWE))
	}
	// Compare a slice (full compare is 30528*768=23M elements; spot-check
	// the first 1000 + last 1000 — if mmap parsing got the offsets wrong
	// it shows up immediately at row boundaries).
	for i := 0; i < 1000; i++ {
		if heapWE[i] != mmapWE[i] {
			t.Fatalf("WordEmb[%d]: heap=%v mmap=%v", i, heapWE[i], mmapWE[i])
		}
	}
	for i := len(heapWE) - 1000; i < len(heapWE); i++ {
		if heapWE[i] != mmapWE[i] {
			t.Fatalf("WordEmb[%d]: heap=%v mmap=%v", i, heapWE[i], mmapWE[i])
		}
	}

	// End-to-end: both models encode the same text to the same vector.
	for _, text := range []string{"hello world", "def fib(n): return n"} {
		hv, err := heap.Encode(text, false)
		if err != nil {
			t.Fatal(err)
		}
		mv, err := mmaped.Encode(text, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(hv) != len(mv) {
			t.Fatalf("Encode(%q) dim: heap=%d mmap=%d", text, len(hv), len(mv))
		}
		// Tensor data is identical, the f32 reduction order is too, so
		// these should be BIT-equal.
		for i := range hv {
			if hv[i] != mv[i] {
				t.Errorf("Encode(%q)[%d]: heap=%v mmap=%v", text, i, hv[i], mv[i])
				break
			}
		}
	}
}

// TestMmapLoad_startupTimeInfo reports — without asserting — the wall-
// clock load time of both paths on a warm page cache.
//
// HONEST FINDINGS to record (M8 memo, operator docs):
//
//   - On darwin/arm64 (Apple Silicon) the in-process Go-runtime metrics
//     CANNOT distinguish mmap from heap: syscall.Mmap allocations show
//     up in MemStats.HeapAlloc just like heap allocations do. The
//     plan §4 expectation that mmap "lowers Go heap RSS" doesn't hold
//     for this particular measurement on this platform.
//   - On warm page cache, the heap-backed path is actually FASTER to
//     `Load`: a single 547 MB sequential read is ~140 ms, while the
//     mmap path pays for VMA setup + faulting in every page on first
//     tensor access. The order flips on COLD cache (first ever load),
//     where mmap's lazy paging beats reading 547 MB through the kernel
//     into a Go buffer — but that's hard to test deterministically.
//   - The operator-visible mmap wins (cross-process page-cache sharing
//     for multi-instance deployments, swap-friendly clean pages under
//     memory pressure, idle pages eligible for eviction) are real but
//     observed via `top`/Activity Monitor, NOT MemStats. They matter
//     for ken-mcp servers running long-lived in cgroups with memory
//     limits; not for short CLI runs.
//
// This test logs the timings for the record without failing the suite.
func TestMmapLoad_startupTimeInfo(t *testing.T) {
	if _, err := os.Stat(modelDir); errors.Is(err, fs.ErrNotExist) {
		t.Skipf("no model at %s", modelDir)
	}
	// Warm-up runs so we measure the steady-state warm-cache path,
	// not the very-first cold-cache one.
	warm, _ := LoadFromFS(os.DirFS(modelDir), ".")
	runtime.KeepAlive(warm)
	if mw, _ := Load(modelDir); mw != nil {
		_ = mw.weights.st.Close()
	}

	timeIt := func(label string, loader func() (*Model, error)) (*Model, time.Duration) {
		t0 := time.Now()
		m, err := loader()
		took := time.Since(t0)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %.1fms", label, float64(took.Microseconds())/1000.0)
		return m, took
	}

	heapM, _ := timeIt("heap Load (warm cache)", func() (*Model, error) {
		return LoadFromFS(os.DirFS(modelDir), ".")
	})
	mmapM, _ := timeIt("mmap Load (warm cache)", func() (*Model, error) { return Load(modelDir) })

	runtime.KeepAlive(heapM)
	runtime.KeepAlive(mmapM)
	defer mmapM.weights.st.Close()
}

// TestMmapLoad_closeIdempotent: SafetensorsFile.Close must be safe to
// call multiple times. Once closed, the underlying mapping is gone;
// the finalizer is removed so it doesn't double-free at GC.
func TestMmapLoad_closeIdempotent(t *testing.T) {
	if _, err := os.Stat(filepath.Join(modelDir, "model.safetensors")); errors.Is(err, fs.ErrNotExist) {
		t.Skip("no model")
	}
	m, err := Load(modelDir)
	if err != nil {
		t.Fatal(err)
	}
	// First close: should munmap.
	if err := m.weights.st.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	// Second close: no-op.
	if err := m.weights.st.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}
