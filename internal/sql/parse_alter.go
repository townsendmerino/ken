package sql

import (
	"strings"
)

// alterDef is a parsed ALTER TABLE statement. v0.7.0 surfaces just the
// table name and the rendered action text — agents searching for "added
// status column" should retrieve the migration regardless of whether the
// action is ADD COLUMN, ALTER COLUMN, DROP, RENAME, etc.
type alterDef struct {
	table     string
	action    string // rendered, whitespace-collapsed
	startLine int
	endLine   int
}

// parseAlterTable parses ALTER TABLE [ONLY] <name> <action> [, <action>...].
// Multi-action statements (`ALTER TABLE foo ADD COLUMN bar int, ADD COLUMN baz int`)
// are emitted as one chunk with the full action body — we don't try to
// split per-action.
func parseAlterTable(s statement) (alterDef, bool) {
	tokens := tokenize(s.body)
	i := 0
	if !nextEqIgnoreCase(tokens, i, "ALTER") {
		return alterDef{}, false
	}
	i++
	if !nextEqIgnoreCase(tokens, i, "TABLE") {
		return alterDef{}, false
	}
	i++
	if i < len(tokens) && strings.EqualFold(tokens[i].text, "ONLY") {
		i++
	}
	if matchSeqIgnoreCase(tokens, i, "IF", "EXISTS") {
		i += 2
	}
	if i >= len(tokens) {
		return alterDef{}, false
	}
	name := tokens[i].text
	i++
	for i+1 < len(tokens) && tokens[i].text == "." {
		name = name + "." + tokens[i+1].text
		i += 2
	}
	if i >= len(tokens) {
		return alterDef{}, false
	}
	// Everything from here to the trailing ; is the action body.
	actionStart := tokens[i].start
	body := s.body[actionStart:]
	// Strip terminating ';'.
	if n := len(body); n > 0 && body[n-1] == ';' {
		body = body[:n-1]
	}
	action := collapseWhitespace(string(body))
	return alterDef{
		table:     name,
		action:    action,
		startLine: s.startLine,
		endLine:   s.startLine + countNewlines(s.body),
	}, true
}
