package structural

import (
	"github.com/odvcencio/gotreesitter"
)

// extractGo walks a tree-sitter-Go AST and fills FileStruct with the
// structural facts the M0d/M0e enrichment + Track 2 tools need:
//
//   - Functions: every function_declaration (top-level funcs) AND
//     every method_declaration. Methods have IsMethod=true and
//     EnclosingClass = receiver type name (the leaf name from
//     `func (u *User) Foo()` is "User").
//   - Classes (Go-flavored): every type_declaration whose body is
//     a struct_type or interface_type. Stored as ClassDef so the
//     Track 2 `outline` tool can render `type T struct{...}` and
//     `type T interface{...}` with their methods.
//   - Imports: bound local names from import_declaration. `import
//     "fmt"` binds "fmt"; `import f "fmt"` binds "f"; `import
//     . "fmt"` is treated as binding nothing distinctive (skipped).
//   - Calls: every distinct callee leaf name from call_expression.
//     `foo(...)` → "foo"; `obj.Bar(...)` → "Bar" (via
//     selector_expression's .field).
//   - Raises: Go doesn't have a raise statement. `panic(err)` is
//     captured as a CALL of "panic" — uniform with Python's stop-
//     word filter; if "panic" feels noisy in the enrichment, the
//     stopword list can drop it. Same goes for the runtime/error
//     idioms (errors.New is captured as a call to "New").
//
// Tree-sitter-Go field-name invariants used here (see
// internal/structural/debug_go_test.go for the AST dump that
// established these):
//
//   - function_declaration: name (identifier), parameters
//     (parameter_list), result (type or parameter_list)
//   - method_declaration: receiver (parameter_list), name
//     (field_identifier), parameters, result
//   - parameter_list child: parameter_declaration with name +
//     type fields. Multiple names per type (a, b int) are
//     handled.
//   - type_declaration → type_spec: name (type_identifier),
//     type (struct_type / interface_type / ...)
//   - call_expression: function field (identifier or
//     selector_expression with .field for the leaf name)
//   - import_declaration → import_spec: path
//     (interpreted_string_literal), optional name (the alias)
//
// The walker is graceful on ERROR-recovered nodes — anything we
// don't recognize is skipped; the FileStruct just lacks that fact.
func extractGo(src []byte, root *gotreesitter.Node, lang *gotreesitter.Language, fs *FileStruct) {
	walkGo(src, root, lang, "", "", fs)
}

func walkGo(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, enclosingType, enclosingSymbol string, fs *FileStruct) {
	if n == nil {
		return
	}
	switch n.Type(lang) {
	case "function_declaration":
		fn := extractGoFunc(src, n, lang, "")
		fn.fillSpan(n)
		fs.Functions = append(fs.Functions, fn)
		// Recurse into the body so nested calls land in fs.CallRefs
		// attributed to this function.
		sym := qualifySymbol("", fn.Name)
		body := n.ChildByFieldName("body", lang)
		if body != nil {
			nc := body.NamedChildCount()
			for i := range nc {
				walkGo(src, body.NamedChild(i), lang, enclosingType, sym, fs)
			}
		}
	case "method_declaration":
		recvType := goReceiverType(src, n, lang)
		fn := extractGoFunc(src, n, lang, recvType)
		fn.fillSpan(n)
		fs.Functions = append(fs.Functions, fn)
		// Methods are also recorded under their receiver type in
		// Classes — same shape Python uses for class methods.
		// Append on first encounter; a type that appears in a
		// type_declaration AND has methods will already have a
		// ClassDef from the type_declaration arm — link the
		// method into that one. Use linkMethodToClass to dedupe.
		if recvType != "" {
			linkMethodToClass(fs, recvType, fn)
		}
		sym := qualifySymbol(recvType, fn.Name)
		body := n.ChildByFieldName("body", lang)
		if body != nil {
			nc := body.NamedChildCount()
			for i := range nc {
				walkGo(src, body.NamedChild(i), lang, recvType, sym, fs)
			}
		}
	case "type_declaration":
		// `type_declaration` may contain multiple `type_spec`
		// children (parenthesized form: `type (A int; B string)`).
		nc := n.NamedChildCount()
		for i := range nc {
			c := n.NamedChild(i)
			if c == nil {
				continue
			}
			if c.Type(lang) == "type_spec" {
				if cls := extractGoType(src, c, lang); cls.Name != "" {
					cls.fillSpan(c)
					ensureClass(fs, cls)
				}
			}
		}
	case "call_expression":
		if name := goCalleeName(src, n, lang); name != "" && !goIsBuiltinOrNoise(name) {
			fs.appendCall(name, "", n, enclosingSymbol)
		}
		nc := n.NamedChildCount()
		for i := range nc {
			walkGo(src, n.NamedChild(i), lang, enclosingType, enclosingSymbol, fs)
		}
	case "import_declaration":
		// `import_declaration` → one or more `import_spec`
		// children (parenthesized or single). Each spec has a
		// `path` (interpreted_string_literal) and optional
		// `name` (alias).
		nc := n.NamedChildCount()
		for i := range nc {
			c := n.NamedChild(i)
			if c == nil {
				continue
			}
			if c.Type(lang) == "import_spec" {
				if name := goImportBoundName(src, c, lang); name != "" {
					fs.Imports = dedupAppend(fs.Imports, name)
				}
			}
			// Some grammar variants nest specs under
			// `import_spec_list`; recurse so we hit any
			// inner specs.
			if c.Type(lang) == "import_spec_list" {
				dc := c.NamedChildCount()
				for j := range dc {
					sp := c.NamedChild(j)
					if sp != nil && sp.Type(lang) == "import_spec" {
						if name := goImportBoundName(src, sp, lang); name != "" {
							fs.Imports = dedupAppend(fs.Imports, name)
						}
					}
				}
			}
		}
	default:
		nc := n.NamedChildCount()
		for i := range nc {
			walkGo(src, n.NamedChild(i), lang, enclosingType, enclosingSymbol, fs)
		}
	}
}

// extractGoFunc returns the FuncDef for a function_declaration or
// method_declaration. enclosingType is the receiver type for methods
// (empty for top-level funcs).
func extractGoFunc(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, enclosingType string) FuncDef {
	fn := FuncDef{
		IsMethod:       enclosingType != "",
		EnclosingClass: enclosingType,
	}
	if name := n.ChildByFieldName("name", lang); name != nil {
		fn.Name = nodeText(src, name)
	}
	if params := n.ChildByFieldName("parameters", lang); params != nil {
		pc := params.NamedChildCount()
		for i := range pc {
			c := params.NamedChild(i)
			if c == nil || c.Type(lang) != "parameter_declaration" {
				continue
			}
			// parameter_declaration may have MULTIPLE name
			// children (Go's `a, b int` form). Walk children and
			// pick up every field=name occurrence — which here
			// means non-type fields whose own type is identifier.
			// Simplest path: any identifier child IS a name.
			// type_identifier children are types, not names.
			cc := c.NamedChildCount()
			for j := range cc {
				cn := c.NamedChild(j)
				if cn != nil && cn.Type(lang) == "identifier" {
					if pname := nodeText(src, cn); pname != "" && pname != "_" {
						fn.Params = append(fn.Params, pname)
					}
				}
			}
		}
	}
	if ret := n.ChildByFieldName("result", lang); ret != nil {
		fn.ReturnType = nodeText(src, ret)
	}
	return fn
}

// extractGoType returns the ClassDef shell for a type_spec. Methods
// are attached separately when method_declarations are walked (Go
// allows methods to be declared far from the type body).
func extractGoType(src []byte, typeSpec *gotreesitter.Node, lang *gotreesitter.Language) ClassDef {
	cls := ClassDef{}
	if name := typeSpec.ChildByFieldName("name", lang); name != nil {
		cls.Name = nodeText(src, name)
	}
	return cls
}

// goReceiverType returns the receiver type's leaf name for a
// method_declaration node. `func (u *User) Foo()` → "User";
// `func (u User) Foo()` → "User"; `func (User) Foo()` → "User".
// Returns "" for shapes we can't resolve.
func goReceiverType(src []byte, methodNode *gotreesitter.Node, lang *gotreesitter.Language) string {
	recv := methodNode.ChildByFieldName("receiver", lang)
	if recv == nil {
		return ""
	}
	// The receiver is a parameter_list containing one
	// parameter_declaration. The declaration's `type` field is the
	// receiver type — typically pointer_type(*T) or just type_
	// identifier(T).
	pc := recv.NamedChildCount()
	for i := range pc {
		decl := recv.NamedChild(i)
		if decl == nil || decl.Type(lang) != "parameter_declaration" {
			continue
		}
		typ := decl.ChildByFieldName("type", lang)
		if typ == nil {
			continue
		}
		switch typ.Type(lang) {
		case "type_identifier":
			return nodeText(src, typ)
		case "pointer_type":
			// pointer_type wraps the inner type_identifier.
			ic := typ.NamedChildCount()
			for j := range ic {
				inner := typ.NamedChild(j)
				if inner != nil && inner.Type(lang) == "type_identifier" {
					return nodeText(src, inner)
				}
			}
		case "generic_type":
			// Generic receiver: `func (s *Stack[T]) Push(...)`
			// inner is a type_identifier for the base type.
			ic := typ.NamedChildCount()
			for j := range ic {
				inner := typ.NamedChild(j)
				if inner != nil && inner.Type(lang) == "type_identifier" {
					return nodeText(src, inner)
				}
			}
		}
	}
	return ""
}

// goCalleeName returns the callee leaf name for a call_expression
// node. `foo()` → "foo"; `obj.Bar()` → "Bar"; `pkg.New()` → "New".
// Unresolvable shapes (function-typed expressions, lambdas) → "".
func goCalleeName(src []byte, callNode *gotreesitter.Node, lang *gotreesitter.Language) string {
	fn := callNode.ChildByFieldName("function", lang)
	if fn == nil {
		return ""
	}
	switch fn.Type(lang) {
	case "identifier":
		return nodeText(src, fn)
	case "selector_expression":
		// selector_expression = operand . field
		// We want the field part — the rightmost name in
		// `pkg.func()` or `obj.method()`.
		if field := fn.ChildByFieldName("field", lang); field != nil {
			return nodeText(src, field)
		}
	}
	return ""
}

// goImportBoundName returns the bound local name for an import_spec
// node. `import "fmt"` → "fmt" (last path component);
// `import f "fmt"` → "f"; `import _ "x"` → ""; `import . "x"` → "".
func goImportBoundName(src []byte, spec *gotreesitter.Node, lang *gotreesitter.Language) string {
	// If an alias (`name` field) is present, that's the bound name
	// — unless it's "_" (blank import) or "." (dot import).
	if name := spec.ChildByFieldName("name", lang); name != nil {
		nm := nodeText(src, name)
		if nm == "_" || nm == "." {
			return ""
		}
		if nm != "" {
			return nm
		}
	}
	// No alias: derive from the path. Path is an
	// interpreted_string_literal like "\"crypto/sha256\""; we
	// want the last segment ("sha256").
	if path := spec.ChildByFieldName("path", lang); path != nil {
		raw := nodeText(src, path)
		// Strip surrounding quotes.
		if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
			raw = raw[1 : len(raw)-1]
		}
		// Last path segment after the final "/".
		if idx := lastSlash(raw); idx >= 0 {
			raw = raw[idx+1:]
		}
		return raw
	}
	return ""
}

func lastSlash(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' {
			return i
		}
	}
	return -1
}

// linkMethodToClass appends fn as a method of the named receiver
// type's ClassDef. Creates the ClassDef if no type_declaration for
// the receiver type has been walked yet (methods declared in a
// different file than the type, or before the type in file order).
func linkMethodToClass(fs *FileStruct, typeName string, fn FuncDef) {
	for i := range fs.Classes {
		if fs.Classes[i].Name == typeName {
			fs.Classes[i].Methods = append(fs.Classes[i].Methods, fn)
			return
		}
	}
	// New class shell — method seen before its type definition.
	fs.Classes = append(fs.Classes, ClassDef{
		Name:    typeName,
		Methods: []FuncDef{fn},
	})
}

// ensureClass adds cls to fs.Classes if not already present. If a
// ClassDef with the same Name exists (e.g. created earlier by
// linkMethodToClass), the existing entry stays and its Methods
// are preserved.
func ensureClass(fs *FileStruct, cls ClassDef) {
	for i := range fs.Classes {
		if fs.Classes[i].Name == cls.Name {
			return
		}
	}
	fs.Classes = append(fs.Classes, cls)
}

// goIsBuiltinOrNoise returns true for Go names whose presence as a
// call target carries no retrieval signal. Mirrors Python's
// pyIsBuiltin. Conservative: includes only the truly ubiquitous Go
// builtins. domain-specific noise (panic, recover) is included since
// every other function calls them — they don't bridge query vocab.
func goIsBuiltinOrNoise(name string) bool {
	switch name {
	case "len", "cap", "append", "make", "new", "copy", "delete",
		"panic", "recover", "close", "print", "println",
		"complex", "real", "imag",
		"true", "false", "nil",
		// Common error idioms — appears in nearly every
		// function but doesn't help retrieval.
		"Errorf", "Error", "Sprintf", "Printf", "Println":
		return true
	}
	return false
}
