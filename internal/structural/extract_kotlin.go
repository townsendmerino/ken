package structural

import (
	"strings"

	"github.com/odvcencio/gotreesitter"
)

// extractKotlin walks a tree-sitter-kotlin AST and fills FileStruct.
// Same FileStruct contract as the other extractors.
//
// Kotlin node-type mapping (gotreesitter v0.20.0-rc3 / tree-sitter-
// kotlin). Note: this grammar does not expose named fields on most
// nodes (FieldNameForChild returns ""), so the walker uses positional
// + Type() access — the same fallback pattern documented in
// docs/add-a-language.md and used by extract_rust.go.
//
//   - function_declaration   — fun foo(...) {...}. First named child
//     of Type "simple_identifier" is the
//     name. Parameters live in a
//     "function_value_parameters" child;
//     each "parameter" has a leading
//     simple_identifier (the param name) and
//     a "user_type" (annotation).
//   - class_declaration      — Kotlin lumps `class`, `interface`, and
//     `data class` here. First "type_identifier"
//     named child is the name; body is in
//     "class_body".
//   - object_declaration     — singletons (`object Foo {...}`); shape
//     matches class_declaration.
//   - primary_constructor    — `(p: T, ...)` next to class name;
//     children are "class_parameter"s whose
//     first simple_identifier is the param.
//   - call_expression        — Kotlin's call node. First named child
//     is either "simple_identifier" (bare
//     call like `foo(x)`) or
//     "navigation_expression" (dotted call
//     like `a.b.foo(x)`).
//   - navigation_expression  — `a.b.c`; rightmost identifier is the
//     call name.
//   - jump_expression        — covers BOTH `return ...` AND
//     `throw ...`. Discriminate by the
//     source-text prefix; throw's first
//     named child is the thrown expression.
//   - import_header          — `import a.b.C`; the contained
//     "identifier" is dotted; rightmost
//     "simple_identifier" is the bound name.
//   - package_header         — skipped (not a binding).
func extractKotlin(src []byte, root *gotreesitter.Node, lang *gotreesitter.Language, fs *FileStruct) {
	walkKotlin(src, root, lang, "", "", fs)
}

func walkKotlin(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, enclosingClass, enclosingSymbol string, fs *FileStruct) {
	if n == nil {
		return
	}
	switch n.Type(lang) {
	case "function_declaration":
		fn := extractKotlinFunc(src, n, lang, enclosingClass)
		fn.fillSpan(n)
		fs.Functions = append(fs.Functions, fn)
		// Recurse into body for nested calls/throws with this
		// function as the enclosing symbol.
		sym := qualifySymbol(enclosingClass, fn.Name)
		if body := firstNamedChildOfType(n, lang, "function_body"); body != nil {
			recurseChildrenKotlin(src, body, lang, enclosingClass, sym, fs)
		}
	case "class_declaration", "object_declaration":
		cls := extractKotlinClass(src, n, lang)
		cls.fillSpan(n)
		fs.Classes = append(fs.Classes, cls)
		if body := firstNamedChildOfType(n, lang, "class_body"); body != nil {
			recurseChildrenKotlin(src, body, lang, cls.Name, enclosingSymbol, fs)
		}
	case "call_expression":
		if name := kotlinCalleeName(src, n, lang); name != "" && !kotlinIsBuiltinOrNoise(name) {
			fs.appendCall(name, "", n, enclosingSymbol)
		}
		recurseChildrenKotlin(src, n, lang, enclosingClass, enclosingSymbol, fs)
	case "jump_expression":
		if strings.HasPrefix(strings.TrimSpace(nodeText(src, n)), "throw") {
			if name := kotlinThrowName(src, n, lang); name != "" {
				fs.Raises = dedupAppend(fs.Raises, name)
			}
		}
		recurseChildrenKotlin(src, n, lang, enclosingClass, enclosingSymbol, fs)
	case "import_header":
		if name := kotlinImportBoundName(src, n, lang); name != "" {
			fs.Imports = dedupAppend(fs.Imports, name)
		}
	case "package_header":
		return
	default:
		recurseChildrenKotlin(src, n, lang, enclosingClass, enclosingSymbol, fs)
	}
}

func recurseChildrenKotlin(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, enclosingClass, enclosingSymbol string, fs *FileStruct) {
	nc := n.NamedChildCount()
	for i := range nc {
		walkKotlin(src, n.NamedChild(i), lang, enclosingClass, enclosingSymbol, fs)
	}
}

func extractKotlinFunc(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, enclosingClass string) FuncDef {
	fn := FuncDef{
		IsMethod:       enclosingClass != "",
		EnclosingClass: enclosingClass,
	}
	// Name is the first simple_identifier (no field name on this
	// grammar).
	if id := firstNamedChildOfType(n, lang, "simple_identifier"); id != nil {
		fn.Name = nodeText(src, id)
	}
	if params := firstNamedChildOfType(n, lang, "function_value_parameters"); params != nil {
		fn.Params = extractKotlinParams(src, params, lang)
	}
	// Return type is a "user_type" sibling between params and body.
	if ut := firstNamedChildOfType(n, lang, "user_type"); ut != nil {
		fn.ReturnType = nodeText(src, ut)
	}
	return fn
}

func extractKotlinParams(src []byte, params *gotreesitter.Node, lang *gotreesitter.Language) []string {
	var out []string
	pc := params.NamedChildCount()
	for i := range pc {
		c := params.NamedChild(i)
		if c == nil || c.Type(lang) != "parameter" {
			continue
		}
		// Each parameter's first simple_identifier is the bound name.
		if id := firstNamedChildOfType(c, lang, "simple_identifier"); id != nil {
			if s := nodeText(src, id); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

func extractKotlinClass(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language) ClassDef {
	cls := ClassDef{}
	if id := firstNamedChildOfType(n, lang, "type_identifier"); id != nil {
		cls.Name = nodeText(src, id)
	}
	body := firstNamedChildOfType(n, lang, "class_body")
	if body == nil {
		return cls
	}
	bc := body.NamedChildCount()
	for i := range bc {
		c := body.NamedChild(i)
		if c == nil {
			continue
		}
		if c.Type(lang) == "function_declaration" {
			cls.Methods = append(cls.Methods, extractKotlinFunc(src, c, lang, cls.Name))
		}
	}
	return cls
}

// kotlinCalleeName returns the called function/method name from a
// call_expression. Handles both bare calls (`foo(x)`) and
// navigation calls (`a.b.foo(x)`).
func kotlinCalleeName(src []byte, callExpr *gotreesitter.Node, lang *gotreesitter.Language) string {
	if callExpr == nil {
		return ""
	}
	// First named child is the callee.
	if callExpr.NamedChildCount() == 0 {
		return ""
	}
	callee := callExpr.NamedChild(0)
	if callee == nil {
		return ""
	}
	switch callee.Type(lang) {
	case "simple_identifier":
		return nodeText(src, callee)
	case "navigation_expression":
		return kotlinNavigationTail(src, callee, lang)
	}
	return ""
}

// kotlinNavigationTail returns the rightmost simple_identifier in a
// navigation_expression chain. For `a.b.c.foo` it returns "foo".
func kotlinNavigationTail(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language) string {
	if n == nil {
		return ""
	}
	// navigation_expression has children { target, navigation_suffix }.
	// The navigation_suffix's first simple_identifier is the rightmost
	// name in the chain.
	nc := n.NamedChildCount()
	for i := nc - 1; i >= 0; i-- {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		if c.Type(lang) == "navigation_suffix" {
			if id := firstNamedChildOfType(c, lang, "simple_identifier"); id != nil {
				return nodeText(src, id)
			}
		}
	}
	return ""
}

// kotlinThrowName returns the name of the thing being thrown — the
// constructor of `throw FooException(...)` or the identifier of
// `throw e`.
func kotlinThrowName(src []byte, throwNode *gotreesitter.Node, lang *gotreesitter.Language) string {
	if throwNode == nil || throwNode.NamedChildCount() == 0 {
		return ""
	}
	expr := throwNode.NamedChild(0)
	if expr == nil {
		return ""
	}
	switch expr.Type(lang) {
	case "simple_identifier":
		return nodeText(src, expr)
	case "call_expression":
		return kotlinCalleeName(src, expr, lang)
	case "navigation_expression":
		return kotlinNavigationTail(src, expr, lang)
	}
	return ""
}

// kotlinImportBoundName returns the rightmost simple_identifier of
// an import_header — the type/name that's now in scope.
func kotlinImportBoundName(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language) string {
	// import_header > identifier > simple_identifier... (chained).
	id := firstNamedChildOfType(n, lang, "identifier")
	if id == nil {
		// Fallback: maybe a bare simple_identifier
		if si := firstNamedChildOfType(n, lang, "simple_identifier"); si != nil {
			return nodeText(src, si)
		}
		return ""
	}
	nc := id.NamedChildCount()
	for i := nc - 1; i >= 0; i-- {
		c := id.NamedChild(i)
		if c != nil && c.Type(lang) == "simple_identifier" {
			return nodeText(src, c)
		}
	}
	// Some files use a bare identifier with no children.
	return nodeText(src, id)
}

func kotlinIsBuiltinOrNoise(name string) bool {
	switch name {
	// Stdlib / collection ops that ride into every Kotlin file;
	// filtering keeps fs.Calls focused on user-vocabulary names.
	// Same spirit as javaIsBuiltinOrNoise.
	case "println", "print",
		"toString", "hashCode", "equals",
		"size", "length", "isEmpty", "isNotEmpty",
		"get", "set", "put", "add", "remove",
		"first", "last", "lastIndex",
		"valueOf", "toInt", "toLong", "toDouble", "toFloat",
		"Int", "Long", "Double", "Float", "Boolean", "String",
		"Any", "Unit", "Nothing",
		"List", "MutableList", "Map", "MutableMap", "Set", "MutableSet",
		"listOf", "mapOf", "setOf",
		"let", "apply", "run", "with", "also":
		return true
	}
	return false
}

// firstNamedChildOfType returns the first named child whose Type ==
// want, or nil if none. Used by both kotlin and swift extractors
// when the grammar doesn't expose useful field names.
func firstNamedChildOfType(n *gotreesitter.Node, lang *gotreesitter.Language, want string) *gotreesitter.Node {
	if n == nil {
		return nil
	}
	nc := n.NamedChildCount()
	for i := range nc {
		c := n.NamedChild(i)
		if c != nil && c.Type(lang) == want {
			return c
		}
	}
	return nil
}
