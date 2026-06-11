package search

// fuzz_test.go — robustness fuzzing for the two custom binary
// deserializers ken loads from potentially untrusted bytes:
//
//   - deserializeIndex   (KEN1) — ken-mcp auto-loads <repo>/.ken/index.bin,
//     and ken-mcp shallow-clones *remote* repos, so a hostile repo can put
//     attacker-controlled bytes in front of this parser. Memory-safe Go
//     caps the blast radius at DoS (panic / OOM / wedge), but a crash that
//     disconnects the agent is still a real failure mode.
//   - decodeRerankCache  (KNRC) — ~/.ken/rerank-cache-*.bin. Weaker
//     untrusted-input story (ken writes it itself) but the same class of
//     parser; cheap to cover.
//
// Both parsers are already hand-hardened against the classic adversarial
// inputs (numChunks=2^31, numChunks*embedDim*4 uint32-wrap, length-prefix
// lies, oversized entry counts). Those defenses are *hypotheses* until a
// fuzzer tries to break them — this file is that fuzzer.
//
// The CRC gate problem: both formats verify a CRC32 trailer before any
// structural parsing, so mutating a valid blob almost always fails CRC and
// the fuzzer never reaches the defensive code. To get past the gate, the
// harness treats the fuzz input as the *pre-CRC body*, appends a correct
// CRC, and feeds that in (driving the structural parser) — and *also*
// feeds the raw bytes (exercising the CRC-reject + too-short paths).
//
// The invariant under test is simply: never panic. Every input must come
// back as either a value or an error, and on success the returned shape
// must be self-consistent. Run with:
//
//	go test ./internal/search/ -run x -fuzz=FuzzDeserializeIndex   -fuzztime=30s
//	go test ./internal/search/ -run x -fuzz=FuzzDecodeRerankCache  -fuzztime=30s
//
// With no -fuzz flag, the seed corpus runs as ordinary unit tests on every
// `go test`, so the round-trip seeds keep guarding the happy path too.

import (
	"hash/crc32"
	"math"
	"testing"

	"github.com/townsendmerino/aikit/chunk"
)

// crcTrailerLE returns the 4-byte little-endian CRC32 (IEEE) of body — the
// trailer both formats expect. Appending it to a body makes a blob that
// passes the CRC gate, so the fuzzer reaches the structural parser.
func crcTrailerLE(body []byte) []byte {
	var t [4]byte
	crc := crc32.ChecksumIEEE(body)
	t[0] = byte(crc)
	t[1] = byte(crc >> 8)
	t[2] = byte(crc >> 16)
	t[3] = byte(crc >> 24)
	return t[:]
}

// withValidCRC returns body with a correct CRC trailer appended (fresh
// backing array — never aliases body).
func withValidCRC(body []byte) []byte {
	out := make([]byte, 0, len(body)+4)
	out = append(out, body...)
	return append(out, crcTrailerLE(body)...)
}

// ── KEN1 serialized-index deserializer ──────────────────────────────────

// indexSeedBodies returns pre-CRC bodies of well-formed serialized indices.
// After the harness re-appends a CRC these are valid blobs, giving the
// fuzzer high-quality starting points to mutate toward the defensive paths
// (numChunks bound, chunks-section length-prefix decoding, the BM25/vecs
// section guards, the trailing-bytes check).
func indexSeedBodies(t *testing.T) [][]byte {
	t.Helper()
	chunks := []chunk.Chunk{
		{File: "main.go", StartLine: 1, EndLine: 3, Text: "package main\n\nfunc main() {}"},
		{File: "util/x.go", StartLine: 10, EndLine: 10, Text: "x"},
		{File: "", StartLine: 1, EndLine: 1, Text: ""}, // empty file + text (the 17-byte minimum chunk)
	}

	var seeds [][]byte
	add := func(data []byte, err error) {
		if err != nil {
			t.Fatalf("seed serializeIndex: %v", err)
		}
		seeds = append(seeds, data[:len(data)-4]) // strip the CRC trailer → body
	}
	add(serializeIndex(chunks, nil, ModeBM25, "regex"))
	add(serializeIndex(nil, nil, ModeBM25, "regex")) // empty corpus
	// A hybrid body exercises header parsing + the numChunks bound; with
	// LoadOptions{} (no model) it bottoms out at ErrModelRequired before
	// vecs parsing — that's a clean error, not a panic.
	add(serializeIndex(chunks,
		[][]float32{{0.1, 0.2}, {-0.3, 0.4}, {0, 0}}, ModeHybrid, "regex"))
	return seeds
}

func FuzzDeserializeIndex(f *testing.F) {
	for _, body := range indexSeedBodies(&testing.T{}) {
		f.Add(body)
	}
	// Degenerate raw seeds (fed straight in, no CRC fix-up).
	f.Add([]byte{})
	f.Add([]byte("KEN1"))
	f.Add([]byte("KEN1\x01\x00\x00\x00"))

	f.Fuzz(func(t *testing.T, body []byte) {
		// (1) Drive the structural parser: a valid CRC over the fuzzed body.
		checkIndexNoPanic(t, withValidCRC(body))
		// (2) Exercise the CRC-reject + too-short paths: raw bytes as-is.
		checkIndexNoPanic(t, body)
	})
}

// checkIndexNoPanic asserts deserializeIndex neither panics (an unrecovered
// panic fails the fuzz run and is recorded as a crasher) nor returns an
// inconsistent (value, error) pair. LoadOptions{} carries no model, so a
// valid non-BM25 blob returns ErrModelRequired — still an error, not a panic.
func checkIndexNoPanic(t *testing.T, data []byte) {
	t.Helper()
	ix, err := deserializeIndex(data, LoadOptions{})
	if err == nil && ix == nil {
		t.Fatalf("deserializeIndex returned (nil, nil) — must be value-or-error")
	}
	if err != nil && ix != nil {
		t.Fatalf("deserializeIndex returned both a value and an error: %v", err)
	}
}

// ── KNRC rerank-cache deserializer ──────────────────────────────────────

// rerankSeedBodies returns pre-CRC bodies of well-formed rerank caches,
// built with the in-package append helpers so they track the on-disk
// format exactly. Covers the empty cache and a one-entry cache (so the
// per-entry read loop — key + reserved u64 + embedDim floats — is seeded).
func rerankSeedBodies() [][]byte {
	header := func(scope string, embedDim, entryCount uint32) []byte {
		b := []byte(rerankCacheMagic)
		b = appendU32(b, rerankCacheFormatVersion)
		b = appendLPString(b, rerankCacheKenVersion)
		b = appendLPString(b, scope)
		b = appendU32(b, embedDim)
		b = appendU32(b, entryCount)
		return b
	}

	empty := header("", 0, 0)

	oneEntry := header("model|int8|3", 3, 1)
	oneEntry = appendU64(oneEntry, 0xDEADBEEF) // key
	oneEntry = appendI64(oneEntry, 0)          // lastAccessUnix (reserved)
	for _, f := range []float32{0.5, -0.25, 0} {
		oneEntry = appendU32(oneEntry, math.Float32bits(f))
	}

	return [][]byte{empty, oneEntry}
}

func FuzzDecodeRerankCache(f *testing.F) {
	for _, body := range rerankSeedBodies() {
		f.Add(body)
	}
	f.Add([]byte{})
	f.Add([]byte("KNRC"))

	f.Fuzz(func(t *testing.T, body []byte) {
		// (1) Past the CRC gate → the entryCount cap, the exact-length
		//     check, and the per-entry read loop.
		checkRerankNoPanic(t, withValidCRC(body))
		// (2) CRC-reject + too-small paths.
		checkRerankNoPanic(t, body)
	})
}

func checkRerankNoPanic(t *testing.T, data []byte) {
	t.Helper()
	keys, vecs, err := decodeRerankCache(data, "", 0)
	if err == nil && len(keys) != len(vecs) {
		t.Fatalf("decodeRerankCache success but keys=%d vecs=%d (must match)", len(keys), len(vecs))
	}
	if err != nil && (keys != nil || vecs != nil) {
		t.Fatalf("decodeRerankCache error path returned non-nil slices: %v", err)
	}
}
