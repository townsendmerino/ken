// Command ken-mcp is ken's Model Context Protocol server. Agents
// (Claude Code / Cursor / Codex / OpenCode / VS Code / GitHub Copilot
// CLI) speak JSON-RPC to it over stdio; tools and arg shapes match
// semble's MCP server so this binary is a drop-in replacement.
//
// main only chooses what the binary links and calls into
// cmd/ken-mcp/internal/server, which reads the environment (LoadConfig)
// and assembles and runs the server (Run).
//
// ── stdout/stderr contract ──────────────────────────────────────────
// stdin and stdout ARE the JSON-RPC channel. ANYTHING written to stdout
// outside of the SDK's protocol writer corrupts the JSON stream and
// agents disconnect with a cryptic JSON-decode error. This is the #1
// way new MCP servers fail — every team rediscovers it. Therefore:
//
//   - All logging is forced to os.Stderr (including the stdlib's
//     default `log` logger, which some third-party libraries write to).
//   - We never call fmt.Println / fmt.Printf to stdout. There are no
//     such calls in this binary.
//   - go-git is configured without a Progress writer (would otherwise
//     write progress lines to stdout in some versions).
//
// If you add a dependency, audit it for default writers pointed at
// stdout and redirect them at startup (see init() below).
package main

import (
	"log"
	"os"

	"github.com/townsendmerino/aikit/chunk"
	// Side-effect imports: register every chunker the stock binary
	// offers. internal/search blank-imports "regex" (the default), so
	// we only need the optional chunkers here. The treesitter import
	// inflates the linked binary by ~26 MB (darwin/arm64; the
	// gotreesitter/grammars embed.FS payload is ~19 MB on-disk for
	// 206 grammar blobs, plus the parser runtime + symbol overhead)
	// — desired for the code-search use case but explicitly skipped
	// by cmd/ken-mcp-docs. Per ADR-023 the bundle is monolithic at
	// the embed layer so per-language gating doesn't shrink it.
	_ "github.com/townsendmerino/aikit/chunk/markdown"
	"github.com/townsendmerino/aikit/chunk/treesitter"

	"github.com/townsendmerino/ken/cmd/ken-mcp/internal/server"
)

func init() {
	// Belt + suspenders: any third-party that calls log.Print at import
	// time would otherwise hit stdout. Redirect before main runs.
	log.SetOutput(os.Stderr)

	// Register the treesitter chunker with a bounded 1s per-parse wall-clock
	// timeout — the aikit default, made explicit here to document the tier
	// choice (#35). The live multi-repo server keeps the per-parse latency
	// guard so a single pathological file can't stall a watch flush; it does
	// NOT need the byte-identical-rebuild guarantee the `ken` CLI opts into
	// (which disables this timeout for reproducible `build-index` output).
	chunk.Register("treesitter", treesitter.New(treesitter.WithParseTimeoutMicros(1_000_000)))
}

func main() {
	if server.RunSubcommand(os.Args) {
		return
	}
	logger := server.NewLogger()

	// M2 (memory campaign): apply the long-lived-server GC policy — a lower
	// default GOGC and an optional KEN_MEMLIMIT soft cap. Server-only; the
	// library/CLI layers stay untuned. Both defer to explicit GOGC/GOMEMLIMIT.
	server.SetupGCHygiene(logger)

	// Per-file tree-sitter parse budget (cold-start M2 / Task 2). Server
	// default 500 ms so one pathological template-like file can't stall the
	// index build; disabled by default in the library to keep the build/golden
	// path deterministic. Must run before the first index build.
	server.SetupEnrichBudget(logger)

	server.Run(server.LoadConfig(logger), logger)
}
