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
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/townsendmerino/aikit/ann"
	"github.com/townsendmerino/aikit/bm25"
	"github.com/townsendmerino/aikit/chunk"
	_ "github.com/townsendmerino/aikit/chunk/regex" // registers the default "regex" chunker
	// NOTE: treesitter and markdown are NOT blank-imported here. Binaries
	// that want them must blank-import them explicitly — e.g. cmd/ken-mcp
	// and cmd/ken-mcp-docs do, but the embedded-corpus demo binary
	// (cmd/ken-mcp-docs) deliberately skips treesitter because
	// importing it inflates the linked binary by ~26 MB
	// (darwin/arm64; the gotreesitter/grammars embed.FS payload is
	// ~19 MB on-disk for 206 grammar blobs, plus parser runtime).
	// Per ADR-023 the bundle is monolithic at the embed layer so
	// per-language gating doesn't shrink it. The chunker registry is
	// the seam: side-effect imports happen at the binary's main
	// package, not in this shared library layer.
	"github.com/townsendmerino/aikit/embed"
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
	// ModeHybridRerank is ModeHybrid + a second-stage neural reranker
	// (M4; see outputs/m3-results.md and m4-results.md). Requires both
	// an embedding model (for stage-1 hybrid) AND a reranker injected
	// via Index.SetReranker; SearchMode downgrades transparently to
	// ModeHybrid when the reranker is absent, mirroring the existing
	// "missing model ⇒ downgrade to bm25" pattern.
	ModeHybridRerank
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
	case "hybrid-rerank":
		return ModeHybridRerank, nil
	}
	return 0, fmt.Errorf("search: unknown mode %q (want %v)", s, ModeNames())
}

// ModeNames returns the CLI strings accepted by ParseMode, in CLI-flag
// order. Callers building allowed-value lists for env-var / flag
// validation should use this rather than hardcoding.
func ModeNames() []string {
	return []string{"bm25", "semantic", "hybrid", "hybrid-rerank"}
}

func (m Mode) needsModel() bool {
	return m == ModeSemantic || m == ModeHybrid || m == ModeHybridRerank
}

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

	// M4 neural reranker (optional; nil = ModeHybridRerank downgrades
	// to ModeHybrid). Set via SetReranker after FromFS so the heavy
	// reranker dep (aikit/encoder) doesn't leak into every Index
	// build path. Reranker implementations are goroutine-safe; the
	// rerankCfg defaults to (rerankN=50, β=0.25) per M0 amendments.
	reranker  Reranker
	rerankCfg rerankerConfig
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
// walkAndChunkFSWithModel walks the corpus, chunks each file, and (for
// hybrid/semantic modes) embeds each chunk via model.Encode. Per-file
// work is fanned out to runtime.NumCPU() workers via a bounded channel;
// results are collected by file index so the resulting chunks/vecs
// slices are byte-identical to a serial build. Migration folding runs
// serially after the parallel pass to preserve the v0.7.1 deterministic
// order over discovered migration directories.
//
// Determinism is load-bearing for two reasons:
//   - NDCG@10 reproducibility (docs/BENCH.md's parity contract)
//   - The pre-built-index format (ADR-024 / serializeIndex) requires
//     byte-stable serialization for embedded-corpus mcp.Run binaries.
//
// Both reasons are satisfied by reassembling per-file results in file
// index order (the walk's lexical order, deterministic by construction
// per repo.WalkFS) and running bm25.Build / serializeIndex serially over
// the ordered chunks slice in BuildIndex's downstream pipeline.
//
// Parallelism shape (per ADR-029's Phase A architecture):
//   - Walk produces the ordered file list (serial, cheap).
//   - For each file: read bytes, chunk, (if model != nil) encode each
//     chunk — all inside one worker. Per-file workers eliminate the
//     queue depth + serialization that a stage-based pipeline would
//     introduce.
//   - Collector reassembles by file index.
//   - Migration folding stays serial.
//
// Concurrency safety prerequisites (verified in parallelism Phase 1):
//   - embed.StaticModel.Encode is goroutine-safe (TestEncodeConcurrent).
//   - chunk.ChunkFile / sql.ParseFile are pure functions of their inputs.
//   - The treesitter chunker's ParserPool is sync.Pool-backed by design
//     (ADR-010); regex + line chunkers are stateless.
//   - tokenizerPool (v0.8.6 / ADR-028) is sync.Pool, concurrency-safe.
func walkAndChunkFSWithModel(fsys fs.FS, mode Mode, chunkerName string, model *embed.StaticModel, opts FSOptions) (
	chunks []chunk.Chunk, vecs [][]float32, returnedModel *embed.StaticModel, migDirs map[string]bool, err error,
) {
	// ModeHybridRerank uses the same build-time pipeline as ModeHybrid
	// (M4: the reranker is layered on at query time, not at index build).
	if mode != ModeBM25 && mode != ModeSemantic && mode != ModeHybrid && mode != ModeHybridRerank {
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

	type fileResult struct {
		chunks []chunk.Chunk
		vecs   [][]float32
	}
	results := make([]fileResult, len(files))

	type job struct {
		idx int
		rel string
	}
	numWorkers := runtime.NumCPU()
	jobs := make(chan job, numWorkers*2)
	errCh := make(chan error, numWorkers)
	var wg sync.WaitGroup

	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				data, rerr := fs.ReadFile(fsys, j.rel)
				if rerr != nil {
					select {
					case errCh <- fmt.Errorf("read %s: %w", j.rel, rerr):
					default:
					}
					continue
				}
				skipSQLStructural := migDirs[path.Dir(j.rel)]
				cs, cerr := chunkOneFile(chunkerName, j.rel, data, skipSQLStructural)
				if cerr != nil {
					select {
					case errCh <- fmt.Errorf("chunk %s: %w", j.rel, cerr):
					default:
					}
					continue
				}
				var localVecs [][]float32
				if model != nil {
					localVecs = make([][]float32, len(cs))
					for i, c := range cs {
						localVecs[i] = model.Encode(c.Text)
					}
				}
				results[j.idx] = fileResult{chunks: cs, vecs: localVecs}
			}
		}()
	}

	for i, rel := range files {
		jobs <- job{idx: i, rel: rel}
	}
	close(jobs)
	wg.Wait()

	// Surface the first worker error if any. Workers continue draining
	// jobs after their first error so the wg.Wait above is unblocked;
	// the errCh capacity (numWorkers) means later errors are dropped.
	// That's fine — one root-cause error per build is all callers need.
	select {
	case e := <-errCh:
		return nil, nil, nil, nil, e
	default:
	}

	// Flatten in file index order — deterministic across runs because
	// repo.WalkFS returns files in lexical order and worker results are
	// indexed by job.idx, not arrival order.
	for _, r := range results {
		chunks = append(chunks, r.chunks...)
		if model != nil {
			vecs = append(vecs, r.vecs...)
		}
	}

	// Migration-folding pass: deterministic order over the discovered
	// dirs so the produced chunk order is stable across runs. Stays
	// serial — small fraction of total cost, and the serial loop
	// trivially preserves determinism without any extra machinery.
	if len(migDirs) > 0 {
		dirs := make([]string, 0, len(migDirs))
		for d := range migDirs {
			dirs = append(dirs, d)
		}
		sort.Strings(dirs)
		for _, d := range dirs {
			folded, ferr := sql.FoldMigrations(fsys, d, opts.LogWriter)
			if ferr != nil {
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
		// Invariant: when model != nil, ix.vecs has one entry per
		// existing chunk. BuildIndex enforces this on all current
		// callers (FromFSWithModel + LoadSerializedIndex); a
		// hypothetical caller that sets model without parallel vecs
		// would produce a short mergedVecs and ann.Flat over a
		// truncated matrix — wrong rankings silently. L2 hardening:
		// fail fast in that case rather than continue with bad data.
		if len(ix.vecs) != len(ix.chunks) {
			panic(fmt.Sprintf(
				"search: WithExtraChunks invariant: model != nil requires len(vecs)==len(chunks); got vecs=%d chunks=%d",
				len(ix.vecs), len(ix.chunks)))
		}
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

// Model returns the embedding model the index was built with, or nil
// for ModeBM25 indices. Exposed so bench harnesses (and the Stage-7a
// HyDE query-fusion path) can encode arbitrary text with the same
// model the corpus was encoded with — required for the fused-vector
// shape that SearchWithQVec expects. Read-only; the model is
// goroutine-safe.
func (ix *Index) Model() *embed.StaticModel { return ix.model }

// BM25 returns the underlying BM25 index. Exposed so bench harnesses
// can call IDF / DF on individual terms — used by the Stage-7a
// transform #2 oracle and PRF predictors to rank candidate
// identifiers by corpus distinctiveness and filter near-hapax
// tokens. Never nil for any non-degenerate index. Read-only;
// bm25.Index is goroutine-safe for queries.
func (ix *Index) BM25() *bm25.Index { return ix.bm }

// Vecs returns the per-chunk potion embeddings. Same length and
// order as Chunks(). nil for ModeBM25 indices. Exposed so bench
// harnesses (M0d encoder-cosine predictor) can build per-identifier
// context centroids over the already-encoded chunk vectors — a
// free aggregation that requires no new compute at index time.
// Read-only; the caller MUST NOT mutate.
func (ix *Index) Vecs() [][]float32 { return ix.vecs }

// ResolveChunk returns the chunk that contains the 1-indexed line in
// filePath, or nil if there is none. Mirrors semble utils._resolve_chunk:
// prefer an interior hit (line < end_line); fall back to a boundary
// (line == end_line) so end-of-file references still resolve. With
// multiple boundary-tied candidates the FIRST one in chunk-slice order
// wins — for chunks produced by repo.WalkFS that's deterministic
// (lexical file order, then in-file order from the chunker), but
// readers should not depend on the specific tied-boundary winner if
// they reorder the chunk slice.
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

// SetReranker attaches a neural reranker for ModeHybridRerank queries.
// Reranker implementations are goroutine-safe; the same instance can
// be shared across snapshot swaps (the LRU cache is content-hashed,
// so stale entries simply never get hit). Pass nil to detach — future
// ModeHybridRerank queries will then transparently downgrade to
// ModeHybrid.
//
// Optional knobs (default rerankN=50, β=0.25 per M0): WithRerankN,
// WithRerankBlendBeta.
func (ix *Index) SetReranker(r Reranker, opts ...RerankerOption) {
	ix.reranker = r
	ix.rerankCfg = defaultRerankerConfig
	for _, o := range opts {
		o(&ix.rerankCfg)
	}
}

// Search returns the top-k chunks for query under the index's
// build-time mode. Thin wrapper around SearchMode for callers that
// don't need per-call mode overrides (the historical signature, kept
// for backward compatibility).
func (ix *Index) Search(query string, k int) []Result {
	results, _ := ix.SearchMode(query, k, ix.mode)
	return results
}

// SearchModeWithTelemetry is SearchMode with a per-query timing
// breakdown. Returns the same (results, effective-mode) plus a
// populated *Telemetry. Used by `ken search --verbose`, `ken bench`,
// ken-mcp's info-level logging, and the optional MCP _telemetry
// response field. The Telemetry struct is documented in
// telemetry.go; zero-value fields mean "stage didn't run" or
// "instrumentation not available for this mode."
//
// Non-rerank modes (bm25 / semantic / hybrid) record only TotalWall.
// ModeHybridRerank records Stage1Wall / RerankWall / BlendWall plus
// the reranker sub-breakdown (via NeuralReranker.RerankWithTelemetry).
func (ix *Index) SearchModeWithTelemetry(query string, k int, mode Mode) ([]Result, Mode, Telemetry) {
	tel := Telemetry{}
	t0 := time.Now()

	// Reuse the existing dispatch when there's nothing extra to time.
	// For ModeHybridRerank we run an instrumented duplicate of the
	// hot path below; everything else delegates to SearchMode.
	if mode != ModeHybridRerank || ix.reranker == nil {
		results, effMode := ix.SearchMode(query, k, mode)
		tel.TotalWall = time.Since(t0)
		return results, effMode, tel
	}
	if ix.flat == nil || ix.model == nil {
		// Same defensive downgrade as SearchMode.
		results, effMode := ix.SearchMode(query, k, ModeBM25)
		tel.TotalWall = time.Since(t0)
		return results, effMode, tel
	}

	// Stage 1: hybrid retrieval (instrumented).
	fetch := ix.rerankCfg.rerankN
	if fetch < k {
		fetch = k
	}
	s1 := time.Now()
	ranked := hybridSearch(query, ix.model.Encode(query), ix.flat, ix.bm, ix.chunks, fetch, -1, nil)
	results := make([]Result, 0, len(ranked))
	for _, r := range ranked {
		c := ix.chunks[r.idx]
		if c.Tombstoned {
			continue
		}
		results = append(results, Result{Chunk: c, Score: r.score})
	}
	tel.Stage1Wall = time.Since(s1)

	// Stage 2: neural rerank (instrumented via the reranker's
	// optional RerankWithTelemetry method when supported).
	s2 := time.Now()
	results = applyRerankerWithTelemetry(ix.reranker, query, results, ix.rerankCfg, &tel)
	// applyRerankerWithTelemetry's wall is mostly the rerank model
	// work; the blend (sort + minmax) is a tiny tail. Bookkeep:
	tel.RerankWall = time.Since(s2)
	// BlendWall is the difference between the outer rerank wall and
	// the reranker-internal compute time, if available.
	if tel.RerankerQueryEncode > 0 || tel.RerankerCandidateEncode > 0 {
		modelWall := tel.RerankerQueryEncode
		if tel.RerankerCandidateEncode > modelWall {
			modelWall = tel.RerankerCandidateEncode // pipelined max, not sum
		}
		if tel.RerankWall > modelWall {
			tel.BlendWall = tel.RerankWall - modelWall
		}
	}

	if len(results) > k {
		results = results[:k]
	}
	tel.TotalWall = time.Since(t0)
	return results, ModeHybridRerank, tel
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
	if mode != ModeBM25 && (ix.flat == nil || ix.model == nil) {
		// Capability downgrade: requested semantic/hybrid but no
		// flat/model. Fall back to BM25 so the caller gets results
		// rather than a panic. Both checks are defensive: BuildIndex
		// sets flat + model atomically today, but a future construction
		// path that sets one without the other (e.g. LoadSerializedIndex
		// with model==nil — caught earlier by ErrModelRequired in v0.8.3,
		// but defense-in-depth here) shouldn't panic.
		mode = ModeBM25
	}
	// M4: ModeHybridRerank ⇒ ModeHybrid when no reranker is attached,
	// same "downgrade rather than error" ethos as the no-model case
	// above. The plan §9.3 calls this out explicitly: "transparently
	// downgrade … mirroring the existing model-missing downgrade pattern."
	if mode == ModeHybridRerank && ix.reranker == nil {
		mode = ModeHybrid
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
		ranked := hybridSearch(query, ix.model.Encode(query), ix.flat, ix.bm, ix.chunks, k, -1, nil)
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
	case ModeHybridRerank:
		// M4: deep over-fetch (rerankN, default 50) from stage-1 hybrid,
		// then neural rerank + score-blend (β=0.25 per M0). Truncate to
		// k AFTER the rerank so the user's k can be smaller than rerankN
		// without losing the reranker's reordering effect on positions
		// 1..k. The reranker pre-check above already downgraded if nil,
		// so ix.reranker is guaranteed non-nil here.
		fetch := ix.rerankCfg.rerankN
		if fetch < k {
			fetch = k
		}
		ranked := hybridSearch(query, ix.model.Encode(query), ix.flat, ix.bm, ix.chunks, fetch, -1, nil)
		// Tombstone-filter BEFORE the neural pass so the rerank
		// budget isn't spent encoding chunks that'll be dropped.
		results := make([]Result, 0, len(ranked))
		for _, r := range ranked {
			c := ix.chunks[r.idx]
			if c.Tombstoned {
				continue
			}
			results = append(results, Result{Chunk: c, Score: r.score})
		}
		results = applyReranker(ix.reranker, query, results, ix.rerankCfg)
		if len(results) > k {
			results = results[:k]
		}
		return results, mode
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

// SearchWithQVec runs the same retrieval pipeline as SearchMode but
// uses a caller-supplied dense query vector for the semantic side
// instead of ix.model.Encode(query). BM25 still tokenizes the original
// query text; α auto-detection and every downstream stage (RRF fuse,
// file-coherence + query boost, path penalties, optional Stage-6
// neural rerank) is unchanged.
//
// Purpose:
//   - Stage 7a HyDE bench harness (bench/ndcg/hyde_test.go) — fuses
//     the real query vector with potion(snippet) before retrieval.
//   - Future Stage 7a M5 production wiring — QueryAnalyzer surfaces
//     a HyDE doc, the calling layer fuses, and hands the fused vector
//     to this method.
//
// qVec MUST have the model's expected dimension (m.Dim()) and SHOULD
// be L2-normalized — the flat ANN computes raw dot products that are
// cosine only when both sides are unit-norm. Caller-side normalization
// after any blend is part of the contract.
//
// Capability downgrades mirror SearchMode exactly: requesting
// semantic/hybrid/hybrid-rerank against a BM25-only index falls back
// to ModeBM25 (qVec is unused in that case); ModeHybridRerank with no
// attached reranker downgrades to ModeHybrid.
func (ix *Index) SearchWithQVec(query string, qVec []float32, k int, mode Mode) ([]Result, Mode) {
	return ix.SearchWithQVecPredicted(query, qVec, nil, k, mode)
}

// SearchWithQVecPredicted is SearchWithQVec plus the Stage-7a
// transform #2 vocab-gap expansion: predicted identifiers from the
// NL query (oracle, PRF, encoder, ...) appended to the BM25 token bag
// and threaded into the embedded-symbol boost path. nil/empty
// predicted is a no-op and reduces to SearchWithQVec semantics.
//
// The neural rerank stage (when mode == ModeHybridRerank) does not
// consume predicted directly — it re-scores stage-1 candidates from
// its own forward pass. So transform #2's only mechanism on the
// default `hybrid+rerank` config is "pull more relevant chunks into
// the stage-1 shortlist." outputs/m0b-phase-b-results.md has the
// reasoning; m0c-results.md will have the numbers.
func (ix *Index) SearchWithQVecPredicted(query string, qVec []float32, predicted []string, k int, mode Mode) ([]Result, Mode) {
	if mode != ModeBM25 && (ix.flat == nil || ix.model == nil) {
		mode = ModeBM25
	}
	if mode == ModeHybridRerank && ix.reranker == nil {
		mode = ModeHybrid
	}
	overFetch := k + ix.tombstoneCount()
	switch mode {
	case ModeSemantic:
		hits := ix.flat.Query(qVec, overFetch)
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
		ranked := hybridSearch(query, qVec, ix.flat, ix.bm, ix.chunks, k, -1, predicted)
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
	case ModeHybridRerank:
		fetch := ix.rerankCfg.rerankN
		if fetch < k {
			fetch = k
		}
		ranked := hybridSearch(query, qVec, ix.flat, ix.bm, ix.chunks, fetch, -1, predicted)
		results := make([]Result, 0, len(ranked))
		for _, r := range ranked {
			c := ix.chunks[r.idx]
			if c.Tombstoned {
				continue
			}
			results = append(results, Result{Chunk: c, Score: r.score})
		}
		results = applyReranker(ix.reranker, query, results, ix.rerankCfg)
		if len(results) > k {
			results = results[:k]
		}
		return results, mode
	default: // ModeBM25 — qVec ignored
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

// SearchWithQVecTelemetry mirrors SearchModeWithTelemetry but uses a
// caller-supplied dense query vector for the semantic side, identical
// to SearchWithQVec's contract. Used by bench/ndcg/hyde_test.go to
// attribute per-query wall to stage-1 retrieval vs neural rerank vs
// reranker query/candidate encode vs cache hit/miss — without this
// the harness can only time the whole call and is blind to where the
// slowdown lives.
//
// Behavior parity with SearchWithQVec: same capability downgrades
// (bm25-only ⇒ ModeBM25 regardless of mode; reranker-missing
// ModeHybridRerank ⇒ ModeHybrid), same qVec contract (model.Dim()
// length, caller L2-normalizes after any blend).
func (ix *Index) SearchWithQVecTelemetry(query string, qVec []float32, k int, mode Mode) ([]Result, Mode, Telemetry) {
	return ix.SearchWithQVecPredictedTelemetry(query, qVec, nil, k, mode)
}

// SearchWithQVecPredictedTelemetry is SearchWithQVecTelemetry plus
// transform #2's predicted-identifier expansion (see
// SearchWithQVecPredicted). nil/empty predicted reduces to
// SearchWithQVecTelemetry semantics.
func (ix *Index) SearchWithQVecPredictedTelemetry(query string, qVec []float32, predicted []string, k int, mode Mode) ([]Result, Mode, Telemetry) {
	tel := Telemetry{}
	t0 := time.Now()

	if mode != ModeHybridRerank || ix.reranker == nil {
		results, effMode := ix.SearchWithQVecPredicted(query, qVec, predicted, k, mode)
		tel.TotalWall = time.Since(t0)
		return results, effMode, tel
	}
	if ix.flat == nil || ix.model == nil {
		results, effMode := ix.SearchWithQVecPredicted(query, qVec, predicted, k, ModeBM25)
		tel.TotalWall = time.Since(t0)
		return results, effMode, tel
	}

	fetch := ix.rerankCfg.rerankN
	if fetch < k {
		fetch = k
	}
	s1 := time.Now()
	ranked := hybridSearch(query, qVec, ix.flat, ix.bm, ix.chunks, fetch, -1, predicted)
	results := make([]Result, 0, len(ranked))
	for _, r := range ranked {
		c := ix.chunks[r.idx]
		if c.Tombstoned {
			continue
		}
		results = append(results, Result{Chunk: c, Score: r.score})
	}
	tel.Stage1Wall = time.Since(s1)

	s2 := time.Now()
	results = applyRerankerWithTelemetry(ix.reranker, query, results, ix.rerankCfg, &tel)
	tel.RerankWall = time.Since(s2)
	if tel.RerankerQueryEncode > 0 || tel.RerankerCandidateEncode > 0 {
		modelWall := tel.RerankerQueryEncode
		if tel.RerankerCandidateEncode > modelWall {
			modelWall = tel.RerankerCandidateEncode
		}
		if tel.RerankWall > modelWall {
			tel.BlendWall = tel.RerankWall - modelWall
		}
	}

	if len(results) > k {
		results = results[:k]
	}
	tel.TotalWall = time.Since(t0)
	return results, ModeHybridRerank, tel
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
