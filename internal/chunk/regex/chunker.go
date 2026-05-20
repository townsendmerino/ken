// Package regex is the v1-default chunker (docs/DESIGN.md §2 Option C): one
// generic line-walking engine driven by per-language LanguageRules. It
// registers itself as "regex" via chunk.Register in init(); import it for
// side effects (internal/search does) — chunk must not import this package
// (import cycle), so registration is decoupled the database/sql way.
//
// Algorithm (docs/DESIGN.md §2 "Build path"): walk lines, mark the start of each
// top-level (or member-level) definition as a candidate boundary, greedily
// accumulate lines into a chunk, and when the next line would exceed
// chunkSize snap the cut back to the latest candidate boundary that still
// fits. If a single definition is itself larger than chunkSize there is no
// boundary to snap to, so it is split at line boundaries (the line-chunker
// fallback rule). Chunks are a contiguous, non-overlapping partition, so
// concatenating Text in order reproduces the source byte-for-byte.
package regex

import (
	"bytes"
	"regexp"
	"sort"

	"github.com/townsendmerino/ken/internal/chunk"
)

type strategy int

const (
	braceStrategy  strategy = iota // top-level ⇔ brace depth 0 (C-likes)
	indentStrategy                 // top-level ⇔ no leading whitespace (Python)
)

// scannerCfg tells the brace-depth scanner which literal/comment forms to
// skip so braces inside them do not perturb the depth count.
type scannerCfg struct {
	lineComment string // e.g. "//"
	dq          bool   // "double quoted" with \ escapes
	sq          bool   // 'single quoted' with \ escapes (char/rune/JS string)
	backtick    bool   // `raw / template` (Go raw, TS template)
	tripleQuote bool   // Java text blocks: """ ... """
	rustRaw     bool   // Rust raw strings: r"..." / r#"..."#
}

// LanguageRules is the per-language driver for the generic engine.
type LanguageRules struct {
	lang     string
	defs     []*regexp.Regexp // a (left-trimmed) line that starts a definition
	skip     []*regexp.Regexp // … unless it also matches one of these (control-flow lines that look defn-ish)
	attach   []*regexp.Regexp // lines that glue onto the following def (docs, annotations, attributes, decorators)
	strat    strategy
	maxDepth int        // brace strategy: a def is a boundary iff depthBefore ≤ maxDepth
	scan     scannerCfg // brace strategy only
}

// languageRules is populated by each per-language file's init().
var languageRules = map[string]LanguageRules{}

// Chunker implements chunk.Chunker.
type Chunker struct{ rules map[string]LanguageRules }

// New returns a Chunker over every registered language ruleset.
func New() *Chunker { return &Chunker{rules: languageRules} }

func init() { chunk.Register("regex", New()) }

func (*Chunker) Name() string { return "regex" }

func (c *Chunker) SupportedLanguages() []string {
	out := make([]string, 0, len(c.rules))
	for l := range c.rules {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

func (c *Chunker) Chunk(source []byte, language string, chunkSize int) ([]chunk.Chunk, error) {
	if chunkSize <= 0 {
		chunkSize = chunk.DefaultChunkSize
	}
	r, ok := c.rules[language]
	if !ok {
		// Defensive: ChunkFile routes unsupported languages to the line
		// chunker, so this is rare. Degrade to size-only splitting (no
		// defs ⇒ no boundaries) — still a valid byte-exact partition.
		r = LanguageRules{lang: language, strat: braceStrategy, maxDepth: -1}
	}
	return chunkWith(r, source, chunkSize), nil
}

func chunkWith(r LanguageRules, src []byte, chunkSize int) []chunk.Chunk {
	if len(src) == 0 {
		return nil
	}

	// lineStart[i] = byte offset of line i. A trailing '\n' does not start
	// a phantom empty line (matches the Stage 1 line chunker).
	lineStart := []int{0}
	for i := range src {
		if src[i] == '\n' && i+1 < len(src) {
			lineStart = append(lineStart, i+1)
		}
	}
	n := len(lineStart)
	off := func(k int) int {
		if k >= n {
			return len(src)
		}
		return lineStart[k]
	}
	rawLine := func(i int) []byte { return src[off(i):off(i+1)] }

	// Per-line "is this the start of a definition?" with attachment of the
	// preceding doc-comment / annotation / decorator block.
	var depth []int
	if r.strat == braceStrategy {
		depth = scanDepth(src, lineStart, r.scan)
	}
	isBoundary := make([]bool, n)
	for i := range n {
		line := rawLine(i)
		var probe []byte
		switch r.strat {
		case indentStrategy:
			// Top-level only: a definition with no leading whitespace.
			if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
				continue
			}
			probe = line
		default: // braceStrategy
			if r.maxDepth < 0 || depth[i] > r.maxDepth {
				continue
			}
			probe = bytes.TrimLeft(line, " \t")
		}
		if !anyMatch(r.defs, probe) || anyMatch(r.skip, probe) {
			continue
		}
		// Snap the boundary up over a contiguous attach block (so a
		// function keeps its doc comment / @annotation / #[attr] / @decorator).
		b := i
		for b-1 >= 1 && attachMatch(r, rawLine(b-1)) {
			b--
		}
		isBoundary[b] = true
	}

	var boundaries []int // sorted line indices > 0 where a chunk may start
	for i := 1; i < n; i++ {
		if isBoundary[i] {
			boundaries = append(boundaries, i)
		}
	}
	// lastBoundaryLE: greatest boundary b with lo < b ≤ hi, else -1.
	lastBoundaryLE := func(lo, hi int) int {
		j := sort.Search(len(boundaries), func(k int) bool { return boundaries[k] > hi })
		if j == 0 {
			return -1
		}
		if b := boundaries[j-1]; b > lo {
			return b
		}
		return -1
	}

	var out []chunk.Chunk
	emit := func(a, b int) {
		out = append(out, chunk.Chunk{
			StartLine: a + 1,
			EndLine:   b,
			Text:      string(src[off(a):off(b)]),
		})
	}

	start := 0
	for start < n {
		end := start + 1
		for end < n && off(end+1)-off(start) <= chunkSize {
			end++
		}
		if end >= n {
			emit(start, n)
			break
		}
		if b := lastBoundaryLE(start, end); b > start {
			emit(start, b) // snap the cut back to a definition boundary
			start = b
			continue
		}
		emit(start, end) // oversized single unit ⇒ line-split fallback
		start = end
	}
	return out
}

func anyMatch(res []*regexp.Regexp, line []byte) bool {
	for _, re := range res {
		if re.Match(line) {
			return true
		}
	}
	return false
}

// attachMatch reports whether line glues onto the following definition. A
// blank line never attaches (it separates a def from anything above it).
func attachMatch(r LanguageRules, line []byte) bool {
	probe := line
	if r.strat == braceStrategy {
		probe = bytes.TrimLeft(line, " \t")
	}
	if len(bytes.TrimSpace(probe)) == 0 {
		return false
	}
	return anyMatch(r.attach, probe)
}

// scanDepth returns the brace depth at the START of each line, ignoring
// braces inside comments and string/char literals. Best-effort: an
// undercount inside an exotic literal only yields a suboptimal boundary,
// never data loss (chunks are always a contiguous byte partition).
func scanDepth(src []byte, lineStart []int, cfg scannerCfg) []int {
	n := len(lineStart)
	depth := make([]int, n)
	cur := 0
	li := 0 // next line whose start we still need to record

	type st int
	const (
		normal st = iota
		lineCmt
		blockCmt
		inDq
		inSq
		inBt
		inTriple // Java text block
		inRawN   // Rust raw string, hashes counted in rawHashes
	)
	state := normal
	rawHashes := 0
	lc := cfg.lineComment

	atLineStart := func(pos int) {
		for li < n && lineStart[li] == pos {
			depth[li] = cur
			li++
		}
	}

	for i := 0; i < len(src); i++ {
		atLineStart(i)
		c := src[i]
		switch state {
		case normal:
			switch {
			case lc != "" && hasPrefixAt(src, i, lc):
				state = lineCmt
				i += len(lc) - 1
			case hasPrefixAt(src, i, "/*"):
				state = blockCmt
				i++
			case cfg.tripleQuote && hasPrefixAt(src, i, `"""`):
				state = inTriple
				i += 2
			case cfg.rustRaw && c == 'r' && (peek(src, i+1) == '"' || peek(src, i+1) == '#'):
				j := i + 1
				h := 0
				for peek(src, j) == '#' {
					h++
					j++
				}
				if peek(src, j) == '"' {
					rawHashes = h
					state = inRawN
					i = j
				}
			case cfg.dq && c == '"':
				state = inDq
			case cfg.sq && c == '\'':
				state = inSq
			case cfg.backtick && c == '`':
				state = inBt
			case c == '{':
				cur++
			case c == '}':
				if cur > 0 {
					cur--
				}
			}
		case lineCmt:
			if c == '\n' {
				state = normal
			}
		case blockCmt:
			if c == '*' && peek(src, i+1) == '/' {
				state = normal
				i++
			}
		case inDq:
			if c == '\\' {
				i++
			} else if c == '"' {
				state = normal
			}
		case inSq:
			if c == '\\' {
				i++
			} else if c == '\'' {
				state = normal
			}
		case inBt:
			if c == '`' {
				state = normal
			}
		case inTriple:
			if hasPrefixAt(src, i, `"""`) {
				state = normal
				i += 2
			}
		case inRawN:
			if c == '"' {
				j := i + 1
				h := 0
				for h < rawHashes && peek(src, j) == '#' {
					h++
					j++
				}
				if h == rawHashes {
					state = normal
					i = j - 1
				}
			}
		}
	}
	atLineStart(len(src))
	return depth
}

func hasPrefixAt(src []byte, i int, s string) bool {
	if i+len(s) > len(src) {
		return false
	}
	return string(src[i:i+len(s)]) == s
}

func peek(src []byte, i int) byte {
	if i < 0 || i >= len(src) {
		return 0
	}
	return src[i]
}
