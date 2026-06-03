package structural

import (
	"os"
	"path/filepath"
	"testing"
)

// TestBuild_PhpBasics confirms the PHP extractor lights up on
// top-level functions, classes with methods, interfaces, parameters
// (drilling through variable_name to strip the leading `$`), function
// calls and member calls, namespace_use_declaration with the
// rightmost-segment binding rule, and throws.
func TestBuild_PhpBasics(t *testing.T) {
	dir := t.TempDir()
	src := `<?php
namespace App\Auth;

use App\Models\User;
use App\Services\TokenService;
use App\Errors\AuthError as Err;

class SessionManager {
	private array $active = [];

	public function login(User $u): bool {
		$this->active[] = $u;
		return true;
	}

	public function logout(User $u): void {
		unset($this->active[$u->id]);
	}

	public function fail(): void {
		throw new Err("denied");
	}
}

function authenticate(User $user, string $password): bool {
	return verifyToken($user->id, $password);
}

interface Authenticator {
	public function authenticate(User $u, string $pwd): bool;
}
`
	if err := os.WriteFile(filepath.Join(dir, "auth.php"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	ix, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	fs := ix.File("auth.php")
	if fs == nil {
		t.Fatal("auth.php not indexed")
	}

	// Functions: authenticate (top-level), login + logout + fail
	// (SessionManager methods), authenticate (Authenticator
	// signature).
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

	// authenticate (the top-level one): params should be [user,
	// password] — the bare names, NOT `$user` / `$password`.
	auth := findFunc(fs.Functions, "authenticate")
	if auth == nil {
		t.Fatal("authenticate not found")
	}
	if !sliceEq(auth.Params, []string{"user", "password"}) && !sliceEq(auth.Params, []string{"u", "pwd"}) {
		t.Errorf("authenticate.Params = %v, want bare names [user password] or [u pwd]", auth.Params)
	}

	// login method on SessionManager
	login := findFunc(fs.Functions, "login")
	if login == nil {
		t.Fatal("login not found")
	}
	if login.EnclosingClass != "SessionManager" {
		t.Errorf("login.EnclosingClass = %q, want SessionManager", login.EnclosingClass)
	}

	// Classes: SessionManager + Authenticator
	wantClasses := map[string]bool{"SessionManager": false, "Authenticator": false}
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

	// Calls: verifyToken (free call). unset is in the noise filter.
	if !contains(fs.CalleeNames(), "verifyToken") {
		t.Errorf("Calls missing 'verifyToken'; have %v", fs.CalleeNames())
	}
	if contains(fs.CalleeNames(), "unset") {
		t.Errorf("Calls should NOT contain 'unset' (filtered); have %v", fs.CalleeNames())
	}

	// Imports: bound names are rightmost segments (User,
	// TokenService) + alias (Err, NOT AuthError).
	wantImports := []string{"User", "TokenService", "Err"}
	for _, want := range wantImports {
		if !contains(fs.Imports, want) {
			t.Errorf("Imports missing %q; have %v", want, fs.Imports)
		}
	}
	// Aliased import should NOT also bind the original name (only
	// the alias is in local scope).
	if contains(fs.Imports, "AuthError") {
		t.Errorf("Imports should NOT contain 'AuthError' (aliased to Err); have %v", fs.Imports)
	}

	// Raises: throw new Err(...) — name resolves to "Err"
	if !contains(fs.Raises, "Err") {
		t.Errorf("Raises missing 'Err'; have %v", fs.Raises)
	}
}
