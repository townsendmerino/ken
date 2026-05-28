// Package chunk splits source files into retrieval units behind a
// runtime-selectable Chunker interface (see registry.go).
//
// The Chunker interface seam supports three options per docs/DESIGN.md §2:
// `regex` (default; per-language regex rules, Stage 2), `treesitter`
// (opt-in; gotreesitter + cAST, v0.2.0 / ADR-010), and `line` (universal
// fallback, also used internally by the other two when they can't handle
// a language). Each registers itself via init() the database/sql way —
// chunk must not import its sub-chunker packages or there's an import
// cycle.
//
// Load-bearing invariants every Chunker must satisfy:
//
//   - **Byte-fidelity:** concatenating the returned Chunk.Text fields in
//     order reproduces the input source exactly. Tests pin this per
//     language because downstream code (snippet display, embedding,
//     find_related's resolve-by-line lookup) trusts it.
//   - **Stamping:** the chunker leaves Chunk.File empty; ChunkFile
//     stamps it on the way out so callers can pass any chunker and get
//     consistent results.
//   - **Stateless after construction:** Chunk is safe to call across
//     goroutines on a single Chunker instance (the registry hands out
//     pointer instances, not factories).
//
// Line numbers in the Chunk struct are 1-based and inclusive on both
// ends, matching how editors and grep report positions.
package chunk

// Chunk is one indexed unit of a source file. Line numbers are 1-based and
// inclusive on both ends, matching how editors and grep report positions.
//
// Chunk is part of the public chunker surface (ADR-032). The File /
// StartLine / EndLine / Text fields are the stable contract a Chunker
// implementation fills in. Tombstoned is a leakier case: it's an
// internal incremental-indexing detail (see below) exposed here only
// because the same struct round-trips through the watch path —
// external Chunker implementations should leave it false.
type Chunk struct {
	File      string // path relative to the index root
	StartLine int    // 1-based, inclusive
	EndLine   int    // 1-based, inclusive
	Text      string // exact source slice for [StartLine, EndLine]
	// Tombstoned marks a chunk whose source file has been deleted or
	// replaced under v0.3's incremental indexing (see
	// internal/search/watch.go). Transiently true within a single flush
	// — the mutator marks chunks as Tombstoned in-place, then
	// compactCorpus drops them before the snapshot is published.
	// Published snapshots never carry tombstones; the field matters
	// only on previously-published snapshots that an in-flight reader
	// still holds. Every read path (Search / FindRelated /
	// ResolveChunk) filters this field defensively. Wire-format callers
	// that round-trip Chunk to disk should preserve it; today the only
	// such caller is the bench harness, which never observes tombstoned
	// chunks because they never escape the search package.
	Tombstoned bool
}
