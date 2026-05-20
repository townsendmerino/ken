// Package bm25 is the lexical (sparse) retriever — the bm25s-equivalent
// scorer plus the identifier-aware tokenizer that feeds it. Stage 1 of
// docs/DESIGN.md: the boring-but-correct foundation validated against semble's
// SearchMode.BM25.
package bm25

import (
	"strings"
	"unicode"
)

// Tokenize lowercases and splits text the way code search wants: it breaks
// on every non-alphanumeric rune, then further splits each run on
// camelCase, PascalCase, ACRONYMBoundary, and letter/digit transitions.
//
// When a run splits into more than one piece it ALSO emits the whole run
// (lowercased, separators already gone) so a query for "camelcase" still
// matches an identifier written "camelCase" — recall the splitter would
// otherwise lose.
func Tokenize(text string) []string {
	var out []string
	var run []rune
	flush := func() {
		if len(run) == 0 {
			return
		}
		parts := splitIdentifier(run)
		for _, p := range parts {
			out = append(out, strings.ToLower(p))
		}
		if len(parts) > 1 {
			out = append(out, strings.ToLower(string(run)))
		}
		run = run[:0]
	}
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			run = append(run, r)
		} else {
			flush()
		}
	}
	flush()
	return out
}

// splitIdentifier breaks one alphanumeric run at sub-token boundaries.
func splitIdentifier(rs []rune) []string {
	if len(rs) == 0 {
		return nil
	}
	var parts []string
	start := 0
	for i := 1; i < len(rs); i++ {
		if boundary(rs, i) {
			parts = append(parts, string(rs[start:i]))
			start = i
		}
	}
	return append(parts, string(rs[start:]))
}

// boundary reports whether a sub-token cut belongs between rs[i-1] and rs[i].
func boundary(rs []rune, i int) bool {
	p, c := rs[i-1], rs[i]
	switch {
	case !unicode.IsUpper(p) && unicode.IsUpper(c):
		// camelCase / digitToUpper: lower|Upper
		return true
	case unicode.IsUpper(p) && unicode.IsUpper(c) && i+1 < len(rs) && unicode.IsLower(rs[i+1]):
		// ACRONYM|Word: HTTPServer -> HTTP | Server
		return true
	case isAlpha(p) && unicode.IsDigit(c), unicode.IsDigit(p) && isAlpha(c):
		// letter<->digit: utf8 -> utf | 8 ; sha256sum -> sha | 256 | sum
		return true
	}
	return false
}

func isAlpha(r rune) bool { return unicode.IsLetter(r) }
