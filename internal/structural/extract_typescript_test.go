package structural

import (
	"os"
	"path/filepath"
	"testing"
)

// TestBuild_TypeScriptBasics confirms the TS extractor lights up on
// function declarations, method definitions inside classes,
// interfaces (as ClassDef shells), calls, imports, and throw
// statements (treated as raises).
//
// The fixture deliberately avoids constructs that trip up the
// gotreesitter v0.18.0 tree-sitter-typescript grammar (notably arrow
// functions in declaration position with type annotations on params);
// those are covered in their own focused test below where we accept
// best-effort behavior.
func TestBuild_TypeScriptBasics(t *testing.T) {
	dir := t.TempDir()
	src := `import { foo } from "./foo";
import * as bar from "./bar";
import Foo, { baz as qux } from "./qux";

interface User {
	name: string;
	id: number;
}

export function authenticate(user: User, password: string): boolean {
	return verifyToken(user.id, password);
}

class SessionManager {
	login(u: User): void {
		this.active.push(u);
	}

	logout(u: User): void {
		this.active = this.active.filter(x => x !== u);
	}

	fail(): void {
		throw new AuthError("denied");
	}
}
`
	if err := os.WriteFile(filepath.Join(dir, "auth.ts"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	ix, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}

	fs := ix.File("auth.ts")
	if fs == nil {
		t.Fatal("auth.ts not indexed")
	}

	// Functions: authenticate (top-level), login + logout + fail
	// (methods on SessionManager).
	wantFuncs := map[string]bool{"authenticate": false, "login": false, "logout": false, "fail": false}
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

	// authenticate: top-level (IsMethod=false), 2 params, return type.
	auth := findFunc(fs.Functions, "authenticate")
	if auth == nil {
		t.Fatal("authenticate not found")
	}
	if auth.IsMethod {
		t.Errorf("authenticate.IsMethod = true, want false (top-level)")
	}
	if !sliceEq(auth.Params, []string{"user", "password"}) {
		t.Errorf("authenticate.Params = %v, want [user password]", auth.Params)
	}

	// login: method on SessionManager
	login := findFunc(fs.Functions, "login")
	if login == nil {
		t.Fatal("login not found")
	}
	if !login.IsMethod {
		t.Errorf("login.IsMethod = false, want true")
	}
	if login.EnclosingClass != "SessionManager" {
		t.Errorf("login.EnclosingClass = %q, want SessionManager", login.EnclosingClass)
	}

	// Classes: SessionManager + User (interface treated as
	// class-shell). SessionManager must have its methods linked.
	wantClasses := map[string]bool{"SessionManager": false, "User": false}
	for _, c := range fs.Classes {
		if _, ok := wantClasses[c.Name]; ok {
			wantClasses[c.Name] = true
		}
	}
	for n, found := range wantClasses {
		if !found {
			t.Errorf("Classes missing %q; got %+v", n, fs.Classes)
		}
	}
	for _, c := range fs.Classes {
		if c.Name == "SessionManager" {
			hasLogin := false
			for _, m := range c.Methods {
				if m.Name == "login" {
					hasLogin = true
				}
			}
			if !hasLogin {
				t.Errorf("SessionManager.Methods missing login; got %+v", c.Methods)
			}
		}
	}

	// Calls: verifyToken (top-level call from authenticate) is the
	// canonical one. push/filter are filtered as builtins; this
	// applies the same identifier-noise discipline as the Go
	// extractor.
	for _, want := range []string{"verifyToken"} {
		if !contains(fs.Calls, want) {
			t.Errorf("Calls missing %q; have %v", want, fs.Calls)
		}
	}
	for _, noise := range []string{"push", "filter"} {
		if contains(fs.Calls, noise) {
			t.Errorf("Calls should NOT contain %q (filtered by tsIsBuiltinOrNoise); have %v", noise, fs.Calls)
		}
	}

	// Imports: foo (named), bar (namespace), Foo (default), qux
	// (named with alias) — the local-bound names, not module paths.
	wantImports := []string{"foo", "bar", "Foo", "qux"}
	for _, want := range wantImports {
		if !contains(fs.Imports, want) {
			t.Errorf("Imports missing %q; have %v", want, fs.Imports)
		}
	}

	// Raises: throw new AuthError(...) inside fail() — the
	// constructor name should land in Raises.
	if !contains(fs.Raises, "AuthError") {
		t.Errorf("Raises missing %q; have %v", "AuthError", fs.Raises)
	}

	// Reverse call graph: verifyToken is called from auth.ts.
	if cs := ix.Callers("verifyToken"); len(cs) != 1 || cs[0].File != "auth.ts" {
		t.Errorf("Callers(verifyToken) = %v, want [auth.ts]", cs)
	}

	// Definition lookup: authenticate is a top-level function defn.
	defs := ix.Definition("authenticate")
	if len(defs) != 1 || defs[0].File != "auth.ts" {
		t.Errorf("Definition(authenticate) = %v, want [auth.ts]", defs)
	}
	if defs[0].Kind != DefinitionKindFunction {
		t.Errorf("Definition(authenticate) kind = %v, want Function", defs[0].Kind)
	}

	// SessionManager (a class) should be DefinitionKindClass.
	clsDefs := ix.Definition("SessionManager")
	if len(clsDefs) != 1 || clsDefs[0].Kind != DefinitionKindClass {
		t.Errorf("Definition(SessionManager) = %v, want one Class def", clsDefs)
	}

	// Qualified-method lookup: SessionManager.login finds only the
	// method, not the top-level authenticate.
	mDef := ix.Definition("SessionManager.login")
	if len(mDef) != 1 || mDef[0].Kind != DefinitionKindMethod || mDef[0].QName != "SessionManager.login" {
		t.Errorf("Definition(SessionManager.login) = %+v, want one Method site with QName SessionManager.login", mDef)
	}
}

// TestBuild_TypeScriptArrowConst covers the lexical_declaration arm
// of the extractor — `const Foo = (x) => ...`. This is a separate
// test because the tree-sitter-typescript grammar shipped with
// gotreesitter v0.18.0 has known parse-recovery issues with arrow
// functions in declaration position when params carry type
// annotations. We pin the WORKING shape (no param type annotations)
// and accept that the typed form is best-effort.
func TestBuild_TypeScriptArrowConst(t *testing.T) {
	dir := t.TempDir()
	src := `const handler = (user) => verifyToken(user);
const noop = () => doNothing();
`
	if err := os.WriteFile(filepath.Join(dir, "x.ts"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	ix, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	fs := ix.File("x.ts")
	if fs == nil {
		t.Fatal("x.ts not indexed")
	}

	// handler + noop should both be FuncDefs (named via
	// variable_declarator → arrow_function rhs).
	wantNames := map[string]bool{"handler": false, "noop": false}
	for _, fn := range fs.Functions {
		if _, ok := wantNames[fn.Name]; ok {
			wantNames[fn.Name] = true
		}
	}
	for n, found := range wantNames {
		if !found {
			t.Errorf("Functions missing arrow-const %q; got %v", n, funcNames(fs.Functions))
		}
	}

	// handler.Params should include "user" (single-arg arrow with no
	// type annotation uses the `parameter` field directly).
	h := findFunc(fs.Functions, "handler")
	if h == nil {
		t.Fatal("handler not found")
	}
	if !sliceEq(h.Params, []string{"user"}) {
		t.Errorf("handler.Params = %v, want [user]", h.Params)
	}

	// Calls inside arrow bodies are captured.
	for _, want := range []string{"verifyToken", "doNothing"} {
		if !contains(fs.Calls, want) {
			t.Errorf("Calls missing %q; have %v", want, fs.Calls)
		}
	}
}

// TestBuild_TypeScriptTSXExt confirms .tsx files route through the
// same typescript grammar (no separate tsx grammar entry in v0;
// adding one is a future revision).
func TestBuild_TypeScriptTSXExt(t *testing.T) {
	dir := t.TempDir()
	// Deliberately NO JSX in this fixture — the typescript grammar
	// excludes JSX, so .tsx files containing it would produce ERROR
	// nodes. .tsx with vanilla TS still indexes.
	src := `export function pageHandler(req: Request): Response {
	return process(req);
}
`
	if err := os.WriteFile(filepath.Join(dir, "page.tsx"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	ix, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	fs := ix.File("page.tsx")
	if fs == nil {
		t.Fatal("page.tsx not indexed (extension routing broken?)")
	}
	if defs := ix.Definition("pageHandler"); len(defs) != 1 || defs[0].File != "page.tsx" {
		t.Errorf("Definition(pageHandler) = %v, want [page.tsx]", defs)
	}
}
