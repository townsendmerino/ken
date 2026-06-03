//go:build swift

// The extractor below is gated behind the `swift` build tag — it
// compiles only when explicitly requested (`go build -tags=swift
// ./...`) and is excluded from the default build. Swift remains
// parked at the kenLangToTSLang gate because the gotreesitter
// v0.20.0-rc3 tree-sitter-swift grammar misparses real-world Swift
// code: a 4-line file whose header comment contains the word "and",
// "software", "associated", or "Permission" already produces a
// root=ERROR parse. Cross-corpus survey on 2026-06-03 showed 0% / 2%
// / 8% / 35% clean-parse rates across Alamofire / swift-nio /
// swift-collections / Defaults respectively — universal failure.
//
// The extractor stays in tree so re-enabling once the grammar fixes
// land is a two-line change: register `.swift → swift` in
// kenLangToTSLang and `swift → extractSwift` in langExtractor, drop
// this build tag, and revert the Swift entry in DESIGN.md §10.

package structural

import (
	"github.com/odvcencio/gotreesitter"
)

// extractSwift walks a tree-sitter-swift AST and fills FileStruct.
// Same FileStruct contract as the other extractors.
//
// Swift node-type mapping (gotreesitter v0.20.0-rc3 / tree-sitter-
// swift). This grammar exposes some field names but in a way that
// (e.g. labelling the class-name child as `declaration_kind`) makes
// positional + Type() walking simpler and more robust; we use that
// pattern, mirroring extract_rust.go and extract_kotlin.go.
//
//   - class_declaration   — covers `class`, `struct`, `enum`,
//     `protocol`, AND `extension`. The first
//     named child is either a `type_identifier`
//     (class/struct/enum/protocol — that's the
//     type name) or a `user_type` (extension —
//     that's the extended type). Body is a
//     `class_body` (or `enum_class_body` for
//     enums). Extensions FOLD into the base
//     type's ClassDef — we recurse into their
//     body with enclosingClass = extended type
//     but do NOT emit a duplicate ClassDef.
//   - function_declaration — first `simple_identifier` child is the
//     function name. `parameter` children
//     carry the params; each parameter's
//     inner `simple_identifier` (the one not
//     labelled `external_name`) is the bound
//     name.
//   - init_declaration    — Swift constructor; emitted as a method
//     named after the enclosing type so
//     `definition(Foo)` covers both the type
//     and its constructor.
//   - call_expression     — same shape as Kotlin: bare (`foo(x)`) →
//     first child is `simple_identifier`;
//     dotted (`a.b.foo(x)`) → first child is
//     `navigation_expression` whose final
//     `navigation_suffix` carries the method
//     name.
//   - control_transfer_statement — covers `return` AND `throw`.
//     Discriminate by presence of a
//     `throw_keyword` child; if throw,
//     the next named child is the
//     thrown expression.
//   - import_declaration  — `import Foundation` / `import a.b.C`;
//     the rightmost identifier is the bound
//     name in scope.
func extractSwift(src []byte, root *gotreesitter.Node, lang *gotreesitter.Language, fs *FileStruct) {
	walkSwift(src, root, lang, "", "", fs)
}

func walkSwift(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, enclosingClass, enclosingSymbol string, fs *FileStruct) {
	if n == nil {
		return
	}
	switch n.Type(lang) {
	case "function_declaration", "protocol_function_declaration":
		fn := extractSwiftFunc(src, n, lang, enclosingClass)
		fn.fillSpan(n)
		fs.Functions = append(fs.Functions, fn)
		sym := qualifySymbol(enclosingClass, fn.Name)
		if body := firstNamedChildOfType(n, lang, "function_body"); body != nil {
			recurseChildrenSwift(src, body, lang, enclosingClass, sym, fs)
		}
	case "init_declaration":
		// Swift constructor — bind to enclosing type.
		fn := FuncDef{
			Name:           enclosingClass,
			IsMethod:       enclosingClass != "",
			EnclosingClass: enclosingClass,
		}
		fn.fillSpan(n)
		fs.Functions = append(fs.Functions, fn)
		sym := qualifySymbol(enclosingClass, fn.Name)
		if body := firstNamedChildOfType(n, lang, "function_body"); body != nil {
			recurseChildrenSwift(src, body, lang, enclosingClass, sym, fs)
		}
	case "class_declaration", "protocol_declaration":
		name, isExtension := swiftClassDeclTarget(src, n, lang)
		if !isExtension {
			cls := extractSwiftClass(src, n, lang, name)
			cls.fillSpan(n)
			fs.Classes = append(fs.Classes, cls)
		}
		// Walk body regardless — for extensions this binds new
		// methods to the extended type without emitting a
		// duplicate ClassDef.
		body := firstNamedChildOfType(n, lang, "class_body")
		if body == nil {
			body = firstNamedChildOfType(n, lang, "enum_class_body")
		}
		if body == nil {
			body = firstNamedChildOfType(n, lang, "protocol_body")
		}
		if body != nil {
			recurseChildrenSwift(src, body, lang, name, enclosingSymbol, fs)
		}
	case "call_expression":
		if name := swiftCalleeName(src, n, lang); name != "" && !swiftIsBuiltinOrNoise(name) {
			fs.appendCall(name, "", n, enclosingSymbol)
		}
		recurseChildrenSwift(src, n, lang, enclosingClass, enclosingSymbol, fs)
	case "control_transfer_statement":
		if swiftIsThrow(n, lang) {
			if name := swiftThrowName(src, n, lang); name != "" {
				fs.Raises = dedupAppend(fs.Raises, name)
			}
		}
		recurseChildrenSwift(src, n, lang, enclosingClass, enclosingSymbol, fs)
	case "import_declaration":
		if name := swiftImportBoundName(src, n, lang); name != "" {
			fs.Imports = dedupAppend(fs.Imports, name)
		}
	default:
		recurseChildrenSwift(src, n, lang, enclosingClass, enclosingSymbol, fs)
	}
}

func recurseChildrenSwift(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, enclosingClass, enclosingSymbol string, fs *FileStruct) {
	nc := n.NamedChildCount()
	for i := 0; i < nc; i++ {
		walkSwift(src, n.NamedChild(i), lang, enclosingClass, enclosingSymbol, fs)
	}
}

// swiftClassDeclTarget inspects a class_declaration's first named
// child to determine (a) the type name we should associate methods
// with, and (b) whether this is an `extension` (which folds into the
// base type and does NOT get its own ClassDef).
func swiftClassDeclTarget(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language) (name string, isExtension bool) {
	if n == nil || n.NamedChildCount() == 0 {
		return "", false
	}
	first := n.NamedChild(0)
	if first == nil {
		return "", false
	}
	switch first.Type(lang) {
	case "type_identifier":
		// class / struct / enum / protocol — the type name is here.
		return nodeText(src, first), false
	case "user_type":
		// extension SomeType: ... — the extended type's name lives
		// in the first type_identifier child of the user_type.
		if id := firstNamedChildOfType(first, lang, "type_identifier"); id != nil {
			return nodeText(src, id), true
		}
		return nodeText(src, first), true
	}
	// Fallback: use the text of the first child as the name.
	return nodeText(src, first), false
}

func extractSwiftClass(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, name string) ClassDef {
	cls := ClassDef{Name: name}
	body := firstNamedChildOfType(n, lang, "class_body")
	if body == nil {
		body = firstNamedChildOfType(n, lang, "enum_class_body")
	}
	if body == nil {
		body = firstNamedChildOfType(n, lang, "protocol_body")
	}
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
		case "function_declaration", "protocol_function_declaration":
			cls.Methods = append(cls.Methods, extractSwiftFunc(src, c, lang, cls.Name))
		case "init_declaration":
			cls.Methods = append(cls.Methods, FuncDef{
				Name:           cls.Name,
				IsMethod:       true,
				EnclosingClass: cls.Name,
			})
		}
	}
	return cls
}

func extractSwiftFunc(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, enclosingClass string) FuncDef {
	fn := FuncDef{
		IsMethod:       enclosingClass != "",
		EnclosingClass: enclosingClass,
	}
	if id := firstNamedChildOfType(n, lang, "simple_identifier"); id != nil {
		fn.Name = nodeText(src, id)
	}
	fn.Params = extractSwiftParams(src, n, lang)
	if ut := firstNamedChildOfType(n, lang, "user_type"); ut != nil {
		fn.ReturnType = nodeText(src, ut)
	}
	return fn
}

// extractSwiftParams pulls `parameter` children directly off the
// function_declaration. Swift's tree-sitter doesn't wrap params in a
// parameters node — they're siblings of the function name.
func extractSwiftParams(src []byte, fn *gotreesitter.Node, lang *gotreesitter.Language) []string {
	var out []string
	nc := fn.NamedChildCount()
	for i := 0; i < nc; i++ {
		c := fn.NamedChild(i)
		if c == nil || c.Type(lang) != "parameter" {
			continue
		}
		// A parameter has up to two simple_identifier children
		// (external_name + name) followed by a user_type. The
		// internal bound name is the LAST simple_identifier child
		// that doesn't sit inside a user_type.
		nm := swiftParamName(src, c, lang)
		if nm != "" {
			out = append(out, nm)
		}
	}
	return out
}

func swiftParamName(src []byte, param *gotreesitter.Node, lang *gotreesitter.Language) string {
	last := ""
	nc := param.NamedChildCount()
	for i := 0; i < nc; i++ {
		c := param.NamedChild(i)
		if c == nil {
			continue
		}
		if c.Type(lang) == "simple_identifier" {
			last = nodeText(src, c)
		}
		// Stop scanning once we hit the user_type — anything past
		// that is the type annotation, not the bound name.
		if c.Type(lang) == "user_type" {
			break
		}
	}
	return last
}

// swiftCalleeName mirrors kotlinCalleeName.
func swiftCalleeName(src []byte, callExpr *gotreesitter.Node, lang *gotreesitter.Language) string {
	if callExpr == nil || callExpr.NamedChildCount() == 0 {
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
		return swiftNavigationTail(src, callee, lang)
	}
	return ""
}

func swiftNavigationTail(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language) string {
	if n == nil {
		return ""
	}
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

func swiftIsThrow(n *gotreesitter.Node, lang *gotreesitter.Language) bool {
	nc := n.NamedChildCount()
	for i := 0; i < nc; i++ {
		c := n.NamedChild(i)
		if c != nil && c.Type(lang) == "throw_keyword" {
			return true
		}
	}
	return false
}

func swiftThrowName(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language) string {
	nc := n.NamedChildCount()
	for i := 0; i < nc; i++ {
		c := n.NamedChild(i)
		if c == nil || c.Type(lang) == "throw_keyword" {
			continue
		}
		switch c.Type(lang) {
		case "simple_identifier":
			return nodeText(src, c)
		case "call_expression":
			return swiftCalleeName(src, c, lang)
		case "navigation_expression":
			// `throw AuthError.denied` — the type name (AuthError)
			// is the navigation target. Drill to its leftmost
			// type_identifier or simple_identifier.
			return swiftNavigationHead(src, c, lang)
		}
	}
	return ""
}

// swiftNavigationHead returns the leftmost identifier in a
// navigation_expression chain — for `Foo.bar.baz` it returns "Foo".
// Used for `throw EnumType.case` where the type is the throw target.
func swiftNavigationHead(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language) string {
	if n == nil || n.NamedChildCount() == 0 {
		return ""
	}
	first := n.NamedChild(0)
	if first == nil {
		return ""
	}
	switch first.Type(lang) {
	case "simple_identifier", "type_identifier":
		return nodeText(src, first)
	case "user_type":
		if id := firstNamedChildOfType(first, lang, "type_identifier"); id != nil {
			return nodeText(src, id)
		}
		return nodeText(src, first)
	case "navigation_expression":
		return swiftNavigationHead(src, first, lang)
	}
	return ""
}

func swiftImportBoundName(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language) string {
	// import_declaration > identifier(s). The whole text after
	// "import " is the bound name (e.g. "Foundation",
	// "a.b.C" → last segment "C").
	nc := n.NamedChildCount()
	for i := 0; i < nc; i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Type(lang) {
		case "identifier":
			// Chained simple_identifier; rightmost is the bound name.
			ncc := c.NamedChildCount()
			for j := ncc - 1; j >= 0; j-- {
				cc := c.NamedChild(j)
				if cc != nil && cc.Type(lang) == "simple_identifier" {
					return nodeText(src, cc)
				}
			}
			return nodeText(src, c)
		case "simple_identifier", "type_identifier":
			return nodeText(src, c)
		}
	}
	return ""
}

func swiftIsBuiltinOrNoise(name string) bool {
	switch name {
	// Foundation / stdlib that ride into every Swift file; filtering
	// keeps fs.Calls focused on user-vocabulary names.
	case "print", "debugPrint",
		"description", "hashValue",
		"count", "isEmpty",
		"append", "remove", "removeAll", "removeFirst", "removeLast",
		"insert", "contains",
		"map", "filter", "reduce", "forEach", "sorted", "first", "last",
		"Int", "Double", "Float", "Bool", "String", "Character",
		"Array", "Dictionary", "Set", "Optional",
		"Any", "AnyObject", "Void":
		return true
	}
	return false
}
