package engine

import (
	"io/fs"

	"github.com/townsendmerino/aikit/embed"

	"github.com/townsendmerino/ken/internal/repo"
	"github.com/townsendmerino/ken/internal/search"
)

// serialize.go — the on-disk forms of an index, for the callers that write
// or inspect one rather than query it: `ken build-index`, `ken index
// --write-snapshot` and `ken doctor`. Everything here is best-effort,
// outside the stability promise.

// BuildIndexOptions configures BuildIndexBytes.
type BuildIndexOptions struct {
	// Mode and Chunker are as in Options; neither has a default here.
	Mode    Mode
	Chunker string

	// Model is the embedding model, required for every Mode but ModeBM25.
	Model *embed.StaticModel

	// DisableEnrichment builds without the Arm B enrichment label.
	DisableEnrichment bool

	// MaxFileBytes, MaxAvgLineBytes, MaxFiles and SkipGenerated are the
	// walk limits, as in Options.
	MaxFileBytes    int64
	MaxAvgLineBytes int
	MaxFiles        int
	SkipGenerated   bool
}

// BuildIndexBytes builds an index over fsys and returns it serialized: the
// pre-built index format (ADR-024) that Options.Prebuilt-style loaders and
// mcp.Run read. The output is a pure function of the corpus and the
// options, byte for byte, as long as no parse budget is set (ADR-040); it
// stores full-precision vectors, so there is no dense-retriever option.
func BuildIndexBytes(fsys fs.FS, opts BuildIndexOptions) ([]byte, error) {
	return search.BuildAndSerializeIndex(fsys, search.BuildOptions{
		Mode:              opts.Mode,
		Chunker:           opts.Chunker,
		Model:             opts.Model,
		DisableEnrichment: opts.DisableEnrichment,
		Walk: repo.Options{
			MaxFileBytes:    opts.MaxFileBytes,
			MaxAvgLineBytes: opts.MaxAvgLineBytes,
			MaxFiles:        opts.MaxFiles,
			SkipGenerated:   opts.SkipGenerated,
		},
	})
}

// WriteSnapshot writes this Workspace's index to <root>/.ken/ now, in the
// snapshot format a later Open with Options.Snapshot loads (ADR-039). It
// works whether or not Options.Snapshot is set, and returns the write
// error instead of logging it, for a caller whose whole job is to produce
// the snapshot (`ken index --write-snapshot`).
func (w *Workspace) WriteSnapshot() error {
	key := snapshotConfigKey(w.mode, w.chunker, w.index.EmbedModel(), w.modelDir, w.fsOpts)
	return search.WriteSnapshot(w.root, w.index, key)
}

// InspectSnapshot reports how many chunks the snapshot under root holds,
// reading only its header. ok is false when there is no readable snapshot.
func InspectSnapshot(root string) (chunks int, ok bool) {
	return search.PeekSnapshotChunks(search.SnapshotBinPath(root))
}
