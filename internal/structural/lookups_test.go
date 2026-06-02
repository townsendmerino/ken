package structural

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReferences pins the Track 2 `references` lookup behavior: all
// recognized syntactic occurrences (calls + imports + raises in
// Stage 8 v0), sorted by file then kind, with same-named entries
// from different files collapsed into one result list.
func TestReferences(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"defines_authenticate.py": `
def authenticate(user, pwd):
    return True
`,
		"calls_authenticate.py": `
def login():
    return authenticate("u", "p")
`,
		"imports_authenticate.py": `
from defines_authenticate import authenticate

def use():
    return authenticate("u", "p")
`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	ix, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}

	refs := ix.References("authenticate")
	// Expect:
	//   - calls_authenticate.py: Call
	//   - imports_authenticate.py: Call (also calls it) AND Import
	// (sorted by file then kind, Call=2, Import=3, so Call comes first)
	if len(refs) < 3 {
		t.Fatalf("References = %+v, want at least 3 (Call×2 + Import×1)", refs)
	}

	hasImport := false
	hasCallInCalls := false
	hasCallInImports := false
	for _, r := range refs {
		if r.File == "imports_authenticate.py" && r.Kind == ReferenceKindImport {
			hasImport = true
		}
		if r.File == "calls_authenticate.py" && r.Kind == ReferenceKindCall {
			hasCallInCalls = true
		}
		if r.File == "imports_authenticate.py" && r.Kind == ReferenceKindCall {
			hasCallInImports = true
		}
	}
	if !hasImport {
		t.Errorf("References missing Import in imports_authenticate.py; got %+v", refs)
	}
	if !hasCallInCalls {
		t.Errorf("References missing Call in calls_authenticate.py; got %+v", refs)
	}
	if !hasCallInImports {
		t.Errorf("References missing Call in imports_authenticate.py; got %+v", refs)
	}
}

// TestDefinition_MethodIndexing pins the Stage 8 v0+ behavior:
// methods are indexed under both their bare name AND the qualified
// Type.method form. Bare lookup returns all methods + any top-level
// matches; qualified lookup returns ONLY the targeted class's
// method. Kind labels distinguish function / class / method, and
// method sites carry QName for disambiguation.
func TestDefinition_MethodIndexing(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		// Top-level function named Login + a User class with a
		// method named Login. Bare "Login" should return both;
		// qualified "User.Login" should return only the method.
		"a.py": `
def Login():
    pass

class User:
    def Login(self):
        pass
`,
		// Different class with same bare method name. Bare
		// "Login" should also include this; qualified
		// "Admin.Login" should return only this.
		"b.py": `
class Admin:
    def Login(self):
        pass
`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ix, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Bare "Login": want 3 sites (top-level Login in a.py +
	// User.Login in a.py + Admin.Login in b.py)
	bare := ix.Definition("Login")
	if len(bare) != 3 {
		t.Fatalf("Definition(Login) = %d sites, want 3 (top-level + 2 methods); got %+v", len(bare), bare)
	}
	// One should be a Function, two should be Methods.
	var fnCount, methodCount int
	for _, s := range bare {
		switch s.Kind {
		case DefinitionKindFunction:
			fnCount++
		case DefinitionKindMethod:
			methodCount++
			if s.QName == "" {
				t.Errorf("method site missing QName; got %+v", s)
			}
		}
	}
	if fnCount != 1 {
		t.Errorf("Definition(Login) function count = %d, want 1", fnCount)
	}
	if methodCount != 2 {
		t.Errorf("Definition(Login) method count = %d, want 2", methodCount)
	}

	// Qualified "User.Login": want 1 method site
	qualUser := ix.Definition("User.Login")
	if len(qualUser) != 1 {
		t.Fatalf("Definition(User.Login) = %d sites, want 1; got %+v", len(qualUser), qualUser)
	}
	if qualUser[0].Kind != DefinitionKindMethod {
		t.Errorf("Definition(User.Login) Kind = %v, want Method", qualUser[0].Kind)
	}
	if qualUser[0].QName != "User.Login" {
		t.Errorf("Definition(User.Login) QName = %q, want User.Login", qualUser[0].QName)
	}
	if qualUser[0].File != "a.py" {
		t.Errorf("Definition(User.Login) File = %q, want a.py", qualUser[0].File)
	}

	// Qualified "Admin.Login": want 1 method site, in b.py
	qualAdmin := ix.Definition("Admin.Login")
	if len(qualAdmin) != 1 || qualAdmin[0].File != "b.py" {
		t.Errorf("Definition(Admin.Login) = %+v, want [b.py]", qualAdmin)
	}

	// Symbols() must NOT include methods or qualified names —
	// the symbol list stays top-level-only to keep agent-visible
	// vocabulary clean. Methods are reachable via Definition or
	// Outline.
	all := ix.Symbols()
	for _, n := range all {
		if n == "Login" {
			// Login top-level WAS a function in a.py, so its
			// bare form IS in defs and SHOULD be listed.
			continue
		}
		if strings.Contains(n, ".") {
			t.Errorf("Symbols() leaked a qualified name %q (qualified forms should NOT appear in the symbol list)", n)
		}
	}
}

func TestDefinition(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"a.py": "def foo(): pass\n",
		"b.py": "class foo: pass\n", // collision: foo defined as function AND class
		"c.py": "def bar(): pass\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ix, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}

	defsFoo := ix.Definition("foo")
	if len(defsFoo) != 2 {
		t.Fatalf("Definition(foo) = %+v, want 2 (collision: a.py + b.py)", defsFoo)
	}
	// Both files should appear; one as function, one as class
	kinds := map[string]DefinitionKind{}
	for _, d := range defsFoo {
		kinds[d.File] = d.Kind
	}
	if kinds["a.py"] != DefinitionKindFunction {
		t.Errorf("a.py:foo kind = %v, want Function", kinds["a.py"])
	}
	if kinds["b.py"] != DefinitionKindClass {
		t.Errorf("b.py:foo kind = %v, want Class", kinds["b.py"])
	}

	defsBar := ix.Definition("bar")
	if len(defsBar) != 1 {
		t.Errorf("Definition(bar) = %+v, want 1", defsBar)
	}

	defsMissing := ix.Definition("does_not_exist")
	if len(defsMissing) != 0 {
		t.Errorf("Definition(missing) = %+v, want nil", defsMissing)
	}
}

func TestOutline(t *testing.T) {
	dir := t.TempDir()
	src := `
def standalone(a, b):
    return a + b

class Session:
    def login(self, token):
        pass

    def logout(self):
        pass

def other(): pass
`
	if err := os.WriteFile(filepath.Join(dir, "x.py"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	ix, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	outline := ix.Outline("x.py")
	// Expect: standalone, other (top-level funcs), Session (class), login, logout (methods of Session)
	if len(outline) != 5 {
		t.Fatalf("Outline = %+v, want 5 entries", outline)
	}

	var names []string
	for _, e := range outline {
		names = append(names, e.Name)
	}
	wantNames := map[string]bool{"standalone": false, "other": false, "Session": false, "login": false, "logout": false}
	for _, n := range names {
		if _, ok := wantNames[n]; ok {
			wantNames[n] = true
		}
	}
	for n, found := range wantNames {
		if !found {
			t.Errorf("Outline missing %q; got %v", n, names)
		}
	}

	// Sessions methods should have Container=Session
	for _, e := range outline {
		if e.Name == "login" || e.Name == "logout" {
			if e.Container != "Session" {
				t.Errorf("Outline entry %s Container = %q, want Session", e.Name, e.Container)
			}
		}
	}
}

func TestSymbolsInPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "pkg_a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "pkg_b"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"pkg_a/x.py":   "def alpha(): pass\n",
		"pkg_a/y.py":   "def beta(): pass\n",
		"pkg_b/z.py":   "def gamma(): pass\n",
		"top_level.py": "def root_fn(): pass\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ix, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}

	all := ix.Symbols()
	if len(all) != 4 {
		t.Errorf("Symbols() = %v, want 4 (alpha, beta, gamma, root_fn)", all)
	}

	aOnly := ix.SymbolsInPath("pkg_a")
	if len(aOnly) != 2 {
		t.Errorf("SymbolsInPath(pkg_a) = %v, want 2 (alpha, beta)", aOnly)
	}
	for _, n := range aOnly {
		if n != "alpha" && n != "beta" {
			t.Errorf("Unexpected symbol in pkg_a: %s", n)
		}
	}

	bOnly := ix.SymbolsInPath("pkg_b")
	if len(bOnly) != 1 || bOnly[0] != "gamma" {
		t.Errorf("SymbolsInPath(pkg_b) = %v, want [gamma]", bOnly)
	}
}
