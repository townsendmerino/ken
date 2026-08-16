// Package binfmt holds the little-endian primitives shared by ken's three
// hand-rolled binary-envelope formats — KEN1 (index_serialize.go), KNRC
// (rerank_cache.go), and KMAN (snapshot.go). Each format is a magic + version +
// length-prefixed fields + CRC32 trailer; they previously each defined their
// own copies of these u32/u64/length-prefixed-string helpers (rerank_cache.go's
// comment noted the duplication the first time it recurred). This package is
// that single home, so a fourth format reuses the primitives instead of copying
// them a fourth time.
//
// Two families are provided because the three formats build/parse in two
// legitimately different styles, and forcing one onto the other would be a
// bigger, riskier change than the duplication it removes:
//
//   - Append/offset family (Append* / ReadLPStringAt): build a []byte payload
//     in memory, CRC it, then atomic-write — used by KNRC and KMAN, which CRC
//     the whole body before writing.
//   - Streaming family (Write* on *bytes.Buffer, Read* on *bytes.Reader): used
//     by KEN1, which writes/reads section-at-a-time with back-patched lengths.
//
// "LP string" = uint32 LE length prefix + UTF-8 bytes, in every format. Every
// function here is byte-for-byte identical to the inline copies it replaced, so
// existing on-disk files round-trip unchanged.
package binfmt

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
)

// ── Append/offset family (byte-slice payloads) ──────────────────────────────

// AppendU32 appends v as 4 little-endian bytes.
func AppendU32(b []byte, v uint32) []byte {
	return binary.LittleEndian.AppendUint32(b, v)
}

// AppendU64 appends v as 8 little-endian bytes.
func AppendU64(b []byte, v uint64) []byte {
	return binary.LittleEndian.AppendUint64(b, v)
}

// AppendI64 appends v as 8 little-endian bytes (two's-complement bit pattern).
func AppendI64(b []byte, v int64) []byte {
	return AppendU64(b, uint64(v))
}

// AppendLPString appends s as a uint32-LE length prefix followed by its bytes.
func AppendLPString(b []byte, s string) []byte {
	b = AppendU32(b, uint32(len(s)))
	return append(b, s...)
}

// ReadLPStringAt reads a uint32-LE length-prefixed UTF-8 string from the front
// of buf and returns the string plus the number of bytes consumed (4 + len).
// The slice-offset shape fits the CRC-then-scan parse flow of KNRC / KMAN.
func ReadLPStringAt(buf []byte) (string, int, error) {
	if len(buf) < 4 {
		return "", 0, io.ErrUnexpectedEOF
	}
	n := binary.LittleEndian.Uint32(buf[:4])
	if uint64(4)+uint64(n) > uint64(len(buf)) {
		return "", 0, fmt.Errorf("string length %d exceeds remaining buffer %d", n, len(buf)-4)
	}
	return string(buf[4 : 4+n]), int(4 + n), nil
}

// ── Streaming family (*bytes.Buffer / *bytes.Reader) ────────────────────────

// WriteU32 writes v as 4 little-endian bytes into buf.
func WriteU32(buf *bytes.Buffer, v uint32) {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	buf.Write(b[:])
}

// WriteLPString writes s as a uint32-LE length prefix followed by its bytes.
func WriteLPString(buf *bytes.Buffer, s string) {
	WriteU32(buf, uint32(len(s)))
	buf.WriteString(s)
}

// ReadU32 reads 4 little-endian bytes from r.
func ReadU32(r *bytes.Reader) (uint32, error) {
	var b [4]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b[:]), nil
}

// ReadLPString reads a uint32-LE length-prefixed UTF-8 string from r.
func ReadLPString(r *bytes.Reader) (string, error) {
	n, err := ReadU32(r)
	if err != nil {
		return "", err
	}
	if n > uint32(r.Len()) {
		return "", fmt.Errorf("len-prefix %d > remaining %d", n, r.Len())
	}
	if n == 0 {
		return "", nil
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	return string(b), nil
}
