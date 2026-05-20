// Package chunk splits source files into retrieval units.
//
// Stage 1 ships only the fallback line chunker (lines.go). The runtime-
// selectable Chunker interface + registry and the per-language regex
// chunkers (Option C) land in Stage 2; see docs/DESIGN.md §2. The Chunk type is
// defined here so the rest of the pipeline (bm25, search) can depend on a
// stable shape before that interface exists.
package chunk

// Chunk is one indexed unit of a source file. Line numbers are 1-based and
// inclusive on both ends, matching how editors and grep report positions.
type Chunk struct {
	File      string // path relative to the index root
	StartLine int    // 1-based, inclusive
	EndLine   int    // 1-based, inclusive
	Text      string // exact source slice for [StartLine, EndLine]
}
