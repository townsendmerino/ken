package structural

import (
	"strings"

	"github.com/odvcencio/gotreesitter"
)

// extractDart walks a tree-sitter-dart AST and fills FileStruct.
// Same FileStruct contract as the other extractors.
//
// Dart node-type mapping (gotreesitter v0.20.0-rc3). Tree-sitter-dart
// exposes some `field=name`-style labels but they're inconsistent
// (the class-name `identifier` of a class_definition shows up at
// namedIdx=0 with no field; namedIdx=1 field=name points at the
// class_body). We rely on positional + Type() access, same fallback
// pattern as extract_kotlin.go and extract_rust.go.
//
// Key shape differences from Java/Kotlin/Swift:
//
//   - There is NO `call_expression` node. Calls are a flat sequence
//     of siblings at the parent level: `identifier + selector + ...
//
//   - selector(argument_part)`. The PRESENCE of an
//     `argument_part`-wrapping selector turns a preceding identifier
//     chain into a call. `active.add(u)` parses as
//
//     <parent> { identifier "active",
//     selector { unconditional_assignable_selector
//     { identifier "add" } },
//     selector { argument_part {...} } }
//
//     So we scan every container's named children left-to-right,
//     track the most recently-seen identifier (either bare or via a
//     `.foo` selector), and when we hit an `argument_part`-selector
//     we record that identifier as a call.
//
//   - Class members aren't single nodes — a method is two siblings
//     in the class_body: `method_signature` (which wraps a
//     `function_signature` carrying the name + params) followed by
//     `function_body`. A field is `declaration { ... }`. A
//     constructor is `declaration { constructor_signature { ... } }`.
//
//   - Top-level functions are `function_signature` + sibling
//     `function_body` at program root.
//
//   - Imports: `import_or_export > library_import >
//     import_specification > configurable_uri > uri >
//     string_literal`. The bound name is the leaf filename without
//     `.dart` (for `'package:foo/bar/baz.dart'` → "baz"); for
//     dart-builtins like `'dart:async'` we bind to the suffix
//     ("async").
//
//   - mixin_declaration is a top-level class-like declaration, with
//     a class_body. Treated as a class for outline purposes.
//
//   - throw_expression carries the thrown thing's identifier as a
//     direct named child (followed by an optional argument selector
//     when it's a constructor-style `throw Foo(...)`).
func extractDart(src []byte, root *gotreesitter.Node, lang *gotreesitter.Language, fs *FileStruct) {
	walkDart(src, root, lang, "", fs)
}

func walkDart(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, enclosingClass string, fs *FileStruct) {
	if n == nil {
		return
	}
	switch n.Type(lang) {
	case "class_definition", "mixin_declaration", "extension_declaration":
		cls := extractDartClass(src, n, lang)
		fs.Classes = append(fs.Classes, cls)
		if body := firstNamedChildOfType(n, lang, "class_body"); body != nil {
			recurseChildrenDart(src, body, lang, cls.Name, fs)
		}
	case "function_signature":
		// Top-level function signature OR method signature inside
		// a class body (when wrapped by method_signature, that
		// outer case takes priority; here we only see the bare
		// signature for top-level funcs).
		if n.Parent() != nil && n.Parent().Type(lang) == "method_signature" {
			break // handled by the method_signature case below
		}
		fn := extractDartFunc(src, n, lang, enclosingClass)
		// Skip nameless signatures — closures, anonymous functions,
		// some getter/setter forms inherit this node type without
		// a leading identifier. Indexing them with empty names
		// pollutes the symbol map.
		if fn.Name != "" {
			fs.Functions = append(fs.Functions, fn)
		}
	case "method_signature":
		fn := extractDartMethodSignature(src, n, lang, enclosingClass)
		if fn.Name != "" {
			fs.Functions = append(fs.Functions, fn)
		}
	case "constructor_signature":
		fn := extractDartConstructor(src, n, lang, enclosingClass)
		if fn.Name != "" {
			fs.Functions = append(fs.Functions, fn)
		}
	case "throw_expression":
		if name := dartThrowName(src, n, lang); name != "" {
			fs.Raises = dedupAppend(fs.Raises, name)
		}
		// Throw can still contain nested calls (e.g. throw Foo(bar())).
		recurseChildrenDart(src, n, lang, enclosingClass, fs)
		return
	case "library_import", "import_specification":
		if name := dartImportBoundName(src, n, lang); name != "" {
			fs.Imports = dedupAppend(fs.Imports, name)
		}
		return
	}

	// Generic recursion + call detection. Every container node has
	// its named children scanned for the `identifier ... selector(argument_part)`
	// pattern so we catch calls in any context (statement, expression,
	// argument list, etc.).
	detectDartCalls(src, n, lang, fs)
	recurseChildrenDart(src, n, lang, enclosingClass, fs)
}

func recurseChildrenDart(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, enclosingClass string, fs *FileStruct) {
	nc := n.NamedChildCount()
	for i := range nc {
		walkDart(src, n.NamedChild(i), lang, enclosingClass, fs)
	}
}

// detectDartCalls scans a container's named children left-to-right
// tracking the most-recent "would-be-callee" identifier, and records
// a call when an argument_part selector follows. Handles both bare
// calls (`f(x)`) and dotted calls (`a.b.f(x)`) uniformly.
func detectDartCalls(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, fs *FileStruct) {
	nc := n.NamedChildCount()
	if nc < 2 {
		return
	}
	lastIdent := ""
	for i := range nc {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Type(lang) {
		case "identifier":
			lastIdent = nodeText(src, c)
		case "selector":
			// A selector is either `.name` (member access) or
			// `(args)` (invocation). Distinguish by looking at the
			// first named child.
			if c.NamedChildCount() == 0 {
				continue
			}
			first := c.NamedChild(0)
			if first == nil {
				continue
			}
			switch first.Type(lang) {
			case "unconditional_assignable_selector",
				"conditional_assignable_selector",
				"assignable_selector":
				// `.name`-style — pull the inner identifier as
				// the new candidate callee.
				if id := firstNamedChildOfType(first, lang, "identifier"); id != nil {
					lastIdent = nodeText(src, id)
				}
			case "argument_part":
				// `(...)`-style — preceding identifier is a call.
				if lastIdent != "" && !dartIsBuiltinOrNoise(lastIdent) {
					fs.Calls = dedupAppend(fs.Calls, lastIdent)
				}
				lastIdent = ""
			}
		}
	}
}

func extractDartClass(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language) ClassDef {
	cls := ClassDef{}
	// First identifier child is the class/mixin/extension name. For
	// `abstract class Foo`, the `abstract` modifier comes first but
	// has Type "abstract" (or similar), not "identifier" — so the
	// first identifier is still the name.
	if id := firstNamedChildOfType(n, lang, "identifier"); id != nil {
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
		switch c.Type(lang) {
		case "method_signature":
			cls.Methods = append(cls.Methods, extractDartMethodSignature(src, c, lang, cls.Name))
		case "declaration":
			// Could be a field or a constructor. If it has a
			// constructor_signature child, treat as constructor.
			if cs := firstNamedChildOfType(c, lang, "constructor_signature"); cs != nil {
				cls.Methods = append(cls.Methods, extractDartConstructor(src, cs, lang, cls.Name))
			}
		}
	}
	return cls
}

func extractDartFunc(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, enclosingClass string) FuncDef {
	fn := FuncDef{
		IsMethod:       enclosingClass != "",
		EnclosingClass: enclosingClass,
	}
	// Name is the first identifier sibling of the function_signature.
	// (Return type, if any, is a type_identifier earlier in the
	// sibling list.)
	if id := firstNamedChildOfType(n, lang, "identifier"); id != nil {
		fn.Name = nodeText(src, id)
	}
	if params := firstNamedChildOfType(n, lang, "formal_parameter_list"); params != nil {
		fn.Params = extractDartParams(src, params, lang)
	}
	return fn
}

func extractDartMethodSignature(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, enclosingClass string) FuncDef {
	// method_signature wraps a function_signature.
	if fs := firstNamedChildOfType(n, lang, "function_signature"); fs != nil {
		return extractDartFunc(src, fs, lang, enclosingClass)
	}
	// Fallback if shape changes.
	return extractDartFunc(src, n, lang, enclosingClass)
}

func extractDartConstructor(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, enclosingClass string) FuncDef {
	fn := FuncDef{
		IsMethod:       true,
		EnclosingClass: enclosingClass,
	}
	if id := firstNamedChildOfType(n, lang, "identifier"); id != nil {
		fn.Name = nodeText(src, id)
	}
	if fn.Name == "" {
		fn.Name = enclosingClass
	}
	if params := firstNamedChildOfType(n, lang, "formal_parameter_list"); params != nil {
		fn.Params = extractDartParams(src, params, lang)
	}
	return fn
}

func extractDartParams(src []byte, params *gotreesitter.Node, lang *gotreesitter.Language) []string {
	var out []string
	pc := params.NamedChildCount()
	for i := range pc {
		c := params.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Type(lang) {
		case "formal_parameter":
			// formal_parameter has TYPE then NAME — pick the LAST
			// identifier (skipping the type_identifier).
			nm := dartParamLastIdent(src, c, lang)
			if nm != "" {
				out = append(out, nm)
			}
		}
	}
	return out
}

func dartParamLastIdent(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language) string {
	// Look for nested constructor_param (e.g. `this.store`).
	if cp := firstNamedChildOfType(n, lang, "constructor_param"); cp != nil {
		if id := firstNamedChildOfType(cp, lang, "identifier"); id != nil {
			return nodeText(src, id)
		}
	}
	last := ""
	nc := n.NamedChildCount()
	for i := range nc {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		if c.Type(lang) == "identifier" {
			last = nodeText(src, c)
		}
	}
	return last
}

// dartThrowName returns the identifier or constructor name of a
// `throw X` or `throw Foo(...)` expression.
func dartThrowName(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language) string {
	// First named child of throw_expression is the thrown thing's
	// leading identifier; subsequent selectors carry argument lists.
	if n == nil || n.NamedChildCount() == 0 {
		return ""
	}
	first := n.NamedChild(0)
	if first == nil {
		return ""
	}
	if first.Type(lang) == "identifier" {
		return nodeText(src, first)
	}
	return ""
}

// dartImportBoundName returns the leaf filename (or dart: suffix) of
// a library import. For `import 'package:foo/bar/baz.dart'` returns
// "baz"; for `import 'dart:async'` returns "async".
func dartImportBoundName(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language) string {
	// Walk down to the string_literal.
	var lit *gotreesitter.Node
	var dfs func(node *gotreesitter.Node)
	dfs = func(node *gotreesitter.Node) {
		if node == nil || lit != nil {
			return
		}
		if node.Type(lang) == "string_literal" {
			lit = node
			return
		}
		nc := node.NamedChildCount()
		for i := range nc {
			dfs(node.NamedChild(i))
		}
	}
	dfs(n)
	if lit == nil {
		return ""
	}
	raw := strings.Trim(nodeText(src, lit), "'\"")
	// `dart:async` → "async"
	if after, ok := strings.CutPrefix(raw, "dart:"); ok {
		return after
	}
	// `package:foo/bar/baz.dart` → "baz"
	// `bar/baz.dart` → "baz"
	if i := strings.LastIndex(raw, "/"); i >= 0 {
		raw = raw[i+1:]
	}
	// `package:foo` (no /) → "foo"
	raw = strings.TrimPrefix(raw, "package:")
	raw = strings.TrimSuffix(raw, ".dart")
	return raw
}

func dartIsBuiltinOrNoise(name string) bool {
	switch name {
	// Common Dart stdlib / pattern noise that rides into every file.
	case "print", "debugPrint",
		"toString", "hashCode", "noSuchMethod",
		"add", "remove", "removeWhere", "removeLast", "clear",
		"insert", "contains", "indexOf", "indexWhere",
		"forEach", "map", "where", "fold", "reduce",
		"first", "last", "isEmpty", "isNotEmpty", "length",
		"toList", "toSet", "toMap",
		"int", "double", "bool", "String", "List", "Map", "Set",
		"Object", "Future", "Stream", "Iterable",
		"of", "from", "fromIterable",
		"setState", "build":
		return true
	}
	return false
}
