package binfmt

import (
	"bytes"
	"testing"
)

// TestAppendReadRoundtrip exercises the append/offset family: values encoded
// with Append* decode identically through ReadLPStringAt and manual offset
// reads, and byte offsets advance exactly as the writers laid them down.
func TestAppendReadRoundtrip(t *testing.T) {
	var b []byte
	b = AppendU32(b, 0xDEADBEEF)
	b = AppendU64(b, 0x0102030405060708)
	b = AppendI64(b, -42)
	b = AppendLPString(b, "hello")
	b = AppendLPString(b, "") // empty string is a valid 4-byte record

	// u32
	if got := u32(b[0:4]); got != 0xDEADBEEF {
		t.Fatalf("u32 = %08x, want DEADBEEF", got)
	}
	// u64
	if got := u64(b[4:12]); got != 0x0102030405060708 {
		t.Fatalf("u64 = %016x", got)
	}
	// i64
	if got := int64(u64(b[12:20])); got != -42 {
		t.Fatalf("i64 = %d, want -42", got)
	}
	// LP "hello"
	s, n, err := ReadLPStringAt(b[20:])
	if err != nil || s != "hello" || n != 4+len("hello") {
		t.Fatalf("ReadLPStringAt hello = (%q,%d,%v)", s, n, err)
	}
	// LP ""
	s, n, err = ReadLPStringAt(b[20+n:])
	if err != nil || s != "" || n != 4 {
		t.Fatalf("ReadLPStringAt empty = (%q,%d,%v)", s, n, err)
	}
}

// TestReadLPStringAt_Corrupt covers the two rejection paths: a buffer too short
// to hold the length prefix, and a length prefix that overruns the buffer.
func TestReadLPStringAt_Corrupt(t *testing.T) {
	if _, _, err := ReadLPStringAt([]byte{0, 0}); err == nil {
		t.Fatal("want error for sub-4-byte buffer")
	}
	// length prefix says 10 bytes but only 2 follow
	over := AppendU32(nil, 10)
	over = append(over, 'a', 'b')
	if _, _, err := ReadLPStringAt(over); err == nil {
		t.Fatal("want error for length overrun")
	}
}

// TestWriteReadRoundtrip exercises the streaming family against a *bytes.Reader,
// including the ReadLPString overrun guard.
func TestWriteReadRoundtrip(t *testing.T) {
	var buf bytes.Buffer
	WriteU32(&buf, 7)
	WriteLPString(&buf, "world")
	WriteLPString(&buf, "")

	r := bytes.NewReader(buf.Bytes())
	if v, err := ReadU32(r); err != nil || v != 7 {
		t.Fatalf("ReadU32 = (%d,%v)", v, err)
	}
	if s, err := ReadLPString(r); err != nil || s != "world" {
		t.Fatalf("ReadLPString = (%q,%v)", s, err)
	}
	if s, err := ReadLPString(r); err != nil || s != "" {
		t.Fatalf("ReadLPString empty = (%q,%v)", s, err)
	}

	// overrun: prefix claims 99 bytes with none following
	bad := bytes.NewReader(AppendU32(nil, 99))
	if _, err := ReadLPString(bad); err == nil {
		t.Fatal("want error for ReadLPString overrun")
	}
}

// tiny local LE decoders so the test doesn't depend on the funcs under test for
// its own assertions.
func u32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

func u64(b []byte) uint64 {
	return uint64(u32(b[0:4])) | uint64(u32(b[4:8]))<<32
}
