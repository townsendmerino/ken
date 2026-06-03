//go:build ignore

// phase0_memory_probe.go — measure structural.Index resident memory
// + CallRef counts on a real corpus, to gate Phase 0's ≤2× budget.
//
// Run:
//
//	go run scripts/phase0_memory_probe.go /tmp/ken-dogfood/jekyll
package main

import (
	"fmt"
	"os"
	"runtime"

	"github.com/townsendmerino/ken/internal/structural"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: phase0_memory_probe <corpus_root>")
		os.Exit(1)
	}
	root := os.Args[1]

	var before runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	ix, err := structural.Build(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Build:", err)
		os.Exit(1)
	}

	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	// Tally CallRefs + functions + classes across every file.
	var nFiles, nFuncs, nClasses, nCallRefs, nUniqueCallees int
	files := ix.Files()
	for _, f := range files {
		nFiles++
		nFuncs += len(f.Functions)
		nClasses += len(f.Classes)
		nCallRefs += len(f.CallRefs)
		nUniqueCallees += len(f.CalleeNames())
	}

	fmt.Printf("corpus:          %s\n", root)
	fmt.Printf("files indexed:   %d\n", nFiles)
	fmt.Printf("functions:       %d\n", nFuncs)
	fmt.Printf("classes:         %d\n", nClasses)
	fmt.Printf("CallRefs total:  %d (Phase 0 substrate)\n", nCallRefs)
	fmt.Printf("unique callees:  %d (what CalleeNames() returns; what Arm B sees)\n", nUniqueCallees)
	fmt.Printf("CallRef density: %.1f per file (avg)\n", float64(nCallRefs)/float64(nFiles))
	fmt.Println()
	fmt.Printf("resident delta (after Build − pre):\n")
	fmt.Printf("  HeapAlloc: %s → %s (+%s)\n", mib(before.HeapAlloc), mib(after.HeapAlloc), mib(after.HeapAlloc-before.HeapAlloc))
	fmt.Printf("  HeapInuse: %s → %s (+%s)\n", mib(before.HeapInuse), mib(after.HeapInuse), mib(after.HeapInuse-before.HeapInuse))
	fmt.Printf("  Sys:       %s → %s (+%s)\n", mib(before.Sys), mib(after.Sys), mib(after.Sys-before.Sys))
}

func mib(b uint64) string {
	return fmt.Sprintf("%.1f MiB", float64(b)/(1<<20))
}
