// Command build-docs-mcp builds cmd/ken-mcp-docs by staging the Model2Vec model
// + ken's docs/ tree into the demo binary's directory (so //go:embed can pick
// them up — embed paths can't traverse ../). Output lands at bin/ken-mcp-docs.
// Replaces scripts/build-docs-mcp.sh — completing that script's own "No Python,
// no system C compiler" claim (it's now no bash either).
//
//	go run ./tools/build-docs-mcp
//
// Requires a Go toolchain. On first run, downloads the Model2Vec model into
// ~/.ken/model via the ken CLI; subsequent runs reuse it.
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
	home, err := os.UserHomeDir()
	if err != nil {
		fatal(err)
	}
	modelDir := filepath.Join(home, ".ken", "model")
	binKen := filepath.Join(root, "bin", "ken")

	fmt.Println("[1/4] Building ken CLI (needed for download-model)...")
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		fatal(err)
	}
	if err := devtools.Run(root, nil, "go", "build", "-o", binKen, "./cmd/ken"); err != nil {
		fatal(err)
	}

	if _, err := os.Stat(filepath.Join(modelDir, "model.safetensors")); err != nil {
		fmt.Println("[2/4] Downloading Model2Vec model to ~/.ken/model...")
		if err := devtools.Run(root, nil, binKen, "download-model"); err != nil {
			fatal(err)
		}
	} else {
		fmt.Println("[2/4] Reusing existing model at ~/.ken/model")
	}

	fmt.Println("[3/4] Staging embeds into cmd/ken-mcp-docs/...")
	stageModel := filepath.Join(root, "cmd", "ken-mcp-docs", "model")
	stageDocs := filepath.Join(root, "cmd", "ken-mcp-docs", "docs")
	for _, d := range []string{stageModel, stageDocs} {
		if err := os.RemoveAll(d); err != nil {
			fatal(err)
		}
		if err := os.MkdirAll(d, 0o755); err != nil {
			fatal(err)
		}
	}
	// Recursively copy the model dir (os.CopyFS, Go 1.23+) then the top-level
	// docs/*.md files (the embed only needs the markdown, not subdirs).
	if err := os.CopyFS(stageModel, os.DirFS(modelDir)); err != nil {
		fatal(fmt.Errorf("stage model: %w", err))
	}
	mds, err := filepath.Glob(filepath.Join(root, "docs", "*.md"))
	if err != nil {
		fatal(err)
	}
	for _, md := range mds {
		if err := copyFile(md, filepath.Join(stageDocs, filepath.Base(md))); err != nil {
			fatal(err)
		}
	}

	fmt.Println("[4/4] Building bin/ken-mcp-docs...")
	// The embed_corpus build tag activates the //go:embed directives in
	// cmd/ken-mcp-docs/main.go. Without the tag the package has zero buildable
	// Go files, so `go build ./...` skips it cleanly on a fresh clone.
	binDocs := filepath.Join(root, "bin", "ken-mcp-docs")
	if err := devtools.Run(root, nil, "go", "build", "-tags=embed_corpus", "-o", binDocs, "./cmd/ken-mcp-docs"); err != nil {
		fatal(err)
	}
	if fi, err := os.Stat(binDocs); err == nil {
		fmt.Printf("\nDone. bin/ken-mcp-docs is %s.\n", devtools.HumanBytes(fi.Size()))
	}
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "build-docs-mcp:", err)
	os.Exit(1)
}
