package search

// corpus.go — WatchedIndex corpus mutation + index composition (split out of
// watch.go, #6): the debounced flush, incremental reconcile, tombstone/append
// per file, migration re-fold, compaction, the unioned-index (re)build, and the
// reranker / DB-extra-chunk wiring. Everything here mutates or composes what the
// published *Index contains; the fsnotify plumbing that DRIVES it lives in
// watch.go, and the flush notifications it emits live in notify.go.

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/townsendmerino/aikit/chunk"
	"github.com/townsendmerino/ken/internal/repo"
	"github.com/townsendmerino/ken/internal/sql"
)

// warmCorpusInBackground is the unified cold-start warm pass for M2 (lazy
// enrichment) and M4 (staged embedding). After a deferred build published a
// partial index — un-labelled (M2/M4) and/or vector-less BM25 (M4) — for a fast
// first query, this brings it to full quality: prepend the Arm B label per file
// (when warmDoEnrich), re-embed under the target mode, and republish atomically,
// upgrading the served mode (bm25 → realMode) if staged.
//
// It works on FRESH slices — the published partial index shares w.chunks/w.vecs
// backing arrays and readers use them lock-free, so it must never mutate those
// in place. Holds corpusMu for the pass (reads unaffected; watch flushes queue,
// and at cold start there are typically no edits yet). ctx cancellation (Close)
// aborts at the next file boundary. The parse honors KEN_ENRICH_FILE_BUDGET_MS
// via ExtractFile; embedding honors the M3 cache.
func (w *WatchedIndex) warmCorpusInBackground(ctx context.Context) {
	start := time.Now()
	w.corpusMu.Lock()
	defer w.corpusMu.Unlock()

	// Target mode: realMode when staged (upgrades bm25 → hybrid), else the
	// current mode (M2 lazy-only keeps its mode).
	targetMode := w.mode
	if w.realMode != ModeBM25 {
		targetMode = w.realMode
	}
	needEmbed := w.model != nil && targetMode.needsModel()

	newChunks := make([]chunk.Chunk, len(w.chunks))
	copy(newChunks, w.chunks)
	newVecs := make([][]float32, len(newChunks))
	if !needEmbed {
		// Preserve any existing vectors verbatim (nothing to (re)embed).
		copy(newVecs, w.vecs)
	}

	byFile := map[string][]int{}
	for i := range newChunks {
		if newChunks[i].Tombstoned {
			continue
		}
		byFile[newChunks[i].File] = append(byFile[newChunks[i].File], i)
	}

	for rel, idxs := range byFile {
		if ctx.Err() != nil {
			return // Close() cancelled — leave the partial index published
		}
		var label string
		if w.warmDoEnrich {
			if data, err := os.ReadFile(filepath.Join(w.root, rel)); err == nil {
				label = enrichLabelFor(rel, data)
			}
		}
		for _, i := range idxs {
			if label != "" {
				// Idempotent (audit R2): strip any label already present before
				// prepending. A snapshot-seeded corpus (M1) can arrive already
				// enriched, and this warm pass runs on every boot — without the
				// strip the label compounds one line per restart, re-breaking
				// the byte-fidelity invariant §10 restored. reassigns the copy,
				// not w.chunks[i].
				newChunks[i].Text = label + stripEnrichLabel(newChunks[i].Text)
			}
			if needEmbed {
				newVecs[i] = encodeCached(w.fsOpts.EmbedCache, w.model, newChunks[i].Text)
			}
		}
	}

	w.chunks = newChunks
	w.vecs = newVecs
	w.mode = targetMode     // upgrade bm25 → hybrid when staged; unchanged otherwise
	w.stagedPending = false // vecs is now full-length: appendFile/refold may embed (audit N1)
	w.ix.Store(w.buildUnionedIndexLocked())
	w.notifySwap()
	// Reuse the flush notification so ken-mcp's OnFlush re-persists the now-full
	// snapshot (M1) — a boot after this is a clean full-quality load.
	w.notifyFlush(len(w.chunks)+len(w.extraChunks), len(byFile), 0, time.Since(start))
}

// Warming reports whether the semantic arm is still being built in the
// background (M4 staged embedding): the served index is BM25 but will upgrade
// to the configured mode. False when not staging, or once the upgrade lands.
// realMode is set once at construction (before any goroutine), so it's safe to
// read; the served mode is an atomic Load.
func (w *WatchedIndex) Warming() bool {
	if w.realMode == ModeBM25 {
		return false
	}
	ix := w.Load()
	return ix == nil || ix.Mode() != w.realMode
}

// ReconcileFiles applies a boot-time drift batch to a snapshot-seeded index
// (cold-start M1 Increment 2): `changed` files (added or modified since the
// snapshot) are re-chunked / enriched / embedded and their old chunks
// replaced; `deleted` files have their chunks dropped. Only these files are
// touched — unchanged files keep the chunks + vectors loaded from the
// snapshot, so a small edit doesn't re-index the whole tree.
//
// It reuses the fsnotify flush path verbatim (tombstone → append → compact →
// rebuild → publish) by synthesizing the equivalent event batch, so a
// boot reconcile and a live edit take the identical, already-tested code.
// Safe to call after the watcher has started (flush holds corpusMu). No-op
// when there's nothing to reconcile.
func (w *WatchedIndex) ReconcileFiles(changed, deleted []string) {
	if len(changed)+len(deleted) == 0 {
		return
	}
	batch := make(map[string]fsnotify.Op, len(changed)+len(deleted))
	for _, f := range changed {
		batch[f] = fsnotify.Write // tombstone existing (if any) + re-append
	}
	for _, f := range deleted {
		batch[f] = fsnotify.Remove // tombstone only
	}
	w.flush(batch, nil) // explicit batch, not an overflow resync
}

// flush rebuilds the snapshot from the current corpus state plus the
// batched dirty events. Called from the debouncer goroutine only.
// resyncPresent is non-nil only after an event-queue-overflow resync
// (audit R4-6): it is the disk truth, and any indexed file absent from it
// AND untouched by this batch is tombstoned as vanished-while-events-dropped.
func (w *WatchedIndex) flush(batch map[string]fsnotify.Op, resyncPresent map[string]bool) {
	start := time.Now()
	w.corpusMu.Lock()
	compacted, changed := w.reconcileCorpusLocked(batch, resyncPresent)
	// No-op batch (audit R4-3): every event resolved to a path we never indexed
	// — a stray gitignored .swp/.orig Remove inside a watched dir, an editor
	// touching a binary/oversized file. Skip the whole expensive tail (full BM25
	// + ANN rebuild, and in ken-mcp the OnFlush = whole-repo WalkFS + serialize +
	// two fsync'd writes + FreeOSMemory). The published snapshot is unchanged, so
	// nothing to swap or notify. A real rename still tombstones, so N2 holds.
	if !changed && compacted == 0 {
		w.corpusMu.Unlock()
		return
	}
	newIx := w.buildUnionedIndexLocked()
	w.ix.Store(newIx)
	total := len(w.chunks) + len(w.extraChunks)
	// Unlock BEFORE the notify callbacks (audit R8): notifyFlush is
	// ken-mcp's OnFlush = writeSnapshot — a whole-repo WalkFS + Stat, a full
	// serialize, two disk writes, and a stop-the-world FreeOSMemory. Holding
	// corpusMu across it blocks every corpusMu reader (SetExtraChunks, and
	// the loop's own isTrackedRel before its R8 lock-free fix) for the entire
	// persist. The published snapshot is already Stored, so the callbacks
	// need no lock.
	w.corpusMu.Unlock()
	w.notifySwap()
	w.notifyFlush(total, len(batch), compacted, time.Since(start))
}

// reconcileCorpusLocked applies a batch of file events to the mutable corpus
// (tombstone/append per file + migration refold + compact) and returns the
// number of tombstones compacted away plus whether the batch actually changed
// the corpus. changed is false when every event was a no-op — e.g. a stray
// Remove of a never-indexed path (a .swp/.orig gitignored file inside a watched
// dir), which the unconditional Remove-accept gate now lets through (audit N2)
// but which must not trigger flush's full rebuild + snapshot persist (audit
// R4-3). It does NOT rebuild or publish the Index — the caller runs
// buildUnionedIndexLocked + publish. Shared by flush (which publishes after)
// and the snapshot-seeded reconcile constructor (which runs it BEFORE the
// single initial publish, avoiding a seed-then-reconcile double build). Caller
// holds corpusMu (or is single-threaded construction).
func (w *WatchedIndex) reconcileCorpusLocked(batch map[string]fsnotify.Op, resyncPresent map[string]bool) (int, bool) {
	// Copy-on-write (audit search §1): BuildIndex stores the chunks slice
	// header verbatim, so the currently-published *Index aliases w.chunks'
	// backing array — and readers touch chunk[i].Tombstoned lock-free
	// (tombstoneCount/ResolveChunk). tombstoneFile below writes that field in
	// place, which would be an unsynchronized read/write on the live snapshot
	// (data race, and the file's chunks vanish from results mid-flush). Clone
	// first so every mutation here lands on a fresh array; the old array the
	// published snapshot serves from is never touched (ARCHITECTURE.md
	// invariant #2). Cheap next to the whole-corpus BM25 rebuild this flush
	// already does. (At construction — NewWatchedIndexReconciled — nothing is
	// published yet, so the clone is a harmless no-op-cost.)
	w.chunks = slices.Clone(w.chunks)

	// Migration dirs touched by this batch; we'll re-fold them in one
	// pass after per-file tombstone/append, so an ALTER added in one
	// migration file shows up in the folded chunk for the whole dir.
	touchedMigDirs := map[string]bool{}

	// Iterate the batch in sorted path order (audit §17): appending in Go's
	// randomized map-iteration order gave two processes on the same repo
	// different chunk orders → different BM25 doc IDs → different tie-breaks
	// in rerankTopK, and non-reproducible SnapshotBytes. The build path
	// sorts for exactly this reason; the flush path must too.
	rels := make([]string, 0, len(batch))
	for rel := range batch {
		rels = append(rels, rel)
	}
	sort.Strings(rels)

	changed := false
	for _, rel := range rels {
		op := batch[rel]
		if w.migrationDirs[path.Dir(rel)] {
			touchedMigDirs[path.Dir(rel)] = true
		}
		if op&(fsnotify.Remove|fsnotify.Rename) != 0 {
			if w.tombstoneFile(rel) > 0 {
				changed = true
			}
			continue
		}
		// WRITE or CREATE: tombstone existing chunks for this file,
		// then re-chunk + re-embed and append.
		if w.tombstoneFile(rel) > 0 {
			changed = true
		}
		if w.appendFile(rel) > 0 {
			changed = true
		}
	}

	// Re-fold every touched migration dir (sorted, same determinism reason).
	// Tombstone the dir's folded chunks first (those live in the chunks
	// slice with File pointing at one of the dir's source files); then
	// re-walk the dir on disk via sql.FoldMigrations and append the fresh
	// folded chunks.
	migDirs := make([]string, 0, len(touchedMigDirs))
	for d := range touchedMigDirs {
		migDirs = append(migDirs, d)
	}
	sort.Strings(migDirs)
	for _, d := range migDirs {
		if w.tombstoneFoldedChunksForDir(d) > 0 {
			changed = true
		}
		if w.refoldMigrationDir(d) > 0 {
			changed = true
		}
	}

	// Test hook (nil in production): fire while the just-appended chunks are in
	// w.chunks but the caller hasn't published — the in-flight window N2 covers.
	// The hook inspects the batch and returns true to consume itself, so a test
	// can skip earlier (e.g. startup-Create) flushes and act only on its own.
	// Cleared under corpusMu (held here) so the next flush proceeds.
	if w.onReconcileAppended != nil {
		if w.onReconcileAppended(batch) {
			w.onReconcileAppended = nil
		}
	}

	// Overflow-resync deletion pass (audit R4-6): tombstone every indexed file
	// that is absent from disk (`resyncPresent`) AND untouched by this batch.
	// Reading w.chunks here — under corpusMu, and after the in-flight flush has
	// completed (flushes are single-flighted) — is what makes this catch a file
	// the previous flush appended but hadn't published when the overflow ate its
	// Remove; the old event-loop pass against the published snapshot could not.
	// The "not in batch" guard protects a file created after the disk walk whose
	// Create arrived normally and is being re-appended this same flush.
	if resyncPresent != nil {
		for i := range w.chunks {
			if w.chunks[i].Tombstoned {
				continue
			}
			f := w.chunks[i].File
			if resyncPresent[f] {
				continue
			}
			if _, inBatch := batch[f]; inBatch {
				continue
			}
			w.chunks[i].Tombstoned = true
			changed = true
		}
	}

	return w.compactCorpus(), changed
}

// tombstoneFoldedChunksForDir marks every chunk whose File lives inside
// migration directory `dir` AND whose Text identifies it as a folded
// chunk ("-- folded from migrations" marker emitted by
// renderFoldedTableChunk). Caller holds corpusMu.
//
// We can't tombstone every chunk in dir indiscriminately: line-chunked
// raw .sql content for files in the dir is owned by per-file appendFile
// and has already been re-emitted by the caller's per-file tombstone +
// re-append. Only the folded chunks need this dir-wide refresh.
func (w *WatchedIndex) tombstoneFoldedChunksForDir(dir string) int {
	const marker = "-- folded from migrations"
	n := 0
	for i := range w.chunks {
		if w.chunks[i].Tombstoned {
			continue
		}
		if path.Dir(w.chunks[i].File) != dir {
			continue
		}
		if strings.Contains(w.chunks[i].Text, marker) {
			w.chunks[i].Tombstoned = true
			n++
		}
	}
	return n
}

// refoldMigrationDir re-runs sql.FoldMigrations against the migration
// directory `dir` on disk and appends the resulting chunks. Caller
// holds corpusMu. Read errors / fold errors are logged via the FSOptions
// LogWriter (nil discards) and the dir keeps the previously-tombstoned
// folded chunks invalidated — net: the snapshot reflects "best effort"
// with no stale folded chunk surviving the flush.
func (w *WatchedIndex) refoldMigrationDir(dir string) int {
	folded, err := sql.FoldMigrations(os.DirFS(w.root), dir, w.fsOpts.LogWriter)
	if err != nil {
		if w.fsOpts.LogWriter != nil {
			fmt.Fprintf(w.fsOpts.LogWriter, "search: WatchedIndex.refoldMigrationDir(%q): %v\n", dir, err)
		}
		return 0
	}
	for _, c := range folded {
		w.chunks = append(w.chunks, c)
		// Same staged guard as appendFile (audit N1): don't grow vecs while
		// pre-warm, or it misaligns with chunks and compactCorpus panics.
		if w.model != nil && !w.stagedPending {
			w.vecs = append(w.vecs, encodeCached(w.fsOpts.EmbedCache, w.model, c.Text))
		}
	}
	return len(folded)
}

// buildUnionedIndexLocked constructs the published Index from the union
// of FS chunks (w.chunks/w.vecs) and the orchestrator-injected extra
// chunks (w.extraChunks/w.extraVecs). Caller MUST hold corpusMu.
//
// The concatenation order is "FS first, extras second" so a chunk's
// index inside the published Index is stable for FS chunks across
// snapshot republishes that only changed the extras — important for
// any reader holding chunk indices across calls (none currently, but
// the invariant is cheap to preserve).
//
// M5: re-applies the watched-level reranker (if any) to every newly
// built snapshot. The reranker instance is shared across rebuilds so
// its content-hash LRU cache carries forward.
func (w *WatchedIndex) buildUnionedIndexLocked() *Index {
	// Parallel-slice invariant, checked wherever the corpus is assembled — not
	// only at first build (audit N1/R6): w.vecs is empty (BM25 or staged
	// pre-warm) or exactly one-per-chunk. A violation would otherwise surface
	// as an opaque makeslice/index panic in compactCorpus on the flush
	// goroutine, killing the process. Degrade to BM25 for this snapshot + log,
	// rather than crash. With the N1 append-site guards this should never fire.
	if len(w.vecs) != 0 && len(w.vecs) != len(w.chunks) {
		w.logf("BUG: vecs/chunks misaligned (vecs=%d chunks=%d); dropping vectors, serving BM25 for this snapshot", len(w.vecs), len(w.chunks))
		w.vecs = nil
	}
	if w.tokens == nil {
		w.tokens = newTokenCache()
	}
	var ix *Index
	if len(w.extraChunks) == 0 {
		docs := tokenizeDocs(w.chunks, w.tokens)
		ix = buildIndexFromDocs(w.chunks, docs, w.vecs, w.mode, w.model)
	} else {
		merged := make([]chunk.Chunk, 0, len(w.chunks)+len(w.extraChunks))
		merged = append(merged, w.chunks...)
		merged = append(merged, w.extraChunks...)
		var mergedVecs [][]float32
		if w.vecs != nil || w.extraVecs != nil {
			mergedVecs = make([][]float32, 0, len(w.vecs)+len(w.extraVecs))
			mergedVecs = append(mergedVecs, w.vecs...)
			mergedVecs = append(mergedVecs, w.extraVecs...)
		}
		// The w.vecs/w.chunks check above passes vacuously during the staged
		// window (len(w.vecs)==0), but SetExtraChunks embeds extras
		// unconditionally on w.model!=nil — so the merged pair can be N+E chunks
		// against only E vectors (audit R5-1). That misalignment doesn't panic
		// (indices stay in range) but silently ranks FS chunks by DB-chunk
		// vectors in FindRelated / semantic search until the warm pass
		// republishes. Re-check the assembled result and degrade to BM25 rather
		// than serve confidently-wrong hits. Gating SetExtraChunks on
		// !stagedPending instead is NOT a fix: the warm pass doesn't re-embed
		// extras, so they'd stay vector-less forever.
		if len(mergedVecs) != 0 && len(mergedVecs) != len(merged) {
			w.logf("BUG: merged vecs/chunks misaligned (vecs=%d chunks=%d); dropping vectors, serving BM25 for this snapshot", len(mergedVecs), len(merged))
			mergedVecs = nil
		}
		// FS chunks reuse the cache (the common, per-save path); the
		// extras (Tier-2 DB chunks) are tokenized fresh — they change on
		// their own refresh cadence and caching them would only add map
		// churn. Concatenated FS-first so indices stay stable (matching
		// the merged chunk order above).
		docs := make([][]string, 0, len(merged))
		docs = append(docs, tokenizeDocs(w.chunks, w.tokens)...)
		docs = append(docs, tokenizeDocs(w.extraChunks, nil)...)
		ix = buildIndexFromDocs(merged, docs, mergedVecs, w.mode, w.model)
	}
	if w.reranker != nil {
		ix.reranker = w.reranker
		ix.rerankCfg = w.rerankCfg
	}
	return ix
}

// SetReranker attaches a neural reranker to this WatchedIndex. The
// reranker is re-applied to every newly published snapshot (live
// flushes, SetExtraChunks rebuilds, etc.) so its content-hash LRU
// cache survives snapshot swaps — the per-snapshot cost is zero, only
// the field is re-pointed.
//
// Pass nil to detach. ken-mcp calls this once at startup with the
// boot-time NeuralReranker; production code does NOT call this from
// hot paths because it takes the corpus lock briefly.
//
// Goroutine-safe; serializes against the flush path via corpusMu.
func (w *WatchedIndex) SetReranker(r Reranker, opts ...RerankerOption) {
	w.corpusMu.Lock()
	defer w.corpusMu.Unlock()
	w.reranker = r
	if r == nil {
		w.rerankCfg = rerankerConfig{}
	} else {
		w.rerankCfg = defaultRerankerConfig
		for _, o := range opts {
			o(&w.rerankCfg)
		}
	}
	// Apply to the currently-published snapshot immediately (without
	// waiting for the next flush) — but via the atomic swap, NOT by
	// mutating the live *Index in place (M4, code review §3). Lock-free
	// Search readers dereference ix.reranker / ix.rerankCfg with no lock,
	// so writing them on the published value is a data race: a torn 2-word
	// interface read can pair a new itab with a stale data word and crash
	// on method dispatch. Index has no lock fields and all its referenced
	// data (bm/flat/model/chunks/vecs) is immutable after construction, so
	// a shallow copy safely shares that data; only the fresh pointer carries
	// the new reranker, and Store makes readers see old-or-new, never a
	// half-written field. (Cheaper than buildUnionedIndexLocked's full
	// BuildIndex rebuild for a field-only change.)
	if cur := w.ix.Load(); cur != nil {
		next := *cur
		next.reranker = r
		next.rerankCfg = w.rerankCfg
		w.ix.Store(&next)
	}
}

// SetExtraChunks replaces the orchestrator-injected extra chunks and
// publishes a new snapshot. Called by cmd/ken-mcp's db.Refresher swap
// callback whenever Tier-2 DB introspection produces a fresh chunk set.
//
// Calling with chunks==nil (or empty) is the canonical "DB unreachable,
// clear the DB chunks" path — the swap callback always fires so the
// published snapshot reflects the latest known state from each source.
//
// Goroutine-safe: serialized via corpusMu against the debouncer's flush
// path. Embedding (when model != nil) happens inside the lock; for
// large DB snapshots this can block fsnotify flushes briefly. That's
// acceptable — DB refreshes are infrequent (startup, periodic ≥1m,
// SIGHUP) and embedding cost is bounded by chunk count.
func (w *WatchedIndex) SetExtraChunks(chunks []chunk.Chunk) {
	// A static (pre-built) index has no live corpus in w.chunks/w.vecs —
	// the snapshot was loaded from serialized bytes. buildUnionedIndexLocked
	// would rebuild from an empty corpus ∪ extras, silently dropping the
	// entire loaded index. Guard against that: pre-built + DB-Tier-2 is an
	// unsupported combination (bake DB chunks in at `ken build-index` time
	// instead), so the right behavior is to keep serving the loaded index
	// and ignore the extras rather than corrupt the snapshot.
	if w.static {
		return
	}

	w.corpusMu.Lock()
	defer w.corpusMu.Unlock()

	w.extraChunks = chunks
	if w.model != nil && len(chunks) > 0 {
		vecs := make([][]float32, len(chunks))
		for i, c := range chunks {
			vecs[i] = encodeCached(w.fsOpts.EmbedCache, w.model, c.Text)
		}
		w.extraVecs = vecs
	} else {
		w.extraVecs = nil
	}
	w.ix.Store(w.buildUnionedIndexLocked())
	w.notifySwap()
}

// compactCorpus drops tombstoned chunks (and their parallel vecs slots)
// from the writer-side corpus state, allocating fresh backing slices so
// any previously-published *Index that references the old slices stays
// unmodified. Returns the number of tombstones dropped — zero when
// there's nothing to compact. Caller holds corpusMu.
//
// Unconditional: runs on every flush. The iteration is already paid by
// BuildIndex below; an unconditional rule has no failure mode where
// compaction silently never triggers. See ADR-012.
func (w *WatchedIndex) compactCorpus() int {
	dropped := 0
	for i := range w.chunks {
		if w.chunks[i].Tombstoned {
			dropped++
		}
	}
	if dropped == 0 {
		return 0
	}
	newChunks := make([]chunk.Chunk, 0, len(w.chunks)-dropped)
	// Defensive: if vecs and chunks ever fall out of alignment (the R4-1 class:
	// a stagedPending append that skipped its vector), drop the vectors entirely
	// and degrade to BM25 rather than index w.vecs[i] out of range and panic on
	// the flush/startup goroutine. buildUnionedIndexLocked's invariant check
	// then republishes as BM25. Aligned corpora take the fast path unchanged.
	var newVecs [][]float32
	if len(w.vecs) == len(w.chunks) {
		newVecs = make([][]float32, 0, len(w.vecs)-dropped)
	} else if len(w.vecs) != 0 {
		w.logf("BUG: compactCorpus vecs/chunks misaligned (%d vecs, %d chunks); dropping vectors, degrading to BM25", len(w.vecs), len(w.chunks))
		w.vecs = nil
	}
	for i, c := range w.chunks {
		if c.Tombstoned {
			continue
		}
		newChunks = append(newChunks, c)
		if newVecs != nil {
			newVecs = append(newVecs, w.vecs[i])
		}
	}
	w.chunks = newChunks
	w.vecs = newVecs
	return dropped
}

// readCappedFile reads abs but refuses anything larger than the matcher's
// size ceiling (audit search §15): even after ShouldIndex's Lstat passes,
// the file could grow before this read (TOCTOU), so bound it with a
// LimitReader at cap+1 and reject if the extra byte materializes. Returns
// the bytes, or an error the caller treats as "skip this file".
func (w *WatchedIndex) readCappedFile(abs string) ([]byte, error) {
	cap := int64(repo.DefaultMaxFileBytes)
	if w.matcher != nil {
		cap = w.matcher.MaxFileBytes()
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, cap+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > cap {
		return nil, fmt.Errorf("search: %s exceeds max file bytes (%d)", abs, cap)
	}
	return data, nil
}

// tombstoneFile marks every existing chunk whose File == rel as
// Tombstoned. Caller holds corpusMu.
// Returns the number of chunks newly tombstoned — 0 means this file/dir had
// nothing indexed, which lets reconcileCorpusLocked skip the expensive rebuild
// for a stray Remove of an unindexed path (audit R4-3).
func (w *WatchedIndex) tombstoneFile(rel string) int {
	// Match the exact file OR — for a directory Remove/Rename, which fires no
	// per-file events (audit §14) — every chunk under rel+"/". The "/" boundary
	// keeps `internal/auth` from matching `internal/authz/…`. A regular file
	// remove (rel="a.go") has an empty prefix match, so this is a no-op beyond
	// the exact case.
	prefix := rel + "/"
	n := 0
	for i := range w.chunks {
		if w.chunks[i].Tombstoned {
			continue
		}
		if w.chunks[i].File == rel || strings.HasPrefix(w.chunks[i].File, prefix) {
			w.chunks[i].Tombstoned = true
			n++
		}
	}
	return n
}

// appendFile re-reads, re-chunks, and re-embeds `rel`, appending the
// resulting chunks and (if semantic/hybrid) vecs to the corpus state.
// A read error silently drops the file — by the time flush runs, the
// file may have been deleted again; we already tombstoned the old
// chunks, so falling through to "no new chunks" is the correct outcome.
// Caller holds corpusMu.
//
// v0.7.1: when rel lives in a migration directory, the SQL structural
// chunks are produced by the post-pass refoldMigrationDir rather than
// per-file — so skipSQLStructural is set to true here. Line-chunked
// raw text still flows through chunk.ChunkFile.
// Returns the number of chunks appended — 0 means the file was rejected at
// admission, unreadable, or produced no chunks, letting reconcileCorpusLocked
// skip a no-op rebuild (audit R4-3).
func (w *WatchedIndex) appendFile(rel string) int {
	// Re-check admission at flush time (audit §15): ShouldIndex (with its
	// size + binary-sniff guards) ran at EVENT time, up to a debounce window
	// ago. A file that was 1 KB when its Create fired can be 800 MB by now
	// (a build artifact / growing log), or have turned binary. Re-running
	// ShouldIndex here re-Lstats and rejects it.
	if w.matcher != nil && !w.matcher.ShouldIndex(rel) {
		return 0
	}
	abs := filepath.Join(w.root, filepath.FromSlash(rel))
	data, err := w.readCappedFile(abs)
	if err != nil {
		return 0
	}
	skipSQLStructural := w.migrationDirs[path.Dir(rel)]
	cs, err := chunkOneFile(w.chunkerName, rel, data, skipSQLStructural)
	if err != nil {
		return 0
	}
	// M3 (code review §3): apply the SAME Arm B enrichment the initial build
	// does, gated on the same flag, BEFORE embedding — otherwise every file
	// edited during a watch session is re-indexed without the func:/calls:/
	// raises: label, and its BM25 tokens + embedding diverge from a fresh
	// build, degrading the index as edits accumulate.
	enrichChunks(rel, data, cs, w.fsOpts.DisableEnrichment)
	for _, c := range cs {
		w.chunks = append(w.chunks, c)
		// Skip embedding while a staged build is still pre-warm (audit N1):
		// w.vecs must stay empty until the warm pass fills it, or vecs and
		// chunks misalign and compactCorpus panics. The warm pass re-embeds
		// every chunk (including these) and clears stagedPending.
		if w.model != nil && !w.stagedPending {
			w.vecs = append(w.vecs, encodeCached(w.fsOpts.EmbedCache, w.model, c.Text))
		}
	}
	return len(cs)
}
