// Package mcp wires ken's internal search package to the
// Model Context Protocol via the modelcontextprotocol/go-sdk. It exposes
// the same two tools semble's Python MCP server does — `search` and
// `find_related` — with matching argument shapes and the same
// formatted-string output so any MCP-compatible agent (Claude Code,
// Cursor, Codex, OpenCode, VS Code, GitHub Copilot CLI) can use ken-mcp
// as a drop-in replacement for semble's MCP server.
//
// The package name is "mcp" to match docs/DESIGN.md §1's layout; the SDK's own
// "mcp" package is imported as `sdk` everywhere in this package to keep
// the two unambiguous.
//
// Output format and arg schemas are ports of /tmp/semble/src/semble/mcp.py
// and utils._format_results, verified against the live source.
package mcp

import (
	"fmt"
	"strings"

	"github.com/townsendmerino/ken/internal/search"
)

// Default value for top_k across both tools (matches semble's
// `top_k: int = 5`; the Stage-5 prompt's "default 10" was a reconstruction).
const DefaultTopK = 5

// SearchArgs is the argument schema for the `search` tool. JSON tag names
// and the jsonschema descriptions are deliberately verbatim of
// semble/mcp.py so the wire schema matches across implementations.
type SearchArgs struct {
	Query string `json:"query" jsonschema:"Natural language or code query."`
	Repo  string `json:"repo,omitempty" jsonschema:"https:// or http:// git URL (e.g. https://github.com/org/repo) or local directory path to index and search. Required when no default index was configured at startup. The index is cached after the first call, so repeat queries are fast."`
	Mode  string `json:"mode,omitempty" jsonschema:"Search mode: hybrid|semantic|bm25. 'hybrid' is best for most queries."`
	TopK  int    `json:"top_k,omitempty" jsonschema:"Number of results to return."`
}

// FindRelatedArgs is the argument schema for `find_related`.
type FindRelatedArgs struct {
	FilePath string `json:"file_path" jsonschema:"Path to the file as stored in the index (use file_path from a search result)."`
	Line     int    `json:"line" jsonschema:"Line number (1-indexed)."`
	Repo     string `json:"repo,omitempty" jsonschema:"https:// or http:// git URL or local directory path. Required when no default index was configured at startup."`
	TopK     int    `json:"top_k,omitempty" jsonschema:"Number of similar chunks to return."`
}

// formatResults mirrors semble utils._format_results: a header, then each
// result as a numbered, fenced code block with score=X.XXX. Returning a
// preformatted string keeps wire compatibility with semble — agents see
// the same text they're already trained against in semble-using prompts.
func formatResults(header string, results []search.Result) string {
	var b strings.Builder
	b.WriteString(header)
	b.WriteString("\n\n")
	for i, r := range results {
		fmt.Fprintf(&b, "## %d. %s:%d-%d  [score=%.3f]\n",
			i+1, r.Chunk.File, r.Chunk.StartLine, r.Chunk.EndLine, r.Score)
		b.WriteString("```\n")
		b.WriteString(strings.TrimSpace(r.Chunk.Text))
		b.WriteString("\n```\n\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
