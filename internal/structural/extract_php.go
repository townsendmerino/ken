package structural

import (
	"github.com/odvcencio/gotreesitter"
)

// extractPhp walks a tree-sitter-php AST and fills FileStruct.
//
// PHP node-type mapping (from gotreesitter v0.18.0):
//
//   - function_definition       — top-level `function foo()`
//   - method_declaration        — inside class_declaration /
//     interface_declaration /
//     trait_declaration
//   - class_declaration         — `class Foo {}`
//   - interface_declaration     — `interface Foo {}` (treated as
//     ClassDef shell; method signatures
//     still recorded)
//   - trait_declaration         — `trait Foo {}` (same shape)
//   - simple_parameter          — has `type` + `name` fields. The
//     `name` field carries a
//     variable_name wrapping the bare
//     identifier we want for fn.Params.
//   - function_call_expression  — `foo(...)`. function field is a
//     `name` node.
//   - member_call_expression    — `$obj->foo(...)`. `name` field is
//     the called method.
//   - scoped_call_expression    — `Foo::bar(...)` / `static::baz()`.
//     `name` field is the method.
//   - object_creation_expression — `new Foo(...)`. Class field carries
//     the type name being instantiated.
//   - throw_expression          — analog of raises.
//   - namespace_use_declaration — `use App\Models\User`. Bound name
//     is the rightmost segment of the
//     qualified_name (or an `as` alias).
//
// PHP-only: identifiers in the AST use the `name` node type (not
// `identifier` like other grammars). This affects everywhere we read
// a node value as a name.
func extractPhp(src []byte, root *gotreesitter.Node, lang *gotreesitter.Language, fs *FileStruct) {
	walkPhp(src, root, lang, "", fs)
}

func walkPhp(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, enclosingClass string, fs *FileStruct) {
	if n == nil {
		return
	}
	switch n.Type(lang) {
	case "function_definition":
		fn := extractPhpFunc(src, n, lang, "")
		fs.Functions = append(fs.Functions, fn)
		if body := n.ChildByFieldName("body", lang); body != nil {
			recurseChildrenPhp(src, body, lang, enclosingClass, fs)
		}
	case "method_declaration":
		fn := extractPhpFunc(src, n, lang, enclosingClass)
		fs.Functions = append(fs.Functions, fn)
		if body := n.ChildByFieldName("body", lang); body != nil {
			recurseChildrenPhp(src, body, lang, enclosingClass, fs)
		}
	case "class_declaration", "interface_declaration", "trait_declaration":
		cls := extractPhpClass(src, n, lang)
		fs.Classes = append(fs.Classes, cls)
		body := n.ChildByFieldName("body", lang)
		if body != nil {
			recurseChildrenPhp(src, body, lang, cls.Name, fs)
		}
	case "function_call_expression":
		// `foo($args)` — function field is a `name` node.
		if fn := n.ChildByFieldName("function", lang); fn != nil {
			if name := phpCalleeName(src, fn, lang); name != "" && !phpIsBuiltinOrNoise(name) {
				fs.Calls = dedupAppend(fs.Calls, name)
			}
		}
		recurseChildrenPhp(src, n, lang, enclosingClass, fs)
	case "member_call_expression":
		// `$obj->method(...)` — `name` field is the method.
		if name := n.ChildByFieldName("name", lang); name != nil {
			if s := nodeText(src, name); s != "" && !phpIsBuiltinOrNoise(s) {
				fs.Calls = dedupAppend(fs.Calls, s)
			}
		}
		recurseChildrenPhp(src, n, lang, enclosingClass, fs)
	case "scoped_call_expression":
		// `Foo::bar(...)` — `name` field is the method.
		if name := n.ChildByFieldName("name", lang); name != nil {
			if s := nodeText(src, name); s != "" && !phpIsBuiltinOrNoise(s) {
				fs.Calls = dedupAppend(fs.Calls, s)
			}
		}
		recurseChildrenPhp(src, n, lang, enclosingClass, fs)
	case "object_creation_expression":
		// `new Foo(...)` — the type is the constructor target.
		// In tree-sitter-php this is the first named child OR a
		// field-named child depending on grammar version.
		if class := phpObjectCreationClass(n, lang); class != nil {
			if s := phpQualifiedNameLeaf(src, class, lang); s != "" && !phpIsBuiltinOrNoise(s) {
				fs.Calls = dedupAppend(fs.Calls, s)
			}
		}
		recurseChildrenPhp(src, n, lang, enclosingClass, fs)
	case "throw_expression", "throw_statement":
		if name := phpThrowName(src, n, lang); name != "" {
			fs.Raises = dedupAppend(fs.Raises, name)
		}
		recurseChildrenPhp(src, n, lang, enclosingClass, fs)
	case "namespace_use_declaration":
		for _, bound := range phpUseBoundNames(src, n, lang) {
			fs.Imports = dedupAppend(fs.Imports, bound)
		}
	case "namespace_definition":
		// File-scope namespace — recurse without changing
		// enclosingClass. Methods still bind to their class
		// inside; the namespace just shapes import resolution.
		recurseChildrenPhp(src, n, lang, enclosingClass, fs)
	default:
		recurseChildrenPhp(src, n, lang, enclosingClass, fs)
	}
}

func recurseChildrenPhp(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, enclosingClass string, fs *FileStruct) {
	nc := n.NamedChildCount()
	for i := 0; i < nc; i++ {
		walkPhp(src, n.NamedChild(i), lang, enclosingClass, fs)
	}
}

func extractPhpFunc(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, enclosingClass string) FuncDef {
	fn := FuncDef{
		IsMethod:       enclosingClass != "",
		EnclosingClass: enclosingClass,
	}
	if name := n.ChildByFieldName("name", lang); name != nil {
		fn.Name = nodeText(src, name)
	}
	if params := n.ChildByFieldName("parameters", lang); params != nil {
		fn.Params = extractPhpParams(src, params, lang)
	}
	if ret := n.ChildByFieldName("return_type", lang); ret != nil {
		fn.ReturnType = nodeText(src, ret)
	}
	return fn
}

func extractPhpParams(src []byte, params *gotreesitter.Node, lang *gotreesitter.Language) []string {
	var out []string
	pc := params.NamedChildCount()
	for i := 0; i < pc; i++ {
		c := params.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Type(lang) {
		case "simple_parameter", "property_promotion_parameter", "variadic_parameter":
			// `name` field points to a variable_name (`$x`);
			// drill to the inner `name` child for the bare ident.
			if vname := c.ChildByFieldName("name", lang); vname != nil {
				if s := phpVariableLeafName(src, vname, lang); s != "" {
					out = append(out, s)
				}
			}
		}
	}
	return out
}

// phpVariableLeafName returns the bare identifier from a
// variable_name node — strips the leading `$` by drilling into the
// inner `name` child (or falling back to text-trimming if the inner
// shape varies).
func phpVariableLeafName(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language) string {
	if n == nil {
		return ""
	}
	if n.Type(lang) == "variable_name" {
		nc := n.NamedChildCount()
		for i := 0; i < nc; i++ {
			c := n.NamedChild(i)
			if c != nil && c.Type(lang) == "name" {
				return nodeText(src, c)
			}
		}
	}
	if n.Type(lang) == "name" {
		return nodeText(src, n)
	}
	return ""
}

func extractPhpClass(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language) ClassDef {
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
		if c.Type(lang) == "method_declaration" {
			cls.Methods = append(cls.Methods, extractPhpFunc(src, c, lang, cls.Name))
		}
	}
	return cls
}

// phpCalleeName resolves a function-call function expression to its
// rightmost identifier.
func phpCalleeName(src []byte, fn *gotreesitter.Node, lang *gotreesitter.Language) string {
	if fn == nil {
		return ""
	}
	switch fn.Type(lang) {
	case "name":
		return nodeText(src, fn)
	case "qualified_name":
		return phpQualifiedNameLeaf(src, fn, lang)
	}
	return ""
}

// phpQualifiedNameLeaf returns the rightmost `name` segment of a
// qualified_name (PHP namespace separator `\`). e.g.
// `App\Models\User` → "User".
func phpQualifiedNameLeaf(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language) string {
	if n == nil {
		return ""
	}
	switch n.Type(lang) {
	case "name":
		return nodeText(src, n)
	case "qualified_name":
		nc := n.NamedChildCount()
		for i := nc - 1; i >= 0; i-- {
			c := n.NamedChild(i)
			if c != nil && c.Type(lang) == "name" {
				return nodeText(src, c)
			}
		}
	}
	return ""
}

// phpObjectCreationClass returns the class-name child of an
// object_creation_expression. Different grammar versions surface
// this either via the `class` field or as the first named child.
func phpObjectCreationClass(n *gotreesitter.Node, lang *gotreesitter.Language) *gotreesitter.Node {
	if c := n.ChildByFieldName("class", lang); c != nil {
		return c
	}
	// Fallback: first named child that's a name/qualified_name.
	nc := n.NamedChildCount()
	for i := 0; i < nc; i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		t := c.Type(lang)
		if t == "name" || t == "qualified_name" {
			return c
		}
	}
	return nil
}

func phpThrowName(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language) string {
	nc := n.NamedChildCount()
	if nc == 0 {
		return ""
	}
	expr := n.NamedChild(0)
	if expr == nil {
		return ""
	}
	switch expr.Type(lang) {
	case "name":
		return nodeText(src, expr)
	case "qualified_name":
		return phpQualifiedNameLeaf(src, expr, lang)
	case "object_creation_expression":
		if class := phpObjectCreationClass(expr, lang); class != nil {
			return phpQualifiedNameLeaf(src, class, lang)
		}
	case "function_call_expression":
		if fn := expr.ChildByFieldName("function", lang); fn != nil {
			return phpCalleeName(src, fn, lang)
		}
	}
	return ""
}

// phpUseBoundNames returns the locally-bound names from a `use`
// declaration. Handles plain, grouped, and aliased forms.
func phpUseBoundNames(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language) []string {
	var out []string
	nc := n.NamedChildCount()
	for i := 0; i < nc; i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Type(lang) {
		case "namespace_use_clause":
			// `App\Models\User` or `App\Models\User as MyUser`.
			// First child is the qualified_name; second
			// (optional) is `namespace_aliasing_clause`.
			alias := ""
			var qn *gotreesitter.Node
			cc := c.NamedChildCount()
			for j := 0; j < cc; j++ {
				inner := c.NamedChild(j)
				if inner == nil {
					continue
				}
				switch inner.Type(lang) {
				case "qualified_name", "name":
					qn = inner
				case "namespace_aliasing_clause":
					// Alias is the rightmost `name` child.
					ac := inner.NamedChildCount()
					for k := ac - 1; k >= 0; k-- {
						an := inner.NamedChild(k)
						if an != nil && an.Type(lang) == "name" {
							alias = nodeText(src, an)
							break
						}
					}
				}
			}
			if alias != "" {
				out = append(out, alias)
			} else if qn != nil {
				if s := phpQualifiedNameLeaf(src, qn, lang); s != "" {
					out = append(out, s)
				}
			}
		case "namespace_use_group":
			// `use App\Models\{User, Admin};` — recurse into
			// the group clauses.
			gc := c.NamedChildCount()
			for j := 0; j < gc; j++ {
				inner := c.NamedChild(j)
				if inner == nil {
					continue
				}
				if inner.Type(lang) == "namespace_use_clause" {
					// Same logic as above for each group member.
					ac := inner.NamedChildCount()
					var qn *gotreesitter.Node
					alias := ""
					for k := 0; k < ac; k++ {
						gi := inner.NamedChild(k)
						if gi == nil {
							continue
						}
						switch gi.Type(lang) {
						case "qualified_name", "name":
							qn = gi
						case "namespace_aliasing_clause":
							aac := gi.NamedChildCount()
							for l := aac - 1; l >= 0; l-- {
								an := gi.NamedChild(l)
								if an != nil && an.Type(lang) == "name" {
									alias = nodeText(src, an)
									break
								}
							}
						}
					}
					if alias != "" {
						out = append(out, alias)
					} else if qn != nil {
						if s := phpQualifiedNameLeaf(src, qn, lang); s != "" {
							out = append(out, s)
						}
					}
				}
			}
		}
	}
	return out
}

func phpIsBuiltinOrNoise(name string) bool {
	switch name {
	// PHP standard library — appears in almost every program.
	case "count", "array_keys", "array_values", "array_map", "array_filter",
		"implode", "explode", "strlen", "strpos", "str_replace",
		"is_array", "is_string", "is_null", "is_numeric", "isset",
		"empty", "unset", "var_dump", "print_r", "echo", "print",
		"in_array", "array_search", "sort", "asort", "ksort":
		return true
	}
	return false
}
