package main

import "testing"

func TestParseByteSize(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		// bare bytes
		{"0", 0, true},
		{"1024", 1024, true},
		{"536870912", 536870912, true},
		// binary suffixes (what GOMEMLIMIT uses)
		{"1KiB", 1 << 10, true},
		{"512MiB", 512 << 20, true},
		{"1GiB", 1 << 30, true},
		{"2GiB", 2 << 30, true},
		// decimal suffixes
		{"1KB", 1_000, true},
		{"10MB", 10_000_000, true},
		{"1GB", 1_000_000_000, true},
		// case / whitespace tolerance
		{"512mib", 512 << 20, true},
		{"  1 GiB ", 1 << 30, true},
		{"1gib", 1 << 30, true},
		// invalid
		{"", 0, false},
		{"abc", 0, false},
		{"GiB", 0, false},
		{"1.5GiB", 0, false}, // no fractional support
		{"-1", 0, false},
		{"1TiB", 0, false},                   // unsupported unit
		{"9999999999999999999GiB", 0, false}, // overflow guard
	}
	for _, c := range cases {
		got, ok := parseByteSize(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("parseByteSize(%q) = (%d, %v), want (%d, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}
