package sql

import (
	"bytes"
	"strings"
)

// statementKind classifies a top-level SQL statement at dispatch time.
type statementKind int

const (
	stmtUnknown statementKind = iota
	stmtCreateTable
	stmtCreateIndex
	stmtCreateView
	stmtAlterTable
)

// statement is one top-level DDL statement extracted from a file. start
// and end are byte offsets into the original source (end exclusive,
// includes the terminating ';' and any trailing whitespace up to the
// next statement). startLine is the 1-based line number of start.
type statement struct {
	kind      statementKind
	body      []byte // source slice [start:end], including the terminating ;
	start     int
	end       int
	startLine int
}

// splitStatements walks source and emits top-level statements separated
// by `;`. The splitter is aware of:
//
//   - line comments (`-- ...` to newline)
//   - block comments (`/* ... */`, NESTED)
//   - single-quoted strings (`'...'` with `”` escape)
//   - double-quoted identifiers (`"..."` with `""` escape)
//   - Postgres dollar-quoted strings (`$$...$$` and `$tag$...$tag$`)
//   - parenthesis depth (a `;` inside `(...)` does not end the statement)
//
// Statement kind is dispatched by sniffing the first 1-3 tokens (after
// any leading whitespace and comments are skipped).
//
// Malformed input (unterminated string, unbalanced parens) terminates
// the in-progress statement at EOF — the caller logs and continues.
func splitStatements(source []byte) []statement {
	var out []statement

	i := 0
	n := len(source)
	stmtStart := -1 // byte offset of the first non-whitespace, non-comment byte of the current statement
	parenDepth := 0
	curLine := 1

	for i < n {
		c := source[i]

		// Track lines for the statement's start position.
		if c == '\n' {
			curLine++
			i++
			if stmtStart == -1 {
				// Still skipping leading whitespace before a statement.
			}
			continue
		}

		// Skip whitespace at the boundary between statements.
		if stmtStart == -1 && (c == ' ' || c == '\t' || c == '\r') {
			i++
			continue
		}

		// Line comment.
		if c == '-' && i+1 < n && source[i+1] == '-' {
			j := i + 2
			for j < n && source[j] != '\n' {
				j++
			}
			i = j
			continue
		}

		// Block comment (with nesting).
		if c == '/' && i+1 < n && source[i+1] == '*' {
			depth := 1
			j := i + 2
			for j < n && depth > 0 {
				if j+1 < n && source[j] == '/' && source[j+1] == '*' {
					depth++
					j += 2
					continue
				}
				if j+1 < n && source[j] == '*' && source[j+1] == '/' {
					depth--
					j += 2
					continue
				}
				if source[j] == '\n' {
					curLine++
				}
				j++
			}
			i = j
			continue
		}

		// First content byte of a statement — record where it starts.
		if stmtStart == -1 {
			stmtStart = i
		}

		// Single-quoted string.
		if c == '\'' {
			i = skipSingleQuoted(source, i)
			continue
		}

		// Double-quoted identifier.
		if c == '"' {
			i = skipDoubleQuoted(source, i)
			continue
		}

		// Dollar-quoted string. Tag is the identifier between the two $s.
		if c == '$' {
			if end, ok := skipDollarQuoted(source, i); ok {
				// Advance line counter for any \n inside.
				for k := i; k < end; k++ {
					if source[k] == '\n' {
						curLine++
					}
				}
				i = end
				continue
			}
		}

		// Parens.
		if c == '(' {
			parenDepth++
			i++
			continue
		}
		if c == ')' {
			if parenDepth > 0 {
				parenDepth--
			}
			i++
			continue
		}

		// Statement terminator at top level.
		if c == ';' && parenDepth == 0 {
			end := i + 1
			body := source[stmtStart:end]
			out = append(out, statement{
				kind:      classifyStatement(body),
				body:      body,
				start:     stmtStart,
				end:       end,
				startLine: curLine - countNewlines(source[stmtStart:end]),
			})
			stmtStart = -1
			parenDepth = 0
			i = end
			continue
		}

		i++
	}

	// Trailing statement without terminating ; — accept it (some files
	// omit the final ; on the last statement; treating it as a full
	// statement is the more useful behavior).
	if stmtStart != -1 && stmtStart < n {
		body := bytes.TrimRight(source[stmtStart:], " \t\r\n")
		if len(body) > 0 {
			out = append(out, statement{
				kind:      classifyStatement(body),
				body:      body,
				start:     stmtStart,
				end:       stmtStart + len(body),
				startLine: curLine - countNewlines(body),
			})
		}
	}

	return out
}

// skipSingleQuoted returns the byte index just past the closing quote
// for a `'...'` string starting at source[i] (i points at the opening
// `'`). Handles the SQL escape `”` (two single quotes = one literal).
// Unterminated strings return len(source).
func skipSingleQuoted(source []byte, i int) int {
	n := len(source)
	j := i + 1
	for j < n {
		if source[j] == '\'' {
			if j+1 < n && source[j+1] == '\'' {
				j += 2 // escaped quote
				continue
			}
			return j + 1
		}
		j++
	}
	return n
}

// skipDoubleQuoted is skipSingleQuoted for `"..."` quoted identifiers.
// Handles `""` as an escaped quote inside an identifier.
func skipDoubleQuoted(source []byte, i int) int {
	n := len(source)
	j := i + 1
	for j < n {
		if source[j] == '"' {
			if j+1 < n && source[j+1] == '"' {
				j += 2
				continue
			}
			return j + 1
		}
		j++
	}
	return n
}

// skipDollarQuoted handles Postgres dollar-quoting: $$ ... $$ or
// $tag$ ... $tag$ where tag is an identifier (letters/digits/underscore;
// must start with a letter or underscore). Returns (endIndex, true) on
// match; (i, false) if the candidate $ does not begin a dollar-quoted
// literal (e.g. a bare `$1` parameter reference).
//
// Returns endIndex pointing just past the closing tag.
func skipDollarQuoted(source []byte, i int) (int, bool) {
	n := len(source)
	if i >= n || source[i] != '$' {
		return i, false
	}
	// Scan the tag: $[ident]$. Tag may be empty (i.e. $$).
	j := i + 1
	for j < n {
		c := source[j]
		if c == '$' {
			break
		}
		if !isIdentByte(c, j == i+1) {
			return i, false
		}
		j++
	}
	if j >= n || source[j] != '$' {
		return i, false
	}
	tag := source[i : j+1] // including both $ delimiters
	bodyStart := j + 1
	k := bodyStart
	for k+len(tag) <= n {
		if source[k] == '$' && bytes.HasPrefix(source[k:], tag) {
			return k + len(tag), true
		}
		k++
	}
	// Unterminated — consume to EOF.
	return n, true
}

// isIdentByte reports whether c is allowed in a Postgres dollar-quote
// tag. first==true means we're checking the first byte (which must be a
// letter or underscore; digits are allowed only after).
func isIdentByte(c byte, first bool) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_':
		return true
	case c >= '0' && c <= '9':
		return !first
	}
	return false
}

// countNewlines is the small newline counter used to back out the
// starting line number from the byte position at statement end.
func countNewlines(b []byte) int {
	n := 0
	for _, c := range b {
		if c == '\n' {
			n++
		}
	}
	return n
}

// classifyStatement dispatches a statement body to one of the known
// kinds by sniffing the first 1-3 keywords. Case-insensitive.
func classifyStatement(body []byte) statementKind {
	tokens := firstKeywords(body, 4)
	if len(tokens) == 0 {
		return stmtUnknown
	}
	switch tokens[0] {
	case "CREATE":
		if len(tokens) < 2 {
			return stmtUnknown
		}
		// Skip optional modifiers: OR REPLACE, UNIQUE, MATERIALIZED, TEMP/TEMPORARY.
		i := 1
		for i < len(tokens) && isCreateModifier(tokens[i]) {
			// "OR REPLACE" is two words.
			if tokens[i] == "OR" && i+1 < len(tokens) && tokens[i+1] == "REPLACE" {
				i += 2
				continue
			}
			i++
		}
		if i >= len(tokens) {
			return stmtUnknown
		}
		switch tokens[i] {
		case "TABLE":
			return stmtCreateTable
		case "INDEX":
			return stmtCreateIndex
		case "VIEW":
			return stmtCreateView
		}
		return stmtUnknown
	case "ALTER":
		if len(tokens) >= 2 && tokens[1] == "TABLE" {
			return stmtAlterTable
		}
		return stmtUnknown
	}
	return stmtUnknown
}

// isCreateModifier reports whether a token is a known modifier between
// CREATE and the object type (e.g. UNIQUE in CREATE UNIQUE INDEX).
func isCreateModifier(tok string) bool {
	switch tok {
	case "OR", "REPLACE", "UNIQUE", "MATERIALIZED", "TEMP", "TEMPORARY", "GLOBAL", "LOCAL":
		return true
	}
	return false
}

// firstKeywords returns up to max ASCII-only uppercased word tokens from
// the beginning of body, skipping whitespace and the comments the
// splitter already handled but a per-statement re-scan might still
// encounter (line comments before the first keyword, for instance).
// Strictly ASCII; identifiers with non-ASCII are not classification
// targets.
func firstKeywords(body []byte, max int) []string {
	var out []string
	i := 0
	n := len(body)
	for i < n && len(out) < max {
		c := body[i]
		// Skip whitespace.
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
			i++
			continue
		}
		// Skip line comment.
		if c == '-' && i+1 < n && body[i+1] == '-' {
			for i < n && body[i] != '\n' {
				i++
			}
			continue
		}
		// Skip block comment (single-level; nested handled by splitter
		// already, but a defensive single-level skip here is harmless).
		if c == '/' && i+1 < n && body[i+1] == '*' {
			i += 2
			for i+1 < n && !(body[i] == '*' && body[i+1] == '/') {
				i++
			}
			i += 2
			continue
		}
		// Word start.
		if isAlpha(c) || c == '_' {
			j := i + 1
			for j < n && (isAlpha(body[j]) || isDigit(body[j]) || body[j] == '_') {
				j++
			}
			out = append(out, asciiUpper(body[i:j]))
			i = j
			continue
		}
		// Anything else (parens, quotes, punctuation) — first token
		// wasn't a keyword, abort.
		break
	}
	return out
}

func isAlpha(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// token is one lexical token (identifier, keyword, quoted name, number,
// punctuation) extracted from a statement body. text preserves the
// original surface form (mixed case, quotes intact for "..." names);
// classification is done case-insensitively by the per-statement
// parsers. start is the byte offset within the body the token was sliced
// from — used to locate the matching ')' for a '(' token, etc.
type token struct {
	text  string
	start int
}

// tokenize produces a token stream over body. Skips comments and
// whitespace; emits identifiers/keywords/numbers as single tokens, quoted
// names ("..."), single-quoted strings ('...'), and dollar-quoted
// strings as themselves (with delimiters), and punctuation
// (`(`, `)`, `,`, `;`, `.`) as single-byte tokens.
//
// This is deliberately a "good enough" lexer — the per-statement parsers
// only need keyword recognition, identifier extraction, and paren
// matching; they never compute on number literals or string contents.
func tokenize(body []byte) []token {
	var out []token
	i := 0
	n := len(body)
	for i < n {
		c := body[i]
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			i++
		case c == '-' && i+1 < n && body[i+1] == '-':
			for i < n && body[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && body[i+1] == '*':
			d := 1
			j := i + 2
			for j < n && d > 0 {
				if j+1 < n && body[j] == '/' && body[j+1] == '*' {
					d++
					j += 2
					continue
				}
				if j+1 < n && body[j] == '*' && body[j+1] == '/' {
					d--
					j += 2
					continue
				}
				j++
			}
			i = j
		case c == '\'':
			end := skipSingleQuoted(body, i)
			out = append(out, token{text: string(body[i:end]), start: i})
			i = end
		case c == '"':
			end := skipDoubleQuoted(body, i)
			out = append(out, token{text: string(body[i:end]), start: i})
			i = end
		case c == '$':
			if end, ok := skipDollarQuoted(body, i); ok {
				out = append(out, token{text: string(body[i:end]), start: i})
				i = end
				continue
			}
			out = append(out, token{text: string(body[i : i+1]), start: i})
			i++
		case c == '(' || c == ')' || c == ',' || c == ';' || c == '.':
			out = append(out, token{text: string(body[i : i+1]), start: i})
			i++
		case isAlpha(c) || c == '_':
			j := i + 1
			for j < n && (isAlpha(body[j]) || isDigit(body[j]) || body[j] == '_') {
				j++
			}
			out = append(out, token{text: string(body[i:j]), start: i})
			i = j
		case isDigit(c):
			j := i + 1
			for j < n && (isDigit(body[j]) || body[j] == '.') {
				j++
			}
			out = append(out, token{text: string(body[i:j]), start: i})
			i = j
		default:
			// Single-byte operator-ish characters (=, +, -, *, etc.).
			// Emit as a one-byte token so the per-statement parsers can
			// at least navigate past them.
			out = append(out, token{text: string(body[i : i+1]), start: i})
			i++
		}
	}
	return out
}

// nextEqIgnoreCase reports whether tokens[i] exists and matches s
// case-insensitively. Out-of-range indices return false.
func nextEqIgnoreCase(tokens []token, i int, s string) bool {
	if i < 0 || i >= len(tokens) {
		return false
	}
	return strings.EqualFold(tokens[i].text, s)
}

// matchSeqIgnoreCase reports whether tokens[i:i+len(words)] matches
// words case-insensitively in order.
func matchSeqIgnoreCase(tokens []token, i int, words ...string) bool {
	if i+len(words) > len(tokens) {
		return false
	}
	for k, w := range words {
		if !strings.EqualFold(tokens[i+k].text, w) {
			return false
		}
	}
	return true
}

// asciiUpper uppercases an ASCII byte slice into a string. Identifiers
// containing non-ASCII bytes are returned unchanged for non-ASCII bytes
// (we never compare those against keyword constants).
func asciiUpper(b []byte) string {
	out := make([]byte, len(b))
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			out[i] = c - 32
		} else {
			out[i] = c
		}
	}
	return string(out)
}
