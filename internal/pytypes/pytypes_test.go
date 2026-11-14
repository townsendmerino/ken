package pytypes

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/townsendmerino/ken/internal/lsp"
	"github.com/townsendmerino/ken/internal/testenv"
)

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, src := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func str(s string) *string { return &s }

func findings(c *EditCheck) string {
	var out []string
	for _, e := range c.Errors {
		out = append(out, fmt.Sprintf("%s:%d: %s", e.File, e.Line, e.Message))
	}
	return strings.Join(out, " | ")
}

// openOnly is a server that, like ty and pyright as they come, reports only on files that are open. A line that
// holds "use X" is an error unless some open file holds "def X". It never reads the disk.
type openOnly struct {
	in    *io.PipeReader
	inW   *io.PipeWriter
	out   *io.PipeReader
	outW  *io.PipeWriter
	mu    sync.Mutex
	open  map[string]string
	seen  []string // didOpen and didChange, in order, by base name
	close sync.Once
}

func newOpenOnly() *openOnly {
	f := &openOnly{open: map[string]string{}}
	f.in, f.inW = io.Pipe()
	f.out, f.outW = io.Pipe()
	go f.serve()
	return f
}

func (f *openOnly) Read(b []byte) (int, error)  { return f.out.Read(b) }
func (f *openOnly) Write(b []byte) (int, error) { return f.inW.Write(b) }
func (f *openOnly) Pid() int                    { return 0 }
func (f *openOnly) Stderr() string              { return "" }
func (f *openOnly) Close() error {
	f.close.Do(func() { _ = f.inW.Close(); _ = f.outW.Close() })
	return nil
}

func (f *openOnly) serve() {
	r := bufio.NewReader(f.in)
	for {
		n := 0
		for {
			ln, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if ln = strings.TrimSpace(ln); ln == "" {
				break
			}
			if v, ok := strings.CutPrefix(ln, "Content-Length:"); ok {
				n, _ = strconv.Atoi(strings.TrimSpace(v))
			}
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(r, buf); err != nil {
			return
		}
		var m struct {
			ID     json.RawMessage
			Method string
			Params struct {
				TextDocument   struct{ URI, Text, LanguageID string }
				ContentChanges []struct{ Text string }
			}
		}
		_ = json.Unmarshal(buf, &m)
		path, _ := lsp.URIToPath(m.Params.TextDocument.URI)
		f.mu.Lock()
		var result any
		switch m.Method {
		case "textDocument/didOpen":
			f.open[path] = m.Params.TextDocument.Text
			f.seen = append(f.seen, "open "+filepath.Base(path)+" "+m.Params.TextDocument.LanguageID)
		case "textDocument/didChange":
			f.open[path] = m.Params.ContentChanges[0].Text
			f.seen = append(f.seen, "change "+filepath.Base(path))
		case "textDocument/diagnostic":
			items := []any{}
			whole := ""
			for _, s := range f.open {
				whole += s + "\n"
			}
			if text, ok := f.open[path]; ok {
				for i, l := range strings.Split(text, "\n") {
					if _, name, ok := strings.Cut(l, "use "); ok && !strings.Contains(whole, "def "+name) {
						items = append(items, map[string]any{"severity": 1, "code": "unresolved", "message": "cannot find " + name,
							"range": map[string]any{"start": map[string]int{"line": i}, "end": map[string]int{"line": i}}})
					}
				}
			}
			result = map[string]any{"kind": "full", "items": items}
		}
		if len(m.ID) > 0 {
			body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result})
			_, _ = fmt.Fprintf(f.outW, "Content-Length: %d\r\n\r\n%s", len(body), body)
		}
		f.mu.Unlock()
	}
}

// Every file is opened before the first question, an edited file that is open is changed and not opened again, and
// a new file is opened.
func TestCheckEdit_opensEveryFile(t *testing.T) {
	root := writeTree(t, map[string]string{"a.py": "def one\ndef two\n", "b.py": "use one\nuse two\n", "c.pyi": "use two\n", "notes.txt": "use nothing\n"})
	srv := newOpenOnly()
	r := &Runner{Repo: root, Checker: "/opt/tools/ty", Spawn: func(_ context.Context, _ string, _, argv []string) (lsp.Proc, error) {
		if strings.Join(argv, " ") != "/opt/tools/ty server" {
			t.Errorf("argv = %q", argv)
		}
		return srv, nil
	}}
	res, err := r.CheckEdit(context.Background(), []FileEdit{
		{File: "a.py", OldString: "def two\n", NewString: ""},
		{File: "d.py", Content: str("use one\nuse three\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := findings(res), "d.py:2: unresolved: cannot find three | b.py:2: unresolved: cannot find two | c.pyi:1: unresolved: cannot find two"; got != want {
		t.Errorf("errors:\n got %s\nwant %s", got, want)
	}
	if res.Clean || res.Checked != 4 || res.Tool != "ty" {
		t.Errorf("clean %v, checked %d, tool %q", res.Clean, res.Checked, res.Tool)
	}
	if text := res.Text(0); !strings.Contains(text, "WOULD NOT TYPE-CHECK: 3 new errors") {
		t.Error(text)
	}
	srv.mu.Lock()
	seen := append([]string(nil), srv.seen...)
	srv.mu.Unlock()
	sort.Strings(seen[:3])
	if got, want := strings.Join(seen, ", "), "open a.py python, open b.py python, open c.pyi python, change a.py, open d.py python"; got != want {
		t.Errorf("the server saw %s\nwant %s", got, want)
	}

	clean, err := (&Runner{Repo: root, Checker: "/opt/tools/ty", Spawn: func(context.Context, string, []string, []string) (lsp.Proc, error) {
		return newOpenOnly(), nil
	}}).CheckEdit(context.Background(), []FileEdit{{File: "a.py", OldString: "def one", NewString: "def one  # same"}})
	if err != nil || !clean.Clean || !strings.Contains(clean.Text(0), "checked: the edit was opened as unsaved contents in the project's type checker (ty)") {
		t.Errorf("a harmless edit: %v\n%s", err, clean.Text(0))
	}
}

func TestCheckEdit_refusals(t *testing.T) {
	root := writeTree(t, map[string]string{"a.py": "x = 1\n", "pyproject.toml": "[project]\n"})
	for _, tc := range []struct {
		r     *Runner
		edits []FileEdit
		want  string
	}{
		{&Runner{Repo: root}, []FileEdit{{File: "pyproject.toml", Content: str("")}}, "only Python files"},
		{&Runner{Repo: root}, []FileEdit{{File: "a.py", Content: str("x = 2\n")}}, "mypy has no language server"},
		{&Runner{Repo: root, Checker: "/usr/bin/mypy"}, []FileEdit{{File: "a.py", Content: str("x = 2\n")}}, "neither ty nor pyright-langserver"},
	} {
		_, err := tc.r.CheckEdit(context.Background(), tc.edits)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: err = %v, want %q", tc.edits, err, tc.want)
		}
	}
}

func TestFindChecker(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the layout below is the POSIX one")
	}
	root := writeTree(t, map[string]string{
		"withty/.venv/bin/ty": "",
		"withty/src/x.py":     "",
		"oldvenv/venv/bin/ty": "",
		"withpyright/node_modules/.bin/pyright-langserver": "",
		"both/.venv/bin/ty":                                   "",
		"both/node_modules/.bin/pyright-langserver":           "",
		"bothpyright/.venv/bin/ty":                            "",
		"bothpyright/node_modules/.bin/pyright-langserver":    "",
		"bothpyright/pyrightconfig.json":                      "{}",
		"bothconfigured/.venv/bin/ty":                         "",
		"bothconfigured/node_modules/.bin/pyright-langserver": "",
		"bothconfigured/pyproject.toml":                       "[tool.pyright]\n[tool.ty]\n",
		"pip/.venv/bin/pyright-langserver":                    "",
		"none/x.py":                                           "",
	})
	for dir, want := range map[string]string{
		"withty/src":     "ty withty/.venv/bin/ty",
		"oldvenv":        "ty oldvenv/venv/bin/ty",
		"withpyright":    "pyright withpyright/node_modules/.bin/pyright-langserver",
		"both":           "ty both/.venv/bin/ty",
		"bothpyright":    "pyright bothpyright/node_modules/.bin/pyright-langserver",
		"bothconfigured": "ty bothconfigured/.venv/bin/ty",
	} {
		c, err := FindChecker(root, filepath.Join(root, dir))
		rel, _ := filepath.Rel(root, c.Path)
		if got := c.Kind + " " + filepath.ToSlash(rel); err != nil || got != want {
			t.Errorf("%s: %q, %v; want %q", dir, got, err, want)
		}
	}
	_, err := FindChecker(root, filepath.Join(root, "none"))
	if !errors.Is(err, ErrNoChecker) || !strings.Contains(err.Error(), "uv add --dev ty") || strings.Contains(err.Error(), "from pip") {
		t.Errorf("none: %v", err)
	}
	if _, err = FindChecker(root, filepath.Join(root, "pip")); !errors.Is(err, ErrNoChecker) || !strings.Contains(err.Error(), "installed here from pip") {
		t.Errorf("pip: %v", err)
	}
	// a checker above the repository root is not the project's
	if _, err := FindChecker(filepath.Join(root, "withty/src"), filepath.Join(root, "withty/src")); !errors.Is(err, ErrNoChecker) {
		t.Errorf("found a checker above the root: %v", err)
	}
	for path, want := range map[string]string{"/a/.venv/bin/ty": Ty, `C:\v\Scripts\ty.exe`: "", "/n/.bin/pyright-langserver": Pyright, "/usr/bin/mypy": "", "/x/typo": ""} {
		if runtime.GOOS != "windows" && strings.Contains(path, `\`) {
			continue
		}
		if got := KindOf(path); got != want {
			t.Errorf("KindOf(%s) = %q, want %q", path, got, want)
		}
	}
}

// The tests below run the real checkers, which ken does not ship or download. KEN_TEST_TY names a ty binary;
// KEN_TEST_PYRIGHT names pyright's pyright-langserver (its sibling `pyright` is the oracle's command line).
var realFixture = map[string]string{
	"pyproject.toml":       "[project]\nname = \"fx\"\nversion = \"0.0.0\"\nrequires-python = \">=3.12\"\n",
	"pkg/__init__.py":      "",
	"pkg/core/__init__.py": "",
	"pkg/app/__init__.py":  "",
	"pkg/core/shape.py": `from typing import Protocol


class Shape(Protocol):
    def area(self) -> float: ...


class Square:
    def __init__(self, side: float) -> None:
        self.side = side

    def area(self) -> float:
        return self.side * self.side


def scale(s: Square, by: float) -> Square:
    return Square(s.side * by)
`,
	"pkg/app/main.py": `from pkg.core.shape import Shape, Square, scale


def double(s: Square) -> Square:
    return scale(s, 2)


shapes: list[Shape] = [Square(1)]
total = [s.area() for s in shapes]
`,
	"pkg/app/broken.py": "n: int = \"already broken\"\n",
}

// checkerErrors runs the checker's own command line over a tree and returns its errors as "file:line", sorted.
func checkerErrors(t *testing.T, kind, program, root string) []string {
	t.Helper()
	var errs []string
	switch kind {
	case Ty:
		cmd := testenv.Command(t, program, "check", "--output-format", "concise")
		cmd.Dir = root
		out, _ := cmd.CombinedOutput()
		for _, m := range regexp.MustCompile(`(?m)^(\S+?):(\d+):\d+: error\[`).FindAllStringSubmatch(string(out), -1) {
			errs = append(errs, m[1]+":"+m[2])
		}
	case Pyright:
		cmd := testenv.Command(t, filepath.Join(filepath.Dir(program), "pyright"), "--outputjson")
		cmd.Dir = root
		out, _ := cmd.Output()
		var rep struct {
			GeneralDiagnostics []struct {
				File, Severity string
				Range          struct{ Start struct{ Line int } }
			}
		}
		if err := json.Unmarshal(out, &rep); err != nil {
			t.Fatalf("pyright --outputjson: %v\n%s", err, out)
		}
		for _, d := range rep.GeneralDiagnostics {
			if d.Severity == "error" {
				rel, _ := filepath.Rel(root, d.File)
				errs = append(errs, fmt.Sprintf("%s:%d", filepath.ToSlash(rel), d.Range.Start.Line+1))
			}
		}
	}
	sort.Strings(errs)
	return errs
}

func TestCheckEdit_againstTheRealCheckers(t *testing.T) {
	ran := false
	for kind, env := range map[string]string{Ty: "KEN_TEST_TY", Pyright: "KEN_TEST_PYRIGHT"} {
		program := os.Getenv(env)
		if program == "" {
			continue
		}
		ran = true
		for _, tc := range []struct {
			name  string
			edits []FileEdit
			want  []string // "file:line" of each new error
			fixed int
		}{
			{"a body change", []FileEdit{{File: "pkg/core/shape.py", OldString: "s.side * by", NewString: "by * s.side"}}, nil, 0},
			{"a new parameter breaks a caller in another package",
				[]FileEdit{{File: "pkg/core/shape.py", OldString: "by: float) -> Square:", NewString: "by: float, digits: int) -> Square:"}},
				[]string{"pkg/app/main.py:5"}, 0},
			{"a method the protocol needs is renamed away",
				[]FileEdit{{File: "pkg/core/shape.py", OldString: "    def area(self) -> float:\n        return", NewString: "    def surface(self) -> float:\n        return"}},
				[]string{"pkg/app/main.py:8"}, 0},
			{"a fix", []FileEdit{{File: "pkg/app/broken.py", OldString: `"already broken"`, NewString: "1"}}, nil, 1},
			{"a new file that calls the other package wrongly",
				[]FileEdit{{File: "pkg/app/extra.py", Content: str("from pkg.core.shape import scale\n\ns = scale(1, 2)\n")}},
				[]string{"pkg/app/extra.py:3"}, 0},
			{"a syntax error", []FileEdit{{File: "pkg/app/main.py", OldString: "return scale(s, 2)", NewString: "return scale(s, 2"}}, []string{"pkg/app/main.py"}, 0},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				root := writeTree(t, realFixture)
				res, err := (&Runner{Repo: root, Checker: program}).CheckEdit(context.Background(), tc.edits)
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("\n%s", res.Text(0))
				var got []string
				for _, e := range res.Errors {
					got = append(got, fmt.Sprintf("%s:%d", e.File, e.Line))
				}
				sort.Strings(got)
				syntax := len(tc.want) == 1 && !strings.Contains(tc.want[0], ":")
				if syntax {
					// a syntax error is reported differently by each checker: what is held is that there is one, in the file
					if len(got) == 0 || !strings.HasPrefix(got[0], tc.want[0]+":") {
						t.Errorf("errors %v, want some in %s", got, tc.want[0])
					}
				} else if strings.Join(got, " ") != strings.Join(tc.want, " ") {
					t.Errorf("errors %v, want %v", got, tc.want)
				}
				if res.Fixed != tc.fixed || res.Incomplete || res.Clean != (len(tc.want) == 0) {
					t.Errorf("fixed %d, incomplete %v, clean %v", res.Fixed, res.Incomplete, res.Clean)
				}

				// The oracle: write the edit to a copy and run the checker's own command line over both trees. What
				// the run gains is what the check must have reported, by file and line.
				base := checkerErrors(t, kind, program, writeTree(t, realFixture))
				patched := map[string]string{}
				for k, v := range realFixture {
					patched[k] = v
				}
				for _, e := range tc.edits {
					if e.Content != nil {
						patched[e.File] = *e.Content
					} else {
						patched[e.File] = strings.Replace(patched[e.File], e.OldString, e.NewString, 1)
					}
				}
				after := checkerErrors(t, kind, program, writeTree(t, patched))
				had := map[string]int{}
				for _, e := range base {
					had[e]++
				}
				var gained []string
				for _, e := range after {
					if had[e] > 0 {
						had[e]--
						continue
					}
					gained = append(gained, e)
				}
				if strings.Join(gained, " ") != strings.Join(got, " ") {
					t.Errorf("the checker's command line gains %v\nthe check reported %v", gained, got)
				}
			})
		}
	}
	if !ran {
		t.Skip("neither KEN_TEST_TY nor KEN_TEST_PYRIGHT is set")
	}
}
