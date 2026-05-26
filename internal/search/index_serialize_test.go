package search

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/townsendmerino/ken/internal/embed"
)

// tinyCorpus is the shared fixture: three files with distinctive
// content so search results can be asserted before and after a
// roundtrip.
func tinyCorpus() fstest.MapFS {
	return fstest.MapFS{
		"main.go": {Data: []byte(`package main

import "fmt"

func ValidateUser(name string) bool {
	return len(name) > 0
}

func main() {
	fmt.Println(ValidateUser("alice"))
}
`)},
		"auth.py": {Data: []byte(`def validate_user(name):
    """Return True iff name is non-empty."""
    return bool(name)


def hash_password(password):
    return password[::-1]
`)},
		"README.md": {Data: []byte(`# Demo corpus

Used by internal/search/index_serialize_test.go.
`)},
	}
}

// loadTestModel returns the testdata model if present, else skips.
// The serialization tests for semantic / hybrid mode are gated on
// the per-machine testdata/model presence (same gating as
// internal/embed's full parity suite).
func loadTestModel(t *testing.T) *embed.StaticModel {
	t.Helper()
	modelDir := filepath.Join("..", "..", "testdata", "model")
	if _, err := os.Stat(filepath.Join(modelDir, "model.safetensors")); err != nil {
		t.Skipf("testdata/model not present; see testdata/README.md")
	}
	m, err := embed.Load(modelDir)
	if err != nil {
		t.Fatalf("embed.Load: %v", err)
	}
	return m
}

// TestSerializeRoundtrip_BM25 builds a BM25 index, serializes it,
// loads it back, and confirms ix.Search returns the same top hit on a
// distinctive query.
func TestSerializeRoundtrip_BM25(t *testing.T) {
	fsys := tinyCorpus()
	data, err := BuildAndSerializeIndex(fsys, BuildOptions{
		Mode:    ModeBM25,
		Chunker: "regex",
	})
	if err != nil {
		t.Fatalf("BuildAndSerializeIndex: %v", err)
	}
	if len(data) < 12 {
		t.Fatalf("serialized data implausibly short: %d bytes", len(data))
	}
	if string(data[:4]) != serializeMagic {
		t.Fatalf("magic = %q, want %q", data[:4], serializeMagic)
	}

	ix, err := LoadSerializedIndex(data, LoadOptions{ExpectedMode: "bm25"})
	if err != nil {
		t.Fatalf("LoadSerializedIndex: %v", err)
	}
	if ix.Len() == 0 {
		t.Fatalf("loaded index has no chunks")
	}

	results := ix.Search("ValidateUser", 3)
	if len(results) == 0 {
		t.Fatalf("loaded index returned no results for ValidateUser")
	}
	if !strings.Contains(results[0].Chunk.File, "main.go") {
		t.Errorf("top hit = %s, want main.go", results[0].Chunk.File)
	}

	// Cross-check: a fresh build over the same corpus produces the
	// same top hit at the same score.
	freshIx, err := FromFS(fsys, ModeBM25, "regex", "")
	if err != nil {
		t.Fatalf("FromFS: %v", err)
	}
	freshResults := freshIx.Search("ValidateUser", 3)
	if len(freshResults) != len(results) {
		t.Errorf("result count diverges: loaded=%d fresh=%d", len(results), len(freshResults))
	}
	if results[0].Chunk.File != freshResults[0].Chunk.File ||
		results[0].Chunk.StartLine != freshResults[0].Chunk.StartLine {
		t.Errorf("top hit diverges: loaded=%s:%d fresh=%s:%d",
			results[0].Chunk.File, results[0].Chunk.StartLine,
			freshResults[0].Chunk.File, freshResults[0].Chunk.StartLine)
	}
}

// TestSerializeRoundtrip_Semantic checks the vecs section roundtrips
// correctly: a serialized semantic index, reloaded, must return the
// same hits as the original.
func TestSerializeRoundtrip_Semantic(t *testing.T) {
	model := loadTestModel(t)
	fsys := tinyCorpus()
	data, err := BuildAndSerializeIndex(fsys, BuildOptions{
		Mode:    ModeSemantic,
		Chunker: "regex",
		Model:   model,
	})
	if err != nil {
		t.Fatalf("BuildAndSerializeIndex: %v", err)
	}

	ix, err := LoadSerializedIndex(data, LoadOptions{
		ExpectedMode:    "semantic",
		ExpectedChunker: "regex",
		Model:           model,
	})
	if err != nil {
		t.Fatalf("LoadSerializedIndex: %v", err)
	}

	results := ix.Search("validate user", 3)
	if len(results) == 0 {
		t.Fatalf("semantic search returned no results")
	}
	if !strings.Contains(results[0].Chunk.File, "main.go") &&
		!strings.Contains(results[0].Chunk.File, "auth.py") {
		t.Errorf("top hit = %s, want main.go or auth.py", results[0].Chunk.File)
	}
}

// TestSerializeRoundtrip_Hybrid same as semantic but in hybrid mode
// (BM25 + ANN + fusion + rerank pipeline). The whole pipeline must
// work on a loaded index.
func TestSerializeRoundtrip_Hybrid(t *testing.T) {
	model := loadTestModel(t)
	fsys := tinyCorpus()
	data, err := BuildAndSerializeIndex(fsys, BuildOptions{
		Mode:    ModeHybrid,
		Chunker: "regex",
		Model:   model,
	})
	if err != nil {
		t.Fatalf("BuildAndSerializeIndex: %v", err)
	}
	ix, err := LoadSerializedIndex(data, LoadOptions{
		ExpectedMode: "hybrid",
		Model:        model,
	})
	if err != nil {
		t.Fatalf("LoadSerializedIndex: %v", err)
	}
	results := ix.Search("hash password", 3)
	if len(results) == 0 {
		t.Fatalf("hybrid search returned no results")
	}
	if !strings.Contains(results[0].Chunk.File, "auth.py") {
		t.Errorf("top hit = %s, want auth.py", results[0].Chunk.File)
	}
}

// TestLoadSerialized_MagicMismatch flips the first 4 bytes and
// expects ErrCorrupt.
func TestLoadSerialized_MagicMismatch(t *testing.T) {
	data, err := BuildAndSerializeIndex(tinyCorpus(), BuildOptions{
		Mode: ModeBM25, Chunker: "regex",
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	corrupt := make([]byte, len(data))
	copy(corrupt, data)
	copy(corrupt[:4], []byte("XXXX"))
	// Re-checksum so the CRC isn't the first thing that fails —
	// otherwise we'd just be testing CRC detection.
	bodyEnd := len(corrupt) - 4
	binary.LittleEndian.PutUint32(corrupt[bodyEnd:], crc32.ChecksumIEEE(corrupt[:bodyEnd]))

	_, err = LoadSerializedIndex(corrupt, LoadOptions{})
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("expected ErrCorrupt, got %v", err)
	}
	if !strings.Contains(err.Error(), "magic mismatch") {
		t.Errorf("error should mention magic mismatch: %v", err)
	}
}

// TestLoadSerialized_FormatVersionMismatch sets the format-version
// field to a future value and expects ErrFormatVersion.
func TestLoadSerialized_FormatVersionMismatch(t *testing.T) {
	data, err := BuildAndSerializeIndex(tinyCorpus(), BuildOptions{
		Mode: ModeBM25, Chunker: "regex",
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	corrupt := make([]byte, len(data))
	copy(corrupt, data)
	// Format version is bytes [4:8] after the magic.
	binary.LittleEndian.PutUint32(corrupt[4:8], 99)
	// Refresh CRC so the version check fires, not the CRC check.
	bodyEnd := len(corrupt) - 4
	binary.LittleEndian.PutUint32(corrupt[bodyEnd:], crc32.ChecksumIEEE(corrupt[:bodyEnd]))

	_, err = LoadSerializedIndex(corrupt, LoadOptions{})
	if !errors.Is(err, ErrFormatVersion) {
		t.Fatalf("expected ErrFormatVersion, got %v", err)
	}
}

// TestLoadSerialized_ModeMismatch builds a BM25 index and loads it
// asking for hybrid; expects ErrModeMismatch.
func TestLoadSerialized_ModeMismatch(t *testing.T) {
	data, err := BuildAndSerializeIndex(tinyCorpus(), BuildOptions{
		Mode: ModeBM25, Chunker: "regex",
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	_, err = LoadSerializedIndex(data, LoadOptions{ExpectedMode: "hybrid"})
	if !errors.Is(err, ErrModeMismatch) {
		t.Fatalf("expected ErrModeMismatch, got %v", err)
	}
}

// TestLoadSerialized_ChunkerMismatch builds with regex and asks for
// treesitter on load; expects ErrChunkerMismatch.
func TestLoadSerialized_ChunkerMismatch(t *testing.T) {
	data, err := BuildAndSerializeIndex(tinyCorpus(), BuildOptions{
		Mode: ModeBM25, Chunker: "regex",
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	_, err = LoadSerializedIndex(data, LoadOptions{ExpectedChunker: "treesitter"})
	if !errors.Is(err, ErrChunkerMismatch) {
		t.Fatalf("expected ErrChunkerMismatch, got %v", err)
	}
}

// TestLoadSerialized_CRCMismatch flips one byte in the middle and
// expects ErrCorrupt (CRC trailer catches it).
func TestLoadSerialized_CRCMismatch(t *testing.T) {
	data, err := BuildAndSerializeIndex(tinyCorpus(), BuildOptions{
		Mode: ModeBM25, Chunker: "regex",
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	corrupt := make([]byte, len(data))
	copy(corrupt, data)
	// Flip a byte in the middle (well past the header).
	corrupt[len(corrupt)/2] ^= 0xFF
	// Do NOT refresh the CRC — that's the whole point of this test.

	_, err = LoadSerializedIndex(corrupt, LoadOptions{})
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("expected ErrCorrupt, got %v", err)
	}
	if !strings.Contains(err.Error(), "crc") {
		t.Errorf("error should mention crc: %v", err)
	}
}

// TestLoadSerialized_ForwardCompatKenVer rewrites the ken-version
// string to a future value but keeps the format version at 1; expects
// successful load (kenVersion is informational). Rebuilds the LP
// section + tail rather than overwriting at fixed offset so the
// future-version string can be any length.
func TestLoadSerialized_ForwardCompatKenVer(t *testing.T) {
	data, err := BuildAndSerializeIndex(tinyCorpus(), BuildOptions{
		Mode: ModeBM25, Chunker: "regex",
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	// Layout of the prefix we replace:
	//   [0..4)    magic
	//   [4..8)    formatVersion
	//   [8..12)   kenVersion length
	//   [12..12+L) kenVersion bytes
	oldLen := binary.LittleEndian.Uint32(data[8:12])
	tail := data[12+oldLen : len(data)-4] // mode + chunker + numChunks + ... + sections
	future := "v999.0.0-pre-built-future"
	var rebuilt bytes.Buffer
	rebuilt.Write(data[:8]) // magic + formatVersion
	var u32 [4]byte
	binary.LittleEndian.PutUint32(u32[:], uint32(len(future)))
	rebuilt.Write(u32[:])
	rebuilt.WriteString(future)
	rebuilt.Write(tail)
	// Fresh CRC over the new body.
	crc := crc32.ChecksumIEEE(rebuilt.Bytes())
	binary.LittleEndian.PutUint32(u32[:], crc)
	rebuilt.Write(u32[:])

	if _, err := LoadSerializedIndex(rebuilt.Bytes(), LoadOptions{}); err != nil {
		t.Fatalf("expected successful load for future kenVersion, got %v", err)
	}
}

// TestSerializeRoundtrip_Determinism builds + serializes the same
// corpus twice and asserts byte-identical output. Regression guard
// for accidental map iteration / time.Now() leaks that would break
// reproducible-build comparisons.
func TestSerializeRoundtrip_Determinism(t *testing.T) {
	fsys := tinyCorpus()
	a, err := BuildAndSerializeIndex(fsys, BuildOptions{Mode: ModeBM25, Chunker: "regex"})
	if err != nil {
		t.Fatalf("build A: %v", err)
	}
	b, err := BuildAndSerializeIndex(fsys, BuildOptions{Mode: ModeBM25, Chunker: "regex"})
	if err != nil {
		t.Fatalf("build B: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("two builds of the same corpus produced different bytes (len A=%d B=%d)", len(a), len(b))
	}
}

// TestLoadSerialized_SemanticRequiresModel confirms ErrModelRequired
// fires when a semantic/hybrid index is loaded without a model.
func TestLoadSerialized_SemanticRequiresModel(t *testing.T) {
	model := loadTestModel(t)
	data, err := BuildAndSerializeIndex(tinyCorpus(), BuildOptions{
		Mode: ModeSemantic, Chunker: "regex", Model: model,
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	_, err = LoadSerializedIndex(data, LoadOptions{ /* no Model */ })
	if !errors.Is(err, ErrModelRequired) {
		t.Fatalf("expected ErrModelRequired, got %v", err)
	}
}
