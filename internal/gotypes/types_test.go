package gotypes

import (
	"context"
	"fmt"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// tricky is a module built from the cases where gopls's notion of a reference is not the obvious one: a method and
// the interface methods it satisfies in either direction, the standard library's interfaces counted inside the
// declaring package only, a test file that adds a method, an external test package, a package recompiled for another
// package's test, promoted methods and fields, generics, types local to a function, unexported names, aliases and dot
// imports, and a file behind a build tag.
var tricky = map[string]string{
	"go.mod": "module example.com/tricky\n\ngo 1.22\n",
	"a/a.go": `package a

import "io"

// Shape is implemented in a, b and c.
type Shape interface {
	Area() float64
	Name() string
}

// Namer is a part of Shape: a Shape's Name corresponds to a Namer's.
type Namer interface{ Name() string }

// Solid extends Shape.
type Solid interface {
	Shape
	Volume() float64
}

type Square struct{ Side float64 }

func (s Square) Area() float64 { return s.Side * s.Side }
func (s Square) Name() string  { return "square" }

type Cube struct {
	Square
	Depth float64
}

func (c *Cube) Volume() float64 { return c.Area() * c.Depth }

// Err is an error: Error corresponds to error.Error inside this package only.
type Err struct{ Msg string }

func (e *Err) Error() string { return e.Msg }

// File is an io.Closer.
type File struct{ open bool }

func (f *File) Close() error { f.open = false; return nil }

func Total(ss []Shape) float64 {
	t := 0.0
	for _, s := range ss {
		t += s.Area()
	}
	return t
}

func Describe(n Namer) string { return n.Name() }

func Fail(err error) string { return err.Error() }

func Shut(c io.Closer) error { return c.Close() }

func use() (string, error) {
	var e error = &Err{Msg: "x"}
	f := &File{}
	_ = Shut(f)
	return e.Error() + Describe(Square{}), f.Close()
}

type hidden struct{ Count int }

func (h hidden) Bump() int { return h.Count + 1 }

func viaHidden() int {
	h := hidden{Count: 1}
	return h.Bump() + h.Count
}

func local() float64 {
	type tri struct{ B, H float64 }
	t := tri{B: 1, H: 2}
	return t.B * t.H
}

type localShape interface{ Area() float64 }

func viaLocal() float64 {
	type blob struct{ r float64 }
	var x localShape = Square{Side: 2}
	_ = blob{r: 1}
	return x.Area()
}

const Limit = 10

var Default = Square{Side: Limit}

type Alias = Square

func aliased() float64 { return Alias{Side: 1}.Area() }

// Visitor takes an anonymous interface: a signature that is matched loosely across packages.
type Visitor interface {
	Visit(x interface{ Area() float64 }) bool
}
`,
	"a/a_test.go": `package a

import "testing"

// A test file that adds a method: the plain package and its test variant are then both searched.
func (s Square) Perimeter() float64 { return 4 * s.Side }

type testShape struct{}

func (testShape) Area() float64 { return 1 }
func (testShape) Name() string  { return "t" }

func TestTotal(t *testing.T) {
	if Total([]Shape{Square{Side: 1}, testShape{}}) != 2 {
		t.Fatal()
	}
	_ = Square{}.Perimeter()
	_ = Fail(&Err{})
}
`,
	"a/x_test.go": `package a_test

import (
	"testing"

	"example.com/tricky/a"
	"example.com/tricky/b"
)

func TestExternal(t *testing.T) {
	var s a.Shape = b.Circle{R: 1}
	_ = s.Area() + a.Total(nil) + a.Limit
	_ = a.Default.Side
	_ = (&a.Err{}).Error()
}
`,
	"b/b.go": `package b

import (
	"fmt"

	alias "example.com/tricky/a"
	. "example.com/tricky/a"
)

type Circle struct{ R float64 }

func (c Circle) Area() float64 { return 3 * c.R * c.R }
func (c Circle) Name() string  { return "circle" }

// Ball embeds a.Cube: Area and Volume are promoted from another package.
type Ball struct {
	alias.Cube
	Label string
}

type areaOnly interface{ Area() float64 }

func Sum() float64 {
	shapes := []Shape{Circle{R: 1}, Square{Side: 2}}
	var ao areaOnly = Circle{}
	b := &Ball{Label: "b"}
	b.Side = Limit
	fmt.Println(alias.Describe(Circle{}), b.Label)
	return Total(shapes) + ao.Area() + b.Volume() + b.Area()
}

func Errs() string {
	e := &alias.Err{Msg: "m"}
	var err error = e
	return e.Error() + err.Error() + e.Msg
}

type walker struct{}

func (walker) Visit(x interface{ Area() float64 }) bool { return x.Area() > 0 }

var _ alias.Visitor = walker{}
`,
	"b/b_fancy.go": `//go:build fancy

package b

import "example.com/tricky/a"

type Star struct{ Points int }

func (s Star) Area() float64 { return float64(s.Points) }
func (s Star) Name() string  { return "star" }

func Fancy() float64 { return a.Total([]a.Shape{Star{Points: 5}}) + Star{}.Area() }
`,
	"c/c.go": `package c

import (
	"example.com/tricky/a"
	"example.com/tricky/b"
)

// Box is generic: a field reached through an instantiation is a different object to the type checker.
type Box[T any] struct{ Item T }

func (b Box[T]) Get() T { return b.Item }

func (b *Box[T]) Set(v T) { b.Item = v }

type Getter[T any] interface{ Get() T }

func Unbox() float64 {
	bx := Box[a.Square]{Item: a.Square{Side: 1}}
	bx.Set(a.Square{Side: 2})
	var g Getter[a.Square] = bx
	return g.Get().Area() + bx.Item.Side + b.Sum()
}

type Pair[K comparable, V any] struct {
	Key K
	Val V
}

func First[K comparable, V any](p Pair[K, V]) K { return p.Key }

func usePair() string { return First(Pair[string, int]{Key: "k", Val: 1}) }
`,
	"d/d.go": `package d

import "example.com/tricky/c"

// Peek uses fields of c's generic types through instantiations, from another package: gopls's references leave
// these lines out (its rename edits them); this engine returns them unless asked for gopls's exact lines.
func Peek() int {
	bx := c.Box[int]{Item: 1}
	p := c.Pair[string, int]{Key: "k", Val: 2}
	return bx.Item + p.Val + len(p.Key) + bx.Get()
}
`,
	"c/c_test.go": `package c

import "testing"

func TestUnbox(t *testing.T) {
	bx := Box[int]{Item: 1}
	if bx.Get() != 1 || Unbox() == 0 || usePair() != "k" {
		t.Fatal()
	}
}
`,
}

var trickyConfigs = Config{Configs: []BuildConfig{
	{Name: "plain", Env: map[string]string{"GOWORK": "off", "GOFLAGS": "-mod=mod"}},
	{Name: "fancy", Env: map[string]string{"GOWORK": "off", "GOFLAGS": "-mod=mod -tags=fancy"}},
}}

// The in-process engine against gopls, on every declaration of the tricky module under both configurations. This is
// the differential that TestDifferentialAgainstGopls runs on a real repository, small enough to run every time gopls
// is installed.
func TestTypesEngine_matchesGoplsOnTheTrickyModule(t *testing.T) {
	needGo(t)
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not on PATH")
	}
	root := writeTree(t, tricky)
	asked, diffs := runDifferential(t, root, trickyConfigs, diffOptions{step: 1, impls: true})
	if asked < 100 {
		t.Errorf("only %d questions were asked: the fixture's declarations were not found", asked)
	}
	if diffs > 0 {
		t.Errorf("%d of %d answers differ from gopls's", diffs, asked)
	}
}

// The same engine with no gopls anywhere: its answers on the tricky module, written down. Each expectation is what
// gopls answers (the test above keeps them honest), so this pins the behaviour where gopls is not installed.
func TestTypesEngine_answersWithoutGopls(t *testing.T) {
	needGo(t)
	root := writeTree(t, tricky)
	r := &Runner{Repo: root, Config: trickyConfigs, Engine: EngineTypes, Gopls: "/nonexistent/gopls", Timeout: 2 * time.Minute}
	ctx := context.Background()
	refs := func(sym string) []string {
		t.Helper()
		res, err := r.References(ctx, sym)
		if err != nil {
			t.Fatalf("%s: %v", sym, err)
		}
		if res.Incomplete {
			t.Fatalf("%s: incomplete:\n%s", sym, res.Text(2, 0))
		}
		return hitLines(res)
	}
	check := func(what string, got, want []string) {
		t.Helper()
		if !slices.Equal(got, want) {
			t.Errorf("%s:\n got  %q\n want %q", what, got, want)
		}
	}
	// a package-level function: its direct importers, test packages included; the fancy file only under fancy
	check("references of a.Total", refs("a.Total"), []string{
		"a/a_test.go:14 plain,fancy", "a/x_test.go:12 plain,fancy", "b/b.go:29 plain,fancy", "b/b_fancy.go:12 fancy"})
	// an interface method: calls through it, through an interface it is part of or extends, and on each type that
	// implements it; the call through an anonymous interface (b.go:40) is not one of them
	check("references of Shape.Area", refs("Shape.Area"), []string{
		"a/a.go:30 plain,fancy", "a/a.go:45 plain,fancy", "a/a.go:84 plain,fancy", "a/a.go:93 plain,fancy",
		"a/x_test.go:12 plain,fancy", "b/b.go:29 plain,fancy", "b/b_fancy.go:12 fancy", "c/c.go:21 plain,fancy"})
	// Error on a type that is an error: err.Error() counts inside the declaring package and nowhere else
	check("references of Err.Error", refs("Err.Error"), []string{
		"a/a.go:52 plain,fancy", "a/a.go:60 plain,fancy", "a/x_test.go:14 plain,fancy", "b/b.go:35 plain,fancy"})
	// a field of a generic type: the uses through an instantiation in another package (d) are returned, which gopls's
	// references leave out; GoplsExact gives gopls's lines
	check("references of Box.Item", refs("Box.Item"), []string{
		"c/c.go:11 plain,fancy", "c/c.go:13 plain,fancy", "c/c.go:18 plain,fancy", "c/c.go:21 plain,fancy", "c/c_test.go:6 plain,fancy",
		"d/d.go:8 plain,fancy", "d/d.go:10 plain,fancy"})
	r.GoplsExact = true
	check("references of Box.Item, gopls's exact lines", refs("Box.Item"), []string{
		"c/c.go:11 plain,fancy", "c/c.go:13 plain,fancy", "c/c.go:18 plain,fancy", "c/c.go:21 plain,fancy", "c/c_test.go:6 plain,fancy"})
	r.GoplsExact = false
	// an unexported type's exported field: inside the package only
	check("references of hidden.Count", refs("hidden.Count"), []string{
		"a/a.go:65 plain,fancy", "a/a.go:68 plain,fancy", "a/a.go:69 plain,fancy"})

	res, err := r.Implementations(ctx, "Shape")
	if err != nil {
		t.Fatal(err)
	}
	check("implementations of Shape", hitLines(res), []string{
		"a/a.go:15 plain,fancy", "a/a.go:20 plain,fancy", "a/a.go:25 plain,fancy", "a/a_test.go:8 plain,fancy",
		"b/b.go:10 plain,fancy", "b/b.go:16 plain,fancy", "b/b_fancy.go:7 fancy"})
	for _, run := range res.Runs {
		if run.Err != "" {
			t.Errorf("configuration %s failed: %s", run.Config, run.Err)
		}
	}
}

func TestTypesEngine_parallelGivesTheSameAnswer(t *testing.T) {
	needGo(t)
	root := writeTree(t, tricky)
	ctx := context.Background()
	one := &Runner{Repo: root, Config: trickyConfigs, Engine: EngineTypes}
	two := &Runner{Repo: root, Config: trickyConfigs, Engine: EngineTypes, Parallel: 2}
	for _, sym := range []string{"Shape.Area", "a.Total", "Square.Side"} {
		a, err := one.References(ctx, sym)
		if err != nil {
			t.Fatal(err)
		}
		b, err := two.References(ctx, sym)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(hitLines(a), hitLines(b)) || len(a.Hits) == 0 {
			t.Errorf("%s: one at a time %v, two at once %v", sym, hitLines(a), hitLines(b))
		}
	}
}

// Which packages are checked with function bodies: every searched package, except a plain package whose test variant
// answers for it, which is every one whose test files declare no method.
func TestLoad_checksBodiesOnlyWhereTheAnswerNeedsThem(t *testing.T) {
	needGo(t)
	root := writeTree(t, tricky)
	r := &Runner{Repo: root, Config: trickyConfigs}
	prog, err := r.load(context.Background(), trickyConfigs.Configs[0], root)
	if err != nil {
		t.Fatal(err)
	}
	const m = "example.com/tricky/"
	want := map[string]bool{
		m + "a":                        true,  // a_test.go declares a method on Square: both are searched
		m + "a [" + m + "a.test]":      true,  //
		m + "a_test [" + m + "a.test]": true,  // the external test package
		m + "b":                        true,  // no tests: the plain package is all there is
		m + "c":                        false, // its test variant holds every file and adds no method
		m + "c [" + m + "c.test]":      true,  //
		m + "b [" + m + "a.test]":      false, // recompiled for a's test: imported, never searched
		"fmt":                          false, // a dependency: declarations only
	}
	for id, bodies := range want {
		tp := prog.pkgs[id]
		if tp == nil {
			t.Errorf("no package %q in the program; have %d", id, len(prog.pkgs))
			continue
		}
		if tp.bodies != bodies {
			t.Errorf("%s: checked with bodies = %v, want %v", id, tp.bodies, bodies)
		}
		if tp.types == nil {
			t.Errorf("%s did not type-check", id)
		}
	}
	if itv := prog.pkgs[m+"b ["+m+"a.test]"]; itv != nil && (!itv.itv() || itv.full()) {
		t.Errorf("b [a.test] should be an intermediate test variant, not searched")
	}
}

func TestProgramCache_keepsAProgramAndReloadsAfterAnEdit(t *testing.T) {
	needGo(t)
	root := writeTree(t, tricky)
	settle(t, root)
	pc := NewProgramCache(2, time.Minute)
	defer pc.Close()
	r := &Runner{Repo: root, Config: trickyConfigs, Engine: EngineTypes, Programs: pc}
	ctx := context.Background()
	refs := func(sym string) []string {
		t.Helper()
		res, err := r.References(ctx, sym)
		if err != nil || res.Incomplete {
			t.Fatalf("%s: %v %v", sym, err, res)
		}
		return hitLines(res)
	}
	before := refs("a.Total")
	if st := pc.Status(); st.Loads != 2 || len(st.Programs) != 2 || st.Bytes <= 0 || st.Programs[0].Packages == 0 {
		t.Fatalf("after the first query: %+v, want two programs loaded and their memory reported", st)
	}
	refs("Shape.Area") // a different question: no load
	if st := pc.Status(); st.Loads != 2 || st.Reuses != 2 {
		t.Errorf("after a second question: loads %d reuses %d, want 2 and 2", st.Loads, st.Reuses)
	}
	// an edit: the held programs describe the old tree and must not answer
	p := filepath.Join(root, "b", "b.go")
	src, _ := os.ReadFile(p)
	if err := os.WriteFile(p, append(src, []byte("\nfunc More() float64 { return Total(nil) }\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	after := refs("a.Total")
	if len(after) != len(before)+1 || !slices.Contains(after, "b/b.go:44 plain,fancy") {
		t.Errorf("after an edit: %v, want the new use at b/b.go:44 added to %v", after, before)
	}
	if st := pc.Status(); st.Stale != 2 || st.Refreshes != 2 || st.Loads != 2 {
		t.Errorf("after an edit: %+v, want both programs found stale and refreshed, with no new load", st)
	}
	pc.Invalidate("/elsewhere")
	if len(pc.Status().Programs) != 2 {
		t.Error("invalidating another repository dropped this one's programs")
	}
	pc.Invalidate(root)
	if st := pc.Status(); len(st.Programs) != 0 || st.Dropped != 2 || st.Bytes != 0 {
		t.Errorf("after Invalidate: %+v", st)
	}
	var none *ProgramCache
	if st := none.Status(); len(st.Programs) != 0 {
		t.Error("a nil cache holds nothing")
	}
}

func TestProgramCache_isBoundedAndDropsIdlePrograms(t *testing.T) {
	needGo(t)
	root := writeTree(t, tricky)
	pc := NewProgramCache(1, 80*time.Millisecond)
	r := &Runner{Repo: root, Config: trickyConfigs, Engine: EngineTypes, Programs: pc}
	if _, err := r.References(context.Background(), "a.Total"); err != nil {
		t.Fatal(err)
	}
	if st := pc.Status(); len(st.Programs) != 1 || st.Evictions != 1 {
		t.Errorf("two configurations through one slot: %+v, want one held after one eviction", st)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(pc.Status().Programs) > 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if st := pc.Status(); len(st.Programs) != 0 || st.IdleStops < 1 {
		t.Errorf("after the idle timeout: %+v", st)
	}
	pc.Close()
	if res, err := r.References(context.Background(), "a.Total"); err != nil || len(res.Hits) == 0 {
		t.Errorf("a query after Close must still work: %v %v", res, err)
	}
}

// The files an answer depends on are not all inside the repository: a workspace's other modules, and directories a
// go.mod replaces a module with, are read too, and an edit there must invalidate.
func TestSourceRoots_coverTheWorkspaceAndLocalReplaces(t *testing.T) {
	ws := writeTree(t, map[string]string{
		"go.work":        "go 1.22\n\nuse (\n\t./m1\n\t./m2 // sibling\n)\n",
		"m1/go.mod":      "module example.com/m1\n\ngo 1.22\n\nrequire example.com/x v0.0.0\n\nreplace example.com/x => ../x\n",
		"m1/a.go":        "package m1\n\nfunc Hello() {}\n",
		"m2/go.mod":      "module example.com/m2\n\ngo 1.22\n",
		"m2/b.go":        "package m2\n",
		"x/go.mod":       "module example.com/x\n\ngo 1.22\n",
		"x/x.go":         "package x\n",
		"unrelated/u.go": "package u\n",
	})
	repo := filepath.Join(ws, "m1")
	roots := sourceRoots(repo)
	for _, want := range []string{filepath.Join(ws, "m2"), filepath.Join(ws, "x"), filepath.Join(ws, "go.work")} {
		if !slices.Contains(roots, want) {
			t.Errorf("sourceRoots = %v, missing %s", roots, want)
		}
	}
	stats, err := scanTree(repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"m1/a.go", "m1/go.mod", "m2/b.go", "m2/go.mod", "x/x.go", "go.work"} {
		if _, ok := stats[filepath.Join(ws, filepath.FromSlash(want))]; !ok {
			t.Errorf("the scan of m1 does not cover %s", want)
		}
	}
	if _, ok := stats[filepath.Join(ws, "unrelated", "u.go")]; ok {
		t.Error("the scan covers a directory the build does not read")
	}

	// through the answer cache: an edit in the sibling module makes the remembered answer unusable
	settle(t, ws)
	f := &lspFake{}
	r := &Runner{Repo: repo, Config: Config{Configs: []BuildConfig{{Name: "one"}}}, Spawn: f.spawn, Engine: EngineLSP, Cache: NewCache(1 << 20)}
	ctx := context.Background()
	if _, err := r.References(ctx, "Hello"); err != nil {
		t.Fatal(err)
	}
	if again, _ := r.References(ctx, "Hello"); !again.Cached {
		t.Fatal("the repeat was not remembered")
	}
	if err := os.WriteFile(filepath.Join(ws, "m2", "b.go"), []byte("package m2\n\nfunc New() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if after, _ := r.References(ctx, "Hello"); after.Cached {
		t.Error("an edit in a sibling module of the workspace left the remembered answer in use")
	}
}

// programAnswers asks prog about every declaration of want's listing (references, and implementations for types and
// methods) and returns the lines of each answer, by question.
func programAnswers(prog, lister *program, root string) map[string]string {
	out := map[string]string{}
	for _, d := range diffDecls(lister, root) {
		rel, _ := filepath.Rel(root, d.file)
		for _, kind := range []reqKind{askReferences, askImplementations} {
			if kind == askImplementations && d.kind != "type" && d.kind != "method" && d.kind != "interface-method" {
				continue
			}
			var hits []token.Pos
			var err error
			if kind == askImplementations {
				hits, err = prog.implementations(d.file, d.line, d.col)
			} else {
				hits, err = prog.references(d.file, d.line, d.col, false)
			}
			var lines []string
			for _, ln := range strings.Split(string(prog.positionsText(hits)), "\n") {
				if r, ok := diffRel(root, ln); ok {
					lines = append(lines, r)
				}
			}
			sort.Strings(lines)
			lines = slices.Compact(lines)
			out[fmt.Sprintf("%d %s:%d:%d %s", kind, filepath.ToSlash(rel), d.line, d.col, d.name)] = fmt.Sprintf("%v err=%v", lines, err != nil)
		}
	}
	return out
}

// A refreshed program must answer exactly as a program loaded afresh from the same tree. A chain of edits is applied
// to the tricky module; after each, the program refreshed from the previous one is compared with a fresh load on
// every declaration. The chain also has the edits that must not be refreshed (the go command's list could differ),
// and checks that they are refused.
func TestRefresh_equalsAFreshLoad(t *testing.T) {
	needGo(t)
	root := writeTree(t, tricky)
	ctx := context.Background()
	r := &Runner{Repo: root, Config: trickyConfigs}
	edit := func(name string, change func(string) string) string {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(name))
		src, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		next := change(string(src))
		if next == string(src) {
			t.Fatalf("the edit to %s changed nothing", name)
		}
		if err := os.WriteFile(p, []byte(next), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	replace := func(old, new string) func(string) string {
		return func(s string) string { return strings.Replace(s, old, new, 1) }
	}
	steps := []struct {
		what    string
		file    string
		change  func(string) string
		refresh bool // whether a refresh must be possible (under the fancy configuration, which compiles every file)
		leaf    bool // whether only a few packages should be checked again
	}{
		{"a statement in a function body, moving every line below it", "a/a.go",
			replace("func use() (string, error) {\n", "func use() (string, error) {\n\t_ = Total(nil)\n\t_ = Describe(Square{})\n"), true, false},
		{"a new function in an importer", "b/b.go",
			func(s string) string {
				return s + "\nfunc Extra() string { return alias.Describe(Circle{R: 2}) + Circle{}.Name() }\n"
			}, true, false},
		{"a method renamed, so a type stops implementing an interface", "b/b.go",
			replace("func (c Circle) Name() string  { return \"circle\" }", "func (c Circle) Label() string { return \"circle\" }"), false, false},
		{"a test body", "a/a_test.go",
			replace("\t_ = Fail(&Err{})\n", "\t_ = Fail(&Err{})\n\t_ = Describe(testShape{}) + (&Err{}).Error()\n"), true, false},
		{"a generic function's body, in a leaf package", "c/c.go",
			replace("func usePair() string { return First(Pair[string, int]{Key: \"k\", Val: 1}) }",
				"func usePair() string {\n\tp := Pair[string, int]{Key: \"k\", Val: 2}\n\t_ = p.Val\n\treturn First(p)\n}"), true, true},
		{"a file behind a build tag", "b/b_fancy.go",
			replace("func Fancy() float64 {", "func Fancy2() float64 { return Star{Points: 3}.Area() }\n\nfunc Fancy() float64 {"), true, false},
		{"a field added to a struct that other packages embed", "a/a.go",
			replace("type Cube struct {\n\tSquare\n\tDepth float64\n}", "type Cube struct {\n\tSquare\n\tDepth float64\n\tTint  string\n}"), true, false},
		// These change what the go command would list, or which packages are searched: a refresh must refuse them.
		{"a new import", "c/c.go", replace("import (\n", "import (\n\t\"strings\"\n"), false, false},
		{"use of the new import", "c/c.go", func(s string) string { return s + "\nvar _ = strings.ToUpper\n" }, true, true},
		{"a first method in a test file", "c/c_test.go", func(s string) string { return s + "\nfunc (b Box[T]) Peek() T { return b.Item }\n" }, false, false},
		{"a build constraint", "b/b_fancy.go", replace("//go:build fancy", "//go:build fancy && !never"), false, false},
	}
	// "a method renamed" is refreshable in principle (no import, clause or constraint changes): let it be, and compare.
	steps[2].refresh = true

	for _, bc := range trickyConfigs.Configs {
		t.Run(bc.Name, func(t *testing.T) {
			// each configuration walks the chain on its own copy of the module
			root = writeTree(t, tricky)
			r = &Runner{Repo: root, Config: trickyConfigs}
			prog, err := r.load(ctx, bc, root)
			if err != nil {
				t.Fatal(err)
			}
			for _, st := range steps {
				file := edit(st.file, st.change)
				compiled := len(prog.byFile[file]) > 0
				np, ok := prog.refresh([]string{file}, false)
				fresh, err := r.loadWith(ctx, bc, root, true)
				if err != nil {
					t.Fatalf("%s: fresh load: %v", st.what, err)
				}
				if compiled && ok != st.refresh {
					t.Errorf("%s: refresh possible = %v, want %v", st.what, ok, st.refresh)
				}
				if !ok {
					prog, err = r.load(ctx, bc, root) // what the cache does when a refresh is refused
					if err != nil {
						t.Fatal(err)
					}
					continue
				}
				if st.leaf && compiled && (np.refreshed == 0 || np.refreshed > 4) {
					t.Errorf("%s: %d packages were checked again, want only the leaf package's own variants", st.what, np.refreshed)
				}
				got, want := programAnswers(np, fresh, root), programAnswers(fresh, fresh, root)
				if len(want) < 100 {
					t.Fatalf("%s: only %d questions", st.what, len(want))
				}
				bad := 0
				for q, w := range want {
					if got[q] != w {
						if bad++; bad <= 5 {
							t.Errorf("after %q, %s:\n refreshed %s\n fresh     %s", st.what, q, got[q], w)
						}
					}
				}
				if bad > 0 {
					t.Fatalf("after %q: %d of %d answers differ between the refreshed program and a fresh load", st.what, bad, len(want))
				}
				prog = np
			}
		})
	}
}

// The type-check runs in this process, where no wrapped subprocess covers it: the repository's wrap lock is taken for
// it through the helper, once per configuration loaded, after that configuration's go list has run behind the wrap
// command itself.
func TestTypesEngine_holdsTheWrapLockWhileItTypeChecks(t *testing.T) {
	needGo(t)
	if _, err := exec.LookPath("env"); err != nil {
		t.Skip("no env command to stand in for a wrap command")
	}
	root := writeTree(t, tricky)
	cfg := trickyConfigs
	cfg.Wrap = []string{"env"} // a wrapper that changes nothing
	var mu sync.Mutex
	var holders [][]string
	r := &Runner{Repo: root, Config: cfg, Engine: EngineTypes, Spawn: func(ctx context.Context, dir string, env, argv []string) (Proc, error) {
		mu.Lock()
		holders = append(holders, append([]string{}, argv...))
		mu.Unlock()
		return spawnExec(ctx, dir, env, argv)
	}}
	res, err := r.References(context.Background(), "a.Total")
	if err != nil || res.Incomplete || len(res.Hits) != 4 {
		t.Fatalf("hits %v incomplete %v err %v", hitLines(res), res.Incomplete, err)
	}
	if len(holders) != len(cfg.Configs) {
		t.Fatalf("the lock was held %d times, want once per configuration (%d): %v", len(holders), len(cfg.Configs), holders)
	}
	for _, argv := range holders {
		if argv[0] != "env" || !slices.Contains(argv, lockHolderScript) {
			t.Errorf("lock holder argv %v, want the wrap prefix around the helper", argv)
		}
	}
}

// A cache that sizes itself grows to the configurations it is used for and no further than its ceiling; a cache given
// a size keeps it.
func TestProgramCache_sizesItselfToTheConfigurations(t *testing.T) {
	c := NewAutoProgramCache(time.Minute)
	defer c.Close()
	if got := c.Status().MaxPrograms; got != 1 {
		t.Errorf("starts with room for %d, want 1", got)
	}
	c.Fit(3)
	c.Fit(2)
	if got := c.Status().MaxPrograms; got != 3 {
		t.Errorf("after Fit(3), Fit(2): %d, want 3", got)
	}
	c.Fit(100)
	if got := c.Status().MaxPrograms; got != AutoProgramCeiling {
		t.Errorf("after Fit(100): %d, want the ceiling %d", got, AutoProgramCeiling)
	}
	fixed := NewProgramCache(2, time.Minute)
	defer fixed.Close()
	fixed.Fit(5)
	if got := fixed.Status().MaxPrograms; got != 2 {
		t.Errorf("a cache given a size grew to %d", got)
	}
	var none *ProgramCache
	none.Fit(3) // nothing to do, and nothing to crash on
}

// Where this engine returns more than gopls, it is for one stated reason and nothing else: on every declaration of
// the tricky module the default answer holds gopls's exact answer, and the declarations with extra lines are the
// fields of generic types, the extra lines their uses through an instantiation in another package.
func TestTypesEngine_goesBeyondGoplsOnlyForGenericFields(t *testing.T) {
	needGo(t)
	root := writeTree(t, tricky)
	r := &Runner{Repo: root, Config: trickyConfigs}
	prog, err := r.loadWith(context.Background(), trickyConfigs.Configs[0], root, true)
	if err != nil {
		t.Fatal(err)
	}
	lines := func(hits []token.Pos) []string {
		var out []string
		for _, ln := range strings.Split(string(prog.positionsText(hits)), "\n") {
			if rel, ok := diffRel(root, ln); ok {
				out = append(out, rel)
			}
		}
		sort.Strings(out)
		return slices.Compact(out)
	}
	extra := map[string][]string{}
	for _, d := range diffDecls(prog, root) {
		exact, err1 := prog.references(d.file, d.line, d.col, true)
		more, err2 := prog.references(d.file, d.line, d.col, false)
		if err1 != nil || err2 != nil {
			t.Fatalf("%s: %v %v", d.name, err1, err2)
		}
		e, m := lines(exact), lines(more)
		for _, l := range e {
			if !slices.Contains(m, l) {
				t.Errorf("%s (%s): the default answer lacks %s, which gopls's has", d.name, d.kind, l)
			}
		}
		for _, l := range m {
			if !slices.Contains(e, l) {
				extra[d.kind+" "+d.name] = append(extra[d.kind+" "+d.name], l)
			}
		}
	}
	want := map[string][]string{
		"field Item": {"d/d.go:10", "d/d.go:8"},
		"field Key":  {"d/d.go:10", "d/d.go:9"},
		"field Val":  {"d/d.go:10", "d/d.go:9"},
	}
	for k, v := range extra {
		sort.Strings(v)
		if !slices.Equal(v, want[k]) {
			t.Errorf("beyond gopls for %s: %v, want %v", k, v, want[k])
		}
	}
	for k := range want {
		if extra[k] == nil {
			t.Errorf("no lines beyond gopls for %s: the uses in package d were not found", k)
		}
	}
}
