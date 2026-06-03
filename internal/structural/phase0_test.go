package structural

import (
	"os"
	"path/filepath"
	"testing"
)

// TestPhase0_FieldsPopulated is the cross-language regression net for
// the Phase 0 substrate (FuncDef.StartLine/EndLine, ClassDef
// StartLine/EndLine, CallRef.Line, CallRef.EnclosingSymbol). The
// existing extract_<lang>_test.go suite asserts backward compat —
// that classes / funcs / CalleeNames() outputs are unchanged from
// the pre-Phase-0 shape. This test asserts the NEW fields actually
// populate: a future change that accidentally drops the
// enclosingSymbol thread or skips a fillSpan() call won't slip past
// CI.
//
// Three languages cover the three major walker shapes:
//
//   - Python: field-name-driven (`function_definition`, `call`,
//     `class_definition`).
//   - Go: similar field-name shape but with method_declaration's
//     receiver type as enclosingClass.
//   - Java: navigation-style + braces + method_invocation rather
//     than `call`.
//
// Each fixture has at least one top-level function, one class with a
// method, and one method call whose enclosing-symbol value is
// known. The assertions are written so adding a new language to the
// matrix is a 5-minute edit.
func TestPhase0_FieldsPopulated(t *testing.T) {
	cases := []struct {
		lang               string
		filename           string
		source             string
		wantClass          string
		wantClassSpan      bool
		wantTopLevel       string // top-level function name
		wantMethod         string // method name on wantClass
		wantMethodEnc      string // expected EnclosingSymbol for calls inside wantMethod
		wantCalleeOK       string // a callee name we expect inside wantMethod
		wantTopLevelCallee string // a callee name we expect from wantTopLevel
		wantTopLevelEnc    string // expected EnclosingSymbol for calls inside wantTopLevel
	}{
		{
			lang:     "python",
			filename: "session.py",
			source: `import time

def verify_token(token):
    return time.time() > 0

class SessionManager:
    def login(self, user, password):
        if not verify_token(password):
            raise AuthError("denied")
        return True
`,
			wantClass:          "SessionManager",
			wantClassSpan:      true,
			wantTopLevel:       "verify_token",
			wantMethod:         "login",
			wantMethodEnc:      "SessionManager.login",
			wantCalleeOK:       "verify_token",
			wantTopLevelCallee: "time",
			wantTopLevelEnc:    "verify_token",
		},
		{
			lang:     "go",
			filename: "session.go",
			source: `package auth

import "fmt"

type SessionManager struct{}

func verifyToken(s string) bool {
	return fmt.Sprintln(s) != ""
}

func (m *SessionManager) Login(user, password string) error {
	if !verifyToken(password) {
		return fmt.Errorf("denied")
	}
	return nil
}
`,
			wantClass:     "SessionManager",
			wantClassSpan: true,
			wantTopLevel:  "verifyToken",
			wantMethod:    "Login",
			wantMethodEnc: "SessionManager.Login",
			wantCalleeOK:  "verifyToken",
			// fmt.Sprintln is filtered by goIsBuiltinOrNoise (Sprintln),
			// so we don't assert a top-level callee for Go — just
			// confirm the EnclosingSymbol thread works on the method.
		},
		{
			lang:     "java",
			filename: "Session.java",
			source: `package com.example;

class SessionManager {
	boolean verifyToken(String token) {
		return token.length() > 0;
	}

	boolean login(String user, String password) {
		if (!verifyToken(password)) {
			throw new AuthException("denied");
		}
		return true;
	}
}
`,
			wantClass:     "SessionManager",
			wantClassSpan: true,
			wantTopLevel:  "", // Java has no top-level funcs at this fixture
			wantMethod:    "login",
			wantMethodEnc: "SessionManager.login",
			wantCalleeOK:  "verifyToken",
		},
	}

	for _, tc := range cases {
		t.Run(tc.lang, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, tc.filename), []byte(tc.source), 0o644); err != nil {
				t.Fatal(err)
			}
			ix, err := Build(dir)
			if err != nil {
				t.Fatal(err)
			}
			fs := ix.File(tc.filename)
			if fs == nil {
				t.Fatalf("%s not indexed", tc.filename)
			}

			// === ClassDef spans ===
			if tc.wantClass != "" {
				cls := findClass(fs.Classes, tc.wantClass)
				if cls == nil {
					t.Fatalf("class %q not found in %v", tc.wantClass, fs.Classes)
				}
				if tc.wantClassSpan && cls.StartLine == 0 {
					t.Errorf("%s class StartLine == 0; want non-zero (fillSpan not called?)", tc.wantClass)
				}
				if cls.EndLine < cls.StartLine {
					t.Errorf("%s class EndLine (%d) < StartLine (%d)", tc.wantClass, cls.EndLine, cls.StartLine)
				}
			}

			// === FuncDef spans on a top-level func ===
			if tc.wantTopLevel != "" {
				top := findFunc(fs.Functions, tc.wantTopLevel)
				if top == nil {
					t.Fatalf("top-level func %q not found", tc.wantTopLevel)
				}
				if top.StartLine == 0 {
					t.Errorf("%s.StartLine == 0; want non-zero", tc.wantTopLevel)
				}
				if top.EndLine < top.StartLine {
					t.Errorf("%s EndLine (%d) < StartLine (%d)", tc.wantTopLevel, top.EndLine, top.StartLine)
				}
				if top.IsMethod {
					t.Errorf("%s IsMethod=true; want false (top-level func)", tc.wantTopLevel)
				}
			}

			// === FuncDef spans on a method ===
			m := findFunc(fs.Functions, tc.wantMethod)
			if m == nil {
				t.Fatalf("method %q not found in %v", tc.wantMethod, funcNames(fs.Functions))
			}
			if m.StartLine == 0 {
				t.Errorf("method %s.StartLine == 0; want non-zero", tc.wantMethod)
			}
			if m.EndLine < m.StartLine {
				t.Errorf("method %s EndLine (%d) < StartLine (%d)", tc.wantMethod, m.EndLine, m.StartLine)
			}
			if !m.IsMethod {
				t.Errorf("method %s.IsMethod=false; want true", tc.wantMethod)
			}
			if m.EnclosingClass != tc.wantClass {
				t.Errorf("method %s.EnclosingClass=%q; want %q", tc.wantMethod, m.EnclosingClass, tc.wantClass)
			}

			// === CallRef attribution: a call site inside the method
			//     must carry EnclosingSymbol = "ClassName.methodName"
			//     and a non-zero Line. ===
			methodCalls := callRefsByEnclosing(fs.CallRefs, tc.wantMethodEnc)
			if len(methodCalls) == 0 {
				t.Fatalf("no CallRef records found with EnclosingSymbol=%q; have %s",
					tc.wantMethodEnc, callRefSummary(fs.CallRefs))
			}
			foundCallee := false
			for _, r := range methodCalls {
				if r.Line == 0 {
					t.Errorf("CallRef inside %s has Line=0; want non-zero (appendCall not threading callNode?): %+v",
						tc.wantMethodEnc, r)
				}
				if r.Callee == tc.wantCalleeOK {
					foundCallee = true
				}
			}
			if !foundCallee {
				calls := make([]string, 0, len(methodCalls))
				for _, r := range methodCalls {
					calls = append(calls, r.Callee)
				}
				t.Errorf("expected callee %q inside %s; got callees %v",
					tc.wantCalleeOK, tc.wantMethodEnc, calls)
			}

			// === Top-level callee attribution (if the fixture has
			//     a top-level callee worth checking) ===
			if tc.wantTopLevelCallee != "" && tc.wantTopLevelEnc != "" {
				topCalls := callRefsByEnclosing(fs.CallRefs, tc.wantTopLevelEnc)
				foundTop := false
				for _, r := range topCalls {
					if r.Callee == tc.wantTopLevelCallee {
						foundTop = true
						break
					}
				}
				if !foundTop {
					calls := make([]string, 0, len(topCalls))
					for _, r := range topCalls {
						calls = append(calls, r.Callee)
					}
					t.Errorf("expected callee %q inside %s; got callees %v",
						tc.wantTopLevelCallee, tc.wantTopLevelEnc, calls)
				}
			}
		})
	}
}

// TestPhase0_CalleeNamesByteIdentical is the explicit guard against
// the Arm B byte-identity invariant — every test in the
// extract_<lang>_test.go suite has been migrated from `fs.Calls` to
// `fs.CalleeNames()`, but a future refactor that accidentally
// changes CalleeNames()'s dedup order would silently break Arm B
// retrieval (which gates on byte-identical labels). This test asserts
// the contract: appending the SAME callee multiple times into
// CallRefs collapses to ONE entry in CalleeNames() output, in
// first-appearance order.
func TestPhase0_CalleeNamesByteIdentical(t *testing.T) {
	fs := &FileStruct{
		CallRefs: []CallRef{
			{Callee: "foo", Line: 10},
			{Callee: "bar", Line: 11},
			{Callee: "foo", Line: 12}, // dup; should drop
			{Callee: "baz", Line: 13},
			{Callee: "bar", Line: 14}, // dup; should drop
			{Callee: "", Line: 15},    // empty; should drop
		},
	}
	got := fs.CalleeNames()
	want := []string{"foo", "bar", "baz"}
	if !sliceEq(got, want) {
		t.Errorf("CalleeNames() = %v; want %v (first-appearance dedup, empties dropped)", got, want)
	}

	// Empty CallRefs → empty (NOT nil panic).
	empty := &FileStruct{}
	if names := empty.CalleeNames(); len(names) != 0 {
		t.Errorf("CalleeNames() on empty FileStruct = %v; want empty", names)
	}

	// nil receiver → nil (defensive; matches the implementation).
	var nilFS *FileStruct
	if names := nilFS.CalleeNames(); names != nil {
		t.Errorf("CalleeNames() on nil FileStruct = %v; want nil", names)
	}
}

// findClass returns a pointer to the named class, or nil if absent.
func findClass(cs []ClassDef, name string) *ClassDef {
	for i := range cs {
		if cs[i].Name == name {
			return &cs[i]
		}
	}
	return nil
}

// callRefsByEnclosing returns every CallRef whose EnclosingSymbol
// matches the argument. Empty enclosingSym matches file-top-level
// CallRefs (i.e. EnclosingSymbol == "").
func callRefsByEnclosing(refs []CallRef, enclosingSym string) []CallRef {
	out := make([]CallRef, 0)
	for _, r := range refs {
		if r.EnclosingSymbol == enclosingSym {
			out = append(out, r)
		}
	}
	return out
}

// callRefSummary returns a short string summarising the CallRefs by
// EnclosingSymbol → callee count. Used in failure messages so an
// unexpected enclosing-symbol value is obvious.
func callRefSummary(refs []CallRef) string {
	if len(refs) == 0 {
		return "(no CallRefs)"
	}
	byEnc := make(map[string][]string)
	for _, r := range refs {
		byEnc[r.EnclosingSymbol] = append(byEnc[r.EnclosingSymbol], r.Callee)
	}
	out := ""
	for enc, callees := range byEnc {
		if out != "" {
			out += " | "
		}
		out += "[enc=" + enc + " → "
		for i, c := range callees {
			if i > 0 {
				out += ","
			}
			out += c
		}
		out += "]"
	}
	return out
}
