package structural

import (
	"os"
	"path/filepath"
	"testing"
)

// TestBuild_RubyBasics confirms the Ruby extractor handles classes
// inside modules (treats both as ClassDef shells), instance methods,
// singleton_method (`def self.foo`) class-methods, calls (Ruby's
// universal `call` node), and raise-as-call-special-case.
func TestBuild_RubyBasics(t *testing.T) {
	dir := t.TempDir()
	src := `require 'set'

module Auth
	class SessionManager
		def initialize
			@active = Set.new
		end

		def login(user)
			@active.add(user)
			true
		end

		def logout(user)
			@active.delete(user)
		end

		def fail_one
			raise AuthError, "denied"
		end
	end

	def self.authenticate(user, password)
		verify_token(user.id, password)
	end
end
`
	if err := os.WriteFile(filepath.Join(dir, "auth.rb"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	ix, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	fs := ix.File("auth.rb")
	if fs == nil {
		t.Fatal("auth.rb not indexed")
	}

	// Functions: initialize + login + logout + fail_one (methods
	// on SessionManager) + authenticate (singleton on the Auth
	// module).
	wantFuncs := map[string]bool{"initialize": false, "login": false, "logout": false, "fail_one": false, "authenticate": false}
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

	// login: method, enclosing class = SessionManager, params = [user]
	login := findFunc(fs.Functions, "login")
	if login == nil {
		t.Fatal("login not found")
	}
	if login.EnclosingClass != "SessionManager" {
		t.Errorf("login.EnclosingClass = %q, want SessionManager", login.EnclosingClass)
	}
	if !sliceEq(login.Params, []string{"user"}) {
		t.Errorf("login.Params = %v, want [user]", login.Params)
	}

	// authenticate: singleton on Auth (the module wraps the class)
	auth := findFunc(fs.Functions, "authenticate")
	if auth == nil {
		t.Fatal("authenticate not found")
	}
	if auth.EnclosingClass != "Auth" {
		t.Errorf("authenticate.EnclosingClass = %q, want Auth (the module)", auth.EnclosingClass)
	}
	if !sliceEq(auth.Params, []string{"user", "password"}) {
		t.Errorf("authenticate.Params = %v, want [user password]", auth.Params)
	}

	// Classes: Auth (module) + SessionManager (class).
	wantClasses := map[string]bool{"Auth": false, "SessionManager": false}
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

	// Calls: verify_token (free in singleton). add / delete /
	// require / new are all in the noise filter.
	if !contains(fs.Calls, "verify_token") {
		t.Errorf("Calls missing 'verify_token'; have %v", fs.Calls)
	}
	for _, noise := range []string{"add", "delete", "require", "new"} {
		if contains(fs.Calls, noise) {
			t.Errorf("Calls should NOT contain %q (Ruby kernel/common noise); have %v", noise, fs.Calls)
		}
	}

	// Raises: `raise AuthError, "denied"` → "AuthError"
	if !contains(fs.Raises, "AuthError") {
		t.Errorf("Raises missing 'AuthError'; have %v", fs.Raises)
	}

	// Definition lookups: top-level class + qualified method.
	defs := ix.Definition("SessionManager")
	if len(defs) != 1 || defs[0].Kind != DefinitionKindClass {
		t.Errorf("Definition(SessionManager) = %+v, want one Class site", defs)
	}
	mDef := ix.Definition("SessionManager.login")
	if len(mDef) != 1 || mDef[0].Kind != DefinitionKindMethod {
		t.Errorf("Definition(SessionManager.login) = %+v, want one Method site", mDef)
	}
}
