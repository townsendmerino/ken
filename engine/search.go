package engine

// search.go — the retrieval surface: Search, SearchMode, FindRelated, and
// the Hit type they return.
//
// No ranking math lives here. Every Hit's Score/ordering comes straight
// from search.(*Index).SearchMode / FindRelated, unchanged — this file
// only adds presentation metadata (Hash, Rank, Gen) and, for the hybrid
// modes, best-effort per-arm rank lookups computed via two EXTRA read-only
// queries against the already-loaded immutable *search.Index snapshot
// (one BM25-only, one semantic-only). That keeps recall/NDCG byte-for-byte
// identical to calling internal/search directly — see docs/BENCH.md's
// recall-decomposition re-run cited in the ADR-046 commit.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/townsendmerino/ken/internal/search"
)

// Mode selects the retrieval pipeline. 1.0-stable alias for search.Mode.
type Mode = search.Mode

// Mode values. See search.Mode's doc for what each pipeline stage does.
const (
	// ModeBM25 runs only the lexical retriever.
	ModeBM25 = search.ModeBM25
	// ModeSemantic runs only the dense (Model2Vec cosine) retriever.
	ModeSemantic = search.ModeSemantic
	// ModeHybrid fuses BM25 and dense via weighted RRF, then applies the
	// boost/penalty pipeline. The default when Options.Mode is "".
	ModeHybrid = search.ModeHybrid
	// ModeHybridRerank is ModeHybrid plus a second-stage neural reranker.
	// Falls back to ModeHybrid if the index has no reranker attached.
	ModeHybridRerank = search.ModeHybridRerank
)

// Hit is one ranked chunk returned by Search, SearchMode, or FindRelated.
type Hit struct {
	// File, StartLine, EndLine, Text locate and contain the hit; lines are
	// 1-based inclusive, matching aikit's chunk.Chunk.
	File      string
	StartLine int
	EndLine   int
	Text      string

	// Hash is sha256(Text), hex-encoded — a content fingerprint stable
	// across reindexes as long as the chunk's text doesn't change, useful
	// for a caller's own dedup/cache keys.
	Hash string

	// Score is the pipeline's fused (or single-arm) relevance score, on
	// whatever scale that pipeline stage produces — not comparable across
	// different Mode values.
	Score float64

	// Rank is this Hit's 1-based position in the slice it came from.
	Rank int

	// Gen is the index generation (search.WatchedIndex.Generation) this
	// Hit was served from — a monotonically increasing counter bumped on
	// every republish. Lets a caller detect "the index changed under me
	// between two calls" without comparing full results.
	Gen uint64

	// BM25Rank is this chunk's 1-based rank within the BM25-only arm's
	// ranking for the same query, or 0 if it wasn't found there (outside
	// the arm's candidate pool, or the arm wasn't run for this Mode).
	BM25Rank int

	// DenseRank is this chunk's 1-based rank within the dense-only arm's
	// ranking for the same query (or, for FindRelated, simply equal to
	// Rank — FindRelated IS a dense-only query), or 0 if not applicable.
	DenseRank int
}

// chunkKey identifies a chunk by its span, stable enough to cross-reference
// the same chunk across two independent single-arm queries against the
// same immutable *search.Index snapshot.
type chunkKey struct {
	file      string
	startLine int
	endLine   int
}

func keyOf(r search.Result) chunkKey {
	return chunkKey{r.Chunk.File, r.Chunk.StartLine, r.Chunk.EndLine}
}

// rankMap builds a 1-based chunkKey→rank lookup from an already-ranked
// result slice.
func rankMap(results []search.Result) map[chunkKey]int {
	m := make(map[chunkKey]int, len(results))
	for i, r := range results {
		m[keyOf(r)] = i + 1
	}
	return m
}

func hashText(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// armOverfetch sizes the auxiliary single-arm queries toHits runs to
// recover per-arm ranks for a hybrid result set — generous enough that a
// chunk ranked respectably in its arm is actually found, cheap enough that
// it's not another full rerank pass (these never trigger the neural
// reranker, just raw BM25.TopK / flat.Query).
func armOverfetch(k int) int {
	if n := k * 5; n > 50 {
		return n
	}
	return 50
}

// toHits converts a ranked result slice into Hits, filling BM25Rank/
// DenseRank with best-effort per-arm ranks for the hybrid modes. ix is the
// exact snapshot results was computed against (the caller's single Load()
// for the whole call), so the auxiliary queries see the same data.
func (w *Workspace) toHits(ix *search.Index, results []search.Result, effMode search.Mode, gen uint64, query string, k int) []Hit {
	var bm25Rank, denseRank map[chunkKey]int
	switch effMode {
	case search.ModeBM25:
		bm25Rank = rankMap(results)
	case search.ModeSemantic:
		denseRank = rankMap(results)
	case search.ModeHybrid, search.ModeHybridRerank:
		n := armOverfetch(k)
		if bmRes, _ := ix.SearchMode(query, n, search.ModeBM25); len(bmRes) > 0 {
			bm25Rank = rankMap(bmRes)
		}
		if ix.Model() != nil {
			if semRes, _ := ix.SearchMode(query, n, search.ModeSemantic); len(semRes) > 0 {
				denseRank = rankMap(semRes)
			}
		}
	}

	hits := make([]Hit, len(results))
	for i, r := range results {
		key := keyOf(r)
		hits[i] = Hit{
			File:      r.Chunk.File,
			StartLine: r.Chunk.StartLine,
			EndLine:   r.Chunk.EndLine,
			Text:      r.Chunk.Text,
			Hash:      hashText(r.Chunk.Text),
			Score:     r.Score,
			Rank:      i + 1,
			Gen:       gen,
			BM25Rank:  bm25Rank[key],
			DenseRank: denseRank[key],
		}
	}
	return hits
}

// Search returns the top-k hits for query under the Workspace's configured
// Mode (Options.Mode at Open). Thin wrapper around SearchMode for callers
// that don't need a per-call override.
func (w *Workspace) Search(ctx context.Context, query string, k int) ([]Hit, error) {
	hits, _, err := w.SearchMode(ctx, query, k, w.mode)
	return hits, err
}

// SearchMode runs Search with an explicit per-call Mode override, returning
// the hits plus the mode actually used — which may differ from requested
// if the index lacks the required retriever (e.g. requesting ModeSemantic
// against a BM25-only index downgrades to ModeBM25 rather than erroring;
// see search.(*Index).SearchMode).
func (w *Workspace) SearchMode(ctx context.Context, query string, k int, mode Mode) ([]Hit, Mode, error) {
	if err := ctx.Err(); err != nil {
		return nil, mode, fmt.Errorf("engine: search: %w", err)
	}
	ix := w.index.Load()
	gen := w.index.Generation()
	results, eff := ix.SearchMode(query, k, mode)
	return w.toHits(ix, results, eff, gen, query, k), eff, nil
}

// FindRelated returns chunks semantically similar to the chunk containing
// (filePath, line), dropping the source chunk itself. Requires a
// semantic/hybrid-capable Workspace; returns an error otherwise (see
// search.(*Index).FindRelated).
func (w *Workspace) FindRelated(ctx context.Context, filePath string, line, k int) ([]Hit, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("engine: find_related: %w", err)
	}
	ix := w.index.Load()
	gen := w.index.Generation()
	results, err := ix.FindRelated(filePath, line, k)
	if err != nil {
		return nil, fmt.Errorf("engine: find_related: %w", err)
	}
	hits := make([]Hit, len(results))
	for i, r := range results {
		hits[i] = Hit{
			File:      r.Chunk.File,
			StartLine: r.Chunk.StartLine,
			EndLine:   r.Chunk.EndLine,
			Text:      r.Chunk.Text,
			Hash:      hashText(r.Chunk.Text),
			Score:     r.Score,
			Rank:      i + 1,
			Gen:       gen,
			DenseRank: i + 1,
		}
	}
	return hits, nil
}
