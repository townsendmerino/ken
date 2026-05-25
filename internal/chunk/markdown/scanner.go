package markdown

import (
	"bytes"
)

// lineKind is the per-line classification produced by the scanner. Used
// by the aggregator (markdown.go) to decide where chunks should break.
type lineKind int

const (
	// lineText is an ordinary prose or paragraph line. Default classification.
	lineText lineKind = iota
	// lineBlank is empty or whitespace-only — separates paragraphs.
	lineBlank
	// lineHeadingATX matches `#` to `######` followed by space, tab, or EOL.
	// Always a section boundary.
	lineHeadingATX
	// lineSetextUnderline is a `===+` or `---+` line that, when preceded by
	// a non-blank text line, promotes that prior line to a heading.
	// Detected speculatively in the first pass; the aggregator decides
	// whether to honor it based on the prior line's kind.
	lineSetextUnderline
	// lineCodeFence is a ``` or ~~~ line — toggles fenced-code state. The
	// open and close fence lines are both classified lineCodeFence; lines
	// strictly inside the fence are lineCodeInside.
	lineCodeFence
	// lineCodeInside is any line that appears between an opening and the
	// matching closing fence. NEVER a heading or boundary even if it
	// happens to start with `#`.
	lineCodeInside
	// lineFrontmatterDelim is the opening or closing `---`/`+++` of a YAML
	// or TOML frontmatter block. Recognized only at the very top of the
	// file (line 0) and again when closing.
	lineFrontmatterDelim
	// lineFrontmatterInside is any line inside frontmatter.
	lineFrontmatterInside
	// lineTableSep is a |---|---|...  separator row that, combined with a
	// preceding lineTableRow, marks the start of a table block.
	lineTableSep
	// lineTableRow is a |...|...| row (header or body).
	lineTableRow
	// lineList is a list-item-starting line: `- `, `* `, `+ `, or `N. `.
	lineList
)

// scannedLine is one line of source after classification. start/end are
// byte offsets into the original source; end is exclusive and INCLUDES
// the trailing newline (or \r\n) if present, so concatenating Text
// substrings reproduces the source.
type scannedLine struct {
	kind  lineKind
	start int
	end   int
}

// scanLines classifies every line in source. Line ends recognize both
// `\n` and `\r\n`; line starts use byte offsets so the aggregator can
// build chunks that preserve byte fidelity (concat == source).
func scanLines(source []byte) []scannedLine {
	if len(source) == 0 {
		return nil
	}

	// Pass 1: walk the bytes and produce raw [start, end) ranges per line.
	type lr struct{ start, end int }
	var ranges []lr
	{
		start := 0
		for i := range source {
			if source[i] == '\n' {
				ranges = append(ranges, lr{start, i + 1})
				start = i + 1
			}
		}
		if start < len(source) {
			// File doesn't end with a newline — trailing partial line.
			ranges = append(ranges, lr{start, len(source)})
		}
	}

	// Pass 2: classify each line with fence + frontmatter state carried forward.
	out := make([]scannedLine, len(ranges))
	var (
		inFence          bool
		fenceChar        byte
		fenceLen         int
		inFrontmatter    bool
		frontmatterDelim []byte // "---\n", "---\r\n", "+++\n", "+++\r\n"
	)
	for i, r := range ranges {
		line := source[r.start:r.end]
		body := stripTrailingNewline(line)

		switch {
		case inFrontmatter:
			// Inside frontmatter until we see the matching close delim.
			if isFrontmatterClose(body, frontmatterDelim) {
				inFrontmatter = false
				out[i] = scannedLine{lineFrontmatterDelim, r.start, r.end}
			} else {
				out[i] = scannedLine{lineFrontmatterInside, r.start, r.end}
			}
		case inFence:
			// Inside a fenced code block. Only a matching close fence
			// (same character, length >= opening length) ends it. We
			// classify the close fence itself as lineCodeFence so the
			// aggregator sees a contiguous fenced-code block.
			if fc, fl := detectCodeFence(body); fc != 0 && fc == fenceChar && fl >= fenceLen {
				inFence = false
				fenceChar, fenceLen = 0, 0
				out[i] = scannedLine{lineCodeFence, r.start, r.end}
			} else {
				out[i] = scannedLine{lineCodeInside, r.start, r.end}
			}
		default:
			// Frontmatter only opens on line 0 and only when the line is
			// exactly `---` or `+++` (with optional CRLF).
			if i == 0 && isFrontmatterOpen(body) {
				inFrontmatter = true
				frontmatterDelim = append([]byte(nil), body...)
				out[i] = scannedLine{lineFrontmatterDelim, r.start, r.end}
				break
			}
			if fc, fl := detectCodeFence(body); fc != 0 {
				inFence = true
				fenceChar, fenceLen = fc, fl
				out[i] = scannedLine{lineCodeFence, r.start, r.end}
				break
			}
			out[i] = scannedLine{classifyContentLine(body), r.start, r.end}
		}
	}
	return out
}

// stripTrailingNewline returns line without the trailing \n or \r\n.
// Used everywhere a "line content" view is needed without the line
// terminator confusing the classifier.
func stripTrailingNewline(line []byte) []byte {
	if n := len(line); n > 0 && line[n-1] == '\n' {
		if n >= 2 && line[n-2] == '\r' {
			return line[:n-2]
		}
		return line[:n-1]
	}
	return line
}

// classifyContentLine classifies a line body (no trailing newline) that
// is NOT inside a fenced code block or frontmatter. Detects headings,
// setext underlines, tables, lists, and the blank/text fallback.
func classifyContentLine(body []byte) lineKind {
	trimmed := bytes.TrimLeft(body, " \t")
	if len(trimmed) == 0 {
		return lineBlank
	}

	// ATX heading: 1-6 `#`s followed by space/tab/EOL. We deliberately
	// reject `#foo` (no space) to match CommonMark.
	if trimmed[0] == '#' {
		hashCount := 0
		for hashCount < len(trimmed) && hashCount < 7 && trimmed[hashCount] == '#' {
			hashCount++
		}
		if hashCount >= 1 && hashCount <= 6 {
			if hashCount == len(trimmed) || trimmed[hashCount] == ' ' || trimmed[hashCount] == '\t' {
				return lineHeadingATX
			}
		}
	}

	// Setext underline: only `=`s or only `-`s, with at least one char.
	if isAllSameNonEmpty(trimmed, '=') || isAllSameNonEmpty(trimmed, '-') {
		return lineSetextUnderline
	}

	// Table classification: a row starts with `|` (after optional leading
	// whitespace) and contains another `|`. The separator row additionally
	// only contains `|`, `-`, `:`, and whitespace. The aggregator
	// distinguishes header+separator vs free-floating pipe text by the
	// pairing rule (a separator must follow a row to be a table).
	if isTableSeparator(trimmed) {
		return lineTableSep
	}
	if isTableRow(trimmed) {
		return lineTableRow
	}

	// List item: `-`, `*`, `+`, or `N.` / `N)` followed by space/tab.
	if isListMarker(trimmed) {
		return lineList
	}

	return lineText
}

// detectCodeFence returns (fenceChar, runLen) if body is a code fence
// line (3+ consecutive ` or ~ with optional info string after). Returns
// (0, 0) otherwise. Leading whitespace up to 3 spaces is allowed.
func detectCodeFence(body []byte) (byte, int) {
	// CommonMark allows 0-3 spaces of indent before a fence.
	i := 0
	for i < len(body) && i < 4 && body[i] == ' ' {
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

// isFrontmatterOpen reports whether body is exactly `---` or `+++` (after
// optional trailing whitespace). Only ever called for line 0.
func isFrontmatterOpen(body []byte) bool {
	trimmed := bytes.TrimRight(body, " \t")
	return bytes.Equal(trimmed, []byte("---")) || bytes.Equal(trimmed, []byte("+++"))
}

// isFrontmatterClose reports whether body matches the opening delim
// (body == "---" if opened with ---, body == "+++" if opened with +++).
// delim is the opening line body (already stripped of trailing newline).
func isFrontmatterClose(body, delim []byte) bool {
	trimmed := bytes.TrimRight(body, " \t")
	return bytes.Equal(trimmed, delim)
}

// isAllSameNonEmpty reports whether s contains only repetitions of c and
// is non-empty (after right-trimming whitespace). Used for setext
// underline detection (=== or ---).
func isAllSameNonEmpty(s []byte, c byte) bool {
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

// isTableRow reports whether trimmed looks like a table row: starts with
// `|`, contains another `|`. Doesn't pair with a separator here — the
// aggregator handles that.
func isTableRow(trimmed []byte) bool {
	if len(trimmed) == 0 || trimmed[0] != '|' {
		return false
	}
	return bytes.Count(trimmed, []byte("|")) >= 2
}

// isTableSeparator reports whether trimmed is the |---|---| separator
// row: starts with optional `|`, contains only `|`, `-`, `:`, space.
func isTableSeparator(trimmed []byte) bool {
	if len(trimmed) == 0 {
		return false
	}
	// Must contain at least one `-` (otherwise a row of just `|` would qualify).
	if !bytes.ContainsRune(trimmed, '-') {
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
	// Must contain at least one `|` to actually look like a table separator.
	return bytes.ContainsRune(trimmed, '|')
}

// isListMarker reports whether trimmed begins with a list-item marker:
// `- `, `* `, `+ `, `N. `, or `N) ` (for N in 1..9999, CommonMark caps at
// 9 digits but a small bound is fine for chunking).
func isListMarker(trimmed []byte) bool {
	if len(trimmed) == 0 {
		return false
	}
	c := trimmed[0]
	if c == '-' || c == '*' || c == '+' {
		// Need at least 1 char after, AND that char must be space/tab,
		// AND followed by something (to distinguish `- foo` from `---`).
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
