package structural

import (
	"os"
	"path/filepath"
	"testing"
)

// TestBuild_CBasics confirms the C extractor (which reuses the C++
// walker — the shared node types overlap cleanly) handles free
// functions, structs (no methods in C, just type+fields), parameters
// wrapped in pointer_declarator, and calls.
//
// C-specific routing: .c and .h both route to the "c" grammar.
// .h is shared with C++ in real codebases — routing it to C
// produces incomplete extraction for C++ headers (templates,
// namespaces, classes silently degrade) but doesn't crash.
func TestBuild_CBasics(t *testing.T) {
	dir := t.TempDir()
	src := `#include <stdlib.h>
#include "user.h"

struct SessionManager {
	int count;
	User* users;
};

int authenticate(User* u, const char* password) {
	return verify_token(u->id, password);
}

void login(struct SessionManager* mgr, User* u) {
	mgr->users[mgr->count++] = *u;
}

void log_event(const char* msg) {
	printf("event: %s\n", msg);
}
`
	if err := os.WriteFile(filepath.Join(dir, "session.c"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	ix, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	fs := ix.File("session.c")
	if fs == nil {
		t.Fatal("session.c not indexed")
	}

	// Functions: authenticate, login, log_event.
	wantFuncs := map[string]bool{"authenticate": false, "login": false, "log_event": false}
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

	// authenticate: top-level (not a method — C has no methods),
	// params unwrap through pointer_declarator.
	auth := findFunc(fs.Functions, "authenticate")
	if auth == nil {
		t.Fatal("authenticate not found")
	}
	if auth.IsMethod {
		t.Errorf("authenticate.IsMethod = true, want false (C has no methods)")
	}
	if !sliceEq(auth.Params, []string{"u", "password"}) {
		t.Errorf("authenticate.Params = %v, want [u password]", auth.Params)
	}

	// Classes (in the abstract sense — struct counts).
	hasClass := false
	for _, c := range fs.Classes {
		if c.Name == "SessionManager" {
			hasClass = true
		}
	}
	if !hasClass {
		t.Errorf("Classes missing SessionManager; got %+v", fs.Classes)
	}

	// Calls: verify_token. printf is in the noise filter (extended
	// from the cppIsBuiltinOrNoise list).
	if !contains(fs.CalleeNames(), "verify_token") {
		t.Errorf("Calls missing 'verify_token'; have %v", fs.CalleeNames())
	}
	if contains(fs.CalleeNames(), "printf") {
		t.Errorf("Calls should NOT contain 'printf' (filtered as C stdlib); have %v", fs.CalleeNames())
	}

	// Imports: #include surfaces as bound names — `<stdlib.h>` →
	// "stdlib" (system_lib_string), `"user.h"` → "user"
	// (string_literal). Directory prefix + extension stripped.
	for _, want := range []string{"stdlib", "user"} {
		if !contains(fs.Imports, want) {
			t.Errorf("Imports missing %q; have %v", want, fs.Imports)
		}
	}

	// Definition lookups.
	if defs := ix.Definition("authenticate"); len(defs) != 1 || defs[0].Kind != DefinitionKindFunction {
		t.Errorf("Definition(authenticate) = %+v, want one Function site", defs)
	}
	if defs := ix.Definition("SessionManager"); len(defs) < 1 {
		t.Errorf("Definition(SessionManager) = empty, want at least one Class site")
	}
}

// TestBuild_CHeaderExt confirms .h files route through the C
// grammar.
func TestBuild_CHeaderExt(t *testing.T) {
	dir := t.TempDir()
	src := `void api_init(void);
int api_run(int argc, char** argv);
`
	if err := os.WriteFile(filepath.Join(dir, "api.h"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	ix, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, fn := range []string{"api_init", "api_run"} {
		if defs := ix.Definition(fn); len(defs) != 1 || defs[0].File != "api.h" {
			t.Errorf("Definition(%s) = %v, want [api.h]", fn, defs)
		}
	}
}
