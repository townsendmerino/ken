//go:build unix

package main

import (
	"os"
	"runtime"
	"syscall"
)

// peakRSSBytes returns the child's peak resident set size in bytes from
// getrusage's ru_maxrss. Unit differs by OS: Linux reports kilobytes, the BSDs
// (incl. macOS) report bytes — normalized here.
func peakRSSBytes(st *os.ProcessState) int64 {
	if st == nil {
		return 0
	}
	ru, ok := st.SysUsage().(*syscall.Rusage)
	if !ok {
		return 0
	}
	maxrss := int64(ru.Maxrss)
	switch runtime.GOOS {
	case "darwin", "ios", "freebsd", "netbsd", "openbsd", "dragonfly":
		return maxrss // already bytes
	default:
		return maxrss * 1024 // linux/solaris: kilobytes → bytes
	}
}
