package structural

import (
	"strings"

	"github.com/odvcencio/gotreesitter"
)

// extractTypeScript walks a tree-sitter-typescript AST and fills
// FileStruct. Shape parity with extract_python.go +
// extract_go.go — same FileStruct fields populated.
//
// TypeScript node-type mapping (from the tree-sitter-typescript
// grammar shipped with gotreesitter v0.18.0):
//
//   - function_declaration       — top-level `function foo()`
//   - method_definition          — `class X { foo() {} }`
//   - arrow_function             — `(x) => ...` (named via
//     variable_declarator)
//   - class_declaration          — `class Foo {}`
//   - interface_declaration      — `interface Foo {}`
//   - call_expression            — `foo(...)` / `obj.foo(...)`
//   - member_expression          — `obj.foo` (call's leaf name)
//   - import_statement           — every form
//   - lexical_declaration        — `const`/`let` (carries
//     variable_declarator children)
//
// Common TS wrapper: many declarations are inside `export_statement`
// — we recurse through it, treating the inner declaration as the
// real one. Same for `ambient_declaration` (`declare ...`) and
// the type-only `type_alias_declaration` (skipped — not a runtime
// definition).
//
// Caveat (gotreesitter v0.18.0 limitation): the bundled
// tree-sitter-typescript grammar trips on some current-TS
// constructs (e.g. arrow with type annotation in declaration
// position) and produces ERROR nodes. The walker handles ERROR
// children defensively — anything we don't recognize is skipped,
// the FileStruct just lacks that fact.
//
// Stage 8 v0 does NOT capture:
//   - JSX/TSX components (the "typescript" grammar excludes JSX;
//     a future revision could route .tsx files to a "tsx" grammar
//     entry).
//   - Object-literal methods (`{ foo() {} }`) — covered by
//     pair-with-arrow shorthand instead.
//   - Decorators / class field declarations beyond methods.
func extractTypeScript(src []byte, root *gotreesitter.Node, lang *gotreesitter.Language, fs *FileStruct) {
	walkTS(src, root, lang, "", fs)
}

func walkTS(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, enclosingClass string, fs *FileStruct) {
	if n == nil {
		return
	}
	switch n.Type(lang) {
	case "function_declaration":
		fn := extractTSFunc(src, n, lang, "")
		fs.Functions = append(fs.Functions, fn)
		if body := n.ChildByFieldName("body", lang); body != nil {
			recurseChildrenTS(src, body, lang, enclosingClass, fs)
		}
	case "method_definition":
		// Methods live inside a class_body. The walkTS
		// class_declaration arm sets enclosingClass before
		// descending into the body, so by the time we hit a
		// method_definition the enclosingClass var carries the
		// type name.
		fn := extractTSFunc(src, n, lang, enclosingClass)
		fs.Functions = append(fs.Functions, fn)
		if body := n.ChildByFieldName("body", lang); body != nil {
			recurseChildrenTS(src, body, lang, enclosingClass, fs)
		}
	case "class_declaration":
		cls := extractTSClass(src, n, lang)
		fs.Classes = append(fs.Classes, cls)
		body := n.ChildByFieldName("body", lang)
		if body != nil {
			recurseChildrenTS(src, body, lang, cls.Name, fs)
		}
	case "interface_declaration":
		// TS interfaces are class-like for outline purposes —
		// they have a Name and member signatures. Stage 8 v0
		// captures the interface as a ClassDef with no methods
		// (methods on an interface are signatures, not
		// definitions). The Name surfaces in defs and Outline.
		if name := n.ChildByFieldName("name", lang); name != nil {
			fs.Classes = append(fs.Classes, ClassDef{Name: nodeText(src, name)})
		}
	case "call_expression":
		// Node CommonJS `require('module')` doesn't use the
		// import_statement node — it's a normal call_expression
		// whose function is the bare identifier `require`. Bind
		// it on the import side instead of letting "require"
		// pollute fs.Calls.
		if mod := jsExtractRequire(src, n, lang); mod != "" {
			fs.Imports = dedupAppend(fs.Imports, mod)
		} else if name := tsCalleeName(src, n, lang); name != "" && !tsIsBuiltinOrNoise(name) {
			fs.Calls = dedupAppend(fs.Calls, name)
		}
		recurseChildrenTS(src, n, lang, enclosingClass, fs)
	case "throw_statement":
		// TS doesn't have a "raise" — but `throw new X(...)` /
		// `throw X` is the analog. Capture the constructor/
		// identifier so Enrich's `raises:` section fires.
		if name := tsThrowName(src, n, lang); name != "" {
			fs.Raises = dedupAppend(fs.Raises, name)
		}
		recurseChildrenTS(src, n, lang, enclosingClass, fs)
	case "import_statement":
		extractTSImports(src, n, lang, fs)
	case "lexical_declaration", "variable_declaration":
		// `const Login = (...) => ...` is a variable_declarator
		// with an arrow_function as its value. Treat it as a
		// FuncDef so the agent's `definition("Login")` finds it.
		nc := n.NamedChildCount()
		for i := 0; i < nc; i++ {
			c := n.NamedChild(i)
			if c == nil || c.Type(lang) != "variable_declarator" {
				continue
			}
			// Pattern is the variable name; value is the rhs.
			pattern := c.ChildByFieldName("name", lang)
			value := c.ChildByFieldName("value", lang)
			if pattern == nil || value == nil {
				continue
			}
			if value.Type(lang) != "arrow_function" && value.Type(lang) != "function_expression" {
				// Not a function-shaped declaration — keep
				// recursing in case the rhs has calls.
				recurseChildrenTS(src, c, lang, enclosingClass, fs)
				continue
			}
			fn := FuncDef{
				Name:           nodeText(src, pattern),
				IsMethod:       false,
				EnclosingClass: "",
			}
			// Parameters: arrow_function can have either a
			// `parameter` field (single identifier, e.g.
			// `x => ...`) or `parameters` (formal_parameters).
			if p := value.ChildByFieldName("parameter", lang); p != nil {
				fn.Params = append(fn.Params, nodeText(src, p))
			} else if ps := value.ChildByFieldName("parameters", lang); ps != nil {
				fn.Params = extractTSParams(src, ps, lang)
			}
			if ret := value.ChildByFieldName("return_type", lang); ret != nil {
				fn.ReturnType = nodeText(src, ret)
			}
			fs.Functions = append(fs.Functions, fn)
			// Arrow functions have two body shapes:
			//   - block body: `(x) => { ... }` — under `body`
			//     field (statement_block).
			//   - expression body: `(x) => expr` — NO field name
			//     on the expression child; it's just the next
			//     named child after parameters.
			// We recurse over every named child of the arrow,
			// skipping `parameters`/`parameter`/`return_type`.
			// This catches both shapes uniformly.
			anc := value.NamedChildCount()
			for j := 0; j < anc; j++ {
				ac := value.NamedChild(j)
				if ac == nil {
					continue
				}
				fname := value.FieldNameForChild(j, lang)
				if fname == "parameters" || fname == "parameter" || fname == "return_type" {
					continue
				}
				walkTS(src, ac, lang, enclosingClass, fs)
			}
		}
	case "export_statement", "ambient_declaration":
		// `export function ...` / `declare function ...` wrap
		// an inner declaration. Recurse into children at the
		// same enclosingClass scope.
		recurseChildrenTS(src, n, lang, enclosingClass, fs)
	case "type_alias_declaration":
		// `type Foo = ...` is a type-only construct, not a
		// runtime definition. Skip — agents asking
		// definition("Foo") for a type alias should grep, not
		// use structural.
		return
	default:
		recurseChildrenTS(src, n, lang, enclosingClass, fs)
	}
}

func recurseChildrenTS(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, enclosingClass string, fs *FileStruct) {
	nc := n.NamedChildCount()
	for i := 0; i < nc; i++ {
		walkTS(src, n.NamedChild(i), lang, enclosingClass, fs)
	}
}

func extractTSFunc(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, enclosingClass string) FuncDef {
	fn := FuncDef{
		IsMethod:       enclosingClass != "",
		EnclosingClass: enclosingClass,
	}
	if name := n.ChildByFieldName("name", lang); name != nil {
		fn.Name = nodeText(src, name)
	}
	if params := n.ChildByFieldName("parameters", lang); params != nil {
		fn.Params = extractTSParams(src, params, lang)
	}
	if ret := n.ChildByFieldName("return_type", lang); ret != nil {
		fn.ReturnType = nodeText(src, ret)
	}
	return fn
}

func extractTSParams(src []byte, params *gotreesitter.Node, lang *gotreesitter.Language) []string {
	var out []string
	pc := params.NamedChildCount()
	for i := 0; i < pc; i++ {
		c := params.NamedChild(i)
		if c == nil {
			continue
		}
		// Both `required_parameter` and `optional_parameter`
		// shapes; both have a `pattern` field with the bound
		// name (identifier or destructuring).
		var pat *gotreesitter.Node
		switch c.Type(lang) {
		case "required_parameter", "optional_parameter":
			pat = c.ChildByFieldName("pattern", lang)
		case "identifier":
			pat = c
		}
		if pat == nil {
			continue
		}
		// Skip destructuring patterns for v0 (object_pattern /
		// array_pattern) — they don't have a single bound name.
		if pat.Type(lang) != "identifier" {
			continue
		}
		if name := nodeText(src, pat); name != "" {
			out = append(out, name)
		}
	}
	return out
}

func extractTSClass(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language) ClassDef {
	cls := ClassDef{}
	if name := n.ChildByFieldName("name", lang); name != nil {
		cls.Name = nodeText(src, name)
	}
	body := n.ChildByFieldName("body", lang)
	if body == nil {
		return cls
	}
	bc := body.NamedChildCount()
	for i := 0; i < bc; i++ {
		c := body.NamedChild(i)
		if c == nil {
			continue
		}
		if c.Type(lang) == "method_definition" {
			cls.Methods = append(cls.Methods, extractTSFunc(src, c, lang, cls.Name))
		}
	}
	return cls
}

func tsCalleeName(src []byte, callNode *gotreesitter.Node, lang *gotreesitter.Language) string {
	fn := callNode.ChildByFieldName("function", lang)
	if fn == nil {
		return ""
	}
	switch fn.Type(lang) {
	case "identifier":
		return nodeText(src, fn)
	case "member_expression":
		// We want the rightmost property name (`obj.foo` → "foo").
		if prop := fn.ChildByFieldName("property", lang); prop != nil {
			return nodeText(src, prop)
		}
	}
	return ""
}

func tsThrowName(src []byte, throwNode *gotreesitter.Node, lang *gotreesitter.Language) string {
	// `throw X` / `throw X(...)` / `throw new X(...)` — the first
	// named child is the thrown expression. Resolve the leaf name.
	nc := throwNode.NamedChildCount()
	if nc == 0 {
		return ""
	}
	expr := throwNode.NamedChild(0)
	if expr == nil {
		return ""
	}
	switch expr.Type(lang) {
	case "identifier":
		return nodeText(src, expr)
	case "new_expression":
		// `new X(...)` — the constructor field carries the name.
		if ctor := expr.ChildByFieldName("constructor", lang); ctor != nil {
			if ctor.Type(lang) == "identifier" {
				return nodeText(src, ctor)
			}
		}
	case "call_expression":
		return tsCalleeName(src, expr, lang)
	}
	return ""
}

func extractTSImports(src []byte, importNode *gotreesitter.Node, lang *gotreesitter.Language, fs *FileStruct) {
	// `import_statement` has an optional `import_clause` (named
	// imports, default, namespace) and a `source` (the module
	// path). We surface every locally-bound name from the
	// import_clause; the module path itself doesn't go into
	// fs.Imports (it's not the BOUND name).
	clause := findChildByType(importNode, lang, "import_clause")
	if clause == nil {
		return
	}
	cc := clause.NamedChildCount()
	for i := 0; i < cc; i++ {
		c := clause.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Type(lang) {
		case "identifier":
			// Default import: `import Foo from "..."`
			if name := nodeText(src, c); name != "" {
				fs.Imports = dedupAppend(fs.Imports, name)
			}
		case "named_imports":
			// `{ a, b as c }` — each import_specifier child
			// has `name` and optional `alias` fields. Alias
			// wins as the bound local name; else `name`.
			sc := c.NamedChildCount()
			for j := 0; j < sc; j++ {
				spec := c.NamedChild(j)
				if spec == nil || spec.Type(lang) != "import_specifier" {
					continue
				}
				if alias := spec.ChildByFieldName("alias", lang); alias != nil {
					fs.Imports = dedupAppend(fs.Imports, nodeText(src, alias))
				} else if name := spec.ChildByFieldName("name", lang); name != nil {
					fs.Imports = dedupAppend(fs.Imports, nodeText(src, name))
				}
			}
		case "namespace_import":
			// `import * as bar from "..."` — the identifier
			// child IS the bound name.
			sc := c.NamedChildCount()
			for j := 0; j < sc; j++ {
				inner := c.NamedChild(j)
				if inner != nil && inner.Type(lang) == "identifier" {
					fs.Imports = dedupAppend(fs.Imports, nodeText(src, inner))
					break
				}
			}
		}
	}
}

// findChildByType returns the first named child whose Type matches.
// Tiny helper to avoid an explicit loop at call sites.
func findChildByType(n *gotreesitter.Node, lang *gotreesitter.Language, typeName string) *gotreesitter.Node {
	nc := n.NamedChildCount()
	for i := 0; i < nc; i++ {
		c := n.NamedChild(i)
		if c != nil && c.Type(lang) == typeName {
			return c
		}
	}
	return nil
}

// jsExtractRequire returns the bound-name basename when the given
// call_expression is a Node CommonJS `require('modulePath')` call;
// returns "" otherwise. Detection is strict:
//
//   - function field is the bare identifier `require`
//   - first argument is a string literal
//
// Both filters together avoid matching `require.resolve(...)`
// (function = member_expression, not identifier) and
// `require(varName)` (arg = identifier, not string). The bound name
// is the rightmost segment of the module path, with extension /
// scope-prefix stripped:
//
//	require('lodash')           → "lodash"
//	require('node:path')        → "path"
//	require('@my/foo/bar')      → "bar"
//	require('./util/helpers')   → "helpers"
//
// This shape only appears in JavaScript codebases (TS uses
// import_statement) but the same walker covers both, so the check
// runs regardless — TS code that doesn't use require() simply
// never triggers it.
func jsExtractRequire(src []byte, callNode *gotreesitter.Node, lang *gotreesitter.Language) string {
	fn := callNode.ChildByFieldName("function", lang)
	if fn == nil || fn.Type(lang) != "identifier" {
		return ""
	}
	if nodeText(src, fn) != "require" {
		return ""
	}
	args := callNode.ChildByFieldName("arguments", lang)
	if args == nil || args.NamedChildCount() == 0 {
		return ""
	}
	arg0 := args.NamedChild(0)
	if arg0 == nil || arg0.Type(lang) != "string" {
		return ""
	}
	// The string node holds a string_fragment child with the
	// quote-stripped path; fall back to trimming the raw text if
	// the shape varies.
	var path string
	cc := arg0.NamedChildCount()
	for j := 0; j < cc; j++ {
		inner := arg0.NamedChild(j)
		if inner != nil && inner.Type(lang) == "string_fragment" {
			path = nodeText(src, inner)
			break
		}
	}
	if path == "" {
		path = strings.Trim(nodeText(src, arg0), "\"'`")
	}
	return jsModuleBoundName(path)
}

// jsModuleBoundName collapses a module path to the agent-searchable
// bound name. Strips a `node:` / scope prefix and any directory
// segments, returning the rightmost path component.
func jsModuleBoundName(path string) string {
	// `node:path` → `path`
	if i := strings.LastIndex(path, ":"); i >= 0 {
		path = path[i+1:]
	}
	// `@scope/pkg/sub` → `sub`; `./util/helpers` → `helpers`
	if i := strings.LastIndex(path, "/"); i >= 0 {
		path = path[i+1:]
	}
	return path
}

func tsIsBuiltinOrNoise(name string) bool {
	switch name {
	// JS builtins / common idioms — every function calls these;
	// they don't bridge any vocab.
	case "console", "log", "error", "warn", "info", "debug",
		"parseInt", "parseFloat", "Number", "String", "Boolean",
		"Object", "Array",
		"push", "pop", "shift", "unshift", "slice", "splice",
		"map", "filter", "reduce", "forEach", "find",
		"toString", "valueOf",
		"setTimeout", "setInterval", "clearTimeout", "clearInterval":
		return true
	}
	return false
}
