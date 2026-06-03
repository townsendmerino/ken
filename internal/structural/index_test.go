package structural

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestBuild_BasicPython smoke-tests the Python extractor + Build()
// pipeline on a handcrafted source file. Confirms function /
// imports / calls / raises / signature extraction all light up.
// If this fails, all downstream Stage 8 work is built on sand.
func TestBuild_BasicPython(t *testing.T) {
	dir := t.TempDir()
	src := `
import os
from foo import bar as renamed_bar

def authenticate(username: str, password: str) -> bool:
    """Auth a user."""
    user = lookup_user(username)
    if not user:
        raise UserNotFoundError("no such user")
    if not check_hash(user.pwd, password):
        raise InvalidCredentialsError()
    return True

class Session:
    def login(self, token: str) -> bool:
        verify_token(token)
        return True

    def logout(self):
        revoke_token()
`
	if err := os.WriteFile(filepath.Join(dir, "auth.py"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	ix, err := Build(dir)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	fs := ix.File("auth.py")
	if fs == nil {
		t.Fatal("auth.py not indexed")
	}

	// Functions: authenticate (top-level) + login + logout (methods)
	if got := len(fs.Functions); got != 3 {
		t.Errorf("Functions count = %d, want 3 (authenticate, login, logout); got %+v",
			got, funcNames(fs.Functions))
	}
	// First function is the top-level authenticate
	if fs.Functions[0].Name != "authenticate" {
		t.Errorf("Functions[0].Name = %q, want authenticate", fs.Functions[0].Name)
	}
	if fs.Functions[0].IsMethod {
		t.Errorf("authenticate marked as method")
	}
	wantParams := []string{"username", "password"}
	if !sliceEq(fs.Functions[0].Params, wantParams) {
		t.Errorf("authenticate params = %v, want %v", fs.Functions[0].Params, wantParams)
	}
	if !strings.Contains(fs.Functions[0].ReturnType, "bool") {
		t.Errorf("authenticate return type = %q, want it to contain 'bool'", fs.Functions[0].ReturnType)
	}

	// Classes
	if got := len(fs.Classes); got != 1 {
		t.Errorf("Classes count = %d, want 1 (Session)", got)
	}
	if fs.Classes[0].Name != "Session" {
		t.Errorf("Classes[0].Name = %q, want Session", fs.Classes[0].Name)
	}
	if got := len(fs.Classes[0].Methods); got != 2 {
		t.Errorf("Session methods = %d, want 2 (login, logout)", got)
	}

	// Calls: lookup_user, check_hash, verify_token, revoke_token
	// (built-ins like 'not' or attribute accesses like user.pwd
	// don't count — call nodes only).
	for _, want := range []string{"lookup_user", "check_hash", "verify_token", "revoke_token"} {
		if !contains(fs.CalleeNames(), want) {
			t.Errorf("Calls missing %q; have %v", want, fs.CalleeNames())
		}
	}

	// Raises
	for _, want := range []string{"UserNotFoundError", "InvalidCredentialsError"} {
		if !contains(fs.Raises, want) {
			t.Errorf("Raises missing %q; have %v", want, fs.Raises)
		}
	}

	// Imports: os + renamed_bar (the alias, not the original)
	for _, want := range []string{"os", "renamed_bar"} {
		if !contains(fs.Imports, want) {
			t.Errorf("Imports missing %q; have %v", want, fs.Imports)
		}
	}

	// Defs index: authenticate + Session are top-level defs
	if defs := ix.Defs("authenticate"); !contains(defs, "auth.py") {
		t.Errorf("Defs(authenticate) missing auth.py; got %v", defs)
	}
	if defs := ix.Defs("Session"); !contains(defs, "auth.py") {
		t.Errorf("Defs(Session) missing auth.py; got %v", defs)
	}

	// Reverse call graph: lookup_user is called from auth.py
	if cs := ix.Callers("lookup_user"); len(cs) != 1 || cs[0].File != "auth.py" {
		t.Errorf("Callers(lookup_user) = %v, want [auth.py]", cs)
	}
}

// TestEnrich_ArmBBaseline_FormatStability pins the M0d Arm B baseline
// format: "# func: NAME | calls: A, B | raises: X". This is the format
// the production extractor MUST produce when EnrichOptions is the zero
// value, so the Go and Python materializers stay byte-identical.
func TestEnrich_ArmBBaseline_FormatStability(t *testing.T) {
	dir := t.TempDir()
	src := `
def sina_xml_to_url_list(xml_data):
    rawurl = []
    dom = parseString(xml_data)
    for node in dom.getElementsByTagName('durl'):
        url = node.getElementsByTagName('url')[0]
        rawurl.append(url.childNodes[0].data)
    return rawurl
`
	if err := os.WriteFile(filepath.Join(dir, "q265734.py"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	ix, err := Build(dir)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	got := ix.Enrich("q265734.py", EnrichOptions{})
	want := "# func: sina_xml_to_url_list | calls: parseString, getElementsByTagName, append\n"
	if got != want {
		t.Errorf("Enrich(Arm B baseline) mismatch:\n  got:  %q\n  want: %q", got, want)
	}
}

// TestEnrich_AdditiveArms checks each opts flag emits its section
// when applicable and is absent otherwise.
func TestEnrich_AdditiveArms(t *testing.T) {
	dir := t.TempDir()
	// Caller file: defines `consumer` which calls into `producer`.
	caller := `
def consumer():
    return producer()
`
	// Producer file: defines `producer`, imports os, has a method-
	// laden class. The Stage 8 arms surface different facts of this
	// file's structure.
	producer := `
import os
from typing import List

def producer(input_path: str) -> List[str]:
    files = scan_dir(input_path)
    return files
`
	if err := os.WriteFile(filepath.Join(dir, "caller.py"), []byte(caller), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "producer.py"), []byte(producer), 0o644); err != nil {
		t.Fatal(err)
	}
	ix, err := Build(dir)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	t.Run("Callers adds 'called by'", func(t *testing.T) {
		got := ix.Enrich("producer.py", EnrichOptions{Callers: true})
		if !strings.Contains(got, "called by: caller") {
			t.Errorf("Enrich(Callers) missing 'called by: caller'; got %q", got)
		}
	})
	t.Run("Imports adds 'imports'", func(t *testing.T) {
		got := ix.Enrich("producer.py", EnrichOptions{Imports: true})
		if !strings.Contains(got, "imports: ") {
			t.Errorf("Enrich(Imports) missing 'imports:'; got %q", got)
		}
		if !strings.Contains(got, "os") {
			t.Errorf("Enrich(Imports) missing 'os'; got %q", got)
		}
	})
	t.Run("Signature adds 'params' and 'returns'", func(t *testing.T) {
		got := ix.Enrich("producer.py", EnrichOptions{Signature: true})
		if !strings.Contains(got, "params: input_path") {
			t.Errorf("Enrich(Signature) missing 'params: input_path'; got %q", got)
		}
		if !strings.Contains(got, "returns:") {
			t.Errorf("Enrich(Signature) missing 'returns:'; got %q", got)
		}
	})
	t.Run("Baseline excludes additive sections", func(t *testing.T) {
		got := ix.Enrich("producer.py", EnrichOptions{})
		if strings.Contains(got, "called by") || strings.Contains(got, "imports:") ||
			strings.Contains(got, "params:") || strings.Contains(got, "returns:") {
			t.Errorf("Enrich(zero opts) leaked additive section; got %q", got)
		}
	})
}

func funcNames(fs []FuncDef) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.Name
	}
	return out
}

func contains(haystack []string, needle string) bool {
	return slices.Contains(haystack, needle)
}

func sliceEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
