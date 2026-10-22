package engine_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/townsendmerino/ken/engine"
)

// The structural index has its own size cap (1 MiB by default), separate
// from enrichment's 64 KiB: a type in an 80 KiB file is found, unless
// Options.StructuralMaxFileBytes lowers the cap below the file.
func TestStructuralMaxFileBytes(t *testing.T) {
	t.Setenv("KEN_STRUCTURAL_MAX_FILE_BYTES", "")
	var b strings.Builder
	b.WriteString("package big\n\ntype Model struct{ n int }\n\nvar table = []string{\n")
	for b.Len() < 80<<10 {
		b.WriteString("\t\"entry-value-padding-to-grow-the-file\",\n")
	}
	b.WriteString("}\n")
	dir := writeTree(t, map[string]string{"big/big.go": b.String()})

	for _, tc := range []struct {
		name  string
		cap   int64
		found bool
	}{
		{"default 1 MiB", 0, true},
		{"64 KiB", 64 << 10, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			ws, err := engine.Open(ctx, dir, engine.Options{Mode: "bm25", StructuralMaxFileBytes: tc.cap})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = ws.Close() }()
			sites, err := ws.Definition(ctx, "Model")
			if err != nil {
				t.Fatal(err)
			}
			if got := len(sites) == 1 && sites[0].Line == 3; got != tc.found {
				t.Errorf("Definition(Model) = %+v with StructuralMaxFileBytes=%d; found=%v, want %v", sites, tc.cap, got, tc.found)
			}
			// Search still finds the file either way: the cap is the
			// structural index's alone.
			if hits, _ := ws.Search(ctx, "Model", 3); len(hits) == 0 {
				t.Error("Search lost the file")
			}
		})
	}
}
