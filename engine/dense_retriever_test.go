package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/townsendmerino/ken/engine"
)

// TestOpen_DenseRetrieverOption pins the path tack's large-repository
// memory plan depends on: engine.Options.DenseRetriever is the field that
// selects the watched index's vector store, and the store it names is the
// one the Workspace serves — on the cold build, after a Refresh, and after
// a snapshot reload. Needs the embedding model (a BM25 index has no dense
// store).
func TestOpen_DenseRetrieverOption(t *testing.T) {
	modelDir := modelDirForTest(t)
	for _, kind := range []string{"", "flat-i8", "flat-binary-i8"} {
		t.Run("kind="+kind, func(t *testing.T) {
			// An ambient KEN_ANN must not matter: engine reads Options only.
			t.Setenv("KEN_ANN", "flat-binary-i8")
			root := writeTree(t, map[string]string{
				"a.go": "package p\n\n// Alpha adds two numbers.\nfunc Alpha(a, b int) int { return a + b }\n",
				"b.go": "package p\n\n// Beta joins two strings.\nfunc Beta(a, b string) string { return a + b }\n",
			})
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			opts := engine.Options{Mode: "hybrid", ModelDir: modelDir, DenseRetriever: kind, Snapshot: true}

			ws, err := engine.Open(ctx, root, opts)
			if err != nil {
				t.Fatal(err)
			}
			served := func(ws *engine.Workspace) string { return ws.WatchedIndex().Load().DenseRetriever() }
			if got := served(ws); got != kind {
				t.Errorf("cold build serves dense retriever %q, want %q", got, kind)
			}
			if hits, err := ws.Search(ctx, "add two numbers", 3); err != nil || len(hits) == 0 {
				t.Errorf("Search on %q store: %d hits, err %v", kind, len(hits), err)
			}

			if err := os.WriteFile(filepath.Join(root, "c.go"), []byte("package p\n\n// Gamma multiplies.\nfunc Gamma(a, b int) int { return a * b }\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := ws.Refresh(ctx, []string{"c.go"}, nil); err != nil {
				t.Fatal(err)
			}
			if got := served(ws); got != kind {
				t.Errorf("after Refresh the dense retriever is %q, want %q", got, kind)
			}
			if hits, _ := ws.Search(ctx, "Gamma multiplies", 3); len(hits) == 0 || hits[0].File != "c.go" {
				t.Errorf("refreshed file not found on %q store: %+v", kind, hits)
			}
			if err := ws.Close(); err != nil {
				t.Fatal(err)
			}

			ws2, err := engine.Open(ctx, root, opts) // loads the snapshot Close wrote
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = ws2.Close() }()
			if got := served(ws2); got != kind {
				t.Errorf("after a snapshot reload the dense retriever is %q, want %q", got, kind)
			}
		})
	}
}
