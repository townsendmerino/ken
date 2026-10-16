package engine

import "github.com/townsendmerino/ken/internal/search"

// Status summarizes a Workspace's current state: its index's size and
// generation, and whether the structural index is ready.
type Status struct {
	Root        string
	Mode        Mode
	Chunker     string
	FileCount   int
	ChunkCount  int
	Generation  uint64
	WatchActive bool

	StructuralBuilt   bool
	StructuralPending bool
}

// countUniqueFiles counts distinct non-tombstoned files across ix's chunks.
func countUniqueFiles(ix *search.Index) int {
	seen := make(map[string]struct{})
	for _, c := range ix.Chunks() {
		if c.Tombstoned {
			continue
		}
		seen[c.File] = struct{}{}
	}
	return len(seen)
}

// Status reports the Workspace's current state. Never triggers a
// structural build — it only peeks at whatever StructuralBuilt/Pending
// already reflects, so calling Status doesn't itself pay the structural
// parse cost.
func (w *Workspace) Status() Status {
	ix := w.index.Load()
	return Status{
		Root:              w.root,
		Mode:              ix.Mode(),
		Chunker:           w.chunker,
		FileCount:         countUniqueFiles(ix),
		ChunkCount:        ix.Len(),
		Generation:        w.index.Generation(),
		WatchActive:       w.watch,
		StructuralBuilt:   w.structural.built.Load(),
		StructuralPending: w.structural.pending(),
	}
}
