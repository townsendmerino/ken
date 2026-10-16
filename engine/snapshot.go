package engine

// snapshot.go — ADR-039 (cold-start M1) wiring, ported from
// cmd/ken-mcp/snapshot.go so Open gets the same load-if-clean-else-rebuild
// behavior ken-mcp already shipped, instead of a second copy that could
// drift from it. See search.WriteSnapshot / search.SnapshotConfigKey for
// what's actually keyed (and, importantly, what isn't — size caps and
// ignore rules are caught by file-set drift, not the config key).
//
// Any snapshot problem — missing, corrupt, config mismatch, drift — is a
// cache-miss that falls back to a live build; Open never fails because a
// snapshot was unusable.

import (
	"os"

	"github.com/townsendmerino/aikit/embed"
	"github.com/townsendmerino/ken/internal/search"
)

// snapshotConfigKey builds the invalidation key for the workspace's current
// config. See search.SnapshotConfigKey for the exact fields it covers.
func snapshotConfigKey(mode search.Mode, chunker string, model *embed.StaticModel, modelDir string, fsOpts search.FSOptions) string {
	return search.SnapshotConfigKey(mode, chunker, search.ModelFingerprint(model, modelDir),
		!fsOpts.DisableEnrichment, fsOpts.LazyEnrichment, fsOpts.StagedEmbedding)
}

// currentManifest walks root and stamps each file, swallowing a walk error
// into an empty manifest — which won't match a non-empty stored one, so a
// walk failure is conservative (forces a rebuild) rather than silently
// trusting a stale snapshot.
func currentManifest(root, configKey string) search.SnapshotManifest {
	m, _ := search.CurrentManifest(root, configKey)
	return m
}

// tryLoadSnapshot is the M1 fast path: if <root>/.ken/snapshot.{manifest,bin}
// exist, the config-key matches, and the tree hasn't drifted too far, load
// the serialized corpus and seed a *watching* index from it — no
// walk/chunk/enrich/embed. Returns nil on ANY miss; the caller then builds
// live.
func (w *Workspace) tryLoadSnapshot(modeStr string) *search.WatchedIndex {
	manData, err := os.ReadFile(search.SnapshotManifestPath(w.root))
	if err != nil {
		return nil // no sidecar → first run, or not an M1-managed repo
	}
	stored, err := search.DecodeManifest(manData)
	if err != nil {
		w.logf("engine: snapshot manifest %s unreadable (%v); rebuilding", search.SnapshotManifestPath(w.root), err)
		return nil
	}

	// Load the model up front — needed both to fingerprint the config-key
	// and to seed the index (query-time encoding). BM25 needs none.
	var model *embed.StaticModel
	if w.mode != search.ModeBM25 {
		m, mErr := search.LoadModelDir(w.modelDir, w.fsOpts.MmapModel)
		if mErr != nil {
			w.logf("engine: snapshot needs a model but loading %q failed (%v); rebuilding", w.modelDir, mErr)
			return nil
		}
		model = m
	}

	wantKey := snapshotConfigKey(w.mode, w.chunker, model, w.modelDir, w.fsOpts)
	if stored.ConfigKey != wantKey {
		w.logf("engine: snapshot config changed for %s (mode/chunker/model/enrich); rebuilding", w.root)
		return nil
	}

	// Compute the drift set. A large fraction of the corpus changing is
	// cheaper to rebuild from scratch than to reconcile file-by-file.
	changed, deleted := stored.Diff(currentManifest(w.root, wantKey))
	drift := len(changed) + len(deleted)
	if drift > 0 && (len(stored.Files) == 0 || drift*2 > len(stored.Files)) {
		w.logf("engine: %s drifted heavily since snapshot (%d/%d files changed); full rebuild",
			w.root, drift, len(stored.Files))
		return nil
	}

	binData, err := os.ReadFile(search.SnapshotBinPath(w.root))
	if err != nil {
		w.logf("engine: snapshot manifest present but %s missing (%v); rebuilding", search.SnapshotBinPath(w.root), err)
		return nil
	}
	// Corpus-only load: return the raw chunks/vecs and let
	// NewWatchedIndexReconciled do the single BM25/ANN build, instead of
	// LoadSerializedIndex building a throwaway Index we'd discard.
	chunks, vecs, err := search.LoadSerializedCorpus(binData, search.LoadOptions{
		ExpectedMode:    modeStr,
		ExpectedChunker: w.chunker,
		Model:           model,
	})
	if err != nil {
		w.logf("engine: snapshot %s unusable (%v); rebuilding", search.SnapshotBinPath(w.root), err)
		return nil
	}
	// Single-publish: seed + reconcile the drift BEFORE the initial build,
	// so only one BM25/ANN build runs (not seed-then-reconcile's two). When
	// drift is empty this is a plain snapshot seed (the clean-load fast
	// path).
	wi, err := search.NewWatchedIndexReconciled(w.root, w.mode, w.chunker, w.modelDir, model, chunks, vecs, changed, deleted, w.watch, w.fsOpts)
	if err != nil {
		w.logf("engine: seeding index from snapshot %s failed (%v); rebuilding", w.root, err)
		return nil
	}

	if drift == 0 {
		w.logf("engine: loaded snapshot for %s (%d chunks, no drift) — skipped rebuild", w.root, len(chunks))
		return wi
	}
	// Re-persist so the on-disk snapshot matches the reconciled state (the
	// next Open is a clean load). Best-effort. Skip while the index is
	// genuinely still warming (a staged pre-warm publishes vector-less BM25
	// with the real mode held back; persisting now would stamp a snapshot
	// with the wrong served mode) — this reconcile path arrives with vecs
	// already full from LoadSerializedCorpus, so Warming() is the correct
	// gate, not the raw StagedEmbedding option.
	w.logf("engine: reconciled snapshot for %s: %d changed/added, %d deleted (of %d snapshot files) — skipped full rebuild",
		w.root, len(changed), len(deleted), len(stored.Files))
	if !w.fsOpts.LazyEnrichment && !wi.Warming() {
		w.persistSnapshot(wi)
	}
	return wi
}

// persistSnapshot writes wi's published corpus + drift manifest to
// <root>/.ken/ via search.WriteSnapshot. Best-effort: a failure logs and
// returns — the workspace keeps running on its in-memory index.
func (w *Workspace) persistSnapshot(wi *search.WatchedIndex) {
	key := snapshotConfigKey(w.mode, w.chunker, wi.EmbedModel(), w.modelDir, w.fsOpts)
	if err := search.WriteSnapshot(w.root, wi, key); err != nil {
		w.logf("engine: persisting snapshot for %s failed (%v); continuing with in-memory index", w.root, err)
		return
	}
	w.logf("engine: wrote snapshot for %s", w.root)
}
