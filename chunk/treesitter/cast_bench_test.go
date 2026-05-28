package treesitter

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/townsendmerino/ken/chunk"
)

// loadFixture is the same realistic-input pattern as the regex chunker's
// benchmark — see internal/chunk/regex/chunker_bench_test.go for the
// fixture-choice rationale (Go = ken's own internal/search/index.go;
// TS + Py = testdata/repo/* small but real). Using the same files lets
// a Phase-1 benchstat run directly diff regex-vs-treesitter per-language
// throughput on identical input.
func loadFixture(b *testing.B, relPath string) []byte {
	b.Helper()
	data, err := os.ReadFile(relPath)
	if err != nil {
		b.Skipf("fixture not found at %s: %v", relPath, err)
	}
	return data
}

func benchChunk(b *testing.B, lang string, source []byte) {
	c := New()
	// Warm up: first call materialises the parser pool entry for this
	// language (gotreesitter allocates a fresh parser on first use).
	// We want to measure steady-state cAST cost, not first-call pool
	// init, so do one call before ResetTimer.
	if _, err := c.Chunk(source, lang, chunk.DefaultChunkSize); err != nil {
		b.Fatalf("warm-up Chunk: %v", err)
	}
	b.SetBytes(int64(len(source)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := c.Chunk(source, lang, chunk.DefaultChunkSize)
		if err != nil {
			b.Fatalf("Chunk: %v", err)
		}
	}
}

func BenchmarkCAST_Go(b *testing.B) {
	src := loadFixture(b, filepath.Join("..", "..", "search", "index.go"))
	benchChunk(b, "go", src)
}

func BenchmarkCAST_TypeScript(b *testing.B) {
	src := loadFixture(b, filepath.Join("..", "..", "..", "testdata", "repo", "widget.ts"))
	benchChunk(b, "typescript", src)
}

func BenchmarkCAST_Python(b *testing.B) {
	src := loadFixture(b, filepath.Join("..", "..", "..", "testdata", "repo", "auth.py"))
	benchChunk(b, "python", src)
}
