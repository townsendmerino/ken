package markdown

import (
	"testing"
)

// TestScanLines_BasicHeading covers ATX heading detection and the
// surrounding paragraph + blank classification.
func TestScanLines_BasicHeading(t *testing.T) {
	src := []byte("# Title\n\nSome prose.\n")
	got := scanLines(src)
	wantKinds := []lineKind{lineHeadingATX, lineBlank, lineText}
	assertKinds(t, got, wantKinds)
}

// TestScanLines_FencedCodeKeepsHashAsCode confirms that a `#` inside a
// fenced code block is NOT classified as a heading. This is THE
// load-bearing invariant of the scanner state machine.
func TestScanLines_FencedCodeKeepsHashAsCode(t *testing.T) {
	src := []byte("# Real heading\n\n```\n# this is bash, not a heading\necho hi\n```\n\n# After\n")
	got := scanLines(src)
	wantKinds := []lineKind{
		lineHeadingATX, // # Real heading
		lineBlank,      // blank
		lineCodeFence,  // ```
		lineCodeInside, // # this is bash, not a heading  ← must NOT be lineHeadingATX
		lineCodeInside, // echo hi
		lineCodeFence,  // ```
		lineBlank,      // blank
		lineHeadingATX, // # After
	}
	assertKinds(t, got, wantKinds)
}

// TestScanLines_FrontmatterAtTop covers `---`-bounded YAML frontmatter
// at the start of a file.
func TestScanLines_FrontmatterAtTop(t *testing.T) {
	src := []byte("---\ntitle: Hello\ntags: [demo]\n---\n\n# Body\n")
	got := scanLines(src)
	wantKinds := []lineKind{
		lineFrontmatterDelim,  // ---
		lineFrontmatterInside, // title: Hello
		lineFrontmatterInside, // tags: [demo]
		lineFrontmatterDelim,  // ---
		lineBlank,             // blank
		lineHeadingATX,        // # Body
	}
	assertKinds(t, got, wantKinds)
}

// TestScanLines_PlusFrontmatter covers TOML frontmatter (+++ delim) at
// the top of a file — the second supported delimiter shape.
func TestScanLines_PlusFrontmatter(t *testing.T) {
	src := []byte("+++\ntitle = \"x\"\n+++\nbody\n")
	got := scanLines(src)
	wantKinds := []lineKind{
		lineFrontmatterDelim,
		lineFrontmatterInside,
		lineFrontmatterDelim,
		lineText,
	}
	assertKinds(t, got, wantKinds)
}

// TestScanLines_DashesAreSetextNotFrontmatter — a `---` AFTER content
// (not at file start) is a setext H2 underline, not frontmatter.
func TestScanLines_DashesAreSetextNotFrontmatter(t *testing.T) {
	src := []byte("Subhead\n---\n\nbody\n")
	got := scanLines(src)
	wantKinds := []lineKind{
		lineText,            // "Subhead" (the aggregator will promote it via the setext underline below)
		lineSetextUnderline, // ---
		lineBlank,
		lineText,
	}
	assertKinds(t, got, wantKinds)
}

// TestScanLines_TildeFenceVariant ensures `~~~` is recognized as a code
// fence alongside the more common backtick fence.
func TestScanLines_TildeFenceVariant(t *testing.T) {
	src := []byte("~~~\nfoo\n~~~\n")
	got := scanLines(src)
	wantKinds := []lineKind{
		lineCodeFence,
		lineCodeInside,
		lineCodeFence,
	}
	assertKinds(t, got, wantKinds)
}

// TestScanLines_TableSeparatorAndRow checks the table-row + separator
// classification.
func TestScanLines_TableSeparatorAndRow(t *testing.T) {
	src := []byte("| a | b |\n|---|---|\n| 1 | 2 |\n")
	got := scanLines(src)
	wantKinds := []lineKind{lineTableRow, lineTableSep, lineTableRow}
	assertKinds(t, got, wantKinds)
}

// TestScanLines_ListMarkers covers each list-marker shape.
func TestScanLines_ListMarkers(t *testing.T) {
	src := []byte("- a\n* b\n+ c\n1. d\n2) e\n")
	got := scanLines(src)
	for i, ln := range got {
		if ln.kind != lineList {
			t.Errorf("line %d: kind=%d, want lineList", i, ln.kind)
		}
	}
}

// TestScanLines_CRLF mixed line endings — the scanner must classify
// correctly regardless of \n vs \r\n termination.
func TestScanLines_CRLF(t *testing.T) {
	src := []byte("# A\r\n\r\nbody1\r\n```\r\ncode line\r\n```\r\n")
	got := scanLines(src)
	wantKinds := []lineKind{
		lineHeadingATX,
		lineBlank,
		lineText,
		lineCodeFence,
		lineCodeInside,
		lineCodeFence,
	}
	assertKinds(t, got, wantKinds)
}

// TestScanLines_EmptySource returns nil — degenerate input shouldn't panic.
func TestScanLines_EmptySource(t *testing.T) {
	if got := scanLines(nil); got != nil {
		t.Errorf("scanLines(nil) = %v, want nil", got)
	}
	if got := scanLines([]byte{}); got != nil {
		t.Errorf("scanLines([]) = %v, want nil", got)
	}
}

func assertKinds(t *testing.T, got []scannedLine, want []lineKind) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("len mismatch: got %d lines, want %d\ngot: %v", len(got), len(want), kindsOf(got))
	}
	for i := range got {
		if got[i].kind != want[i] {
			t.Errorf("line %d: kind=%v, want %v\nfull: got=%v", i, got[i].kind, want[i], kindsOf(got))
			return
		}
	}
}

func kindsOf(ls []scannedLine) []lineKind {
	out := make([]lineKind, len(ls))
	for i, l := range ls {
		out[i] = l.kind
	}
	return out
}
