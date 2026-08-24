package structural

import "testing"

// TestBuild_CsharpBasics confirms the C# extractor lights up on classes,
// interfaces, methods, constructors, params, invocation + object-creation
// calls, throw → raises, and using-directive imports.
//
// It is also a REGRESSION GUARD for the OOM that parked C# until
// gotreesitter v0.20.2: the source below uses the exact minimal trigger
// from docs/internal/csharp-oom-root-cause.md — a block-scoped namespace plus a
// call whose first arg is an identifier and a later arg is a bitwise-or
// of member-accesses (`Configure(u, Flags.A | Flags.B)`). On v0.20.0-rc3
// this shape drove unbounded namespace-recovery recursion to SIGKILL;
// v0.20.2 bounds it. If this test ever hangs or OOMs, the grammar
// regressed.
//
// NOTE: .cs is currently PARKED out of kenLangToTSLang for a SECOND,
// unrelated grammar defect (the collection-initializer parse blowup —
// see the map's trailing comment and TestCsharpIsParkedFromTheLangMap).
// So this drives extractGuarded directly rather than Build: the
// extractor and its grammar coverage stay tested, so re-enabling the
// map row is a one-line change that lands on green tests rather than
// on untested code.
func TestBuild_CsharpBasics(t *testing.T) {
	src := `using System.Collections.Generic;

namespace Auth.Core
{
    public class SessionManager
    {
        private readonly TokenStore store;

        public SessionManager(TokenStore store)
        {
            this.store = store;
        }

        public bool Login(User u, string password)
        {
            if (!VerifyToken(u.Id, password))
            {
                throw new AuthException("denied");
            }
            Configure(u, Flags.A | Flags.B);
            return true;
        }
    }

    public interface IAuthenticator
    {
        bool Authenticate(User u, string pwd);
    }
}
`
	fs := extractGuarded("c_sharp", "session.cs", []byte(src))
	if fs == nil {
		t.Fatal("c_sharp extraction returned nil — is c_sharp registered in langExtractor?")
	}

	// Functions: Login + Authenticate (interface signature) + the
	// SessionManager constructor.
	wantFuncs := map[string]bool{"Login": false, "Authenticate": false, "SessionManager": false}
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

	// Login: method on SessionManager, params [u, password].
	login := findFunc(fs.Functions, "Login")
	if login == nil {
		t.Fatal("Login not found")
	}
	if !login.IsMethod || login.EnclosingClass != "SessionManager" {
		t.Errorf("Login = {IsMethod=%v Encl=%q}, want method on SessionManager", login.IsMethod, login.EnclosingClass)
	}
	if !sliceEq(login.Params, []string{"u", "password"}) {
		t.Errorf("Login.Params = %v, want [u password]", login.Params)
	}

	// Classes: SessionManager + IAuthenticator (interface_declaration).
	wantClasses := map[string]bool{"SessionManager": false, "IAuthenticator": false}
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

	// Calls: VerifyToken + Configure (invocation_expression).
	for _, want := range []string{"VerifyToken", "Configure"} {
		if !contains(fs.CalleeNames(), want) {
			t.Errorf("Calls missing %q; have %v", want, fs.CalleeNames())
		}
	}

	// Raises: `throw new AuthException("denied")` → "AuthException".
	if !contains(fs.Raises, "AuthException") {
		t.Errorf("Raises missing %q; have %v", "AuthException", fs.Raises)
	}

	// Imports: `using System.Collections.Generic;` → rightmost "Generic".
	if !contains(fs.Imports, "Generic") {
		t.Errorf("Imports missing %q; have %v", "Generic", fs.Imports)
	}

	// The corpus-level Definition() lookups this test used to make are
	// dropped rather than ported: they exercise the index layer, not the
	// C# extractor, and Build() no longer indexes .cs while the grammar
	// is parked. Definition() stays covered by the non-parked languages
	// (extract_go_test.go, extract_java_test.go, and friends).
}

// TestCsharpIsParkedFromTheLangMap pins the 2026-08-24 mitigation in
// place so re-enabling C# is a deliberate act, not a merge accident.
//
// The defect: on gotreesitter v0.51.0 the c_sharp grammar's parse of a
// collection initializer built from `{ typeof(T), X.Instance }` entries
// grows explosively with the entry count. MessagePack's
// BuiltinResolver.cs (11 KB, 211 lines) does not finish parsing in two
// minutes, and `ken index` over a directory containing it hung outright.
// Reproduced against raw gotreesitter with no ken code in the path.
// Filed upstream as odvcencio/gotreesitter#972.
//
// Why a map row rather than a timeout: ADR-040 keeps the CLI's parse
// budget off so `ken build-index` stays byte-identical across runs, and
// a wall-clock guard would make enrichment load-dependent — trading a
// hang for the reproducibility bug ADR-040 closed. Parking is
// deterministic, and it matches the standing precedent for the bash
// grammar.
//
// When upstream fixes the grammar: bump the gotreesitter pin, delete
// this test, restore `".cs": "c_sharp"` in kenLangToTSLang, and re-run
// the traceability sweep (bench/chunkdiff) over messagepack-csharp to
// confirm it completes.
func TestCsharpIsParkedFromTheLangMap(t *testing.T) {
	if gram, ok := kenLangToTSLang[".cs"]; ok {
		t.Fatalf(".cs is mapped to %q again. If that is deliberate, confirm the "+
			"gotreesitter c_sharp collection-initializer blowup is fixed upstream first — "+
			"`ken index` hangs forever on an 11 KB file otherwise. See this test's doc comment.", gram)
	}
	// The extractor itself stays registered and tested, so restoring the
	// map row is genuinely one line.
	if _, ok := langExtractor["c_sharp"]; !ok {
		t.Error("c_sharp extractor was unregistered — parking should keep it available for re-enabling")
	}
}
