package gotypes

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// ed is a module for checking edits: a package with an interface and a function other packages call, a second
// package that uses them (with one file per platform), names placed so that an edit can shadow or capture them, and
// a package that does not compile.
var ed = map[string]string{
	"go.mod": "module example.com/ed\n\ngo 1.21\n",
	"a/a.go": `package a

import "errors"

type Shape interface{ Area() float64 }

type Sq struct{ Side float64 }

func (s Sq) Area() float64 { return s.Side * s.Side }

func Scale(s Sq, by float64) Sq { return Sq{s.Side * by} }

func Parse(text string) (int, error) {
	if text == "" {
		return 0, errors.New("empty")
	}
	return len(text), nil
}
`,
	"a/a_test.go": `package a

import "testing"

func TestScale(t *testing.T) {
	if Scale(Sq{1}, 2).Side != 2 {
		t.Fail()
	}
}
`,
	"b/b.go": `package b

import "example.com/ed/a"

func Double(s a.Sq) a.Sq { return a.Scale(s, 2) }

var _ a.Shape = a.Sq{}

func Smallest(xs ...int) int {
	m := xs[0]
	for _, x := range xs {
		m = min(m, x)
	}
	return m
}

func Label(k int) string {
	name := "none"
	if k > 0 {
		name = name + "!"
		println(name)
	}
	return name
}
`,
	"b/b_linux.go": `package b

import "example.com/ed/a"

func Triple(s a.Sq) a.Sq { return a.Scale(s, 3) }
`,
	"b/b_other.go": `//go:build !linux

package b

import "example.com/ed/a"

func Triple(s a.Sq) a.Sq { return a.Sq{Side: s.Side * 3} }
`,
	"c/c.go": `package c

var Broken int = "not an int"

func Fine() int { return 1 }
`,
}

var edConfigs = Config{Configs: []BuildConfig{
	{Name: "linux", Env: map[string]string{"GOWORK": "off", "GOFLAGS": "-mod=mod", "GOOS": "linux", "GOARCH": "amd64", "CGO_ENABLED": "0", "GOPROXY": "off"}},
	{Name: "darwin", Env: map[string]string{"GOWORK": "off", "GOFLAGS": "-mod=mod", "GOOS": "darwin", "GOARCH": "arm64", "CGO_ENABLED": "0", "GOPROXY": "off"}},
}}

var vetLine = regexp.MustCompile(`(?m)^(?:vet: )?(?:\./)?([^\s:]+\.go):(\d+):\d+: `)

// vetEdited applies edits to a copy of the tree and asks the go command what it thinks, under each configuration:
// whether it fails, and the file:line of every error it prints.
func vetEdited(t *testing.T, files map[string]string, patch map[string]string) (fails bool, at map[string]bool, out string) {
	t.Helper()
	tree := map[string]string{}
	for f, s := range files {
		tree[f] = s
	}
	for f, s := range patch {
		tree[f] = s
	}
	root := writeTree(t, tree)
	at = map[string]bool{}
	for _, bc := range edConfigs.Configs {
		cmd := exec.Command("go", "vet", "./...")
		cmd.Dir, cmd.Env = root, bc.environ(os.Environ())
		var buf bytes.Buffer
		cmd.Stdout, cmd.Stderr = &buf, &buf
		if err := cmd.Run(); err != nil {
			fails = true
		}
		out += buf.String()
		for _, m := range vetLine.FindAllStringSubmatch(buf.String(), -1) {
			at[m[1]+":"+m[2]] = true
		}
	}
	return fails, at, out
}

type editCase struct {
	name  string
	edits []FileEdit
	// what the check must say
	errs  []string // "file:line [configs] substring of the message"
	moved []string // "file:line substring"
	api   []string // substrings, one per line
	fixed int
	// vet is false for an edit the go command cannot be asked about in the same way (see the case)
	noVet bool
}

func str(s string) *string { return &s }

func TestCheckEdit(t *testing.T) {
	needGo(t)
	bFile := ed["b/b.go"]
	cases := []editCase{
		{
			name:  "a body changes and nothing else",
			edits: []FileEdit{{File: "a/a.go", OldString: "return len(text), nil", NewString: "return len(text) + 0, nil"}},
		},
		{
			name:  "a function gains a parameter: every caller breaks, the platform file under its platform only",
			edits: []FileEdit{{File: "a/a.go", OldString: "func Scale(s Sq, by float64) Sq {", NewString: "func Scale(s Sq, by float64, round bool) Sq {"}},
			errs: []string{
				"a/a_test.go:6 [linux,darwin] not enough arguments in call to Scale",
				"b/b.go:5 [linux,darwin] not enough arguments in call to a.Scale",
				"b/b_linux.go:5 [linux] not enough arguments in call to a.Scale",
			},
			api: []string{"example.com/ed/a: changed: func Scale(s Sq, by float64) Sq"},
		},
		{
			name:  "a method is removed: the type stops implementing the interface it is assigned to",
			edits: []FileEdit{{File: "a/a.go", OldString: "func (s Sq) Area() float64 { return s.Side * s.Side }\n", NewString: ""}},
			errs:  []string{"b/b.go:7 [linux,darwin] missing method Area"},
			api:   []string{"example.com/ed/a: changed: type Sq struct{Side float64} with methods Area() float64"},
		},
		{
			name:  "an unused variable, which the type checker calls a soft error and the compiler rejects",
			edits: []FileEdit{{File: "a/a.go", OldString: "\treturn len(text), nil", NewString: "\tn := 3\n\treturn len(text), nil"}},
			errs:  []string{"a/a.go:17 [linux,darwin] declared and not used: n"},
		},
		{
			name:  "an unused import: the import list changes, so the configuration is listed again",
			edits: []FileEdit{{File: "b/b.go", OldString: `import "example.com/ed/a"`, NewString: "import (\n\t\"os\"\n\n\t\"example.com/ed/a\"\n)"}},
			errs:  []string{`b/b.go:4 [linux,darwin] "os" imported and not used`},
		},
		{
			name: "a new import that is used",
			edits: []FileEdit{
				{File: "b/b.go", OldString: `import "example.com/ed/a"`, NewString: "import (\n\t\"strings\"\n\n\t\"example.com/ed/a\"\n)"},
				{File: "b/b.go", OldString: `name = name + "!"`, NewString: `name = strings.ToUpper(name)`},
			},
		},
		{
			name:  "a new import of a package that does not exist",
			edits: []FileEdit{{File: "b/b.go", OldString: `import "example.com/ed/a"`, NewString: "import (\n\t\"example.com/ed/nowhere\"\n\n\t\"example.com/ed/a\"\n)\n\nvar _ = nowhere.X"}},
			errs:  []string{"b/b.go:4 [linux,darwin] could not import example.com/ed/nowhere: "},
		},
		{
			name:  "a declaration added inside a block: the lines after it, untouched, now mean the inner one",
			edits: []FileEdit{{File: "b/b.go", OldString: "\t\tname = name + \"!\"", NewString: "\t\tname := \"some\"\n\t\tname = name + \"!\""}},
			moved: []string{"b/b.go:21 name would refer to name (declared at b/b.go:20)", "b/b.go:22 name would refer to name (declared at b/b.go:20)"},
		},
		{
			name:  "a new file declares min: the call in another file, untouched, stops meaning the builtin",
			edits: []FileEdit{{File: "b/min.go", Content: str("package b\n\nfunc min(x, y int) int {\n\tif x < y {\n\t\treturn x\n\t}\n\treturn y\n}\n")}},
			moved: []string{"b/b.go:12 min would refer to min (declared at b/min.go:3), not to min (built in)"},
		},
		{
			name:  "a function moves within its file: every caller still means it",
			edits: []FileEdit{{File: "b/b.go", Content: str(strings.Replace(strings.Replace(bFile, "func Double(s a.Sq) a.Sq { return a.Scale(s, 2) }\n\n", "", 1), "func Label(", "func Double(s a.Sq) a.Sq { return a.Scale(s, 2) }\n\nfunc Label(", 1))}},
		},
		{
			name:  "a platform file breaks: only its configuration is checked",
			edits: []FileEdit{{File: "b/b_linux.go", OldString: "a.Scale(s, 3)", NewString: "a.Scale(s)"}},
			errs:  []string{"b/b_linux.go:5 [linux] not enough arguments in call to a.Scale"},
		},
		{
			name:  "an edit beside an error that is already there reports nothing",
			edits: []FileEdit{{File: "c/c.go", OldString: "return 1", NewString: "return 2"}},
		},
		{
			name:  "an edit that fixes the error that is there",
			edits: []FileEdit{{File: "c/c.go", OldString: `var Broken int = "not an int"`, NewString: `var Broken int = 7`}},
			fixed: 1,
		},
		{
			name:  "an import that closes a circle",
			edits: []FileEdit{{File: "a/a.go", OldString: `import "errors"`, NewString: "import (\n\t\"errors\"\n\n\t\"example.com/ed/b\"\n)\n\nvar _ = b.Double"}},
			errs:  []string{" [linux,darwin] import cycle not allowed"},
			noVet: true, // the go command names no line for a cycle
		},
		{
			name: "a new package is created and used",
			edits: []FileEdit{
				{File: "d/d.go", Content: str("package d\n\nfunc Nine() int { return 9 }\n")},
				{File: "b/b.go", OldString: `import "example.com/ed/a"`, NewString: "import (\n\t\"example.com/ed/a\"\n\t\"example.com/ed/d\"\n)\n\nvar _ = d.Nine() + \"x\""},
			},
			errs: []string{"b/b.go:8 [linux,darwin] mismatched types"},
		},
	}
	root := writeTree(t, ed)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	// One runner keeps its programs between cases, as a server does; the other loads afresh for each and lists
	// every configuration again with the edit in place. They must say the same.
	pc := NewProgramCache(2, time.Minute)
	defer pc.Close()
	held := &Runner{Repo: root, Config: edConfigs, Programs: pc}
	fresh := &Runner{Repo: root, Config: edConfigs, checkEditReload: true}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := held.CheckEdit(ctx, c.edits)
			if err != nil {
				t.Fatal(err)
			}
			again, err := fresh.CheckEdit(ctx, c.edits)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(findings(got), findings(again)) {
				t.Errorf("refreshing the held program and loading afresh disagree:\nrefresh:\n%s\nfresh:\n%s", got.Text(2, 0), again.Text(2, 0))
			}
			text := got.Text(2, 0)
			match := func(kind string, found []EditFinding, want []string) {
				if len(found) != len(want) {
					t.Errorf("%d %s, want %d\n%s", len(found), kind, len(want), text)
					return
				}
				for i, w := range want {
					f := found[i]
					place, rest, _ := strings.Cut(w, " ")
					if cfgs, msg, ok := strings.Cut(rest, "] "); ok && strings.HasPrefix(cfgs, "[") {
						if strings.Join(f.Configs, ",") != cfgs[1:] {
							t.Errorf("%s %d holds under %v, want %s\n%s", kind, i, f.Configs, cfgs[1:], text)
						}
						rest = msg
					}
					at := ""
					if f.File != "" {
						at = fmt.Sprintf("%s:%d", f.File, f.Line)
					}
					if at != place || !strings.Contains(f.Message, rest) {
						t.Errorf("%s %d is %q at %q, want %q at %q\n%s", kind, i, f.Message, at, rest, place, text)
					}
				}
			}
			match("errors", got.Errors, c.errs)
			match("moved identifiers", got.Moved, c.moved)
			if len(got.API) != len(c.api) {
				t.Errorf("API lines %q, want %q", got.API, c.api)
			} else {
				for i, w := range c.api {
					if !strings.Contains(got.API[i], w) {
						t.Errorf("API line %q, want one with %q", got.API[i], w)
					}
				}
			}
			if got.Fixed != c.fixed {
				t.Errorf("fixes %d errors, want %d\n%s", got.Fixed, c.fixed, text)
			}
			if clean := len(c.errs) == 0 && len(c.moved) == 0; got.Clean != clean {
				t.Errorf("Clean = %v, want %v\n%s", got.Clean, clean, text)
			}
			if got.Incomplete || got.Syntax {
				t.Errorf("incomplete or unparsed:\n%s", text)
			}

			// The go command, on the tree with the edit made. Package c fails before any edit, so it is taken out
			// of the comparison unless the case is about it.
			if c.noVet {
				return
			}
			files, patch := map[string]string{}, map[string]string{}
			for f, s := range ed {
				files[f] = s
			}
			aboutC := false
			for _, e := range c.edits {
				aboutC = aboutC || strings.HasPrefix(e.File, "c/")
				cur := files[e.File]
				if p, ok := patch[e.File]; ok {
					cur = p
				}
				if e.Content != nil {
					patch[e.File] = *e.Content
				} else {
					patch[e.File] = strings.ReplaceAll(cur, e.OldString, e.NewString)
				}
			}
			if !aboutC {
				files["c/c.go"] = "package c\n"
			}
			fails, at, out := vetEdited(t, files, patch)
			wantFail := len(c.errs) > 0 || aboutC && c.fixed == 0
			if fails != wantFail {
				t.Errorf("go vet fails = %v on the edited tree, and the check reported %d errors\n%s\n%s", fails, len(got.Errors), out, text)
			}
			ours := map[string]bool{}
			for _, f := range got.Errors {
				ours[fmt.Sprintf("%s:%d", f.File, f.Line)] = true
			}
			for place := range at {
				if !ours[place] && !(aboutC && strings.HasPrefix(place, "c/")) {
					t.Errorf("go vet reports an error at %s that the check does not\n%s\n%s", place, out, text)
				}
			}
		})
	}
}

// findings is what a check found, without what differs between two runs of the same check (their times).
func findings(c *EditCheck) any {
	sort.Strings(c.API)
	return []any{c.Files, c.Errors, c.Moved, c.API, c.Fixed, c.Clean, c.Incomplete}
}

func TestCheckEdit_refusals(t *testing.T) {
	needGo(t)
	root := writeTree(t, ed)
	r := &Runner{Repo: root, Config: edConfigs}
	ctx := context.Background()
	for _, c := range []struct {
		edit FileEdit
		want string
	}{
		{FileEdit{File: "a/a.go", OldString: "no such text", NewString: "x"}, "old_string is not in the file"},
		{FileEdit{File: "a/a.go", OldString: "Sq", NewString: "Square"}, "occurs"},
		{FileEdit{File: "a/a.go", OldString: "errors", NewString: "errors"}, "occurs"},
		{FileEdit{File: "../outside.go", Content: str("package x\n")}, "outside the repository"},
		{FileEdit{File: "go.mod", Content: str("module x\n")}, "only .go files"},
		{FileEdit{File: "a/new.go", OldString: "x", NewString: "y"}, "does not exist"},
		{FileEdit{File: "a/a.go", Content: str(ed["a/a.go"])}, "changes nothing"},
	} {
		if _, err := r.CheckEdit(ctx, []FileEdit{c.edit}); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: error %v, want one saying %q", c.edit, err, c.want)
		}
	}
	// A file that does not parse is reported as that, and nothing is type-checked.
	got, err := r.CheckEdit(ctx, []FileEdit{{File: "a/a.go", OldString: "func Scale(s Sq, by float64) Sq {", NewString: "func Scale(s Sq, by float64 Sq {"}})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Syntax || got.Clean || len(got.Errors) == 0 || got.Errors[0].File != "a/a.go" || got.Errors[0].Line != 11 {
		t.Errorf("a syntax error was not reported as one:\n%s", got.Text(2, 0))
	}
	// Nothing was written.
	if src, _ := os.ReadFile(filepath.Join(root, "a", "a.go")); string(src) != ed["a/a.go"] {
		t.Error("the check changed a file")
	}
}

func TestLineMap(t *testing.T) {
	old := "a\nb\nc\nd\ne\n"
	for _, c := range []struct {
		new   string
		lines []int // of new, written
		gone  int
		// where old line 4 ("d") is in new: its line there, or 0 when it is in a hunk
		d int
	}{
		{"a\nb\nc\nd\ne\n", nil, 0, 4},
		{"a\nX\nb\nc\nd\ne\n", []int{2}, 0, 5},
		{"a\nc\nd\ne\n", nil, 1, 3},
		{"a\nb\nc\nD\ne\n", []int{4}, 1, 0},
		{"a\nB\nc\nd\nE\nf\n", []int{2, 5, 6}, 2, 4},
		{"x\ny\n", []int{1, 2}, 5, 0},
	} {
		m := newLineMap([]byte(old), []byte(c.new))
		lines, gone := m.changedLines()
		if !reflect.DeepEqual(lines, c.lines) || gone != c.gone {
			t.Errorf("%q: wrote lines %v and removed %d, want %v and %d", c.new, lines, gone, c.lines, c.gone)
		}
		off, hunk := m.toNew(strings.Index(old, "d"))
		got := 0
		if hunk < 0 {
			got = lineOf([]byte(c.new), off)
			if c.new[off] != 'd' {
				t.Errorf("%q: old d maps to offset %d, which holds %q", c.new, off, c.new[off])
			}
		}
		if got != c.d {
			t.Errorf("%q: old line 4 is at new line %d, want %d", c.new, got, c.d)
		}
	}
}

// A held program is a whole workspace: a question about a symbol in another directory is answered from it, not by
// loading the repository again.
func TestProgramCache_oneProgramServesEveryDirectory(t *testing.T) {
	needGo(t)
	root := writeTree(t, ed)
	pc := NewProgramCache(2, time.Minute)
	defer pc.Close()
	r := &Runner{Repo: root, Config: edConfigs, Programs: pc}
	for _, sym := range []string{"a.Scale", "b.Double", "a.Parse", "b.Label"} {
		if _, err := r.References(context.Background(), sym); err != nil {
			t.Fatal(err)
		}
	}
	if st := pc.Status(); st.Loads != 2 || st.Evictions != 0 || len(st.Programs) != 2 {
		t.Errorf("four questions in two directories under two configurations: %d loads, %d evictions, %d programs held; want 2, 0, 2", st.Loads, st.Evictions, len(st.Programs))
	}
}
