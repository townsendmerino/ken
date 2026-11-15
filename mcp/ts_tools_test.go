package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/townsendmerino/ken/engine"
)

func writeTSRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		p := filepath.Join(repo, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return repo
}

// nativeTSC is where the typescript package puts the native compiler for this machine.
func nativeTSC() string {
	goos, arch, exe := runtime.GOOS, runtime.GOARCH, "tsc"
	if goos == "windows" {
		goos, exe = "win32", "tsc.exe"
	}
	if arch == "amd64" {
		arch = "x64"
	}
	return "node_modules/@typescript/typescript-" + goos + "-" + arch + "/lib/" + exe
}

// ts_check_edit is offered only where it can answer: the default repository is a local checkout with a TypeScript 7
// compiler at its root.
func TestServer_TSToolFollowsTheDefaultRepo(t *testing.T) {
	with := writeTSRepo(t, map[string]string{nativeTSC(): "", "a.ts": ""})
	older := writeTSRepo(t, map[string]string{"node_modules/typescript/package.json": `{"version":"5.9.3"}`, "a.ts": ""})
	nested := writeTSRepo(t, map[string]string{"apps/web/" + nativeTSC(): "", "apps/web/a.ts": "", "backend/main.py": ""})
	tooDeep := writeTSRepo(t, map[string]string{"a/b/c/" + nativeTSC(): "", "a/b/c/a.ts": ""})
	inDeps := writeTSRepo(t, map[string]string{"vendor/x/" + nativeTSC(): "", "a.ts": ""})
	for _, tc := range []struct {
		name, repo string
		want       bool
	}{
		{"a repo with TypeScript 7", with, true},
		{"a repo with TypeScript 5", older, false},
		{"a project two levels down", nested, true},
		{"a project three levels down", tooDeep, false},
		{"a compiler inside vendored dependencies", inDeps, false},
		{"no default repo", "", false},
		{"a URL", "https://example.com/x/y", false},
	} {
		if got := tsToolsAvailable(&Config{DefaultRepo: tc.repo}); got != tc.want {
			t.Errorf("%s: offered = %v, want %v", tc.name, got, tc.want)
		}
	}
	ctx, sess, cleanup := newInMemoryServerClientWith(t, func(c *Config) { c.DefaultRepo = with })
	defer cleanup()
	res, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tl := range res.Tools {
		found = found || tl.Name == "ts_check_edit"
	}
	if !found {
		t.Error("ts_check_edit is not listed for a default repo that has the compiler")
	}
}

// checkCounter is a usage recorder that keeps the edit checks it is told of.
type checkCounter struct{ rows []string }

func (c *checkCounter) Record(string, int, int, int) {}
func (c *checkCounter) RecordCheck(call, outcome string, findings int) {
	c.rows = append(c.rows, fmt.Sprintf("%s %s %d", call, outcome, findings))
}

func TestTSCheckEdit_handlerRefusals(t *testing.T) {
	repo := writeTSRepo(t, map[string]string{"node_modules/typescript/package.json": `{"version":"5.9.3"}`, "a.ts": "export const a = 1;\n"})
	counted := &checkCounter{}
	cfg := &Config{DefaultRepo: repo, UsageRecorder: counted}
	check := func(args TSCheckEditArgs) string {
		res, _, err := handleTSCheckEdit(context.Background(), cfg, args)
		if err != nil {
			t.Fatal(err)
		}
		return resultText(t, res)
	}
	edit := []TSEditArg{{File: "a.ts", OldString: "1", NewString: "2"}}
	t.Setenv("KEN_MCP_ALLOWED_REPO_ROOTS", "")
	if text := check(TSCheckEditArgs{}); !strings.Contains(text, "edits is required") {
		t.Errorf("no edits: %q", text)
	}
	// an unconfined server runs the compiler of the default repo only
	if text := check(TSCheckEditArgs{Edits: edit, repoArg: repoArg{Repo: repo}}); !strings.Contains(text, "checks the default repo only") {
		t.Errorf("a repo argument on an unconfined server: %q", text)
	}
	if text := check(TSCheckEditArgs{Edits: edit}); !strings.Contains(text, "this project has TypeScript 5.9.3 installed") {
		t.Errorf("a project on TypeScript 5: %q", text)
	}
	// the refusal is counted with its reason, and nothing else about it
	if len(counted.rows) != 1 || counted.rows[0] != "ts_check_edit older-tool 0" {
		t.Errorf("counted checks = %q, want one older-tool row", counted.rows)
	}
	t.Setenv("KEN_MCP_ALLOWED_REPO_ROOTS", repo)
	if text := check(TSCheckEditArgs{Edits: edit, repoArg: repoArg{Repo: repo}}); !strings.Contains(text, "this project has TypeScript 5.9.3 installed") {
		t.Errorf("a repo argument on a confined server should reach the compiler lookup: %q", text)
	}
	if text := check(TSCheckEditArgs{Edits: edit, repoArg: repoArg{Repo: "https://example.com/x/y"}}); !strings.Contains(text, "local checkout") && !strings.Contains(text, "allowed") {
		t.Errorf("a URL: %q", text)
	}
}

// End to end through the handler with a real TypeScript 7: see KEN_TEST_TSC in internal/tstypes.
func TestTSCheckEdit_handlerWithTheRealCompiler(t *testing.T) {
	tsc := os.Getenv("KEN_TEST_TSC")
	if tsc == "" {
		t.Skip("KEN_TEST_TSC is not set")
	}
	lib := "export function make(n: number): number {\n  return n;\n}\n"
	repo := writeTSRepo(t, map[string]string{
		"tsconfig.json": `{"compilerOptions": {"strict": true, "noEmit": true}, "include": ["src"]}`,
		"src/lib.ts":    lib,
		"src/app.ts":    "import { make } from \"./lib\";\n\nexport const v = make(1);\n",
	})
	open := openTSTyped
	openTSTyped = func(r string) *engine.TSTyped { tt := open(r); tt.Tsc = tsc; return tt }
	defer func() { openTSTyped = open }()
	check := func(args TSCheckEditArgs) string {
		res, _, err := handleTSCheckEdit(context.Background(), &Config{DefaultRepo: repo}, args)
		if err != nil {
			t.Fatal(err)
		}
		return resultText(t, res)
	}
	text := check(TSCheckEditArgs{Edits: []TSEditArg{{File: "src/lib.ts", OldString: "n: number)", NewString: "n: number, scale: number)"}}})
	for _, want := range []string{"ERROR: src/app.ts:3: TS2554: Expected 2 arguments, but got 1.", "WOULD NOT COMPILE: 1 new errors"} {
		if !strings.Contains(text, want) {
			t.Errorf("ts_check_edit lacks %q:\n%s", want, text)
		}
	}
	if text = check(TSCheckEditArgs{Edits: []TSEditArg{{File: "src/lib.ts", OldString: "return n;", NewString: "return n + 0;"}}}); !strings.Contains(text, "checked: the edit was opened as unsaved contents") {
		t.Errorf("a harmless edit was not called checked:\n%s", text)
	}
	if js := check(TSCheckEditArgs{Output: "json", Edits: []TSEditArg{{File: "src/lib.ts", OldString: "return n;", NewString: "return n + 0;"}}}); !strings.Contains(js, `"clean": true`) {
		t.Errorf("json: %s", js)
	}
	if src, _ := os.ReadFile(filepath.Join(repo, "src", "lib.ts")); string(src) != lib {
		t.Error("ts_check_edit changed a file")
	}
}

func TestRenamePlanTools_offeredAndRefusals(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the layouts below are the POSIX ones")
	}
	withMypy := writeTSRepo(t, map[string]string{".venv/bin/mypy": "", "a.py": ""})
	withTy := writeTSRepo(t, map[string]string{".venv/bin/ty": "", "a.py": "x = 1\n"})
	if pyRenameAvailable(&Config{DefaultRepo: withMypy}) || !pyRenameAvailable(&Config{DefaultRepo: withTy}) || pyRenameAvailable(&Config{}) {
		t.Error("py_rename_plan is offered where the checker is ty, pyright or basedpyright, and not for mypy alone")
	}
	// the instructions name a rename tool only where it is offered
	if got := defaultInstructions(typedTools{py: true}); strings.Contains(got, "rename_plan") {
		t.Errorf("instructions name a rename tool that is not offered: %s", got)
	}
	if got := defaultInstructions(typedTools{ts: true, py: true, pyRename: true}); !strings.Contains(got, "`ts_rename_plan` or `py_rename_plan`") {
		t.Errorf("instructions: %s", got)
	}
	ctx, sess, cleanup := newInMemoryServerClientWith(t, func(c *Config) { c.DefaultRepo = withTy })
	defer cleanup()
	res, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tl := range res.Tools {
		names[tl.Name] = true
	}
	if !names["py_rename_plan"] || names["ts_rename_plan"] {
		t.Errorf("tools for a Python repo with ty: %v", names)
	}

	cfg := &Config{DefaultRepo: withTy}
	ask := func(args LSPRenameArgs) string {
		res, _, err := handleLSPRename(context.Background(), cfg, args, "py_rename_plan", "the type checker installed in the repo",
			func(string) (*engine.PyRenamePlan, error) {
				return &engine.PyRenamePlan{Name: args.Name, NewName: args.NewName, File: args.File, Line: args.Line, Verified: true,
					Check: &engine.PyEditCheck{Tool: "ty"}, Patch: "--- a/a.py\n+++ b/a.py\n"}, nil
			})
		if err != nil {
			t.Fatal(err)
		}
		return res.Content[0].(*sdk.TextContent).Text
	}
	for _, tc := range []struct {
		args LSPRenameArgs
		want string
	}{
		{LSPRenameArgs{File: "a.py", Line: 1, Name: "x"}, "file, line, name and new_name are required"},
		{LSPRenameArgs{File: "a.py", Name: "x", NewName: "y"}, "line is required"},
		{LSPRenameArgs{File: "a.py", Line: 1, Name: "x", NewName: "y", repoArg: repoArg{Repo: withMypy}}, "leave `repo` out"},
		{LSPRenameArgs{File: "a.py", Line: 1, Name: "x", NewName: "y"}, "patch: not included; call again with patch=true"},
		{LSPRenameArgs{File: "a.py", Line: 1, Name: "x", NewName: "y", Patch: true}, "```diff\n--- a/a.py"},
	} {
		if got := ask(tc.args); !strings.Contains(got, tc.want) {
			t.Errorf("%+v:\n%s\nwant %q", tc.args, got, tc.want)
		}
	}
}
