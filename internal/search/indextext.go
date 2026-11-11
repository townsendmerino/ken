package search

import (
	"regexp"
	"strings"

	"github.com/townsendmerino/aikit/chunk"
)

// dbChunkPrefix marks a chunk that came from live database introspection
// (internal/db): its File is "db://<engine>@<host>/<object>".
const dbChunkPrefix = "db://"

// freshnessHeader matches the first line internal/db gives every such
// chunk: "-- indexed at 2026-08-15T14:23Z from postgres@dev-pg.local".
// The format is internal/db.freshnessHeader's; TestIndexTextDropsWhatDBEmits
// in internal/db fails if the two drift apart.
var freshnessHeader = regexp.MustCompile(`^-- indexed at \d{4}-\d{2}-\d{2}T\d{2}:\d{2}Z from [^\n]*\n?`)

// indexText is the text of c that is tokenized for BM25 and embedded for
// the dense arm and the neural reranker. It is c.Text, except that a
// database chunk's freshness header is left out (ADR-058).
//
// The header says when the schema was read, to the minute. It is there for
// the reader, and Chunk.Text keeps it, so results still show it. Indexed,
// it made a DB chunk's tokens and embedding change every minute, and with
// them the hybrid scores of the whole corpus: the same query over the same
// schema returned slightly different scores from one minute to the next.
func indexText(c chunk.Chunk) string {
	if !strings.HasPrefix(c.File, dbChunkPrefix) || !strings.HasPrefix(c.Text, "-- indexed at ") {
		return c.Text
	}
	if loc := freshnessHeader.FindStringIndex(c.Text); loc != nil {
		return c.Text[loc[1]:]
	}
	return c.Text
}

// IndexText exposes indexText to internal/db's format test.
func IndexText(c chunk.Chunk) string { return indexText(c) }
