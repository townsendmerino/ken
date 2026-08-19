package structural

import (
	"os"
	"path/filepath"
	"testing"
)

// TestBuild_ScalaBasics confirms the Scala extractor handles class/object/trait
// definitions, methods, top-level defs, calls (bare + receiver.method),
// `throw new X` → Raises, and imports.
func TestBuild_ScalaBasics(t *testing.T) {
	dir := t.TempDir()
	src := `package auth
import scala.collection.mutable.Set

class SessionManager {
  val active = Set[String]()
  def login(user: String): Boolean = {
    active.add(user)
    verifyToken(user)
    true
  }
  def fail(): Unit = throw new AuthError("denied")
}

object Auth {
  def authenticate(user: String): Unit = verifyToken(user)
}

trait Guarded {
  def check(): Boolean
}
`
	if err := os.WriteFile(filepath.Join(dir, "auth.scala"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	ix, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	fs := ix.File("auth.scala")
	if fs == nil {
		t.Fatal("no FileStruct for auth.scala")
	}

	funcs := map[string]bool{}
	for _, f := range fs.Functions {
		funcs[f.Name] = true
	}
	for _, want := range []string{"login", "fail", "authenticate", "check"} {
		if !funcs[want] {
			t.Errorf("missing function %q; got %v", want, funcs)
		}
	}

	classes := map[string]bool{}
	for _, c := range fs.Classes {
		classes[c.Name] = true
	}
	for _, want := range []string{"SessionManager", "Auth", "Guarded"} {
		if !classes[want] {
			t.Errorf("missing class/object/trait %q; got %v", want, classes)
		}
	}

	calls := map[string]bool{}
	for _, c := range fs.CalleeNames() {
		calls[c] = true
	}
	if !calls["add"] || !calls["verifyToken"] {
		t.Errorf("expected add + verifyToken in calls; got %v", calls)
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

	var importsSet bool
	for _, im := range fs.Imports {
		if im == "Set" {
			importsSet = true
		}
	}
	if !importsSet {
		t.Errorf("expected Set (leaf of scala.collection.mutable.Set) in Imports; got %v", fs.Imports)
	}
}
