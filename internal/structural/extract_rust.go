package structural

import (
	"github.com/odvcencio/gotreesitter"
)

// extractRust walks a tree-sitter-rust AST and fills FileStruct.
//
// Rust node-type mapping (from gotreesitter v0.18.0):
//
//   - function_item              — `fn foo(...)`. Top-level is a
//     free function; inside an
//     impl_item it's a method (the
//     walker threads the type name as
//     enclosingClass).
//   - function_signature_item    — trait method signature (no body).
//   - struct_item                — `struct Foo {...}` / `struct Foo;`
//     / tuple struct (`struct Foo(...)`)
//   - enum_item                  — `enum Foo {...}` (treated as
//     ClassDef; variants not extracted
//     in v0)
//   - trait_item                 — `trait Foo {...}` (ClassDef +
//     method signature children)
//   - impl_item                  — `impl Foo {...}` /
//     `impl Trait for Foo {...}`.
//     The `type` field carries the
//     *receiver* type; functions
//     defined inside are recorded as
//     methods of that type.
//   - call_expression            — Rust's call node. Function side
//     can be identifier (`f()`),
//     field_expression (`x.foo()`),
//     or scoped_identifier
//     (`Foo::bar()`).
//   - macro_invocation           — `vec![...]` / `println!(...)`.
//     Captured as a call on the macro
//     name (no trailing `!`).
//   - use_declaration            — `use a::b::c;` / `use a as b;` /
//     `use a::{b, c};`. Bound name is
//     the rightmost identifier
//     introduced into local scope.
func extractRust(src []byte, root *gotreesitter.Node, lang *gotreesitter.Language, fs *FileStruct) {
	walkRust(src, root, lang, "", fs)
}

func walkRust(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, enclosingClass string, fs *FileStruct) {
	if n == nil {
		return
	}
	switch n.Type(lang) {
	case "function_item", "function_signature_item":
		fn := extractRustFunc(src, n, lang, enclosingClass)
		fs.Functions = append(fs.Functions, fn)
		if body := n.ChildByFieldName("body", lang); body != nil {
			recurseChildrenRust(src, body, lang, enclosingClass, fs)
		}
	case "impl_item":
		// `impl Foo` or `impl Trait for Foo`. The `type` field
		// is the receiver — that's the enclosingClass for any
		// function_item children.
		recvName := ""
		if tnode := n.ChildByFieldName("type", lang); tnode != nil {
			recvName = rustTypeLeafName(src, tnode, lang)
		}
		// `impl Trait for Foo` — `trait` field carries Trait,
		// `type` carries Foo. We already grabbed `type`; that's
		// correct for method-receiver scoping.
		if body := n.ChildByFieldName("body", lang); body != nil {
			recurseChildrenRust(src, body, lang, recvName, fs)
		} else {
			// fallback: walk all children
			nc := n.NamedChildCount()
			for i := 0; i < nc; i++ {
				c := n.NamedChild(i)
				if c == nil {
					continue
				}
				if c.Type(lang) == "declaration_list" {
					recurseChildrenRust(src, c, lang, recvName, fs)
				}
			}
		}
	case "struct_item", "enum_item", "union_item":
		cls := ClassDef{}
		if name := n.ChildByFieldName("name", lang); name != nil {
			cls.Name = nodeText(src, name)
		}
		// Methods on a struct/enum live in a separate impl_item,
		// not inside the struct/enum body. So cls.Methods is
		// empty here — methods land via impl_item recursion above.
		fs.Classes = append(fs.Classes, cls)
	case "trait_item":
		cls := ClassDef{}
		if name := n.ChildByFieldName("name", lang); name != nil {
			cls.Name = nodeText(src, name)
		}
		// Trait method signatures (function_signature_item):
		// collected as methods of the trait so Outline shows
		// the trait surface.
		if body := n.ChildByFieldName("body", lang); body != nil {
			bc := body.NamedChildCount()
			for i := 0; i < bc; i++ {
				c := body.NamedChild(i)
				if c == nil {
					continue
				}
				if c.Type(lang) == "function_signature_item" || c.Type(lang) == "function_item" {
					cls.Methods = append(cls.Methods, extractRustFunc(src, c, lang, cls.Name))
				}
			}
		}
		fs.Classes = append(fs.Classes, cls)
		// Also recurse so the trait body's signatures land in
		// fs.Functions as defs.
		if body := n.ChildByFieldName("body", lang); body != nil {
			recurseChildrenRust(src, body, lang, cls.Name, fs)
		}
	case "call_expression":
		if fn := n.ChildByFieldName("function", lang); fn != nil {
			if name := rustCalleeName(src, fn, lang); name != "" && !rustIsBuiltinOrNoise(name) {
				fs.Calls = dedupAppend(fs.Calls, name)
			}
		}
		recurseChildrenRust(src, n, lang, enclosingClass, fs)
	case "macro_invocation":
		// `println!(...)`, `vec![...]`. The macro is named via
		// the `macro` field (identifier or scoped_identifier).
		if macroNode := n.ChildByFieldName("macro", lang); macroNode != nil {
			if name := rustCalleeName(src, macroNode, lang); name != "" && !rustIsBuiltinOrNoise(name) {
				fs.Calls = dedupAppend(fs.Calls, name)
			}
		}
		recurseChildrenRust(src, n, lang, enclosingClass, fs)
	case "use_declaration":
		for _, bound := range rustUseBoundNames(src, n, lang) {
			fs.Imports = dedupAppend(fs.Imports, bound)
		}
	default:
		recurseChildrenRust(src, n, lang, enclosingClass, fs)
	}
}

func recurseChildrenRust(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, enclosingClass string, fs *FileStruct) {
	nc := n.NamedChildCount()
	for i := 0; i < nc; i++ {
		walkRust(src, n.NamedChild(i), lang, enclosingClass, fs)
	}
}

func extractRustFunc(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, enclosingClass string) FuncDef {
	fn := FuncDef{
		IsMethod:       enclosingClass != "",
		EnclosingClass: enclosingClass,
	}
	if name := n.ChildByFieldName("name", lang); name != nil {
		fn.Name = nodeText(src, name)
	}
	if params := n.ChildByFieldName("parameters", lang); params != nil {
		fn.Params = extractRustParams(src, params, lang)
	}
	if ret := n.ChildByFieldName("return_type", lang); ret != nil {
		fn.ReturnType = nodeText(src, ret)
	}
	return fn
}

func extractRustParams(src []byte, params *gotreesitter.Node, lang *gotreesitter.Language) []string {
	var out []string
	pc := params.NamedChildCount()
	for i := 0; i < pc; i++ {
		c := params.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Type(lang) {
		case "parameter":
			// pattern field is usually an identifier; can also
			// be destructuring patterns (skipped for v0).
			if pat := c.ChildByFieldName("pattern", lang); pat != nil {
				if pat.Type(lang) == "identifier" {
					out = append(out, nodeText(src, pat))
				}
			}
		case "self_parameter":
			// Skip — `&self` / `&mut self` is not user vocab.
		}
	}
	return out
}

// rustTypeLeafName resolves a Rust type node (type_identifier,
// generic_type, reference_type, scoped_type_identifier) to its
// rightmost identifier — the type name an agent would search for.
func rustTypeLeafName(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language) string {
	if n == nil {
		return ""
	}
	switch n.Type(lang) {
	case "type_identifier", "identifier":
		return nodeText(src, n)
	case "generic_type":
		if t := n.ChildByFieldName("type", lang); t != nil {
			return rustTypeLeafName(src, t, lang)
		}
	case "reference_type":
		if nc := n.NamedChildCount(); nc > 0 {
			// Children are [optional lifetime, optional 'mut',
			// type]. The last named child is the type.
			return rustTypeLeafName(src, n.NamedChild(nc-1), lang)
		}
	case "scoped_type_identifier", "scoped_identifier":
		if name := n.ChildByFieldName("name", lang); name != nil {
			return nodeText(src, name)
		}
		// Fallback: rightmost identifier child.
		nc := n.NamedChildCount()
		for i := nc - 1; i >= 0; i-- {
			c := n.NamedChild(i)
			if c == nil {
				continue
			}
			if t := c.Type(lang); t == "identifier" || t == "type_identifier" {
				return nodeText(src, c)
			}
		}
	}
	return ""
}

func rustCalleeName(src []byte, fn *gotreesitter.Node, lang *gotreesitter.Language) string {
	if fn == nil {
		return ""
	}
	switch fn.Type(lang) {
	case "identifier":
		return nodeText(src, fn)
	case "field_expression":
		// `x.foo` / `self.active.insert` — the `field` field
		// carries the rightmost name being called.
		if f := fn.ChildByFieldName("field", lang); f != nil {
			return nodeText(src, f)
		}
	case "scoped_identifier":
		// `Foo::bar` / `std::vec::Vec::new` — rightmost ident.
		if name := fn.ChildByFieldName("name", lang); name != nil {
			return nodeText(src, name)
		}
		nc := fn.NamedChildCount()
		for i := nc - 1; i >= 0; i-- {
			c := fn.NamedChild(i)
			if c != nil && c.Type(lang) == "identifier" {
				return nodeText(src, c)
			}
		}
	}
	return ""
}

// rustUseBoundNames returns the locally-bound names from a use
// declaration. Handles:
//
//   - `use a::b::c;`                → ["c"]
//   - `use a::b as c;`              → ["c"]
//   - `use a::{b, c, d as e};`      → ["b", "c", "e"]
//   - `use a::*;`                   → []  (glob, no bound name)
func rustUseBoundNames(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language) []string {
	var out []string
	nc := n.NamedChildCount()
	for i := 0; i < nc; i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		out = append(out, rustUseTreeBoundNames(src, c, lang)...)
	}
	return out
}

func rustUseTreeBoundNames(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language) []string {
	if n == nil {
		return nil
	}
	switch n.Type(lang) {
	case "identifier", "type_identifier":
		return []string{nodeText(src, n)}
	case "scoped_identifier":
		// Rightmost identifier IS the bound name.
		if name := n.ChildByFieldName("name", lang); name != nil {
			return []string{nodeText(src, name)}
		}
	case "use_as_clause":
		// alias field is the bound name.
		if alias := n.ChildByFieldName("alias", lang); alias != nil {
			return []string{nodeText(src, alias)}
		}
	case "use_list", "scoped_use_list":
		var out []string
		nc := n.NamedChildCount()
		for i := 0; i < nc; i++ {
			c := n.NamedChild(i)
			if c == nil {
				continue
			}
			out = append(out, rustUseTreeBoundNames(src, c, lang)...)
		}
		return out
	case "use_wildcard":
		// `use a::*` — no specific bound name; skip.
		return nil
	}
	return nil
}

func rustIsBuiltinOrNoise(name string) bool {
	switch name {
	// Rust prelude / common-call macros: every program uses these,
	// they don't carry user-vocabulary signal.
	case "println", "print", "eprintln", "eprint",
		"format", "write", "writeln", "vec", "dbg", "assert", "assert_eq", "assert_ne",
		"panic", "todo", "unimplemented", "unreachable",
		"clone", "to_string", "to_owned", "into", "as_ref", "as_str", "as_bytes",
		"len", "is_empty", "iter", "into_iter",
		"unwrap", "expect", "ok", "err", "some", "none",
		"push", "pop", "insert", "remove":
		return true
	}
	return false
}
