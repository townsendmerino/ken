package regex

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/townsendmerino/ken/internal/chunk"
)

// loadFixture reads a real source file from this repo. The Go fixture
// (ken's own internal/search/index.go) is the same one BM25 / embed
// benchmarks use — non-trivial size (~30KB, 650 lines). The TypeScript
// and Python fixtures come from testdata/repo/, which are smaller (~25
// lines each) but real — there are no larger TS/Py files checked in
// outside the COIR bench corpus, and reaching into that would couple
// the chunker benchmarks to bench data they don't otherwise share.
// Briefing judgment call #5: documented the choice in this comment +
// the commit message.
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

func BenchmarkChunker_Go(b *testing.B) {
	src := loadFixture(b, filepath.Join("..", "..", "search", "index.go"))
	benchChunk(b, "go", src)
}

func BenchmarkChunker_TypeScript(b *testing.B) {
	src := loadFixture(b, filepath.Join("..", "..", "..", "testdata", "repo", "widget.ts"))
	benchChunk(b, "typescript", src)
}

func BenchmarkChunker_Python(b *testing.B) {
	src := loadFixture(b, filepath.Join("..", "..", "..", "testdata", "repo", "auth.py"))
	benchChunk(b, "python", src)
}
