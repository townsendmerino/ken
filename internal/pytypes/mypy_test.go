package pytypes

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeMypy answers as mypy does for a project where main.py calls area from pkg/geom.py: it reads the shadow file
// it is given for pkg/geom.py, and reports the call when area has gained a parameter.
type fakeMypy struct {
	t     *testing.T
	calls [][]string
	dirs  []string
}

func (f *fakeMypy) exec(_ context.Context, dir string, _ []string, argv []string) ([]byte, []byte, int, error) {
	f.calls = append(f.calls, argv)
	f.dirs = append(f.dirs, dir)
	if argv[1] == "--version" {
		return []byte("mypy 9.9.9 (compiled: yes)\n"), nil, 0, nil
	}
	if argv[len(argv)-1] != "." {
		return nil, []byte("usage: mypy [-h]\nmypy: error: Missing target module, package, files, or command.\n"), 2, nil
	}
	already := filepath.Join(dir, "main.py") + `:4:10: error: Incompatible types in assignment (expression has type "str", variable has type "int")  [assignment]` + "\n"
	out, n := already, 1
	for i, a := range argv {
		if a != "--shadow-file" {
			continue
		}
		src, err := os.ReadFile(argv[i+2])
		if err != nil {
			f.t.Errorf("the shadow file is not readable: %v", err)
		}
		switch {
		case argv[i+1] != filepath.Join("pkg", "geom.py"):
			f.t.Errorf("shadowed %q", argv[i+1])
		case strings.Contains(string(src), "def area(w: int, h: int -> int"):
			return []byte(filepath.Join(dir, "pkg", "geom.py") + ":1: error: Expected `)`, found `->`  [syntax]\nFound 1 error in 1 file (errors prevented further checking)\n"), nil, 2, nil
		case strings.Contains(string(src), "d: int"):
			out = filepath.Join("main.py") + `:3:7: error: Missing positional argument "d" in call to "area"  [call-arg]` + "\n" +
				"main.py:3: note: a note is not an error\n" + out
			n = 2
		}
	}
	return []byte(out + "Found " + map[int]string{1: "1 error", 2: "2 errors"}[n] + " in 1 file (checked 3 source files)\n"), nil, 1, nil
}

func TestCheckEdit_mypy(t *testing.T) {
	tree := map[string]string{
		".venv/bin/mypy":  "",
		"pkg/__init__.py": "",
		"pkg/geom.py":     "def area(w: int, h: int) -> int:\n    return w * h\n",
		"main.py":         "from pkg.geom import area\n\nprint(area(2, 3))\nx: int = \"a\"\n",
	}
	check := func(t *testing.T, edits []FileEdit) (*EditCheck, *fakeMypy) {
		t.Helper()
		root := writeTree(t, tree)
		fake := &fakeMypy{t: t}
		res, err := (&Runner{Repo: root, Exec: fake.exec, CacheDir: filepath.Join(t.TempDir(), "cache")}).CheckEdit(context.Background(), edits)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("\n%s", res.Text(0))
		return res, fake
	}

	t.Run("a new parameter breaks a caller", func(t *testing.T) {
		res, fake := check(t, []FileEdit{{File: "pkg/geom.py", OldString: "h: int)", NewString: "h: int, d: int)"}})
		if len(res.Errors) != 1 || res.Errors[0].File != "main.py" || res.Errors[0].Line != 3 ||
			res.Errors[0].Message != `call-arg: Missing positional argument "d" in call to "area"` {
			t.Errorf("errors = %+v", res.Errors)
		}
		if res.Clean || res.Incomplete || res.Checked != 3 || res.Tool != "mypy" || res.Compiler != "9.9.9" {
			t.Errorf("clean %v, incomplete %v, checked %d, %s %s", res.Clean, res.Incomplete, res.Checked, res.Tool, res.Compiler)
		}
		if !strings.Contains(res.Text(0), "WOULD NOT TYPE-CHECK: 1 new errors") {
			t.Errorf("text:\n%s", res.Text(0))
		}
		// the version, a run with no target that mypy refuses, the project as it is, and the project with the edit
		if len(fake.calls) != 4 {
			t.Fatalf("%d runs of mypy, want 4: %v", len(fake.calls), fake.calls)
		}
		last := strings.Join(fake.calls[3], " ")
		for _, want := range []string{"--shadow-file pkg/geom.py ", "--cache-dir ", "--show-absolute-path"} {
			if !strings.Contains(filepath.ToSlash(last), want) {
				t.Errorf("the edited run lacks %q: %s", want, last)
			}
		}
		if strings.Contains(strings.Join(fake.calls[2], " "), "--shadow-file") {
			t.Errorf("the run before the edit is shadowed: %v", fake.calls[2])
		}
		for _, d := range fake.dirs {
			if filepath.Base(d) == ".venv" || !isFile(filepath.Join(d, "main.py")) {
				t.Errorf("mypy ran in %s, not the project directory", d)
			}
		}
	})

	t.Run("an edit that changes nothing mypy minds", func(t *testing.T) {
		res, _ := check(t, []FileEdit{{File: "pkg/geom.py", OldString: "w * h", NewString: "h * w"}})
		if !res.Clean || len(res.Errors) != 0 || !strings.Contains(res.Text(0), "the project's mypy was run with each edited file replaced") {
			t.Errorf("clean %v, errors %+v\n%s", res.Clean, res.Errors, res.Text(0))
		}
	})

	t.Run("a new file is not checked, and the answer says so", func(t *testing.T) {
		res, fake := check(t, []FileEdit{{File: "pkg/extra.py", Content: str("x: int = 'no'\n")}})
		if res.Clean || !res.Incomplete || len(res.Unasked) != 1 || res.Unasked[0] != "pkg/extra.py" ||
			!strings.Contains(res.Text(0), "cannot be given one that is not there yet") {
			t.Errorf("clean %v, incomplete %v, unasked %v\n%s", res.Clean, res.Incomplete, res.Unasked, res.Text(0))
		}
		for _, c := range fake.calls {
			if strings.Contains(strings.Join(c, " "), "--shadow-file") {
				t.Errorf("a file that is not on disk was shadowed: %v", c)
			}
		}
	})

	t.Run("a syntax error stops mypy", func(t *testing.T) {
		res, _ := check(t, []FileEdit{{File: "pkg/geom.py", OldString: "h: int)", NewString: "h: int"}})
		if len(res.Errors) != 1 || res.Errors[0].File != "pkg/geom.py" || !res.Incomplete || res.Partial == "" ||
			!strings.Contains(res.Text(0), "INCOMPLETE: with the edit mypy stops") {
			t.Errorf("errors %+v, partial %q\n%s", res.Errors, res.Partial, res.Text(0))
		}
	})

	t.Run("a mypy that cannot run is an error", func(t *testing.T) {
		root := writeTree(t, tree)
		_, err := (&Runner{Repo: root, CacheDir: t.TempDir(), Exec: func(_ context.Context, _ string, _ []string, argv []string) ([]byte, []byte, int, error) {
			return nil, []byte("pyproject.toml: [mypy]: unrecognized option\n"), 2, nil
		}}).CheckEdit(context.Background(), []FileEdit{{File: "main.py", Content: str("")}})
		if err == nil || !strings.Contains(err.Error(), "could not check the project") || !strings.Contains(err.Error(), "unrecognized option") {
			t.Errorf("err = %v", err)
		}
	})
}
