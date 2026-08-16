//go:build !unix

package main

import "os"

// peakRSSBytes is unavailable off unix (no getrusage ru_maxrss); the report then
// prints "unavailable" for RSS but still reports the wall/user/system times.
func peakRSSBytes(*os.ProcessState) int64 { return 0 }
