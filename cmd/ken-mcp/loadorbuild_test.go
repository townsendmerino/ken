package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/townsendmerino/ken/internal/search"
	kenmcp "github.com/townsendmerino/ken/mcp"

	_ "github.com/townsendmerino/aikit/chunk/regex"
)

// writeCorpus lays down a tiny bm25-indexable corpus and returns its dir.
func writeCorpus(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"),
		[]byte("package x\n\nfunc Alpha() int { return 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// bakePrebuilt builds a bm25 index for dir with the given chunker and
// writes it to <dir>/.ken/index.bin (the ADR-024 convention).
func bakePrebuilt(t *testing.T, dir, chunker string) {
	t.Helper()
	data, err := search.BuildAndSerializeIndex(os.DirFS(dir), search.BuildOptions{
		Mode:    search.ModeBM25,
		Chunker: chunker,
	})
	if err != nil {
		t.Fatalf("BuildAndSerializeIndex: %v", err)
	}
	kenDir := filepath.Join(dir, ".ken")
	if err := os.MkdirAll(kenDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kenDir, "index.bin"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func quietLogger() *kenmcp.Logger { return kenmcp.NewLogger(os.Stderr, kenmcp.LogError) }

// No pre-built index → live build with the watcher (original behavior).
func TestLoadOrBuild_NoPrebuilt_LiveBuilds(t *testing.T) {
	dir := writeCorpus(t)
	wi, err := loadOrBuildWatched(dir, search.ModeBM25, "bm25", "regex", "", search.FSOptions{}, quietLogger())
	if err != nil {
		t.Fatalf("loadOrBuildWatched: %v", err)
	}
	defer func() { _ = wi.Close() }()
	if wi.Len() == 0 {
		t.Errorf("live build produced an empty index")
	}
}

// Pre-built index present + matching chunker → static load (no watcher).
func TestLoadOrBuild_Prebuilt_Matching_Loads(t *testing.T) {
	dir := writeCorpus(t)
	bakePrebuilt(t, dir, "regex")
	wi, err := loadOrBuildWatched(dir, search.ModeBM25, "bm25", "regex", "", search.FSOptions{}, quietLogger())
	if err != nil {
		t.Fatalf("loadOrBuildWatched: %v", err)
	}
	defer func() { _ = wi.Close() }()
	if wi.Len() == 0 {
		t.Errorf("loaded pre-built index is empty")
	}
}

// Pre-built index present but chunker mismatches the server config →
// hard error (the operator must fix it; we won't serve wrong-config
// results). This is the failure main() turns into a startup exit(1).
func TestLoadOrBuild_Prebuilt_ChunkerMismatch_Errors(t *testing.T) {
	dir := writeCorpus(t)
	bakePrebuilt(t, dir, "regex") // index built with regex...
	// ...server configured for treesitter.
	_, err := loadOrBuildWatched(dir, search.ModeBM25, "bm25", "treesitter", "", search.FSOptions{}, quietLogger())
	if err == nil {
		t.Fatal("expected a hard error on chunker mismatch, got nil")
	}
	if !errors.Is(err, search.ErrChunkerMismatch) {
		t.Errorf("error chain should include ErrChunkerMismatch; got %v", err)
	}
	if !strings.Contains(err.Error(), "index.bin") {
		t.Errorf("error should name the offending file; got %v", err)
	}
}

// localPathHasPrebuilt: true only for a local dir carrying .ken/index.bin.
func TestLocalPathHasPrebuilt(t *testing.T) {
	dir := writeCorpus(t)
	if localPathHasPrebuilt(dir) {
		t.Error("no .ken/index.bin yet → want false")
	}
	bakePrebuilt(t, dir, "regex")
	if !localPathHasPrebuilt(dir) {
		t.Error("after baking index.bin → want true")
	}
	if localPathHasPrebuilt("https://github.com/townsendmerino/ken") {
		t.Error("http(s) sources never have a local pre-built → want false")
	}
}
