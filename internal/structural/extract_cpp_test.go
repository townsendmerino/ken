package structural

import (
	"os"
	"path/filepath"
	"testing"
)

// TestBuild_CppBasics confirms the C++ extractor handles classes
// with inline AND out-of-line method definitions, namespaces (which
// don't change enclosingClass scope), parameter declarators wrapped
// in reference / pointer / const modifiers, member-of-namespace
// function calls (qualified_identifier in the function field), and
// throw expressions.
func TestBuild_CppBasics(t *testing.T) {
	dir := t.TempDir()
	src := `#include <vector>
#include <string>

namespace auth {

class SessionManager {
public:
	SessionManager() = default;
	bool login(const User& u);
	void logout(const std::string& id);

private:
	std::vector<User> active_;
};

bool authenticate(const User& u, const std::string& password) {
	return verifyToken(u.id, password);
}

bool SessionManager::login(const User& u) {
	active_.push_back(u);
	return true;
}

void fail() {
	throw AuthError("denied");
}

} // namespace auth
`
	if err := os.WriteFile(filepath.Join(dir, "session.cpp"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	ix, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	fs := ix.File("session.cpp")
	if fs == nil {
		t.Fatal("session.cpp not indexed")
	}

	// Functions: authenticate (free, in namespace), login + logout
	// (forward-declared inside class), login (out-of-line definition),
	// fail (free), SessionManager (default constructor).
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

	// Out-of-line SessionManager::login MUST surface as a method
	// of SessionManager (EnclosingClass set via the
	// qualified_identifier scope).
	hasMethodLogin := false
	for _, fn := range fs.Functions {
		if fn.Name == "login" && fn.EnclosingClass == "SessionManager" {
			hasMethodLogin = true
		}
	}
	if !hasMethodLogin {
		t.Errorf("login NOT recorded as method of SessionManager; functions = %+v", funcNames(fs.Functions))
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

	// Calls: verifyToken (free call inside authenticate).
	// push_back is filtered as STL noise.
	if !contains(fs.Calls, "verifyToken") {
		t.Errorf("Calls missing 'verifyToken'; have %v", fs.Calls)
	}
	if contains(fs.Calls, "push_back") {
		t.Errorf("Calls should NOT contain 'push_back' (STL noise filter); have %v", fs.Calls)
	}

	// Raises: throw AuthError(...) → "AuthError"
	if !contains(fs.Raises, "AuthError") {
		t.Errorf("Raises missing 'AuthError'; have %v", fs.Raises)
	}

	// Definition lookups: SessionManager (class), qualified method.
	defs := ix.Definition("SessionManager")
	if len(defs) < 1 {
		t.Errorf("Definition(SessionManager) = empty, want at least the class")
	}
	mDef := ix.Definition("SessionManager.login")
	if len(mDef) < 1 {
		t.Errorf("Definition(SessionManager.login) = %+v, want at least one Method site", mDef)
	} else {
		foundMethod := false
		for _, d := range mDef {
			if d.Kind == DefinitionKindMethod && d.QName == "SessionManager.login" {
				foundMethod = true
			}
		}
		if !foundMethod {
			t.Errorf("Definition(SessionManager.login) = %+v, want a Method-kind site with QName SessionManager.login", mDef)
		}
	}
}
