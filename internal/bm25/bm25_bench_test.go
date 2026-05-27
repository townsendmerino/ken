package bm25

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadRealSource reads a real Go source file from this repo. The choice
// is deliberate — ken's own internal/search/index.go is checked in,
// non-trivial in size, exercises camelCase / snake_case / digit-bearing
// identifiers, and would never be missing on a developer machine.
// Skips if not found (so benchmarks don't fail in an unusual checkout).
func loadRealSource(b *testing.B) string {
	b.Helper()
	// internal/bm25 → ../search/index.go from this file's CWD when go test
	// runs (CWD = package dir, by Go test convention).
	path := filepath.Join("..", "search", "index.go")
	data, err := os.ReadFile(path)
	if err != nil {
		b.Skipf("real source not found at %s: %v", path, err)
	}
	return string(data)
}

func BenchmarkTokenize(b *testing.B) {
	src := loadRealSource(b)
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Tokenize(src)
	}
}

// buildBenchCorpus builds a synthetic N-document corpus from a single
// real source file by splitting it into roughly equal line-count chunks.
// Each "document" is the chunk's lines re-joined with newlines, then
// tokenized. Deterministic; same input always produces the same corpus.
func buildBenchCorpus(src string, numDocs int) [][]string {
	lines := strings.Split(src, "\n")
	if numDocs <= 0 {
		numDocs = 1
	}
	per := len(lines) / numDocs
	if per < 1 {
		per = 1
	}
	docs := make([][]string, 0, numDocs)
	for i := 0; i < numDocs && i*per < len(lines); i++ {
		end := (i + 1) * per
		if end > len(lines) {
			end = len(lines)
		}
		chunk := strings.Join(lines[i*per:end], "\n")
		docs = append(docs, Tokenize(chunk))
	}
	return docs
}

func BenchmarkScore(b *testing.B) {
	src := loadRealSource(b)
	// Table-driven: small / medium corpus. The 1000-doc size is the
	// briefing's prediction-#2-falsification target; the 100-doc size
	// provides a fast inner-loop variant.
	cases := []struct {
		name string
		n    int
	}{
		{"N100", 100},
		{"N1000", 1000},
	}
	query := Tokenize("index search build chunks")
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			corpus := buildBenchCorpus(src, tc.n)
			if len(corpus) == 0 {
				b.Skipf("corpus build produced 0 docs for N=%d", tc.n)
			}
			ix := Build(corpus)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = ix.TopK(query, 10)
			}
		})
	}
}
