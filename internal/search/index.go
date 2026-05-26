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
	"io"
	"io/fs"
	"os"
	"path"
	"sort"

	"github.com/townsendmerino/ken/internal/ann"
	"github.com/townsendmerino/ken/internal/bm25"
	"github.com/townsendmerino/ken/internal/chunk"
	_ "github.com/townsendmerino/ken/internal/chunk/regex" // registers the default "regex" chunker
	// NOTE: treesitter and markdown are NOT blank-imported here. Binaries
	// that want them must blank-import them explicitly — e.g. cmd/ken-mcp
	// and cmd/ken-mcp-docs do, but the embedded-corpus demo binary
	// (cmd/ken-mcp-docs) deliberately skips treesitter to keep its
	// gotreesitter/grammars 19MB blob bundle out of the binary. The
	// chunker registry is the seam: side-effect imports happen at the
	// binary's main package, not in this shared library layer.
	"github.com/townsendmerino/ken/internal/embed"
	"github.com/townsendmerino/ken/internal/repo"
	"github.com/townsendmerino/ken/internal/sql"
)

// FSOptions configures the FromFSWithOptions / NewWatchedIndexWithOptions
// entry points added in v0.7.1. The zero value is the stock behavior for
// every existing caller: migration folding ENABLED, log discarded.
// Existing wrappers (FromFS, FromPath, FromFSWithModel, NewWatchedIndex)
// pass the zero value, so v0.7.0 callers get folding transparently.
type FSOptions struct {
	// DisableFoldMigrations turns off v0.7.1 Tier-1 migration-history
	// folding (sql.FoldMigrations). When true, .sql files in directories
	// matching a recognized migration naming pattern are chunked the
	// v0.7.0 way (one per-file ALTER chunk per statement) instead of
	// folded into one chunk per table.
	//
	// Inverted name so the zero value is "folding enabled" — matches the
	// semantic default the prompt requires.
	DisableFoldMigrations bool

	// LogWriter receives the per-statement skip warnings from
	// sql.FoldMigrations. nil discards. Wired by cmd/ken-mcp from its
	// leveled logger's stderr writer.
	LogWriter io.Writer
}

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

	// vecs is the per-chunk embedding slice BuildIndex received,
	// retained so WithExtraChunks can rebuild a new Index over
	// (chunks ∪ extras) without re-encoding the original corpus.
	// nil for ModeBM25 (no embeddings) or when the caller passed nil
	// vecs at build time. Same length as chunks when non-nil.
	//
	// v0.8.0 Part 3 addendum (ADR-020): retained for the WithExtraChunks
	// rebuild path that powers mcp.Run's Tier-2 chunk integration.
	// Unused by cmd/ken-mcp (which uses WatchedIndex.SetExtraChunks via
	// the cache pre-warm path, where the WatchedIndex holds its own
	// vecs slice).
	vecs [][]float32
}

// FromFS walks fsys, chunks every indexable file with the named chunker,
// builds the BM25 index, and (for semantic/hybrid) embeds every chunk
// with the Model2Vec model at modelDir.
//
// This is the canonical entry point as of v0.5.0. Pass any fs.FS —
// os.DirFS for a real directory, embed.FS for a baked-in corpus,
// fstest.MapFS for tests, or any other implementation. The deprecated
// FromPath wraps FromFS(os.DirFS(root), ...) for callers still using a
// concrete path.
//
// As of v0.7.1 FromFS enables Tier-1 migration-history folding by
// default. Operators who want the v0.7.0 per-file behavior should call
// FromFSWithOptions with FSOptions.DisableFoldMigrations = true.
//
// Implementation note (v0.3): FromFS is a thin wrapper around
// walkAndChunkFS + BuildIndex. The split exists because internal/search/
// watch.go reuses BuildIndex to publish new snapshots after incremental
// re-chunk / re-embed work — it shouldn't re-walk the tree just to
// rebuild the index struct.
func FromFS(fsys fs.FS, mode Mode, chunkerName, modelDir string) (*Index, error) {
	return FromFSWithOptions(fsys, mode, chunkerName, modelDir, FSOptions{})
}

// FromFSWithOptions is FromFS plus the v0.7.1 FSOptions knob — currently
// only the migration-folding opt-out. The zero value of FSOptions
// matches the FromFS default exactly, so callers that don't care can
// keep using FromFS.
func FromFSWithOptions(fsys fs.FS, mode Mode, chunkerName, modelDir string, opts FSOptions) (*Index, error) {
	chunks, vecs, model, _, err := walkAndChunkFS(fsys, mode, chunkerName, modelDir, opts)
	if err != nil {
		return nil, err
	}
	return BuildIndex(chunks, vecs, mode, model), nil
}

// FromPath is the real-filesystem entry point retained for backward
// compatibility with pre-v0.5.0 callers.
//
// Deprecated: use FromFS(os.DirFS(root), mode, chunkerName, modelDir) instead.
func FromPath(root string, mode Mode, chunkerName, modelDir string) (*Index, error) {
	return FromFS(os.DirFS(root), mode, chunkerName, modelDir)
}

// walkAndChunk is the real-FS-only bootstrap path retained for
// internal/search/watch.go (fsnotify is real-FS-only by construction).
// New code should call walkAndChunkFS directly. The migDirs return is
// the set of directories the migration-folding pass treated as a
// migration chain — WatchedIndex carries this forward so fsnotify-driven
// flushes know which dirs to re-fold.
func walkAndChunk(root string, mode Mode, chunkerName, modelDir string, opts FSOptions) (
	chunks []chunk.Chunk, vecs [][]float32, model *embed.StaticModel, migDirs map[string]bool, err error,
) {
	return walkAndChunkFS(os.DirFS(root), mode, chunkerName, modelDir, opts)
}

// walkAndChunkFS resolves modelDir to an *embed.StaticModel (when the mode
// needs one) and delegates the actual walk+chunk+embed pass to
// walkAndChunkFSWithModel. Kept for callers that resolve the model path
// at index-build time (the in-tree path-based entry points and the
// watcher).
func walkAndChunkFS(fsys fs.FS, mode Mode, chunkerName, modelDir string, opts FSOptions) (
	chunks []chunk.Chunk, vecs [][]float32, model *embed.StaticModel, migDirs map[string]bool, err error,
) {
	if mode.needsModel() {
		if modelDir == "" {
			return nil, nil, nil, nil, fmt.Errorf("search: mode requires an embedding model — pass --model <dir>, run `ken download-model`, or use --mode=bm25")
		}
		m, err := embed.LoadFromFS(os.DirFS(modelDir), ".")
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("search: model not found at %s: %w — run `ken download-model --to %s` to fetch it, or use --mode=bm25", modelDir, err, modelDir)
		}
		model = m
	}
	chunks, vecs, returnedModel, migDirs, err := walkAndChunkFSWithModel(fsys, mode, chunkerName, model, opts)
	return chunks, vecs, returnedModel, migDirs, err
}

// walkAndChunkFSWithModel does the actual corpus-bootstrapping work:
// validate the mode + chunker, walk fsys, chunk every file, embed every
// chunk under semantic/hybrid using the caller-supplied model. The model
// arg may be nil iff mode is ModeBM25. Returns the raw materials
// BuildIndex needs (chunks slice, parallel vecs slice, and the model
// passed through unchanged for the watcher's incremental-embed loop)
// plus the migration-directory set the folding pass discovered (used by
// the WatchedIndex to re-fold on file change).
//
// This is the shared backbone between walkAndChunkFS (model loaded from
// a directory path) and FromFSWithModel (model supplied directly by the
// caller — the mcp.Run embedded-corpus path where the model comes from a
// caller's //go:embed fs.FS).
func walkAndChunkFSWithModel(fsys fs.FS, mode Mode, chunkerName string, model *embed.StaticModel, opts FSOptions) (
	chunks []chunk.Chunk, vecs [][]float32, returnedModel *embed.StaticModel, migDirs map[string]bool, err error,
) {
	if mode != ModeBM25 && mode != ModeSemantic && mode != ModeHybrid {
		return nil, nil, nil, nil, fmt.Errorf("search: unknown mode %d", mode)
	}
	if _, err := chunk.Get(chunkerName); err != nil {
		return nil, nil, nil, nil, err
	}
	if mode.needsModel() && model == nil {
		return nil, nil, nil, nil, fmt.Errorf("search: mode requires an embedding model but model is nil")
	}

	files, err := repo.WalkFS(fsys, repo.Options{})
	if err != nil {
		return nil, nil, nil, nil, err
	}

	// Pre-classify migration dirs so the per-file path can skip SQL
	// structural chunking for files in those dirs (we re-fold them
	// holistically in the post-walk pass).
	migDirs = map[string]bool{}
	if !opts.DisableFoldMigrations {
		seen := map[string]bool{}
		for _, rel := range files {
			if !sql.IsSQLFile(rel) {
				continue
			}
			d := path.Dir(rel)
			if seen[d] {
				continue
			}
			seen[d] = true
			if sql.IsMigrationDir(fsys, d) {
				migDirs[d] = true
			}
		}
	}

	for _, rel := range files {
		data, err := fs.ReadFile(fsys, rel)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		skipSQLStructural := migDirs[path.Dir(rel)]
		cs, err := chunkOneFile(chunkerName, rel, data, skipSQLStructural)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		for _, c := range cs {
			chunks = append(chunks, c)
			if model != nil {
				vecs = append(vecs, model.Encode(c.Text))
			}
		}
	}

	// Migration-folding pass: deterministic order over the discovered
	// dirs so the produced chunk order is stable across runs.
	if len(migDirs) > 0 {
		dirs := make([]string, 0, len(migDirs))
		for d := range migDirs {
			dirs = append(dirs, d)
		}
		sort.Strings(dirs)
		for _, d := range dirs {
			folded, ferr := sql.FoldMigrations(fsys, d, opts.LogWriter)
			if ferr != nil {
				// Don't fail the build — log via the writer if available
				// and fall back to the per-file behavior (already emitted
				// since skipSQLStructural was set for these files).
				if opts.LogWriter != nil {
					fmt.Fprintf(opts.LogWriter, "search: FoldMigrations(%q): %v\n", d, ferr)
				}
				continue
			}
			for _, c := range folded {
				chunks = append(chunks, c)
				if model != nil {
					vecs = append(vecs, model.Encode(c.Text))
				}
			}
		}
	}
	return chunks, vecs, model, migDirs, nil
}

// chunkOneFile is the single point both the build-once path
// (walkAndChunkFSWithModel) and the watch path (WatchedIndex.appendFile)
// route file-bytes through, so .sql files get both the regular chunker
// output AND v0.7.0 Tier 1's structural per-table chunks (ADR-017).
//
// Decision: the structural chunks are ADDITIVE — the .sql file is still
// routed through whatever chunker is configured (line for .sql in the
// stock binary), so BM25 still surfaces the original byte slice for
// raw-text queries; the structural chunks are extra retrieval units
// agents can hit when their query matches a column name + type +
// constraint shape. SQL-parser warnings (skipped malformed statements)
// are currently discarded — the BM25 path catches them via the original
// file text. If operators ask to surface them, route a logger here.
//
// v0.7.1: skipSQLStructural=true is passed by the orchestrator when the
// file lives in a migration directory; the structural chunks are produced
// once for the whole directory by sql.FoldMigrations rather than per-file,
// avoiding redundant N+1 chunks (CREATE + many ALTERs).
func chunkOneFile(chunkerName, rel string, data []byte, skipSQLStructural bool) ([]chunk.Chunk, error) {
	cs, err := chunk.ChunkFile(chunkerName, rel, data, chunk.DefaultChunkSize)
	if err != nil {
		return nil, err
	}
	if !skipSQLStructural && sql.IsSQLFile(rel) {
		extras, perr := sql.ParseFile(rel, data, nil) // nil logger → discard
		if perr == nil {
			cs = append(cs, extras...)
		}
		// Parse errors from ParseFile are file-level (already best-effort
		// at statement level). Silently drop — the BM25 index of the raw
		// file text still surfaces the content; we just don't get the
		// structural per-object chunks for this file.
	}
	return cs, nil
}

// FromFSWithModel is FromFS with the model supplied directly rather than
// loaded from a directory path. Same return shape; chunkerName is one of
// the registered chunker names (see internal/chunk.Names). model may be
// nil iff mode == ModeBM25.
//
// This is the entry point for callers that bake the model into their
// binary via //go:embed and load it via embed.LoadFromFS — typically the
// mcp.Run library API serving an embedded-corpus MCP server.
//
// v0.7.1: migration folding is enabled by default. mcp.Run callers whose
// embedded corpora include numbered .sql files in the same directory get
// folded chunks automatically — no API change required.
func FromFSWithModel(fsys fs.FS, mode Mode, chunkerName string, model *embed.StaticModel) (*Index, error) {
	chunks, vecs, m, _, err := walkAndChunkFSWithModel(fsys, mode, chunkerName, model, FSOptions{})
	if err != nil {
		return nil, err
	}
	return BuildIndex(chunks, vecs, mode, m), nil
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
	ix := &Index{mode: mode, chunks: chunks, bm: bm25.Build(docs), model: model, vecs: vecs}
	if model != nil {
		ix.flat = ann.New(vecs)
	}
	return ix
}

// WithExtraChunks returns a new *Index containing the receiver's chunks
// UNION the provided extras. The receiver is unchanged (immutable
// pre-existing snapshot, important for callers that may hold a
// reference); the returned Index is freshly built over the merged set.
//
// Used by v0.8.0 Part 3 addendum (ADR-020) for mcp.Run's Tier-2 chunk
// integration: when mcp/db.Refresher's Start callback fires with new
// DB chunks, mcp.Run calls ix.WithExtraChunks(extras) and atomic-
// stores the result for subsequent search handlers to read.
//
// Semantic / hybrid mode: extras are encoded via the retained model
// reference (model is held on *Index since build time). BM25 index
// is rebuilt over the merged docs so token-frequency stats reflect
// the combined corpus.
//
// BM25 mode (no model): BM25 index is rebuilt over (chunks ∪ extras)
// tokens; no embedding work happens. The flat ANN index stays nil.
//
// extras==nil or len(extras)==0 returns a freshly-built Index
// equivalent to the receiver (no-op semantically, but a new pointer).
// The "replace, not accumulate" rule applies: each call rebuilds
// against the receiver's original chunks plus the SUPPLIED extras —
// previous extras (from a prior WithExtraChunks call) are not retained.
//
// Goroutine-safety: callers may invoke WithExtraChunks on the same
// receiver concurrently (the receiver is immutable). The atomic-swap
// of the resulting pointer is the caller's responsibility (mcp.Run
// uses atomic.Pointer[Index] for this).
func (ix *Index) WithExtraChunks(extras []chunk.Chunk) *Index {
	if len(extras) == 0 {
		// No-op: rebuild from the receiver's state. Returns a fresh
		// pointer (not ix itself) so callers can always treat the
		// return value as a new snapshot to atomic-store.
		return BuildIndex(ix.chunks, ix.vecs, ix.mode, ix.model)
	}

	merged := make([]chunk.Chunk, 0, len(ix.chunks)+len(extras))
	merged = append(merged, ix.chunks...)
	merged = append(merged, extras...)

	var mergedVecs [][]float32
	if ix.model != nil {
		// Semantic / hybrid: encode extras via the retained model.
		// ix.vecs may be nil (BM25 mode promoted to semantic via a
		// build path that omitted vecs); guard for that.
		extraVecs := make([][]float32, len(extras))
		for i, c := range extras {
			extraVecs[i] = ix.model.Encode(c.Text)
		}
		mergedVecs = make([][]float32, 0, len(ix.vecs)+len(extras))
		mergedVecs = append(mergedVecs, ix.vecs...)
		mergedVecs = append(mergedVecs, extraVecs...)
	}
	// ModeBM25 path: mergedVecs stays nil; BuildIndex builds BM25 only.

	return BuildIndex(merged, mergedVecs, ix.mode, ix.model)
}

// Len is the number of indexed chunks.
func (ix *Index) Len() int { return len(ix.chunks) }

// Chunks returns a read-only view of the master chunk slice (used by the
// MCP find_related handler to resolve a file:line into an indexed chunk).
func (ix *Index) Chunks() []chunk.Chunk { return ix.chunks }

// Mode returns the build-time mode of the index. The MCP search tool
// uses this to compute the "natural default" mode for a request that
// doesn't supply args.Mode — the index's own mode, not a fixed
// "hybrid" assumption.
func (ix *Index) Mode() Mode { return ix.mode }

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

// Search returns the top-k chunks for query under the index's
// build-time mode. Thin wrapper around SearchMode for callers that
// don't need per-call mode overrides (the historical signature, kept
// for backward compatibility).
func (ix *Index) Search(query string, k int) []Result {
	results, _ := ix.SearchMode(query, k, ix.mode)
	return results
}

// SearchMode runs Search with the supplied mode override. Returns the
// results plus the mode actually used — which may differ from the
// requested mode if the index lacks the required retriever (e.g.
// requesting ModeSemantic against a BM25-only index silently
// downgrades to ModeBM25 rather than panicking on nil flat/model).
// The "transparent downgrade" semantics match the build-time pattern
// in mcp.Run, where a missing model downgrades hybrid→bm25 instead of
// erroring.
//
// This is a ken-side extension; the upstream semble MCP server has no
// per-call mode arg. ken's MCP `search` tool routes args.Mode through
// here so an agent can experiment with bm25-vs-hybrid retrieval on a
// single long-lived index without rebuilding.
//
// Tombstoned chunks are filtered after the underlying retriever
// returns; over-fetch by the tombstone count so the filtered result
// still hits k on indices with edit churn.
func (ix *Index) SearchMode(query string, k int, mode Mode) ([]Result, Mode) {
	if mode != ModeBM25 && ix.flat == nil {
		// Capability downgrade: requested semantic/hybrid but no
		// flat/model. Fall back to BM25 so the caller gets results
		// rather than a panic.
		mode = ModeBM25
	}
	overFetch := k + ix.tombstoneCount()
	switch mode {
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
		return out, mode
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
		return out, mode
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
		return out, mode
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
