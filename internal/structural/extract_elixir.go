package structural

import (
	"strings"

	"github.com/odvcencio/gotreesitter"
)

// extractElixir walks a tree-sitter-elixir AST and fills FileStruct. Same
// FileStruct contract as the other extractors.
//
// Elixir is macro-uniform: nearly everything is a `call` node, discriminated by
// its leading child (gotreesitter v0.48.1 / tree-sitter-elixir):
//
//   - call{identifier=defmodule, arguments{alias=Name}, do_block} — a module.
//     Modelled as a ClassDef shell (Elixir has no classes); its def/defp
//     children are its "methods".
//   - call{identifier=def|defp, arguments{call{identifier=NAME, arguments=PARAMS}}, …}
//     — a function. The name/params live in a nested `call` (or a bare
//     identifier for a zero-arg def). The body is a `do_block` child or, for the
//     `def f, do: expr` form, a `keywords`→`pair` under arguments.
//   - call{identifier=import|alias|require|use, arguments{alias=Mod}} — brings a
//     module into scope; the local leaf name (last dotted segment) → Imports.
//   - call{identifier=raise|reraise, arguments{alias=Err, …}} → Raises.
//   - call{identifier=NAME, …} otherwise — a local function call → CallRef.
//   - call{alias=Mod, identifier=fn, …} — a remote call `Mod.fn(...)`; fn is the
//     callee, Mod the receiver.
//
// Control-flow macros (if/case/cond/for/with/try/…) are calls too and are
// filtered as noise so they don't drown the call graph.
func extractElixir(src []byte, root *gotreesitter.Node, lang *gotreesitter.Language, fs *FileStruct) {
	walkElixir(src, root, lang, "", "", fs)
}

func walkElixir(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, enclosingClass, enclosingSymbol string, fs *FileStruct) {
	if n == nil {
		return
	}
	if n.Type(lang) != "call" || n.NamedChildCount() == 0 {
		recurseChildrenElixir(src, n, lang, enclosingClass, enclosingSymbol, fs)
		return
	}

	first := n.NamedChild(0)
	switch first.Type(lang) {
	case "identifier":
		head := nodeText(src, first)
		switch head {
		case "defmodule":
			name := elixirFirstAlias(src, n, lang)
			cls := ClassDef{Name: name}
			cls.fillSpan(n)
			// Fill Methods from the module body's def/defp.
			if body := firstNamedChildOfType(n, lang, "do_block"); body != nil {
				cls.Methods = elixirModuleMethods(src, body, lang, name)
			}
			fs.Classes = append(fs.Classes, cls)
			if body := firstNamedChildOfType(n, lang, "do_block"); body != nil {
				recurseChildrenElixir(src, body, lang, name, enclosingSymbol, fs)
			}
		case "def", "defp":
			fn := elixirDefSig(src, n, lang, enclosingClass)
			fn.fillSpan(n)
			fs.Functions = append(fs.Functions, fn)
			sym := qualifySymbol(enclosingClass, fn.Name)
			elixirRecurseBody(src, n, lang, enclosingClass, sym, fs)
		case "import", "alias", "require", "use":
			if a := elixirFirstAlias(src, n, lang); a != "" {
				fs.Imports = dedupAppend(fs.Imports, elixirLeaf(a))
			}
		case "raise", "reraise":
			if a := elixirFirstAlias(src, n, lang); a != "" {
				fs.Raises = dedupAppend(fs.Raises, elixirLeaf(a))
			}
		default:
			if !elixirIsBuiltinOrNoise(head) {
				fs.appendCall(head, "", n, enclosingSymbol)
			}
			elixirRecurseArgs(src, n, lang, enclosingClass, enclosingSymbol, fs)
		}
	case "dot":
		// Remote call Mod.fn(...) / recv.fn(...): first is a `dot` wrapping the
		// receiver (an `alias` for a module, or an `identifier` for a value) and
		// the method identifier.
		var recv, meth string
		for i := range first.NamedChildCount() {
			c := first.NamedChild(i)
			switch c.Type(lang) {
			case "alias":
				recv = nodeText(src, c)
			case "identifier":
				if meth != "" { // a prior identifier was the receiver
					recv = meth
				}
				meth = nodeText(src, c)
			}
		}
		if meth != "" && !elixirIsBuiltinOrNoise(meth) {
			fs.appendCall(meth, recv, n, enclosingSymbol)
		}
		elixirRecurseArgs(src, n, lang, enclosingClass, enclosingSymbol, fs)
	default:
		recurseChildrenElixir(src, n, lang, enclosingClass, enclosingSymbol, fs)
	}
}

func recurseChildrenElixir(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, enclosingClass, enclosingSymbol string, fs *FileStruct) {
	for i := range n.NamedChildCount() {
		walkElixir(src, n.NamedChild(i), lang, enclosingClass, enclosingSymbol, fs)
	}
}

// elixirRecurseArgs walks a call's arguments (and any do_block) for nested
// calls/raises, skipping the leading macro/receiver token.
func elixirRecurseArgs(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, enclosingClass, enclosingSymbol string, fs *FileStruct) {
	if args := firstNamedChildOfType(n, lang, "arguments"); args != nil {
		recurseChildrenElixir(src, args, lang, enclosingClass, enclosingSymbol, fs)
	}
	if body := firstNamedChildOfType(n, lang, "do_block"); body != nil {
		recurseChildrenElixir(src, body, lang, enclosingClass, enclosingSymbol, fs)
	}
}

// elixirRecurseBody walks a def's body — the do_block child and/or the
// `keywords` (do: form) inside arguments — for nested calls/raises.
func elixirRecurseBody(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, enclosingClass, enclosingSymbol string, fs *FileStruct) {
	if body := firstNamedChildOfType(n, lang, "do_block"); body != nil {
		recurseChildrenElixir(src, body, lang, enclosingClass, enclosingSymbol, fs)
	}
	if args := firstNamedChildOfType(n, lang, "arguments"); args != nil {
		if kw := firstNamedChildOfType(args, lang, "keywords"); kw != nil {
			recurseChildrenElixir(src, kw, lang, enclosingClass, enclosingSymbol, fs)
		}
	}
}

// elixirModuleMethods lists the def/defp signatures directly in a module body,
// for ClassDef.Methods (mirrors the FuncDef entries added during recursion).
func elixirModuleMethods(src []byte, body *gotreesitter.Node, lang *gotreesitter.Language, moduleName string) []FuncDef {
	var out []FuncDef
	for i := range body.NamedChildCount() {
		c := body.NamedChild(i)
		if c.Type(lang) != "call" || c.NamedChildCount() == 0 {
			continue
		}
		if h := c.NamedChild(0); h.Type(lang) == "identifier" {
			if t := nodeText(src, h); t == "def" || t == "defp" {
				m := elixirDefSig(src, c, lang, moduleName)
				m.fillSpan(c)
				out = append(out, m)
			}
		}
	}
	return out
}

// elixirDefSig pulls the name + params out of a def/defp call.
func elixirDefSig(src []byte, callNode *gotreesitter.Node, lang *gotreesitter.Language, enclosingClass string) FuncDef {
	fn := FuncDef{IsMethod: enclosingClass != "", EnclosingClass: enclosingClass}
	args := firstNamedChildOfType(callNode, lang, "arguments")
	if args == nil || args.NamedChildCount() == 0 {
		return fn
	}
	sig := args.NamedChild(0)
	// `def foo when guard` wraps the signature in a binary_operator; unwrap to
	// the inner call.
	if sig.Type(lang) == "binary_operator" {
		if inner := firstNamedChildOfType(sig, lang, "call"); inner != nil {
			sig = inner
		}
	}
	switch sig.Type(lang) {
	case "call":
		if id := firstNamedChildOfType(sig, lang, "identifier"); id != nil {
			fn.Name = nodeText(src, id)
		}
		if p := firstNamedChildOfType(sig, lang, "arguments"); p != nil {
			for i := range p.NamedChildCount() {
				if c := p.NamedChild(i); c.Type(lang) == "identifier" {
					fn.Params = append(fn.Params, nodeText(src, c))
				}
			}
		}
	case "identifier":
		fn.Name = nodeText(src, sig)
	}
	return fn
}

// elixirFirstAlias returns the text of the first `alias` node inside a call's
// arguments (the module name for defmodule/import/alias/raise).
func elixirFirstAlias(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language) string {
	args := firstNamedChildOfType(n, lang, "arguments")
	if args == nil {
		return ""
	}
	if a := firstNamedChildOfType(args, lang, "alias"); a != nil {
		return nodeText(src, a)
	}
	return ""
}

// elixirLeaf returns the last dotted segment of a module path (MyApp.Repo → Repo).
func elixirLeaf(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 && i+1 < len(name) {
		return name[i+1:]
	}
	return name
}

func elixirIsBuiltinOrNoise(name string) bool {
	switch name {
	case "if", "unless", "case", "cond", "for", "with", "try", "receive",
		"quote", "unquote", "fn", "when", "and", "or", "not",
		"is_nil", "is_atom", "is_binary", "is_list", "is_map", "inspect",
		"send", "spawn", "then", "tap":
		return true
	}
	return false
}
