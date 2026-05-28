//go:build embed_corpus

// Command ken-mcp-docs is the v0.6.0 embedded-corpus demo: a single
// static binary MCP server that serves search/find_related over ken's
// own docs/ directory with the Model2Vec model baked in via //go:embed.
// It is the canonical worked example of the build pattern ADR-016
// introduces — SDK authors can ship docs as single-binary MCP servers
// for their tooling with no per-query network egress, no operator
// burden, and structural agent sandboxing (no path-resolution → no
// path-traversal escape from the embedded corpus).
//
// Build:
//
//	scripts/build-docs-mcp.sh
//
// The script stages ~/.ken/model and docs/*.md into this directory
// (so //go:embed can pick them up — embed paths can't traverse ../),
// then runs `go build -tags=embed_corpus -o bin/ken-mcp-docs ./cmd/ken-mcp-docs`.
//
// The build tag `embed_corpus` is mandatory: this file is excluded from
// the default `go build ./...` so a fresh clone where
// cmd/ken-mcp-docs/{model,docs}/ don't yet exist still builds cleanly.
// Without the tag, this package has zero buildable Go files and Go's
// toolchain skips it silently.
//
// stdout/stderr contract: stdout is the JSON-RPC channel; all logs go
// to stderr via mcp.Run's default LogWriter. See cmd/ken-mcp/main.go
// for the full version of the contract — corruption of stdout is the
// most common new-MCP-server failure and the load-bearing rule for
// any binary built on github.com/townsendmerino/ken/mcp.
package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"os"

	"github.com/townsendmerino/ken/mcp"

	// The markdown chunker is the right default for a docs corpus —
	// heading-aware boundaries, atomic fenced-code / tables / lists.
	// internal/search blank-imports the regex chunker (the universal
	// default); the line chunker is auto-registered by internal/chunk
	// itself. NOT imported: internal/chunk/treesitter — importing it
	// would inflate this binary by ~26 MB (darwin/arm64; the
	// gotreesitter/grammars embed.FS payload is ~19 MB on-disk for
	// 206 grammar blobs, plus parser runtime) for zero benefit on a
	// docs-only corpus. See ADR-023 for the per-language-gating
	// investigation outcome.
	_ "github.com/townsendmerino/ken/chunk/markdown"
)

// docsFS embeds every .md file under cmd/ken-mcp-docs/docs/ at build
// time. scripts/build-docs-mcp.sh copies docs/*.md from the repo root
// into here before invoking `go build`. The resulting fs.FS has files
// at "docs/<name>.md" paths; fs.Sub rebases to "<name>.md" so search
// result paths stay clean.
//
//go:embed docs/*.md
var docsFS embed.FS

// modelFS embeds the Model2Vec snapshot files. Listed explicitly (not
// model/* with a glob) because we want the build to fail loudly if any
// of the three files is missing — a half-staged model would otherwise
// surface as a runtime "tensor not found" error.
//
//go:embed model/tokenizer.json model/config.json model/model.safetensors
var modelFS embed.FS

func main() {
	// Rebase the embedded filesystems so their root corresponds to
	// the corpus / model directory (otherwise mcp.Run and
	// embed.LoadFromFS would have to know about the "docs/" / "model/"
	// prefix — the fs.Sub call keeps that detail local to this binary).
	docsSub, err := fs.Sub(docsFS, "docs")
	if err != nil {
		log.Fatalf("ken-mcp-docs: fs.Sub(docs): %v", err)
	}
	modelSub, err := fs.Sub(modelFS, "model")
	if err != nil {
		log.Fatalf("ken-mcp-docs: fs.Sub(model): %v", err)
	}

	if err := mcp.Run(context.Background(), docsSub, mcp.Options{
		Mode:        "hybrid",
		ChunkerName: "markdown",
		ModelFS:     modelSub,
		LogWriter:   os.Stderr, // belt + suspenders: never stdout
	}); err != nil {
		log.Fatalf("ken-mcp-docs: %v", err)
	}
}
