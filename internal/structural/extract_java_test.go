package structural

import (
	"os"
	"path/filepath"
	"testing"
)

// TestBuild_JavaBasics confirms the Java extractor lights up on
// classes / interfaces / methods, formal parameters, method
// invocations, object_creation_expression (treated as a call on the
// constructor), throws (treated as raises), and scoped imports
// (bound to the last segment of the dotted path).
func TestBuild_JavaBasics(t *testing.T) {
	dir := t.TempDir()
	src := `package com.example;

import java.util.List;
import java.util.ArrayList;
import static java.lang.Math.max;

public class AuthService {
	private List<User> users = new ArrayList<>();

	public AuthService() {
		this.users = new ArrayList<>();
	}

	public boolean authenticate(User user, String password) {
		return verifyToken(user.getId(), password);
	}

	public void login(User u) {
		users.add(u);
	}

	public void fail() throws AuthException {
		throw new AuthError("denied");
	}
}

interface Authenticator {
	boolean authenticate(User u, String pwd);
}
`
	if err := os.WriteFile(filepath.Join(dir, "AuthService.java"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	ix, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}

	fs := ix.File("AuthService.java")
	if fs == nil {
		t.Fatal("AuthService.java not indexed")
	}

	// Functions: AuthService (constructor) + authenticate + login
	// + fail (on AuthService) + authenticate (on Authenticator
	// interface — a signature without body, still a declaration).
	wantFuncs := map[string]bool{"AuthService": false, "authenticate": false, "login": false, "fail": false}
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

	// authenticate: method on AuthService, params [user, password],
	// return type "boolean".
	auth := findFunc(fs.Functions, "authenticate")
	if auth == nil {
		t.Fatal("authenticate not found")
	}
	if !auth.IsMethod {
		t.Errorf("authenticate.IsMethod = false, want true")
	}
	if auth.EnclosingClass != "AuthService" && auth.EnclosingClass != "Authenticator" {
		t.Errorf("authenticate.EnclosingClass = %q, want AuthService or Authenticator", auth.EnclosingClass)
	}
	if !sliceEq(auth.Params, []string{"user", "password"}) && !sliceEq(auth.Params, []string{"u", "pwd"}) {
		t.Errorf("authenticate.Params = %v, want [user password] or [u pwd]", auth.Params)
	}

	// Classes: AuthService (class) + Authenticator (interface).
	wantClasses := map[string]bool{"AuthService": false, "Authenticator": false}
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

	// Calls: verifyToken (method_invocation) + ArrayList (the
	// object_creation_expression's type). add is in the noise filter.
	for _, want := range []string{"verifyToken", "ArrayList"} {
		if !contains(fs.Calls, want) {
			t.Errorf("Calls missing %q; have %v", want, fs.Calls)
		}
	}
	if contains(fs.Calls, "add") {
		t.Errorf("Calls should NOT contain 'add' (filtered by javaIsBuiltinOrNoise); have %v", fs.Calls)
	}

	// Imports: bound names are the LAST segments of the scoped
	// identifiers. `import static java.lang.Math.max` binds "max".
	wantImports := []string{"List", "ArrayList", "max"}
	for _, want := range wantImports {
		if !contains(fs.Imports, want) {
			t.Errorf("Imports missing %q; have %v", want, fs.Imports)
		}
	}

	// Raises: throw new AuthError(...) → "AuthError"
	if !contains(fs.Raises, "AuthError") {
		t.Errorf("Raises missing %q; have %v", "AuthError", fs.Raises)
	}

	// Definition lookups: AuthService class + qualified method.
	if defs := ix.Definition("AuthService"); len(defs) < 1 {
		t.Errorf("Definition(AuthService) = empty, want at least the class")
	} else {
		// AuthService is BOTH a class AND a constructor function
		// (same name) — at minimum one Class kind site.
		hasClass := false
		for _, d := range defs {
			if d.Kind == DefinitionKindClass {
				hasClass = true
			}
		}
		if !hasClass {
			t.Errorf("Definition(AuthService) lacks Class kind; got %+v", defs)
		}
	}
	if defs := ix.Definition("AuthService.login"); len(defs) != 1 {
		t.Errorf("Definition(AuthService.login) = %+v, want one method site", defs)
	} else if defs[0].Kind != DefinitionKindMethod || defs[0].QName != "AuthService.login" {
		t.Errorf("Definition(AuthService.login) = %+v, want Method with QName AuthService.login", defs[0])
	}
}
