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

// DefaultTopK is the default value for top_k across both tools
// (matches semble's `top_k: int = 5`; the Stage-5 prompt's "default
// 10" was a reconstruction).
const DefaultTopK = 5

// SearchArgs is the argument schema for the `search` tool. The Query /
// Repo / TopK fields and their jsonschema descriptions mirror
// semble/mcp.py verbatim so the wire schema matches across
// implementations. Mode is a ken-side extension (semble's MCP search
// has no equivalent) — see Mode's jsonschema and runSearch for
// per-call override semantics.
type SearchArgs struct {
	Query string `json:"query" jsonschema:"Natural language or code query."`
	Repo  string `json:"repo,omitempty" jsonschema:"https:// or http:// git URL (e.g. https://github.com/org/repo) or local directory path to index and search. Required when no default index was configured at startup. The index is cached after the first call, so repeat queries are fast."`
	Mode  string `json:"mode,omitempty" jsonschema:"Optional per-call mode override: hybrid|semantic|bm25. If omitted, uses the mode the server was started with. Requesting semantic or hybrid against a bm25-only index transparently downgrades to bm25 (the response header reports the effective mode)."`
	TopK  int    `json:"top_k,omitempty" jsonschema:"Number of results to return."`
}

// FindRelatedArgs is the argument schema for `find_related`.
type FindRelatedArgs struct {
	FilePath string `json:"file_path" jsonschema:"Path to the file as stored in the index (use file_path from a search result)."`
	Line     int    `json:"line" jsonschema:"Line number (1-indexed)."`
	Repo     string `json:"repo,omitempty" jsonschema:"https:// or http:// git URL or local directory path. Required when no default index was configured at startup."`
	TopK     int    `json:"top_k,omitempty" jsonschema:"Number of similar chunks to return."`
}

// ReindexDBArgs is the argument schema for the v0.8.0 reindex_db tool.
// Argument-free by design (ADR-020 Part 2) — operates on the
// process-configured KEN_DB_DSN. Future v0.8.x+ refinements (async
// return, per-engine selectors) can extend this struct; for v0.8.0
// it's deliberately empty so the agent invokes the tool with no
// parameters.
type ReindexDBArgs struct{}

// === Stage 8 Track 2 — exact-answer tool argument schemas ===
//
// Surface: definition, references, outline, symbols. All structural
// tools share one resolution discipline: tree-sitter-grade
// name-based lookup, NOT compiler-grade type analysis. Tool
// descriptions on each AddTool registration spell this out so
// agents calibrate ("ranked likely definitions; name-resolved,
// not type-resolved" — see cmd/ken-mcp/main.go for the registered
// strings).
//
// callers / callees are deliberately NOT included in v0 per the
// planning instance's Stage 8 review: Track 1's negative result
// showed callers floods the label, but DID NOT separately validate
// call-edge precision (resolving "X calls Y" across files and
// name collisions is the tree-sitter-grade-≠-compiler-grade hard
// part). callers/callees ship only after a sampled-correctness
// check on ~50 hand-verified call edges publishes a precision
// number. references is honest because it makes no resolution
// claim — "here are the places this name appears" — so it ships now.

// DefinitionArgs is the argument schema for the `definition` tool.
// Returns the file(s) where a top-level symbol (function or class)
// is defined. For name collisions (same symbol defined in multiple
// files), all sites are returned in ranked order; the tool
// description tells the agent the ranking is alphabetical-by-path
// (not confidence-weighted) for stage-1 honesty.
type DefinitionArgs struct {
	Symbol string `json:"symbol" jsonschema:"The symbol name to locate (function or class name as written in source — exact match; no fuzzy matching, no qualification)."`
	Repo   string `json:"repo,omitempty" jsonschema:"https:// or http:// git URL or local directory path. Required when no default index was configured at startup."`
}

// ReferencesArgs is the argument schema for the `references` tool.
// Returns every file where a name appears in a recognized syntactic
// context (call site, import, raise). Tree-sitter-grade: same name
// in different files collapses into one result list; no type
// resolution.
type ReferencesArgs struct {
	Symbol string `json:"symbol" jsonschema:"The name to find references for (function, class, or any identifier — exact match)."`
	Repo   string `json:"repo,omitempty" jsonschema:"https:// or http:// git URL or local directory path. Required when no default index was configured at startup."`
}

// OutlineArgs is the argument schema for the `outline` tool.
// Returns the structural outline of a file (top-level functions,
// classes, and their methods). For directory paths, returns the
// outline for every indexed file under the directory.
type OutlineArgs struct {
	Path string `json:"path" jsonschema:"File path (relative to repo root) or directory path. A file returns just that file's outline; a directory returns outlines for every indexed file under it."`
	Repo string `json:"repo,omitempty" jsonschema:"https:// or http:// git URL or local directory path. Required when no default index was configured at startup."`
}

// SymbolsArgs is the argument schema for the `symbols` tool.
// Returns every top-level symbol (function or class) defined in
// the repo, optionally filtered to a subdirectory prefix.
type SymbolsArgs struct {
	Path string `json:"path,omitempty" jsonschema:"Optional path prefix (relative to repo root) to filter the symbol list. Empty/omitted returns every top-level symbol in the repo."`
	Repo string `json:"repo,omitempty" jsonschema:"https:// or http:// git URL or local directory path. Required when no default index was configured at startup."`
}

// FormatResults mirrors semble utils._format_results: a header, then each
// result as a numbered, fenced code block with score=X.XXX. Returning a
// preformatted string keeps wire compatibility with semble — agents see
// the same text they're already trained against in semble-using prompts.
//
// Exported because bench/tokens/ measures token budgets against this
// exact wire format (not the in-memory chunk text), so it has to call
// the same formatter ken-mcp emits over the JSON-RPC channel.
func FormatResults(header string, results []search.Result) string {
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
