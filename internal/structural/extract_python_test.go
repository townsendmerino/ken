package structural

import (
	"os"
	"path/filepath"
	"testing"
)

// TestBuild_PythonBasics confirms the Python extractor lights up on
// top-level functions, class methods, calls, raises, and classes.
// Python predates docs/internal/add-a-language.md's mandatory-test-file
// step; this backfills the one non-compliant language.
func TestBuild_PythonBasics(t *testing.T) {
	dir := t.TempDir()
	src := `import hashlib
from typing import Optional


class AuthError(Exception):
    pass


def authenticate(user, password):
    token = hashlib.sha256(password.encode()).hexdigest()
    if not verify(user, token):
        raise AuthError("denied")
    return True


class SessionManager:
    def __init__(self):
        self.active = set()

    def login(self, user):
        self.active.add(user)
        return authenticate(user, user.password)

    def logout(self, user):
        self.active.discard(user)
`
	if err := os.WriteFile(filepath.Join(dir, "auth.py"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	ix, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	fs := ix.File("auth.py")
	if fs == nil {
		t.Fatal("auth.py not indexed")
	}

	// Functions: authenticate (top-level) + __init__/login/logout (methods
	// of SessionManager).
	wantFuncs := map[string]bool{"authenticate": false, "__init__": false, "login": false, "logout": false}
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

	// authenticate: top-level, params (user, password).
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

	// login: method on SessionManager.
	login := findFunc(fs.Functions, "login")
	if login == nil {
		t.Fatal("login not found")
	}
	if !login.IsMethod {
		t.Errorf("login.IsMethod = false, want true (defined in a class)")
	}
	if login.EnclosingClass != "SessionManager" {
		t.Errorf("login.EnclosingClass = %q, want SessionManager", login.EnclosingClass)
	}

	// Calls (leaf names): authenticate (from login), verify (from
	// authenticate). `obj.method(...)` contributes the leaf, so add/discard
	// show up too — assert the two that carry meaning.
	for _, want := range []string{"authenticate", "verify"} {
		if !contains(fs.CalleeNames(), want) {
			t.Errorf("Calls missing %q; have %v", want, fs.CalleeNames())
		}
	}

	// Raises: `raise AuthError("denied")` → "AuthError".
	if !contains(fs.Raises, "AuthError") {
		t.Errorf("Raises missing 'AuthError'; have %v", fs.Raises)
	}

	// Classes: AuthError + SessionManager.
	wantClasses := map[string]bool{"AuthError": false, "SessionManager": false}
	for _, c := range fs.Classes {
		if _, ok := wantClasses[c.Name]; ok {
			wantClasses[c.Name] = true
		}
	}
	for n, found := range wantClasses {
		if !found {
			t.Errorf("Classes missing %q", n)
		}
	}
}
