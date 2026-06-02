package structural

import (
	"os"
	"path/filepath"
	"testing"
)

// TestBuild_RustBasics confirms the Rust extractor lights up on
// free functions, impl methods, structs, traits, calls (including
// method calls, scoped calls, macros), and use declarations
// (including aliases and grouped imports).
func TestBuild_RustBasics(t *testing.T) {
	dir := t.TempDir()
	src := `use std::collections::HashMap;
use crate::user::User;
use crate::token::{verify_token, mint_token as mk};

pub struct SessionManager {
	active: HashMap<String, User>,
}

impl SessionManager {
	pub fn new() -> Self {
		SessionManager { active: HashMap::new() }
	}

	pub fn login(&mut self, user: User) -> bool {
		self.active.insert(user.id.clone(), user);
		true
	}
}

pub fn authenticate(user: &User, password: &str) -> bool {
	verify_token(&user.id, password)
}

pub trait Authenticator {
	fn authenticate(&self, user: &User, pwd: &str) -> bool;
}

fn log_event(msg: &str) {
	println!("event: {}", msg);
}
`
	if err := os.WriteFile(filepath.Join(dir, "session.rs"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	ix, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	fs := ix.File("session.rs")
	if fs == nil {
		t.Fatal("session.rs not indexed")
	}

	// Functions: new + login (methods on SessionManager),
	// authenticate (free fn), authenticate (trait signature, same
	// name), log_event.
	wantFuncs := map[string]bool{"new": false, "login": false, "authenticate": false, "log_event": false}
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

	// login: method on SessionManager (set via impl_item recursion).
	login := findFunc(fs.Functions, "login")
	if login == nil {
		t.Fatal("login not found")
	}
	if !login.IsMethod {
		t.Errorf("login.IsMethod = false, want true")
	}
	if login.EnclosingClass != "SessionManager" {
		t.Errorf("login.EnclosingClass = %q, want SessionManager", login.EnclosingClass)
	}
	// `&mut self` is filtered from Params; `user` should remain.
	if !sliceEq(login.Params, []string{"user"}) {
		t.Errorf("login.Params = %v, want [user] (self_parameter excluded)", login.Params)
	}

	// Classes: SessionManager (struct) + Authenticator (trait).
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

	// Calls: verify_token (free) + new (HashMap::new — scoped),
	// but `new` is NOT in our noise filter and IS valuable to
	// surface as a constructor call. `insert`, `clone`, `println`
	// are all noise-filtered.
	for _, want := range []string{"verify_token", "new"} {
		if !contains(fs.Calls, want) {
			t.Errorf("Calls missing %q; have %v", want, fs.Calls)
		}
	}
	for _, noise := range []string{"insert", "clone", "println"} {
		if contains(fs.Calls, noise) {
			t.Errorf("Calls should NOT contain %q (filtered by rustIsBuiltinOrNoise); have %v", noise, fs.Calls)
		}
	}

	// Imports: HashMap (scoped leaf), User (scoped leaf),
	// verify_token (group member), mk (use_as_clause alias).
	wantImports := []string{"HashMap", "User", "verify_token", "mk"}
	for _, want := range wantImports {
		if !contains(fs.Imports, want) {
			t.Errorf("Imports missing %q; have %v", want, fs.Imports)
		}
	}

	// Definition lookups: SessionManager (struct → Class kind),
	// qualified method.
	defs := ix.Definition("SessionManager")
	if len(defs) != 1 || defs[0].Kind != DefinitionKindClass {
		t.Errorf("Definition(SessionManager) = %+v, want one Class def", defs)
	}
	mDef := ix.Definition("SessionManager.login")
	if len(mDef) != 1 || mDef[0].Kind != DefinitionKindMethod {
		t.Errorf("Definition(SessionManager.login) = %+v, want one Method site", mDef)
	}
}
