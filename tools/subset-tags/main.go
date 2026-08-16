// Command subset-tags prints the space-separated gotreesitter grammar_subset
// build tags for ken's slim build — the exact set .goreleaser.yml ships
// (ADR-033). Replaces scripts/subset-tags.sh; CI and the build-subset command
// call this so local slim builds, the CI compile-smoke, and the release use one
// source of truth.
//
//	go run ./tools/subset-tags
package main

import (
	"fmt"
	"os"

	"github.com/townsendmerino/ken/internal/devtools"
)

func main() {
	root, err := devtools.RepoRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, "subset-tags:", err)
		os.Exit(1)
	}
	tags, err := devtools.SubsetTagsString(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "subset-tags:", err)
		os.Exit(1)
	}
	fmt.Println(tags)
}
