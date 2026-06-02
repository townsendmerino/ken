package mcp

// Stage 8 Track 2 — exact-answer structural tool handlers.
//
// Four tools sit on top of internal/structural.Index:
//
//   - definition(symbol)     → file(s) defining the symbol
//   - references(symbol)     → file(s) referencing the symbol
//                              (calls + imports + raises)
//   - outline(path)          → structural outline of a file or dir
//   - symbols(path)          → every top-level symbol in a path
//
// All return a preformatted markdown string for wire compatibility
// with semble's existing tool surface (FormatResults shape). Tool
// resolution is name-based, tree-sitter-grade — see each tool's
// AddTool description for the honest framing the agent reads.
//
// Lifecycle: each handler resolves the cached RepoBundle via
// Cache.GetBundle (eager structural build per Stage 8 design).
// Bundle.Structural may be nil for repos whose corpus the
// extractor doesn't support (no Python files, or Stage 8 v0 only
// indexes Python). Handlers degrade to a clear "no structural
// index available" text response.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/townsendmerino/ken/internal/structural"
)

// resolveBundleForTool wraps Cache.GetBundle with the same repo-
// argument resolution the search and find_related handlers do:
// args.Repo overrides the configured DefaultRepo; an empty repo
// with no default falls back to a clear "configure repo" error.
// Returns the resolved RepoBundle, or an MCP-shaped error result.
func resolveBundleForTool(ctx context.Context, cfg *Config, repoArg string) (*RepoBundle, *sdk.CallToolResult, error) {
	repo := repoArg
	if repo == "" {
		repo = cfg.DefaultRepo
	}
	if repo == "" {
		return nil, textResult(
			"No repo specified and no default repo configured. " +
				"Pass `repo` (a git URL or local directory path) or " +
				"set KEN_MCP_DEFAULT_REPO at server startup.",
		), nil
	}
	if cfg.Cache == nil {
		return nil, textResult("server cache unavailable; cannot resolve repo"), nil
	}
	bundle, err := cfg.Cache.GetBundle(ctx, repo)
	if err != nil {
		return nil, textResult(fmt.Sprintf("failed to index %s: %v", repo, err)), nil
	}
	return bundle, nil, nil
}

// handleDefinition implements the `definition` tool: given a
// symbol name, return the file(s) where it's defined. Tree-sitter-
// grade: collisions return all sites (ordered alphabetically by
// file path); ambiguity is NOT resolved by type.
func handleDefinition(ctx context.Context, cfg *Config, args DefinitionArgs) (*sdk.CallToolResult, any, error) {
	bundle, errRes, _ := resolveBundleForTool(ctx, cfg, args.Repo)
	if errRes != nil {
		return errRes, nil, nil
	}
	if bundle.Structural == nil {
		return textResult(
			"No structural index available for this repo. " +
				"The Stage 8 extractor currently covers Python only; " +
				"repos with no .py files have no structural index.",
		), nil, nil
	}
	sym := strings.TrimSpace(args.Symbol)
	if sym == "" {
		return textResult("symbol is required"), nil, nil
	}

	sites := bundle.Structural.Definition(sym)
	if len(sites) == 0 {
		return textResult(fmt.Sprintf("No definition found for %q.", sym)), nil, nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# Definition: `%s`\n\n", sym)
	if len(sites) > 1 {
		fmt.Fprintf(&b, "_%d sites — name resolved by identifier, not type. Ambiguous; results ordered alphabetically by file path. Method sites carry their qualified `Type.method` form in parentheses._\n\n", len(sites))
	}
	for i, s := range sites {
		switch {
		case s.Kind == structural.DefinitionKindMethod && s.QName != "" && s.QName != sym:
			fmt.Fprintf(&b, "%d. **%s** — method (%s)\n", i+1, s.File, s.QName)
		default:
			fmt.Fprintf(&b, "%d. **%s** — %s\n", i+1, s.File, kindLabel(s.Kind))
		}
	}
	return textResult(strings.TrimRight(b.String(), "\n")), nil, nil
}

// handleReferences implements the `references` tool: given a name,
// return every file where it appears in a recognized syntactic
// context (call site, import, or raise statement in Stage 8 v0).
//
// Honest framing in the response header: this is "all places the
// name appears in these specific contexts," not "all places that
// are semantically the same definition's references." A function
// named `parse` and a class field named `parse` in a different
// file both show up; tooling that needs semantic precision should
// use an LSP, not ken's structural index.
func handleReferences(ctx context.Context, cfg *Config, args ReferencesArgs) (*sdk.CallToolResult, any, error) {
	bundle, errRes, _ := resolveBundleForTool(ctx, cfg, args.Repo)
	if errRes != nil {
		return errRes, nil, nil
	}
	if bundle.Structural == nil {
		return textResult(
			"No structural index available for this repo. " +
				"The Stage 8 extractor currently covers Python only.",
		), nil, nil
	}
	sym := strings.TrimSpace(args.Symbol)
	if sym == "" {
		return textResult("symbol is required"), nil, nil
	}

	refs := bundle.Structural.References(sym)
	if len(refs) == 0 {
		return textResult(fmt.Sprintf("No references found for %q.", sym)), nil, nil
	}

	// Group by file so the agent reads "this file uses it in these
	// contexts" rather than a flat list of (file, kind) pairs.
	byFile := make(map[string][]structural.ReferenceKind)
	order := make([]string, 0)
	for _, r := range refs {
		if _, ok := byFile[r.File]; !ok {
			order = append(order, r.File)
		}
		byFile[r.File] = append(byFile[r.File], r.Kind)
	}
	sort.Strings(order)

	var b strings.Builder
	fmt.Fprintf(&b, "# References: `%s`\n\n", sym)
	fmt.Fprintf(&b, "_%d reference%s across %d file%s. Name-resolved, not type-resolved — same-spelled identifiers in different contexts collapse into one list._\n\n",
		len(refs), pluralS(len(refs)), len(order), pluralS(len(order)))
	for i, f := range order {
		kinds := dedupRefKinds(byFile[f])
		fmt.Fprintf(&b, "%d. **%s** — %s\n", i+1, f, strings.Join(kinds, ", "))
	}
	return textResult(strings.TrimRight(b.String(), "\n")), nil, nil
}

// handleOutline implements the `outline` tool: given a file path,
// return the file's structural outline (top-level functions,
// classes, methods). Given a directory path, return the outline of
// every indexed file under that directory.
func handleOutline(ctx context.Context, cfg *Config, args OutlineArgs) (*sdk.CallToolResult, any, error) {
	bundle, errRes, _ := resolveBundleForTool(ctx, cfg, args.Repo)
	if errRes != nil {
		return errRes, nil, nil
	}
	if bundle.Structural == nil {
		return textResult(
			"No structural index available for this repo. " +
				"The Stage 8 extractor currently covers Python only.",
		), nil, nil
	}
	rawPath := strings.TrimSpace(args.Path)
	if rawPath == "" {
		return textResult("path is required (a file path or directory path relative to the repo root)"), nil, nil
	}
	path := structural.NormalizePath(rawPath)

	// First, try as a single file: if the index has an entry for
	// this exact path, render its outline directly.
	if fs := bundle.Structural.File(path); fs != nil {
		return textResult(formatOutline(path, bundle.Structural.Outline(path))), nil, nil
	}

	// Otherwise treat as a directory prefix and walk every
	// indexed file under it. Returns empty if the prefix matches
	// nothing.
	files := bundle.Structural.FilesUnderPath(path)
	if len(files) == 0 {
		return textResult(fmt.Sprintf("No indexed files found at or under %q. "+
			"Possible reasons: the path doesn't exist, the files aren't a supported "+
			"language (Stage 8 v0 = Python only), or the corpus excluded them.", path)), nil, nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# Outline: `%s` (%d file%s)\n\n",
		path, len(files), pluralS(len(files)))
	for _, f := range files {
		entries := bundle.Structural.Outline(f)
		if len(entries) == 0 {
			continue
		}
		b.WriteString("## ")
		b.WriteString(f)
		b.WriteString("\n\n")
		b.WriteString(formatOutlineEntries(entries))
		b.WriteString("\n")
	}
	return textResult(strings.TrimRight(b.String(), "\n")), nil, nil
}

// handleSymbols implements the `symbols` tool: given an optional
// path prefix, return every top-level symbol (function or class)
// defined under it. Useful as a "what's in this repo?" or "what's
// in this package?" overview.
func handleSymbols(ctx context.Context, cfg *Config, args SymbolsArgs) (*sdk.CallToolResult, any, error) {
	bundle, errRes, _ := resolveBundleForTool(ctx, cfg, args.Repo)
	if errRes != nil {
		return errRes, nil, nil
	}
	if bundle.Structural == nil {
		return textResult(
			"No structural index available for this repo. " +
				"The Stage 8 extractor currently covers Python only.",
		), nil, nil
	}
	path := strings.TrimSpace(args.Path)
	var names []string
	if path == "" || path == "." {
		names = bundle.Structural.Symbols()
	} else {
		names = bundle.Structural.SymbolsInPath(structural.NormalizePath(path))
	}
	if len(names) == 0 {
		if path == "" {
			return textResult("No symbols indexed. " +
				"The Stage 8 extractor currently covers Python only; " +
				"repos with no .py files produce an empty symbol list."), nil, nil
		}
		return textResult(fmt.Sprintf("No symbols found at or under %q.", path)), nil, nil
	}

	var b strings.Builder
	if path == "" {
		fmt.Fprintf(&b, "# Symbols (%d total)\n\n", len(names))
	} else {
		fmt.Fprintf(&b, "# Symbols under `%s` (%d total)\n\n", path, len(names))
	}
	for _, n := range names {
		fmt.Fprintf(&b, "- `%s`\n", n)
	}
	return textResult(strings.TrimRight(b.String(), "\n")), nil, nil
}

// === formatting helpers ===

func formatOutline(path string, entries []structural.OutlineEntry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Outline: `%s`\n\n", path)
	if len(entries) == 0 {
		b.WriteString("_No symbols extracted._")
		return b.String()
	}
	b.WriteString(formatOutlineEntries(entries))
	return strings.TrimRight(b.String(), "\n")
}

func formatOutlineEntries(entries []structural.OutlineEntry) string {
	var b strings.Builder
	for _, e := range entries {
		params := ""
		if len(e.Params) > 0 {
			params = "(" + strings.Join(e.Params, ", ") + ")"
		}
		switch {
		case e.Container != "":
			fmt.Fprintf(&b, "  - **%s.%s**%s\n", e.Container, e.Name, params)
		case e.Kind == structural.DefinitionKindClass:
			fmt.Fprintf(&b, "- class **%s**\n", e.Name)
		default:
			fmt.Fprintf(&b, "- func **%s**%s\n", e.Name, params)
		}
	}
	return b.String()
}

func kindLabel(k structural.DefinitionKind) string {
	switch k {
	case structural.DefinitionKindFunction:
		return "function"
	case structural.DefinitionKindClass:
		return "class"
	case structural.DefinitionKindMethod:
		return "method"
	default:
		return "definition"
	}
}

func dedupRefKinds(kinds []structural.ReferenceKind) []string {
	seen := make(map[structural.ReferenceKind]struct{}, len(kinds))
	var out []string
	for _, k := range kinds {
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, refKindLabel(k))
	}
	return out
}

func refKindLabel(k structural.ReferenceKind) string {
	switch k {
	case structural.ReferenceKindCall:
		return "call"
	case structural.ReferenceKindImport:
		return "import"
	case structural.ReferenceKindRaise:
		return "raise"
	case structural.ReferenceKindAnnotation:
		return "annotation"
	case structural.ReferenceKindName:
		return "name"
	default:
		return "reference"
	}
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
