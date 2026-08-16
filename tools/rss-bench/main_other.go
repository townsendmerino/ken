//go:build !linux

package main

import (
	"fmt"
	"os"
)

// rss-bench reads VmRSS/VmHWM from /proc, which only exists on Linux. On other
// platforms it's a clear no-op so `go build ./...` stays green everywhere.
func main() {
	fmt.Fprintln(os.Stderr, "rss-bench: Linux only (samples /proc/<pid>/status). On macOS use '/usr/bin/time -l' (peak RSS) or Instruments.")
	os.Exit(2)
}
