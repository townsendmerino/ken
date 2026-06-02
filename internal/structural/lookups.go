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

// DefinitionSite is one location where a name (function, class, or
// method) is defined. Returned by Index.Definition for the Track 2
// `definition` MCP tool.
//
// For methods (Kind == DefinitionKindMethod), QName carries the
// qualified Type.method form (e.g. "User.Login") so the agent can
// disambiguate when the same bare method name lives on multiple
// types. For top-level functions and classes, QName is empty (the
// bare name uniquely identifies the definition within a file).
type DefinitionSite struct {
	File  string
	Kind  DefinitionKind
	QName string // qualified Type.method name; empty for non-methods
}

// DefinitionKind tags what kind of definition was found at a site.
type DefinitionKind uint8

const (
	// DefinitionKindUnknown is the zero value.
	DefinitionKindUnknown DefinitionKind = iota
	// DefinitionKindFunction: a top-level function (no receiver
	// type / no enclosing class).
	DefinitionKindFunction
	// DefinitionKindClass: a class (Python) or named type
	// (Go struct/interface/etc).
	DefinitionKindClass
	// DefinitionKindMethod: a method defined on a class/type.
	// Stage 8 v0+: methods are queryable by bare name OR
	// qualified Type.method form. A site returned with Method
	// kind carries the qualified form in DefinitionSite.QName.
	DefinitionKindMethod
)

// Definition returns the site(s) where the name is defined. Stage 8
// v0+ supports three lookup forms:
//
//   - Bare name like "Login": returns ALL definition sites — top-
//     level functions/classes with that name AND every method named
//     Login across the corpus's classes. Per-site Kind labels
//     distinguish (Function / Class / Method); for methods, QName
//     carries the "Type.method" form.
//   - Qualified name like "User.Login": returns ONLY the method
//     sites where the enclosing class is User and the method is
//     Login. The dotted form disambiguates when the same bare
//     method name exists on multiple types.
//   - Top-level type/function name: returns Function or Class sites
//     same as before; methods of unrelated types are NOT included
//     (the bare match is restricted to exact-name).
//
// Tree-sitter-grade: name-resolved, NOT type-resolved. Collisions
// across files (two unrelated classes both with a "User" type, two
// modules both defining a top-level "parse" function) return all
// matching sites in alphabetical-by-file order. Ordering does NOT
// reflect confidence; the agent reading the list cannot assume
// "first result is the right one." The MCP tool description spells
// this out.
func (ix *Index) Definition(name string) []DefinitionSite {
	// For qualified names ("Type.method"), only the methods map can
	// match — defs holds bare names. Short-circuit so we don't
	// accidentally pick up a top-level function or class whose
	// literal text happens to contain a dot (which can't happen in
	// any of Stage 8 v0's supported grammars, but is defensive).
	if dot := indexOfDot(name); dot >= 0 {
		var out []DefinitionSite
		for _, f := range ix.methods[name] {
			out = append(out, DefinitionSite{
				File:  f,
				Kind:  DefinitionKindMethod,
				QName: name,
			})
		}
		return out
	}

	// Bare name: merge top-level defs + method sites. Same file
	// may appear in both (e.g. file foo.go defines top-level
	// `Login()` AND class User with method Login). Dedup on
	// (File, Kind) so the response shows distinct kinds even when
	// they share a file; same (File, Kind) tuple at most once.
	type key struct {
		file string
		kind DefinitionKind
	}
	seen := make(map[key]string) // (file, kind) → QName (empty for non-methods)
	var ordered []key

	// Top-level defs first. Kind is Function unless the file's
	// Classes table claims `name` as a class.
	for _, f := range ix.defs[name] {
		kind := DefinitionKindFunction
		if fs := ix.files[f]; fs != nil {
			for _, cls := range fs.Classes {
				if cls.Name == name {
					kind = DefinitionKindClass
					break
				}
			}
		}
		k := key{file: f, kind: kind}
		if _, dup := seen[k]; !dup {
			seen[k] = ""
			ordered = append(ordered, k)
		}
	}

	// Methods (bare-name lookup). The methods map under a bare
	// key holds files containing ≥1 method by that name; the
	// specific qualified form (which class's method) requires a
	// per-file walk of fs.Functions. Multiple classes in one
	// file can each have a method with this bare name — emit
	// one DefinitionSite per (file, class.method) pair so the
	// agent sees all of them.
	for _, f := range ix.methods[name] {
		fs := ix.files[f]
		if fs == nil {
			continue
		}
		for _, fn := range fs.Functions {
			if !fn.IsMethod || fn.Name != name {
				continue
			}
			// Could be a method whose qualified name is
			// "User.Login" with EnclosingClass="User". Emit
			// the qualified form so the agent can route a
			// follow-up call to a specific class's method.
			qname := name
			if fn.EnclosingClass != "" {
				qname = fn.EnclosingClass + "." + name
			}
			k := key{file: f, kind: DefinitionKindMethod}
			// Multiple methods with same bare name in the
			// same file collapse to one site (rare; would
			// require two classes in one file both defining
			// the same-named method). The first
			// EnclosingClass wins under that collision —
			// acceptable for v0; could split later.
			if _, dup := seen[k]; !dup {
				seen[k] = qname
				ordered = append(ordered, k)
			}
		}
	}

	if len(ordered) == 0 {
		return nil
	}

	// Stable alphabetical-by-file order. The MCP tool's response
	// emits this order so reads are reproducible across calls.
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].file != ordered[j].file {
			return ordered[i].file < ordered[j].file
		}
		return ordered[i].kind < ordered[j].kind
	})

	out := make([]DefinitionSite, 0, len(ordered))
	for _, k := range ordered {
		out = append(out, DefinitionSite{
			File:  k.file,
			Kind:  k.kind,
			QName: seen[k],
		})
	}
	return out
}

// indexOfDot returns the index of the first '.' in s, or -1. Tiny
// helper kept here to avoid a strings import for a 4-line check.
func indexOfDot(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			return i
		}
	}
	return -1
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
