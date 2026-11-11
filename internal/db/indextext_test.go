package db

import (
	"strings"
	"testing"
	"time"

	"github.com/townsendmerino/ken/internal/search"
)

// TestIndexTextDropsWhatDBEmits ties the two halves of ADR-058 together:
// the header this package writes on every chunk is the line internal/search
// leaves out of the indexed text. If freshnessHeader's format changes, or a
// renderer stops putting it first, this fails.
func TestIndexTextDropsWhatDBEmits(t *testing.T) {
	for _, host := range []string{"postgres@dev-pg.local", "mysql@db.internal:3306", "sqlite@schema.db", "mariadb@10.0.0.7"} {
		for _, ts := range []time.Time{
			time.Date(2026, 8, 15, 14, 23, 0, 0, time.UTC),
			time.Date(2031, 12, 31, 23, 59, 59, 0, time.FixedZone("x", -8*3600)),
		} {
			header := freshnessHeader(host, ts)
			c := renderTableChunk(tableDef{name: "customers"}, header, "db://"+host)
			if !strings.HasPrefix(c.Text, header+"\n") {
				t.Fatalf("%s: table chunk does not start with its header: %q", host, c.Text)
			}
			got := search.IndexText(c)
			if want := strings.TrimPrefix(c.Text, header+"\n"); got != want {
				t.Errorf("%s at %s: indexed text = %q, want the chunk without its header %q", host, ts, got, want)
			}
			if strings.Contains(got, "indexed at") {
				t.Errorf("%s: header still in the indexed text: %q", host, got)
			}
		}
	}
}
