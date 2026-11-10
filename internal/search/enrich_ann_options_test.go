package search

import (
	"os"
	"testing"
	"testing/fstest"

	"github.com/townsendmerino/aikit/chunk"
	"github.com/townsendmerino/aikit/embed"
)

var enrichFixture = fstest.MapFS{
	"a.go": {Data: []byte("package p\n\nfunc Alpha() int { return Beta() + 1 }\n\nfunc Beta() int { return 2 }\n")},
	"b.py": {Data: []byte("def gamma():\n    return delta()\n\ndef delta():\n    raise ValueError('x')\n")},
}

// The static paths (build-index, mcp.Run, pre-built load) must honor the
// same two switches the one-shot and watched paths do (ADR-054).

func TestBuildAndSerialize_DisableEnrichment(t *testing.T) {
	load := func(disable bool) *Index {
		t.Helper()
		data, err := BuildAndSerializeIndex(enrichFixture, BuildOptions{Mode: ModeBM25, Chunker: "regex", DisableEnrichment: disable})
		if err != nil {
			t.Fatal(err)
		}
		ix, err := LoadSerializedIndex(data, LoadOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return ix
	}
	if n := load(false).EnrichedChunks(); n == 0 {
		t.Error("default build-index output has no enriched chunks; the fixture should produce labels")
	}
	if n := load(true).EnrichedChunks(); n != 0 {
		t.Errorf("DisableEnrichment build-index output has %d enriched chunks, want 0", n)
	}
}

func TestWatchedIndex_DisableEnrichment(t *testing.T) {
	dir := t.TempDir()
	for name, f := range enrichFixture {
		if err := os.WriteFile(dir+"/"+name, f.Data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, disable := range []bool{false, true} {
		wi, err := NewWatchedIndexWithOptions(dir, ModeBM25, "regex", "", false, FSOptions{DisableEnrichment: disable})
		if err != nil {
			t.Fatal(err)
		}
		n := wi.Load().EnrichedChunks()
		_ = wi.Close()
		if disable && n != 0 {
			t.Errorf("watched index with DisableEnrichment has %d enriched chunks, want 0", n)
		}
		if !disable && n == 0 {
			t.Error("watched index has no enriched chunks by default")
		}
	}
}

func TestStaticPaths_DenseRetriever(t *testing.T) {
	modelDir := modelDirOrSkip(t)
	model, err := embed.LoadFromFS(os.DirFS(modelDir), ".")
	if err != nil {
		t.Fatal(err)
	}
	data, err := BuildAndSerializeIndex(enrichFixture, BuildOptions{Mode: ModeHybrid, Chunker: "regex", Model: model})
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"", "flat-i8", "flat-binary-i8"} {
		// mcp.Run's build-from-corpus path.
		built, err := FromFSWithModelOptions(enrichFixture, ModeHybrid, "regex", model, FSOptions{DenseRetriever: kind})
		if err != nil {
			t.Fatal(err)
		}
		if got := built.DenseRetriever(); got != kind {
			t.Errorf("FromFSWithModelOptions(%q) serves %q", kind, got)
		}
		// The pre-built load path (ken-mcp and mcp.Run).
		loaded, err := LoadSerializedIndex(data, LoadOptions{Model: model, DenseRetriever: kind})
		if err != nil {
			t.Fatal(err)
		}
		if got := loaded.DenseRetriever(); got != kind {
			t.Errorf("LoadSerializedIndex(%q) serves %q", kind, got)
		}
		// DB extras must not silently fall back to exact Flat.
		ext, err := loaded.WithExtraChunks([]chunk.Chunk{{File: "db/users.sql", Text: "CREATE TABLE users (id int)", StartLine: 1, EndLine: 1}})
		if err != nil {
			t.Fatal(err)
		}
		if got := ext.DenseRetriever(); got != kind {
			t.Errorf("WithExtraChunks on a %q index serves %q", kind, got)
		}
		if res := ext.Search("alpha beta", 3); len(res) == 0 {
			t.Errorf("no results from the %q index after WithExtraChunks", kind)
		}
	}
	// The zero-option entry points are unchanged: exact Flat.
	plain, err := FromFSWithModel(enrichFixture, ModeHybrid, "regex", model)
	if err != nil {
		t.Fatal(err)
	}
	if got := plain.DenseRetriever(); got != "" {
		t.Errorf("FromFSWithModel serves %q, want exact Flat", got)
	}
}
