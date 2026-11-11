package engine_test

import (
	"context"
	"errors"
	"testing"
	"testing/fstest"

	"github.com/townsendmerino/aikit/chunk"

	"github.com/townsendmerino/ken/engine"
)

var staticCorpus = fstest.MapFS{
	"a.go":  {Data: []byte("package p\n\nfunc Alpha() int { return Beta() + 1 }\n\nfunc Beta() int { return 2 }\n")},
	"n.txt": {Data: []byte("plain notes about alpha and beta\n")},
}

func staticSearch(t *testing.T, ws *engine.Workspace, q string) []engine.Result {
	t.Helper()
	res, _, err := ws.SearchResults(context.Background(), q, 10, engine.ModeBM25, engine.SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// OpenFS serves a fixed corpus: queries work, there is no root, no
// structural index, and nothing that needs a directory.
func TestOpenFS_FixedCorpus(t *testing.T) {
	ctx := context.Background()
	ws, err := engine.OpenFS(ctx, staticCorpus, engine.StaticOptions{Mode: engine.ModeBM25, Chunker: "regex"})
	if err != nil {
		t.Fatal(err)
	}
	if ws.Root() != "" || ws.ChunkCount() == 0 || ws.CurrentMode() != engine.ModeBM25 {
		t.Errorf("root %q, %d chunks, mode %v", ws.Root(), ws.ChunkCount(), ws.CurrentMode())
	}
	if len(staticSearch(t, ws, "Alpha Beta")) == 0 {
		t.Error("no results from an OpenFS workspace")
	}
	if st := ws.IndexStatus(); st.EnrichedChunks == 0 {
		t.Errorf("IndexStatus = %+v, want enriched chunks", st)
	}
	if _, err := ws.Definition(ctx, "Alpha"); !errors.Is(err, engine.ErrNoStructuralIndex) {
		t.Errorf("Definition on a fixed corpus = %v, want ErrNoStructuralIndex", err)
	}
	if err := ws.Refresh(ctx, []string{"a.go"}, nil); !errors.Is(err, engine.ErrWrappedIndex) {
		t.Errorf("Refresh = %v, want ErrWrappedIndex", err)
	}

	plain, err := engine.OpenFS(ctx, staticCorpus, engine.StaticOptions{Mode: engine.ModeBM25, Chunker: "regex", DisableEnrichment: true})
	if err != nil {
		t.Fatal(err)
	}
	if n := plain.IndexStatus().EnrichedChunks; n != 0 {
		t.Errorf("DisableEnrichment left %d enriched chunks", n)
	}
}

// A pre-built index is loaded instead of indexing, answers like the live
// build, and is refused with *PrebuiltError when it does not match.
func TestOpenFS_Prebuilt(t *testing.T) {
	ctx := context.Background()
	data, err := engine.BuildIndexBytes(staticCorpus, engine.BuildIndexOptions{Mode: engine.ModeBM25, Chunker: "regex"})
	if err != nil {
		t.Fatal(err)
	}
	live, err := engine.OpenFS(ctx, staticCorpus, engine.StaticOptions{Mode: engine.ModeBM25, Chunker: "regex"})
	if err != nil {
		t.Fatal(err)
	}
	// An empty corpus proves the index came from the bytes, not a walk.
	pre, err := engine.OpenFS(ctx, fstest.MapFS{}, engine.StaticOptions{Mode: engine.ModeBM25, Chunker: "regex", Prebuilt: data})
	if err != nil {
		t.Fatal(err)
	}
	a, b := staticSearch(t, live, "Alpha Beta"), staticSearch(t, pre, "Alpha Beta")
	if len(a) == 0 || len(a) != len(b) {
		t.Fatalf("live %d results, pre-built %d", len(a), len(b))
	}
	for i := range a {
		if a[i].Chunk.File != b[i].Chunk.File || a[i].Score != b[i].Score || a[i].Chunk.Text != b[i].Chunk.Text {
			t.Errorf("result %d differs between the live build and the pre-built index", i)
		}
	}

	for name, opts := range map[string]engine.StaticOptions{
		"other chunker": {Mode: engine.ModeBM25, Chunker: "line", Prebuilt: data},
		"corrupt bytes": {Mode: engine.ModeBM25, Chunker: "regex", Prebuilt: []byte("not an index")},
	} {
		_, err := engine.OpenFS(ctx, staticCorpus, opts)
		var pe *engine.PrebuiltError
		if !errors.As(err, &pe) || pe.Err == nil {
			t.Errorf("%s: OpenFS = %v, want *PrebuiltError", name, err)
		}
	}
}

// ReplaceExtraChunks rebuilds from the corpus as first indexed, so calls
// replace one another and an empty call restores the original index.
func TestOpenFS_ReplaceExtraChunks(t *testing.T) {
	ws, err := engine.OpenFS(context.Background(), staticCorpus, engine.StaticOptions{Mode: engine.ModeBM25, Chunker: "regex"})
	if err != nil {
		t.Fatal(err)
	}
	base := ws.ChunkCount()
	extra := func(name string) chunk.Chunk {
		return chunk.Chunk{File: "db://x/" + name, StartLine: 1, EndLine: 1, Text: "CREATE TABLE " + name + " (id int)"}
	}
	for _, tc := range []struct {
		chunks []chunk.Chunk
		want   int
		find   string
		absent string
	}{
		{[]chunk.Chunk{extra("customers"), extra("invoices")}, base + 2, "customers", ""},
		{[]chunk.Chunk{extra("ledger")}, base + 1, "ledger", "customers"},
		{nil, base, "", "ledger"},
	} {
		if err := ws.ReplaceExtraChunks(tc.chunks); err != nil {
			t.Fatal(err)
		}
		if got := ws.ChunkCount(); got != tc.want {
			t.Errorf("after %d extra chunks: %d chunks, want %d (extras must replace, not accumulate)", len(tc.chunks), got, tc.want)
		}
		has := func(q string) bool {
			for _, r := range staticSearch(t, ws, q) {
				if r.Chunk.File == "db://x/"+q {
					return true
				}
			}
			return false
		}
		if tc.find != "" && !has(tc.find) {
			t.Errorf("extra chunk %q not searchable", tc.find)
		}
		if tc.absent != "" && has(tc.absent) {
			t.Errorf("replaced chunk %q is still in the index", tc.absent)
		}
	}
	// On a directory Workspace it is SetExtraChunks and reports no error.
	dirWS, _ := openBM25Tree(t, labelFiles, engine.Options{})
	if err := dirWS.ReplaceExtraChunks([]chunk.Chunk{extra("customers")}); err != nil {
		t.Errorf("ReplaceExtraChunks on a directory Workspace: %v", err)
	}
}
