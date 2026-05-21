// Package search is the orchestration layer. Stage 4 completes the
// hybrid pipeline: walk → chunk → {BM25 lexical | Model2Vec semantic} →
// RRF fuse → file-coherence + query boosts → path penalties, all ported
// verbatim from semble (search.py + ranking/*). Hybrid is the default.
//
// The Mode enum (ModeBM25 / ModeSemantic / ModeHybrid) picks which
// retrievers run and whether the rerank pipeline applies:
//
//   - **ModeBM25** runs only the lexical retriever (BM25.TopK) — no
//     rerank, no semantic, no model required. Corresponds to semble's
//     "BM25 raw" row, not "BM25 + ranking".
//   - **ModeSemantic** runs only the dense retriever (cosine over a
//     flat ANN index) — no rerank. Corresponds to semble's
//     "potion-code-16M raw" row.
//   - **ModeHybrid** runs both, normalizes each via RRF (1/(60+rank),
//     rank-based so absolute scores don't matter), α-weight-fuses
//     (semble's resolveAlpha: α=0.3 for symbol queries, 0.5 for NL),
//     then applies the full rerank pipeline.
//
// Pipeline-order invariants in ModeHybrid (hybrid.go + rerank.go +
// penalties.go) — porting bug if reordered:
//
//   - candidate over-fetch (k*5) BEFORE any boosting
//   - RRF normalization BEFORE α-fusion (rank-based, not score-based)
//   - boost_multi_chunk_files BEFORE apply_query_boost
//   - rerank_topk's path penalties applied LAST, gated `alpha < 1.0`
//     (semantic-only mode skips path penalties)
//
// See docs/DESIGN.md §7 for the constants and the file:line audit trail
// back to semble's live source.
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
//
// Implementation note (v0.3): FromPath is now a thin wrapper around
// walkAndChunk + BuildIndex. The split exists because internal/search/
// watch.go reuses BuildIndex to publish new snapshots after incremental
// re-chunk / re-embed work — it shouldn't re-walk the tree just to
// rebuild the index struct.
func FromPath(root string, mode Mode, chunkerName, modelDir string) (*Index, error) {
	chunks, vecs, model, err := walkAndChunk(root, mode, chunkerName, modelDir)
	if err != nil {
		return nil, err
	}
	return BuildIndex(chunks, vecs, mode, model), nil
}

// walkAndChunk does the corpus-bootstrapping half of FromPath: validate
// the mode + chunker, load the model (if needed), walk root, chunk every
// file, embed every chunk under semantic/hybrid. Returns the raw
// materials BuildIndex needs. Internal to v0.3's incremental indexing —
// the watcher keeps its own copies of chunks + vecs around as the
// mutable corpus state.
func walkAndChunk(root string, mode Mode, chunkerName, modelDir string) (
	chunks []chunk.Chunk, vecs [][]float32, model *embed.StaticModel, err error,
) {
	if mode != ModeBM25 && mode != ModeSemantic && mode != ModeHybrid {
		return nil, nil, nil, fmt.Errorf("search: unknown mode %d", mode)
	}
	if _, err := chunk.Get(chunkerName); err != nil {
		return nil, nil, nil, err
	}
	if mode.needsModel() {
		if modelDir == "" {
			return nil, nil, nil, fmt.Errorf("search: mode requires an embedding model — pass --model <dir> (or use --mode=bm25)")
		}
		m, err := embed.Load(modelDir)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("search: load model: %w", err)
		}
		model = m
	}

	files, err := repo.Walk(repo.Options{Root: root})
	if err != nil {
		return nil, nil, nil, err
	}
	for _, rel := range files {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return nil, nil, nil, err
		}
		cs, err := chunk.ChunkFile(chunkerName, rel, data, chunk.DefaultChunkSize)
		if err != nil {
			return nil, nil, nil, err
		}
		for _, c := range cs {
			chunks = append(chunks, c)
			if model != nil {
				vecs = append(vecs, model.Encode(c.Text))
			}
		}
	}
	return chunks, vecs, model, nil
}

// BuildIndex assembles a snapshot *Index from a chunks slice and (for
// semantic/hybrid) the parallel embedding vectors. It re-tokenizes every
// chunk for BM25 — incremental BM25 postings updates are intentionally
// not implemented (see docs/DECISIONS.md ADR-012; the rebuild is dwarfed
// by embedding cost on real workloads).
//
// Tombstoned chunks are kept in the chunks slice so callers can rely on
// stable indices into bm25/ann across snapshots. BuildIndex emits an
// empty token list for each tombstoned chunk so its terms don't bump
// df, and uses the caller-supplied vec (which can be the chunk's
// original embedding) as the matching row in ann.Flat. Every read path
// (Search / FindRelated / ResolveChunk) checks Tombstoned before
// returning a result.
func BuildIndex(chunks []chunk.Chunk, vecs [][]float32, mode Mode, model *embed.StaticModel) *Index {
	docs := make([][]string, len(chunks))
	for i, c := range chunks {
		if c.Tombstoned {
			docs[i] = nil // contributes no postings; df unaffected
			continue
		}
		docs[i] = bm25.Tokenize(c.Text)
	}
	ix := &Index{mode: mode, chunks: chunks, bm: bm25.Build(docs), model: model}
	if model != nil {
		ix.flat = ann.New(vecs)
	}
	return ix
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
//
// Tombstoned chunks are skipped — a file that's been deleted under v0.3's
// incremental indexing should resolve to nil even if its chunks are still
// in the slice for index stability.
func (ix *Index) ResolveChunk(filePath string, line int) *chunk.Chunk {
	var fallback *chunk.Chunk
	for i := range ix.chunks {
		c := &ix.chunks[i]
		if c.Tombstoned {
			continue
		}
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
// trim to k. Tombstoned chunks are dropped during the trim — the
// k+tombstone-count over-fetch keeps the result size stable as long as
// tombstone density is small.
func (ix *Index) FindRelated(filePath string, line, k int) ([]Result, error) {
	if ix.model == nil || ix.flat == nil {
		return nil, fmt.Errorf("search: FindRelated requires semantic or hybrid mode")
	}
	src := ix.ResolveChunk(filePath, line)
	if src == nil {
		return nil, nil
	}
	// Over-fetch by the current tombstone count so the filtered result
	// still hits k. Cheap: tombstone count is len(slice)-aligned and
	// flat.Query's cost is linear in vecs anyway.
	overFetch := k + 1 + ix.tombstoneCount()
	hits := ix.flat.Query(ix.model.Encode(src.Text), overFetch)
	out := make([]Result, 0, k)
	for _, h := range hits {
		c := ix.chunks[h.Index]
		if c.Tombstoned {
			continue
		}
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
// Tombstoned chunks are filtered after the underlying retriever returns;
// over-fetch by the tombstone count so the filtered result still hits k
// on indices with edit churn.
func (ix *Index) Search(query string, k int) []Result {
	overFetch := k + ix.tombstoneCount()
	switch ix.mode {
	case ModeSemantic:
		// semble search_semantic: cosine similarity, no rerank.
		hits := ix.flat.Query(ix.model.Encode(query), overFetch)
		out := make([]Result, 0, k)
		for _, h := range hits {
			c := ix.chunks[h.Index]
			if c.Tombstoned {
				continue
			}
			out = append(out, Result{Chunk: c, Score: h.Score})
			if len(out) >= k {
				break
			}
		}
		return out
	case ModeHybrid:
		// hybridSearch already over-fetches by k*5 internally for its
		// rerank pipeline; tombstones are filtered there.
		ranked := hybridSearch(query, ix.model.Encode(query), ix.flat, ix.bm, ix.chunks, k, -1)
		out := make([]Result, 0, k)
		for _, r := range ranked {
			c := ix.chunks[r.idx]
			if c.Tombstoned {
				continue
			}
			out = append(out, Result{Chunk: c, Score: r.score})
			if len(out) >= k {
				break
			}
		}
		return out
	default: // ModeBM25 — raw lexical (Stage 1 behavior, no rerank)
		hits := ix.bm.TopK(bm25.Tokenize(query), overFetch)
		out := make([]Result, 0, k)
		for _, h := range hits {
			c := ix.chunks[h.Doc]
			if c.Tombstoned {
				continue
			}
			out = append(out, Result{Chunk: c, Score: h.Score})
			if len(out) >= k {
				break
			}
		}
		return out
	}
}

// tombstoneCount returns how many entries in ix.chunks have
// Tombstoned=true. O(N) but read-only and called only from over-fetch
// math at query entry. Could be cached at snapshot-build time if it
// shows up in a profile.
func (ix *Index) tombstoneCount() int {
	n := 0
	for i := range ix.chunks {
		if ix.chunks[i].Tombstoned {
			n++
		}
	}
	return n
}
