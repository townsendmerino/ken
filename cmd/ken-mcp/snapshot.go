package main

// snapshot.go — cold-start M1 (ADR-039) ken-mcp wiring: persist the built
// index to <repo>/.ken/ and, on boot, load it + drift-scan instead of
// rebuilding when the repo is unchanged.
//
// This is a DISTINCT artifact from the ADR-024 operator prebuilt
// (<repo>/.ken/index.bin, loaded frozen/non-watching by loadOrBuildWatched's
// top branch). M1 writes <repo>/.ken/snapshot.bin + snapshot.manifest and
// seeds a *watching* index, so the two coexist: an operator-placed index.bin
// still wins (that branch returns before this one), and this layer only
// engages on the live-build fall-through.
//
// Increment 1: load-if-clean-else-full-rebuild. Increment 2 (later) replaces
// the full rebuild on drift with an incremental reconcile of only the changed
// files. Any snapshot problem — missing, corrupt, config mismatch, drift — is
// a cache-miss that falls back to the live build; it never fails the server.

import (
	"os"
	"path/filepath"

	"github.com/townsendmerino/aikit/embed"
	"github.com/townsendmerino/ken/internal/repo"
	"github.com/townsendmerino/ken/internal/search"
	kenmcp "github.com/townsendmerino/ken/mcp"
)

const (
	snapshotBinName      = "snapshot.bin"
	snapshotManifestName = "snapshot.manifest"
)

func snapshotBinPath(dir string) string      { return filepath.Join(dir, ".ken", snapshotBinName) }
func snapshotManifestPath(dir string) string { return filepath.Join(dir, ".ken", snapshotManifestName) }

// snapshotConfigKey builds the invalidation key for dir's current server
// config. See search.SnapshotConfigKey for what's keyed (and, importantly,
// what isn't — size caps / ignore rules are caught by drift, not the key).
func snapshotConfigKey(mode search.Mode, chunker string, model *embed.StaticModel, modelDir string, fsOpts search.FSOptions) string {
	return search.SnapshotConfigKey(mode, chunker, search.ModelFingerprint(model, modelDir), !fsOpts.DisableEnrichment)
}

// currentManifest walks dir the SAME way the index build does
// (repo.WalkFS with the default options) and stamps each file, so the drift
// check compares like with like. The walker prunes .ken/, so the snapshot's
// own files never appear here (no self-drift). A walk error yields an empty
// manifest, which won't match a non-empty stored one → conservative rebuild.
func currentManifest(dir, configKey string) search.SnapshotManifest {
	files, err := repo.WalkFS(os.DirFS(dir), repo.Options{})
	if err != nil {
		return search.SnapshotManifest{ConfigKey: configKey}
	}
	return search.SnapshotManifest{ConfigKey: configKey, Files: search.BuildFileStamps(os.DirFS(dir), files)}
}

// tryLoadSnapshot is the M1 fast path: if <dir>/.ken/snapshot.{manifest,bin}
// exist, the config-key matches, and the tree hasn't drifted, load the
// serialized corpus and seed a *watching* index from it — no
// walk/chunk/enrich/embed. Returns nil on ANY miss (caller then live-builds).
func tryLoadSnapshot(dir string, mode search.Mode, modeStr, chunker, modelDir string, fsOpts search.FSOptions, logger *kenmcp.Logger) *search.WatchedIndex {
	manData, err := os.ReadFile(snapshotManifestPath(dir))
	if err != nil {
		return nil // no sidecar → first run, or not an M1-managed repo
	}
	stored, err := search.DecodeManifest(manData)
	if err != nil {
		logger.Logf(kenmcp.LogWarn, "snapshot manifest %s unreadable (%v); rebuilding", snapshotManifestPath(dir), err)
		return nil
	}

	// Load the model up front — needed both to fingerprint the config-key and
	// to seed the index (query-time encoding). BM25 needs none.
	var model *embed.StaticModel
	if mode != search.ModeBM25 {
		m, mErr := embed.LoadFromFS(os.DirFS(modelDir), ".")
		if mErr != nil {
			logger.Logf(kenmcp.LogWarn, "snapshot needs a model but loading %q failed (%v); rebuilding", modelDir, mErr)
			return nil
		}
		model = m
	}

	wantKey := snapshotConfigKey(mode, chunker, model, modelDir, fsOpts)
	if stored.ConfigKey != wantKey {
		logger.Logf(kenmcp.LogInfo, "snapshot config changed for %s (mode/chunker/model/enrich); rebuilding", dir)
		return nil
	}
	// Compute the drift set. Increment 2: on drift, reconcile only the
	// changed files instead of a full rebuild — UNLESS the change set is a
	// large fraction of the corpus, where tombstone (O(K·N) over chunks) +
	// partial re-embed approaches or exceeds a clean rebuild's cost.
	changed, deleted := stored.Diff(currentManifest(dir, wantKey))
	drift := len(changed) + len(deleted)
	if drift > 0 && (len(stored.Files) == 0 || drift*2 > len(stored.Files)) {
		logger.Logf(kenmcp.LogInfo, "%s drifted heavily since snapshot (%d/%d files changed); full rebuild",
			dir, drift, len(stored.Files))
		return nil
	}

	binData, err := os.ReadFile(snapshotBinPath(dir))
	if err != nil {
		logger.Logf(kenmcp.LogWarn, "snapshot manifest present but %s missing (%v); rebuilding", snapshotBinPath(dir), err)
		return nil
	}
	// Corpus-only load: return the raw chunks/vecs and let
	// NewWatchedIndexFromSnapshot do the single BM25/ANN build, instead of
	// LoadSerializedIndex building a throwaway Index we'd discard (M1 perf).
	chunks, vecs, err := search.LoadSerializedCorpus(binData, search.LoadOptions{
		ExpectedMode:    modeStr,
		ExpectedChunker: chunker,
		Model:           model,
	})
	if err != nil {
		logger.Logf(kenmcp.LogWarn, "snapshot %s unusable (%v); rebuilding", snapshotBinPath(dir), err)
		return nil
	}
	// Single-publish: seed + reconcile the drift BEFORE the initial build, so
	// only one BM25/ANN build runs (not seed-then-reconcile's two). When drift
	// is empty this is a plain snapshot seed (the clean-load fast path).
	wi, err := search.NewWatchedIndexReconciled(dir, mode, chunker, modelDir, model, chunks, vecs, changed, deleted, true, fsOpts)
	if err != nil {
		logger.Logf(kenmcp.LogWarn, "seeding index from snapshot %s failed (%v); rebuilding", dir, err)
		return nil
	}

	if drift == 0 {
		logger.Logf(kenmcp.LogInfo, "loaded snapshot for %s (%d chunks, no drift) — skipped rebuild, watching", dir, len(chunks))
		return wi
	}
	// Re-persist so the on-disk snapshot matches the reconciled state (next
	// boot is a clean load). Best-effort.
	logger.Logf(kenmcp.LogInfo, "reconciled snapshot for %s: %d changed/added, %d deleted (of %d snapshot files) — skipped full rebuild, watching",
		dir, len(changed), len(deleted), len(stored.Files))
	writeSnapshot(dir, wi, mode, chunker, modelDir, fsOpts, logger)
	return wi
}

// writeSnapshot persists wi's current published corpus + a fresh drift
// manifest to <dir>/.ken/. Best-effort: any failure logs a warning and
// returns — the server keeps running with its in-memory index. Writes the
// .bin BEFORE the manifest so a crash between the two leaves no manifest,
// which boot reads as a clean cache-miss (never a half-loaded index).
func writeSnapshot(dir string, wi *search.WatchedIndex, mode search.Mode, chunker, modelDir string, fsOpts search.FSOptions, logger *kenmcp.Logger) {
	data, err := wi.SnapshotBytes()
	if err != nil {
		logger.Logf(kenmcp.LogWarn, "snapshot: serialize %s failed (%v); not persisting", dir, err)
		return
	}
	key := snapshotConfigKey(mode, chunker, wi.EmbedModel(), modelDir, fsOpts)
	manifest := currentManifest(dir, key)

	if err := os.MkdirAll(filepath.Join(dir, ".ken"), 0o755); err != nil {
		logger.Logf(kenmcp.LogWarn, "snapshot: mkdir %s/.ken failed (%v); not persisting", dir, err)
		return
	}
	if err := atomicWriteFile(snapshotBinPath(dir), data); err != nil {
		logger.Logf(kenmcp.LogWarn, "snapshot: write %s failed (%v); not persisting", snapshotBinPath(dir), err)
		return
	}
	if err := atomicWriteFile(snapshotManifestPath(dir), manifest.Encode()); err != nil {
		logger.Logf(kenmcp.LogWarn, "snapshot: write %s failed (%v); manifest absent → next boot rebuilds", snapshotManifestPath(dir), err)
		return
	}
	logger.Logf(kenmcp.LogInfo, "wrote snapshot for %s (%d files, %d bytes)", dir, len(manifest.Files), len(data))
}

// atomicWriteFile stages to <path>.tmp then renames — a partial write can
// never be observed as the real file.
func atomicWriteFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
