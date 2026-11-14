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

// py_check_edit is offered only where it can answer: the default repository is a local checkout with ty or pyright
// installed at its root.
func TestServer_PyToolFollowsTheDefaultRepo(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the layouts below are the POSIX ones")
	}
	withTy := writeTSRepo(t, map[string]string{".venv/bin/ty": "", "a.py": ""})
	withPyright := writeTSRepo(t, map[string]string{"node_modules/.bin/pyright-langserver": "", "a.py": ""})
	neither := writeTSRepo(t, map[string]string{".venv/bin/mypy": "", "a.py": ""})
	for _, tc := range []struct {
		name, repo string
		want       bool
	}{
		{"a repo with ty", withTy, true},
		{"a repo with pyright", withPyright, true},
		{"a repo with mypy only", neither, false},
		{"no default repo", "", false},
		{"a URL", "https://example.com/x/y", false},
	} {
		if got := pyToolsAvailable(&Config{DefaultRepo: tc.repo}); got != tc.want {
			t.Errorf("%s: offered = %v, want %v", tc.name, got, tc.want)
		}
	}
	ctx, sess, cleanup := newInMemoryServerClientWith(t, func(c *Config) { c.DefaultRepo = withTy })
	defer cleanup()
	res, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tl := range res.Tools {
		found = found || tl.Name == "py_check_edit"
		if tl.Name == "ts_check_edit" {
			t.Error("ts_check_edit is listed for a repo without TypeScript")
		}
	}
	if !found {
		t.Error("py_check_edit is not listed for a default repo that has ty")
	}
}

func TestPyCheckEdit_handlerRefusals(t *testing.T) {
	repo := writeTSRepo(t, map[string]string{"a.py": "a = 1\n"})
	cfg := &Config{DefaultRepo: repo}
	check := func(args PyCheckEditArgs) string {
		res, _, err := handlePyCheckEdit(context.Background(), cfg, args)
		if err != nil {
			t.Fatal(err)
		}
		return resultText(t, res)
	}
	edit := []PyEditArg{{File: "a.py", OldString: "1", NewString: "2"}}
	t.Setenv("KEN_MCP_ALLOWED_REPO_ROOTS", "")
	if text := check(PyCheckEditArgs{}); !strings.Contains(text, "edits is required") {
		t.Errorf("no edits: %q", text)
	}
	if text := check(PyCheckEditArgs{Edits: edit, repoArg: repoArg{Repo: repo}}); !strings.Contains(text, "py_check_edit runs the type checker installed in the repo") || !strings.Contains(text, "default repo only") {
		t.Errorf("a repo argument on an unconfined server: %q", text)
	}
	if text := check(PyCheckEditArgs{Edits: edit}); !strings.Contains(text, "no Python type checker to ask") {
		t.Errorf("a project with no checker: %q", text)
	}
}

// End to end through the handler with a real ty: see KEN_TEST_TY in internal/pytypes.
func TestPyCheckEdit_handlerWithARealChecker(t *testing.T) {
	ty := os.Getenv("KEN_TEST_TY")
	if ty == "" {
		t.Skip("KEN_TEST_TY is not set")
	}
	lib := "def make(n: int) -> int:\n    return n\n"
	repo := writeTSRepo(t, map[string]string{
		"pyproject.toml": "[project]\nname = \"fx\"\nversion = \"0.0.0\"\n",
		"lib.py":         lib,
		"app.py":         "from lib import make\n\nv = make(1)\n",
	})
	open := openPyTyped
	openPyTyped = func(r string) *engine.PyTyped { pt := open(r); pt.Checker = ty; return pt }
	defer func() { openPyTyped = open }()
	check := func(args PyCheckEditArgs) string {
		res, _, err := handlePyCheckEdit(context.Background(), &Config{DefaultRepo: repo}, args)
		if err != nil {
			t.Fatal(err)
		}
		return resultText(t, res)
	}
	text := check(PyCheckEditArgs{Edits: []PyEditArg{{File: "lib.py", OldString: "n: int)", NewString: "n: int, scale: int)"}}})
	for _, want := range []string{"ERROR: app.py:3: missing-argument:", "WOULD NOT TYPE-CHECK: 1 new errors", "checked with ty "} {
		if !strings.Contains(text, want) {
			t.Errorf("py_check_edit lacks %q:\n%s", want, text)
		}
	}
	if text = check(PyCheckEditArgs{Edits: []PyEditArg{{File: "lib.py", OldString: "return n", NewString: "return n + 0"}}}); !strings.Contains(text, "checked: the edit was opened as unsaved contents") {
		t.Errorf("a harmless edit was not called checked:\n%s", text)
	}
	if src, _ := os.ReadFile(filepath.Join(repo, "lib.py")); string(src) != lib {
		t.Error("py_check_edit changed a file")
	}
}
