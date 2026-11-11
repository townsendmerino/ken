package search

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/townsendmerino/aikit/bm25"
	"github.com/townsendmerino/aikit/chunk"
)

func TestIndexText(t *testing.T) {
	const body = "TABLE customers\n  id  integer  PK\n"
	const header = "-- indexed at 2026-08-15T14:23Z from postgres@dev-pg.local\n"
	cases := []struct {
		name string
		c    chunk.Chunk
		want string
	}{
		{"db chunk: header dropped", chunk.Chunk{File: "db://postgres@dev-pg.local/public.customers", Text: header + body}, body},
		{"db chunk without a header", chunk.Chunk{File: "db://postgres@h/x", Text: body}, body},
		{"db chunk, header only", chunk.Chunk{File: "db://postgres@h/x", Text: "-- indexed at 2026-08-15T14:23Z from postgres@h"}, ""},
		{"db chunk, a comment that is not the header", chunk.Chunk{File: "db://postgres@h/x", Text: "-- indexed at some point\n" + body}, "-- indexed at some point\n" + body},
		{"a file with the same first line keeps it", chunk.Chunk{File: "schema/dump.sql", Text: header + body}, header + body},
		{"header later in the text stays", chunk.Chunk{File: "db://postgres@h/x", Text: body + header}, body + header},
	}
	for _, tc := range cases {
		if got := indexText(tc.c); got != tc.want {
			t.Errorf("%s: indexText = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestDBChunkScoresDoNotDependOnTheMinute pins ADR-058: the same schema
// read at different times gives the same index. Before, the header was
// indexed with the rest: its hour was a BM25 token ("21", "t21"), so a
// query holding one scored differently from one hour to the next, and with
// a model its minute changed the chunk's embedding, so hybrid scores moved
// every minute (33 distinct score sets in 180 minutes on a small corpus).
func TestDBChunkScoresDoNotDependOnTheMinute(t *testing.T) {
	base := []chunk.Chunk{
		{File: "store.go", StartLine: 1, EndLine: 3, Text: "package store\n\nfunc Get(key string) string { return key }\n"},
		{File: "notes.md", StartLine: 1, EndLine: 2, Text: "Released 2026-11-10 at 21 27, build 14\n"},
	}
	at := func(ts time.Time) *Index {
		h := "-- indexed at " + ts.UTC().Format("2006-01-02T15:04Z") + " from sqlite@schema.db\n"
		cs := append(slices.Clone(base),
			chunk.Chunk{File: "db://sqlite@schema.db/customers", StartLine: 1, EndLine: 1, Text: h + "TABLE customers\n  id  integer  PK\n  email  text  NOT NULL\n"},
			chunk.Chunk{File: "db://sqlite@schema.db/invoices", StartLine: 1, EndLine: 1, Text: h + "TABLE invoices\n  id  integer  PK\n  total_cents  integer  NOT NULL\n"})
		return BuildIndex(cs, nil, ModeBM25, nil)
	}
	sig := func(ix *Index) string {
		s := ""
		// "21" and "t21" are the tokens of a header written at 21:xx;
		// "sqlite" and "indexed" are its words.
		for _, q := range []string{"customers email", "21", "t21 released", "indexed sqlite build", "store get"} {
			for _, r := range ix.Search(q, 5) {
				s += fmt.Sprintf("%s|%s|%.15g\n", q, r.Chunk.File, r.Score)
			}
		}
		return s
	}
	t0 := time.Date(2026, 11, 10, 21, 27, 0, 0, time.UTC)
	want := sig(at(t0))
	for i := 1; i <= 30; i++ { // 61-minute steps: every hour of the day, and many minutes
		if got := sig(at(t0.Add(time.Duration(i) * 61 * time.Minute))); got != want {
			t.Fatalf("scores changed %d steps of 61 minutes later:\n--- at %s\n%s--- later\n%s", i, t0.Format(time.RFC3339), want, got)
		}
	}
	// The header is still in the chunk's text, for display.
	ix := at(t0)
	rs := ix.Search("customers email", 1)
	if len(rs) == 0 || rs[0].Chunk.File != "db://sqlite@schema.db/customers" {
		t.Fatalf("customers table is not the top hit: %+v", rs)
	}
	if want := "-- indexed at 2026-11-10T21:27Z from sqlite@schema.db\n"; rs[0].Chunk.Text[:len(want)] != want {
		t.Errorf("displayed text lost its header: %q", rs[0].Chunk.Text)
	}
	// And none of its words are indexed for that chunk.
	if toks := bm25.Tokenize(indexText(rs[0].Chunk)); slices.Contains(toks, "indexed") || slices.Contains(toks, "t21") {
		t.Errorf("header tokens are indexed: %v", toks)
	}
}
