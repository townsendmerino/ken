// Package bytesize parses human byte-size strings ("512MiB", "1GiB", or a
// bare byte count) shared by ken's memory/size knobs (KEN_MEMLIMIT,
// KEN_MAX_FILE_BYTES). The accepted suffixes mirror what the Go runtime
// takes for GOMEMLIMIT so the env vars read the same way.
package bytesize

import (
	"strconv"
	"strings"
)

// Parse parses a byte count with an optional binary (KiB/MiB/GiB) or decimal
// (KB/MB/GB) suffix, or a bare integer of bytes. Case-insensitive; leading/
// trailing whitespace and a single space before the unit are tolerated.
// Returns (n, true) on success, (0, false) on any malformed or overflowing
// input. Fractional values are not supported.
func Parse(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	numPart := s[:i]
	unit := strings.ToLower(strings.TrimSpace(s[i:]))
	if numPart == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(numPart, 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	var mult int64
	switch unit {
	case "", "b":
		mult = 1
	case "kib":
		mult = 1 << 10
	case "mib":
		mult = 1 << 20
	case "gib":
		mult = 1 << 30
	case "kb":
		mult = 1_000
	case "mb":
		mult = 1_000_000
	case "gb":
		mult = 1_000_000_000
	default:
		return 0, false
	}
	if mult != 0 && n > (1<<62)/mult {
		return 0, false // int64 overflow guard
	}
	return n * mult, true
}
