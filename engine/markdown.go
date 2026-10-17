package engine

// markdown.go — heading/section detection for markdown files (K9).
//
// The line-classification logic below (fences, frontmatter, ATX/setext
// headings, tables, lists) is a deliberate, faithful port of the unexported
// scanner in github.com/townsendmerino/aikit/chunk/markdown (scanner.go) —
// NOT an independent reimplementation. That package already classifies
// every line to decide where chunk boundaries go; this file needs the same
// classification to decide where SECTION boundaries go, and those must
// agree (a markdown chunk's line range must fall inside exactly one
// Section with a matching heading path — see sections_test.go). Since the
// classifier is unexported there, porting it here is lower-risk than
// re-deriving equivalent rules from scratch and risking silent drift on an
// edge case (a code fence containing a "#" line, a setext heading that
// looks like a table separator, etc). TODO: if aikit ever needs the same
// classification for a second consumer, promote it to a public function
// there and delete this copy.
//
// What's NOT ported: the size-bounded subdivision pass (subdivideSection)
// and safe-split logic — sections here are the pre-subdivision,
// heading-bounded ranges (what the real chunker calls a "section" before
// it decides whether to split it further for chunk-size reasons). A chunk
// produced by subdivision is always a sub-range of exactly one such
// section, which is the whole point: Section is coarser than Chunk, never
// finer, so "every chunk's lines fall inside exactly one Section" holds by
// construction.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/townsendmerino/aikit/chunk/markdown"
)

// Section is a heading-bounded region of a markdown file, in file order.
// A Section spans from one heading (inclusive) to the next heading
// (exclusive), or from the start of the file to the first heading (the
// headerless preamble, HeadingPath == nil).
type Section struct {
	// HeadingPath is the chain of headings enclosing this section,
	// outermost first, ending with the section's own heading (e.g.
	// ["Testing", "A skip is not a pass"]). Empty for the headerless
	// preamble.
	HeadingPath []string
	// Level is this section's own heading level (1-6), or 0 for the
	// headerless preamble.
	Level int
	// StartLine, EndLine are 1-based inclusive line numbers.
	StartLine int
	EndLine   int
	// ApproxTokens is ApproxTokens(text) over the section's full text
	// (heading line included).
	ApproxTokens int
}

type mdLineKind int

const (
	mdLineText mdLineKind = iota
	mdLineBlank
	mdLineHeadingATX
	mdLineSetextUnderline
	mdLineCodeFence
	mdLineCodeInside
	mdLineFrontmatterDelim
	mdLineFrontmatterInside
	mdLineTableSep
	mdLineTableRow
	mdLineList
)

type mdScannedLine struct {
	kind  mdLineKind
	start int
	end   int
}

// scanMdLines classifies every line of source, porting aikit/chunk/markdown's
// scanLines (see file doc comment).
func scanMdLines(source []byte) []mdScannedLine {
	if len(source) == 0 {
		return nil
	}
	out := make([]mdScannedLine, 0, bytes.Count(source, []byte{'\n'})+1)

	var (
		inFence          bool
		fenceChar        byte
		fenceLen         int
		inFrontmatter    bool
		frontmatterDelim []byte
	)

	start := 0
	lineIdx := 0
	for start < len(source) {
		idx := bytes.IndexByte(source[start:], '\n')
		var end int
		if idx >= 0 {
			end = start + idx + 1
		} else {
			end = len(source)
		}

		line := source[start:end]
		body := mdStripTrailingNewline(line)

		var kind mdLineKind
		switch {
		case inFrontmatter:
			if mdIsFrontmatterClose(body, frontmatterDelim) {
				inFrontmatter = false
				kind = mdLineFrontmatterDelim
			} else {
				kind = mdLineFrontmatterInside
			}
		case inFence:
			if fc, fl := mdDetectCodeFence(body); fc != 0 && fc == fenceChar && fl >= fenceLen {
				inFence = false
				fenceChar, fenceLen = 0, 0
				kind = mdLineCodeFence
			} else {
				kind = mdLineCodeInside
			}
		default:
			if lineIdx == 0 && mdIsFrontmatterOpen(body) {
				inFrontmatter = true
				frontmatterDelim = bytes.TrimRight(body, " \t")
				kind = mdLineFrontmatterDelim
				break
			}
			if fc, fl := mdDetectCodeFence(body); fc != 0 {
				inFence = true
				fenceChar, fenceLen = fc, fl
				kind = mdLineCodeFence
				break
			}
			kind = mdClassifyContentLine(body)
		}

		out = append(out, mdScannedLine{kind: kind, start: start, end: end})
		start = end
		lineIdx++
	}
	return out
}

func mdStripTrailingNewline(line []byte) []byte {
	if n := len(line); n > 0 && line[n-1] == '\n' {
		if n >= 2 && line[n-2] == '\r' {
			return line[:n-2]
		}
		return line[:n-1]
	}
	return line
}

func mdClassifyContentLine(body []byte) mdLineKind {
	trimmed := bytes.TrimLeft(body, " \t")
	if len(trimmed) == 0 {
		return mdLineBlank
	}
	if trimmed[0] == '#' {
		hashCount := 0
		for hashCount < len(trimmed) && hashCount < 7 && trimmed[hashCount] == '#' {
			hashCount++
		}
		if hashCount >= 1 && hashCount <= 6 {
			if hashCount == len(trimmed) || trimmed[hashCount] == ' ' || trimmed[hashCount] == '\t' {
				return mdLineHeadingATX
			}
		}
	}
	if (trimmed[0] == '=' || trimmed[0] == '-') && mdIsAllSameNonEmpty(trimmed, trimmed[0]) {
		return mdLineSetextUnderline
	}
	if mdIsTableSeparator(trimmed) {
		return mdLineTableSep
	}
	if mdIsTableRow(trimmed) {
		return mdLineTableRow
	}
	if mdIsListMarker(trimmed) {
		return mdLineList
	}
	return mdLineText
}

func mdDetectCodeFence(body []byte) (byte, int) {
	i := 0
	for i < len(body) && i < 3 && body[i] == ' ' {
		i++
	}
	if i >= len(body) {
		return 0, 0
	}
	c := body[i]
	if c != '`' && c != '~' {
		return 0, 0
	}
	runLen := 0
	for i < len(body) && body[i] == c {
		runLen++
		i++
	}
	if runLen < 3 {
		return 0, 0
	}
	return c, runLen
}

func mdIsFrontmatterOpen(body []byte) bool {
	trimmed := bytes.TrimRight(body, " \t")
	return len(trimmed) == 3 && ((trimmed[0] == '-' && trimmed[1] == '-' && trimmed[2] == '-') || (trimmed[0] == '+' && trimmed[1] == '+' && trimmed[2] == '+'))
}

func mdIsFrontmatterClose(body, delim []byte) bool {
	trimmed := bytes.TrimRight(body, " \t")
	return bytes.Equal(trimmed, delim)
}

func mdIsAllSameNonEmpty(s []byte, c byte) bool {
	t := bytes.TrimRight(s, " \t")
	if len(t) == 0 {
		return false
	}
	for _, b := range t {
		if b != c {
			return false
		}
	}
	return true
}

func mdIsTableRow(trimmed []byte) bool {
	if len(trimmed) < 2 || trimmed[0] != '|' {
		return false
	}
	return bytes.IndexByte(trimmed[1:], '|') >= 0
}

func mdIsTableSeparator(trimmed []byte) bool {
	if len(trimmed) == 0 {
		return false
	}
	if bytes.IndexByte(trimmed, '-') < 0 || bytes.IndexByte(trimmed, '|') < 0 {
		return false
	}
	for _, b := range trimmed {
		switch b {
		case '|', '-', ':', ' ', '\t':
			continue
		default:
			return false
		}
	}
	return true
}

func mdIsListMarker(trimmed []byte) bool {
	if len(trimmed) == 0 {
		return false
	}
	c := trimmed[0]
	if c == '-' || c == '*' || c == '+' {
		return len(trimmed) >= 2 && (trimmed[1] == ' ' || trimmed[1] == '\t')
	}
	if c >= '0' && c <= '9' {
		i := 0
		for i < len(trimmed) && i < 9 && trimmed[i] >= '0' && trimmed[i] <= '9' {
			i++
		}
		if i < len(trimmed) && (trimmed[i] == '.' || trimmed[i] == ')') {
			i++
			if i < len(trimmed) && (trimmed[i] == ' ' || trimmed[i] == '\t') {
				return true
			}
		}
	}
	return false
}

// mdHeadingText extracts an ATX heading line's text: strip the leading
// `#`s + one separating space/tab, then strip a trailing closing sequence
// of `#`s if preceded by whitespace (CommonMark allows a closing sequence
// of any length), then trim surrounding whitespace.
func mdHeadingText(body []byte) string {
	trimmed := bytes.TrimLeft(body, " \t")
	i := 0
	for i < len(trimmed) && trimmed[i] == '#' {
		i++
	}
	rest := bytes.TrimLeft(trimmed[i:], " \t")
	rest = bytes.TrimRight(rest, " \t")
	// Strip an optional closing #-sequence: "Title ###" -> "Title".
	if j := len(rest); j > 0 {
		k := j
		for k > 0 && rest[k-1] == '#' {
			k--
		}
		if k < j && k > 0 && (rest[k-1] == ' ' || rest[k-1] == '\t') {
			rest = bytes.TrimRight(rest[:k], " \t")
		} else if k == 0 {
			// The whole line (after the opening hashes) was itself hashes.
			rest = rest[:0]
		}
	}
	return string(rest)
}

// mdHeadingBoundary describes one heading-starting line.
type mdHeadingBoundary struct {
	lineIdx int // 0-based index into the scanned-lines slice
}

// mdSectionBoundaries finds every section start: index 0 (always), every
// ATX heading line, and every line promoted to a setext heading (a
// mdLineText line immediately followed by a setext-underline line — the
// exact promotion rule aikit/chunk/markdown's aggregator applies, so a
// list/table/blank line is never eligible, matching the real chunker).
func mdSectionBoundaries(lines []mdScannedLine) []mdHeadingBoundary {
	var out []mdHeadingBoundary
	if len(lines) == 0 {
		return out
	}
	out = append(out, mdHeadingBoundary{lineIdx: 0})
	for i := 1; i < len(lines); i++ {
		ln := lines[i]
		switch {
		case ln.kind == mdLineHeadingATX:
			out = append(out, mdHeadingBoundary{lineIdx: i})
		case ln.kind == mdLineText && i+1 < len(lines) && lines[i+1].kind == mdLineSetextUnderline:
			out = append(out, mdHeadingBoundary{lineIdx: i})
		}
	}
	return out
}

// mdResolveHeading fills in level/text for a boundary whose line is an
// actual heading (ATX or setext-promoted). Returns ok=false for the
// headerless-preamble boundary (only possible at lineIdx 0).
func mdResolveHeading(source []byte, lines []mdScannedLine, b mdHeadingBoundary) (level int, text string, ok bool) {
	ln := lines[b.lineIdx]
	switch {
	case ln.kind == mdLineHeadingATX:
		body := mdStripTrailingNewline(source[ln.start:ln.end])
		trimmed := bytes.TrimLeft(body, " \t")
		lvl := 0
		for lvl < len(trimmed) && trimmed[lvl] == '#' {
			lvl++
		}
		return lvl, mdHeadingText(body), true
	case ln.kind == mdLineText && b.lineIdx+1 < len(lines) && lines[b.lineIdx+1].kind == mdLineSetextUnderline:
		body := mdStripTrailingNewline(source[ln.start:ln.end])
		underline := mdStripTrailingNewline(source[lines[b.lineIdx+1].start:lines[b.lineIdx+1].end])
		trimmedUnderline := bytes.TrimRight(bytes.TrimLeft(underline, " \t"), " \t")
		lvl := 2
		if len(trimmedUnderline) > 0 && trimmedUnderline[0] == '=' {
			lvl = 1
		}
		return lvl, string(bytes.TrimSpace(body)), true
	default:
		return 0, "", false
	}
}

// parseMdSections scans source and returns its sections in file order.
func parseMdSections(source []byte) []Section {
	lines := scanMdLines(source)
	if len(lines) == 0 {
		return nil
	}
	boundaries := mdSectionBoundaries(lines)

	var stack []struct {
		level int
		text  string
	}
	out := make([]Section, 0, len(boundaries))
	for k, b := range boundaries {
		endIdx := len(lines)
		if k+1 < len(boundaries) {
			endIdx = boundaries[k+1].lineIdx
		}
		startLine := b.lineIdx + 1
		endLine := endIdx // endIdx is the count of lines included (1-based last line = endIdx)

		sectionText := string(source[lines[b.lineIdx].start:lines[endIdx-1].end])

		level, text, ok := mdResolveHeading(source, lines, b)
		var path []string
		if ok {
			for len(stack) > 0 && stack[len(stack)-1].level >= level {
				stack = stack[:len(stack)-1]
			}
			stack = append(stack, struct {
				level int
				text  string
			}{level, text})
			path = make([]string, len(stack))
			for i, s := range stack {
				path[i] = s.text
			}
		}
		// level stays 0 and path stays nil for the headerless preamble.

		out = append(out, Section{
			HeadingPath:  path,
			Level:        level,
			StartLine:    startLine,
			EndLine:      endLine,
			ApproxTokens: ApproxTokens(sectionText),
		})
	}
	return out
}

// sectionsFromDisk reads relPath (repo-relative, under w.root) and parses
// its markdown sections.
func (w *Workspace) sectionsFromDisk(relPath string) ([]Section, error) {
	data, err := os.ReadFile(filepath.Join(w.root, relPath))
	if err != nil {
		return nil, fmt.Errorf("engine: read %s: %w", relPath, err)
	}
	return parseMdSections(data), nil
}

// Sections returns the ordered markdown sections of path, a repo-relative
// file already present in the index. Section boundaries agree with the
// chunk boundaries the same file's Hits use: every Hit's line range falls
// inside exactly one returned Section with the same HeadingPath. The text
// before the first heading (if any) is returned as one Section with an
// empty HeadingPath.
func (w *Workspace) Sections(ctx context.Context, path string) ([]Section, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("engine: sections: %w", err)
	}
	ix := w.index.Load()
	if !indexHasFile(ix, path) {
		return nil, fmt.Errorf("engine: %s is not in the index", path)
	}
	return w.sectionsFromDisk(path)
}

func indexHasFile(ix *Index, path string) bool {
	for _, c := range ix.Chunks() {
		if !c.Tombstoned && c.File == path {
			return true
		}
	}
	return false
}

// headingPathFor returns the HeadingPath of the section containing line in
// file, or nil for a non-markdown file or any read/parse failure (best
// effort — a bad file never fails the whole search). cache memoizes
// per-file section parses across the hits in one toHits call.
func (w *Workspace) headingPathFor(file string, line int, cache map[string][]Section) []string {
	if !markdown.IsMarkdownPath(file) {
		return nil
	}
	secs, ok := cache[file]
	if !ok {
		secs, _ = w.sectionsFromDisk(file)
		cache[file] = secs
	}
	for _, s := range secs {
		if line >= s.StartLine && line <= s.EndLine {
			return s.HeadingPath
		}
	}
	return nil
}
