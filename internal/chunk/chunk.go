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
type Chunk struct {
	File      string // path relative to the index root
	StartLine int    // 1-based, inclusive
	EndLine   int    // 1-based, inclusive
	Text      string // exact source slice for [StartLine, EndLine]
}
