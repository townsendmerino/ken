package structural

import (
	"path/filepath"
	"sort"
	"strings"
)

// ReferenceKind tags the syntactic context of a reference. Track 2 v0
// returns reference sites without distinguishing kinds in the wire
// format, but the kind is carried internally so future tooling (e.g.
// a Track 2 `callers` tool that filters to ReferenceKindCall) can
// build on the same index.
//
// Tree-sitter-grade: we identify the kind by the parse-tree context
// at the reference site, NOT by type analysis. A call to `parse` and
// a use of `parse` as a function argument are both ReferenceKindCall
// or ReferenceKindName respectively — same name, different syntactic
// contexts, no resolution that this `parse` is the same function as
// any specific definition.
type ReferenceKind uint8

const (
	// ReferenceKindUnknown is the zero value — every concrete
	// reference has a more specific kind.
	ReferenceKindUnknown ReferenceKind = iota

	// ReferenceKindCall: identifier appears as the callee of a
	// call expression (`foo(...)` or `obj.foo(...)`'s leaf
	// `foo`). Captured by the extractor's `case "call"` handling.
	ReferenceKindCall

	// ReferenceKindImport: identifier is brought into local scope
	// by an import statement (`import X`, `from m import X as Y`
	// where the bound name is Y).
	ReferenceKindImport

	// ReferenceKindRaise: identifier appears as the exception
	// class in a raise statement.
	ReferenceKindRaise

	// ReferenceKindAnnotation: identifier appears in a type
	// annotation (parameter type, return type). Stage 8 v0 does
	// NOT capture annotation references — left for a future
	// expansion of the extractor.
	ReferenceKindAnnotation

	// ReferenceKindName: bare identifier in code (variable
	// reference, function argument, etc.). Stage 8 v0 does NOT
	// systematically capture these — too noisy without scope
	// analysis. Reserved for a future pass.
	ReferenceKindName
)

// Reference is one location in the corpus where a name appears in a
// recognized syntactic context (call, import, raise). Returned by
// Index.References for the Track 2 `references` MCP tool.
//
// File is the path relative to the corpus root the index was built
// over. Kind is the syntactic context the reference was found in.
// The Stage 8 v0 extractor does not return line numbers — those can
// be added in a future pass by recording StartPoint at extraction
// time. For now, files-only is the tree-sitter-grade surface.
type Reference struct {
	File string
	Kind ReferenceKind
}

// References returns every recognized reference to name across the
// indexed corpus. Tree-sitter-grade: returns all syntactic
// occurrences in the categories the extractor captures (calls +
// imports + raises in Stage 8 v0). Sorted by file then kind for
// stable iteration.
//
// Distinct names with identical spelling (e.g. `parse` defined in
// two different modules) collapse into a single result list — name
// resolution is not type-aware. The MCP tool description must say so.
func (ix *Index) References(name string) []Reference {
	var out []Reference

	// Call sites — already indexed in ix.callers.
	for _, c := range ix.callers[name] {
		out = append(out, Reference{File: c.File, Kind: ReferenceKindCall})
	}

	// Import + raise sites — walk per-file metadata. Imports and
	// raises are infrequent enough that a linear scan is fine for
	// a 14k-file corpus (sub-ms in practice).
	for path, fs := range ix.files {
		for _, n := range fs.Imports {
			if n == name {
				out = append(out, Reference{File: path, Kind: ReferenceKindImport})
				break
			}
		}
		for _, n := range fs.Raises {
			if n == name {
				out = append(out, Reference{File: path, Kind: ReferenceKindRaise})
				break
			}
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

// DefinitionSite is one location where a top-level name (function or
// class) is defined. Returned by Index.Definition for the Track 2
// `definition` MCP tool.
type DefinitionSite struct {
	File string
	Kind DefinitionKind
}

// DefinitionKind tags whether the defined name is a function or a
// class.
type DefinitionKind uint8

const (
	// DefinitionKindUnknown is the zero value.
	DefinitionKindUnknown DefinitionKind = iota
	DefinitionKindFunction
	DefinitionKindClass
)

// Definition returns the file(s) where the top-level name is
// defined. Tree-sitter-grade: collisions (same name defined in
// multiple files) return all definition sites; ordering is by file
// path. The MCP tool surfaces these in the same order so an agent
// can read down the list as a ranked best-guess (best = first; in
// future iterations we may add a confidence score derived from
// stage-1 hybrid retrieval on the symbol-name-as-query, but Stage 8
// v0 just returns alphabetical file order so the result is stable
// and explainable).
func (ix *Index) Definition(name string) []DefinitionSite {
	files := ix.defs[name]
	if len(files) == 0 {
		return nil
	}
	out := make([]DefinitionSite, 0, len(files))
	for _, f := range files {
		// Look up whether the name is a function or class in
		// this specific file. A name like `User` might be a
		// function in one file and a class in another — return
		// the actual kind per-file.
		kind := DefinitionKindFunction
		if fs := ix.files[f]; fs != nil {
			for _, cls := range fs.Classes {
				if cls.Name == name {
					kind = DefinitionKindClass
					break
				}
			}
		}
		out = append(out, DefinitionSite{File: f, Kind: kind})
	}
	return out
}

// OutlineEntry is one item in a file's structural outline. Functions
// and classes are emitted as top-level entries; methods of a class
// are emitted nested under their class with the class's Name set on
// the Container field.
type OutlineEntry struct {
	Name      string
	Kind      DefinitionKind
	Container string // for methods: enclosing class; for top-level: ""
	Params    []string
}

// Outline returns the file's structural outline — every top-level
// function, every class, and every method of each class. Used by
// the Track 2 `outline` MCP tool to give an agent a structural
// overview without reading the file.
//
// Order: top-level entries appear in source order (the order the
// extractor populated fs.Functions / fs.Classes). Methods appear
// after their containing class in the order the extractor saw
// them.
func (ix *Index) Outline(filePath string) []OutlineEntry {
	fs := ix.files[filePath]
	if fs == nil {
		return nil
	}
	var out []OutlineEntry

	// Build a set of method names for fast skip of duplicate
	// top-level entries (methods also appear in fs.Functions with
	// IsMethod=true; we render them under their class instead).
	methodSet := make(map[string]struct{})
	for _, cls := range fs.Classes {
		for _, m := range cls.Methods {
			methodSet[m.EnclosingClass+"."+m.Name] = struct{}{}
		}
	}

	// Walk in the order FileStruct stored them. For each top-level
	// function emit it directly. For a method (IsMethod=true)
	// skip — it'll be emitted via its containing class.
	for _, fn := range fs.Functions {
		if fn.IsMethod {
			continue
		}
		out = append(out, OutlineEntry{
			Name:   fn.Name,
			Kind:   DefinitionKindFunction,
			Params: fn.Params,
		})
	}
	for _, cls := range fs.Classes {
		out = append(out, OutlineEntry{
			Name: cls.Name,
			Kind: DefinitionKindClass,
		})
		for _, m := range cls.Methods {
			out = append(out, OutlineEntry{
				Name:      m.Name,
				Kind:      DefinitionKindFunction,
				Container: cls.Name,
				Params:    m.Params,
			})
		}
	}
	return out
}

// Symbols returns every top-level name (function or class) defined
// anywhere in the indexed corpus. Used by the Track 2 `symbols`
// tool when called with an empty path (list every top-level symbol)
// or via SymbolsInPath for a subdirectory filter. Sorted lexically
// for stable output.
func (ix *Index) Symbols() []string {
	out := make([]string, 0, len(ix.defs))
	for name := range ix.defs {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// SymbolsInPath returns the top-level names defined under path
// (treated as a directory prefix on the file paths the index was
// built with). Empty path returns the same as Symbols(). Used by
// the Track 2 `symbols` tool for "what's defined under foo/bar/?".
func (ix *Index) SymbolsInPath(path string) []string {
	if path == "" || path == "." {
		return ix.Symbols()
	}
	prefix := path
	if !strings.HasSuffix(prefix, "/") {
		prefix = prefix + "/"
	}
	matches := make(map[string]struct{})
	for name, files := range ix.defs {
		for _, f := range files {
			if strings.HasPrefix(f, prefix) || f == path {
				matches[name] = struct{}{}
				break
			}
		}
	}
	out := make([]string, 0, len(matches))
	for n := range matches {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// FilesUnderPath returns every indexed file under the given path
// prefix. Used internally by SymbolsInPath and externally by the
// Track 2 outline tool when given a directory instead of a single
// file. Sorted by path.
func (ix *Index) FilesUnderPath(path string) []string {
	if path == "" || path == "." {
		out := make([]string, 0, len(ix.files))
		for p := range ix.files {
			out = append(out, p)
		}
		sort.Strings(out)
		return out
	}
	prefix := path
	if !strings.HasSuffix(prefix, "/") {
		prefix = prefix + "/"
	}
	var out []string
	for p := range ix.files {
		if strings.HasPrefix(p, prefix) || p == path {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// NormalizePath canonicalizes a user-supplied path argument to the
// form the index stores: forward slashes, no leading "./", no
// trailing slash. Mirrors filepath.Clean behavior but always emits
// forward slashes regardless of OS — the MCP wire format and the
// index are both unix-shaped.
func NormalizePath(p string) string {
	p = filepath.ToSlash(filepath.Clean(p))
	if strings.HasPrefix(p, "./") {
		p = p[2:]
	}
	return p
}
