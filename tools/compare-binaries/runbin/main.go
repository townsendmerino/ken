// Command runbin is a test server built on mcp.Run, for the before/after
// comparison in tools/compare-binaries. ken ships no mcp.Run binary over a
// code corpus (cmd/ken-mcp-docs serves markdown, the demos serve frozen
// pre-built indexes), so this one exists to let compare.py drive that shape
// from an old and a new build: a fixed fs.FS, never watching, an optional
// pre-built index, optional DB chunks.
//
// It is not shipped. Configuration comes from the environment, since
// compare.py controls it completely:
//
//	KEN_RUNBIN_CORPUS  directory served as the corpus (required)
//	KEN_RUNBIN_MODE    bm25 | semantic | hybrid (default bm25)
//	KEN_RUNBIN_MODEL   embedding model directory (needed unless bm25)
//	KEN_RUNBIN_DSN     database DSN; when set, DB chunks are wired through
//	                   mcp/db and the reindex_db tool is registered
//
// A pre-built index is picked up the way any mcp.Run binary picks it up:
// from <corpus>/.ken/index.bin.
package main

import (
	"context"
	"log"
	"os"

	// The markdown and treesitter chunkers are deliberately not linked: this
	// binary stands for a third-party mcp.Run program that imports only mcp.
	"github.com/townsendmerino/ken/mcp"
	mcpdb "github.com/townsendmerino/ken/mcp/db"
)

func main() {
	log.SetOutput(os.Stderr) // stdout is the JSON-RPC channel

	corpus := os.Getenv("KEN_RUNBIN_CORPUS")
	if corpus == "" {
		log.Fatal("runbin: KEN_RUNBIN_CORPUS is required")
	}
	mode := os.Getenv("KEN_RUNBIN_MODE")
	if mode == "" {
		mode = "bm25"
	}
	ctx := context.Background()

	var dbi mcp.DBIntegration
	if dsn := os.Getenv("KEN_RUNBIN_DSN"); dsn != "" {
		refresher, err := mcpdb.Setup(ctx, mcpdb.Config{DSN: dsn})
		if err != nil {
			log.Fatalf("runbin: mcpdb.Setup: %v", err)
		}
		dbi = refresher
	}

	err := mcp.Run(ctx, os.DirFS(corpus), mcp.Options{
		Mode:        mode,
		ChunkerName: "regex",
		ModelDir:    os.Getenv("KEN_RUNBIN_MODEL"),
		LogLevel:    "error",
		DB:          dbi,
	})
	if err != nil {
		log.Fatalf("runbin: mcp.Run: %v", err)
	}
}
