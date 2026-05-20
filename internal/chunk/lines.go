package chunk

// LineChunker is the language-agnostic fallback: fixed-size line windows
// with a small overlap so a match straddling a window boundary still lands
// wholly inside at least one chunk. docs/DESIGN.md §1 pins the defaults at a
// 50-line window with 5 lines of overlap.
//
// It is also the seam-validation stand-in until the Chunker interface
// arrives in Stage 2: every other chunker must produce the same Chunk shape.
type LineChunker struct {
	Size    int // lines per chunk (> 0)
	Overlap int // lines shared with the previous chunk (0 <= Overlap < Size)
}

// NewLineChunker returns the docs/DESIGN.md default 50/5 configuration.
func NewLineChunker() *LineChunker {
	return &LineChunker{Size: 50, Overlap: 5}
}

// Chunk slices source into overlapping line windows. The returned Chunk.Text
// is the exact byte slice of source spanning [StartLine, EndLine] including
// the newline that terminates each interior line. An empty source yields no
// chunks.
func (lc *LineChunker) Chunk(file string, source []byte) []Chunk {
	if len(source) == 0 {
		return nil
	}
	size := lc.Size
	if size <= 0 {
		size = 50
	}
	overlap := lc.Overlap
	if overlap < 0 || overlap >= size {
		overlap = 0
	}
	stride := size - overlap

	// Byte offset where each content line begins. A trailing '\n' does not
	// start a new line (no empty phantom line at EOF).
	lineStart := []int{0}
	for i := range source {
		if source[i] == '\n' && i+1 < len(source) {
			lineStart = append(lineStart, i+1)
		}
	}
	n := len(lineStart)

	var chunks []Chunk
	for i := 0; i < n; i += stride {
		j := min(i+size, n)
		a := lineStart[i]
		b := len(source)
		if j < n {
			b = lineStart[j]
		}
		chunks = append(chunks, Chunk{
			File:      file,
			StartLine: i + 1,
			EndLine:   j,
			Text:      string(source[a:b]),
		})
		if j == n {
			break // last window reached EOF; further strides only duplicate
		}
	}
	return chunks
}
