package sql

import (
	"bytes"
	"strings"
)

// tableDef is a parsed CREATE TABLE statement, structured enough to
// render a denormalized chunk. We DO NOT model the Postgres type system
// — column.rest is the literal source between the column name and the
// next comma (modulo whitespace collapse), e.g. "varchar(255) NOT NULL".
type tableDef struct {
	name        string      // schema-qualified if present, e.g. "public.users"
	columns     []columnDef // in declaration order
	constraints []string    // rendered constraint lines (CONSTRAINT ... / PRIMARY KEY ... / etc.)
	startLine   int         // 1-based line of the CREATE TABLE token
	endLine     int         // 1-based line of the closing );
}

type columnDef struct {
	name string
	rest string // type + modifiers, whitespace-collapsed
}

// indexDef is a parsed CREATE INDEX statement.
type indexDef struct {
	name      string
	table     string // un-qualified or schema-qualified, mirroring source
	unique    bool
	columns   string // raw column list as written, with surrounding parens stripped
	startLine int
	endLine   int
}

// viewDef is a parsed CREATE VIEW statement.
type viewDef struct {
	name      string
	body      string // AS body, trimmed; may be multi-line
	startLine int
	endLine   int
}

// parseCreateTable parses a CREATE TABLE statement. Returns (def, true)
// on success, (_, false) if the statement is too malformed to extract
// at least a name and a column list — caller logs and continues.
func parseCreateTable(s statement) (tableDef, bool) {
	tokens := tokenize(s.body)
	// Expect: CREATE [TEMP/TEMPORARY/UNLOGGED/GLOBAL/LOCAL] TABLE [IF NOT EXISTS] <name> (
	i := 0
	if !nextEqIgnoreCase(tokens, i, "CREATE") {
		return tableDef{}, false
	}
	i++
	for i < len(tokens) && isTableModifier(tokens[i].text) {
		i++
	}
	if !nextEqIgnoreCase(tokens, i, "TABLE") {
		return tableDef{}, false
	}
	i++
	// Optional IF NOT EXISTS.
	if matchSeqIgnoreCase(tokens, i, "IF", "NOT", "EXISTS") {
		i += 3
	}
	if i >= len(tokens) {
		return tableDef{}, false
	}
	name := tokens[i].text
	i++
	// Find the opening paren for the column list.
	for i < len(tokens) && tokens[i].text != "(" {
		// Some forms have additional ID parts (e.g. schema.name as
		// three tokens "schema" "." "name"). Stitch them into name.
		if tokens[i].text == "." && i+1 < len(tokens) {
			name = name + "." + tokens[i+1].text
			i += 2
			continue
		}
		i++
	}
	if i >= len(tokens) || tokens[i].text != "(" {
		return tableDef{}, false
	}
	// Slice the source bytes inside the outermost () for column-list parsing.
	parenStart := tokens[i].start
	parenEnd := matchParen(s.body, parenStart)
	if parenEnd < 0 {
		return tableDef{}, false
	}
	inner := s.body[parenStart+1 : parenEnd]

	columns, constraints := splitTableInner(inner)

	return tableDef{
		name:        name,
		columns:     columns,
		constraints: constraints,
		startLine:   s.startLine,
		endLine:     s.startLine + countNewlines(s.body),
	}, true
}

// parseCreateIndex parses CREATE [UNIQUE] INDEX [IF NOT EXISTS] <name> ON <table> (<cols>).
func parseCreateIndex(s statement) (indexDef, bool) {
	tokens := tokenize(s.body)
	i := 0
	if !nextEqIgnoreCase(tokens, i, "CREATE") {
		return indexDef{}, false
	}
	i++
	unique := false
	if i < len(tokens) && strings.EqualFold(tokens[i].text, "UNIQUE") {
		unique = true
		i++
	}
	if !nextEqIgnoreCase(tokens, i, "INDEX") {
		return indexDef{}, false
	}
	i++
	if matchSeqIgnoreCase(tokens, i, "IF", "NOT", "EXISTS") {
		i += 3
	}
	if i >= len(tokens) {
		return indexDef{}, false
	}
	name := tokens[i].text
	i++
	if !nextEqIgnoreCase(tokens, i, "ON") {
		return indexDef{}, false
	}
	i++
	if i >= len(tokens) {
		return indexDef{}, false
	}
	table := tokens[i].text
	i++
	// Pull schema-qualified name parts (schema.table).
	for i+1 < len(tokens) && tokens[i].text == "." {
		table = table + "." + tokens[i+1].text
		i += 2
	}
	// Optional USING <method>.
	if i < len(tokens) && strings.EqualFold(tokens[i].text, "USING") && i+1 < len(tokens) {
		i += 2
	}
	if i >= len(tokens) || tokens[i].text != "(" {
		return indexDef{}, false
	}
	parenStart := tokens[i].start
	parenEnd := matchParen(s.body, parenStart)
	if parenEnd < 0 {
		return indexDef{}, false
	}
	cols := strings.TrimSpace(string(s.body[parenStart+1 : parenEnd]))
	cols = collapseWhitespace(cols)
	return indexDef{
		name:      name,
		table:     table,
		unique:    unique,
		columns:   cols,
		startLine: s.startLine,
		endLine:   s.startLine + countNewlines(s.body),
	}, true
}

// parseCreateView parses CREATE [OR REPLACE] [MATERIALIZED] VIEW <name> AS <body>.
func parseCreateView(s statement) (viewDef, bool) {
	tokens := tokenize(s.body)
	i := 0
	if !nextEqIgnoreCase(tokens, i, "CREATE") {
		return viewDef{}, false
	}
	i++
	for i < len(tokens) && (strings.EqualFold(tokens[i].text, "OR") ||
		strings.EqualFold(tokens[i].text, "REPLACE") ||
		strings.EqualFold(tokens[i].text, "MATERIALIZED") ||
		strings.EqualFold(tokens[i].text, "TEMP") ||
		strings.EqualFold(tokens[i].text, "TEMPORARY") ||
		strings.EqualFold(tokens[i].text, "RECURSIVE")) {
		i++
	}
	if !nextEqIgnoreCase(tokens, i, "VIEW") {
		return viewDef{}, false
	}
	i++
	if matchSeqIgnoreCase(tokens, i, "IF", "NOT", "EXISTS") {
		i += 3
	}
	if i >= len(tokens) {
		return viewDef{}, false
	}
	name := tokens[i].text
	i++
	for i+1 < len(tokens) && tokens[i].text == "." {
		name = name + "." + tokens[i+1].text
		i += 2
	}
	// Skip optional (col_list) for view column aliases.
	if i < len(tokens) && tokens[i].text == "(" {
		parenStart := tokens[i].start
		parenEnd := matchParen(s.body, parenStart)
		if parenEnd < 0 {
			return viewDef{}, false
		}
		// Advance the token cursor past the closing paren.
		for i < len(tokens) && tokens[i].start <= parenEnd {
			i++
		}
	}
	if !nextEqIgnoreCase(tokens, i, "AS") {
		return viewDef{}, false
	}
	// Body is everything from after AS to the terminating ;.
	asEnd := tokens[i].start + len(tokens[i].text)
	body := s.body[asEnd:]
	body = bytes.TrimRight(body, " \t\r\n;")
	body = bytes.TrimLeft(body, " \t\r\n")
	return viewDef{
		name:      name,
		body:      string(body),
		startLine: s.startLine,
		endLine:   s.startLine + countNewlines(s.body),
	}, true
}

// splitTableInner splits the body of a CREATE TABLE (...) on top-level
// commas and classifies each segment as a column definition (first token
// is an identifier or quoted name) or a constraint (first token is one of
// CONSTRAINT, PRIMARY, FOREIGN, UNIQUE, CHECK, LIKE, EXCLUDE).
func splitTableInner(inner []byte) (cols []columnDef, constraints []string) {
	segments := splitTopLevelCommas(inner)
	for _, seg := range segments {
		seg = bytes.TrimSpace(seg)
		if len(seg) == 0 {
			continue
		}
		tokens := tokenize(seg)
		if len(tokens) == 0 {
			continue
		}
		first := strings.ToUpper(tokens[0].text)
		if isConstraintKeyword(first) {
			constraints = append(constraints, collapseWhitespace(string(seg)))
			continue
		}
		// Column definition: first token is the name; rest is type + modifiers.
		name := tokens[0].text
		// Schema-or-quoted names: a column won't typically be qualified
		// inside a CREATE TABLE body, but the quoted form `"weird name"`
		// shows up. Strip the quotes for the name field; keep the rest
		// verbatim (whitespace-collapsed).
		name = strings.Trim(name, `"`)
		rest := ""
		if len(tokens) > 1 {
			restStart := tokens[1].start
			rest = collapseWhitespace(string(seg[restStart:]))
		}
		cols = append(cols, columnDef{name: name, rest: rest})
	}
	return cols, constraints
}

func isConstraintKeyword(tok string) bool {
	switch tok {
	case "CONSTRAINT", "PRIMARY", "FOREIGN", "UNIQUE", "CHECK", "LIKE", "EXCLUDE":
		return true
	}
	return false
}

func isTableModifier(tok string) bool {
	switch strings.ToUpper(tok) {
	case "TEMP", "TEMPORARY", "UNLOGGED", "GLOBAL", "LOCAL":
		return true
	}
	return false
}

// splitTopLevelCommas splits body on commas that sit at parenthesis
// depth 0, ignoring commas inside strings, comments, and dollar-quotes.
// The returned slices are sub-slices of body.
func splitTopLevelCommas(body []byte) [][]byte {
	var out [][]byte
	depth := 0
	start := 0
	i := 0
	n := len(body)
	for i < n {
		c := body[i]
		switch {
		case c == '-' && i+1 < n && body[i+1] == '-':
			j := i + 2
			for j < n && body[j] != '\n' {
				j++
			}
			i = j
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
			i = skipSingleQuoted(body, i)
		case c == '"':
			i = skipDoubleQuoted(body, i)
		case c == '$':
			if end, ok := skipDollarQuoted(body, i); ok {
				i = end
				continue
			}
			i++
		case c == '(':
			depth++
			i++
		case c == ')':
			if depth > 0 {
				depth--
			}
			i++
		case c == ',' && depth == 0:
			out = append(out, body[start:i])
			start = i + 1
			i++
		default:
			i++
		}
	}
	if start < n {
		out = append(out, body[start:])
	}
	return out
}

// matchParen finds the index of the closing ')' that matches the '(' at
// body[openIdx]. Aware of comments, strings, and dollar-quotes inside
// the parens. Returns -1 on no match (unterminated).
func matchParen(body []byte, openIdx int) int {
	if openIdx < 0 || openIdx >= len(body) || body[openIdx] != '(' {
		return -1
	}
	depth := 1
	i := openIdx + 1
	n := len(body)
	for i < n {
		c := body[i]
		switch {
		case c == '-' && i+1 < n && body[i+1] == '-':
			for i < n && body[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && body[i+1] == '*':
			d := 1
			i += 2
			for i < n && d > 0 {
				if i+1 < n && body[i] == '/' && body[i+1] == '*' {
					d++
					i += 2
					continue
				}
				if i+1 < n && body[i] == '*' && body[i+1] == '/' {
					d--
					i += 2
					continue
				}
				i++
			}
		case c == '\'':
			i = skipSingleQuoted(body, i)
		case c == '"':
			i = skipDoubleQuoted(body, i)
		case c == '$':
			if end, ok := skipDollarQuoted(body, i); ok {
				i = end
				continue
			}
			i++
		case c == '(':
			depth++
			i++
		case c == ')':
			depth--
			if depth == 0 {
				return i
			}
			i++
		default:
			i++
		}
	}
	return -1
}

// collapseWhitespace replaces runs of whitespace (spaces, tabs, CR, LF)
// with a single space and trims leading/trailing whitespace.
func collapseWhitespace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := true // leading: skip
	for i := 0; i < len(s); i++ {
		c := s[i]
		isSpace := c == ' ' || c == '\t' || c == '\r' || c == '\n'
		if isSpace {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		b.WriteByte(c)
		prevSpace = false
	}
	out := b.String()
	return strings.TrimRight(out, " ")
}
