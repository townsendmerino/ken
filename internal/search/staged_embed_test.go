package search

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func stagedTestModelDir(t *testing.T) string {
	t.Helper()
	md := filepath.Join("..", "..", "testdata", "model")
	if _, err := os.Stat(filepath.Join(md, "model.safetensors")); err != nil {
		t.Skip("testdata/model not present; see testdata/README.md")
	}
	return md
}

// TestStagedEmbedding_SyncUpgradesToHybrid: on the non-watching path the staged
// embed pass runs synchronously, so the returned index is already the full
// configured mode (hybrid) with vectors.
func TestStagedEmbedding_SyncUpgradesToHybrid(t *testing.T) {
	md := stagedTestModelDir(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "auth.go"),
		[]byte("package auth\nfunc Login(u string) error { return verify(u) }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wi, err := NewWatchedIndexWithOptions(dir, ModeHybrid, "line", md, false, FSOptions{StagedEmbedding: true})
	if err != nil {
		t.Fatalf("staged build: %v", err)
	}
	defer wi.Close()

	ix := wi.Load()
	if ix.Mode() != ModeHybrid {
		t.Errorf("staged watch=false should upgrade to hybrid synchronously; mode=%v", ix.Mode())
	}
	if len(ix.Vecs()) == 0 {
		t.Error("upgraded index should carry vectors")
	}
	if len(wi.Search("Login", 5)) == 0 {
		t.Error("upgraded index should be searchable")
	}
}

// TestStagedEmbedding_Watch_BM25FirstThenHybrid: with the watcher, the initial
// published index is BM25 (instant, lexical-only); the background pass then
// upgrades it to hybrid.
func TestStagedEmbedding_Watch_BM25FirstThenHybrid(t *testing.T) {
	md := stagedTestModelDir(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "svc.go"),
		[]byte("package svc\nfunc Handle(req string) error { return route(req) }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wi, err := NewWatchedIndexWithOptions(dir, ModeHybrid, "line", md, true, FSOptions{StagedEmbedding: true})
	if err != nil {
		t.Fatalf("staged watch build: %v", err)
	}
	defer wi.Close()

	// Lexical search works immediately, whatever the current mode.
	if len(wi.Search("Handle", 5)) == 0 {
		t.Error("BM25 search should work before the embed upgrade")
	}
	// Background upgrade lands within a generous deadline.
	deadline := time.Now().Add(10 * time.Second)
	for wi.Load().Mode() != ModeHybrid {
		if time.Now().After(deadline) {
			t.Fatal("staged embed did not upgrade to hybrid within the deadline")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(wi.Load().Vecs()) == 0 {
		t.Error("hybrid index should carry vectors after upgrade")
	}
}
