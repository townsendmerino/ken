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
	"github.com/townsendmerino/ken/internal/structural"
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
	//
	// Text is exactly the file's lines StartLine..EndLine as they were when
	// the file was last indexed. The Arm B enrichment label the indexer
	// prepends for BM25 and the embedding (ADR-035) is stripped before the
	// Hit is built, as ken-mcp strips it at its own boundary; the stored
	// chunk keeps its label, so ranking is unaffected. The one exception is
	// a synthetic chunk with no single source range — a Tier-1 folded SQL
	// migration chunk, or a chunk injected through SetExtraChunks — whose
	// Text is whatever that producer wrote.
	File      string
	StartLine int
	EndLine   int
	Text      string

	// Label is the Arm B enrichment line Text omits ("" when the chunk has
	// none), including its trailing newline, so Label+Text is the chunk
	// exactly as the index stores and ranks it. Best-effort, outside the
	// stability promise: the ken CLI prints the stored chunk and needs it.
	Label string

	// Hash is sha256(Text), hex-encoded — a content fingerprint stable
	// across reindexes as long as the hit's own lines don't change, useful
	// for a caller's own dedup/cache keys. Because Text carries no
	// enrichment label, an edit elsewhere in the file (a new call in
	// another function, which changes the file-level label) leaves it
	// unchanged.
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

	// HeadingPath is the chain of markdown headings enclosing this hit,
	// outermost first (e.g. ["Testing", "A skip is not a pass"]) — the
	// HeadingPath of the Section (see Sections) that contains StartLine.
	// Empty for a non-markdown file, or if the file's sections can't be
	// read (best effort; never fails the search).
	HeadingPath []string
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

// hitText returns a result chunk's text as a Hit carries it — the source
// lines only, without the Arm B enrichment label (structural.StripLabel,
// the helper ken-mcp's result boundary uses) — and the hash of that text.
// The one place toHits and FindRelated take Text and Hash from, so the two
// can't disagree.
func hitText(r search.Result) (text, hash string) {
	text = structural.StripLabel(r.Chunk.Text)
	return text, hashText(text)
}

// hitLabel is the enrichment line hitText stripped: StripLabel removes a
// prefix, so what it removed is the head of the stored text.
func hitLabel(r search.Result, text string) string {
	return r.Chunk.Text[:len(r.Chunk.Text)-len(text)]
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
func (w *Workspace) toHits(ix *search.Index, results []search.Result, effMode search.Mode, gen uint64, query string, k int, noArmRanks bool) []Hit {
	var bm25Rank, denseRank map[chunkKey]int
	if noArmRanks {
		effMode = -1 // matches no case: leave both rank maps nil
	}
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

	sectionCache := make(map[string][]Section)
	hits := make([]Hit, len(results))
	for i, r := range results {
		key := keyOf(r)
		text, hash := hitText(r)
		hits[i] = Hit{
			File:        r.Chunk.File,
			StartLine:   r.Chunk.StartLine,
			EndLine:     r.Chunk.EndLine,
			Text:        text,
			Label:       hitLabel(r, text),
			Hash:        hash,
			Score:       r.Score,
			Rank:        i + 1,
			Gen:         gen,
			BM25Rank:    bm25Rank[key],
			DenseRank:   denseRank[key],
			HeadingPath: w.headingPathFor(r.Chunk.File, r.Chunk.StartLine, sectionCache),
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
	return w.SearchWith(ctx, query, k, mode, SearchOptions{})
}

// SearchOptions adjusts one search call. The zero value is SearchMode's
// behavior.
type SearchOptions struct {
	// BudgetPacking is for callers that fill a token budget from the
	// ranked list (an agent's context fill) rather than show a ranked
	// list. It softens the rerank pipeline's file-saturation decay: a
	// file's second chunk scores 0.8x instead of 0.5x, its third 0.64x
	// instead of 0.25x, so a budget takes more of the files that matter
	// while still spreading across files. On the held-out half of
	// ContextBench Lite at 8,192 tokens it raised line coverage by +0.026
	// for −0.012 file coverage (ADR-052, docs/BENCH.md "Gentler-decay gate
	// results"). Affects the hybrid modes only (BM25 and semantic have no
	// rerank). Leave it off for ranked search, where spreading results
	// across files is the point.
	BudgetPacking bool

	// Telemetry, when non-nil, receives this call's per-stage timings.
	// Not filled when BudgetPacking is set. Best-effort, outside the
	// stability promise.
	Telemetry *Telemetry

	// NoArmRanks skips the auxiliary single-arm queries that fill
	// Hit.BM25Rank and Hit.DenseRank in the hybrid modes, leaving both 0.
	// The ranking, scores and every other Hit field are unchanged; the
	// call is cheaper. Best-effort, outside the stability promise.
	NoArmRanks bool
}

// Telemetry is one query's per-stage timing breakdown (see
// SearchOptions.Telemetry). Alias for search.Telemetry; best-effort, outside
// the stability promise: its fields follow the pipeline's stages.
type Telemetry = search.Telemetry

// ParseMode parses a mode name ("bm25", "semantic", "hybrid",
// "hybrid-rerank"), the strings Options.Mode accepts.
func ParseMode(s string) (Mode, error) { return search.ParseMode(s) }

// budgetPackingSaturationDecay is the file-saturation decay BudgetPacking
// uses (shipped ranking: 0.5). ADR-052, superseding ADR-051's threshold 2.
const budgetPackingSaturationDecay = 0.8

// SearchWith is SearchMode with per-call options; SearchWith with a zero
// SearchOptions is exactly SearchMode.
func (w *Workspace) SearchWith(ctx context.Context, query string, k int, mode Mode, opts SearchOptions) ([]Hit, Mode, error) {
	if err := ctx.Err(); err != nil {
		return nil, mode, fmt.Errorf("engine: search: %w", err)
	}
	ix := w.index.Load()
	gen := w.index.Generation()
	results, eff := searchResults(ix, query, k, mode, opts)
	return w.toHits(ix, results, eff, gen, query, k, opts.NoArmRanks), eff, nil
}

// searchResults runs one query against one snapshot, as opts asks.
func searchResults(ix *search.Index, query string, k int, mode Mode, opts SearchOptions) (results []search.Result, eff Mode) {
	switch {
	case opts.BudgetPacking:
		results, eff = ix.SearchModeAblated(query, k, mode, search.AdaptiveAlphas, search.RankAblation{SaturationDecay: budgetPackingSaturationDecay})
	case opts.Telemetry != nil:
		results, eff, *opts.Telemetry = ix.SearchModeWithTelemetry(query, k, mode)
	default:
		results, eff = ix.SearchMode(query, k, mode)
	}
	return results, eff
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
	sectionCache := make(map[string][]Section)
	hits := make([]Hit, len(results))
	for i, r := range results {
		text, hash := hitText(r)
		hits[i] = Hit{
			File:        r.Chunk.File,
			StartLine:   r.Chunk.StartLine,
			EndLine:     r.Chunk.EndLine,
			Text:        text,
			Label:       hitLabel(r, text),
			Hash:        hash,
			Score:       r.Score,
			Rank:        i + 1,
			Gen:         gen,
			DenseRank:   i + 1,
			HeadingPath: w.headingPathFor(r.Chunk.File, r.Chunk.StartLine, sectionCache),
		}
	}
	return hits, nil
}

// SearchFiles is SearchMode restricted to hits whose File is in files —
// "which sections of CLAUDE.md match this message" without pulling in
// hits from the rest of the repo. It is exact at any index size: the hits
// are the ones the whole ranking (SearchMode with k past the index size)
// has in files, with the same scores, in the same order, cut to k. It still
// scores every chunk (the global statistics and arm ranks need them) but
// orders and decorates only the requested files' chunks where the mode
// allows: bm25 skips the whole-index selection, hybrid skips the query
// boosts and final sort for the rest of the index, and semantic and
// hybrid-rerank cost about what the whole ranking costs. See
// search.(*Index).SearchFilesMode for the per-mode detail.
//
// Each Hit's BM25Rank and DenseRank are its exact rank in that arm's
// whole-index ranking (0 if it isn't in the arm), not limited to the
// armOverfetch depth SearchMode's best-effort lookups use.
//
// files must be non-empty; paths not present in the index are silently
// ignored rather than erroring (a typo'd path just yields no hits from it,
// matching SearchMode's own graceful-degradation style).
func (w *Workspace) SearchFiles(ctx context.Context, query string, k int, mode Mode, files []string) ([]Hit, Mode, error) {
	if err := ctx.Err(); err != nil {
		return nil, mode, fmt.Errorf("engine: search: %w", err)
	}
	if len(files) == 0 || k <= 0 {
		return nil, mode, nil
	}
	want := make(map[string]bool, len(files))
	for _, f := range files {
		want[f] = true
	}

	ix := w.index.Load()
	gen := w.index.Generation()

	results, ranks, eff := ix.SearchFilesMode(query, k, mode, func(f string) bool { return want[f] })
	sectionCache := make(map[string][]Section)
	hits := make([]Hit, len(results))
	for i, r := range results {
		text, hash := hitText(r)
		hits[i] = Hit{
			File:        r.Chunk.File,
			StartLine:   r.Chunk.StartLine,
			EndLine:     r.Chunk.EndLine,
			Text:        text,
			Label:       hitLabel(r, text),
			Hash:        hash,
			Score:       r.Score,
			Rank:        i + 1,
			Gen:         gen,
			BM25Rank:    ranks[i].BM25,
			DenseRank:   ranks[i].Dense,
			HeadingPath: w.headingPathFor(r.Chunk.File, r.Chunk.StartLine, sectionCache),
		}
	}
	return hits, eff, nil
}
