package search

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/townsendmerino/aikit/chunk"
	"github.com/townsendmerino/aikit/encoder"
)

// BenchmarkNeuralRerank exercises the full neural-rerank pipeline at
// ken's actual call site — query encode + candidate batch encode +
// cosine score + sort — driven through (*NeuralReranker).Rerank with
// realistic shapes (N=50 cold candidates, then a warm re-run for the
// cache-hit path).
//
// The point is to put aikit's encoder upgrades in front of ken's hot
// path so future bumps can be benchstat'd directly. aikit's own
// encoder benches measure the kernels in isolation; this measures
// what ken actually pays per rerank query.
//
// Skipped when the encoder model isn't symlinked at testdata/encoder-model.
//
//	go test -bench=BenchmarkNeuralRerank -benchtime=10x -count=5 \
//	    -run=^$ ./internal/search/
func BenchmarkNeuralRerank(b *testing.B) {
	encoderModelDir := "../../testdata/encoder-model"
	if _, err := os.Stat(filepath.Join(encoderModelDir, "model.safetensors")); errors.Is(err, fs.ErrNotExist) {
		b.Skipf("no encoder model at %s — symlink HF snapshot to enable", encoderModelDir)
	}
	rm, err := encoder.Load(encoderModelDir)
	if err != nil {
		b.Fatalf("encoder.Load: %v", err)
	}
	rm.SetMaxSeqLength(256) // typical chunk length; matches rerank_integration_test.go's intent

	const N = 50
	cands := buildNeuralCandidates(N)
	query := "build search index from a corpus path"

	b.Run("Cold", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			// New reranker each iter ⇒ cache cold ⇒ every candidate
			// pays a forward pass. This is the upper-bound rerank
			// latency users see on a fresh query against fresh
			// candidates.
			nr := NewNeuralReranker(rm)
			scores := nr.Rerank(query, cands)
			if len(scores) != len(cands) {
				b.Fatalf("got %d scores, want %d", len(scores), len(cands))
			}
		}
	})

	b.Run("WarmCache", func(b *testing.B) {
		// Pre-warm: one Rerank call populates the cache; subsequent
		// iters hit it. This is the steady-state path users see when
		// the same docs keep appearing in candidate lists across
		// queries.
		nr := NewNeuralReranker(rm)
		_ = nr.Rerank(query, cands)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_ = nr.Rerank(query, cands)
		}
	})
}

// buildNeuralCandidates produces N realistic code chunks: short Go
// snippets with identifiers + bodies the BM25 tokenizer + transformer
// see in real corpora. Varying file paths so the per-file aggregation
// inside neural_rerank.go behaves like production.
func buildNeuralCandidates(n int) []chunk.Chunk {
	out := make([]chunk.Chunk, n)
	for i := range n {
		text := fmt.Sprintf(`package idx

// BuildIndex%d walks a corpus path and builds a search index.
// It returns the index and any walk error encountered.
func BuildIndex%d(ctx context.Context, root string) (*Index, error) {
	chunks, err := walkAndChunk(ctx, root)
	if err != nil {
		return nil, fmt.Errorf("walk: %%w", err)
	}
	return BuildFromChunks(chunks), nil
}
`, i, i)
		out[i] = chunk.Chunk{
			File:      fmt.Sprintf("pkg/idx%d/build.go", i%8),
			StartLine: 1,
			EndLine:   10,
			Text:      text,
		}
	}
	return out
}
