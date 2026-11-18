package engine

import (
	"os"
	"slices"
	"testing"
	"time"
)

// TestScopeBench_NarrowNotSlower is F1's timing gate, run by hand:
//
//	KEN_SCOPE_BENCH_ROOT=/path/to/repo KEN_SCOPE_BENCH_PATH=substr go test ./engine -run ScopeBench -v
//
// It prints, per mode, the median of 10 timed queries for the unfiltered
// search and for the same query restricted to files containing the substring.
// Skips unless the root is set.
func TestScopeBench_NarrowNotSlower(t *testing.T) {
	root := os.Getenv("KEN_SCOPE_BENCH_ROOT")
	sub := os.Getenv("KEN_SCOPE_BENCH_PATH")
	if root == "" || sub == "" {
		t.Skip("set KEN_SCOPE_BENCH_ROOT and KEN_SCOPE_BENCH_PATH")
	}
	queries := []string{"parse the request body", "handler error", "cache eviction policy", "open file and read"}
	for _, mode := range []string{"bm25", "semantic", "hybrid"} {
		ws, err := Open(t.Context(), root, Options{Mode: mode, ModelDir: os.Getenv("KEN_SCOPE_BENCH_MODEL")})
		if err != nil {
			t.Fatal(err)
		}
		m, _ := ParseMode(mode)
		files, chunks := ws.ScopeSize(Scope{PathContains: sub})
		median := func(sc Scope) time.Duration {
			var all []time.Duration
			for _, q := range queries {
				_, _, _ = ws.SearchResults(t.Context(), q, 10, m, SearchOptions{Scope: sc}) // warm
				var d []time.Duration
				for range 10 {
					t0 := time.Now()
					_, _, _ = ws.SearchResults(t.Context(), q, 10, m, SearchOptions{Scope: sc})
					d = append(d, time.Since(t0))
				}
				slices.Sort(d)
				all = append(all, d[len(d)/2])
			}
			slices.Sort(all)
			return all[len(all)/2]
		}
		un, sc := median(Scope{}), median(Scope{PathContains: sub})
		t.Logf("%-8s chunks=%d scope(%q)=%d files/%d chunks  unfiltered=%v  scoped=%v", mode, ws.IndexStatus().ChunkCount, sub, files, chunks, un, sc)
		_ = ws.Close()
	}
}
