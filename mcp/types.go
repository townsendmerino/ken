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

	// === 1.0 filters (over-fetch + post-filter; no ranking-quality change) ===
	//
	// All three filters are AND-ed: a result must pass every set
	// constraint to be returned. Filters apply AFTER the hybrid
	// retriever runs; we over-fetch by a generous multiplier so the
	// post-filter top-K still has good candidates. If the filter set
	// removes every candidate, the response says so honestly rather
	// than silently returning fewer than top_k.
	Languages           []string `json:"languages,omitempty" jsonschema:"Optional list of file extensions to include (e.g. ['py','go','ts']). Leading dot optional ('py' and '.py' both work). When set, only results from files with these extensions are returned. Use this to narrow a polyglot repo to one language."`
	PathContains        string   `json:"path_contains,omitempty" jsonschema:"Optional substring that must appear in the result's file path (case-sensitive). E.g. 'src/api' returns only results under directories whose path contains src/api. Substring match, not glob — for glob patterns use a more specific path_contains value."`
	ExcludePathContains string   `json:"exclude_path_contains,omitempty" jsonschema:"Optional substring that must NOT appear in the file path. E.g. '_test.go' excludes Go test files; 'node_modules' excludes vendored JS. Substring match."`
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

// RecentlyChangedArgs is the argument schema for the
// `recently_changed` tool. Returns the last N commits with the
// files they touched, sourced from the repo's git history (go-git
// PlainOpen on the local working tree).
//
// Pass 1 supports LOCAL REPO PATHS ONLY. URL repos (https://...)
// are cloned into the cache's temp dir but ken doesn't expose that
// path; calling recently_changed with a URL returns a helpful
// error. Pass 2 can plumb the local clone path through when there's
// demand.
type RecentlyChangedArgs struct {
	N    int    `json:"n,omitempty" jsonschema:"Number of recent commits to include (default 10, max 100)."`
	Repo string `json:"repo,omitempty" jsonschema:"Local directory path containing a git working tree. Required when no default index was configured at startup. Pass 1 of this tool does NOT support https:// URLs — for those, clone manually first."`
	Path string `json:"path,omitempty" jsonschema:"Optional path prefix to filter the file list. Commits with no matching file changes are skipped from the output. E.g. 'src/api' returns only commits that touched something under src/api/."`
}

// StatusArgs is the argument schema for the `status` tool. All
// fields optional; without a repo it reports machine-level state
// (models, enrichment env, savings) plus the server's cache state.
// With repo set, it ALSO populates live index + structural fields
// for that repo (resolving via the same cache path the search
// tools use).
type StatusArgs struct {
	Repo    string `json:"repo,omitempty" jsonschema:"Optional repo to also report live index state for. If set, the response includes file count, chunk count, mode, chunker, and structural-index symbol counts for that repo (resolved via the same cache the search tools use). If omitted, machine-level state only."`
	Verbose bool   `json:"verbose,omitempty" jsonschema:"Include per-language extractor coverage + per-call-type counts in the response."`
	Output  string `json:"output,omitempty" jsonschema:"Optional output format: 'markdown' (default; human-readable) or 'json' (machine-parsable). Agents that want to programmatically inspect status should pass 'json'."`
}

// CallersArgs is the argument schema for the `callers` tool. Returns
// the list of FILES that contain a call to the named function.
// File-level granularity is what the structural index actually keys
// on (Stage 8 Gate 2 sampled 400 edges across 8 languages and
// confirmed 100% precision — every reported edge resolves to a real
// call in the file). Function-level granularity ("function A calls
// function B") would need a richer index; deferred.
type CallersArgs struct {
	Symbol string `json:"symbol" jsonschema:"The function name whose callers you want (exact match, no qualification — pass 'Login' not 'User.Login'). Returns files that contain a call to this name."`
	Repo   string `json:"repo,omitempty" jsonschema:"https:// or http:// git URL or local directory path. Required when no default index was configured at startup."`
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
