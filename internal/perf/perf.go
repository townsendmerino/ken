// Package perf is the measurement-helper library backing `ken perf` and
// `scripts/perf_collect.sh`. Three responsibilities:
//
//   - LatencyStats over a []time.Duration sample (p50/p95/p99/min/max/mean).
//   - AllocSnapshot wrapping runtime.MemStats deltas (bytes + objects).
//   - HeapSnapshot for the Go-runtime view of live + reserved memory.
//
// All three types are JSON-marshallable so `ken perf {index,search,watch}`
// can emit a single record per invocation that benchstat / pprof / ad-hoc
// shell tooling consumes downstream.
//
// Note on RSS: HeapSnapshot reports the *Go runtime* view only. The
// truthful OS-level peak RSS comes from wrapping the binary in
// `/usr/bin/time -v` (or `gtime -v` on macOS) at the shell level —
// `scripts/perf_collect.sh` does that.
package perf

import (
	"runtime"
	"sort"
	"time"
)

// LatencyStats summarises a sample of durations. Marshalled in milliseconds
// (float64) so the JSON record is human-readable and benchstat-friendly;
// nanosecond precision via time.Duration is retained at compute time.
type LatencyStats struct {
	N      int     `json:"n"`
	MinMs  float64 `json:"min_ms"`
	MaxMs  float64 `json:"max_ms"`
	MeanMs float64 `json:"mean_ms"`
	P50Ms  float64 `json:"p50_ms"`
	P95Ms  float64 `json:"p95_ms"`
	P99Ms  float64 `json:"p99_ms"`
}

// LatencyOf computes LatencyStats from a sample. The sample is sorted
// in-place (caller-visible side effect; nearly always fine since the
// caller built the slice for measurement and discards it). Empty sample
// returns the zero value.
//
// Percentile convention: nearest-rank, 1-indexed. P50 of N=10 is the
// 5th element (index 4). Matches what benchstat / hdr-histogram do and
// avoids the off-by-one P95-of-N=20-is-the-19th-element trap.
func LatencyOf(sample []time.Duration) LatencyStats {
	n := len(sample)
	if n == 0 {
		return LatencyStats{}
	}
	sort.Slice(sample, func(i, j int) bool { return sample[i] < sample[j] })
	toMs := func(d time.Duration) float64 { return float64(d.Microseconds()) / 1000.0 }
	idx := func(p float64) int {
		// nearest-rank 1-indexed: ceil(p * n) - 1, clamped to [0, n-1].
		i := int((p*float64(n))+0.999999) - 1
		if i < 0 {
			i = 0
		}
		if i >= n {
			i = n - 1
		}
		return i
	}
	var sum time.Duration
	for _, d := range sample {
		sum += d
	}
	return LatencyStats{
		N:      n,
		MinMs:  toMs(sample[0]),
		MaxMs:  toMs(sample[n-1]),
		MeanMs: toMs(sum) / float64(n),
		P50Ms:  toMs(sample[idx(0.50)]),
		P95Ms:  toMs(sample[idx(0.95)]),
		P99Ms:  toMs(sample[idx(0.99)]),
	}
}

// AllocSnapshot captures runtime.MemStats's cumulative TotalAlloc +
// Mallocs at a point in time. Compare two via Delta() to get bytes +
// objects allocated between the two snapshots; both fields are
// monotonic so the subtraction is always non-negative.
//
// runtime.GC() is intentionally NOT called inside StartAlloc — the
// caller decides whether to GC first (a fresh GC stabilises the
// post-snapshot heap but takes wall time the caller may not want to
// pay).
type AllocSnapshot struct {
	totalAlloc uint64 // cumulative bytes ever allocated
	mallocs    uint64 // cumulative object allocations
}

// StartAlloc captures the current cumulative allocation counters.
// Use Delta() on a later snapshot to compute allocations since.
func StartAlloc() AllocSnapshot {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return AllocSnapshot{totalAlloc: m.TotalAlloc, mallocs: m.Mallocs}
}

// AllocDelta is the bytes + objects allocated between two AllocSnapshots.
type AllocDelta struct {
	BytesAllocated   uint64 `json:"bytes_allocated"`
	ObjectsAllocated uint64 `json:"objects_allocated"`
}

// Delta returns the allocation delta from s to the current MemStats.
// runtime.MemStats counters are monotonic, so the subtraction never
// underflows in a single process.
func (s AllocSnapshot) Delta() AllocDelta {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return AllocDelta{
		BytesAllocated:   m.TotalAlloc - s.totalAlloc,
		ObjectsAllocated: m.Mallocs - s.mallocs,
	}
}

// HeapStats is the Go-runtime view of memory in use. NOT a substitute
// for OS-level peak RSS — that requires `/usr/bin/time -v` (Linux) or
// `gtime -v` (macOS) wrapping the binary. The fields below are the
// subset of runtime.MemStats worth publishing in a `ken perf` record:
//
//   - HeapInuseBytes:   bytes in in-use heap spans (live working set).
//   - HeapSysBytes:     bytes reserved from the OS for the heap (high-water).
//   - SysBytes:         total bytes reserved by the Go runtime (heap+stack+other).
type HeapStats struct {
	HeapInuseBytes uint64 `json:"heap_inuse_bytes"`
	HeapSysBytes   uint64 `json:"heap_sys_bytes"`
	SysBytes       uint64 `json:"sys_bytes"`
}

// HeapSnapshot reads the current MemStats and returns the publishable
// subset. Cheap; safe to call repeatedly.
func HeapSnapshot() HeapStats {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return HeapStats{
		HeapInuseBytes: m.HeapInuse,
		HeapSysBytes:   m.HeapSys,
		SysBytes:       m.Sys,
	}
}
