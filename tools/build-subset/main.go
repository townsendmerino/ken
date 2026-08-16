// Command build-subset builds the slim `ken` + `ken-mcp` binaries — only the
// grammars ken's treesitter chunker dispatches, the same build .goreleaser.yml
// ships (ADR-033). Replaces scripts/build-subset.sh.
//
//	go run ./tools/build-subset [output-dir]   (default: bin/)
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/townsendmerino/ken/internal/devtools"
)

func main() {
	root, err := devtools.RepoRoot()
	if err != nil {
		fatal(err)
	}
	tags, err := devtools.SubsetTagsString(root)
	if err != nil {
		fatal(err)
	}
	out := "bin"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}
	if !filepath.IsAbs(out) {
		out = filepath.Join(root, out)
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		fatal(err)
	}
	fmt.Println("subset tags:", tags)
	for _, cmd := range []string{"ken", "ken-mcp"} {
		bin := filepath.Join(out, cmd)
		if err := devtools.Run(root, []string{"CGO_ENABLED=0"},
			"go", "build", "-tags", tags, "-o", bin, "./cmd/"+cmd); err != nil {
			fatal(err)
		}
		if fi, err := os.Stat(bin); err == nil {
			fmt.Printf("  built %s (%s)\n", bin, devtools.HumanBytes(fi.Size()))
		}
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "build-subset:", err)
	os.Exit(1)
}
