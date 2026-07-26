package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/townsendmerino/ken/internal/search"
)

// TestCmdIndex_WriteSnapshot: `ken index --write-snapshot` writes valid M1
// artifacts, and their config-key matches what ken-mcp computes for the same
// mode/chunker/model — so a later ken-mcp boot loads them without a rebuild.
func TestCmdIndex_WriteSnapshot(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package p\nfunc Alpha() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("package p\nfunc Beta() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if rc := cmdIndex([]string{dir, "--write-snapshot", "--mode", "bm25", "--chunker", "line"}); rc != 0 {
		t.Fatalf("cmdIndex --write-snapshot returned %d, want 0", rc)
	}

	// Both artifacts exist and parse.
	manData, err := os.ReadFile(search.SnapshotManifestPath(dir))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	m, err := search.DecodeManifest(manData)
	if err != nil {
		t.Fatalf("DecodeManifest: %v", err)
	}
	binData, err := os.ReadFile(search.SnapshotBinPath(dir))
	if err != nil {
		t.Fatalf("read snapshot.bin: %v", err)
	}
	chunks, _, err := search.LoadSerializedCorpus(binData, search.LoadOptions{ExpectedMode: "bm25", ExpectedChunker: "line"})
	if err != nil {
		t.Fatalf("LoadSerializedCorpus: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("expected chunks in the written snapshot")
	}

	// Cross-binary contract: the config-key ken wrote must equal what ken-mcp
	// computes (bm25 → model fingerprint "bm25", enrichment on), so ken-mcp
	// trusts the snapshot instead of rebuilding.
	wantKey := search.SnapshotConfigKey(search.ModeBM25, "line", search.ModelFingerprint(nil, ""), true)
	if m.ConfigKey != wantKey {
		t.Errorf("config-key mismatch:\n wrote %q\n mcp   %q", m.ConfigKey, wantKey)
	}
	// Manifest covers both source files (a.go, b.go), not .ken/.
	if len(m.Files) != 2 {
		t.Errorf("manifest files = %d, want 2 (a.go, b.go): %+v", len(m.Files), m.Files)
	}
}
