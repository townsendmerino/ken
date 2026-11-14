package mcp

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

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
	for _, tc := range []struct {
		name, repo string
		want       bool
	}{
		{"a repo with TypeScript 7", with, true},
		{"a repo with TypeScript 5", older, false},
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

func TestTSCheckEdit_handlerRefusals(t *testing.T) {
	repo := writeTSRepo(t, map[string]string{"node_modules/typescript/package.json": `{"version":"5.9.3"}`, "a.ts": "export const a = 1;\n"})
	cfg := &Config{DefaultRepo: repo}
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
