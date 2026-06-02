package structural

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuild_GoBasics confirms the Go extractor lights up on
// functions, methods, types, calls, imports.
func TestBuild_GoBasics(t *testing.T) {
	dir := t.TempDir()
	src := `package auth

import (
	"crypto/sha256"
	"fmt"
	pkg "github.com/x/y/z"
)

type User struct {
	Name string
	hash []byte
}

func Authenticate(u *User, password string) (bool, error) {
	h := sha256.Sum256([]byte(password))
	if string(h[:]) != string(u.hash) {
		return false, fmt.Errorf("invalid password")
	}
	return true, nil
}

func (u *User) Login(password string) error {
	ok, err := Authenticate(u, password)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("denied")
	}
	pkg.LogAccess(u.Name)
	return nil
}

type Authenticator interface {
	Authenticate(*User, string) (bool, error)
}
`
	if err := os.WriteFile(filepath.Join(dir, "auth.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	ix, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}

	fs := ix.File("auth.go")
	if fs == nil {
		t.Fatal("auth.go not indexed")
	}

	// Functions: Authenticate (top-level), Login (method on User)
	if got := len(fs.Functions); got != 2 {
		t.Errorf("Functions = %d, want 2 (Authenticate + Login); got %+v",
			got, funcNames(fs.Functions))
	}
	wantNames := map[string]bool{"Authenticate": false, "Login": false}
	for _, fn := range fs.Functions {
		if _, ok := wantNames[fn.Name]; ok {
			wantNames[fn.Name] = true
		}
	}
	for n, found := range wantNames {
		if !found {
			t.Errorf("Functions missing %q; got %v", n, funcNames(fs.Functions))
		}
	}

	// Authenticate: top-level (IsMethod=false), 2 params.
	auth := findFunc(fs.Functions, "Authenticate")
	if auth == nil {
		t.Fatal("Authenticate not found")
	}
	if auth.IsMethod {
		t.Errorf("Authenticate.IsMethod = true, want false (it's top-level)")
	}
	wantParams := []string{"u", "password"}
	if !sliceEq(auth.Params, wantParams) {
		t.Errorf("Authenticate.Params = %v, want %v", auth.Params, wantParams)
	}
	if !strings.Contains(auth.ReturnType, "bool") || !strings.Contains(auth.ReturnType, "error") {
		t.Errorf("Authenticate.ReturnType = %q, want substrings 'bool' and 'error'", auth.ReturnType)
	}

	// Login: method on *User
	login := findFunc(fs.Functions, "Login")
	if login == nil {
		t.Fatal("Login not found")
	}
	if !login.IsMethod {
		t.Errorf("Login.IsMethod = false, want true (it has a receiver)")
	}
	if login.EnclosingClass != "User" {
		t.Errorf("Login.EnclosingClass = %q, want User (the receiver type)", login.EnclosingClass)
	}

	// Classes: User (struct), Authenticator (interface)
	wantClasses := map[string]bool{"User": false, "Authenticator": false}
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

	// User class should have Login as a method (linked from
	// method_declaration → receiver resolution).
	for _, c := range fs.Classes {
		if c.Name == "User" {
			hasLogin := false
			for _, m := range c.Methods {
				if m.Name == "Login" {
					hasLogin = true
				}
			}
			if !hasLogin {
				t.Errorf("User class missing Login method; got %+v", c.Methods)
			}
		}
	}

	// Calls: must contain at least the unfiltered call targets
	// (Sum256, Authenticate, LogAccess). fmt.Errorf is in the
	// goIsBuiltinOrNoise filter, so explicitly NOT captured.
	for _, want := range []string{"Sum256", "Authenticate", "LogAccess"} {
		if !contains(fs.Calls, want) {
			t.Errorf("Calls missing %q; have %v", want, fs.Calls)
		}
	}
	if contains(fs.Calls, "Errorf") {
		t.Errorf("Calls should NOT contain Errorf (filtered by goIsBuiltinOrNoise); have %v", fs.Calls)
	}

	// Imports: "sha256" (from crypto/sha256 path tail), "fmt",
	// "pkg" (the alias for the github.com/x/y/z import).
	for _, want := range []string{"sha256", "fmt", "pkg"} {
		if !contains(fs.Imports, want) {
			t.Errorf("Imports missing %q; have %v", want, fs.Imports)
		}
	}

	// Reverse call graph: Authenticate is called from auth.go.
	if cs := ix.Callers("Authenticate"); len(cs) != 1 || cs[0].File != "auth.go" {
		t.Errorf("Callers(Authenticate) = %v, want [auth.go]", cs)
	}

	// Definition lookup: Authenticate is a top-level function defn.
	defs := ix.Definition("Authenticate")
	if len(defs) != 1 || defs[0].File != "auth.go" {
		t.Errorf("Definition(Authenticate) = %v, want [auth.go]", defs)
	}
	if defs[0].Kind != DefinitionKindFunction {
		t.Errorf("Definition(Authenticate) kind = %v, want Function", defs[0].Kind)
	}

	// Definition for User (a type/class)
	userDefs := ix.Definition("User")
	if len(userDefs) != 1 || userDefs[0].File != "auth.go" {
		t.Errorf("Definition(User) = %v, want [auth.go]", userDefs)
	}
	if userDefs[0].Kind != DefinitionKindClass {
		t.Errorf("Definition(User) kind = %v, want Class", userDefs[0].Kind)
	}
}

// TestBuild_GoMultipleNamesPerType pins the `a, b int` parameter
// shape — multiple names sharing one type.
func TestBuild_GoMultipleNamesPerType(t *testing.T) {
	dir := t.TempDir()
	src := `package x

func Add(a, b int) int {
	return a + b
}
`
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	ix, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	fs := ix.File("x.go")
	if fs == nil || len(fs.Functions) != 1 {
		t.Fatalf("expected 1 function; got %+v", fs)
	}
	if !sliceEq(fs.Functions[0].Params, []string{"a", "b"}) {
		t.Errorf("Params = %v, want [a b]", fs.Functions[0].Params)
	}
}

// TestBuild_MixedPythonAndGo confirms the per-extension router
// resolves correctly when a corpus has multiple languages.
func TestBuild_MixedPythonAndGo(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "py_lib.py"), []byte("def py_func(): pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go_lib.go"), []byte("package x\nfunc GoFunc() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ix, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	if defs := ix.Definition("py_func"); len(defs) != 1 || defs[0].File != "py_lib.py" {
		t.Errorf("Definition(py_func) = %v, want [py_lib.py]", defs)
	}
	if defs := ix.Definition("GoFunc"); len(defs) != 1 || defs[0].File != "go_lib.go" {
		t.Errorf("Definition(GoFunc) = %v, want [go_lib.go]", defs)
	}
}

// findFunc is a tiny helper to extract a FuncDef by name from a
// slice. Used to avoid index-juggling in the test assertions above.
func findFunc(fns []FuncDef, name string) *FuncDef {
	for i := range fns {
		if fns[i].Name == name {
			return &fns[i]
		}
	}
	return nil
}
