package regex

import (
	"regexp"
	"strings"
	"testing"

	"github.com/townsendmerino/ken/internal/chunk"
)

func chunkStr(t *testing.T, lang string, size int, src string) []chunk.Chunk {
	t.Helper()
	cs, err := New().Chunk([]byte(src), lang, size)
	if err != nil {
		t.Fatalf("Chunk(%s): %v", lang, err)
	}
	return cs
}

func concat(cs []chunk.Chunk) string {
	var b strings.Builder
	for _, c := range cs {
		b.WriteString(c.Text)
	}
	return b.String()
}

// assertFidelity is the load-bearing invariant: chunks are a contiguous,
// non-overlapping partition, so re-concatenation must reproduce source.
func assertFidelity(t *testing.T, src string, cs []chunk.Chunk) {
	t.Helper()
	if got := concat(cs); got != src {
		t.Fatalf("byte fidelity lost\n--- got (%d) ---\n%q\n--- want (%d) ---\n%q",
			len(got), got, len(src), src)
	}
	// Line numbers must also be a clean partition.
	want := 1
	for i, c := range cs {
		if c.StartLine != want {
			t.Fatalf("chunk %d StartLine=%d, want %d (gap/overlap)", i, c.StartLine, want)
		}
		want = c.EndLine + 1
	}
}

func lineCount(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(strings.TrimSuffix(s, "\n"), "\n") + 1
}

// assertMaxSize: every chunk is ≤ size unless it is a single oversized
// source line (which cannot be split without losing bytes).
func assertMaxSize(t *testing.T, cs []chunk.Chunk, size int) {
	t.Helper()
	for i, c := range cs {
		if len(c.Text) > size && lineCount(c.Text) > 1 {
			t.Errorf("chunk %d is %d bytes (> size %d) across %d lines — should have split",
				i, len(c.Text), size, lineCount(c.Text))
		}
	}
}

func firstNonBlank(s string) string {
	for ln := range strings.SplitSeq(s, "\n") {
		if t := strings.TrimSpace(ln); t != "" {
			return t
		}
	}
	return ""
}

// assertBoundariesMatch: every chunk after the first must begin at a line
// that looks like a definition or an attach line (doc/annotation/etc.).
// Use only with inputs that contain no single unit larger than size, so
// every cut is a real definition boundary (not a line-split fallback).
func assertBoundariesMatch(t *testing.T, cs []chunk.Chunk, re *regexp.Regexp) {
	t.Helper()
	for i := 1; i < len(cs); i++ {
		fl := firstNonBlank(cs[i].Text)
		if !re.MatchString(fl) {
			t.Errorf("chunk %d starts at %q — not a definition/attach boundary", i, fl)
		}
	}
}

// chunkOf returns the index of the chunk containing 1-based source line.
func chunkOf(cs []chunk.Chunk, line int) int {
	for i, c := range cs {
		if line >= c.StartLine && line <= c.EndLine {
			return i
		}
	}
	return -1
}

// startsChunk reports whether some chunk begins exactly at 1-based line.
func startsChunk(cs []chunk.Chunk, line int) bool {
	for _, c := range cs {
		if c.StartLine == line {
			return true
		}
	}
	return false
}

// assertNotCutInside fails if any of subs (body lines) begins a chunk —
// i.e. a definition got split through its body rather than at a boundary.
func assertNotCutInside(t *testing.T, src string, cs []chunk.Chunk, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if ln := lineNo(src, s); ln > 0 && startsChunk(cs, ln) {
			t.Errorf("chunk boundary fell on body line %q (line %d) — cut mid-definition", s, ln)
		}
	}
}

// lineNo returns the 1-based line number of the first line containing sub.
func lineNo(src, sub string) int {
	before, _, ok := strings.Cut(src, sub)
	if !ok {
		return -1
	}
	return strings.Count(before, "\n") + 1
}
