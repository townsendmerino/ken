package structural

import (
	"os"
	"path/filepath"
	"testing"
)

// TestBuild_SwiftBasics confirms the Swift extractor (un-parked 2026-08-19)
// still maps correctly onto the pinned gotreesitter v0.48.1 tree-sitter-swift
// node types: classes, methods, top-level funcs, init, calls, `throw` → Raises,
// and imports.
func TestBuild_SwiftBasics(t *testing.T) {
	dir := t.TempDir()
	src := `import Foundation

enum AuthError: Error { case denied }

class SessionManager {
    var active: Set<String> = []

    init() {}

    func login(user: String) throws -> Bool {
        active.insert(user)
        verifyToken(user)
        return true
    }

    func logout(_ user: String) {
        active.remove(user)
    }
}

func authenticate(user: String, password: String) throws {
    throw AuthError.denied
}
`
	if err := os.WriteFile(filepath.Join(dir, "auth.swift"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	ix, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	fs := ix.File("auth.swift")
	if fs == nil {
		t.Fatal("no FileStruct for auth.swift — extractor not registered or file skipped")
	}

	funcNames := map[string]bool{}
	for _, f := range fs.Functions {
		funcNames[f.Name] = true
	}
	for _, want := range []string{"login", "logout", "authenticate"} {
		if !funcNames[want] {
			t.Errorf("missing function %q; got %v", want, funcNames)
		}
	}

	var hasSession bool
	for _, c := range fs.Classes {
		if c.Name == "SessionManager" {
			hasSession = true
		}
	}
	if !hasSession {
		t.Errorf("missing class SessionManager; got %d classes", len(fs.Classes))
	}

	calls := map[string]bool{}
	for _, c := range fs.CalleeNames() {
		calls[c] = true
	}
	if !calls["insert"] && !calls["verifyToken"] && !calls["remove"] {
		t.Errorf("expected some of insert/verifyToken/remove in calls; got %v", calls)
	}

	var raisesAuth bool
	for _, r := range fs.Raises {
		if r == "AuthError" {
			raisesAuth = true
		}
	}
	if !raisesAuth {
		t.Errorf("expected AuthError in Raises; got %v", fs.Raises)
	}

	var importsFoundation bool
	for _, im := range fs.Imports {
		if im == "Foundation" {
			importsFoundation = true
		}
	}
	if !importsFoundation {
		t.Errorf("expected Foundation in Imports; got %v", fs.Imports)
	}
}
