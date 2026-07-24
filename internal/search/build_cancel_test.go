package search

import (
	"context"
	"errors"
	"testing"
	"testing/fstest"
)

// TestWalkAndChunkFSWithModel_CancelledCtx pins Fix 2: a cancelled build
// context makes the walk+chunk+embed worker loop return promptly with a
// wrapped context.Canceled and NO partial materials (nil chunks/vecs), so a
// caller never builds an Index from a half-finished corpus.
func TestWalkAndChunkFSWithModel_CancelledCtx(t *testing.T) {
	fsys := fstest.MapFS{
		"a.go": {Data: []byte("package a\n\nfunc A() {}\n")},
		"b.go": {Data: []byte("package b\n\nfunc B() {}\n")},
		"c.py": {Data: []byte("def c():\n    return 1\n")},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled

	chunks, vecs, _, _, err := walkAndChunkFSWithModel(ctx, fsys, ModeBM25, "regex", nil, FSOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want a wrapped context.Canceled", err)
	}
	if chunks != nil || vecs != nil {
		t.Errorf("cancelled build returned partial materials: %d chunks, %d vecs (want nil/nil)", len(chunks), len(vecs))
	}
}

// TestNewWatchedIndexWithContext_CancelledBuild pins that the WatchedIndex
// constructor propagates a cancelled build: it returns (nil, err wrapping
// context.Canceled) BEFORE creating the watcher, so no partial index is
// published and no watcher goroutine leaks (goleak covers the package).
func TestNewWatchedIndexWithContext_CancelledBuild(t *testing.T) {
	root := makeTempRepo(t, map[string]string{
		"a.go": "package a\n\nfunc A() {}\n",
		"b.go": "package b\n\nfunc B() {}\n",
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	wi, err := NewWatchedIndexWithContext(ctx, root, ModeBM25, "regex", "", true, FSOptions{})
	if err == nil {
		t.Fatal("expected an error on a cancelled build, got nil (partial index published?)")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want a wrapped context.Canceled", err)
	}
	if wi != nil {
		t.Error("a cancelled build must not return a WatchedIndex")
	}
}

// TestNewWatchedIndexWithContext_LiveCtxBuildsNormally confirms behavior is
// unchanged when the context is not cancelled (byte-identical build path).
func TestNewWatchedIndexWithContext_LiveCtxBuildsNormally(t *testing.T) {
	root := makeTempRepo(t, map[string]string{
		"a.go": "package a\n\nfunc Alpha() {}\n",
	})
	wi, err := NewWatchedIndexWithContext(context.Background(), root, ModeBM25, "regex", "", false, FSOptions{})
	if err != nil {
		t.Fatalf("live build failed: %v", err)
	}
	t.Cleanup(func() { _ = wi.Close() })
	if wi.Len() == 0 {
		t.Error("expected a non-empty index from a live build")
	}
}
