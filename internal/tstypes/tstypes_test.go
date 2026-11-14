package tstypes

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

// fakeServer answers textDocument/diagnostic the way a compiler would for a toy language: every line that holds
// "BAD" is an error (code 1), and every line that holds "use X" is an error (code 2) unless some file holds "def X".
// It reads open documents before the disk, and refuses any file named refuse.ts.
type fakeServer struct {
	in         *io.PipeReader
	inW        *io.PipeWriter
	out        *io.PipeReader
	outW       *io.PipeWriter
	root       string
	mu         sync.Mutex
	open       map[string]string
	closes     int
	once       sync.Once
	sawShutdow bool
}

func newFake(root string) *fakeServer {
	f := &fakeServer{root: root, open: map[string]string{}}
	f.in, f.inW = io.Pipe()
	f.out, f.outW = io.Pipe()
	go f.serve()
	return f
}

func (f *fakeServer) Read(b []byte) (int, error)  { return f.out.Read(b) }
func (f *fakeServer) Write(b []byte) (int, error) { return f.inW.Write(b) }
func (f *fakeServer) Pid() int                    { return 0 }
func (f *fakeServer) Stderr() string              { return "" }
func (f *fakeServer) Close() error {
	f.once.Do(func() { _ = f.inW.Close(); _ = f.outW.Close() })
	return nil
}

func (f *fakeServer) text(path string) string {
	if s, ok := f.open[path]; ok {
		return s
	}
	b, _ := os.ReadFile(path)
	return string(b)
}

func (f *fakeServer) all() string {
	var b strings.Builder
	seen := map[string]bool{}
	_ = filepath.WalkDir(f.root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && IsSource(p) {
			seen[p] = true
			b.WriteString(f.text(p) + "\n")
		}
		return nil
	})
	for p, s := range f.open {
		if !seen[p] {
			b.WriteString(s + "\n")
		}
	}
	return b.String()
}

func (f *fakeServer) reply(id json.RawMessage, result any, errMsg string) {
	m := map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
	if errMsg != "" {
		delete(m, "result")
		m["error"] = map[string]any{"code": -32600, "message": errMsg}
	}
	body, _ := json.Marshal(m)
	_, _ = fmt.Fprintf(f.outW, "Content-Length: %d\r\n\r\n%s", len(body), body)
}

func (f *fakeServer) serve() {
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
				TextDocument struct{ URI, Text string }
			}
		}
		_ = json.Unmarshal(buf, &m)
		path, _ := lsp.URIToPath(m.Params.TextDocument.URI)
		f.mu.Lock()
		switch m.Method {
		case "initialize":
			f.reply(m.ID, map[string]any{"serverInfo": map[string]string{"name": "fake", "version": "7.0.0"}}, "")
		case "textDocument/didOpen":
			f.open[path] = m.Params.TextDocument.Text
		case "textDocument/didClose":
			delete(f.open, path)
			f.closes++
		case "shutdown":
			f.sawShutdow = true
			f.reply(m.ID, nil, "")
		case "textDocument/diagnostic":
			if filepath.Base(path) == "refuse.ts" {
				f.reply(m.ID, nil, "no project for this file")
				break
			}
			items := []any{}
			add := func(line, sev, code int, msg string) {
				items = append(items, map[string]any{"severity": sev, "code": code, "message": msg,
					"range": map[string]any{"start": map[string]int{"line": line}, "end": map[string]int{"line": line}}})
			}
			whole := f.all()
			for i, l := range strings.Split(f.text(path), "\n") {
				if strings.Contains(l, "BAD") {
					add(i, 1, 1, "bad\n  line")
				}
				if strings.Contains(l, "HINT") {
					add(i, 4, 9, "unused")
				}
				if _, name, ok := strings.Cut(l, "use "); ok && !strings.Contains(whole, "def "+name) {
					add(i, 1, 2, "cannot find "+name)
				}
			}
			f.reply(m.ID, map[string]any{"kind": "full", "items": items}, "")
		default:
			if len(m.ID) > 0 {
				f.reply(m.ID, nil, "")
			}
		}
		f.mu.Unlock()
	}
}

func fakeRunner(t *testing.T, root string) (*Runner, *fakeServer) {
	srv := newFake(root)
	return &Runner{Repo: root, Tsc: "fake-tsc", Spawn: func(_ context.Context, _ string, _, argv []string) (lsp.Proc, error) {
		if strings.Join(argv, " ") != "fake-tsc --lsp --stdio" {
			t.Errorf("argv = %q", argv)
		}
		return srv, nil
	}}, srv
}

func str(s string) *string { return &s }

func findings(c *EditCheck) string {
	var out []string
	for _, e := range c.Errors {
		out = append(out, fmt.Sprintf("%s:%d: %s", e.File, e.Line, e.Message))
	}
	return strings.Join(out, " | ")
}

func TestCheckEdit_againstAFakeServer(t *testing.T) {
	tree := map[string]string{
		"a.ts":           "def one\ndef two\nok\n",
		"b.ts":           "use one\nBAD already\nuse two\n",
		"sub/c.tsx":      "use two\n",
		"notes.md":       "BAD but not source\n",
		".gitignore":     "dist/\n",
		"dist/bundle.js": "BAD in build output\n",
	}
	for _, tc := range []struct {
		name  string
		edits []FileEdit
		want  string // findings
		fixed int
		clean bool
	}{
		{"a body change is clean", []FileEdit{{File: "a.ts", OldString: "ok", NewString: "fine"}}, "", 0, true},
		{"removing a definition breaks its users in files that were not edited",
			[]FileEdit{{File: "a.ts", OldString: "def two\n", NewString: ""}},
			"b.ts:3: TS2: cannot find two | sub/c.tsx:1: TS2: cannot find two", 0, false},
		{"an error that was there is not reported, also when the edit moves it down a line",
			[]FileEdit{{File: "b.ts", OldString: "use one", NewString: "use one\nok"}}, "", 0, true},
		{"a second error with the same message in an edited file is new",
			[]FileEdit{{File: "b.ts", OldString: "use one", NewString: "BAD again"}}, "b.ts:1: TS1: bad line", 0, false},
		{"fixing an error is counted", []FileEdit{{File: "b.ts", OldString: "BAD already\n", NewString: ""}}, "", 1, true},
		{"a new file is checked, and can define what others use",
			[]FileEdit{{File: "new.ts", Content: str("def three\nBAD\n")}, {File: "sub/c.tsx", OldString: "two", NewString: "three"}},
			"new.ts:2: TS1: bad line", 0, false},
		{"two edits to one file apply in order, the edited file's errors first",
			[]FileEdit{{File: "sub/c.tsx", OldString: "use two", NewString: "use four"}, {File: "sub/c.tsx", OldString: "four", NewString: "five"},
				{File: "a.ts", OldString: "def one\n", NewString: ""}},
			"sub/c.tsx:1: TS2: cannot find five | b.ts:1: TS2: cannot find one", 0, false},
		{"a hint is not an error", []FileEdit{{File: "a.ts", OldString: "ok", NewString: "HINT"}}, "", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := writeTree(t, tree)
			r, srv := fakeRunner(t, root)
			res, err := r.CheckEdit(context.Background(), tc.edits)
			if err != nil {
				t.Fatal(err)
			}
			if got := findings(res); got != tc.want {
				t.Errorf("errors:\n got %s\nwant %s", got, tc.want)
			}
			if res.Fixed != tc.fixed || res.Clean != tc.clean || res.Incomplete {
				t.Errorf("fixed %d clean %v incomplete %v, want %d %v false", res.Fixed, res.Clean, res.Incomplete, tc.fixed, tc.clean)
			}
			if res.Compiler != "7.0.0" || res.Checked < 3 {
				t.Errorf("compiler %q, checked %d", res.Compiler, res.Checked)
			}
			text := res.Text(1)
			if tc.clean != strings.Contains(text, "\nchecked: ") || (tc.want != "") != strings.Contains(text, "WOULD NOT COMPILE") {
				t.Errorf("text does not carry the verdict:\n%s", text)
			}
			// nothing was written, and the server was stopped
			for name, src := range tree {
				if b, _ := os.ReadFile(filepath.Join(root, name)); string(b) != src {
					t.Errorf("%s was changed on disk", name)
				}
			}
			if _, err := os.Stat(filepath.Join(root, "new.ts")); err == nil {
				t.Error("the new file was written")
			}
			srv.mu.Lock()
			if !srv.sawShutdow {
				t.Error("the server was not shut down")
			}
			srv.mu.Unlock()
		})
	}
}

func TestCheckEdit_aRefusedFileMakesTheAnswerIncomplete(t *testing.T) {
	root := writeTree(t, map[string]string{"a.ts": "ok\n", "refuse.ts": "BAD\n"})
	r, _ := fakeRunner(t, root)
	res, err := r.CheckEdit(context.Background(), []FileEdit{{File: "a.ts", Content: str("fine\n")}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Clean || !res.Incomplete || strings.Join(res.Unasked, ",") != "refuse.ts" || res.UnaskedWhy != "no project for this file" {
		t.Errorf("%+v", res)
	}
	if text := res.Text(0); !strings.Contains(text, "INCOMPLETE: the compiler did not answer for 1 files (refuse.ts: no project for this file)") ||
		!strings.Contains(text, "NO NEW ERROR FOUND, but not every file was checked") {
		t.Error(text)
	}
}

func TestCheckEdit_refusals(t *testing.T) {
	root := writeTree(t, map[string]string{"a.ts": "x\nx\n", "tsconfig.json": "{}"})
	outside := filepath.Join(filepath.Dir(root), "elsewhere.ts")
	for _, tc := range []struct {
		edits []FileEdit
		want  string
	}{
		{nil, "no edit given"},
		{[]FileEdit{{File: " "}}, "names no file"},
		{[]FileEdit{{File: outside, Content: str("")}}, "outside the repository"},
		{[]FileEdit{{File: "../elsewhere.ts", Content: str("")}}, "outside the repository"},
		{[]FileEdit{{File: "tsconfig.json", Content: str("{}")}}, "only TypeScript and JavaScript source files"},
		{[]FileEdit{{File: "missing.ts", OldString: "a", NewString: "b"}}, "does not exist"},
		{[]FileEdit{{File: "a.ts"}}, "needs old_string"},
		{[]FileEdit{{File: "a.ts", OldString: "y", NewString: "z"}}, "is not in the file"},
		{[]FileEdit{{File: "a.ts", OldString: "x", NewString: "z"}}, "occurs 2 times"},
		{[]FileEdit{{File: "a.ts", Content: str("x\nx\n")}}, "changes nothing"},
	} {
		r, _ := fakeRunner(t, root)
		if _, err := r.CheckEdit(context.Background(), tc.edits); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: err = %v, want %q", tc.edits, err, tc.want)
		}
	}
	if _, err := (&Runner{Repo: root, MaxFiles: -1}).sources(root); err != nil {
		t.Error(err)
	}
	r := &Runner{Repo: root, MaxFiles: 1}
	_ = os.WriteFile(filepath.Join(root, "b.ts"), nil, 0o644)
	if _, err := r.sources(root); err == nil || !strings.Contains(err.Error(), "more than the 1") {
		t.Errorf("over the cap: %v", err)
	}
}

func TestFindCompiler(t *testing.T) {
	exe := "tsc"
	if runtime.GOOS == "windows" {
		exe = "tsc.exe"
	}
	native := "node_modules/@typescript/" + platformPackage() + "/lib/" + exe
	root := writeTree(t, map[string]string{
		"packages/new/" + native:                                "",
		"packages/new/src/x.ts":                                 "",
		"packages/old/node_modules/typescript/package.json":     `{"version":"5.9.3"}`,
		"packages/old/src/x.ts":                                 "",
		"packages/half/node_modules/typescript/package.json":    `{"version":"7.0.2"}`,
		"packages/half/src/x.ts":                                "",
		"packages/none/src/x.ts":                                "",
		"packages/dir/node_modules/@typescript/placeholder.txt": "",
	})
	got, err := FindCompiler(root, filepath.Join(root, "packages/new/src"))
	if err != nil || got != filepath.Join(root, "packages/new", filepath.FromSlash(native)) {
		t.Errorf("new: %q, %v", got, err)
	}
	for dir, want := range map[string]string{
		"packages/old/src":  "has TypeScript 5.9.3 installed",
		"packages/half/src": "its native program for this machine",
		"packages/none/src": "install the project's dependencies",
	} {
		_, err := FindCompiler(root, filepath.Join(root, dir))
		if !errors.Is(err, ErrNoCompiler) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", dir, err, want)
		}
	}
	// a compiler above the repository root is not the project's
	if _, err := FindCompiler(filepath.Join(root, "packages/new/src"), filepath.Join(root, "packages/new/src")); !errors.Is(err, ErrNoCompiler) {
		t.Errorf("found a compiler above the root: %v", err)
	}
}

// The tests below run a real TypeScript 7. They need its native program, which is not something ken ships or
// downloads: set KEN_TEST_TSC to node_modules/@typescript/typescript-<os>-<arch>/lib/tsc of any project that has it.
const realTree = "KEN_TEST_TSC"

var realFixture = map[string]string{
	"tsconfig.json":               `{"files": [], "references": [{"path": "packages/core"}, {"path": "packages/app"}]}`,
	"packages/core/tsconfig.json": `{"compilerOptions": {"composite": true, "strict": true, "outDir": "dist", "rootDir": "src"}, "include": ["src"]}`,
	"packages/app/tsconfig.json":  `{"compilerOptions": {"composite": true, "strict": true, "outDir": "dist", "rootDir": "src"}, "include": ["src"], "references": [{"path": "../core"}]}`,
	"packages/core/src/shape.ts": `export interface Shape {
  area(): number;
}

export class Square implements Shape {
  constructor(public side: number) {}
  area(): number {
    return this.side * this.side;
  }
}

export function scale(s: Square, by: number): Square {
  return new Square(s.side * by);
}
`,
	"packages/app/src/main.ts": `import { scale, Square, Shape } from "../../core/src/shape";

export function double(s: Square): Square {
  return scale(s, 2);
}

export const shapes: Shape[] = [new Square(1)];
`,
	"packages/app/src/broken.ts": `export const n: number = "already broken";
`,
	".gitignore": "dist/\n",
}

// compilerErrors builds the tree with the real compiler and returns its errors as "file: TScode", sorted.
func compilerErrors(t *testing.T, tsc, root string) []string {
	t.Helper()
	cmd := testenv.Command(t, tsc, "-b", "--pretty", "false")
	cmd.Dir = root
	out, _ := cmd.CombinedOutput()
	var errs []string
	for _, m := range regexp.MustCompile(`(?m)^(\S+?)\(\d+,\d+\): error (TS\d+):`).FindAllStringSubmatch(string(out), -1) {
		if strings.Contains(m[1], "/dist/") {
			continue // the same error again, in the declaration file the build wrote
		}
		errs = append(errs, m[1]+": "+m[2])
	}
	sort.Strings(errs)
	return errs
}

func TestCheckEdit_againstTheRealCompiler(t *testing.T) {
	tsc := os.Getenv(realTree)
	if tsc == "" {
		t.Skip(realTree + " is not set")
	}
	for _, tc := range []struct {
		name  string
		edits []FileEdit
		want  string
		fixed int
	}{
		{"a body change", []FileEdit{{File: "packages/core/src/shape.ts", OldString: "s.side * by", NewString: "by * s.side"}}, "", 0},
		{"a new parameter breaks a caller in the other project",
			[]FileEdit{{File: "packages/core/src/shape.ts", OldString: "by: number): Square {", NewString: "by: number, round: boolean): Square {"}},
			"packages/app/src/main.ts:4: TS2554: Expected 3 arguments, but got 2.", 0},
		{"a removed interface member",
			[]FileEdit{{File: "packages/core/src/shape.ts", OldString: "  area(): number;\n}", NewString: "  area(): number;\n  name: string;\n}"}},
			"packages/core/src/shape.ts:6: TS2420: Class 'Square' incorrectly implements interface 'Shape'. Property 'name' is missing in type 'Square' but required in type 'Shape'. | " +
				"packages/app/src/main.ts:7: TS2741: Property 'name' is missing in type 'Square' but required in type 'Shape'.", 0},
		{"a syntax error", []FileEdit{{File: "packages/app/src/main.ts", OldString: "return scale(s, 2);", NewString: "return scale(s, 2;"}},
			"packages/app/src/main.ts:4: TS1005: ')' expected.", 0},
		{"a fix", []FileEdit{{File: "packages/app/src/broken.ts", OldString: `"already broken"`, NewString: "1"}}, "", 1},
		{"a new file that uses the other project wrongly",
			[]FileEdit{{File: "packages/app/src/extra.ts", Content: str("import { scale } from \"../../core/src/shape\";\nexport const s = scale(1, 2);\n")}},
			"packages/app/src/extra.ts:2: TS2345: Argument of type 'number' is not assignable to parameter of type 'Square'.", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := writeTree(t, realFixture)
			r := &Runner{Repo: root, Tsc: tsc}
			res, err := r.CheckEdit(context.Background(), tc.edits)
			if err != nil {
				t.Fatal(err)
			}
			if got := findings(res); got != tc.want {
				t.Errorf("errors:\n got %s\nwant %s", got, tc.want)
			}
			if res.Fixed != tc.fixed || res.Incomplete || res.Clean != (tc.want == "") {
				t.Errorf("fixed %d, incomplete %v, clean %v", res.Fixed, res.Incomplete, res.Clean)
			}
			t.Logf("\n%s", res.Text(0))

			// The oracle: write the edit to a copy and build both with the compiler. What the build gains is what
			// the check must have reported, by file and error code.
			base := compilerErrors(t, tsc, writeTree(t, realFixture))
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
			after := compilerErrors(t, tsc, writeTree(t, patched))
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
			var reported []string
			for _, e := range res.Errors {
				reported = append(reported, e.File+": "+strings.SplitN(e.Message, ":", 2)[0])
			}
			sort.Strings(reported)
			if strings.Join(gained, " | ") != strings.Join(reported, " | ") {
				t.Errorf("the build gains %v\nthe check reported %v", gained, reported)
			}
		})
	}
}
