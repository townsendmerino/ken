package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/townsendmerino/ken/internal/search"
)

// buildAndPersist builds a live bm25 index over dir and writes its snapshot,
// returning after closing the builder's watcher. Helper for the M1 tests.
func buildAndPersist(t *testing.T, dir, chunker string) {
	t.Helper()
	wi, err := liveWatched(context.Background(), dir, search.ModeBM25, chunker, "", search.FSOptions{}, quietLogger())
	if err != nil {
		t.Fatalf("liveWatched: %v", err)
	}
	writeSnapshot(dir, wi, search.ModeBM25, chunker, "", search.FSOptions{}, quietLogger())
	_ = wi.Close()
}

// TestSnapshot_WriteThenCleanLoad: after persisting, an unchanged repo loads
// via the fast path (tryLoadSnapshot non-nil) and the seeded index is
// queryable — the everyday-cold win.
func TestSnapshot_WriteThenCleanLoad(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "auth.go"),
		[]byte("package auth\nfunc Login(u string) error { return nil }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	buildAndPersist(t, dir, "line")

	if _, err := os.Stat(snapshotBinPath(dir)); err != nil {
		t.Fatalf("snapshot.bin not written: %v", err)
	}
	if _, err := os.Stat(snapshotManifestPath(dir)); err != nil {
		t.Fatalf("snapshot.manifest not written: %v", err)
	}

	got := tryLoadSnapshot(dir, search.ModeBM25, "bm25", "line", "", search.FSOptions{}, quietLogger())
	if got == nil {
		t.Fatal("expected clean snapshot load (fast path), got nil (would rebuild)")
	}
	t.Cleanup(func() { _ = got.Close() })
	if res := got.Search("Login", 5); len(res) == 0 {
		t.Error("seeded index should find 'Login'")
	}
}

// TestSnapshot_DriftTriggersRebuild: editing a file (size changes) makes the
// drift scan reject the snapshot → nil (caller rebuilds).
func TestSnapshot_DriftTriggersRebuild(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "auth.go")
	if err := os.WriteFile(f, []byte("package auth\nfunc Login() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	buildAndPersist(t, dir, "line")

	// Edit the file — larger content → size differs even if mtime is coarse.
	if err := os.WriteFile(f, []byte("package auth\nfunc Login() {}\nfunc Logout() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := tryLoadSnapshot(dir, search.ModeBM25, "bm25", "line", "", search.FSOptions{}, quietLogger()); got != nil {
		_ = got.Close()
		t.Fatal("expected nil (drift → rebuild) after editing a file, got a loaded snapshot")
	}
}

// TestSnapshot_NewFileTriggersRebuild: adding a file is drift too.
func TestSnapshot_NewFileTriggersRebuild(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package p\nfunc A() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	buildAndPersist(t, dir, "line")

	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("package p\nfunc B() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := tryLoadSnapshot(dir, search.ModeBM25, "bm25", "line", "", search.FSOptions{}, quietLogger()); got != nil {
		_ = got.Close()
		t.Fatal("expected nil (drift → rebuild) after adding a file")
	}
}

// TestSnapshot_ConfigMismatchTriggersRebuild: a different chunker changes the
// config-key → the snapshot is not trusted.
func TestSnapshot_ConfigMismatchTriggersRebuild(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package p\nfunc A() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	buildAndPersist(t, dir, "line") // stored with chunker=line

	// Load requesting chunker=regex → config-key mismatch → nil.
	if got := tryLoadSnapshot(dir, search.ModeBM25, "bm25", "regex", "", search.FSOptions{}, quietLogger()); got != nil {
		_ = got.Close()
		t.Fatal("expected nil on chunker mismatch (config-key changed)")
	}
}

// TestSnapshot_MissingAndCorruptDegradeToRebuild: no manifest, and a garbage
// manifest, both yield nil (never a crash — snapshot is untrusted input).
func TestSnapshot_MissingAndCorruptDegradeToRebuild(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package p\nfunc A() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// No .ken/ at all → nil.
	if got := tryLoadSnapshot(dir, search.ModeBM25, "bm25", "line", "", search.FSOptions{}, quietLogger()); got != nil {
		_ = got.Close()
		t.Fatal("expected nil with no snapshot present")
	}

	buildAndPersist(t, dir, "line")

	// Corrupt the manifest → DecodeManifest fails → nil.
	if err := os.WriteFile(snapshotManifestPath(dir), []byte("not a real KMAN blob"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := tryLoadSnapshot(dir, search.ModeBM25, "bm25", "line", "", search.FSOptions{}, quietLogger()); got != nil {
		_ = got.Close()
		t.Fatal("expected nil on corrupt manifest")
	}

	// Manifest present but bin missing → nil.
	buildAndPersist(t, dir, "line")
	if err := os.Remove(snapshotBinPath(dir)); err != nil {
		t.Fatal(err)
	}
	if got := tryLoadSnapshot(dir, search.ModeBM25, "bm25", "line", "", search.FSOptions{}, quietLogger()); got != nil {
		_ = got.Close()
		t.Fatal("expected nil when snapshot.bin is missing")
	}
}
