// rerank_cache.go — M9 persistent on-disk doc cache.
//
// The reranker's content-hash → embedding LRU is the "perf keystone"
// (plan §8): a warm cache turns a 5 s cold rerankN=50 into ~0.6 s by
// skipping the 50 candidate-forward passes. Pre-M9 the cache was
// in-process only — every fresh ken-mcp launch (agent restart, host
// reboot, container redeploy) paid the cold cost again.
//
// M9 persists the LRU to disk so subsequent ken-mcp launches start
// warm. The on-disk file extends the KEN1-family binary-format
// convention (custom binary + 4-byte magic + uint32 version + CRC32
// trailer; same atomic tmp+rename write path as v0.8.3 prebuilt
// indices). See [[reference-prebuilt-indices]] for the parent format.
//
// On-disk binary format ("KNRC" = Ken Rerank Cache):
//
//	[4]byte    magic = "KNRC"
//	uint32 LE  formatVersion (current = 1)
//	string LP  kenVersion (informational; e.g. "v0.9.0")
//	string LP  scopeKey   (e.g. "coderankembed/f32/dim=768")
//	uint32 LE  embedDim
//	uint32 LE  entryCount
//	For each entry (entryCount times):
//	  uint64 LE  contentHash (fnv64a; matches the LRU's in-memory key)
//	  int64  LE  lastAccessUnix (informational; LRU order is positional)
//	  [embedDim * 4]byte vector (float32 LE, L2-normalized — same shape
//	                             the LRU stores)
//	uint32 LE  CRC32 IEEE over every byte above
//
// "string LP" = uint32 LE length prefix + UTF-8 bytes.
//
// Entries are written in least-recent → most-recent LRU order so a
// later LoadCacheFromFile + restore reproduces the same eviction
// priority — the file is a faithful snapshot of the live cache state.
//
// scopeKey is the load-time gate. Loading a cache built with
// `coderankembed/f32/dim=768` into a reranker configured for
// `coderankembed/int8/dim=768` returns ErrCacheScopeMismatch — the
// embeddings would be wrong-precision and the rerank cosines junk.
// Callers should treat that error as "regenerate the cache" (typically
// by ignoring it and starting cold).
//
// File-level concurrency model: last-writer-wins. Multiple ken-mcp
// processes sharing one cache file is a defensible degraded state —
// each writes its own snapshot at shutdown, the most recent write
// "wins," older entries from concurrent processes are lost. The cache
// is a perf optimization, not a correctness primitive, so a stale
// entry simply causes a re-encode (the LRU re-fills correctly).

package search

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"os"
	"path/filepath"
)

const (
	// rerankCacheMagic — 4-byte file header. Deliberately distinct
	// from "KEN1" (the prebuilt-index magic) so a misaimed reader
	// fails loudly instead of attempting to decode the wrong shape.
	rerankCacheMagic = "KNRC"

	// rerankCacheFormatVersion — bump on any incompatible byte-layout
	// change. Old caches that can't be read are silently treated as
	// "first run" (returns ErrCacheFormatVersion; caller starts cold).
	rerankCacheFormatVersion uint32 = 1

	// rerankCacheKenVersion — informational; helps diagnose stale
	// caches in the field without gating loadability.
	rerankCacheKenVersion = "v1.0.1"

	// maxRerankCacheEntries is a sanity cap on entryCount from a
	// header — protects against a hostile / corrupt file claiming
	// entryCount=2^31 and triggering an OOM pre-allocation. 1 M
	// entries × 3 KB ≈ 3 GB on disk, well past anything realistic.
	maxRerankCacheEntries = 1 << 20
)

// Typed errors returned by LoadCacheFromFile. errors.Is matches each
// sentinel so callers can decide whether to surface, ignore, or
// rebuild.
var (
	// ErrCacheCorrupt — magic mismatch, CRC mismatch, short read,
	// invalid lengths. Caller should treat as "regenerate" (delete +
	// start cold).
	ErrCacheCorrupt = errors.New("search: rerank cache file is corrupt")

	// ErrCacheFormatVersion — on-disk format version differs from
	// this ken build. Same handling as ErrCacheCorrupt: start cold,
	// the next save will write the new format.
	ErrCacheFormatVersion = errors.New("search: rerank cache has incompatible format version")

	// ErrCacheScopeMismatch — the scopeKey in the file doesn't match
	// the caller's expected scope (model / quant / dim). Loading
	// would mix wrong-precision embeddings into the live cache and
	// produce junk cosines. Caller MUST NOT install the loaded
	// entries; "start cold" is the only safe answer.
	ErrCacheScopeMismatch = errors.New("search: rerank cache scope (model/quant/dim) mismatch")

	// ErrCacheEmbedDimMismatch — the embedDim in the file doesn't
	// match the caller's expected dim. Distinct from ScopeMismatch
	// (which catches model/quant changes); this catches a different
	// model entirely (e.g. swapping CodeRankEmbed for a 1024-dim
	// alternative). Same handling: start cold.
	ErrCacheEmbedDimMismatch = errors.New("search: rerank cache embedding dimension mismatch")
)

// CacheScopeKey builds the scopeKey string from its components. The
// canonical shape — model name + quantization + dim — guarantees a
// stale cache from a prior quant or dim won't poison the live cache.
//
// Callers should pass exactly the strings used when the reranker was
// loaded. cmd/ken-mcp and cmd/ken both go through this helper so the
// format is consistent across entry points.
func CacheScopeKey(modelName, quant string, embedDim int) string {
	return fmt.Sprintf("%s/%s/dim=%d", modelName, quant, embedDim)
}

// SaveCacheToFile writes the reranker's LRU contents to path,
// scope-tagged so a later LoadCacheFromFile can reject a wrong-
// precision load. Atomic via tmp+rename — readers see either the old
// file or the new file, never a half-written one.
//
// path's parent directory is created with 0755 if it doesn't exist.
// Empty path is a no-op (caller passed "" to disable persistence);
// nil reranker is a no-op; an LRU with zero entries STILL writes the
// file (so the next load gets a well-formed empty cache rather than
// ErrCacheCorrupt from a missing-file fallback).
//
// Returns nil on success or a wrapped i/o error. Crucially, it does
// NOT return a "we wrote 0 entries" signal — that's reflected in the
// file itself, and the caller logs the entry count from CacheStats().
func SaveCacheToFile(r *NeuralReranker, path, scopeKey string, embedDim int) error {
	if r == nil || path == "" {
		return nil
	}
	keys, vecs := r.cache.snapshot()
	if len(keys) != len(vecs) {
		return fmt.Errorf("search: SaveCacheToFile snapshot length mismatch (%d keys, %d vecs)", len(keys), len(vecs))
	}
	// Build the in-memory payload, then CRC, then write+rename.
	// Single allocation upfront — bounded by entry count × (8 + 8 +
	// embedDim*4) + the small header — so we can compute size exactly.
	headerLen := 4 /*magic*/ + 4 /*version*/ +
		4 + len(rerankCacheKenVersion) +
		4 + len(scopeKey) +
		4 /*embedDim*/ + 4 /*entryCount*/
	entryLen := 8 /*hash*/ + 8 /*lastAccess (always 0 today)*/ + embedDim*4
	body := make([]byte, 0, headerLen+len(keys)*entryLen)

	body = append(body, []byte(rerankCacheMagic)...)
	body = appendU32(body, rerankCacheFormatVersion)
	body = appendLPString(body, rerankCacheKenVersion)
	body = appendLPString(body, scopeKey)
	body = appendU32(body, uint32(embedDim))
	body = appendU32(body, uint32(len(keys)))

	for i, k := range keys {
		v := vecs[i]
		if len(v) != embedDim {
			return fmt.Errorf("search: SaveCacheToFile entry %d has %d-dim vec, want %d", i, len(v), embedDim)
		}
		body = appendU64(body, k)
		body = appendI64(body, 0) // lastAccessUnix reserved; LRU order is positional
		for _, x := range v {
			body = appendU32(body, math.Float32bits(x))
		}
	}
	crc := crc32.ChecksumIEEE(body)
	body = appendU32(body, crc)

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("search: SaveCacheToFile mkdir: %w", err)
	}
	// Atomic write: tmp file → rename. The rename is atomic on POSIX
	// when src+dst are on the same filesystem (they are: both under
	// the same parent dir).
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return fmt.Errorf("search: SaveCacheToFile write tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("search: SaveCacheToFile rename: %w", err)
	}
	return nil
}

// LoadCacheFromFile reads path and replaces r.cache's contents with
// the on-disk entries. The expectedScopeKey + expectedEmbedDim
// parameters gate the load — mismatches return typed errors so the
// caller can distinguish "first run" (file doesn't exist) from
// "regenerate" (corrupt / version skew) from "wrong precision"
// (scope mismatch).
//
// Behavior:
//
//	path doesn't exist           → returns (0, os.ErrNotExist) wrapped;
//	                               caller should treat as "first run"
//	                               (no entries loaded; cache stays empty).
//	corrupt / CRC mismatch       → returns ErrCacheCorrupt; caller may
//	                               delete + restart.
//	version mismatch             → returns ErrCacheFormatVersion.
//	scope or dim mismatch        → returns ErrCacheScopeMismatch /
//	                               ErrCacheEmbedDimMismatch.
//	success                      → returns (entryCount, nil); LRU is
//	                               now populated in original order.
//
// On any error, r.cache is NOT modified (the load operation is
// atomic: failure halfway through the file leaves the live cache
// untouched).
func LoadCacheFromFile(r *NeuralReranker, path, expectedScopeKey string, expectedEmbedDim int) (int, error) {
	if r == nil || path == "" {
		return 0, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	keys, vecs, err := decodeRerankCache(data, expectedScopeKey, expectedEmbedDim)
	if err != nil {
		return 0, err
	}
	r.cache.restore(keys, vecs)
	return len(keys), nil
}

// decodeRerankCache parses the binary blob and returns the LRU entries
// (oldest-first ordering). Factored from LoadCacheFromFile so tests can
// drive corner cases without touching the filesystem.
func decodeRerankCache(data []byte, expectedScopeKey string, expectedEmbedDim int) ([]uint64, [][]float32, error) {
	const minSize = 4 /*magic*/ + 4 /*version*/ + 4 /*kenVer LP*/ +
		4 /*scopeKey LP*/ + 4 /*embedDim*/ + 4 /*entryCount*/ + 4 /*CRC*/
	if len(data) < minSize {
		return nil, nil, fmt.Errorf("%w: file too small (%d < %d bytes)", ErrCacheCorrupt, len(data), minSize)
	}

	body := data[:len(data)-4]
	gotCRC := binary.LittleEndian.Uint32(data[len(data)-4:])
	wantCRC := crc32.ChecksumIEEE(body)
	if gotCRC != wantCRC {
		return nil, nil, fmt.Errorf("%w: CRC mismatch (got %08x, want %08x)", ErrCacheCorrupt, gotCRC, wantCRC)
	}

	p := 0
	if string(body[p:p+4]) != rerankCacheMagic {
		return nil, nil, fmt.Errorf("%w: magic mismatch (got %q, want %q)", ErrCacheCorrupt, body[p:p+4], rerankCacheMagic)
	}
	p += 4

	version := binary.LittleEndian.Uint32(body[p:])
	p += 4
	if version != rerankCacheFormatVersion {
		return nil, nil, fmt.Errorf("%w: file version %d, this ken speaks %d", ErrCacheFormatVersion, version, rerankCacheFormatVersion)
	}

	_, n, err := readLPStringAt(body[p:])
	if err != nil {
		return nil, nil, fmt.Errorf("%w: kenVersion: %v", ErrCacheCorrupt, err)
	}
	p += n

	gotScope, n, err := readLPStringAt(body[p:])
	if err != nil {
		return nil, nil, fmt.Errorf("%w: scopeKey: %v", ErrCacheCorrupt, err)
	}
	p += n
	if expectedScopeKey != "" && gotScope != expectedScopeKey {
		return nil, nil, fmt.Errorf("%w: file scope %q, expected %q", ErrCacheScopeMismatch, gotScope, expectedScopeKey)
	}

	if len(body)-p < 8 {
		return nil, nil, fmt.Errorf("%w: short header (embedDim+entryCount)", ErrCacheCorrupt)
	}
	embedDim := binary.LittleEndian.Uint32(body[p:])
	p += 4
	if expectedEmbedDim > 0 && int(embedDim) != expectedEmbedDim {
		return nil, nil, fmt.Errorf("%w: file dim %d, expected %d", ErrCacheEmbedDimMismatch, embedDim, expectedEmbedDim)
	}

	entryCount := binary.LittleEndian.Uint32(body[p:])
	p += 4
	if entryCount > maxRerankCacheEntries {
		return nil, nil, fmt.Errorf("%w: entryCount %d exceeds cap %d", ErrCacheCorrupt, entryCount, maxRerankCacheEntries)
	}

	entryLen := 8 + 8 + int(embedDim)*4
	wantBodyLen := int(entryCount) * entryLen
	if len(body)-p != wantBodyLen {
		return nil, nil, fmt.Errorf("%w: entry section length %d, expected %d (entryCount=%d entryLen=%d)",
			ErrCacheCorrupt, len(body)-p, wantBodyLen, entryCount, entryLen)
	}

	keys := make([]uint64, 0, entryCount)
	vecs := make([][]float32, 0, entryCount)
	for range entryCount {
		k := binary.LittleEndian.Uint64(body[p:])
		p += 8
		_ = binary.LittleEndian.Uint64(body[p:]) // lastAccessUnix reserved
		p += 8
		v := make([]float32, embedDim)
		for j := range v {
			v[j] = math.Float32frombits(binary.LittleEndian.Uint32(body[p:]))
			p += 4
		}
		keys = append(keys, k)
		vecs = append(vecs, v)
	}
	return keys, vecs, nil
}

// ── encoding helpers (mirror internal/search/index_serialize.go's
// equivalents but kept local to keep that file's surface stable) ────

func appendU32(b []byte, v uint32) []byte {
	var tmp [4]byte
	binary.LittleEndian.PutUint32(tmp[:], v)
	return append(b, tmp[:]...)
}

func appendU64(b []byte, v uint64) []byte {
	var tmp [8]byte
	binary.LittleEndian.PutUint64(tmp[:], v)
	return append(b, tmp[:]...)
}

func appendI64(b []byte, v int64) []byte {
	return appendU64(b, uint64(v))
}

func appendLPString(b []byte, s string) []byte {
	b = appendU32(b, uint32(len(s)))
	return append(b, s...)
}

// readLPStringAt reads a uint32-LE length-prefixed UTF-8 string from
// the front of buf and returns the string + bytes consumed. Distinct
// from index_serialize.go's readLPString (which takes *bytes.Reader);
// this slice-offset variant fits the CRC-then-scan flow above.
func readLPStringAt(buf []byte) (string, int, error) {
	if len(buf) < 4 {
		return "", 0, io.ErrUnexpectedEOF
	}
	n := binary.LittleEndian.Uint32(buf[:4])
	if uint64(4)+uint64(n) > uint64(len(buf)) {
		return "", 0, fmt.Errorf("string length %d exceeds remaining buffer %d", n, len(buf)-4)
	}
	return string(buf[4 : 4+n]), int(4 + n), nil
}
