package structural

import (
	"github.com/odvcencio/gotreesitter"
)

// extractPython walks a tree-sitter-Python AST and fills FileStruct
// with the structural facts the M0d Arm B heuristic + the Stage 8
// additive arms need:
//
//   - Functions: every function_definition, with parameter names +
//     return type annotation. Method-ness is inferred from being
//     nested inside a class_definition.
//   - Classes: every class_definition with the methods inside it.
//   - Imports: leaf names introduced by import_statement /
//     import_from_statement (the name as bound in the local
//     namespace; `from foo import X as Y` contributes "Y").
//   - Calls: every call's callee leaf name. `foo(...)` ⇒ "foo";
//     `obj.bar(...)` ⇒ "bar"; nested calls (`foo(bar())`) contribute
//     both. Dedup'd; order is first-occurrence.
//   - Raises: exception class names from raise_statement.
//     `raise X(...)` ⇒ "X"; `raise X` ⇒ "X"; bare `raise` ⇒ nothing.
//
// Every node-API call (Type(), ChildByFieldName, FieldNameForChild)
// in gotreesitter requires the *Language handle the tree was parsed
// with — we thread it through every recursive step.
//
// The walker is intentionally graceful on malformed parses
// (gotreesitter returns ERROR nodes mid-tree on bad input). Anything
// we don't recognize is skipped silently — the FileStruct just lacks
// that fact.
func extractPython(src []byte, root *gotreesitter.Node, lang *gotreesitter.Language, fs *FileStruct) {
	walkPy(src, root, lang, "", fs)
}

// walkPy is the recursive driver. enclosingClass is the name of the
// nearest class_definition ancestor (empty string at file scope), so
// nested function_definitions can record IsMethod + EnclosingClass.
func walkPy(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, enclosingClass string, fs *FileStruct) {
	if n == nil {
		return
	}
	switch n.Type(lang) {
	case "function_definition":
		fn := extractPyFunc(src, n, lang, enclosingClass)
		fs.Functions = append(fs.Functions, fn)
		// Recurse into the body so nested calls/raises are
		// captured at file scope (matches the Python stdlib
		// `ast.walk` behavior the M0d Python materializer used).
		// Don't change enclosingClass — a nested function is not
		// a method of any class it's lexically inside, only of
		// the immediately-enclosing class_definition.
		nc := n.NamedChildCount()
		for i := range nc {
			walkPy(src, n.NamedChild(i), lang, enclosingClass, fs)
		}
	case "class_definition":
		cls := extractPyClass(src, n, lang)
		fs.Classes = append(fs.Classes, cls)
		// DON'T append cls.Methods to fs.Functions directly: the
		// body recursion below propagates enclosingClass=cls.Name
		// and the `case "function_definition"` arm picks them up
		// with IsMethod=true. Appending here too would duplicate
		// them.
		body := n.ChildByFieldName("body", lang)
		if body != nil {
			nc := body.NamedChildCount()
			for i := range nc {
				walkPy(src, body.NamedChild(i), lang, cls.Name, fs)
			}
		}
	case "call":
		if name := pyCalleeName(src, n, lang); name != "" && !pyIsBuiltin(name) {
			fs.Calls = dedupAppend(fs.Calls, name)
		}
		// Recurse — nested calls inside arguments also count.
		nc := n.NamedChildCount()
		for i := range nc {
			walkPy(src, n.NamedChild(i), lang, enclosingClass, fs)
		}
	case "raise_statement":
		if name := pyRaiseName(src, n, lang); name != "" {
			fs.Raises = dedupAppend(fs.Raises, name)
		}
		nc := n.NamedChildCount()
		for i := range nc {
			walkPy(src, n.NamedChild(i), lang, enclosingClass, fs)
		}
	case "import_statement":
		// `import foo, bar.baz` — record each dotted_name's last
		// component as the bound name.
		nc := n.NamedChildCount()
		for i := range nc {
			c := n.NamedChild(i)
			if c == nil {
				continue
			}
			if name := pyImportBoundName(src, c, lang); name != "" {
				fs.Imports = dedupAppend(fs.Imports, name)
			}
		}
	case "import_from_statement":
		// `from foo import X` — record X (and Y from `import X
		// as Y` if the aliased_import form). The module clause is
		// always the FIRST named child of import_from_statement
		// in tree-sitter-python; subsequent named children are the
		// imported names. We rely on this structural invariant
		// rather than field names because FieldNameForChild() uses
		// the all-children index (which includes unnamed `from`/
		// `import` keyword tokens) and doesn't match the
		// NamedChild() index — easier to skip by position.
		nc := n.NamedChildCount()
		for i := 1; i < nc; i++ { // i=0 is the module; skip it.
			c := n.NamedChild(i)
			if c == nil {
				continue
			}
			if name := pyImportBoundName(src, c, lang); name != "" {
				fs.Imports = dedupAppend(fs.Imports, name)
			}
		}
	default:
		// Generic recurse for everything else (module body,
		// expression statements, if/try/with, etc.).
		nc := n.NamedChildCount()
		for i := range nc {
			walkPy(src, n.NamedChild(i), lang, enclosingClass, fs)
		}
	}
}

// extractPyFunc returns the FuncDef for a function_definition node.
// Methods are flagged via IsMethod (caller sets enclosingClass when
// the node is nested inside a class_definition).
func extractPyFunc(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, enclosingClass string) FuncDef {
	fn := FuncDef{
		IsMethod:       enclosingClass != "",
		EnclosingClass: enclosingClass,
	}
	if name := n.ChildByFieldName("name", lang); name != nil {
		fn.Name = nodeText(src, name)
	}
	if params := n.ChildByFieldName("parameters", lang); params != nil {
		pc := params.NamedChildCount()
		for i := range pc {
			pname := pyParamName(src, params.NamedChild(i), lang)
			if pname != "" && pname != "self" && pname != "cls" {
				fn.Params = append(fn.Params, pname)
			}
		}
	}
	if ret := n.ChildByFieldName("return_type", lang); ret != nil {
		fn.ReturnType = nodeText(src, ret)
	}
	return fn
}

// extractPyClass returns the ClassDef for a class_definition node.
// Methods are extracted via a one-level scan of the body — nested
// classes are handled by the recursive walkPy.
func extractPyClass(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language) ClassDef {
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
		case "function_definition":
			cls.Methods = append(cls.Methods, extractPyFunc(src, c, lang, cls.Name))
		case "decorated_definition":
			// `@decorator def f(...)` — descend one level to
			// find the actual function_definition.
			dc := c.NamedChildCount()
			for j := range dc {
				inner := c.NamedChild(j)
				if inner != nil && inner.Type(lang) == "function_definition" {
					cls.Methods = append(cls.Methods, extractPyFunc(src, inner, lang, cls.Name))
					break
				}
			}
		}
	}
	return cls
}

// pyCalleeName returns the leaf name a call expression invokes:
//
//	foo(arg)        ⇒ "foo"  (call.function is identifier)
//	obj.bar(arg)    ⇒ "bar"  (call.function is attribute; we want .attribute, not .object)
//	mod.cls.baz()   ⇒ "baz"  (same; we take the rightmost attribute)
//	(f or g)(x)     ⇒ ""     (call.function is parenthesized_expression; unresolvable by name)
//	arr[i]()        ⇒ ""     (subscript; ditto)
//
// Returning "" for unresolvable shapes is correct — those aren't
// vocab-bridging signals we'd want to inject anyway.
func pyCalleeName(src []byte, callNode *gotreesitter.Node, lang *gotreesitter.Language) string {
	fn := callNode.ChildByFieldName("function", lang)
	if fn == nil {
		return ""
	}
	switch fn.Type(lang) {
	case "identifier":
		return nodeText(src, fn)
	case "attribute":
		// attribute = object . attribute_name; we want the rightmost.
		if name := fn.ChildByFieldName("attribute", lang); name != nil {
			return nodeText(src, name)
		}
	}
	return ""
}

// pyRaiseName extracts the exception class name from a raise_statement.
//
//	raise X            ⇒ "X"  (single identifier child)
//	raise X("msg")     ⇒ "X"  (call child; recurse to its function)
//	raise mod.X(...)   ⇒ "X"  (attribute; rightmost name)
//	raise              ⇒ ""   (bare re-raise; no name to surface)
func pyRaiseName(src []byte, raiseNode *gotreesitter.Node, lang *gotreesitter.Language) string {
	// The raised expression is typically the first named child after
	// the `raise` keyword; gotreesitter's tree exposes it as a
	// children-list-search rather than a named field.
	nc := raiseNode.NamedChildCount()
	for i := range nc {
		c := raiseNode.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Type(lang) {
		case "identifier":
			return nodeText(src, c)
		case "call":
			return pyCalleeName(src, c, lang)
		case "attribute":
			if name := c.ChildByFieldName("attribute", lang); name != nil {
				return nodeText(src, name)
			}
		}
		// Only consider the first named child — `raise X from Y`
		// puts the cause as a later child, but X is what we want.
		break
	}
	return ""
}

// pyImportBoundName returns the bound local name for an import-clause
// node. Handles `foo`, `foo.bar` (last component), `foo as bar` (the
// alias), `from m import X as Y` (the Y).
func pyImportBoundName(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language) string {
	switch n.Type(lang) {
	case "identifier":
		return nodeText(src, n)
	case "dotted_name":
		nc := n.NamedChildCount()
		if nc == 0 {
			return nodeText(src, n)
		}
		// Take the last dotted component (the leaf name, e.g.
		// `import os.path` ⇒ "path").
		return nodeText(src, n.NamedChild(nc-1))
	case "aliased_import":
		// aliased_import has exactly 2 named children in tree-
		// sitter-python: the original name (with grammar field
		// "name") first, the alias identifier second (no field).
		// We want the alias — that's what's bound in the local
		// namespace. Use position rather than ChildByFieldName
		// because the field-name lookup has off-by-index issues
		// with NamedChild indexing (gotreesitter's
		// FieldNameForChild operates on all-children indices).
		nc := n.NamedChildCount()
		if nc >= 2 {
			return nodeText(src, n.NamedChild(nc-1))
		}
		if nc == 1 {
			return pyImportBoundName(src, n.NamedChild(0), lang)
		}
	}
	return ""
}

// pyParamName returns the parameter's bound name. Handles
// `identifier`, `typed_parameter`, `default_parameter`, and the
// `*args` / `**kwargs` shapes. Returns "" for shapes we don't
// recognize.
func pyParamName(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language) string {
	if n == nil {
		return ""
	}
	switch n.Type(lang) {
	case "identifier":
		return nodeText(src, n)
	case "typed_parameter":
		if n.NamedChildCount() > 0 {
			return pyParamName(src, n.NamedChild(0), lang)
		}
	case "default_parameter":
		if name := n.ChildByFieldName("name", lang); name != nil {
			return pyParamName(src, name, lang)
		}
	case "typed_default_parameter":
		if name := n.ChildByFieldName("name", lang); name != nil {
			return pyParamName(src, name, lang)
		}
	case "list_splat_pattern", "dictionary_splat_pattern":
		if n.NamedChildCount() > 0 {
			return pyParamName(src, n.NamedChild(0), lang)
		}
	}
	return ""
}

// nodeText returns the source bytes spanned by a node as a string.
// Bounded by len(src) defensively — ERROR-recovery sometimes emits
// nodes with bytes outside the source range.
func nodeText(src []byte, n *gotreesitter.Node) string {
	if n == nil {
		return ""
	}
	start, end := n.StartByte(), n.EndByte()
	if end <= start || end > uint32(len(src)) {
		return ""
	}
	return string(src[start:end])
}

// pyIsBuiltin returns true for the small list of Python built-in
// names whose presence as a call target carries no retrieval signal
// (every function in the corpus calls len/range/etc.). Mirrors the
// stopword filter the M0d Python materializer used so the Go and
// Python extractors agree on Arm B's baseline output.
func pyIsBuiltin(name string) bool {
	switch name {
	case "len", "range", "str", "int", "float", "bool", "list", "dict",
		"set", "tuple", "type", "isinstance", "hasattr", "getattr",
		"setattr", "super", "print", "open", "format", "sorted",
		"reversed", "enumerate", "zip", "map", "filter", "any", "all",
		"next", "iter", "sum", "min", "max", "abs", "round",
		"self", "cls", "args", "kwargs":
		return true
	}
	return false
}
