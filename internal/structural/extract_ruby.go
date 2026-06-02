package structural

import (
	"github.com/odvcencio/gotreesitter"
)

// extractRuby walks a tree-sitter-ruby AST and fills FileStruct.
//
// Ruby node-type mapping (from gotreesitter v0.18.0):
//
//   - module               — `module Foo`; treated as a ClassDef
//     shell for outline purposes. Methods
//     and classes defined inside use the
//     module as their enclosingClass.
//   - class                — `class Foo`; standard ClassDef.
//   - method               — `def foo(args)`. enclosingClass is set
//     from the lexically enclosing class /
//     module / nothing (top-level method).
//   - singleton_method     — `def self.foo(args)`; class-level
//     method. EnclosingClass = current class.
//   - method_parameters    — contains `identifier` children directly
//     for positional params; also handles
//     optional_parameter / keyword_parameter
//     / hash_splat_parameter etc.
//   - call                 — Ruby's call. `method` field is the
//     called identifier; `receiver` field (if
//     present) is the object being called on.
//   - assignment / others  — recurse only.
//
// Ruby DOESN'T have:
//   - import declarations — `require 'foo'` is a regular method call.
//     We don't surface require'd files in fs.Imports for v0.
//   - throw / raise as statement — `raise` is a method call. We
//     special-case `raise X` (and `fail X`) to populate fs.Raises
//     with the raised class name.
func extractRuby(src []byte, root *gotreesitter.Node, lang *gotreesitter.Language, fs *FileStruct) {
	walkRuby(src, root, lang, "", fs)
}

func walkRuby(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, enclosingClass string, fs *FileStruct) {
	if n == nil {
		return
	}
	switch n.Type(lang) {
	case "module", "class":
		cls := ClassDef{}
		if name := n.ChildByFieldName("name", lang); name != nil {
			cls.Name = nodeText(src, name)
		}
		fs.Classes = append(fs.Classes, cls)
		// Recurse into body_statement (which is the second
		// named child in most cases) with enclosingClass set.
		// Ruby's grammar uses a `body` field name in some
		// versions; fall back to walking children.
		body := n.ChildByFieldName("body", lang)
		if body != nil {
			recurseChildrenRuby(src, body, lang, cls.Name, fs)
		} else {
			nc := n.NamedChildCount()
			for i := 0; i < nc; i++ {
				c := n.NamedChild(i)
				if c == nil {
					continue
				}
				if c.Type(lang) == "body_statement" {
					recurseChildrenRuby(src, c, lang, cls.Name, fs)
				}
			}
		}
		// Also walk top-level methods of this class via direct
		// child iteration in case the body lives outside a
		// body_statement wrapper.
	case "method":
		fn := extractRubyMethod(src, n, lang, enclosingClass, false)
		fs.Functions = append(fs.Functions, fn)
		if body := n.ChildByFieldName("body", lang); body != nil {
			recurseChildrenRuby(src, body, lang, enclosingClass, fs)
		} else {
			// Fallback: walk children skipping name/parameters.
			nc := n.NamedChildCount()
			for i := 0; i < nc; i++ {
				c := n.NamedChild(i)
				if c == nil {
					continue
				}
				if c.Type(lang) == "body_statement" {
					recurseChildrenRuby(src, c, lang, enclosingClass, fs)
				}
			}
		}
	case "singleton_method":
		// `def self.foo(...)` (or `def Class.foo(...)`). The
		// receiver field carries the receiver (self / Class).
		// When inside a class, treat as a class-method of that
		// class; at top-level treat as a class-method of the
		// named receiver if it's a constant.
		recv := enclosingClass
		if obj := n.ChildByFieldName("object", lang); obj != nil {
			if obj.Type(lang) == "constant" {
				// `def Foo.bar(...)` — explicit receiver.
				recv = nodeText(src, obj)
			}
			// `self` keeps recv = enclosingClass.
		}
		fn := extractRubyMethod(src, n, lang, recv, true)
		fs.Functions = append(fs.Functions, fn)
		if body := n.ChildByFieldName("body", lang); body != nil {
			recurseChildrenRuby(src, body, lang, recv, fs)
		} else {
			nc := n.NamedChildCount()
			for i := 0; i < nc; i++ {
				c := n.NamedChild(i)
				if c == nil {
					continue
				}
				if c.Type(lang) == "body_statement" {
					recurseChildrenRuby(src, c, lang, recv, fs)
				}
			}
		}
	case "call":
		methodName := ""
		if m := n.ChildByFieldName("method", lang); m != nil {
			methodName = nodeText(src, m)
		}
		if methodName != "" {
			// `raise Foo` / `fail Foo` populate fs.Raises with
			// the first argument's class name (when it's a
			// constant). Other calls go to fs.Calls.
			if methodName == "raise" || methodName == "fail" {
				if name := rubyRaiseArgName(src, n, lang); name != "" {
					fs.Raises = dedupAppend(fs.Raises, name)
				}
			} else if !rubyIsBuiltinOrNoise(methodName) {
				fs.Calls = dedupAppend(fs.Calls, methodName)
			}
		}
		recurseChildrenRuby(src, n, lang, enclosingClass, fs)
	default:
		recurseChildrenRuby(src, n, lang, enclosingClass, fs)
	}
}

func recurseChildrenRuby(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, enclosingClass string, fs *FileStruct) {
	nc := n.NamedChildCount()
	for i := 0; i < nc; i++ {
		walkRuby(src, n.NamedChild(i), lang, enclosingClass, fs)
	}
}

// extractRubyMethod handles both `method` and `singleton_method`
// shapes. Both have `name` and `parameters` fields with the same
// internal structure.
func extractRubyMethod(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, enclosingClass string, singleton bool) FuncDef {
	fn := FuncDef{
		IsMethod:       enclosingClass != "",
		EnclosingClass: enclosingClass,
	}
	if name := n.ChildByFieldName("name", lang); name != nil {
		fn.Name = nodeText(src, name)
	}
	if params := n.ChildByFieldName("parameters", lang); params != nil {
		fn.Params = extractRubyParams(src, params, lang)
	}
	return fn
}

func extractRubyParams(src []byte, params *gotreesitter.Node, lang *gotreesitter.Language) []string {
	var out []string
	pc := params.NamedChildCount()
	for i := 0; i < pc; i++ {
		c := params.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Type(lang) {
		case "identifier":
			out = append(out, nodeText(src, c))
		case "optional_parameter", "keyword_parameter", "splat_parameter", "hash_splat_parameter", "block_parameter":
			// Drill to the name field (identifier).
			if name := c.ChildByFieldName("name", lang); name != nil {
				out = append(out, nodeText(src, name))
				continue
			}
			// Fallback: first identifier child.
			cc := c.NamedChildCount()
			for j := 0; j < cc; j++ {
				inner := c.NamedChild(j)
				if inner != nil && inner.Type(lang) == "identifier" {
					out = append(out, nodeText(src, inner))
					break
				}
			}
		}
	}
	return out
}

// rubyRaiseArgName resolves the FIRST argument of a `raise`/`fail`
// call to its class-name leaf. e.g. `raise AuthError` → "AuthError",
// `raise AuthError.new("denied")` → "AuthError",
// `raise AuthError, "msg"` → "AuthError".
func rubyRaiseArgName(src []byte, callNode *gotreesitter.Node, lang *gotreesitter.Language) string {
	args := callNode.ChildByFieldName("arguments", lang)
	if args == nil {
		return ""
	}
	if args.NamedChildCount() == 0 {
		return ""
	}
	first := args.NamedChild(0)
	if first == nil {
		return ""
	}
	switch first.Type(lang) {
	case "constant":
		return nodeText(src, first)
	case "call":
		// `raise AuthError.new(...)` — the receiver IS the
		// class name (a constant).
		if recv := first.ChildByFieldName("receiver", lang); recv != nil {
			if recv.Type(lang) == "constant" {
				return nodeText(src, recv)
			}
		}
	case "scope_resolution":
		// `Errors::AuthError` — the rightmost name child IS
		// the bound class name. Drill through.
		if name := first.ChildByFieldName("name", lang); name != nil {
			return nodeText(src, name)
		}
	}
	return ""
}

func rubyIsBuiltinOrNoise(name string) bool {
	switch name {
	// Ruby core / kernel methods called constantly in real
	// programs.
	case "puts", "print", "p", "pp", "require", "require_relative", "load",
		"attr_accessor", "attr_reader", "attr_writer",
		"new", "to_s", "to_str", "to_i", "to_a", "to_h",
		"each", "map", "select", "reject", "reduce", "inject",
		"size", "length", "empty", "include",
		"send", "respond_to", "instance_of", "kind_of", "is_a",
		"add", "delete", "push", "pop", "shift", "unshift":
		return true
	}
	return false
}
