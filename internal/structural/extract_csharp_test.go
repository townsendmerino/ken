package structural

import (
	"os"
	"path/filepath"
	"testing"
)

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
func TestBuild_CsharpBasics(t *testing.T) {
	dir := t.TempDir()
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
	if err := os.WriteFile(filepath.Join(dir, "session.cs"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	ix, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	fs := ix.File("session.cs")
	if fs == nil {
		t.Fatal("session.cs not indexed — is .cs registered in kenLangToTSLang and c_sharp in langExtractor?")
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

	// Definition lookups.
	if defs := ix.Definition("SessionManager"); len(defs) < 1 {
		t.Errorf("Definition(SessionManager) = empty, want at least the class")
	} else {
		hasClass := false
		for _, d := range defs {
			if d.Kind == DefinitionKindClass {
				hasClass = true
			}
		}
		if !hasClass {
			t.Errorf("Definition(SessionManager) lacks Class kind; got %+v", defs)
		}
	}
	if defs := ix.Definition("SessionManager.Login"); len(defs) != 1 {
		t.Errorf("Definition(SessionManager.Login) = %+v, want one method site", defs)
	} else if defs[0].Kind != DefinitionKindMethod {
		t.Errorf("Definition(SessionManager.Login) kind = %v, want Method", defs[0].Kind)
	}
}
