// C# structural extractor. Un-parked 2026-06-06: gotreesitter v0.20.2
// bounded the C# namespace-recovery sub-parses that previously OOM'd on
// real-world C# (DapperLib/Dapper retest under v0.20.0-rc3: 93+ GB
// resident before SIGKILL). Re-verified on v0.20.2: Dapper's 156 .cs
// files parse in ~3s, 89% clean root, no OOM (the minimal reproducer
// from docs/internal/csharp-oom-root-cause.md now parses in ~5ms). See DESIGN.md
// §10 and that memo for the resolution history.

package structural

import (
	"github.com/odvcencio/gotreesitter"
)

// extractCsharp walks a tree-sitter-c_sharp AST and fills FileStruct.
// Same shape as the other extractors.
//
// Status as of 2026-06-06: UN-PARKED. gotreesitter v0.20.2 bounded the
// namespace-recovery sub-parses whose unbounded recursion previously
// OOM'd on real-world C# (dapper retest under v0.20.0-rc3: 93+ GB
// resident before SIGKILL). Re-verified on v0.20.2: Dapper's 156 .cs
// files parse in ~3s, 89% clean root, no OOM; the minimal reproducer
// from docs/internal/csharp-oom-root-cause.md now parses in ~5ms. This file
// compiles in the default build, `.cs → c_sharp` is registered in
// kenLangToTSLang, and `c_sharp → extractCsharp` in langExtractor.
//
// C# node-type mapping (from gotreesitter v0.20.2):
//
//   - method_declaration       — name field is identifier; parameters,
//     returns, body all field-named.
//   - constructor_declaration  — same shape; name == enclosing class.
//   - class_declaration        — name + declaration_list body.
//   - interface_declaration    — same shape, signature-only methods.
//   - struct_declaration       — value type; treated as class for
//     outline purposes.
//   - record_declaration       — C# 9+ records; positional-record
//     params are skipped (no body methods).
//   - namespace_declaration    — declaration_list body; recurse
//     without changing enclosingClass
//     (namespaces shape symbols but
//     methods bind to classes, not
//     namespaces).
//   - invocation_expression    — C#'s call node; `function` field is
//     identifier OR member_access_expression.
//   - object_creation_expression — `new Foo(...)`; type is the
//     constructor target.
//   - throw_statement / throw_expression — analog of raises.
//   - using_directive          — `using System.Collections.Generic;`
//     bound name is the rightmost identifier
//     of the qualified_name (the type that's
//     in scope).
func extractCsharp(src []byte, root *gotreesitter.Node, lang *gotreesitter.Language, fs *FileStruct) {
	walkCsharp(src, root, lang, "", "", fs)
}

func walkCsharp(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, enclosingClass, enclosingSymbol string, fs *FileStruct) {
	if n == nil {
		return
	}
	switch n.Type(lang) {
	case "method_declaration":
		fn := extractCsharpMethod(src, n, lang, enclosingClass)
		fn.fillSpan(n)
		fs.Functions = append(fs.Functions, fn)
		sym := qualifySymbol(enclosingClass, fn.Name)
		if body := n.ChildByFieldName("body", lang); body != nil {
			recurseChildrenCsharp(src, body, lang, enclosingClass, sym, fs)
		}
	case "constructor_declaration":
		fn := extractCsharpMethod(src, n, lang, enclosingClass)
		if fn.Name == "" && enclosingClass != "" {
			fn.Name = enclosingClass
		}
		fn.fillSpan(n)
		fs.Functions = append(fs.Functions, fn)
		sym := qualifySymbol(enclosingClass, fn.Name)
		if body := n.ChildByFieldName("body", lang); body != nil {
			recurseChildrenCsharp(src, body, lang, enclosingClass, sym, fs)
		}
	case "class_declaration", "struct_declaration", "record_declaration", "enum_declaration":
		cls := extractCsharpClass(src, n, lang)
		cls.fillSpan(n)
		fs.Classes = append(fs.Classes, cls)
		body := n.ChildByFieldName("body", lang)
		if body != nil {
			recurseChildrenCsharp(src, body, lang, cls.Name, enclosingSymbol, fs)
		}
	case "interface_declaration":
		cls := extractCsharpClass(src, n, lang)
		cls.fillSpan(n)
		fs.Classes = append(fs.Classes, cls)
		body := n.ChildByFieldName("body", lang)
		if body != nil {
			recurseChildrenCsharp(src, body, lang, cls.Name, enclosingSymbol, fs)
		}
	case "namespace_declaration", "file_scoped_namespace_declaration":
		body := n.ChildByFieldName("body", lang)
		if body != nil {
			recurseChildrenCsharp(src, body, lang, enclosingClass, enclosingSymbol, fs)
		} else {
			recurseChildrenCsharp(src, n, lang, enclosingClass, enclosingSymbol, fs)
		}
	case "invocation_expression":
		if fn := n.ChildByFieldName("function", lang); fn != nil {
			if name := csharpCalleeName(src, fn, lang); name != "" && !csharpIsBuiltinOrNoise(name) {
				fs.appendCall(name, "", n, enclosingSymbol)
			}
		}
		recurseChildrenCsharp(src, n, lang, enclosingClass, enclosingSymbol, fs)
	case "object_creation_expression":
		if tnode := n.ChildByFieldName("type", lang); tnode != nil {
			if s := csharpTypeLeafName(src, tnode, lang); s != "" && !csharpIsBuiltinOrNoise(s) {
				fs.appendCall(s, "", n, enclosingSymbol)
			}
		}
		recurseChildrenCsharp(src, n, lang, enclosingClass, enclosingSymbol, fs)
	case "throw_statement", "throw_expression":
		if name := csharpThrowName(src, n, lang); name != "" {
			fs.Raises = dedupAppend(fs.Raises, name)
		}
		recurseChildrenCsharp(src, n, lang, enclosingClass, enclosingSymbol, fs)
	case "using_directive":
		if name := csharpUsingBoundName(src, n, lang); name != "" {
			fs.Imports = dedupAppend(fs.Imports, name)
		}
	default:
		recurseChildrenCsharp(src, n, lang, enclosingClass, enclosingSymbol, fs)
	}
}

func recurseChildrenCsharp(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, enclosingClass, enclosingSymbol string, fs *FileStruct) {
	nc := n.NamedChildCount()
	for i := 0; i < nc; i++ {
		walkCsharp(src, n.NamedChild(i), lang, enclosingClass, enclosingSymbol, fs)
	}
}

func extractCsharpMethod(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, enclosingClass string) FuncDef {
	fn := FuncDef{
		IsMethod:       enclosingClass != "",
		EnclosingClass: enclosingClass,
	}
	if name := n.ChildByFieldName("name", lang); name != nil {
		fn.Name = nodeText(src, name)
	}
	if params := n.ChildByFieldName("parameters", lang); params != nil {
		fn.Params = extractCsharpParams(src, params, lang)
	}
	if ret := n.ChildByFieldName("returns", lang); ret != nil {
		fn.ReturnType = nodeText(src, ret)
	}
	return fn
}

func extractCsharpParams(src []byte, params *gotreesitter.Node, lang *gotreesitter.Language) []string {
	var out []string
	pc := params.NamedChildCount()
	for i := 0; i < pc; i++ {
		c := params.NamedChild(i)
		if c == nil {
			continue
		}
		if c.Type(lang) != "parameter" {
			continue
		}
		// parameter has `name` field (identifier) — that's the bound
		// name. The `type` field is the type annotation; we ignore it
		// for fs.Params, which only carries names.
		if name := c.ChildByFieldName("name", lang); name != nil {
			if s := nodeText(src, name); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

func extractCsharpClass(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language) ClassDef {
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
		switch c.Type(lang) {
		case "method_declaration":
			cls.Methods = append(cls.Methods, extractCsharpMethod(src, c, lang, cls.Name))
		case "constructor_declaration":
			ctor := extractCsharpMethod(src, c, lang, cls.Name)
			if ctor.Name == "" {
				ctor.Name = cls.Name
			}
			cls.Methods = append(cls.Methods, ctor)
		}
	}
	return cls
}

// csharpCalleeName resolves an invocation_expression's function field
// to the rightmost identifier. Identifier nodes return their text;
// member_access_expression nodes return their `name` field
// (`obj.Method` → "Method"); generic_name returns its `name` field
// (`Foo<T>` → "Foo").
func csharpCalleeName(src []byte, fn *gotreesitter.Node, lang *gotreesitter.Language) string {
	if fn == nil {
		return ""
	}
	switch fn.Type(lang) {
	case "identifier":
		return nodeText(src, fn)
	case "member_access_expression":
		if name := fn.ChildByFieldName("name", lang); name != nil {
			return csharpCalleeName(src, name, lang)
		}
	case "generic_name":
		if name := fn.ChildByFieldName("name", lang); name != nil {
			return nodeText(src, name)
		}
		// fallback: first identifier child
		nc := fn.NamedChildCount()
		for i := 0; i < nc; i++ {
			c := fn.NamedChild(i)
			if c != nil && c.Type(lang) == "identifier" {
				return nodeText(src, c)
			}
		}
	}
	return ""
}

// csharpTypeLeafName resolves a type node to the rightmost identifier
// — useful for `new Foo<T>()` (generic_name → "Foo") and
// `new System.Text.StringBuilder()` (qualified_name → "StringBuilder").
func csharpTypeLeafName(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language) string {
	if n == nil {
		return ""
	}
	switch n.Type(lang) {
	case "identifier":
		return nodeText(src, n)
	case "generic_name":
		if name := n.ChildByFieldName("name", lang); name != nil {
			return nodeText(src, name)
		}
	case "qualified_name":
		// Rightmost identifier component.
		if name := n.ChildByFieldName("name", lang); name != nil {
			return csharpTypeLeafName(src, name, lang)
		}
		nc := n.NamedChildCount()
		for i := nc - 1; i >= 0; i-- {
			c := n.NamedChild(i)
			if c != nil && c.Type(lang) == "identifier" {
				return nodeText(src, c)
			}
		}
	}
	return ""
}

func csharpThrowName(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language) string {
	nc := n.NamedChildCount()
	if nc == 0 {
		return ""
	}
	expr := n.NamedChild(0)
	if expr == nil {
		return ""
	}
	switch expr.Type(lang) {
	case "identifier":
		return nodeText(src, expr)
	case "object_creation_expression":
		if tnode := expr.ChildByFieldName("type", lang); tnode != nil {
			return csharpTypeLeafName(src, tnode, lang)
		}
	case "invocation_expression":
		if fn := expr.ChildByFieldName("function", lang); fn != nil {
			return csharpCalleeName(src, fn, lang)
		}
	}
	return ""
}

// csharpUsingBoundName resolves a using_directive to the locally-
// scoped name. `using System.Collections.Generic;` brings everything
// under that namespace into scope; the right convention for ken's
// fs.Imports is to bind the rightmost identifier (the namespace's
// terminal segment), matching the Java + PHP precedent.
//
// `using static System.Console;` binds the static-imported type
// (the terminal Console); we treat it the same way.
//
// `using Foo = Some.Other.Type;` (alias) binds "Foo".
func csharpUsingBoundName(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language) string {
	// Alias form: `using <name> = <qualified_name>;` — `name` field
	// carries the alias.
	if alias := n.ChildByFieldName("name", lang); alias != nil && alias.Type(lang) == "identifier" {
		return nodeText(src, alias)
	}
	// Regular form: walk named children, find the qualified_name or
	// identifier, return its leaf.
	nc := n.NamedChildCount()
	for i := 0; i < nc; i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Type(lang) {
		case "identifier":
			return nodeText(src, c)
		case "qualified_name":
			return csharpTypeLeafName(src, c, lang)
		}
	}
	return ""
}

func csharpIsBuiltinOrNoise(name string) bool {
	switch name {
	// C# / .NET BCL idioms that flood the calls list without
	// carrying user-vocabulary signal. Calibrated against the
	// dogfood pass top-calls list on dapper / Newtonsoft.Json.
	case "ToString", "GetHashCode", "Equals", "GetType",
		"WriteLine", "Write", "ReadLine",
		"Add", "Remove", "Contains", "Count", "ToArray", "ToList",
		"Concat", "Select", "Where", "FirstOrDefault", "Any", "All",
		"Length", "Substring", "Trim", "Split", "Replace",
		"Parse", "TryParse", "Convert", "Format",
		"String", "Int32", "Boolean", "Object",
		"NotNull", "IsNullOrEmpty", "IsNullOrWhiteSpace":
		return true
	}
	return false
}
