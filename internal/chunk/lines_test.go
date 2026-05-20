package chunk

import (
	"strings"
	"testing"
)

func mkLines(n int) []byte {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		b.WriteString("line")
		b.WriteByte(byte('0' + i%10))
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

func TestLineChunker_Empty(t *testing.T) {
	if got := NewLineChunker().Chunk("f", nil); got != nil {
		t.Fatalf("empty source: got %d chunks, want nil", len(got))
	}
}

func TestLineChunker_SingleShortFile(t *testing.T) {
	lc := &LineChunker{Size: 50, Overlap: 5}
	got := lc.Chunk("f.go", []byte("a\nb\nc"))
	if len(got) != 1 {
		t.Fatalf("got %d chunks, want 1", len(got))
	}
	c := got[0]
	if c.StartLine != 1 || c.EndLine != 3 {
		t.Errorf("lines = %d-%d, want 1-3", c.StartLine, c.EndLine)
	}
	if c.Text != "a\nb\nc" {
		t.Errorf("text = %q, want exact source", c.Text)
	}
}

func TestLineChunker_WindowingAndOverlap(t *testing.T) {
	lc := &LineChunker{Size: 50, Overlap: 5} // stride 45
	got := lc.Chunk("f", mkLines(100))
	// Windows: [1-50], [46-95], [91-100].
	wantSpans := [][2]int{{1, 50}, {46, 95}, {91, 100}}
	if len(got) != len(wantSpans) {
		t.Fatalf("got %d chunks, want %d (%v)", len(got), len(wantSpans), spans(got))
	}
	for i, w := range wantSpans {
		if got[i].StartLine != w[0] || got[i].EndLine != w[1] {
			t.Errorf("chunk %d span = %d-%d, want %d-%d", i, got[i].StartLine, got[i].EndLine, w[0], w[1])
		}
	}
	// Overlap is exactly Overlap lines between consecutive chunks.
	if got[1].StartLine != got[0].EndLine-lc.Overlap+1 {
		t.Errorf("overlap mismatch: c0 ends %d, c1 starts %d, want overlap %d",
			got[0].EndLine, got[1].StartLine, lc.Overlap)
	}
}

func TestLineChunker_ExactBytesPreserved(t *testing.T) {
	src := []byte("x\ny\nz\n") // trailing newline, 3 content lines
	got := NewLineChunker().Chunk("f", src)
	if len(got) != 1 || got[0].EndLine != 3 {
		t.Fatalf("got %v, want one 1-3 chunk", spans(got))
	}
	if got[0].Text != string(src) {
		t.Errorf("text = %q, want %q (trailing newline preserved)", got[0].Text, src)
	}
}

func spans(cs []Chunk) [][2]int {
	out := make([][2]int, len(cs))
	for i, c := range cs {
		out[i] = [2]int{c.StartLine, c.EndLine}
	}
	return out
}
