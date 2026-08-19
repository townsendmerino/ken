package structural

import (
	"github.com/odvcencio/gotreesitter"
)

// extractScala walks a tree-sitter-scala AST and fills FileStruct. Same
// FileStruct contract as the other extractors.
//
// Scala node-type mapping (gotreesitter v0.48.1 / tree-sitter-scala):
//
//   - class_definition / object_definition / trait_definition — the three
//     type-defining forms. First named child "identifier" is the name; members
//     live in a "template_body". Objects (singletons) and traits are modelled
//     as ClassDef shells, same as Kotlin's object_declaration.
//   - function_definition (def with a body) / function_declaration (abstract
//     def, e.g. in a trait). First "identifier" is the name; "parameters" holds
//     the params; a "type_identifier" sibling is the return type.
//   - parameters → parameter → first "identifier" is the param name.
//   - call_expression — callee is an "identifier" (bare call), a
//     "field_expression" (recv.method — the last identifier is the method,
//     the first is the receiver), or a "generic_function" (f[T](...)).
//   - throw_expression → "instance_expression" → "type_identifier" is the
//     thrown type (`throw new AuthError(...)`); a bare `throw e` (a value, not a
//     type) contributes nothing.
//   - import_declaration — `import a.b.C` binds the last identifier; `import
//     a.b.{C, D}` binds the identifiers under "import_selectors".
//   - package_clause — skipped (not a binding).
func extractScala(src []byte, root *gotreesitter.Node, lang *gotreesitter.Language, fs *FileStruct) {
	walkScala(src, root, lang, "", "", fs)
}

func walkScala(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, enclosingClass, enclosingSymbol string, fs *FileStruct) {
	if n == nil {
		return
	}
	switch n.Type(lang) {
	case "class_definition", "object_definition", "trait_definition":
		cls := extractScalaClass(src, n, lang)
		cls.fillSpan(n)
		fs.Classes = append(fs.Classes, cls)
		if body := firstNamedChildOfType(n, lang, "template_body"); body != nil {
			recurseChildrenScala(src, body, lang, cls.Name, enclosingSymbol, fs)
		}
	case "function_definition", "function_declaration":
		fn := extractScalaFunc(src, n, lang, enclosingClass)
		fn.fillSpan(n)
		fs.Functions = append(fs.Functions, fn)
		sym := qualifySymbol(enclosingClass, fn.Name)
		recurseChildrenScala(src, n, lang, enclosingClass, sym, fs)
	case "call_expression":
		if name, recv := scalaCalleeName(src, n, lang); name != "" && !scalaIsBuiltinOrNoise(name) {
			fs.appendCall(name, recv, n, enclosingSymbol)
		}
		recurseChildrenScala(src, n, lang, enclosingClass, enclosingSymbol, fs)
	case "throw_expression":
		if name := scalaThrowName(src, n, lang); name != "" {
			fs.Raises = dedupAppend(fs.Raises, name)
		}
		recurseChildrenScala(src, n, lang, enclosingClass, enclosingSymbol, fs)
	case "import_declaration":
		for _, im := range scalaImportNames(src, n, lang) {
			fs.Imports = dedupAppend(fs.Imports, im)
		}
	case "package_clause":
		return
	default:
		recurseChildrenScala(src, n, lang, enclosingClass, enclosingSymbol, fs)
	}
}

func recurseChildrenScala(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, enclosingClass, enclosingSymbol string, fs *FileStruct) {
	for i := range n.NamedChildCount() {
		walkScala(src, n.NamedChild(i), lang, enclosingClass, enclosingSymbol, fs)
	}
}

func extractScalaClass(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language) ClassDef {
	var cls ClassDef
	if id := firstNamedChildOfType(n, lang, "identifier"); id != nil {
		cls.Name = nodeText(src, id)
	}
	if body := firstNamedChildOfType(n, lang, "template_body"); body != nil {
		for i := range body.NamedChildCount() {
			c := body.NamedChild(i)
			if t := c.Type(lang); t == "function_definition" || t == "function_declaration" {
				m := extractScalaFunc(src, c, lang, cls.Name)
				m.fillSpan(c)
				cls.Methods = append(cls.Methods, m)
			}
		}
	}
	return cls
}

func extractScalaFunc(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, enclosingClass string) FuncDef {
	fn := FuncDef{IsMethod: enclosingClass != "", EnclosingClass: enclosingClass}
	if id := firstNamedChildOfType(n, lang, "identifier"); id != nil {
		fn.Name = nodeText(src, id)
	}
	if params := firstNamedChildOfType(n, lang, "parameters"); params != nil {
		fn.Params = extractScalaParams(src, params, lang)
	}
	if rt := firstNamedChildOfType(n, lang, "type_identifier"); rt != nil {
		fn.ReturnType = nodeText(src, rt)
	}
	return fn
}

func extractScalaParams(src []byte, params *gotreesitter.Node, lang *gotreesitter.Language) []string {
	var out []string
	for i := range params.NamedChildCount() {
		p := params.NamedChild(i)
		if p.Type(lang) == "parameter" {
			if id := firstNamedChildOfType(p, lang, "identifier"); id != nil {
				out = append(out, nodeText(src, id))
			}
		}
	}
	return out
}

// scalaCalleeName returns the called method's leaf name and its receiver text
// (empty for a bare call) from a call_expression.
func scalaCalleeName(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language) (name, receiver string) {
	if n.NamedChildCount() == 0 {
		return "", ""
	}
	callee := n.NamedChild(0)
	switch callee.Type(lang) {
	case "identifier":
		return nodeText(src, callee), ""
	case "field_expression":
		// recv.method — the field (method) is the last identifier; the receiver
		// is the text of the first named child.
		var meth string
		for i := range callee.NamedChildCount() {
			if c := callee.NamedChild(i); c.Type(lang) == "identifier" {
				meth = nodeText(src, c)
			}
		}
		if callee.NamedChildCount() > 0 {
			receiver = nodeText(src, callee.NamedChild(0))
		}
		return meth, receiver
	case "generic_function":
		if id := firstNamedChildOfType(callee, lang, "identifier"); id != nil {
			return nodeText(src, id), ""
		}
	case "call_expression":
		return scalaCalleeName(src, callee, lang) // chained a.b().c()
	}
	return "", ""
}

func scalaThrowName(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language) string {
	if inst := firstNamedChildOfType(n, lang, "instance_expression"); inst != nil {
		if ti := firstNamedChildOfType(inst, lang, "type_identifier"); ti != nil {
			return nodeText(src, ti)
		}
	}
	return ""
}

func scalaImportNames(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language) []string {
	if sel := firstNamedChildOfType(n, lang, "import_selectors"); sel != nil {
		var out []string
		for i := range sel.NamedChildCount() {
			if c := sel.NamedChild(i); c.Type(lang) == "identifier" {
				out = append(out, nodeText(src, c))
			}
		}
		return out
	}
	var last string
	for i := range n.NamedChildCount() {
		if c := n.NamedChild(i); c.Type(lang) == "identifier" {
			last = nodeText(src, c)
		}
	}
	if last != "" {
		return []string{last}
	}
	return nil
}

func scalaIsBuiltinOrNoise(name string) bool {
	switch name {
	case "println", "print", "require", "assert", "apply":
		return true
	}
	return false
}
