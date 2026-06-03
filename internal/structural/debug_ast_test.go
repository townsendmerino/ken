package structural

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// TestDebug_ASTShape is a reusable AST-dump test for writing new
// language extractors. Gated on KEN_DEBUG_AST=1 + KEN_DEBUG_LANG
// (gotreesitter grammar name). Dumps the parse tree for an inline
// fixture so we can see exact node types + field names before
// writing extract_<lang>.go.
//
// Usage:
//
//	KEN_DEBUG_AST=1 KEN_DEBUG_LANG=typescript go test -run TestDebug_ASTShape ./internal/structural -v
//
// Fixtures live in fixtureForLang below; add a case per language as
// we work through the top-10. Never runs in normal CI.
func TestDebug_ASTShape(t *testing.T) {
	if os.Getenv("KEN_DEBUG_AST") == "" {
		t.Skip("set KEN_DEBUG_AST=1 to print")
	}
	langName := os.Getenv("KEN_DEBUG_LANG")
	if langName == "" {
		t.Fatal("set KEN_DEBUG_LANG=<grammar-name> (e.g. typescript, java, rust)")
	}
	src := fixtureForLang(langName)
	if src == "" {
		t.Fatalf("no debug fixture for KEN_DEBUG_LANG=%q; add one in debug_ast_test.go", langName)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	gramName := langName
	if alt, ok := debugLangGrammar[langName]; ok {
		gramName = alt
	}
	entry := grammars.DetectLanguageByName(gramName)
	if entry == nil {
		t.Fatalf("gotreesitter has no %q grammar", gramName)
	}
	lang := entry.Language()
	pool := gotreesitter.NewParserPool(lang)
	tree, err := pool.Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	maxDepth := 6
	if md := os.Getenv("KEN_DEBUG_AST_DEPTH"); md != "" {
		if v, err := strconv.Atoi(md); err == nil && v > 0 {
			maxDepth = v
		}
	}
	dumpAST(t, []byte(src), tree.RootNode(), lang, 0, maxDepth)
}

func dumpAST(t *testing.T, src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, depth, maxDepth int) {
	if n == nil || depth > maxDepth {
		return
	}
	indent := ""
	for i := 0; i < depth; i++ {
		indent += "  "
	}
	text := nodeText(src, n)
	if len(text) > 70 {
		text = text[:70] + "..."
	}
	t.Logf("%s%s = %q", indent, n.Type(lang), text)
	nc := n.NamedChildCount()
	for i := 0; i < nc; i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		fieldName := n.FieldNameForChild(i, lang)
		if fieldName != "" {
			t.Logf("%s  [namedIdx=%d field=%s]", indent, i, fieldName)
		}
		dumpAST(t, src, c, lang, depth+1, maxDepth)
	}
}

// debugLangGrammar maps a debug-fixture key to the gotreesitter
// grammar name to use for parsing it. Useful when we want a small
// focused fixture under a label (e.g. "ts_arrow") that should still
// be parsed with the "typescript" grammar.
var debugLangGrammar = map[string]string{
	"ts_arrow": "typescript",
	"cpp_min":  "cpp",
	"csharp":   "c_sharp",
}

// fixtureForLang returns a small representative source string per
// grammar — function, method, class, call, import — enough to see
// every shape extract_<lang>.go needs to handle.
func fixtureForLang(name string) string {
	switch name {
	case "csharp":
		return `using System;
using System.Collections.Generic;

namespace App.Auth
{
    public class SessionManager
    {
        private List<User> active = new List<User>();

        public bool Login(User u, string password)
        {
            if (!VerifyToken(u.Id, password))
            {
                throw new AuthException("denied");
            }
            active.Add(u);
            return true;
        }

        public void Logout(User u)
        {
            active.Remove(u);
        }
    }

    public interface IAuthenticator
    {
        bool Authenticate(User u, string pwd);
    }
}
`
	case "ts_arrow":
		return `const handler = (user) => verifyToken(user);
const noop = () => doNothing();
`
	case "cpp_min":
		return `class S { public: void logout(int id); };
void fail() { throw AuthError("x"); }
`
	case "typescript":
		return `
import { foo } from "./foo";
import * as bar from "./bar";

interface User {
	name: string;
	id: number;
}

export function authenticate(user: User, password: string): boolean {
	return verifyToken(user.id, password);
}

const Login = (u: User) => authenticate(u, "x");

class SessionManager {
	active: User[] = [];

	login(u: User): void {
		this.active.push(u);
	}

	logout(u: User): void {
		this.active = this.active.filter(x => x !== u);
	}
}
`
	case "javascript":
		return `
import { foo } from "./foo";

function authenticate(user, password) {
	return verifyToken(user.id, password);
}

const Login = (u) => authenticate(u, "x");

class SessionManager {
	constructor() {
		this.active = [];
	}
	login(u) {
		this.active.push(u);
	}
}
`
	case "kotlin":
		return `
package com.example.auth

import kotlin.collections.List
import java.util.ArrayList

class SessionManager(private val store: TokenStore) {
    private val active: MutableList<User> = ArrayList()

    fun login(u: User, password: String): Boolean {
        if (!verifyToken(u.id, password)) {
            throw AuthException("denied")
        }
        active.add(u)
        return true
    }

    fun logout(u: User) {
        active.remove(u)
    }
}

interface Authenticator {
    fun authenticate(u: User, pwd: String): Boolean
}

fun verifyToken(id: String, pwd: String): Boolean = true

object Constants {
    const val MAX_TRIES = 3
}
`
	case "swift":
		return `
import Foundation

protocol Authenticator {
    func authenticate(user: User, password: String) -> Bool
}

class SessionManager {
    private var active: [User] = []

    init() {}

    func login(_ u: User, password: String) throws -> Bool {
        guard verifyToken(u.id, password) else {
            throw AuthError.denied
        }
        active.append(u)
        return true
    }

    func logout(_ u: User) {
        active.removeAll { $0.id == u.id }
    }
}

extension SessionManager: Authenticator {
    func authenticate(user: User, password: String) -> Bool {
        return verifyToken(user.id, password)
    }
}

struct User {
    let id: String
    let name: String
}

enum AuthError: Error {
    case denied
}

func verifyToken(_ id: String, _ pwd: String) -> Bool { return true }
`
	case "java":
		return `
package com.example;

import java.util.List;
import java.util.ArrayList;

public class AuthService {
	private List<User> users = new ArrayList<>();

	public boolean authenticate(User user, String password) {
		return verifyToken(user.getId(), password);
	}

	public void login(User u) {
		users.add(u);
	}
}

interface Authenticator {
	boolean authenticate(User u, String pwd);
}
`
	case "rust":
		return `
use std::collections::HashMap;
use crate::user::User;

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
`
	case "cpp":
		return `
#include <vector>
#include <string>

namespace auth {

class SessionManager {
public:
	SessionManager() = default;
	bool login(const User& u);
	void logout(const std::string& id);

private:
	std::vector<User> active_;
};

bool authenticate(const User& u, const std::string& password) {
	return verifyToken(u.id, password);
}

bool SessionManager::login(const User& u) {
	active_.push_back(u);
	return true;
}

} // namespace auth
`
	case "c":
		return `
#include <stdlib.h>
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
`
	case "php":
		return `
<?php
namespace App\Auth;

use App\Models\User;
use App\Services\TokenService;

class SessionManager {
	private array $active = [];

	public function login(User $u): bool {
		$this->active[] = $u;
		return true;
	}

	public function logout(User $u): void {
		unset($this->active[$u->id]);
	}
}

function authenticate(User $user, string $password): bool {
	return verifyToken($user->id, $password);
}

interface Authenticator {
	public function authenticate(User $u, string $pwd): bool;
}
`
	case "ruby":
		return `
require 'set'

module Auth
	class SessionManager
		def initialize
			@active = Set.new
		end

		def login(user)
			@active.add(user)
			true
		end

		def logout(user)
			@active.delete(user)
		end
	end

	def self.authenticate(user, password)
		verify_token(user.id, password)
	end
end
`
	}
	return ""
}
