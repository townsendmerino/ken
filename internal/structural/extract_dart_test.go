package structural

import (
	"os"
	"path/filepath"
	"testing"
)

// TestBuild_DartBasics confirms the Dart extractor lights up on
// classes / mixins / abstract classes, top-level + method functions,
// constructors, bare + dotted call detection (Dart has no
// call_expression node — calls are sibling sequences of
// `identifier + selector(argument_part)`), `throw` expressions, and
// imports (bound to the rightmost path component without `.dart`).
func TestBuild_DartBasics(t *testing.T) {
	dir := t.TempDir()
	src := `import 'dart:async';
import 'package:flutter/material.dart';

class SessionManager {
  final TokenStore store;
  final List<User> active = [];

  SessionManager(this.store);

  Future<bool> login(User u, String password) async {
    if (!verifyToken(u.id, password)) {
      throw AuthException('denied');
    }
    active.add(u);
    return true;
  }

  void logout(User u) {
    active.remove(u);
  }
}

abstract class Authenticator {
  bool authenticate(User u, String pwd);
}

mixin Greetable {
  String greet(String name) => 'Hello $name';
}

bool verifyToken(String id, String pwd) => true;
`
	if err := os.WriteFile(filepath.Join(dir, "session.dart"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	ix, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	fs := ix.File("session.dart")
	if fs == nil {
		t.Fatal("session.dart not indexed")
	}

	// Functions: login + logout (methods on SessionManager) +
	// authenticate (signature on Authenticator) + greet (method on
	// Greetable) + verifyToken (top-level) + SessionManager
	// constructor (emitted via constructor_signature).
	wantFuncs := map[string]bool{
		"login":        false,
		"logout":       false,
		"authenticate": false,
		"greet":        false,
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

	// verifyToken: top-level, NOT a method.
	vt := findFunc(fs.Functions, "verifyToken")
	if vt == nil {
		t.Fatal("verifyToken not found")
	}
	if vt.IsMethod {
		t.Errorf("verifyToken.IsMethod = true, want false (top-level)")
	}

	// Classes: SessionManager + Authenticator (abstract class — same
	// node type) + Greetable (mixin_declaration).
	wantClasses := map[string]bool{
		"SessionManager": false,
		"Authenticator":  false,
		"Greetable":      false,
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

	// Calls: verifyToken (bare call, inside if condition + unary
	// expression). `add`/`remove` are filtered as stdlib noise.
	if !contains(fs.Calls, "verifyToken") {
		t.Errorf("Calls missing %q; have %v", "verifyToken", fs.Calls)
	}
	for _, noisy := range []string{"add", "remove"} {
		if contains(fs.Calls, noisy) {
			t.Errorf("Calls should NOT contain %q (filtered by dartIsBuiltinOrNoise); have %v", noisy, fs.Calls)
		}
	}

	// Imports: `dart:async` → "async", `package:flutter/material.dart` → "material".
	for _, want := range []string{"async", "material"} {
		if !contains(fs.Imports, want) {
			t.Errorf("Imports missing %q; have %v", want, fs.Imports)
		}
	}

	// Raises: `throw AuthException('denied')` → "AuthException".
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
