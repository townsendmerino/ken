package structural

import (
	"os"
	"path/filepath"
	"testing"
)

// TestBuild_JavaScriptBasics confirms the JS extractor (which reuses
// the TS walker — JS shapes are a strict subset of the TS grammar's
// shared node-type names) lights up on function declarations, class
// methods, arrow constants, calls, imports, and throws.
//
// The fixture deliberately uses vanilla ES6+ syntax (no JSX, no type
// annotations) so the javascript grammar parses without ERROR nodes.
func TestBuild_JavaScriptBasics(t *testing.T) {
	dir := t.TempDir()
	src := `import { foo } from "./foo";
import * as bar from "./bar";

function authenticate(user, password) {
	return verifyToken(user.id, password);
}

const Login = (u) => authenticate(u, "x");

class SessionManager {
	constructor() {
		this.active = [];
	}
	login(u) {
		this.active.push(u);
	}
	fail() {
		throw new AuthError("denied");
	}
}
`
	if err := os.WriteFile(filepath.Join(dir, "auth.js"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	ix, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	fs := ix.File("auth.js")
	if fs == nil {
		t.Fatal("auth.js not indexed (extension routing broken?)")
	}

	// Functions: authenticate (top-level), Login (arrow const),
	// constructor + login + fail (methods).
	wantFuncs := map[string]bool{"authenticate": false, "Login": false, "constructor": false, "login": false, "fail": false}
	for _, fn := range fs.Functions {
		if _, ok := wantFuncs[fn.Name]; ok {
			wantFuncs[fn.Name] = true
		}
	}
	for n, found := range wantFuncs {
		if !found {
			t.Errorf("Functions missing %q; got %v", n, funcNames(fs.Functions))
		}
	}

	// authenticate: untyped JS params — JS uses bare `identifier`
	// children inside formal_parameters (vs TS's
	// `required_parameter` wrapper). extractTSParams handles both.
	auth := findFunc(fs.Functions, "authenticate")
	if auth == nil {
		t.Fatal("authenticate not found")
	}
	if !sliceEq(auth.Params, []string{"user", "password"}) {
		t.Errorf("authenticate.Params = %v, want [user password]", auth.Params)
	}

	// login: method on SessionManager
	login := findFunc(fs.Functions, "login")
	if login == nil {
		t.Fatal("login not found")
	}
	if login.EnclosingClass != "SessionManager" {
		t.Errorf("login.EnclosingClass = %q, want SessionManager", login.EnclosingClass)
	}

	// Classes: SessionManager
	hasClass := false
	for _, c := range fs.Classes {
		if c.Name == "SessionManager" {
			hasClass = true
		}
	}
	if !hasClass {
		t.Errorf("Classes missing SessionManager; got %+v", fs.Classes)
	}

	// Calls: verifyToken + authenticate (Login's body calls it).
	// push is in the noise filter.
	for _, want := range []string{"verifyToken", "authenticate"} {
		if !contains(fs.Calls, want) {
			t.Errorf("Calls missing %q; have %v", want, fs.Calls)
		}
	}

	// Imports: foo (named) + bar (namespace)
	for _, want := range []string{"foo", "bar"} {
		if !contains(fs.Imports, want) {
			t.Errorf("Imports missing %q; have %v", want, fs.Imports)
		}
	}

	// Raises: throw new AuthError(...) → "AuthError"
	if !contains(fs.Raises, "AuthError") {
		t.Errorf("Raises missing %q; have %v", "AuthError", fs.Raises)
	}

	// Definition lookups: top-level + class
	if defs := ix.Definition("authenticate"); len(defs) != 1 || defs[0].File != "auth.js" {
		t.Errorf("Definition(authenticate) = %v, want [auth.js]", defs)
	}
	if defs := ix.Definition("SessionManager"); len(defs) != 1 || defs[0].Kind != DefinitionKindClass {
		t.Errorf("Definition(SessionManager) = %+v, want one Class def", defs)
	}
}

// TestBuild_JavaScriptExtVariants confirms .jsx / .mjs / .cjs all
// route to the javascript grammar.
func TestBuild_JavaScriptExtVariants(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"mod.mjs": "export function modFn() { return 1; }\n",
		"cmd.cjs": "function cmdFn() { return 2; }\nmodule.exports = { cmdFn };\n",
		"ui.jsx":  "function uiFn() { return null; }\n", // no JSX in the body — keep it vanilla
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ix, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	for name, sym := range map[string]string{
		"mod.mjs": "modFn",
		"cmd.cjs": "cmdFn",
		"ui.jsx":  "uiFn",
	} {
		if defs := ix.Definition(sym); len(defs) != 1 || defs[0].File != name {
			t.Errorf("Definition(%s) = %v, want [%s]", sym, defs, name)
		}
	}
}
