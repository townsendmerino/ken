package embed

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// modelDirForBench resolves testdata/model/ (the per-machine, not-committed
// model snapshot). Skip pattern mirrors internal/embed/parity_test.go +
// internal/embed/golden_test.go: a fresh checkout has no model and the
// benchmark must not fail there.
func modelDirForBench(b *testing.B) string {
	b.Helper()
	dir := filepath.Join("..", "..", "testdata", "model")
	if _, err := os.Stat(filepath.Join(dir, "model.safetensors")); err != nil {
		b.Skip("testdata/model/ not present; see testdata/README.md")
	}
	return dir
}

// loadBenchSource reads a real Go file from this repo for inference
// input. Uses internal/search/index.go for the same reason BM25's
// benchmarks do — non-trivial size, realistic identifier shapes,
// always checked in.
func loadBenchSource(b *testing.B) string {
	b.Helper()
	data, err := os.ReadFile(filepath.Join("..", "search", "index.go"))
	if err != nil {
		b.Skipf("source not found: %v", err)
	}
	return string(data)
}

// chunkSource splits src into approximately N equal pieces by line count.
// Used so BenchmarkInferBatch has a realistic batch of distinct chunks
// rather than 1000 copies of the same string (which the runtime/CPU might
// optimise unrealistically well).
func chunkSource(src string, n int) []string {
	lines := strings.Split(src, "\n")
	if n <= 0 {
		n = 1
	}
	per := len(lines) / n
	if per < 1 {
		per = 1
	}
	out := make([]string, 0, n)
	for i := 0; i < n && i*per < len(lines); i++ {
		end := (i + 1) * per
		if end > len(lines) {
			end = len(lines)
		}
		out = append(out, strings.Join(lines[i*per:end], "\n"))
	}
	return out
}

func BenchmarkInferOne(b *testing.B) {
	m, err := Load(modelDirForBench(b))
	if err != nil {
		b.Fatalf("Load: %v", err)
	}
	// A single representative chunk. Take the first ~50 lines of the
	// source file — comparable to one chunker output in steady state.
	chunks := chunkSource(loadBenchSource(b), 50)
	if len(chunks) == 0 {
		b.Skip("no chunks built from source")
	}
	text := chunks[0]
	b.SetBytes(int64(len(text)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.Encode(text)
	}
}

func BenchmarkInferBatch(b *testing.B) {
	m, err := Load(modelDirForBench(b))
	if err != nil {
		b.Fatalf("Load: %v", err)
	}
	const batchSize = 1000
	chunks := chunkSource(loadBenchSource(b), batchSize)
	if len(chunks) == 0 {
		b.Skip("no chunks built from source")
	}
	// Aggregate bytes across the batch so SetBytes reflects total work,
	// not per-call work. benchstat MB/s thus reads as embedding throughput.
	var totalBytes int64
	for _, c := range chunks {
		totalBytes += int64(len(c))
	}
	b.SetBytes(totalBytes)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, c := range chunks {
			_ = m.Encode(c)
		}
	}
}
