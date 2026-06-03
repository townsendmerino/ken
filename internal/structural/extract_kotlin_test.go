package structural

import (
	"os"
	"path/filepath"
	"testing"
)

// TestBuild_KotlinBasics confirms the Kotlin extractor lights up on
// classes / interfaces / objects, top-level + method functions,
// parameters, bare and dotted call expressions, throw via
// jump_expression, and dotted imports (bound to the rightmost
// simple_identifier).
func TestBuild_KotlinBasics(t *testing.T) {
	dir := t.TempDir()
	src := `package com.example.auth

import kotlin.collections.List
import java.util.ArrayList

class SessionManager(private val store: TokenStore) {
    private val active: MutableList<User> = ArrayList()

    fun login(u: User, password: String): Boolean {
        if (!verifyToken(u.id, password)) {
            throw AuthException("denied")
        }
        active.add(u)
        return true
    }

    fun logout(u: User) {
        active.remove(u)
    }
}

interface Authenticator {
    fun authenticate(u: User, pwd: String): Boolean
}

object Constants {
    const val MAX_TRIES = 3
}

fun verifyToken(id: String, pwd: String): Boolean = true
`
	if err := os.WriteFile(filepath.Join(dir, "SessionManager.kt"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	ix, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	fs := ix.File("SessionManager.kt")
	if fs == nil {
		t.Fatal("SessionManager.kt not indexed")
	}

	// Functions: login, logout (methods on SessionManager) +
	// authenticate (signature on Authenticator) + verifyToken
	// (top-level).
	wantFuncs := map[string]bool{
		"login":        false,
		"logout":       false,
		"authenticate": false,
		"verifyToken":  false,
	}
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

	// login: method on SessionManager, params [u, password].
	login := findFunc(fs.Functions, "login")
	if login == nil {
		t.Fatal("login not found")
	}
	if !login.IsMethod || login.EnclosingClass != "SessionManager" {
		t.Errorf("login = {IsMethod=%v Encl=%q}, want method on SessionManager", login.IsMethod, login.EnclosingClass)
	}
	if !sliceEq(login.Params, []string{"u", "password"}) {
		t.Errorf("login.Params = %v, want [u password]", login.Params)
	}

	// verifyToken: top-level function (not a method).
	vt := findFunc(fs.Functions, "verifyToken")
	if vt == nil {
		t.Fatal("verifyToken not found")
	}
	if vt.IsMethod {
		t.Errorf("verifyToken.IsMethod = true, want false (top-level)")
	}

	// Classes: SessionManager + Authenticator (interface, same node
	// type) + Constants (object_declaration).
	wantClasses := map[string]bool{
		"SessionManager": false,
		"Authenticator":  false,
		"Constants":      false,
	}
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

	// Calls: verifyToken (bare) + AuthException (the throw target —
	// constructor-shaped call). `add`/`remove` are filtered as
	// stdlib noise.
	for _, want := range []string{"verifyToken", "AuthException"} {
		if !contains(fs.CalleeNames(), want) {
			t.Errorf("Calls missing %q; have %v", want, fs.CalleeNames())
		}
	}
	if contains(fs.CalleeNames(), "add") {
		t.Errorf("Calls should NOT contain 'add' (filtered by kotlinIsBuiltinOrNoise); have %v", fs.CalleeNames())
	}

	// Imports: bound names are rightmost segments.
	for _, want := range []string{"List", "ArrayList"} {
		if !contains(fs.Imports, want) {
			t.Errorf("Imports missing %q; have %v", want, fs.Imports)
		}
	}

	// Raises: `throw AuthException(...)` → "AuthException".
	if !contains(fs.Raises, "AuthException") {
		t.Errorf("Raises missing %q; have %v", "AuthException", fs.Raises)
	}

	// Definition lookups.
	if defs := ix.Definition("SessionManager"); len(defs) < 1 {
		t.Errorf("Definition(SessionManager) = empty, want at least the class")
	} else {
		hasClass := false
		for _, d := range defs {
			if d.Kind == DefinitionKindClass {
				hasClass = true
			}
		}
		if !hasClass {
			t.Errorf("Definition(SessionManager) lacks Class kind; got %+v", defs)
		}
	}
	if defs := ix.Definition("SessionManager.login"); len(defs) != 1 {
		t.Errorf("Definition(SessionManager.login) = %+v, want one method site", defs)
	} else if defs[0].Kind != DefinitionKindMethod || defs[0].QName != "SessionManager.login" {
		t.Errorf("Definition(SessionManager.login) = %+v, want Method with QName SessionManager.login", defs[0])
	}
}
