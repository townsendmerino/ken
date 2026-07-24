package mcp

import (
	"encoding/json"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// JSON output mode (Pass 1.0 — applies to `search`, `find_related`,
// `definition`, `references`, `callers`, `outline`, `symbols`).
//
// Each tool's Args struct carries an optional `Output` string
// (jsonschema enum-ish: "markdown" default, "json" opt-in). Handlers
// build a typed response struct and call dispatchOutput, which
// either marshals the struct to JSON or invokes the per-tool
// markdown renderer. Two outputs, one source of truth — the
// markdown rendering pulls from the SAME struct the JSON
// serializer sees, so the two surfaces can't drift.
//
// The response struct fields are the 1.0-stable surface for any
// agent that wants to parse ken's output. Adding fields is safe;
// renaming or removing fields is a breaking change.

// SearchResponse is the `search` tool's JSON response. Wraps the
// query echo, the effective retrieval mode (after capability
// downgrade), the filter block (when filters were applied), and
// the per-result list.
type SearchResponse struct {
	Query   string            `json:"query"`
	Mode    string            `json:"mode"`
	Results []SearchResultRow `json:"results"`
	// Filter is non-nil ONLY when one of languages /
	// path_contains / exclude_path_contains was set. Lets agents
	// see when a filter was the limiting factor — same signal the
	// markdown header carries.
	Filter *SearchFilterMeta `json:"filter,omitempty"`
}

// SearchResultRow is one ranked hit.
type SearchResultRow struct {
	Rank      int     `json:"rank"`
	File      string  `json:"file"`
	StartLine int     `json:"start_line"`
	EndLine   int     `json:"end_line"`
	Score     float64 `json:"score"`
	Text      string  `json:"text"`
}

// SearchFilterMeta documents how a filter affected the result count.
type SearchFilterMeta struct {
	Languages              []string `json:"languages,omitempty"`
	PathContains           string   `json:"path_contains,omitempty"`
	ExcludePathContains    string   `json:"exclude_path_contains,omitempty"`
	CandidatesBeforeFilter int      `json:"candidates_before_filter"`
	ResultsAfterFilter     int      `json:"results_after_filter"`
}

// FindRelatedResponse mirrors SearchResponse without the filter +
// query echo (find_related has no query string and no filters in
// Pass 1).
type FindRelatedResponse struct {
	Anchor  FindRelatedAnchor `json:"anchor"`
	Results []SearchResultRow `json:"results"`
}

// FindRelatedAnchor echoes the (file, line) the agent used to seed
// the find_related query.
type FindRelatedAnchor struct {
	File string `json:"file"`
	Line int    `json:"line"`
}

// DefinitionResponse lists every site the structural index has for
// the queried symbol.
type DefinitionResponse struct {
	Symbol      string             `json:"symbol"`
	Definitions []DefinitionRowOut `json:"definitions"`
}

// DefinitionRowOut is the JSON-stable shape of a definition site.
// Kind is the human label ("function" / "class" / "method").
// QName is the qualified name for method sites (e.g. "User.Login")
// and empty for top-level definitions.
type DefinitionRowOut struct {
	File  string `json:"file"`
	Kind  string `json:"kind"`
	QName string `json:"qname,omitempty"`
}

// ReferencesResponse lists every file where the symbol appears in a
// recognized syntactic context.
type ReferencesResponse struct {
	Symbol     string             `json:"symbol"`
	References []ReferenceRowOut  `json:"references"`
	Totals     ReferencesRowTotal `json:"totals"`
}

// ReferenceRowOut is one (file, kinds) entry. Kinds is the set of
// reference categories observed in that file — currently "call" /
// "import" / "raise" (per-language extractors normalize their
// language-native references into these three; the renderer
// dedupes within a file).
type ReferenceRowOut struct {
	File  string   `json:"file"`
	Kinds []string `json:"kinds"`
}

// ReferencesRowTotal is the (#references, #files) summary the
// markdown header surfaces; included in JSON so agents don't have
// to count.
type ReferencesRowTotal struct {
	References int `json:"references"`
	Files      int `json:"files"`
}

// CallersResponse is the `callers` tool's JSON shape. File-level
// granularity matching the Stage 8 Gate 2 precision-validated
// surface.
type CallersResponse struct {
	Symbol string   `json:"symbol"`
	Files  []string `json:"files"`
}

// OutlineResponse is the `outline` tool's JSON shape. Path is the
// path the agent asked for (normalized); Entries are the
// structural OutlineEntry rows per file.
type OutlineResponse struct {
	Path    string             `json:"path"`
	Entries []OutlineEntryOut  `json:"entries"`
	ByFile  []OutlineFileEntry `json:"by_file,omitempty"`
}

// OutlineEntryOut is one item in a file's outline.
type OutlineEntryOut struct {
	File      string   `json:"file"`
	Name      string   `json:"name"`
	Kind      string   `json:"kind"` // "function" / "class" / "method"
	Container string   `json:"container,omitempty"`
	Params    []string `json:"params,omitempty"`
}

// OutlineFileEntry groups outline entries when the agent passed a
// directory path. Entries-flat is also returned for callers that
// want a single iteration.
type OutlineFileEntry struct {
	File    string            `json:"file"`
	Entries []OutlineEntryOut `json:"entries"`
}

// SymbolsResponse lists every top-level symbol matching the optional
// path filter.
type SymbolsResponse struct {
	PathPrefix string   `json:"path_prefix,omitempty"`
	Symbols    []string `json:"symbols"`
}

// RecentlyChangedResponse is the `recently_changed` tool's JSON shape.
// Considered is how many commits were walked (≥ len(Commits) when a path
// filter dropped some); PathPrefix echoes the filter if one was passed.
type RecentlyChangedResponse struct {
	PathPrefix string                  `json:"path_prefix,omitempty"`
	Considered int                     `json:"considered"`
	Commits    []RecentlyChangedCommit `json:"commits"`
}

// RecentlyChangedCommit is one commit in the recently_changed list.
// When is RFC3339; ChangedFiles is sorted and (if PathPrefix was set)
// filtered to that prefix.
type RecentlyChangedCommit struct {
	Hash         string   `json:"hash"`
	ShortHash    string   `json:"short_hash"`
	Subject      string   `json:"subject"`
	AuthorName   string   `json:"author_name"`
	AuthorEmail  string   `json:"author_email"`
	When         string   `json:"when"`
	ChangedFiles []string `json:"changed_files"`
}

// dispatchOutput routes a handler result to either the JSON
// serialization of `resp` or the agent-provided `markdown` string.
// Empty/unspecified Output defaults to markdown.
//
// outputMode is the args.Output value verbatim; valid values are
// "" (default → markdown) and "json". Any other value is rejected
// with a clear error rather than silently treated as markdown — we'd
// rather an agent that mis-spells "jsom" sees the typo than gets
// the wrong format.
// errorResult renders an error / edge-case message honoring the requested
// output mode, so a json-mode agent that json.Parses every tool result
// doesn't break on an error path (code review §4). json → {"error": msg};
// markdown / empty / unknown → the plain message. Reuses dispatchOutput so
// the mode handling stays in one place.
func errorResult(outputMode, msg string) *sdk.CallToolResult {
	res, _, _ := dispatchOutput(outputMode, map[string]string{"error": msg}, msg)
	return res
}

func dispatchOutput(outputMode string, resp any, markdown string) (*sdk.CallToolResult, any, error) {
	switch outputMode {
	case "", "markdown":
		return textResult(markdown), nil, nil
	case "json":
		j, err := json.MarshalIndent(resp, "", "  ")
		if err != nil {
			return textResult("error marshaling JSON response: " + err.Error()), nil, nil
		}
		return textResult(string(j)), nil, nil
	default:
		return textResult(
			"unknown output mode " + outputMode + " — supported: 'markdown' (default) or 'json'",
		), nil, nil
	}
}
