package structural

import (
	"strings"

	"github.com/odvcencio/gotreesitter"
)

// extractCpp walks a tree-sitter-cpp AST and fills FileStruct.
//
// C++ structural shape (gotreesitter v0.18.0):
//
//   - function_definition / declaration
//     └─ declarator: function_declarator
//     └─ declarator: identifier OR qualified_identifier
//     (the function name; qualified form is
//     out-of-line `Class::method` definitions)
//     └─ parameters: parameter_list
//     └─ parameter_declaration
//     (with type + declarator
//     fields — declarator may be
//     a reference/pointer/array
//     wrapper around the
//     identifier)
//
//   - class_specifier / struct_specifier
//     └─ name: type_identifier
//     └─ body: field_declaration_list
//     └─ access_specifier / function_definition /
//     field_declaration (for forward-declared methods)
//
//   - namespace_definition — recurse without changing enclosingClass
//     (namespaces shape symbols but methods
//     live on classes, not namespaces).
//
//   - call_expression with `function` field (identifier,
//     field_expression `obj.foo`, or qualified_identifier `Foo::bar`)
//
//   - throw_expression — analog of raises.
//
//   - preproc_include — SKIPPED in v0. Header files don't introduce
//     a single bound name in the way Python/JS/TS imports do.
//
// Out-of-line definitions: `bool Foo::login(...) { ... }` — the
// function_declarator's declarator is a qualified_identifier whose
// scope is the class name. The walker pulls that out so the function
// is recorded as a method of Foo even though it's at file scope.
func extractCpp(src []byte, root *gotreesitter.Node, lang *gotreesitter.Language, fs *FileStruct) {
	walkCpp(src, root, lang, "", "", fs)
}

func walkCpp(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, enclosingClass, enclosingSymbol string, fs *FileStruct) {
	if n == nil {
		return
	}
	switch n.Type(lang) {
	case "function_definition", "declaration", "field_declaration":
		// declaration is a top-level forward declaration;
		// field_declaration is the in-class form. Both have the
		// same `type` + `declarator` shape — we only count
		// declarations whose declarator chain ends at a
		// function_declarator (i.e. NOT plain member variables).
		fdecl := cppUnwrapDeclarator(n.ChildByFieldName("declarator", lang), lang)
		if fdecl == nil || fdecl.Type(lang) != "function_declarator" {
			// Not a function-decl shape — could be a member
			// variable or top-level variable declaration. Just
			// keep walking children for nested calls etc.
			recurseChildrenCpp(src, n, lang, enclosingClass, enclosingSymbol, fs)
			return
		}
		fn, recvFromDeclarator := extractCppFunc(src, n, lang, enclosingClass)
		if fn.Name == "" {
			recurseChildrenCpp(src, n, lang, enclosingClass, enclosingSymbol, fs)
			return
		}
		// Out-of-line method definition: declarator carried a
		// qualified_identifier with scope=ClassName.
		if recvFromDeclarator != "" {
			fn.IsMethod = true
			fn.EnclosingClass = recvFromDeclarator
		}
		fn.fillSpan(n)
		fs.Functions = append(fs.Functions, fn)
		sym := qualifySymbol(fn.EnclosingClass, fn.Name)
		if body := n.ChildByFieldName("body", lang); body != nil {
			recurseChildrenCpp(src, body, lang, fn.EnclosingClass, sym, fs)
		}
	case "class_specifier", "struct_specifier", "union_specifier":
		cls := extractCppClass(src, n, lang)
		if cls.Name != "" {
			cls.fillSpan(n)
			fs.Classes = append(fs.Classes, cls)
		}
		body := n.ChildByFieldName("body", lang)
		if body != nil {
			recurseChildrenCpp(src, body, lang, cls.Name, enclosingSymbol, fs)
		}
	case "namespace_definition":
		// Recurse without changing enclosingClass.
		body := n.ChildByFieldName("body", lang)
		if body == nil {
			// Some versions use a different field name; fall
			// back to walking all named children.
			recurseChildrenCpp(src, n, lang, enclosingClass, enclosingSymbol, fs)
			return
		}
		recurseChildrenCpp(src, body, lang, enclosingClass, enclosingSymbol, fs)
	case "call_expression":
		if fn := n.ChildByFieldName("function", lang); fn != nil {
			if name := cppCalleeName(src, fn, lang); name != "" && !cppIsBuiltinOrNoise(name) {
				fs.appendCall(name, "", n, enclosingSymbol)
			}
		}
		recurseChildrenCpp(src, n, lang, enclosingClass, enclosingSymbol, fs)
	case "throw_statement", "throw_expression":
		// throw_statement: `throw <expr>;` (statement-position).
		// throw_expression: `throw <expr>` inside an expression.
		// Both have the thrown expression as their first named
		// child.
		if name := cppThrowName(src, n, lang); name != "" {
			fs.Raises = dedupAppend(fs.Raises, name)
		}
		recurseChildrenCpp(src, n, lang, enclosingClass, enclosingSymbol, fs)
	case "preproc_include":
		// `#include <foo.h>` / `#include "bar.h"` — surface the
		// header's basename (sans extension) as an import. C/C++
		// have no module-name binding the way Python or JS do;
		// the file basename is the closest analog and is what an
		// agent searching "where is foo.h used" would type.
		if name := cppIncludeBoundName(src, n, lang); name != "" {
			fs.Imports = dedupAppend(fs.Imports, name)
		}
		return
	default:
		recurseChildrenCpp(src, n, lang, enclosingClass, enclosingSymbol, fs)
	}
}

func recurseChildrenCpp(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, enclosingClass, enclosingSymbol string, fs *FileStruct) {
	nc := n.NamedChildCount()
	for i := range nc {
		walkCpp(src, n.NamedChild(i), lang, enclosingClass, enclosingSymbol, fs)
	}
}

// extractCppFunc returns a FuncDef + the receiver class name if the
// function_declarator's declarator was a qualified_identifier (i.e.
// an out-of-line method definition).
func extractCppFunc(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, enclosingClass string) (FuncDef, string) {
	fn := FuncDef{
		IsMethod:       enclosingClass != "",
		EnclosingClass: enclosingClass,
	}
	if ret := n.ChildByFieldName("type", lang); ret != nil {
		fn.ReturnType = nodeText(src, ret)
	}
	declarator := n.ChildByFieldName("declarator", lang)
	if declarator == nil {
		return fn, ""
	}
	fdecl := cppUnwrapDeclarator(declarator, lang)
	if fdecl == nil || fdecl.Type(lang) != "function_declarator" {
		return fn, ""
	}
	inner := fdecl.ChildByFieldName("declarator", lang)
	recvType := ""
	if inner != nil {
		switch inner.Type(lang) {
		case "identifier", "field_identifier":
			fn.Name = nodeText(src, inner)
		case "qualified_identifier":
			// Out-of-line method def. Scope = class name; the
			// `name` field is the method identifier.
			if scope := inner.ChildByFieldName("scope", lang); scope != nil {
				recvType = cppScopeLeaf(src, scope, lang)
			}
			if name := inner.ChildByFieldName("name", lang); name != nil {
				fn.Name = nodeText(src, name)
			}
		case "destructor_name":
			// `~Foo` — destructor; record as `~Foo`. Useful so
			// `definition("~Foo")` works.
			fn.Name = nodeText(src, inner)
		case "operator_name":
			fn.Name = nodeText(src, inner)
		}
	}
	if params := fdecl.ChildByFieldName("parameters", lang); params != nil {
		fn.Params = extractCppParams(src, params, lang)
	}
	return fn, recvType
}

// cppUnwrapDeclarator drills past wrappers (pointer_declarator,
// reference_declarator, etc.) to the inner function_declarator. If
// the input is already a function_declarator it's returned as-is.
func cppUnwrapDeclarator(n *gotreesitter.Node, lang *gotreesitter.Language) *gotreesitter.Node {
	for n != nil {
		switch n.Type(lang) {
		case "function_declarator":
			return n
		case "pointer_declarator", "reference_declarator", "parenthesized_declarator":
			n = n.ChildByFieldName("declarator", lang)
		default:
			return n
		}
	}
	return nil
}

func extractCppParams(src []byte, params *gotreesitter.Node, lang *gotreesitter.Language) []string {
	var out []string
	pc := params.NamedChildCount()
	for i := range pc {
		c := params.NamedChild(i)
		if c == nil {
			continue
		}
		if c.Type(lang) != "parameter_declaration" {
			continue
		}
		// declarator field carries the bound name, possibly
		// wrapped in reference/pointer declarators. Unwrap to
		// the leaf identifier.
		decl := c.ChildByFieldName("declarator", lang)
		if decl == nil {
			continue
		}
		if name := cppDeclaratorLeafName(src, decl, lang); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// cppDeclaratorLeafName drills past pointer/reference wrappers to
// the leaf identifier. For arrays / function pointers we return "" —
// no single bound name to capture.
func cppDeclaratorLeafName(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language) string {
	for n != nil {
		switch n.Type(lang) {
		case "identifier", "field_identifier":
			return nodeText(src, n)
		case "pointer_declarator", "reference_declarator", "parenthesized_declarator", "init_declarator":
			n = n.ChildByFieldName("declarator", lang)
		case "abstract_pointer_declarator", "abstract_reference_declarator":
			// e.g. `(const User&)` with no param name — common
			// in pure-virtual signatures.
			return ""
		default:
			return ""
		}
	}
	return ""
}

func extractCppClass(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language) ClassDef {
	cls := ClassDef{}
	if name := n.ChildByFieldName("name", lang); name != nil {
		cls.Name = nodeText(src, name)
	}
	body := n.ChildByFieldName("body", lang)
	if body == nil {
		return cls
	}
	bc := body.NamedChildCount()
	for i := range bc {
		c := body.NamedChild(i)
		if c == nil {
			continue
		}
		if c.Type(lang) != "function_definition" && c.Type(lang) != "field_declaration" {
			continue
		}
		// field_declaration is BOTH the forward-declared-method
		// shape AND the member-variable shape. Distinguish by
		// checking the declarator chain — a function_declarator
		// at the leaf means it's a method.
		fdecl := cppUnwrapDeclarator(c.ChildByFieldName("declarator", lang), lang)
		if fdecl == nil || fdecl.Type(lang) != "function_declarator" {
			continue
		}
		fn, _ := extractCppFunc(src, c, lang, cls.Name)
		if fn.Name == "" {
			continue
		}
		cls.Methods = append(cls.Methods, fn)
	}
	return cls
}

func cppCalleeName(src []byte, fn *gotreesitter.Node, lang *gotreesitter.Language) string {
	if fn == nil {
		return ""
	}
	switch fn.Type(lang) {
	case "identifier", "field_identifier":
		return nodeText(src, fn)
	case "field_expression":
		// `x.foo` / `active_.push_back` — `field` is the
		// rightmost name being called.
		if f := fn.ChildByFieldName("field", lang); f != nil {
			return nodeText(src, f)
		}
	case "qualified_identifier":
		// `Foo::bar` / `std::vector<T>::push_back` — `name`
		// field is the rightmost identifier.
		if name := fn.ChildByFieldName("name", lang); name != nil {
			return cppCalleeName(src, name, lang)
		}
	case "template_function":
		// `foo<T>` — name field is the function identifier.
		if name := fn.ChildByFieldName("name", lang); name != nil {
			return nodeText(src, name)
		}
	}
	return ""
}

func cppThrowName(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language) string {
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
	case "call_expression":
		if fn := expr.ChildByFieldName("function", lang); fn != nil {
			return cppCalleeName(src, fn, lang)
		}
	}
	return ""
}

// cppScopeLeaf walks a scope node (namespace_identifier,
// type_identifier, or nested qualified_identifier) and returns the
// rightmost identifier — useful for resolving an out-of-line method
// def's enclosing class.
func cppScopeLeaf(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language) string {
	if n == nil {
		return ""
	}
	switch n.Type(lang) {
	case "namespace_identifier", "type_identifier", "identifier":
		return nodeText(src, n)
	case "qualified_identifier":
		// Nested scope `A::B::method` — recurse on the name
		// field (the rightmost component below this scope).
		if name := n.ChildByFieldName("name", lang); name != nil {
			return cppScopeLeaf(src, name, lang)
		}
	}
	return ""
}

// cppIncludeBoundName resolves a preproc_include node to the
// agent-searchable name. Strips wrapping <>/"" + directory prefix +
// extension, so `#include <std/vector.h>` → "vector",
// `#include "redis/foo.h"` → "foo".
func cppIncludeBoundName(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language) string {
	nc := n.NamedChildCount()
	for i := range nc {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Type(lang) {
		case "system_lib_string":
			// Raw text is "<foo.h>" or "<foo/bar.h>".
			t := nodeText(src, c)
			t = strings.TrimPrefix(t, "<")
			t = strings.TrimSuffix(t, ">")
			return includeBasename(t)
		case "string_literal":
			// Quoted form `#include "foo.h"`. Drill to
			// string_content for the bare path; fall back to
			// stripping quotes from the raw text.
			cc := c.NamedChildCount()
			for j := range cc {
				inner := c.NamedChild(j)
				if inner != nil && inner.Type(lang) == "string_content" {
					return includeBasename(nodeText(src, inner))
				}
			}
			t := nodeText(src, c)
			t = strings.Trim(t, `"`)
			return includeBasename(t)
		}
	}
	return ""
}

// includeBasename strips the directory prefix and file extension
// from an #include path. `redis/foo.h` → `foo`.
func includeBasename(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		path = path[i+1:]
	}
	if i := strings.LastIndex(path, "."); i >= 0 {
		path = path[:i]
	}
	return path
}

func cppIsBuiltinOrNoise(name string) bool {
	switch name {
	// STL builtins / common idioms — every C++ program uses them.
	case "size", "length", "empty", "begin", "end",
		"push_back", "pop_back", "emplace_back", "insert", "erase",
		"front", "back", "at", "data",
		"c_str", "string", "to_string",
		"make_unique", "make_shared", "move", "forward",
		// C++ casts (modeled as call_expression by the grammar
		// even though they're keywords) + assertion macros that
		// reach the AST as ordinary calls. Dogfood-surfaced from
		// google/leveldb.
		"static_cast", "dynamic_cast", "reinterpret_cast", "const_cast",
		"assert", "static_assert",
		"ASSERT_EQ", "ASSERT_NE", "ASSERT_TRUE", "ASSERT_FALSE",
		"ASSERT_GT", "ASSERT_LT", "ASSERT_GE", "ASSERT_LE",
		"EXPECT_EQ", "EXPECT_NE", "EXPECT_TRUE", "EXPECT_FALSE",
		// C stdlib basics — shared with extractC (same walker)
		// because the names below are universal in C programs and
		// add no user-vocabulary signal.
		"printf", "fprintf", "sprintf", "snprintf", "puts", "fputs",
		"malloc", "calloc", "realloc", "free",
		"memcpy", "memset", "memmove", "memcmp",
		"strcpy", "strncpy", "strcmp", "strncmp", "strlen", "strcat":
		return true
	}
	return false
}
