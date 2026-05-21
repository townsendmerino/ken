// Package search is the orchestration layer. Stage 4 completes the
// hybrid pipeline: walk → chunk → {BM25 lexical | Model2Vec semantic} →
// RRF fuse → file-coherence + query boosts → path penalties, all ported
// verbatim from semble (search.py + ranking/*). Hybrid is the default.
package search

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/townsendmerino/ken/internal/ann"
	"github.com/townsendmerino/ken/internal/bm25"
	"github.com/townsendmerino/ken/internal/chunk"
	_ "github.com/townsendmerino/ken/internal/chunk/regex"      // registers the "regex" chunker
	_ "github.com/townsendmerino/ken/internal/chunk/treesitter" // registers the "treesitter" chunker
	"github.com/townsendmerino/ken/internal/embed"
	"github.com/townsendmerino/ken/internal/repo"
)

// Mode selects the retrieval strategy.
type Mode int

const (
	ModeBM25 Mode = iota
	ModeSemantic
	ModeHybrid
)

// ParseMode maps a CLI string to a Mode.
func ParseMode(s string) (Mode, error) {
	switch s {
	case "bm25":
		return ModeBM25, nil
	case "semantic":
		return ModeSemantic, nil
	case "hybrid":
		return ModeHybrid, nil
	}
	return 0, fmt.Errorf("search: unknown mode %q (want %v)", s, ModeNames())
}

// ModeNames returns the CLI strings accepted by ParseMode, in CLI-flag
// order (bm25, semantic, hybrid). Callers building allowed-value lists
// for env-var / flag validation should use this rather than hardcoding.
func ModeNames() []string { return []string{"bm25", "semantic", "hybrid"} }

func (m Mode) needsModel() bool { return m == ModeSemantic || m == ModeHybrid }

// Result is one ranked chunk.
type Result struct {
	Chunk chunk.Chunk
	Score float64
}

// Index is a built, queryable index over a directory tree.
type Index struct {
	mode   Mode
	chunks []chunk.Chunk
	bm     *bm25.Index
	model  *embed.StaticModel // nil for ModeBM25
	flat   *ann.Flat          // nil for ModeBM25
}

// FromPath walks root, chunks every indexable file with the named chunker,
// builds the BM25 index, and (for semantic/hybrid) embeds every chunk with
// the Model2Vec model at modelDir.
func FromPath(root string, mode Mode, chunkerName, modelDir string) (*Index, error) {
	if mode != ModeBM25 && mode != ModeSemantic && mode != ModeHybrid {
		return nil, fmt.Errorf("search: unknown mode %d", mode)
	}
	if _, err := chunk.Get(chunkerName); err != nil {
		return nil, err
	}
	var model *embed.StaticModel
	if mode.needsModel() {
		if modelDir == "" {
			return nil, fmt.Errorf("search: mode requires an embedding model — pass --model <dir> (or use --mode=bm25)")
		}
		m, err := embed.Load(modelDir)
		if err != nil {
			return nil, fmt.Errorf("search: load model: %w", err)
		}
		model = m
	}

	files, err := repo.Walk(repo.Options{Root: root})
	if err != nil {
		return nil, err
	}
	var (
		chunks []chunk.Chunk
		docs   [][]string
		vecs   [][]float32
	)
	for _, rel := range files {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return nil, err
		}
		cs, err := chunk.ChunkFile(chunkerName, rel, data, chunk.DefaultChunkSize)
		if err != nil {
			return nil, err
		}
		for _, c := range cs {
			chunks = append(chunks, c)
			docs = append(docs, bm25.Tokenize(c.Text))
			if model != nil {
				vecs = append(vecs, model.Encode(c.Text))
			}
		}
	}
	ix := &Index{mode: mode, chunks: chunks, bm: bm25.Build(docs), model: model}
	if model != nil {
		ix.flat = ann.New(vecs)
	}
	return ix, nil
}

// Len is the number of indexed chunks.
func (ix *Index) Len() int { return len(ix.chunks) }

// Chunks returns a read-only view of the master chunk slice (used by the
// MCP find_related handler to resolve a file:line into an indexed chunk).
func (ix *Index) Chunks() []chunk.Chunk { return ix.chunks }

// ResolveChunk returns the chunk that contains the 1-indexed line in
// filePath, or nil if there is none. Mirrors semble utils._resolve_chunk:
// prefer an interior hit (line < end_line); fall back to a boundary
// (line == end_line) so end-of-file references still resolve.
func (ix *Index) ResolveChunk(filePath string, line int) *chunk.Chunk {
	var fallback *chunk.Chunk
	for i := range ix.chunks {
		c := &ix.chunks[i]
		if c.File == filePath && c.StartLine <= line && line <= c.EndLine {
			if line < c.EndLine {
				return c
			}
			if fallback == nil {
				fallback = c
			}
		}
	}
	return fallback
}

// FindRelated returns chunks semantically similar to the chunk containing
// (filePath, line). Requires a semantic/hybrid index (a model is loaded);
// returns an error otherwise. Mirrors semble Index.find_related: query
// with the source chunk's text, fetch top-(k+1), drop the source itself,
// trim to k.
func (ix *Index) FindRelated(filePath string, line, k int) ([]Result, error) {
	if ix.model == nil || ix.flat == nil {
		return nil, fmt.Errorf("search: FindRelated requires semantic or hybrid mode")
	}
	src := ix.ResolveChunk(filePath, line)
	if src == nil {
		return nil, nil
	}
	hits := ix.flat.Query(ix.model.Encode(src.Text), k+1)
	out := make([]Result, 0, k)
	for _, h := range hits {
		c := ix.chunks[h.Index]
		if c.File == src.File && c.StartLine == src.StartLine && c.EndLine == src.EndLine {
			continue
		}
		out = append(out, Result{Chunk: c, Score: h.Score})
		if len(out) >= k {
			break
		}
	}
	return out, nil
}

// Search returns the top-k chunks for query under the index's mode.
func (ix *Index) Search(query string, k int) []Result {
	switch ix.mode {
	case ModeSemantic:
		// semble search_semantic: cosine similarity, no rerank.
		hits := ix.flat.Query(ix.model.Encode(query), k)
		out := make([]Result, len(hits))
		for i, h := range hits {
			out[i] = Result{Chunk: ix.chunks[h.Index], Score: h.Score}
		}
		return out
	case ModeHybrid:
		ranked := hybridSearch(query, ix.model.Encode(query), ix.flat, ix.bm, ix.chunks, k, -1)
		out := make([]Result, len(ranked))
		for i, r := range ranked {
			out[i] = Result{Chunk: ix.chunks[r.idx], Score: r.score}
		}
		return out
	default: // ModeBM25 — raw lexical (Stage 1 behavior, no rerank)
		hits := ix.bm.TopK(bm25.Tokenize(query), k)
		out := make([]Result, len(hits))
		for i, h := range hits {
			out[i] = Result{Chunk: ix.chunks[h.Doc], Score: h.Score}
		}
		return out
	}
}
