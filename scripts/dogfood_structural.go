//go:build ignore
// +build ignore

// dogfood_structural.go — quick smoke driver for internal/structural.
// Builds an index over the given directory (default: ken's own
// repo root) and probes a handful of common symbols. Useful for
// eyeballing the Stage 8 Track 2 lookup quality on a real corpus
// before exposing it through ken-mcp.
//
// Usage:  go run scripts/dogfood_structural.go [dir]

package main

import (
	"fmt"
	"os"

	"github.com/townsendmerino/ken/internal/structural"
)

func main() {
	dir := "."
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}
	ix, err := structural.Build(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "build:", err)
		os.Exit(1)
	}
	stats := ix.Stats()
	fmt.Printf("=== structural index over %s ===\n", dir)
	fmt.Printf("%d files indexed, %d unique top-level symbols, %d unique call targets\n",
		stats.IndexedFiles, stats.UniqueSymbols, stats.UniqueCallees)

	for _, sym := range []string{"NewServer", "Build", "Search", "Tokenize", "FromPath", "Predict"} {
		defs := ix.Definition(sym)
		refs := ix.References(sym)
		fmt.Printf("\n%s:\n  defs=%d  refs=%d", sym, len(defs), len(refs))
		if len(defs) > 0 {
			fmt.Printf("\n  defined in: %v", defs)
		}
		if len(refs) > 0 && len(refs) <= 5 {
			fmt.Printf("\n  referenced in:")
			for _, r := range refs {
				fmt.Printf("\n    %s [%v]", r.File, r.Kind)
			}
		} else if len(refs) > 5 {
			fmt.Printf("\n  (referenced in %d files; first 3: %s, %s, %s)",
				len(refs), refs[0].File, refs[1].File, refs[2].File)
		}
		fmt.Println()
	}
}
