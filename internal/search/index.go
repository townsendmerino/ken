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
	"context"
	"fmt"
	"hash/maphash"
	"io"
	"io/fs"
	"os"
	"path"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
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
	"github.com/townsendmerino/ken/internal/structural"
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

	// DisableEnrichment turns off the Stage 8 Arm B structural-
	// enrichment prefix that the indexer prepends to each chunk's
	// Text before BM25/embed (the `# func: NAME | calls: A |
	// raises: X\n` line). When true, chunks pass through unchanged
	// (v0.5/v0.6/v0.7 behavior). When false (the zero-value
	// default), the indexer extracts per-file structural data via
	// structural.ExtractFile + structural.EnrichFromFileStruct and
	// prepends the label to every chunk's Text. Files whose
	// extension has no registered extractor are no-op pass-through
	// regardless of the flag.
	//
	// Default-on is the kickoff's product decision: enrichment is
	// a deterministic, pure-Go, no-extra-model improvement validated
	// on stripped-CSN (+0.0100 NDCG@10, +0.0160 R@50, p=0.04 at
	// M0d) and CoSQA dev (+0.0342 hybrid / +0.0696 semantic /
	// +0.0412 bm25 at Stage 8 Gate 1). The opt-out is for
	// back-compat / debugging / corpora whose extractor misbehaves;
	// FromPath/FromFS additionally read KEN_ENRICH=off as an env
	// shortcut.
	DisableEnrichment bool

	// EmbedCache, when non-nil, is a persistent content-hash → vector cache
	// (cold-start M3) consulted for every model.Encode on the build / watch /
	// lazy-enrich paths: a cache hit skips the encode. Deterministic (Model2Vec
	// is a pure function of text+model), so it only speeds up rebuilds of
	// already-seen chunk text — the second line of defense behind an M1 snapshot
	// load (which skips embedding entirely). nil = no cache (encode directly).
	EmbedCache VecCache

	// StagedEmbedding defers the semantic (embedding) arm off the cold-build
	// critical path (cold-start M4). When true on a model-needing mode, the
	// initial build embeds NOTHING and serves as BM25 (lexical-only) for an
	// instant first query; a WatchedIndex then embeds every chunk in the
	// background and republishes as the configured mode (bm25 → hybrid) — the
	// same serve-bm25-then-upgrade pattern as model auto-fetch (ADR-037).
	// Ignored for ModeBM25 (nothing to defer). BM25 results are correct
	// immediately; the semantic arm just arrives shortly after.
	StagedEmbedding bool

	// LazyEnrichment defers Arm B enrichment off the cold-build critical path
	// (cold-start M2). When true, the initial build embeds RAW chunks (no
	// `# func:` label) so first-servable pays only the walk/chunk/embed floor,
	// not the ~50%-of-index-time tree-sitter parse; a WatchedIndex then runs a
	// background pass that extracts the label per file, re-embeds, and
	// republishes the fully-enriched index atomically. Ignored when
	// DisableEnrichment is set (nothing to defer). Only meaningful on the
	// WatchedIndex path (a live server); the plain FromFS/FromPath builds treat
	// it as enrich-inline. Enrichment is additive (label-only, ADR-035), so
	// pre-enrichment results are well-formed — just lower-ranked until the
	// background pass lands.
	LazyEnrichment bool
}

// Mode selects the retrieval strategy.
type Mode int

const (
	ModeBM25 Mode = iota
	ModeSemantic
	ModeHybrid
	// ModeHybridRerank is ModeHybrid + a second-stage neural reranker
	// (M4; see the rerank plan, docs/internal/results/ken-rerank-plan.md §9). Requires both
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
	return FromFSWithOptions(fsys, mode, chunkerName, modelDir, defaultFSOptions())
}

// defaultFSOptions returns the FSOptions the no-knob entry points
// (FromFS, FromPath, FromFSWithModel) thread through to the walker.
// Honors the KEN_ENRICH env shortcut: any of "0", "off", "false",
// "no" (case-insensitive) disables Arm B enrichment; all other
// values (including unset and the explicit "on") leave it enabled.
// Callers that want explicit control should construct FSOptions
// themselves and call FromFSWithOptions.
func defaultFSOptions() FSOptions {
	opts := FSOptions{}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("KEN_ENRICH"))) {
	case "0", "off", "false", "no":
		opts.DisableEnrichment = true
	}
	return opts
}

// FromFSWithOptions is FromFS plus the v0.7.1 FSOptions knob — currently
// only the migration-folding opt-out. The zero value of FSOptions
// matches the FromFS default exactly, so callers that don't care can
// keep using FromFS.
func FromFSWithOptions(fsys fs.FS, mode Mode, chunkerName, modelDir string, opts FSOptions) (*Index, error) {
	// Public 1.0 entry — no ctx in the signature; the build isn't cancellable
	// here. The ken-mcp server uses the ctx-aware WatchedIndex path instead.
	chunks, vecs, model, _, err := walkAndChunkFS(context.Background(), fsys, mode, chunkerName, modelDir, opts)
	if err != nil {
		return nil, err
	}
	return BuildIndex(chunks, vecs, mode, model), nil
}

// FromPath is the real-filesystem entry point — a thin wrapper
// around FromFS that resolves `root` to an `os.DirFS`. Use this
// when you have a directory path on disk; use FromFS directly when
// you have an `fs.FS` (e.g. an embedded `//go:embed` corpus for the
// mcp.Run library pattern).
//
// 1.0-stable. The deprecation marker was dropped after the 1.0
// audit confirmed both entry points are useful and stable. Keeping
// both is cheap (FromPath is 1 line) and saves callers a
// `os.DirFS(...)` wrap at every invocation.
func FromPath(root string, mode Mode, chunkerName, modelDir string) (*Index, error) {
	return FromFS(os.DirFS(root), mode, chunkerName, modelDir)
}

// walkAndChunk is the real-FS-only bootstrap path retained for
// internal/search/watch.go (fsnotify is real-FS-only by construction).
// New code should call walkAndChunkFS directly. The migDirs return is
// the set of directories the migration-folding pass treated as a
// migration chain — WatchedIndex carries this forward so fsnotify-driven
// flushes know which dirs to re-fold.
func walkAndChunk(ctx context.Context, root string, mode Mode, chunkerName, modelDir string, opts FSOptions) (
	chunks []chunk.Chunk, vecs [][]float32, model *embed.StaticModel, migDirs map[string]bool, err error,
) {
	return walkAndChunkFS(ctx, os.DirFS(root), mode, chunkerName, modelDir, opts)
}

// walkAndChunkFS resolves modelDir to an *embed.StaticModel (when the mode
// needs one) and delegates the actual walk+chunk+embed pass to
// walkAndChunkFSWithModel. Kept for callers that resolve the model path
// at index-build time (the in-tree path-based entry points and the
// watcher).
func walkAndChunkFS(ctx context.Context, fsys fs.FS, mode Mode, chunkerName, modelDir string, opts FSOptions) (
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
	chunks, vecs, returnedModel, migDirs, err := walkAndChunkFSWithModel(ctx, fsys, mode, chunkerName, model, opts)
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
// enrichChunks prepends the per-file Arm B structural-enrichment label
// (func:/calls:/raises: terms from ExtractFile, ADR-035) to every chunk's
// Text in place — before BM25 tokenization AND embedding, so both the
// lexical and semantic sides see it. A no-op when disabled or when the
// file's extension has no extractor (ExtractFile returns nil).
//
// Shared by the initial build (walkAndChunkFSWithModel) and the incremental
// watch re-index (WatchedIndex.appendFile) so the two can't drift (code
// review M3): before this, appendFile embedded raw text, so every file
// edited during a session was re-indexed WITHOUT the label — a heterogeneous
// index whose BM25 tokens and embeddings diverged from the initial build's.
func enrichChunks(rel string, data []byte, cs []chunk.Chunk, disable bool) {
	if disable {
		return
	}
	label := enrichLabelFor(rel, data)
	if label == "" {
		return
	}
	for i := range cs {
		cs[i].Text = label + cs[i].Text
	}
}

// enrichLabelFor returns the Arm B structural label for a file (`# func: … |
// calls: … | raises: …\n`), or "" when the file has no registered extractor or
// no structural content. Single source of the label so the inline build path
// (enrichChunks) and the lazy background pass (WatchedIndex.enrichCorpusInBackground)
// can't drift.
func enrichLabelFor(rel string, data []byte) string {
	efs := structural.ExtractFile(rel, data)
	if efs == nil {
		return ""
	}
	return structural.EnrichFromFileStruct(efs, structural.EnrichOptions{})
}

// stripEnrichLabel removes a leading Arm B enrichment label line if present.
// Package-local alias so watch.go's idempotent warm pass (audit R2) doesn't
// need its own structural import; the real logic lives in structural.StripLabel.
func stripEnrichLabel(text string) string {
	return structural.StripLabel(text)
}

// Concurrency safety prerequisites (verified in parallelism Phase 1):
//   - embed.StaticModel.Encode is goroutine-safe (TestEncodeConcurrent).
//   - chunk.ChunkFile / sql.ParseFile are pure functions of their inputs.
//   - The treesitter chunker's ParserPool is sync.Pool-backed by design
//     (ADR-010); regex + line chunkers are stateless.
//   - tokenizerPool (v0.8.6 / ADR-028) is sync.Pool, concurrency-safe.
func walkAndChunkFSWithModel(ctx context.Context, fsys fs.FS, mode Mode, chunkerName string, model *embed.StaticModel, opts FSOptions) (
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

	for range numWorkers {
		wg.Go(func() {
			for j := range jobs {
				// Cancellation checkpoint: on a cancelled build (e.g. SIGINT
				// during a large uncached index) skip the heavy read/chunk/
				// embed but keep draining `jobs` so wg.Wait unblocks. The
				// post-Wait ctx.Err() check turns this into a clean error
				// return — no partial index is published.
				if ctx.Err() != nil {
					continue
				}
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
				// Arm B enrichment (Stage 8): per-file structural
				// extract → format the M0d label (`# func: NAME |
				// calls: A | raises: X\n`) → prepend to every chunk's
				// Text before BM25 tokenization / embedding. The
				// label gets BOTH the lexical-side (BM25 sees the
				// tokens) and semantic-side (the embed encoder sees
				// them) of the production retrieval signal — same
				// shape as the M0d/Gate-1 materialized corpora that
				// validated the +0.0100 NDCG@10 win. ExtractFile
				// returns nil silently when the extension has no
				// extractor (no label = chunks pass through
				// unchanged, which is the correct no-op behavior
				// for unsupported languages). DisableEnrichment
				// opts out entirely.
				// LazyEnrichment (M2) and StagedEmbedding (M4) both defer the
				// enrichment parse to the background warm pass for a fast
				// first-servable; the initial build serves un-labelled chunks.
				enrichChunks(j.rel, data, cs, opts.DisableEnrichment || opts.LazyEnrichment || opts.StagedEmbedding)
				// StagedEmbedding (M4) defers embedding to a background pass:
				// the initial build serves BM25 for a fast first query.
				var localVecs [][]float32
				if model != nil && !opts.StagedEmbedding {
					localVecs = make([][]float32, len(cs))
					for i, c := range cs {
						localVecs[i] = encodeCached(opts.EmbedCache, model, c.Text)
					}
				}
				results[j.idx] = fileResult{chunks: cs, vecs: localVecs}
			}
		})
	}

feedLoop:
	for i, rel := range files {
		select {
		case <-ctx.Done():
			// Stop enqueueing; workers drain the buffered jobs (skipping
			// work) so wg.Wait below still completes promptly.
			break feedLoop
		case jobs <- job{idx: i, rel: rel}:
		}
	}
	close(jobs)
	wg.Wait()

	// Cancelled build → return ctx.Err() and publish NO partial index. This
	// runs before the flatten/migration passes, so the caller
	// (newWatchedIndexWithDebounce) never builds an Index from partial
	// materials; the cache's singleflight discards the error result.
	if cerr := ctx.Err(); cerr != nil {
		return nil, nil, nil, nil, fmt.Errorf("search: index build cancelled: %w", cerr)
	}

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
			if cerr := ctx.Err(); cerr != nil {
				return nil, nil, nil, nil, fmt.Errorf("search: index build cancelled: %w", cerr)
			}
			folded, ferr := sql.FoldMigrations(fsys, d, opts.LogWriter)
			if ferr != nil {
				if opts.LogWriter != nil {
					fmt.Fprintf(opts.LogWriter, "search: FoldMigrations(%q): %v\n", d, ferr)
				}
				continue
			}
			for _, c := range folded {
				chunks = append(chunks, c)
				// Mirror the worker path's staged/cache handling (audit R6):
				// without the !StagedEmbedding guard, a repo with a migrations
				// dir + KEN_MCP_STAGED=1 produced len(vecs)==numFolded while
				// len(chunks)==numRegular+numFolded — misaligning the parallel
				// slices ann.New/compactCorpus/serialize all assume, giving
				// wrong FindRelated neighbours and eventually a flush panic.
				// Also route through encodeCached so folded chunks aren't
				// silently excluded from the embed cache.
				if model != nil && !opts.StagedEmbedding {
					vecs = append(vecs, encodeCached(opts.EmbedCache, model, c.Text))
				}
			}
		}
	}
	// Parallel-slice invariant (audit R6): vecs is either empty (BM25 or
	// staged — filled later by the warm pass) or one-per-chunk. Anything
	// else silently corrupts ann.Flat / compaction / serialization.
	if len(vecs) != 0 && len(vecs) != len(chunks) {
		return nil, nil, nil, nil, fmt.Errorf(
			"search: build invariant: len(vecs)=%d must be 0 or len(chunks)=%d", len(vecs), len(chunks))
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
	chunks, vecs, m, _, err := walkAndChunkFSWithModel(context.Background(), fsys, mode, chunkerName, model, FSOptions{})
	if err != nil {
		return nil, err
	}
	return BuildIndex(chunks, vecs, mode, m), nil
}

// BuildIndex assembles a snapshot *Index from a chunks slice and (for
// semantic/hybrid) the parallel embedding vectors. It re-tokenizes every
// chunk for BM25 — incremental BM25 postings updates are intentionally
// not implemented (see docs/internal/DECISIONS.md ADR-012; the rebuild is dwarfed
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
	return buildIndexFromDocs(chunks, tokenizeDocs(chunks, nil), vecs, mode, model)
}

// buildIndexFromDocs is BuildIndex with the per-chunk BM25 token lists
// supplied by the caller (docs[i]==nil for tombstoned chunks). Split out
// so the incremental watch path can feed cached tokens (audit §5) while
// the cold path computes them fresh — both share the assembly + ann.New.
// docs MUST be index-aligned with chunks.
func buildIndexFromDocs(chunks []chunk.Chunk, docs [][]string, vecs [][]float32, mode Mode, model *embed.StaticModel) *Index {
	ix := &Index{mode: mode, chunks: chunks, bm: bm25.Build(docs), model: model, vecs: vecs}
	if model != nil {
		ix.flat = ann.New(vecs)
	}
	return ix
}

// tokenCache memoizes BM25 tokenization keyed by chunk text. Tokens are a
// pure function of Chunk.Text, so on the incremental watch path — where a
// single-file save re-chunks ~5 of N chunks — reusing cached tokens turns
// BuildIndex's per-flush O(corpus) tokenize into O(changed) (audit §5).
//
// tokenizeDocs rebuilds the map from the current chunk set each call, so
// entries whose text was edited away are evicted; the map holds at most
// one entry per live chunk (no unbounded process-lifetime growth — the
// smell called out in audit §6). Not safe for concurrent use; the watch
// path only touches it under corpusMu.
//
// Keyed by a 64-bit FNV hash of the chunk text, NOT the full text string
// (audit R11): a string-keyed map hashes the ENTIRE text on every lookup
// AND insert — two full-length hashes per chunk per flush, O(corpus bytes)
// even when nothing changed (e.g. a Tier-2 SetExtraChunks rebuild). The
// hash key is computed once per chunk and the map then hashes 8 bytes.
// Collision risk is a benign, astronomically-rare wrong-tokens-for-one-
// chunk (same class encodeCached already accepts for embeddings).
type tokenCache struct {
	byHash map[uint64][]string
}

func newTokenCache() *tokenCache { return &tokenCache{byHash: map[uint64][]string{}} }

// tokenHashSeed seeds the token-cache hash. Process-random (the cache is
// ephemeral and never serialized, so cross-process stability is irrelevant),
// which also makes collisions unconstructible — relevant since ken shallow-
// clones untrusted remote repos (audit N5 note).
var tokenHashSeed = maphash.MakeSeed()

// hashText64 hashes text for the token-cache key. maphash.String is AES-
// backed and ZERO-ALLOC (audit N5): the previous fnv.New64a().Write([]byte(
// text)) allocated a full copy of every chunk's text per flush plus a hasher,
// through a hash.Hash64 interface that defeats inlining — a net loss versus
// the runtime memhash it was meant to beat.
//
// This is ONE of three deliberately-different content hashes in the package,
// each matched to its durability need: maphash here (in-memory token cache —
// speed/zero-alloc, per-process seed OK), fnv64a in neural_rerank.go (in-memory
// rerank LRU key), and sha256 in embed_cache.go (PERSISTENT on-disk embed cache
// — needs a stable, process-independent digest). Not an oversight.
func hashText64(text string) uint64 {
	return maphash.String(tokenHashSeed, text)
}

// tokenizeDocs returns the index-aligned BM25 token lists for chunks.
// Tombstoned chunks map to nil (no postings, df unaffected). Misses are
// tokenized in parallel across NumCPU workers — each worker writes only
// its own docs[i], so the result is identical to a serial pass regardless
// of worker count (byte-stability preserved; bm25.Build consumes the
// index-ordered slice). When cache is non-nil, cached texts are reused
// and the cache is repopulated to exactly the current live chunks.
func tokenizeDocs(chunks []chunk.Chunk, cache *tokenCache) [][]string {
	docs := make([][]string, len(chunks))
	if cache == nil {
		miss := make([]int, 0, len(chunks))
		for i := range chunks {
			if !chunks[i].Tombstoned {
				miss = append(miss, i)
			}
		}
		parallelTokenize(chunks, docs, miss)
		return docs
	}
	next := make(map[uint64][]string, len(chunks))
	hashes := make([]uint64, len(chunks)) // one full-text hash per chunk, reused below
	miss := make([]int, 0)
	for i := range chunks {
		if chunks[i].Tombstoned {
			continue
		}
		hashes[i] = hashText64(chunks[i].Text)
		if toks, ok := cache.byHash[hashes[i]]; ok {
			docs[i] = toks
			next[hashes[i]] = toks
		} else {
			miss = append(miss, i)
		}
	}
	parallelTokenize(chunks, docs, miss)
	for _, i := range miss {
		next[hashes[i]] = docs[i]
	}
	cache.byHash = next
	return docs
}

// parallelTokenize tokenizes chunks[i].Text for every i in idxs, writing
// docs[i]. bm25.Tokenize is goroutine-safe (its scratch buffers come from
// a sync.Pool), and workers touch disjoint docs indices, so no locking is
// needed. Small batches run serially to skip the goroutine overhead.
func parallelTokenize(chunks []chunk.Chunk, docs [][]string, idxs []int) {
	n := len(idxs)
	if n == 0 {
		return
	}
	workers := min(runtime.NumCPU(), n)
	if workers <= 1 || n < 64 {
		for _, i := range idxs {
			docs[i] = bm25.Tokenize(chunks[i].Text)
		}
		return
	}
	var next atomic.Int64
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for {
				k := int(next.Add(1)) - 1
				if k >= n {
					return
				}
				i := idxs[k]
				docs[i] = bm25.Tokenize(chunks[i].Text)
			}
		}()
	}
	wg.Wait()
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
func (ix *Index) WithExtraChunks(extras []chunk.Chunk) (*Index, error) {
	if len(extras) == 0 {
		// No-op: rebuild from the receiver's state. Returns a fresh
		// pointer (not ix itself) so callers can always treat the
		// return value as a new snapshot to atomic-store.
		return BuildIndex(ix.chunks, ix.vecs, ix.mode, ix.model), nil
	}

	merged := make([]chunk.Chunk, 0, len(ix.chunks)+len(extras))
	merged = append(merged, ix.chunks...)
	merged = append(merged, extras...)

	var mergedVecs [][]float32
	if ix.model != nil {
		// Semantic / hybrid: encode extras via the retained model.
		// Invariant: when model != nil, ix.vecs has one entry per
		// existing chunk. A caller that sets model without parallel vecs
		// would produce a short mergedVecs and ann.Flat over a truncated
		// matrix — wrong rankings silently. Return an error instead of
		// panicking (audit §23): this is reachable from mcp.Run's DB-
		// refresh callback, and a panic there takes down the whole MCP
		// server; the caller keeps serving the prior snapshot.
		if len(ix.vecs) != len(ix.chunks) {
			return nil, fmt.Errorf(
				"search: WithExtraChunks: model != nil requires len(vecs)==len(chunks); got vecs=%d chunks=%d",
				len(ix.vecs), len(ix.chunks))
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

	return BuildIndex(merged, mergedVecs, ix.mode, ix.model), nil
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
// resolveMode applies the capability downgrades every Search* entry point
// shares: semantic/hybrid/rerank against a bm25-only index (no flat/model)
// falls back to ModeBM25; ModeHybridRerank with no attached reranker downgrades
// to ModeHybrid. Both are "downgrade rather than panic/error" — see SearchMode.
func (ix *Index) resolveMode(mode Mode) Mode {
	if mode != ModeBM25 && (ix.flat == nil || ix.model == nil) {
		mode = ModeBM25
	}
	if mode == ModeHybridRerank && ix.reranker == nil {
		mode = ModeHybrid
	}
	return mode
}

// collectResults builds up to k tombstone-free Results by walking n candidates
// and mapping each to (chunkIndex, score) via at. Every retriever branch shares
// this "over-fetch, skip tombstoned, stop at k" shape; the differences are only
// the hit type's index field (ann.Hit.Index / rankedItem.idx / bm25 .Doc). Pass
// k=n to collect all non-tombstoned (the rerank path truncates after re-scoring).
func (ix *Index) collectResults(n, k int, at func(i int) (idx int, score float64)) []Result {
	out := make([]Result, 0, k)
	for i := range n {
		idx, score := at(i)
		c := ix.chunks[idx]
		if c.Tombstoned {
			continue
		}
		out = append(out, Result{Chunk: c, Score: score})
		if len(out) >= k {
			break
		}
	}
	return out
}

// searchCore is the shared retrieval body behind SearchMode and
// SearchWithQVec*. Capability downgrades are the CALLER's job (via resolveMode);
// qVec is the dense query vector (ignored for ModeBM25 — the caller need not
// even compute it there), predicted the transform-#2 identifier expansion (nil
// for the plain entry points).
func (ix *Index) searchCore(query string, qVec []float32, predicted []string, k int, mode Mode) []Result {
	overFetch := k + ix.tombstoneCount()
	switch mode {
	case ModeSemantic:
		// semble search_semantic: cosine similarity, no rerank.
		hits := ix.flat.Query(qVec, overFetch)
		return ix.collectResults(len(hits), k, func(i int) (int, float64) { return hits[i].Index, hits[i].Score })
	case ModeHybrid:
		// Over-fetch by tombstoneCount (audit §11 / code review §4):
		// hybridSearch does NOT filter Tombstoned and collectResults trims to k
		// AFTER dropping them, so without the headroom a tombstoned top-k hit
		// leaves the result short of k.
		ranked := hybridSearch(query, qVec, ix.flat, ix.bm, ix.chunks, overFetch, -1, predicted)
		return ix.collectResults(len(ranked), k, func(i int) (int, float64) { return ranked[i].idx, ranked[i].score })
	case ModeHybridRerank:
		// M4: deep over-fetch (rerankN) from stage-1 hybrid, tombstone-filter
		// BEFORE the neural pass (don't spend rerank budget on dropped chunks),
		// then truncate to k AFTER rerank so k<rerankN keeps the reordering.
		fetch := max(ix.rerankCfg.rerankN, k)
		ranked := hybridSearch(query, qVec, ix.flat, ix.bm, ix.chunks, fetch, -1, predicted)
		results := ix.collectResults(len(ranked), len(ranked), func(i int) (int, float64) { return ranked[i].idx, ranked[i].score })
		results = applyReranker(ix.reranker, query, results, ix.rerankCfg)
		if len(results) > k {
			results = results[:k]
		}
		return results
	default: // ModeBM25 — raw lexical (Stage 1 behavior, no rerank; qVec ignored)
		hits := ix.bm.TopK(bm25.Tokenize(query), overFetch)
		return ix.collectResults(len(hits), k, func(i int) (int, float64) { return hits[i].Doc, hits[i].Score })
	}
}

// searchRerankTelemetryCore is the instrumented ModeHybridRerank body shared by
// SearchModeWithTelemetry and SearchWithQVecPredictedTelemetry. Preconditions
// (caller-checked before delegating here): mode == ModeHybridRerank and
// reranker/flat/model are all non-nil. t0 is the entry timestamp for TotalWall.
func (ix *Index) searchRerankTelemetryCore(query string, qVec []float32, predicted []string, k int, t0 time.Time) ([]Result, Mode, Telemetry) {
	tel := Telemetry{}

	// Stage 1: hybrid retrieval (instrumented).
	fetch := max(ix.rerankCfg.rerankN, k)
	s1 := time.Now()
	ranked := hybridSearch(query, qVec, ix.flat, ix.bm, ix.chunks, fetch, -1, predicted)
	results := ix.collectResults(len(ranked), len(ranked), func(i int) (int, float64) { return ranked[i].idx, ranked[i].score })
	tel.Stage1Wall = time.Since(s1)

	// Stage 2: neural rerank (instrumented via the reranker's optional
	// RerankWithTelemetry method when supported).
	s2 := time.Now()
	results = applyRerankerWithTelemetry(ix.reranker, query, results, ix.rerankCfg, &tel)
	// applyRerankerWithTelemetry's wall is mostly the rerank model work; the
	// blend (sort + minmax) is a tiny tail. BlendWall = outer rerank wall minus
	// the reranker-internal compute (pipelined max of query/candidate encode).
	tel.RerankWall = time.Since(s2)
	if tel.RerankerQueryEncode > 0 || tel.RerankerCandidateEncode > 0 {
		modelWall := max(tel.RerankerCandidateEncode, tel.RerankerQueryEncode)
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

func (ix *Index) SearchModeWithTelemetry(query string, k int, mode Mode) ([]Result, Mode, Telemetry) {
	t0 := time.Now()
	// Nothing extra to time unless we're actually going to neural-rerank.
	if mode != ModeHybridRerank || ix.reranker == nil {
		results, effMode := ix.SearchMode(query, k, mode)
		return results, effMode, Telemetry{TotalWall: time.Since(t0)}
	}
	if ix.flat == nil || ix.model == nil {
		results, effMode := ix.SearchMode(query, k, ModeBM25)
		return results, effMode, Telemetry{TotalWall: time.Since(t0)}
	}
	return ix.searchRerankTelemetryCore(query, ix.model.Encode(query), nil, k, t0)
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
	mode = ix.resolveMode(mode)
	// Encode the query only when a dense arm actually runs — ModeBM25 (incl. a
	// downgrade to it) needs no vector, and model may be nil there.
	var qVec []float32
	if mode != ModeBM25 {
		qVec = ix.model.Encode(query)
	}
	return ix.searchCore(query, qVec, nil, k, mode), mode
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
// the stage-1 shortlist" — the HyDE Phase B analysis established this.
func (ix *Index) SearchWithQVecPredicted(query string, qVec []float32, predicted []string, k int, mode Mode) ([]Result, Mode) {
	mode = ix.resolveMode(mode)
	return ix.searchCore(query, qVec, predicted, k, mode), mode
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
	t0 := time.Now()
	if mode != ModeHybridRerank || ix.reranker == nil {
		results, effMode := ix.SearchWithQVecPredicted(query, qVec, predicted, k, mode)
		return results, effMode, Telemetry{TotalWall: time.Since(t0)}
	}
	if ix.flat == nil || ix.model == nil {
		results, effMode := ix.SearchWithQVecPredicted(query, qVec, predicted, k, ModeBM25)
		return results, effMode, Telemetry{TotalWall: time.Since(t0)}
	}
	return ix.searchRerankTelemetryCore(query, qVec, predicted, k, t0)
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
