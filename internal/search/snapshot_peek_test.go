package search

import (
	"os"
	"path/filepath"
	"testing"
)

// TestPeekSnapshotChunks confirms the header-only peek returns the true chunk
// count of a real KEN1 snapshot, and degrades to (0, false) on absent / garbage
// files — the contract ken doctor relies on for the large-corpus advice.
func TestPeekSnapshotChunks(t *testing.T) {
	data, err := BuildAndSerializeIndex(tinyCorpus(), BuildOptions{Mode: ModeBM25, Chunker: "line"})
	if err != nil {
		t.Fatalf("BuildAndSerializeIndex: %v", err)
	}
	ix, err := LoadSerializedIndex(data, LoadOptions{})
	if err != nil {
		t.Fatalf("LoadSerializedIndex: %v", err)
	}
	want := ix.Len()
	if want == 0 {
		t.Fatal("tinyCorpus produced 0 chunks — can't validate the peek")
	}

	dir := t.TempDir()
	binPath := SnapshotBinPath(dir)
	if err := os.MkdirAll(filepath.Dir(binPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	got, ok := PeekSnapshotChunks(binPath)
	if !ok {
		t.Fatal("PeekSnapshotChunks returned !ok on a valid snapshot")
	}
	if got != want {
		t.Errorf("PeekSnapshotChunks = %d, want %d (ix.Len)", got, want)
	}

	if n, ok := PeekSnapshotChunks(filepath.Join(dir, "absent.bin")); ok || n != 0 {
		t.Errorf("absent file: got (%d, %v), want (0, false)", n, ok)
	}
	junk := filepath.Join(dir, "junk.bin")
	if err := os.WriteFile(junk, []byte("not a ken index at all, definitely not KEN1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if n, ok := PeekSnapshotChunks(junk); ok || n != 0 {
		t.Errorf("garbage file: got (%d, %v), want (0, false)", n, ok)
	}
}
