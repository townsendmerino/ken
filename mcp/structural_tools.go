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
// Cache.GetBundle, then calls bundle.StructuralIndex(ctx), which builds
// the structural symbol index lazily on first use (cold-start M0).
// It may return nil for repos whose corpus has no files
// matching a registered extractor — the structural index covers
// ten languages (Python, Go, TypeScript, JavaScript, Java, Rust, C,
// C++, PHP, Ruby); files outside this set are silently skipped at
// Build time. Handlers degrade to a clear "no structural index
// available" text response.

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
// Returns the resolved RepoBundle, or a non-nil MCP-shaped error result
// (the caller returns it directly). No error return — the failure IS the
// *CallToolResult (code review §5).
func resolveBundleForTool(ctx context.Context, cfg *Config, repoArg, outputMode string) (*RepoBundle, *sdk.CallToolResult) {
	// Reuse resolveRepo (server.go) rather than reimplement the
	// explicit-arg-wins-else-default-else-error step — the two used to diverge
	// in wording so an agent got different guidance depending on which of the
	// repo-taking tools it called with no repo.
	repo, err := resolveRepo(cfg, repoArg)
	if err != nil {
		return nil, errorResult(outputMode, err.Error())
	}
	if cfg.Cache == nil {
		return nil, errorResult(outputMode, "server cache unavailable; cannot resolve repo")
	}
	bundle, err := cfg.Cache.GetBundle(ctx, repo)
	if err != nil {
		return nil, errorResult(outputMode, fmt.Sprintf("failed to index %s: %v", repo, err))
	}
	return bundle, nil
}

// structuralUnavailableResult is the error a structural handler returns when
// StructuralIndex yielded nil. It distinguishes "still building" from
// "genuinely no structural index" (audit R3) so an agent on a large repo
// retries instead of concluding the repo has no symbols.
func structuralUnavailableResult(bundle *RepoBundle, outputMode string) *sdk.CallToolResult {
	if bundle.StructuralPending() {
		return errorResult(outputMode,
			"The structural index is still building for this repo (large corpus). "+
				"Retry in a few seconds.")
	}
	return errorResult(outputMode,
		"No structural index available for this repo. "+
			"The structural index has no extractors registered for any file in this corpus "+
			"(a repo with no supported source files has no structural index).")
}

// resolveStructuralIndex is the shared preamble for every structural handler:
// resolve the repo bundle, then build/fetch its StructuralIndex, nil-checking
// both. On any failure it returns a nil index plus a ready-to-return MCP error
// result (the caller returns it as `errResult, nil, nil`); on success errResult
// is nil. Handlers use only the returned index — the bundle is consumed here.
func resolveStructuralIndex(ctx context.Context, cfg *Config, repoArg, outputMode string) (*structural.Index, *sdk.CallToolResult) {
	bundle, errResult := resolveBundleForTool(ctx, cfg, repoArg, outputMode)
	if errResult != nil {
		return nil, errResult
	}
	// Builds the structural symbol index on first use (lazy — cold-start M0);
	// nil on unsupported corpus or build failure.
	sidx := bundle.StructuralIndex(ctx)
	if sidx == nil {
		return nil, structuralUnavailableResult(bundle, outputMode)
	}
	return sidx, nil
}

// handleDefinition implements the `definition` tool: given a
// symbol name, return the file(s) where it's defined. Tree-sitter-
// grade: collisions return all sites (ordered alphabetically by
// file path); ambiguity is NOT resolved by type.
func handleDefinition(ctx context.Context, cfg *Config, args DefinitionArgs) (*sdk.CallToolResult, any, error) {
	sidx, errResult := resolveStructuralIndex(ctx, cfg, args.Repo, args.Output)
	if errResult != nil {
		return errResult, nil, nil
	}
	sym := strings.TrimSpace(args.Symbol)
	if sym == "" {
		return errorResult(args.Output, "symbol is required"), nil, nil
	}

	sites := sidx.Definition(sym)
	if len(sites) == 0 {
		resp := DefinitionResponse{Symbol: sym, Definitions: []DefinitionRowOut{}}
		return dispatchOutput(args.Output, resp, fmt.Sprintf("No definition found for %q.", sym))
	}

	resp := DefinitionResponse{Symbol: sym, Definitions: make([]DefinitionRowOut, 0, len(sites))}
	for _, s := range sites {
		qname := ""
		if s.Kind == structural.DefinitionKindMethod && s.QName != "" && s.QName != sym {
			qname = s.QName
		}
		resp.Definitions = append(resp.Definitions, DefinitionRowOut{
			File:  s.File,
			Kind:  kindLabel(s.Kind),
			QName: qname,
		})
	}
	return dispatchOutput(args.Output, resp, renderDefinitionMarkdown(resp))
}

func renderDefinitionMarkdown(r DefinitionResponse) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Definition: `%s`\n\n", r.Symbol)
	if len(r.Definitions) > 1 {
		fmt.Fprintf(&b, "_%d sites — name resolved by identifier, not type. Ambiguous; results ordered alphabetically by file path. Method sites carry their qualified `Type.method` form in parentheses._\n\n", len(r.Definitions))
	}
	for i, d := range r.Definitions {
		if d.Kind == "method" && d.QName != "" {
			fmt.Fprintf(&b, "%d. **%s** — method (%s)\n", i+1, d.File, d.QName)
		} else {
			fmt.Fprintf(&b, "%d. **%s** — %s\n", i+1, d.File, d.Kind)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// handleReferences implements the `references` tool: given a name,
// return every file where it appears in a recognized syntactic
// context (call site, import statement, or raise statement; the
// per-language extractors map their language-native equivalents
// into these three kinds).
//
// Honest framing in the response header: this is "all places the
// name appears in these specific contexts," not "all places that
// are semantically the same definition's references." A function
// named `parse` and a class field named `parse` in a different
// file both show up; tooling that needs semantic precision should
// use an LSP, not ken's structural index.
func handleReferences(ctx context.Context, cfg *Config, args ReferencesArgs) (*sdk.CallToolResult, any, error) {
	sidx, errResult := resolveStructuralIndex(ctx, cfg, args.Repo, args.Output)
	if errResult != nil {
		return errResult, nil, nil
	}
	sym := strings.TrimSpace(args.Symbol)
	if sym == "" {
		return errorResult(args.Output, "symbol is required"), nil, nil
	}

	refs := sidx.References(sym)
	if len(refs) == 0 {
		resp := ReferencesResponse{
			Symbol:     sym,
			References: []ReferenceRowOut{},
			Totals:     ReferencesRowTotal{},
		}
		return dispatchOutput(args.Output, resp, fmt.Sprintf("No references found for %q.", sym))
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

	resp := ReferencesResponse{
		Symbol:     sym,
		References: make([]ReferenceRowOut, 0, len(order)),
		Totals:     ReferencesRowTotal{References: len(refs), Files: len(order)},
	}
	for _, f := range order {
		resp.References = append(resp.References, ReferenceRowOut{
			File:  f,
			Kinds: dedupRefKinds(byFile[f]),
		})
	}
	return dispatchOutput(args.Output, resp, renderReferencesMarkdown(resp))
}

func renderReferencesMarkdown(r ReferencesResponse) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# References: `%s`\n\n", r.Symbol)
	fmt.Fprintf(&b, "_%d reference%s across %d file%s. Name-resolved, not type-resolved — same-spelled identifiers in different contexts collapse into one list._\n\n",
		r.Totals.References, pluralS(r.Totals.References),
		r.Totals.Files, pluralS(r.Totals.Files))
	for i, row := range r.References {
		fmt.Fprintf(&b, "%d. **%s** — %s\n", i+1, row.File, strings.Join(row.Kinds, ", "))
	}
	return strings.TrimRight(b.String(), "\n")
}

// handleCallers implements the `callers` tool: given a function
// name, return the list of FILES that contain a call to it. The
// structural index keys reverse-call by file (not by function), so
// the response honestly describes "file-level callers" — if a file
// has 5 functions and one of them calls the target, the file shows
// once in the list.
//
// Honest framing: the response header notes the granularity AND the
// Stage 8 Gate 2 precision number (100% on a 400-edge sample across
// 8 languages). Tools that need function-level call hierarchy
// ("which function in the file calls X") should fall back to an
// LSP — ken's structural index doesn't track caller-function
// scopes today.
func handleCallers(ctx context.Context, cfg *Config, args CallersArgs) (*sdk.CallToolResult, any, error) {
	sidx, errResult := resolveStructuralIndex(ctx, cfg, args.Repo, args.Output)
	if errResult != nil {
		return errResult, nil, nil
	}
	sym := strings.TrimSpace(args.Symbol)
	if sym == "" {
		return errorResult(args.Output, "symbol is required"), nil, nil
	}

	sites := sidx.Callers(sym)
	if len(sites) == 0 {
		// Empty: same shape in JSON (files: []) so agents don't
		// have to special-case missing data; markdown gets the
		// human-readable hint.
		resp := CallersResponse{Symbol: sym, Files: []string{}}
		md := fmt.Sprintf(
			"No callers found for %q. "+
				"This means no indexed file contains a call to that exact name "+
				"(name-resolved, NOT type-resolved — try the bare method name without a class qualifier).",
			sym)
		return dispatchOutput(args.Output, resp, md)
	}

	// Dedupe + sort by file path. Callers may report a file once
	// per call site; for the file-level granularity we expose, one
	// entry per file is what the agent wants.
	seen := make(map[string]bool, len(sites))
	files := make([]string, 0, len(sites))
	for _, s := range sites {
		if seen[s.File] {
			continue
		}
		seen[s.File] = true
		files = append(files, s.File)
	}
	sort.Strings(files)

	resp := CallersResponse{Symbol: sym, Files: files}
	return dispatchOutput(args.Output, resp, renderCallersMarkdown(resp))
}

func renderCallersMarkdown(r CallersResponse) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Callers: `%s`\n\n", r.Symbol)
	fmt.Fprintf(&b, "_%d file%s contain%s a call to this name. File-level granularity; "+
		"tree-sitter-grade, name-resolved, NOT type-resolved._\n\n",
		len(r.Files), pluralS(len(r.Files)), agreeVerb(len(r.Files) == 1, "s", ""))
	for i, f := range r.Files {
		fmt.Fprintf(&b, "%d. **%s**\n", i+1, f)
	}
	return strings.TrimRight(b.String(), "\n")
}

// agreeVerb returns a or b depending on cond — tiny helper to keep
// English subject-verb agreement readable inline (e.g. "1 file
// contains" vs "5 files contain"). Not specific to did/does; the name
// describes the general subject-verb agreement pick.
func agreeVerb(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}

// handleOutline implements the `outline` tool: given a file path,
// return the file's structural outline (top-level functions,
// classes, methods). Given a directory path, return the outline of
// every indexed file under that directory.
func handleOutline(ctx context.Context, cfg *Config, args OutlineArgs) (*sdk.CallToolResult, any, error) {
	sidx, errResult := resolveStructuralIndex(ctx, cfg, args.Repo, args.Output)
	if errResult != nil {
		return errResult, nil, nil
	}
	rawPath := strings.TrimSpace(args.Path)
	if rawPath == "" {
		return errorResult(args.Output, "path is required (a file path or directory path relative to the repo root)"), nil, nil
	}
	path := structural.NormalizePath(rawPath)

	// First, try as a single file: if the index has an entry for
	// this exact path, render its outline directly.
	if fs := sidx.File(path); fs != nil {
		entries := sidx.Outline(path)
		resp := OutlineResponse{
			Path:    path,
			Entries: convertOutlineEntries(path, entries),
		}
		return dispatchOutput(args.Output, resp, formatOutline(path, entries))
	}

	// Otherwise treat as a directory prefix and walk every
	// indexed file under it. Returns empty if the prefix matches
	// nothing.
	files := sidx.FilesUnderPath(path)
	if len(files) == 0 {
		resp := OutlineResponse{Path: path, Entries: []OutlineEntryOut{}}
		md := fmt.Sprintf("No indexed files found at or under %q. "+
			"Possible reasons: the path doesn't exist, the files aren't a supported "+
			"language (no registered extractor for that file extension), or the corpus excluded them.", path)
		return dispatchOutput(args.Output, resp, md)
	}

	// Bound the whole-repo dump (audit §6): a path-less outline on a big
	// monorepo would otherwise emit every symbol of every file.
	totalFiles := len(files)
	lo, hi, truncated, overshot := pageWindow(totalFiles, args.Offset, args.Limit, MaxOutlineFiles)
	files = files[lo:hi]

	// Outline each file ONCE (audit §16): keep the raw entries for the
	// markdown render instead of calling sidx.Outline again, and only build
	// the JSON structs when json output was actually requested.
	jsonMode := args.Output == "json"
	// Entries always initialized (audit R15): JSON mode must emit "entries":
	// [] not null on an empty window, matching every other empty path here.
	resp := OutlineResponse{Path: path, Entries: []OutlineEntryOut{}, TotalFiles: totalFiles, Offset: lo, Truncated: truncated, Overshot: overshot}
	if overshot {
		return dispatchOutput(args.Output, resp,
			fmt.Sprintf("offset %d is past the end (%d file%s total). Lower `offset`.", args.Offset, totalFiles, pluralS(totalFiles)))
	}
	type fileOutline struct {
		file    string
		entries []structural.OutlineEntry
	}
	rendered := make([]fileOutline, 0, len(files))
	for _, f := range files {
		entries := sidx.Outline(f)
		if len(entries) == 0 {
			continue
		}
		rendered = append(rendered, fileOutline{file: f, entries: entries})
		if jsonMode {
			converted := convertOutlineEntries(f, entries)
			resp.Entries = append(resp.Entries, converted...)
			resp.ByFile = append(resp.ByFile, OutlineFileEntry{File: f, Entries: converted})
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# Outline: `%s` (%d of %d file%s)\n\n",
		path, len(files), totalFiles, pluralS(totalFiles))
	for _, fo := range rendered {
		b.WriteString("## ")
		b.WriteString(fo.file)
		b.WriteString("\n\n")
		b.WriteString(formatOutlineEntries(fo.entries))
		b.WriteString("\n")
	}
	if truncated {
		fmt.Fprintf(&b, "\n_… truncated: showing files %d–%d of %d. Narrow with `path`, or page with `offset: %d`._\n",
			lo+1, hi, totalFiles, hi)
	}
	return dispatchOutput(args.Output, resp, strings.TrimRight(b.String(), "\n"))
}

// convertOutlineEntries adapts structural.OutlineEntry rows into the
// JSON-stable shape. File is the path being outlined (added so a
// flat Entries slice across multiple files is self-describing).
func convertOutlineEntries(file string, entries []structural.OutlineEntry) []OutlineEntryOut {
	out := make([]OutlineEntryOut, 0, len(entries))
	for _, e := range entries {
		out = append(out, OutlineEntryOut{
			File:      file,
			Name:      e.Name,
			Kind:      kindLabel(e.Kind),
			Container: e.Container,
			Params:    e.Params,
		})
	}
	return out
}

// handleSymbols implements the `symbols` tool: given an optional
// path prefix, return every top-level symbol (function or class)
// defined under it. Useful as a "what's in this repo?" or "what's
// in this package?" overview.
func handleSymbols(ctx context.Context, cfg *Config, args SymbolsArgs) (*sdk.CallToolResult, any, error) {
	sidx, errResult := resolveStructuralIndex(ctx, cfg, args.Repo, args.Output)
	if errResult != nil {
		return errResult, nil, nil
	}
	path := strings.TrimSpace(args.Path)
	var names []string
	if path == "" || path == "." {
		names = sidx.Symbols()
	} else {
		names = sidx.SymbolsInPath(structural.NormalizePath(path))
	}
	if names == nil {
		names = []string{}
	}
	// Bound the whole-repo dump (audit §6).
	totalSymbols := len(names)
	lo, hi, truncated, overshot := pageWindow(totalSymbols, args.Offset, args.Limit, MaxSymbols)
	names = names[lo:hi]
	resp := SymbolsResponse{
		PathPrefix:   path,
		Symbols:      names,
		TotalSymbols: totalSymbols,
		Offset:       lo,
		Truncated:    truncated,
		Overshot:     overshot,
	}
	if overshot {
		// Paged past the end (audit R15): tell the agent it overshot rather
		// than returning a silent empty page that reads as "no symbols".
		return dispatchOutput(args.Output, resp,
			fmt.Sprintf("offset %d is past the end (%d symbols total). Lower `offset`.", args.Offset, totalSymbols))
	}
	if totalSymbols == 0 {
		md := ""
		if path == "" {
			md = "No symbols indexed. " +
				"The structural index has no extractors for this corpus."
		} else {
			md = fmt.Sprintf("No symbols found at or under %q.", path)
		}
		return dispatchOutput(args.Output, resp, md)
	}
	return dispatchOutput(args.Output, resp, renderSymbolsMarkdown(resp))
}

func renderSymbolsMarkdown(r SymbolsResponse) string {
	var b strings.Builder
	shown := len(r.Symbols)
	if r.PathPrefix == "" {
		fmt.Fprintf(&b, "# Symbols (%d of %d total)\n\n", shown, r.TotalSymbols)
	} else {
		fmt.Fprintf(&b, "# Symbols under `%s` (%d of %d total)\n\n", r.PathPrefix, shown, r.TotalSymbols)
	}
	for _, n := range r.Symbols {
		fmt.Fprintf(&b, "- `%s`\n", n)
	}
	if r.Truncated {
		fmt.Fprintf(&b, "\n_… truncated: showing symbols %d–%d of %d. Narrow with `path`, or page with `offset: %d`._\n",
			r.Offset+1, r.Offset+shown, r.TotalSymbols, r.Offset+shown)
	}
	return strings.TrimRight(b.String(), "\n")
}

// pageWindow clamps an offset/limit request against a total, returning
// the half-open [lo, hi) bounds to slice and whether the window stops
// short of the total (i.e. the caller should page or narrow). A
// zero-or-negative limit means "use the cap"; limit is itself capped at
// max so a client can't ask past the hard bound (audit §6).
func pageWindow(total, offset, limit, max int) (lo, hi int, truncated, overshot bool) {
	if offset < 0 {
		offset = 0
	}
	// overshot: the caller paged PAST the end of a non-empty set (audit R15).
	// Handlers surface this as "offset N is past the end (total M)" instead
	// of a silent empty page that reads as "nothing here".
	overshot = total > 0 && offset >= total
	if offset > total {
		offset = total
	}
	if limit <= 0 || limit > max {
		limit = max
	}
	lo = offset
	hi = offset + limit
	if hi > total {
		hi = total
	}
	return lo, hi, hi < total, overshot
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
