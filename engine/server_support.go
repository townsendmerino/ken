package engine

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/townsendmerino/ken/internal/search"
	"github.com/townsendmerino/ken/internal/structural"
)

// server_support.go — what the mcp package needs from a Workspace beyond
// the Hit-based methods, so that it can serve its tools without importing
// internal/search or internal/structural. Everything here is best-effort,
// outside the stability promise.

// Result is one ranked chunk as the index stores it: the chunk (with its
// enrichment label still in Text) and its score. Alias for search.Result;
// the mcp package re-exports it as mcp.Result. Most callers want Hit.
type Result = search.Result

// ModeNames lists the mode names in Mode order, so ModeNames()[int(m)] is
// m's name.
func ModeNames() []string { return search.ModeNames() }

// SearchResults is SearchWith returning the stored chunks instead of Hits:
// the same ranking and scores, with none of the per-Hit work (content hash,
// per-arm ranks, heading path). It is what the MCP server formats its tool
// responses from. opts.NoArmRanks has no effect here.
func (w *Workspace) SearchResults(ctx context.Context, query string, k int, mode Mode, opts SearchOptions) ([]Result, Mode, error) {
	if err := ctx.Err(); err != nil {
		return nil, mode, err
	}
	results, eff := searchResults(w.index.Load(), query, k, mode, opts)
	return results, eff, nil
}

// FindRelatedResults is FindRelated returning the stored chunks instead of
// Hits. The error is the index's own (no such chunk, no semantic arm).
func (w *Workspace) FindRelatedResults(ctx context.Context, filePath string, line, k int) ([]Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return w.index.Load().FindRelated(filePath, line, k)
}

// CurrentMode is the mode the published index is serving right now. It can
// be ModeBM25 while a staged build (Options.StagedEmbedding) is still
// embedding, whatever Options.Mode asked for.
func (w *Workspace) CurrentMode() Mode { return w.index.Load().Mode() }

// Warming reports whether a staged build is still serving BM25 while the
// embedding arm builds in the background.
func (w *Workspace) Warming() bool { return w.index.Warming() }

// IndexStatus describes the published index itself, read from the index
// rather than from the options it was built with.
type IndexStatus struct {
	Mode       Mode
	FileCount  int
	ChunkCount int

	// EnrichedChunks is how many chunks carry an Arm B enrichment label.
	EnrichedChunks int

	// DenseRetriever is the dense arm being served: "" for exact Flat (and
	// for a BM25 index, which has none), "flat-i8" or "flat-binary-i8".
	DenseRetriever string
}

// IndexStatus reports on the published index. It walks every chunk, so it
// is for a status report, not a query path.
func (w *Workspace) IndexStatus() IndexStatus {
	ix := w.index.Load()
	return IndexStatus{
		Mode:           ix.Mode(),
		FileCount:      countUniqueFiles(ix),
		ChunkCount:     ix.Len(),
		EnrichedChunks: ix.EnrichedChunks(),
		DenseRetriever: ix.DenseRetriever(),
	}
}

// StructuralStatus summarizes a built structural index.
type StructuralStatus struct {
	TopLevelSymbols int // functions and classes
	Methods         int
	// FilesByExtension counts indexed files per extension, leading dot
	// kept (".go", ".py").
	FilesByExtension map[string]int
	// IndexedFiles is how many files the index holds; UniqueSymbols how
	// many distinct names have a definition; UniqueCallees how many
	// distinct names are called somewhere.
	IndexedFiles, UniqueSymbols, UniqueCallees int
}

// StructuralStatus summarizes the structural index if one is already
// built. Like StructuralIfBuilt it never starts a build; ok is false when
// there is none yet.
func (w *Workspace) StructuralStatus() (st StructuralStatus, ok bool) {
	sx := w.structural.ifBuilt()
	if sx == nil {
		return st, false
	}
	s := sx.Stats()
	st.IndexedFiles, st.UniqueSymbols, st.UniqueCallees = s.IndexedFiles, s.UniqueSymbols, s.UniqueCallees
	st.FilesByExtension = map[string]int{}
	for path, fs := range sx.Files() {
		st.FilesByExtension[filepath.Ext(path)]++
		for _, fn := range fs.Functions {
			if fn.IsMethod {
				st.Methods++
			} else {
				st.TopLevelSymbols++
			}
		}
		st.TopLevelSymbols += len(fs.Classes)
	}
	return st, true
}

// StructuralPending reports whether a structural lookup made now would
// find a build in flight or start one, rather than an index or a settled
// "none available".
func (w *Workspace) StructuralPending() bool { return w.structural.pending() }

// EnrichFileBudgetCounts returns how many files the parse budget has
// skipped (parse timed out) and reached (parse accepted at the budget) in
// this process, across every Workspace.
func EnrichFileBudgetCounts() (skipped, reached uint64) {
	return structural.ParseBudgetSkips(), structural.ParseBudgetReached()
}

// NormalizePath cleans a repo-relative path the way the structural index
// stores them (slash-separated, no leading "./").
func NormalizePath(path string) string { return structural.NormalizePath(path) }

// StructuralSource supplies a structural index that someone other than the
// Workspace builds and owns; see WrapIndex. The three methods mean what
// Workspace.Structural, StructuralPending and StructuralIfBuilt mean, with
// nil standing for "not available".
type StructuralSource interface {
	StructuralIndex(ctx context.Context) *StructuralIndex
	StructuralPending() bool
	StructuralIfBuilt() *StructuralIndex
}

// WrapIndex returns a Workspace over an index the caller already built and
// keeps ownership of, for code that holds a WatchedIndex (an mcp.RepoBundle
// from a third-party Builder) and wants the Workspace methods on it. It is
// the escape hatch in reverse.
//
// The Workspace is a view. It queries index and asks structural, which may
// be nil, for the structural index on every call; it builds nothing and
// owns nothing, so there is no need to Close it, and Close does not close
// index. Refresh and WriteSnapshot are not supported on a wrapped index.
func WrapIndex(index *WatchedIndex, structural StructuralSource) *Workspace {
	w := &Workspace{
		root:    index.Root(),
		mode:    index.Load().Mode(),
		index:   index,
		wrapped: true,
		logf:    func(string, ...any) {},
	}
	if structural != nil {
		w.structural.external = structural
	} else {
		w.structural.external = noStructural{}
	}
	return w
}

// noStructural is the StructuralSource of a wrapped index with none.
type noStructural struct{}

func (noStructural) StructuralIndex(context.Context) *StructuralIndex { return nil }
func (noStructural) StructuralPending() bool                          { return false }
func (noStructural) StructuralIfBuilt() *StructuralIndex              { return nil }

// ErrWrappedIndex is returned by the operations a WrapIndex view cannot
// perform, because it does not own the index: Refresh and WriteSnapshot.
var ErrWrappedIndex = errors.New("engine: not supported on a wrapped index")

// StripLabel removes the Arm B enrichment line from a stored chunk's text,
// leaving the file's own lines (what Hit.Text carries). A no-op on text
// with no label.
func StripLabel(text string) string { return structural.StripLabel(text) }

// OutlineOfFile returns one file's outline and whether the structural
// index holds that file at all (a file it holds can still have no
// entries). path is repo-relative. Unlike Outline it never treats path as
// a directory.
func (w *Workspace) OutlineOfFile(ctx context.Context, path string) (entries []OutlineEntry, indexed bool, err error) {
	idx, err := w.structuralIndex(ctx)
	if err != nil {
		return nil, false, err
	}
	norm := structural.NormalizePath(path)
	if idx.File(norm) == nil {
		return nil, false, nil
	}
	return idx.Outline(norm), true, nil
}

// StructuralFilesUnder lists the structurally indexed files at or under a
// repo-relative directory path, sorted. It is what Outline walks for a
// directory, exposed so a caller can page through the files and outline
// only a window of them.
func (w *Workspace) StructuralFilesUnder(ctx context.Context, path string) ([]string, error) {
	idx, err := w.structuralIndex(ctx)
	if err != nil {
		return nil, err
	}
	return idx.FilesUnderPath(structural.NormalizePath(path)), nil
}
