package engine_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/townsendmerino/ken/engine"
)

func writeTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := `package sample

// Foo does a thing.
func Foo() int {
	return bar() + 1
}

func bar() int {
	return 41
}
`
	if err := os.WriteFile(filepath.Join(dir, "sample.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return dir
}

func TestOpenSearchBM25(t *testing.T) {
	dir := writeTestRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ws, err := engine.Open(ctx, dir, engine.Options{Mode: "bm25"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = ws.Close() }()

	if ws.Root() != dir {
		// dir may be a symlink-resolved tmp path on macOS (/private/var vs
		// /var); Open resolves to an absolute path either way, so just
		// require it's non-empty and absolute.
		if !filepath.IsAbs(ws.Root()) {
			t.Fatalf("Root() = %q, want an absolute path", ws.Root())
		}
	}

	hits, err := ws.Search(ctx, "bar", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("Search(\"bar\") returned no hits")
	}
	h := hits[0]
	if h.Rank != 1 {
		t.Errorf("hits[0].Rank = %d, want 1", h.Rank)
	}
	if h.Gen == 0 {
		t.Error("hits[0].Gen = 0, want a published generation (>=1)")
	}
	sum := sha256.Sum256([]byte(h.Text))
	if want := hex.EncodeToString(sum[:]); h.Hash != want {
		t.Errorf("hits[0].Hash = %q, want sha256(Text) = %q", h.Hash, want)
	}

	st := ws.Status()
	if st.ChunkCount == 0 {
		t.Error("Status().ChunkCount = 0, want > 0")
	}
	if st.FileCount != 1 {
		t.Errorf("Status().FileCount = %d, want 1", st.FileCount)
	}
	if st.Generation == 0 {
		t.Error("Status().Generation = 0, want >= 1")
	}
}

func TestGenerationBumpsOnExtraChunksUnion(t *testing.T) {
	dir := writeTestRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ws, err := engine.Open(ctx, dir, engine.Options{Mode: "bm25"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = ws.Close() }()

	before := ws.Status().Generation
	ws.SetExtraChunks(nil) // a no-op union still republishes a new snapshot
	after := ws.Status().Generation
	if after <= before {
		t.Errorf("Generation after SetExtraChunks = %d, want > %d", after, before)
	}
}

func TestStructuralDefinitionAndReferences(t *testing.T) {
	dir := writeTestRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ws, err := engine.Open(ctx, dir, engine.Options{Mode: "bm25"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = ws.Close() }()

	// The structural build is lazy/background; poll briefly rather than
	// assume the first call (which may return ErrStructuralBuilding on a
	// slow CI runner) already has it.
	deadline := time.Now().Add(20 * time.Second)
	var sites []engine.DefinitionSite
	for {
		sites, err = ws.Definition(ctx, "Foo")
		if !errors.Is(err, engine.ErrStructuralBuilding) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("structural index never finished building")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("Definition: %v", err)
	}
	if len(sites) != 1 || sites[0].Kind != engine.DefinitionKindFunction {
		t.Fatalf("Definition(\"Foo\") = %+v, want one DefinitionKindFunction site", sites)
	}

	refs, err := ws.References(ctx, "bar")
	if err != nil {
		t.Fatalf("References: %v", err)
	}
	foundCall := false
	for _, r := range refs {
		if r.Kind == engine.ReferenceKindCall {
			foundCall = true
		}
	}
	if !foundCall {
		t.Errorf("References(\"bar\") = %+v, want a ReferenceKindCall entry (Foo calls bar)", refs)
	}

	outline, err := ws.Outline(ctx, "sample.go")
	if err != nil {
		t.Fatalf("Outline: %v", err)
	}
	if len(outline) != 1 || len(outline[0].Entries) == 0 {
		t.Fatalf("Outline(\"sample.go\") = %+v, want one file with entries", outline)
	}

	symbols, err := ws.Symbols(ctx, "")
	if err != nil {
		t.Fatalf("Symbols: %v", err)
	}
	if len(symbols) == 0 {
		t.Error("Symbols(\"\") returned none, want at least Foo/bar")
	}
}

func TestOpenRejectsNonDirectory(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "notadir")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if _, err := engine.Open(context.Background(), file, engine.Options{Mode: "bm25"}); err == nil {
		t.Fatal("Open(file) = nil error, want an error (not a directory)")
	}
}

func modelDirForTest(t *testing.T) string {
	t.Helper()
	dir := filepath.Join("..", "testdata", "model")
	if _, err := os.Stat(filepath.Join(dir, "model.safetensors")); err != nil {
		t.Skip("testdata/model not present; see testdata/README.md")
	}
	return dir
}

func TestSearchModeHybridPerArmRanks(t *testing.T) {
	dir := writeTestRepo(t)
	modelDir := modelDirForTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	ws, err := engine.Open(ctx, dir, engine.Options{Mode: "hybrid", ModelDir: modelDir})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = ws.Close() }()

	hits, eff, err := ws.SearchMode(ctx, "bar", 5, engine.ModeHybrid)
	if err != nil {
		t.Fatalf("SearchMode: %v", err)
	}
	if eff != engine.ModeHybrid {
		t.Fatalf("effective mode = %v, want ModeHybrid", eff)
	}
	if len(hits) == 0 {
		t.Fatal("SearchMode returned no hits")
	}
	foundRank := false
	for _, h := range hits {
		if h.BM25Rank > 0 || h.DenseRank > 0 {
			foundRank = true
		}
	}
	if !foundRank {
		t.Error("no hit had BM25Rank/DenseRank filled in for ModeHybrid")
	}
}

func TestFindRelated(t *testing.T) {
	dir := writeTestRepo(t)
	modelDir := modelDirForTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	ws, err := engine.Open(ctx, dir, engine.Options{Mode: "semantic", ModelDir: modelDir})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = ws.Close() }()

	// The fixture is a single chunk, so FindRelated legitimately finds
	// nothing (it drops the source chunk itself) — this just exercises the
	// call path without erroring.
	if _, err := ws.FindRelated(ctx, "sample.go", 2, 3); err != nil {
		t.Fatalf("FindRelated: %v", err)
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	dir := writeTestRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ws, err := engine.Open(ctx, dir, engine.Options{Mode: "bm25", Snapshot: true})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	chunkCount := ws.Status().ChunkCount
	if err := ws.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, ".ken", "snapshot.bin")); err != nil {
		t.Fatalf("snapshot.bin not written: %v", err)
	}

	ws2, err := engine.Open(ctx, dir, engine.Options{Mode: "bm25", Snapshot: true})
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	defer func() { _ = ws2.Close() }()
	if got := ws2.Status().ChunkCount; got != chunkCount {
		t.Errorf("reloaded chunk count = %d, want %d", got, chunkCount)
	}
}

// writeMixedRepo lays down one Go file and one markdown file (with nested
// headings) in a fresh temp dir, for testing Hit.HeadingPath and
// SearchFiles across file types.
func writeMixedRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	goSrc := `package sample

func Alpha() int { return 1 }
`
	mdSrc := `# Guide

## Setup

Run alpha to get started.

## Alpha details

Alpha returns a constant.
`
	if err := os.WriteFile(filepath.Join(dir, "code.go"), []byte(goSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "GUIDE.md"), []byte(mdSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSearch_HeadingPathOnlyOnMarkdownHits(t *testing.T) {
	dir := writeMixedRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ws, err := engine.Open(ctx, dir, engine.Options{Mode: "bm25"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = ws.Close() }()

	hits, err := ws.Search(ctx, "alpha", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	sawMarkdown, sawGo := false, false
	for _, h := range hits {
		switch h.File {
		case "GUIDE.md":
			sawMarkdown = true
			if len(h.HeadingPath) == 0 {
				t.Errorf("markdown hit %s:%d has empty HeadingPath", h.File, h.StartLine)
			}
		case "code.go":
			sawGo = true
			if len(h.HeadingPath) != 0 {
				t.Errorf("non-markdown hit %s:%d has non-empty HeadingPath %v", h.File, h.StartLine, h.HeadingPath)
			}
		}
	}
	if !sawMarkdown || !sawGo {
		t.Fatalf("expected hits from both files, got: %+v", hits)
	}
}

func TestSearchFiles(t *testing.T) {
	dir := writeMixedRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ws, err := engine.Open(ctx, dir, engine.Options{Mode: "bm25"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = ws.Close() }()

	hits, _, err := ws.SearchFiles(ctx, "alpha", 10, engine.ModeBM25, []string{"GUIDE.md"})
	if err != nil {
		t.Fatalf("SearchFiles: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("SearchFiles returned no hits")
	}
	for _, h := range hits {
		if h.File != "GUIDE.md" {
			t.Errorf("SearchFiles restricted to GUIDE.md returned a hit from %s", h.File)
		}
	}

	// A file not in the index is silently ignored, not an error.
	none, _, err := ws.SearchFiles(ctx, "alpha", 10, engine.ModeBM25, []string{"nope.md"})
	if err != nil {
		t.Fatalf("SearchFiles(unknown file): %v", err)
	}
	if len(none) != 0 {
		t.Errorf("SearchFiles(unknown file) = %+v, want no hits", none)
	}
}

func TestApproxTokens(t *testing.T) {
	if got := engine.ApproxTokens(""); got != 0 {
		t.Errorf("ApproxTokens(\"\") = %d, want 0", got)
	}
	if got := engine.ApproxTokens("hello world"); got == 0 {
		t.Error("ApproxTokens(\"hello world\") = 0, want > 0")
	}
}
