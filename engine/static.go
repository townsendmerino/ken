package engine

import (
	"context"
	"fmt"
	"io/fs"

	"github.com/townsendmerino/aikit/chunk"
	"github.com/townsendmerino/aikit/embed"

	"github.com/townsendmerino/ken/internal/repo"
	"github.com/townsendmerino/ken/internal/search"
)

// static.go — a Workspace over a fixed corpus: one fs.FS, indexed once,
// never watched. It is the shape mcp.Run serves (an embedded corpus,
// optionally with a pre-built index), as opposed to Open's directory that
// can change. Everything here is best-effort, outside the stability
// promise.

// StaticOptions configures OpenFS.
type StaticOptions struct {
	// Mode and Chunker are as in Options; neither has a default here.
	Mode    Mode
	Chunker string

	// Model is the embedding model, already loaded, required for every
	// Mode but ModeBM25.
	Model *embed.StaticModel

	// Prebuilt, when non-nil, is a serialized index (BuildIndexBytes, `ken
	// build-index`) to load instead of walking and indexing the corpus. It
	// must have been built with this Mode and Chunker. If it cannot be
	// used, OpenFS returns a *PrebuiltError and builds nothing; the caller
	// decides whether to call again without it.
	Prebuilt []byte

	// DisableEnrichment and DenseRetriever are as in Options.
	// DisableEnrichment has no effect on a pre-built index, which is
	// enriched or not as it was built.
	DisableEnrichment bool
	DenseRetriever    string

	// MaxFileBytes, MaxAvgLineBytes, MaxFiles and SkipGenerated are the
	// walk limits, as in Options.
	MaxFileBytes    int64
	MaxAvgLineBytes int
	MaxFiles        int
	SkipGenerated   bool
}

// PrebuiltError is OpenFS's error when StaticOptions.Prebuilt cannot be
// used: corrupt, another format version, or built with a different mode or
// chunker. Err says which.
type PrebuiltError struct{ Err error }

// Error implements error.
func (e *PrebuiltError) Error() string { return "engine: pre-built index: " + e.Err.Error() }

// Unwrap returns the cause.
func (e *PrebuiltError) Unwrap() error { return e.Err }

// OpenFS indexes a fixed corpus and returns a Workspace over it. The
// Workspace never watches and has no root directory, so Refresh and
// WriteSnapshot return ErrWrappedIndex, Root is "", and there is no
// structural index. Search, FindRelated and their Results forms, the
// status methods and ReplaceExtraChunks work.
//
// A failed build is returned as it comes from the index build, unwrapped.
func OpenFS(ctx context.Context, fsys fs.FS, opts StaticOptions) (*Workspace, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var (
		ix  *search.Index
		err error
	)
	if opts.Prebuilt != nil {
		ix, err = search.LoadSerializedIndex(opts.Prebuilt, search.LoadOptions{
			ExpectedMode:    search.ModeNames()[int(opts.Mode)],
			ExpectedChunker: opts.Chunker,
			Model:           opts.Model,
			DenseRetriever:  opts.DenseRetriever,
		})
		if err != nil {
			return nil, &PrebuiltError{Err: err}
		}
	} else {
		ix, err = search.FromFSWithModelOptions(fsys, opts.Mode, opts.Chunker, opts.Model, search.FSOptions{
			DisableEnrichment: opts.DisableEnrichment,
			DenseRetriever:    opts.DenseRetriever,
			Walk: repo.Options{
				MaxFileBytes:    opts.MaxFileBytes,
				MaxAvgLineBytes: opts.MaxAvgLineBytes,
				MaxFiles:        opts.MaxFiles,
				SkipGenerated:   opts.SkipGenerated,
			},
		})
		if err != nil {
			return nil, err
		}
	}
	w := &Workspace{
		mode:       opts.Mode,
		chunker:    opts.Chunker,
		index:      search.WrapStatic(ix, "", opts.Mode, opts.Chunker),
		wrapped:    true,
		staticBase: ix,
		logf:       func(string, ...any) {},
	}
	w.structural.external = noStructural{}
	return w, nil
}

// ReplaceExtraChunks is SetExtraChunks that reports failure: it replaces
// the chunks unioned into the index alongside the corpus (DB tier-2 schema
// chunks) and returns an error if the index could not be rebuilt with
// them, in which case the previous index keeps serving.
//
// On an OpenFS Workspace the index is rebuilt from the corpus as first
// indexed plus chunks, so calls replace one another and never accumulate.
// On any other Workspace it is SetExtraChunks and returns nil.
func (w *Workspace) ReplaceExtraChunks(chunks []chunk.Chunk) error {
	if w.staticBase == nil {
		w.index.SetExtraChunks(chunks)
		return nil
	}
	ix, err := w.staticBase.WithExtraChunks(chunks)
	if err != nil {
		return err
	}
	if !w.index.PublishStatic(ix) {
		return fmt.Errorf("engine: could not publish the rebuilt index")
	}
	return nil
}

// ChunkCount is the number of chunks in the published index.
func (w *Workspace) ChunkCount() int { return w.index.Load().Len() }
