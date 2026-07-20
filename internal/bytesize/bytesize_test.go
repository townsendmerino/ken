package bytesize

import "testing"

func TestParse(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"0", 0, true},
		{"1024", 1024, true},
		{"536870912", 536870912, true},
		{"1KiB", 1 << 10, true},
		{"512MiB", 512 << 20, true},
		{"1GiB", 1 << 30, true},
		{"2GiB", 2 << 30, true},
		{"1KB", 1_000, true},
		{"10MB", 10_000_000, true},
		{"1GB", 1_000_000_000, true},
		{"512mib", 512 << 20, true},
		{"  1 GiB ", 1 << 30, true},
		{"1gib", 1 << 30, true},
		{"", 0, false},
		{"abc", 0, false},
		{"GiB", 0, false},
		{"1.5GiB", 0, false},
		{"-1", 0, false},
		{"1TiB", 0, false},
		{"9999999999999999999GiB", 0, false},
	}
	for _, c := range cases {
		got, ok := Parse(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("Parse(%q) = (%d, %v), want (%d, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}
