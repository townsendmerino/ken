package search

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzDecodeManifest: the KMAN manifest reader parses untrusted bytes
// (<repo>/.ken/snapshot.manifest — an operator or attacker can hand it
// anything). It must NEVER panic, and any manifest it *accepts* must
// round-trip through Encode/DecodeManifest unchanged.
func FuzzDecodeManifest(f *testing.F) {
	f.Add(SnapshotManifest{ConfigKey: "v1|mode=0", Files: []FileStamp{{File: "a.go", MTimeNano: 1, Size: 2}}}.Encode())
	f.Add(SnapshotManifest{ConfigKey: "k"}.Encode())
	f.Add([]byte(manifestMagic))
	f.Add([]byte(manifestMagic + "\x01\x00\x00\x00"))
	f.Add([]byte{})
	f.Add([]byte("KMAN garbage tail"))

	f.Fuzz(func(t *testing.T, data []byte) {
		m, err := DecodeManifest(data) // must not panic on any input
		if err != nil {
			return
		}
		// Accepted → must round-trip.
		m2, err2 := DecodeManifest(m.Encode())
		if err2 != nil {
			t.Fatalf("re-decode of an accepted manifest failed: %v", err2)
		}
		if m2.ConfigKey != m.ConfigKey || !m2.FilesEqual(m) {
			t.Fatalf("manifest round-trip mismatch:\n got  %+v\n want %+v", m2, m)
		}
	})
}

// FuzzLoadSerializedCorpus: the KEN1 loader parses untrusted bytes
// (<repo>/.ken/snapshot.bin). Bit-flips, truncations, and hostile
// length-prefixes must all yield a clean error, never a panic or a runaway
// allocation. (ADR-024's CRC is not a MAC — the contract here is
// crash-safety, not tamper resistance.)
func FuzzLoadSerializedCorpus(f *testing.F) {
	dir := f.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"),
		[]byte("package p\nfunc Alpha() {}\nfunc Beta() {}\n"), 0o644); err != nil {
		f.Fatal(err)
	}
	if data, err := BuildAndSerializeIndex(os.DirFS(dir), BuildOptions{Mode: ModeBM25, Chunker: "line"}); err == nil {
		f.Add(data)
	}
	f.Add([]byte(serializeMagic))
	f.Add([]byte{})
	f.Add(make([]byte, 64)) // all-zero body

	f.Fuzz(func(t *testing.T, data []byte) {
		// BM25 opts so a valid seed exercises the full parse without needing a
		// model. Must not panic / OOM on arbitrary bytes.
		_, _, _ = LoadSerializedCorpus(data, LoadOptions{})
	})
}
