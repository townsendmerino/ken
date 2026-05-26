// Command embedded-with-db is the v0.8.0 Part 3 test mini-binary
// exercising the SDK-author code path: mcp.Run + mcp/db.Setup wired
// together over the SDK's stdio transport. The integration tests in
// mcp/db (run_integration_test.go) build + spawn this binary to
// verify the protocol round-trip end-to-end.
//
// Reads the SDK author's DSN from MY_DB_DSN env var — matching the
// canonical example from the v0.8.0 Part 3 README. Empty MY_DB_DSN
// triggers the "no DB" branch: mcp/db.Setup returns nil-nil-nil, the
// resulting opts.Reindex stays nil, and mcp.Run registers only the
// search + find_related tools (the v0.6.0 docs-only behavior).
//
// IMPORTANT: this binary is also subject to the stdio JSON-RPC
// contract — writes to stdout (outside the SDK's protocol writer)
// corrupt the channel. All logging goes to stderr via mcp.Run's
// LogWriter default. The seventh stdout-cleanliness test
// (TestBinary_StdoutIsCleanJSONRPC_EmbeddedWithDB in
// run_integration_test.go) audits this contract for the mcp.Run +
// mcp/db.Setup path.
package main

import (
	"context"
	"io/fs"
	"log"
	"os"
	"testing/fstest"

	"github.com/townsendmerino/ken/mcp"
	mcpdb "github.com/townsendmerino/ken/mcp/db"
)

func main() {
	// Belt + suspenders: redirect any stray stdlib log calls to stderr
	// before importing third-party packages that might initialize loggers.
	log.SetOutput(os.Stderr)

	ctx := context.Background()

	// SDK author's opt-in: only construct DB wiring if MY_DB_DSN is set.
	// This mirrors the canonical README example for v0.8.0 Part 3 (with
	// the Part 3 addendum's DBIntegration shape: pass the *Refresher
	// directly as mcp.Options.DB; mcp.Run calls Refresher.Start
	// internally with the chunk-integration callback).
	var dbi mcp.DBIntegration
	if dsn := os.Getenv("MY_DB_DSN"); dsn != "" {
		refresher, err := mcpdb.Setup(ctx, mcpdb.Config{
			DSN: dsn,
		})
		if err != nil {
			log.Fatalf("mcpdb.Setup: %v", err)
		}
		dbi = refresher // *Refresher satisfies mcp.DBIntegration
	}

	corpus := embeddedCorpus()
	opts := mcp.Options{
		Mode:        "bm25",
		ChunkerName: "regex",
		LogLevel:    "warn",
		DB:          dbi, // nil when MY_DB_DSN is unset → reindex_db NOT registered
	}
	if err := mcp.Run(ctx, corpus, opts); err != nil {
		log.Fatalf("mcp.Run: %v", err)
	}
}

// embeddedCorpus is a tiny in-memory fs.FS for the test binary. Real
// SDK authors typically use //go:embed to bake docs into the binary;
// for this test we just construct an MapFS so the binary doesn't need
// any non-Go assets.
func embeddedCorpus() fs.FS {
	return fstest.MapFS{
		"docs.md": &fstest.MapFile{Data: []byte(`# Embedded Demo Corpus

This binary exercises mcp.Run + mcp/db.Setup for v0.8.0 Part 3
integration tests. ValidateUser is a marker the test asserts on.
`)},
	}
}
