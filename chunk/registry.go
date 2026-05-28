package chunk

import (
	"fmt"
	"slices"
	"sort"
)

// DefaultChunkSize is the target chunk size in bytes (≈characters for the
// ASCII-heavy code ken indexes). docs/DESIGN.md "Build order" pins Stage 2 at 1500.
const DefaultChunkSize = 1500

// Chunker turns a source file into retrieval units. Implementations are
// stateless and goroutine-safe after construction.
//
// The signature is deliberately minimal — no context.Context (regex
// chunking is synchronous and fast) and no filename (the caller stamps
// Chunk.File; a chunker only needs the language to pick rules). docs/DESIGN.md §2
// originally sketched a ctx parameter; it was dropped because nothing in
// Option C needs it and Options B/A can adopt this same shape (see §2).
//
// STABILITY (ADR-032): this interface — plus Register/Get/Names, ChunkFile,
// and the Chunk struct — is ken's PUBLIC, 1.0-committed chunker surface.
// External mcp.Run authors implement Chunker (or import one of ken's
// registered chunkers) and Register it before calling mcp.Run. The
// interface is small and dependency-free on purpose; it is the swap-out
// boundary ADR-010 designed for. The CONCRETE chunkers ken ships behind
// it (especially chunk/treesitter, which is backed by the pre-1.0
// gotreesitter dep) are best-effort: their exact chunk boundaries may
// shift across versions. Depend on the interface, not on a specific
// chunker's byte-for-byte output.
type Chunker interface {
	// Chunk partitions source into chunks. The returned chunks are
	// contiguous and non-overlapping so concatenating their Text in order
	// reproduces source byte-for-byte; Chunk.File is left empty for the
	// caller to set.
	Chunk(source []byte, language string, chunkSize int) ([]Chunk, error)
	// SupportedLanguages returns the canonical language names this chunker
	// handles. An empty slice means "all languages" (the line fallback).
	SupportedLanguages() []string
	Name() string // "line" | "regex" | (future) "chroma" | "treesitter"
}

var registry = map[string]Chunker{}

// Register adds a chunker under name. Called from init() — the "line"
// chunker registers itself in this package; "regex" registers from
// chunk/regex (blank-imported by internal/search to avoid an import
// cycle: chunk must not import its own sub-chunkers). External mcp.Run
// authors call this directly to register a custom or ken-provided
// chunker before invoking mcp.Run (ADR-032).
func Register(name string, c Chunker) { registry[name] = c }

// Get returns the registered chunker, or an error listing what is available.
func Get(name string) (Chunker, error) {
	c, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("chunk: unknown chunker %q (have %v)", name, Names())
	}
	return c, nil
}

// Names lists registered chunker names, sorted.
func Names() []string {
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func supports(c Chunker, lang string) bool {
	langs := c.SupportedLanguages()
	if len(langs) == 0 {
		return true // universal fallback (the line chunker)
	}
	return slices.Contains(langs, lang)
}

// ChunkFile routes one file to the named chunker, falling back to the
// "line" chunker for languages the chosen chunker does not support, and
// stamps Chunk.File on the result. This is the single entry point the
// orchestration layer (internal/search) uses.
func ChunkFile(name, file string, source []byte, chunkSize int) ([]Chunk, error) {
	c, err := Get(name)
	if err != nil {
		return nil, err
	}
	lang := Language(file)
	if !supports(c, lang) {
		lc, lerr := Get("line")
		if lerr != nil {
			return nil, lerr
		}
		c = lc
	}
	if chunkSize <= 0 {
		chunkSize = DefaultChunkSize
	}
	cs, err := c.Chunk(source, lang, chunkSize)
	if err != nil {
		return nil, err
	}
	for i := range cs {
		cs[i].File = file
	}
	return cs, nil
}
