package structural

import (
	"github.com/odvcencio/gotreesitter"
)

// extractJava walks a tree-sitter-java AST and fills FileStruct.
// Same FileStruct contract as the other extractors.
//
// Java node-type mapping (from gotreesitter v0.18.0):
//
//   - method_declaration       — function-like definition; lives
//     inside class_body / interface_body
//     (where IsMethod=true,
//     EnclosingClass=enclosing.name) or
//     at file top-level (extraction-time
//     rare in valid Java but supported).
//   - constructor_declaration  — treated as a method named after the
//     enclosing class; useful for
//     Definition lookups like `new Foo()`.
//   - class_declaration        — Java class
//   - interface_declaration    — Java interface (treated as a class
//     shell for outline purposes; method
//     signatures are still extracted with
//     empty body)
//   - record_declaration       — Java records (Java 14+); same shape
//     as class_declaration for outline.
//   - enum_declaration         — Java enums; same shape as class.
//   - method_invocation        — Java's call node (NOT
//     call_expression). Has `name` field
//     (the called method) + optional
//     `object` field (the receiver).
//   - object_creation_expression  — `new Foo(...)`; captured as a
//     call on the type name so agents can
//     find "where is Foo instantiated".
//   - throw_statement          — Java's analog of raises; the thrown
//     object's constructor / identifier
//     lands in Raises.
//   - import_declaration       — `import java.util.List`. The bound
//     name is the LAST segment of the
//     scoped_identifier (the type name).
//     `import static`'s tail is the
//     imported member; we capture that too.
//   - package_declaration      — skipped (not a definition).
func extractJava(src []byte, root *gotreesitter.Node, lang *gotreesitter.Language, fs *FileStruct) {
	walkJava(src, root, lang, "", fs)
}

func walkJava(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, enclosingClass string, fs *FileStruct) {
	if n == nil {
		return
	}
	switch n.Type(lang) {
	case "method_declaration":
		fn := extractJavaMethod(src, n, lang, enclosingClass)
		fs.Functions = append(fs.Functions, fn)
		if body := n.ChildByFieldName("body", lang); body != nil {
			recurseChildrenJava(src, body, lang, enclosingClass, fs)
		}
	case "constructor_declaration":
		// Constructor name == enclosing class name. Treat it as
		// a method named after the class so `definition(Foo)`
		// returns both the class AND the constructor.
		fn := extractJavaMethod(src, n, lang, enclosingClass)
		if fn.Name == "" && enclosingClass != "" {
			fn.Name = enclosingClass
		}
		fs.Functions = append(fs.Functions, fn)
		if body := n.ChildByFieldName("body", lang); body != nil {
			recurseChildrenJava(src, body, lang, enclosingClass, fs)
		}
	case "class_declaration", "record_declaration", "enum_declaration":
		cls := extractJavaClass(src, n, lang)
		fs.Classes = append(fs.Classes, cls)
		body := n.ChildByFieldName("body", lang)
		if body != nil {
			recurseChildrenJava(src, body, lang, cls.Name, fs)
		}
	case "interface_declaration":
		cls := extractJavaClass(src, n, lang)
		fs.Classes = append(fs.Classes, cls)
		body := n.ChildByFieldName("body", lang)
		if body != nil {
			recurseChildrenJava(src, body, lang, cls.Name, fs)
		}
	case "method_invocation":
		if name := n.ChildByFieldName("name", lang); name != nil {
			if s := nodeText(src, name); s != "" && !javaIsBuiltinOrNoise(s) {
				fs.Calls = dedupAppend(fs.Calls, s)
			}
		}
		recurseChildrenJava(src, n, lang, enclosingClass, fs)
	case "object_creation_expression":
		// `new Foo(...)` — the type is the constructor target.
		// Resolve scoped types (e.g. `new pkg.Foo(...)`) to the
		// rightmost identifier.
		if tnode := n.ChildByFieldName("type", lang); tnode != nil {
			if s := javaTypeLeafName(src, tnode, lang); s != "" && !javaIsBuiltinOrNoise(s) {
				fs.Calls = dedupAppend(fs.Calls, s)
			}
		}
		recurseChildrenJava(src, n, lang, enclosingClass, fs)
	case "throw_statement":
		if name := javaThrowName(src, n, lang); name != "" {
			fs.Raises = dedupAppend(fs.Raises, name)
		}
		recurseChildrenJava(src, n, lang, enclosingClass, fs)
	case "import_declaration":
		if name := javaImportBoundName(src, n, lang); name != "" {
			fs.Imports = dedupAppend(fs.Imports, name)
		}
	case "package_declaration":
		// Skip — not a binding.
		return
	default:
		recurseChildrenJava(src, n, lang, enclosingClass, fs)
	}
}

func recurseChildrenJava(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, enclosingClass string, fs *FileStruct) {
	nc := n.NamedChildCount()
	for i := range nc {
		walkJava(src, n.NamedChild(i), lang, enclosingClass, fs)
	}
}

func extractJavaMethod(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, enclosingClass string) FuncDef {
	fn := FuncDef{
		IsMethod:       enclosingClass != "",
		EnclosingClass: enclosingClass,
	}
	if name := n.ChildByFieldName("name", lang); name != nil {
		fn.Name = nodeText(src, name)
	}
	if params := n.ChildByFieldName("parameters", lang); params != nil {
		fn.Params = extractJavaParams(src, params, lang)
	}
	if ret := n.ChildByFieldName("type", lang); ret != nil {
		fn.ReturnType = nodeText(src, ret)
	}
	return fn
}

func extractJavaParams(src []byte, params *gotreesitter.Node, lang *gotreesitter.Language) []string {
	var out []string
	pc := params.NamedChildCount()
	for i := range pc {
		c := params.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Type(lang) {
		case "formal_parameter", "spread_parameter":
			if name := c.ChildByFieldName("name", lang); name != nil {
				if s := nodeText(src, name); s != "" {
					out = append(out, s)
				}
			}
		}
	}
	return out
}

func extractJavaClass(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language) ClassDef {
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
		switch c.Type(lang) {
		case "method_declaration":
			cls.Methods = append(cls.Methods, extractJavaMethod(src, c, lang, cls.Name))
		case "constructor_declaration":
			ctor := extractJavaMethod(src, c, lang, cls.Name)
			if ctor.Name == "" {
				ctor.Name = cls.Name
			}
			cls.Methods = append(cls.Methods, ctor)
		}
	}
	return cls
}

// javaTypeLeafName resolves a type node (type_identifier,
// scoped_type_identifier, generic_type) to its rightmost identifier
// — the name an agent would search for.
func javaTypeLeafName(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language) string {
	if n == nil {
		return ""
	}
	switch n.Type(lang) {
	case "type_identifier", "identifier":
		return nodeText(src, n)
	case "scoped_type_identifier":
		// Has multiple identifier children; the LAST one is the
		// leaf type name.
		nc := n.NamedChildCount()
		for i := nc - 1; i >= 0; i-- {
			c := n.NamedChild(i)
			if c == nil {
				continue
			}
			if t := c.Type(lang); t == "type_identifier" || t == "identifier" {
				return nodeText(src, c)
			}
		}
	case "generic_type":
		// `Foo<T>` — recurse on the first named child (the base
		// type).
		if nc := n.NamedChildCount(); nc > 0 {
			return javaTypeLeafName(src, n.NamedChild(0), lang)
		}
	}
	return ""
}

func javaThrowName(src []byte, throwNode *gotreesitter.Node, lang *gotreesitter.Language) string {
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
	case "object_creation_expression":
		// `throw new Foo(...)` — the type carries the name.
		if tnode := expr.ChildByFieldName("type", lang); tnode != nil {
			return javaTypeLeafName(src, tnode, lang)
		}
	case "method_invocation":
		if name := expr.ChildByFieldName("name", lang); name != nil {
			return nodeText(src, name)
		}
	}
	return ""
}

func javaImportBoundName(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language) string {
	// import_declaration has a single named child: a
	// scoped_identifier (or asterisk for wildcards we skip). Drill
	// to the LAST identifier component — that's the bound type
	// name in the local namespace.
	nc := n.NamedChildCount()
	for i := range nc {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		if c.Type(lang) == "scoped_identifier" {
			return scopedIdentifierLeaf(src, c, lang)
		}
		if c.Type(lang) == "identifier" {
			return nodeText(src, c)
		}
	}
	return ""
}

// scopedIdentifierLeaf walks `a.b.c` and returns "c" — the rightmost
// identifier component.
func scopedIdentifierLeaf(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language) string {
	// A scoped_identifier has children [scope, name?]. The
	// `name` field holds the rightmost ident; fallback to the
	// last identifier child if no field name is set.
	if name := n.ChildByFieldName("name", lang); name != nil {
		if name.Type(lang) == "identifier" {
			return nodeText(src, name)
		}
	}
	nc := n.NamedChildCount()
	for i := nc - 1; i >= 0; i-- {
		c := n.NamedChild(i)
		if c != nil && c.Type(lang) == "identifier" {
			return nodeText(src, c)
		}
	}
	return ""
}

func javaIsBuiltinOrNoise(name string) bool {
	switch name {
	// java.lang core types ride into every program; filtering
	// keeps fs.Calls focused on user-vocabulary names.
	case "println", "print", "printf",
		"toString", "hashCode", "equals", "getClass",
		"size", "length", "isEmpty",
		"get", "set", "put", "add", "remove",
		"valueOf", "parseInt", "parseLong",
		"Integer", "Long", "Double", "Float", "Boolean", "String",
		"Object", "List", "Map", "Set":
		return true
	}
	return false
}
