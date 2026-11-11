package engine_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/townsendmerino/ken/engine"
)

func openBM25Tree(t *testing.T, files map[string]string, opts engine.Options) (*engine.Workspace, string) {
	t.Helper()
	root := writeTree(t, files)
	opts.Mode = "bm25"
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ws, err := engine.Open(ctx, root, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	return ws, root
}

var labelFiles = map[string]string{
	"a.go":  "package p\n\nfunc Alpha() int { return Beta() + 1 }\n\nfunc Beta() int { return 2 }\n",
	"n.txt": "plain notes about alpha and beta\n",
}

// Hit.Label is the enrichment line Hit.Text omits: together they are the
// chunk the index stores, and with enrichment off there is no label.
func TestHit_LabelPlusTextIsTheStoredChunk(t *testing.T) {
	ctx := context.Background()
	ws, root := openBM25Tree(t, labelFiles, engine.Options{})
	hits, err := ws.Search(ctx, "Alpha Beta", 5)
	if err != nil || len(hits) == 0 {
		t.Fatalf("Search: %d hits, err %v", len(hits), err)
	}
	labelled := 0
	for _, h := range hits {
		if h.Label != "" {
			labelled++
			if !strings.HasPrefix(h.Label, "# ken: ") || !strings.HasSuffix(h.Label, "\n") {
				t.Errorf("Label = %q, want one `# ken: …` line", h.Label)
			}
		}
		src, err := os.ReadFile(filepath.Join(root, h.File))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(src), h.Text) {
			t.Errorf("%s: Text is not the file's own bytes: %q", h.File, h.Text)
		}
	}
	if labelled == 0 {
		t.Error("no hit carries a Label on an enriched index")
	}

	plain, _ := openBM25Tree(t, labelFiles, engine.Options{DisableEnrichment: true})
	hits, _ = plain.Search(ctx, "Alpha Beta", 5)
	for _, h := range hits {
		if h.Label != "" {
			t.Errorf("Label = %q on an index built with enrichment off", h.Label)
		}
	}
}

// NoArmRanks and Telemetry change nothing about the ranking.
func TestSearchWith_NoArmRanksAndTelemetryKeepTheRanking(t *testing.T) {
	ctx := context.Background()
	ws, _ := openBM25Tree(t, labelFiles, engine.Options{})
	base, _, err := ws.SearchWith(ctx, "Alpha Beta", 5, engine.ModeBM25, engine.SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var tel engine.Telemetry
	got, _, err := ws.SearchWith(ctx, "Alpha Beta", 5, engine.ModeBM25, engine.SearchOptions{NoArmRanks: true, Telemetry: &tel})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(base) || len(got) == 0 {
		t.Fatalf("got %d hits, base %d", len(got), len(base))
	}
	for i := range got {
		if got[i].File != base[i].File || got[i].StartLine != base[i].StartLine || got[i].Score != base[i].Score || got[i].Text != base[i].Text {
			t.Errorf("hit %d differs: %+v vs %+v", i, got[i], base[i])
		}
		if got[i].BM25Rank != 0 || got[i].DenseRank != 0 {
			t.Errorf("hit %d has arm ranks with NoArmRanks set", i)
		}
		if base[i].BM25Rank == 0 {
			t.Errorf("hit %d: default call should fill BM25Rank", i)
		}
	}
}

// Open reports a failed build as *BuildError with the cause intact.
func TestOpen_BuildErrorCarriesTheCause(t *testing.T) {
	root := writeTree(t, labelFiles)
	_, err := engine.Open(context.Background(), root, engine.Options{Mode: "bm25", Chunker: "no-such-chunker"})
	var be *engine.BuildError
	if !errors.As(err, &be) {
		t.Fatalf("Open with an unknown chunker returned %T %v, want *BuildError", err, err)
	}
	if !strings.Contains(be.Err.Error(), "unknown chunker") || !strings.HasPrefix(err.Error(), "engine: build index for ") {
		t.Errorf("cause %q / message %q", be.Err, err)
	}
	if _, err := engine.ParseMode("nope"); err == nil {
		t.Error("ParseMode accepted an unknown mode")
	}
}

// WriteSnapshot writes a snapshot a later Open loads, keyed by the options
// the index was built with: a Workspace with different options rebuilds.
func TestWriteSnapshot_KeyedByBuildOptions(t *testing.T) {
	ctx := context.Background()
	ws, root := openBM25Tree(t, labelFiles, engine.Options{DisableEnrichment: true})
	if err := ws.WriteSnapshot(); err != nil {
		t.Fatal(err)
	}
	if n, ok := engine.InspectSnapshot(root); !ok || n != ws.Status().ChunkCount {
		t.Errorf("InspectSnapshot = %d, %v; want %d chunks", n, ok, ws.Status().ChunkCount)
	}
	for _, tc := range []struct {
		name        string
		disable     bool
		wantLabeled bool
	}{{"same options load it", true, false}, {"enrichment on must not load an unenriched snapshot", false, true}} {
		w2, err := engine.Open(ctx, root, engine.Options{Mode: "bm25", Snapshot: true, DisableEnrichment: tc.disable})
		if err != nil {
			t.Fatal(err)
		}
		hits, _ := w2.Search(ctx, "Alpha Beta", 5)
		labelled := false
		for _, h := range hits {
			labelled = labelled || h.Label != ""
		}
		_ = w2.Close()
		if labelled != tc.wantLabeled {
			t.Errorf("%s: hits labelled = %v, want %v", tc.name, labelled, tc.wantLabeled)
		}
	}
}

// BuildIndexBytes is deterministic and honors DisableEnrichment.
func TestBuildIndexBytes(t *testing.T) {
	root := writeTree(t, labelFiles)
	build := func(disable bool) []byte {
		data, err := engine.BuildIndexBytes(os.DirFS(root), engine.BuildIndexOptions{Mode: engine.ModeBM25, Chunker: "regex", DisableEnrichment: disable})
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	a, b, off := build(false), build(false), build(true)
	if string(a) != string(b) {
		t.Error("two builds of the same corpus differ")
	}
	if !strings.Contains(string(a), "# ken: ") || strings.Contains(string(off), "# ken: ") {
		t.Error("enrichment label presence does not follow DisableEnrichment")
	}
}
