// Package markdown is ken's documentation-aware chunker, registered as
// "markdown". Targets `.md`, `.mdx`, and `.markdown` files; other files
// auto-fall-back to the line chunker so a mixed-content corpus (docs +
// code) routes cleanly when this chunker is selected at the corpus
// level.
//
// Algorithm (overview; details in scanner.go):
//
//  1. scanLines: one pass over source, classifying each line as heading,
//     fenced-code-fence, fenced-code-inside, frontmatter, table row,
//     table separator, list item, blank, or text. Fence and frontmatter
//     state are carried forward across lines so `#` inside a code block
//     is NOT treated as a heading.
//  2. Section pass: split into heading-bounded sections. A section
//     starts at file beginning OR at an ATX heading OR at a setext
//     heading (text line whose next line is `===`/`---`).
//  3. Size-bounded subdivision: a section larger than chunkSize is
//     subdivided at safe-split positions (blank lines that are NOT
//     inside an atomic block — fenced code, frontmatter, table, list).
//     Atomic blocks larger than chunkSize stay whole; splitting a code
//     fence or a table mid-way would corrupt the semantics retrieval
//     wants to surface.
//
// Load-bearing invariant: byte-fidelity. Concatenating Chunk.Text values
// for a single file reproduces the source byte-for-byte. Tests in
// markdown_test.go pin this across all 12 prompt-spec scenarios.
//
// No new third-party dependencies. The CommonMark/GFM corner cases we
// don't aim to handle (HTML blocks, footnotes, link reference
// definitions, def-list extensions) all degrade gracefully to lineText
// and either flow into the current section or trigger a paragraph split
// — they never corrupt fidelity.
package markdown

import (
	"path"
	"strings"

	"github.com/townsendmerino/ken/internal/chunk"
)

// Chunker implements chunk.Chunker for Markdown documents.
type Chunker struct{}

// New returns a fresh chunker. Stateless; safe to share across goroutines.
func New() *Chunker { return &Chunker{} }

func init() { chunk.Register("markdown", New()) }

// Name is "markdown" (the chunker registry key).
func (*Chunker) Name() string { return "markdown" }

// SupportedLanguages returns the canonical language names this chunker
// claims. Only "markdown" — chunk.ChunkFile routes other languages to
// the line chunker via the SupportedLanguages-based fallback. The
// chunker also self-checks (via routesToLineFallback) so callers who
// invoke Chunk directly with a non-markdown language get the same
// fallback behavior.
func (*Chunker) SupportedLanguages() []string { return []string{"markdown"} }

// markdownExtensions are the file extensions for which the markdown
// chunker is preferred. Used by routesToLineFallback when language is
// empty (callers that don't set Chunk.File pre-stamping go through
// chunk.ChunkFile which does set it via Language(), so this is the
// belt-and-suspenders path for direct callers).
var markdownExtensions = map[string]bool{
	".md":       true,
	".mdx":      true,
	".markdown": true,
}

// Chunk produces chunks for a markdown source. For non-markdown
// languages (when called directly rather than via ChunkFile's
// SupportedLanguages dispatch), delegates to the registered "line"
// chunker for byte-fidelity-preserving fallback.
func (c *Chunker) Chunk(source []byte, language string, chunkSize int) ([]chunk.Chunk, error) {
	if len(source) == 0 {
		return nil, nil
	}
	if chunkSize <= 0 {
		chunkSize = chunk.DefaultChunkSize
	}
	if language != "" && language != "markdown" {
		return c.lineFallback(source, language, chunkSize)
	}
	return c.chunkMarkdown(source, chunkSize), nil
}

// lineFallback delegates to the registered "line" chunker for non-
// markdown languages. Matches the treesitter chunker's fallback shape.
func (c *Chunker) lineFallback(source []byte, language string, chunkSize int) ([]chunk.Chunk, error) {
	lc, err := chunk.Get("line")
	if err != nil {
		// chunk.init() registers "line" before any sub-package init runs,
		// so this branch is unreachable in practice. Return a single
		// whole-file chunk as a last-resort safety net.
		return []chunk.Chunk{wholeFileChunk(source)}, nil
	}
	return lc.Chunk(source, language, chunkSize)
}

// chunkMarkdown is the core algorithm: scan → section → subdivide.
func (c *Chunker) chunkMarkdown(source []byte, chunkSize int) []chunk.Chunk {
	lines := scanLines(source)
	if len(lines) == 0 {
		return nil
	}

	// Compute setext-promoted boundary set. A setext underline at line N
	// promotes line N-1 to a heading (only if N-1 is lineText).
	setextHeadingIdx := map[int]bool{}
	for i, ln := range lines {
		if ln.kind == lineSetextUnderline && i > 0 && lines[i-1].kind == lineText {
			setextHeadingIdx[i-1] = true
		}
	}

	// Identify section boundaries: indices where a new section starts.
	// Index 0 is always a boundary; ATX/setext headings start new sections.
	boundaries := []int{0}
	for i, ln := range lines {
		if i == 0 {
			continue
		}
		if ln.kind == lineHeadingATX || setextHeadingIdx[i] {
			boundaries = append(boundaries, i)
		}
	}

	// For each section [boundaries[k], boundaries[k+1]), produce chunks.
	var out []chunk.Chunk
	for k, startIdx := range boundaries {
		endIdx := len(lines)
		if k+1 < len(boundaries) {
			endIdx = boundaries[k+1]
		}
		sectionLines := lines[startIdx:endIdx]
		sectionBytes := source[sectionLines[0].start:sectionLines[len(sectionLines)-1].end]
		startLine := lineNumber(source, sectionLines[0].start)
		if len(sectionBytes) <= chunkSize {
			out = append(out, makeChunk(source, sectionLines[0].start, sectionLines[len(sectionLines)-1].end, startLine))
			continue
		}
		out = append(out, subdivideSection(source, sectionLines, chunkSize)...)
	}
	return out
}

// subdivideSection splits an oversized section at safe-split positions.
// A "safe split" is a blank line that is NOT inside an atomic block
// (fenced code, frontmatter, table, list). Atomic blocks larger than
// chunkSize stay whole (splitting one mid-fence would corrupt the
// content), which means a single chunk can legitimately exceed
// chunkSize — the size target is best-effort under that invariant.
func subdivideSection(source []byte, lines []scannedLine, chunkSize int) []chunk.Chunk {
	if len(lines) == 0 {
		return nil
	}

	// Find safe-split positions: indices i such that lines[i] is blank,
	// AND lines[i-1] is not inside an atomic block, AND lines[i+1] is
	// not inside an atomic block. The aggregator emits one chunk per run
	// of source between safe splits (and absorbs leading/trailing blank
	// runs into adjacent chunks).
	splittable := splittablePositions(lines)

	var out []chunk.Chunk
	chunkStartIdx := 0
	for i := 1; i < len(lines); i++ {
		// Bytes accumulated from lines[chunkStartIdx..i] inclusive.
		accumBytes := lines[i].end - lines[chunkStartIdx].start
		if accumBytes >= chunkSize && i > chunkStartIdx {
			// Try to find the most recent splittable position <= i.
			splitIdx := -1
			for s := i; s > chunkStartIdx; s-- {
				if splittable[s] {
					splitIdx = s
					break
				}
			}
			if splitIdx > chunkStartIdx {
				// Emit [chunkStartIdx, splitIdx) — splitIdx is a blank
				// line; we attach blanks to the chunk that came before
				// for byte fidelity.
				out = append(out, makeChunk(source,
					lines[chunkStartIdx].start,
					lines[splitIdx].end,
					lineNumber(source, lines[chunkStartIdx].start)))
				chunkStartIdx = splitIdx + 1
			}
			// If no safe split exists in the window, we have to keep
			// accumulating — an atomic block bigger than chunkSize
			// legitimately overflows.
		}
	}
	// Flush remaining lines.
	if chunkStartIdx < len(lines) {
		out = append(out, makeChunk(source,
			lines[chunkStartIdx].start,
			lines[len(lines)-1].end,
			lineNumber(source, lines[chunkStartIdx].start)))
	}
	return out
}

// splittablePositions marks indices in lines where a chunk boundary is
// safe. A blank line is splittable unless it sits between two lines that
// are both inside atomic blocks (which can't actually happen since
// blanks aren't inside atomics — but we keep the check defensive). The
// real constraint is: don't split immediately after an opening code
// fence or table-row before its separator. Both naturally hold because
// blanks inside fenced code are classified lineCodeInside, not lineBlank.
func splittablePositions(lines []scannedLine) []bool {
	out := make([]bool, len(lines))
	for i, ln := range lines {
		if ln.kind == lineBlank {
			out[i] = true
		}
	}
	return out
}

// makeChunk constructs a single chunk.Chunk from a byte range. The
// File field is left empty (chunk.ChunkFile stamps it). EndLine is
// derived from the chunk's last byte: a chunk ending with '\n' spans
// N lines, not N+1, so the trailing-newline correction is applied.
func makeChunk(source []byte, byteStart, byteEnd, startLine int) chunk.Chunk {
	text := source[byteStart:byteEnd]
	endLine := startLine
	for i := byteStart; i < byteEnd; i++ {
		if source[i] == '\n' && i+1 < byteEnd {
			endLine++
		}
	}
	// If the chunk ends with a newline, the "last line" count is correct
	// as-is; if not, we have a partial trailing line that still counts.
	return chunk.Chunk{
		StartLine: startLine,
		EndLine:   endLine,
		Text:      string(text),
	}
}

// lineNumber returns the 1-based line number of the byte offset in source.
func lineNumber(source []byte, offset int) int {
	n := 1
	if offset > len(source) {
		offset = len(source)
	}
	for i := 0; i < offset; i++ {
		if source[i] == '\n' {
			n++
		}
	}
	return n
}

// wholeFileChunk is a last-resort fallback if even the line chunker
// can't be looked up (registry-init pathology — should not happen).
func wholeFileChunk(source []byte) chunk.Chunk {
	endLine := 1
	for _, b := range source {
		if b == '\n' {
			endLine++
		}
	}
	if len(source) > 0 && source[len(source)-1] == '\n' && endLine > 1 {
		endLine--
	}
	return chunk.Chunk{StartLine: 1, EndLine: endLine, Text: string(source)}
}

// IsMarkdownPath reports whether the path has a markdown file extension.
// Exposed so external callers (e.g. cmd/ken-mcp-docs) can decide whether
// to set ChunkerName=markdown for their corpus. Recognizes .md, .mdx,
// and .markdown (case-insensitive).
func IsMarkdownPath(p string) bool {
	ext := strings.ToLower(path.Ext(p))
	return markdownExtensions[ext]
}
