package chunk

// lineChunker adapts the Stage 1 LineChunker to the Chunker interface. It
// is the universal fallback: SupportedLanguages() == nil means "handles
// anything", and language/chunkSize are ignored (it is line-window based,
// not size based — that is the point of a dumb fallback).
type lineChunker struct{ lc *LineChunker }

func (l lineChunker) Chunk(source []byte, _ string, _ int) ([]Chunk, error) {
	return l.lc.Chunk("", source), nil
}

func (lineChunker) SupportedLanguages() []string { return nil }
func (lineChunker) Name() string                 { return "line" }

func init() {
	Register("line", lineChunker{lc: NewLineChunker()})
}
