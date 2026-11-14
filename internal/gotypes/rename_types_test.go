package gotypes

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

// The in-process rename against gopls's, on every declaration of the tricky module under both configurations, with
// every patch applied in memory and checked.
func TestTypesRename_matchesGoplsOnTheTrickyModule(t *testing.T) {
	needGo(t)
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not on PATH")
	}
	root := writeTree(t, tricky)
	res := runRenameDifferential(t, root, trickyConfigs, renameDiffOptions{step: 1, verifyEvery: 1, both: true, progress: testing.Verbose()})
	if res.disagree > 0 {
		t.Errorf("the narrow check and the full check disagreed on %d patches", res.disagree)
	}
	if res.asked < 100 {
		t.Errorf("only %d renames were compared: the fixture's declarations were not found", res.asked)
	}
	for _, n := range res.notes {
		t.Log(n)
	}
	if n := res.class["different"] + res.class["gopls edits more"] + res.class["same, check refuses"]; n > 0 {
		t.Errorf("%d of %d renames differ from gopls's in a way that is not accounted for: %v", n, res.asked, res.class)
	}
}

// ren is a module for renames: an interface with implementations in two packages, an embedded type, doc comments
// and doc links, and names placed so that a careless rename captures or clashes.
var ren = map[string]string{
	"go.mod": "module example.com/ren\n\ngo 1.21\n",
	"p/p.go": `// Package p has a [Limit] and a [Sq.Area].
package p

import "fmt"

// Shape is anything with an Area.
type Shape interface {
	// Area is the surface.
	Area() float64
}

type Sq struct{ Side float64 }

// Area of a Sq.
func (s Sq) Area() float64 { return s.Side * s.Side }

// Limit is the largest Side: see [Limit].
const Limit = 10

var count, total int

func Add(n int) int {
	count += n
	return count + total
}

type Inner struct{ ID int }

type Outer struct {
	Inner
	Name string
}

func (o Outer) Label() string { return fmt.Sprint(o.ID, o.Name, o.Inner.ID) }

type helper struct{}

// Area of a helper, which is never used as a Shape.
func (helper) Area() float64 { return 0 }

var _ Shape = Sq{}

type Namer interface{ Name() string }

type Full interface {
	Name() string
	Rank() int
}

// Promote reports whether n is also a Full.
func Promote(n Namer) bool {
	_, ok := n.(Full)
	return ok
}

// Titled has Namer's method and one of the same signature. Nothing calls either through it.
type Titled interface {
	Namer
	Caption() string
}

// Tagged has Inner's ID and a field of its own. Nothing selects either through it.
type Tagged struct {
	Inner
	Tag int
}

func setup() {}

// Shadow has a field of its own with the name of one it embeds, and a method of its own with the name of one it
// embeds. Nothing selects either through it.
type Shadow struct {
	Inner
	ID string
}

func (Inner) Kind() string  { return "inner" }
func (Shadow) Kind() string { return "shadow" }

// Core2 is another name for Inner, and Holder embeds it under that name.
type Core2 = Inner

type Holder struct{ Core2 }

func (h Holder) Get() int { return h.Core2.ID }
`,
	"q/q.go": `package q

import (
	"os"

	"example.com/ren/p"
)

type Circle struct{ R float64 }

func (c Circle) Area() float64 { return c.R }

func Sum() float64 {
	var s p.Shape = Circle{1}
	return s.Area() + p.Sq{Side: p.Limit}.Area()
}

type Wrap struct{ p.Inner }

func (w Wrap) Get() int { return w.Inner.ID + w.ID }

// Closer is satisfied here by a type the workspace does not own.
type Closer interface{ Close() error }

func Shut(f *os.File) Closer { return f }
`,
	"r/r.go": `package r

import "example.com/ren/p"

// Sized is another interface p.Sq satisfies, in a package p cannot see.
type Sized interface{ Area() float64 }

var _ Sized = p.Sq{}

// Weigher is only ever a constraint. No Heavy is assigned to it, or even passed to anything: Heavy is what Zero is
// instantiated with.
type Weigher interface{ Weight() int }

type Heavy struct{}

func (Heavy) Weight() int { return 9 }

func Zero[T Weigher]() int {
	var z T
	return z.Weight()
}

var _ = Zero[Heavy]()
`,
}

var renConfigs = Config{Configs: []BuildConfig{{Name: "host", Env: map[string]string{"GOWORK": "off", "GOFLAGS": "-mod=mod"}}}}

// renamed returns the lines a plan's patch adds, as "file: text".
func renamed(p *RenamePlan) []string {
	var out []string
	file := ""
	for _, ln := range strings.Split(p.Patch, "\n") {
		switch {
		case strings.HasPrefix(ln, "+++ b/"):
			file = ln[len("+++ b/"):]
		case strings.HasPrefix(ln, "+"):
			out = append(out, file+": "+strings.TrimSpace(ln[1:]))
		}
	}
	return out
}

func TestTypesRename_plansAndChecks(t *testing.T) {
	needGo(t)
	root := writeTree(t, ren)
	// no gopls anywhere: the plan is computed and checked here
	r := &Runner{Repo: root, Config: renConfigs, Gopls: "/nonexistent/gopls", Timeout: 2 * time.Minute}
	ctx := context.Background()
	for _, c := range []struct {
		symbol, to string
		want       []string // the lines the patch adds, when the rename is safe
		conflict   string   // a piece of the conflict, when it is not
	}{
		// An interface method takes with it the methods of the types assigned to the interface, in every package, and
		// leaves a method of the same name that is not tied to it.
		{symbol: "Shape.Area", to: "Surface", want: []string{
			"p/p.go: // Surface is the surface.",
			"p/p.go: Surface() float64",
			"p/p.go: // Surface of a Sq.",
			"p/p.go: func (s Sq) Surface() float64 { return s.Side * s.Side }",
			"p/p.go: // Package p has a [Limit] and a [Sq.Surface].",
			"q/q.go: func (c Circle) Surface() float64 { return c.R }",
			"q/q.go: return s.Surface() + p.Sq{Side: p.Limit}.Surface()",
			"r/r.go: type Sized interface{ Surface() float64 }",
		}},
		// The same rename started from the other interface, in a package that p cannot see: it reaches p.Sq.Area,
		// which p sees as a concrete method required by its own Shape; the intention to rename what is tied holds
		// there too, and the result is the same patch.
		{symbol: "Sized.Area", to: "Surface", want: []string{
			"p/p.go: // Surface is the surface.",
			"p/p.go: Surface() float64",
			"p/p.go: // Surface of a Sq.",
			"p/p.go: func (s Sq) Surface() float64 { return s.Side * s.Side }",
			"p/p.go: // Package p has a [Limit] and a [Sq.Surface].",
			"q/q.go: func (c Circle) Surface() float64 { return c.R }",
			"q/q.go: return s.Surface() + p.Sq{Side: p.Limit}.Surface()",
			"r/r.go: type Sized interface{ Surface() float64 }",
		}},
		// Starting from the concrete method, it is refused: the interface method is the one to rename.
		{symbol: "Sq.Area", to: "Surface", conflict: "no longer assignable to interface Shape"},
		// A method tied to nothing is just renamed.
		{symbol: "helper.Area", to: "Surface", want: []string{
			"p/p.go: // Surface of a helper, which is never used as a Shape.",
			"p/p.go: func (helper) Surface() float64 { return 0 }",
		}},
		// A type and the embedded fields named after it, in both packages.
		{symbol: "Inner", to: "Core", want: []string{
			"p/p.go: type Core struct{ ID int }",
			"p/p.go: Core", // in Outer
			"p/p.go: Core", // in Tagged
			"p/p.go: Core", // in Shadow
			`p/p.go: func (Core) Kind() string  { return "inner" }`,
			"p/p.go: type Core2 = Core",
			"p/p.go: func (o Outer) Label() string { return fmt.Sprint(o.ID, o.Name, o.Core.ID) }",
			"q/q.go: type Wrap struct{ p.Core }",
			"q/q.go: func (w Wrap) Get() int { return w.Core.ID + w.ID }",
		}},
		// The doc comment and the doc links follow.
		{symbol: "Limit", to: "Max", want: []string{
			"p/p.go: // Package p has a [Max] and a [Sq.Area].",
			"p/p.go: // Max is the largest Side: see [Max].",
			"p/p.go: const Max = 10",
			"q/q.go: return s.Area() + p.Sq{Side: p.Max}.Area()",
		}},
		// A name already taken in the same block.
		{symbol: "count", to: "total", conflict: "would no longer type-check"},
		// A name that a parameter has: the uses inside Add would be captured by it.
		{symbol: "count", to: "n", conflict: "would refer to n (declared at p/p.go:22) instead of count"},
		// A field named like a promoted one: o.ID would stop meaning Inner.ID.
		{symbol: "Outer.Name", to: "ID", conflict: "would refer to ID (declared at p/p.go:31) instead of ID (declared at p/p.go:27)"},
		// Unexporting a name another package uses.
		{symbol: "Limit", to: "limit", conflict: "would no longer type-check"},
		// A name of the universe: every use of the builtin type in the package would be captured.
		{symbol: "count", to: "float64", conflict: "would refer to float64 (declared at p/p.go:20) instead of float64 (built in)"},
		// An interface method tied to another interface's only by a type assertion: both, or the assertion could
		// never succeed again (and nothing would fail to compile).
		{symbol: "Full.Name", to: "Title", want: []string{
			"p/p.go: type Namer interface{ Title() string }",
			"p/p.go: Title() string",
		}},
		// Names that clash with nothing a compiler or an identifier can show, because nothing uses the member that
		// would be lost: an interface method merged into one it embeds, a promoted field hidden by a new one.
		{symbol: "Titled.Caption", to: "Name", conflict: "this type would lose Name (declared at p/p.go:43)"},
		{symbol: "Tagged.Tag", to: "ID", conflict: "this type would lose ID (declared at p/p.go:27)"},
		// An interface that is only a type constraint: the type a generic function is instantiated with is tied to
		// it by the instantiation, as an assignment would tie it, and its method goes too. (gopls ties a value
		// passed to a generic function, and not this: it leaves Heavy's method, and Zero[Heavy] no longer compiles.)
		{symbol: "Weigher.Weight", to: "Mass", want: []string{
			"r/r.go: type Weigher interface{ Mass() int }",
			"r/r.go: func (Heavy) Mass() int { return 9 }",
			"r/r.go: return z.Mass()",
		}},
		{symbol: "Heavy.Weight", to: "Mass", conflict: "no longer assignable to interface Weigher"},
		// A field that hides an embedded one of the same name: renamed, the embedded one is reachable again. Nothing
		// written changes its meaning, so that is allowed. (Tagged.Tag -> ID above is the opposite, and is not.)
		{symbol: "Shadow.ID", to: "Label", want: []string{"p/p.go: Label string"}},
		// A method that hides an embedded one of the same name: renamed, the embedded one would answer wherever an
		// interface asks for Kind, with nothing failing to compile.
		{symbol: "Shadow.Kind", to: "Sort", conflict: "this type's Kind would become Kind (declared at p/p.go:77)"},
		// The other way about is only an addition: the embedded type's method, renamed, shows through under a name
		// nothing asks for yet.
		{symbol: "Inner.Kind", to: "Sort", want: []string{`p/p.go: func (Inner) Sort() string  { return "inner" }`}},
		// A function the runtime would start calling.
		{symbol: "setup", to: "init", conflict: "run by the Go runtime"},
		// An alias, and the field embedded under its name; the type it stands for keeps its own.
		{symbol: "Core2", to: "Kernel", want: []string{
			"p/p.go: // Kernel is another name for Inner, and Holder embeds it under that name.",
			"p/p.go: type Kernel = Inner",
			"p/p.go: type Holder struct{ Kernel }",
			"p/p.go: func (h Holder) Get() int { return h.Kernel.ID }",
		}},
		// An interface method tied to a method of the standard library's, which cannot be renamed.
		{symbol: "Closer.Close", to: "Shutdown", conflict: "would also have to rename func (*os.File).Close() error, declared outside the workspace"},
		// A field: the composite literal's key in the other package too.
		{symbol: "Sq.Side", to: "Side2", want: []string{
			"p/p.go: type Sq struct{ Side2 float64 }",
			"p/p.go: func (s Sq) Area() float64 { return s.Side2 * s.Side2 }",
			"q/q.go: return s.Area() + p.Sq{Side2: p.Limit}.Area()",
		}},
	} {
		plan, err := r.RenamePlan(ctx, c.symbol, c.to)
		if err != nil {
			t.Errorf("%s -> %s: %v", c.symbol, c.to, err)
			continue
		}
		if c.conflict != "" {
			if plan.Verified || plan.Patch != "" || !plan.Incomplete || !strings.Contains(strings.Join(plan.Conflicts, "\n"), c.conflict) {
				t.Errorf("%s -> %s: want it refused with %q and no patch; got verified=%v incomplete=%v conflicts=%q patch=%q",
					c.symbol, c.to, c.conflict, plan.Verified, plan.Incomplete, plan.Conflicts, plan.Patch)
			}
			continue
		}
		got := renamed(plan)
		sort.Strings(got)
		want := append([]string{}, c.want...)
		sort.Strings(want)
		if !plan.Verified || plan.Incomplete || len(plan.Conflicts) > 0 || strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("%s -> %s: verified=%v incomplete=%v conflicts=%q runs=%+v\n got:\n  %s\nwant:\n  %s", c.symbol, c.to, plan.Verified, plan.Incomplete,
				plan.Conflicts, plan.Runs, strings.Join(got, "\n  "), strings.Join(want, "\n  "))
		}
	}
	// nothing was written
	if src, _ := os.ReadFile(filepath.Join(root, "p/p.go")); string(src) != ren["p/p.go"] {
		t.Error("RenamePlan wrote to the repository")
	}
}

// The check itself, on patches made by hand: each is a rename done wrong, and compiles or not as said.
func TestRenameCheck_findsWhatAWrongPatchChanges(t *testing.T) {
	needGo(t)
	root := writeTree(t, ren)
	r := &Runner{Repo: root, Config: renConfigs}
	ctx := context.Background()
	l, err := r.listWorkspace(ctx, renConfigs.Configs[0], root)
	if err != nil {
		t.Fatal(err)
	}
	prog, err := l.build(false)
	if err != nil {
		t.Fatal(err)
	}
	pfile := filepath.Join(root, "p", "p.go")
	for _, c := range []struct {
		name     string
		old, new string // one replacement in p/p.go
		want     string // a piece of what the check must find; "" when it must find nothing
	}{
		// Only one of two interfaces tied by an assertion: n.(Full) still compiles and can never succeed.
		{"half an assertion", "type Full interface {\n\tName() string", "type Full interface {\n\tTitle() string", "this type assertion could no longer succeed"},
		// A comment changed, and nothing else: no difference.
		{"a comment", "// Promote reports", "// Promoted reports", ""},
		// A token that is not an identifier.
		{"a literal", "const Limit = 10", "const Limit = 11", "changes more than identifiers and comments"},
		// The declaration renamed and a use left behind.
		{"a use left behind", "var count, total int", "var tally, total int", "would no longer type-check: undefined: count"},
	} {
		src := strings.ReplaceAll(strings.ReplaceAll(c.old, "\\n", "\n"), "\\t", "\t")
		dst := strings.ReplaceAll(strings.ReplaceAll(c.new, "\\n", "\n"), "\\t", "\t")
		if !strings.Contains(ren["p/p.go"], src) {
			t.Fatalf("%s: the fixture has no %q", c.name, src)
		}
		patched := []byte(strings.Replace(ren["p/p.go"], src, dst, 1))
		var found []string
		if tm, ok := newTokenMap([]byte(ren["p/p.go"]), patched); !ok {
			found = []string{"the patch changes more than identifiers and comments"}
		} else {
			patch := map[string][]byte{pfile: patched}
			diffs, err := prog.checkPatch(patch, map[string]*tokenMap{pfile: tm}, patchEnv{hold: func() (func(), error) { return func() {}, nil },
				reload: func(opts buildOpts) (*program, error) { return l.buildWith(opts) }})
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range diffs {
				found = append(found, d.msg)
			}
		}
		got := strings.Join(found, "\n")
		if (c.want == "") != (len(found) == 0) || !strings.Contains(got, c.want) {
			t.Errorf("%s: the check found %q, want %q", c.name, found, c.want)
		}
	}
}

// A rename under several configurations: each contributes the files only it compiles, and the merged patch is
// checked under every one of them.
func TestTypesRename_mergesConfigurationsAndChecksThePatchUnderEach(t *testing.T) {
	needGo(t)
	root := writeTree(t, tagged)
	r := &Runner{Repo: root, Config: taggedConfigs, Gopls: "/nonexistent/gopls", Timeout: 2 * time.Minute}
	ctx := context.Background()
	plan, err := r.RenamePlan(ctx, "Shape", "Form")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range plan.Files {
		got = append(got, fmt.Sprintf("%s %v %v", f.File, f.Lines, f.Configs))
	}
	want := []string{
		"shape/circle_fancy.go [10 12] [linux-fancy]",
		"shape/shape.go [3 4 8] [linux linux-fancy darwin]", // line 3 is the doc comment
		"shape/square.go [7] [linux linux-fancy darwin]",
	}
	if !slices.Equal(got, want) || !plan.Verified || plan.Incomplete || len(plan.Conflicts) != 0 {
		t.Fatalf("files %v verified %v incomplete %v conflicts %v\nwant %v, verified\n%s", got, plan.Verified, plan.Incomplete, plan.Conflicts, want, plan.Text(3, 0))
	}
	if text := plan.Text(3, 0); !strings.Contains(text, "checked: the patch was applied in memory") {
		t.Errorf("the summary does not say the patch was checked:\n%s", text)
	}

	// A name only one configuration's file already has: the clash is found under that configuration, and the plan
	// is refused for all of them, since there is one patch.
	plan, err = r.RenamePlan(ctx, "UnitSquare", "UnitCircle")
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Refused || plan.Patch != "" || plan.Verified || len(plan.Conflicts) == 0 || !strings.Contains(plan.Conflicts[0], "[linux-fancy]") {
		t.Errorf("UnitSquare -> UnitCircle: want it refused under linux-fancy alone, got refused=%v verified=%v conflicts=%q", plan.Refused, plan.Verified, plan.Conflicts)
	}
	if text := plan.Text(3, 0); !strings.Contains(text, "NOT SAFE as computed") {
		t.Errorf("the summary does not say the rename is not safe:\n%s", text)
	}

	// A twin that no listed configuration compiles: the plan is a lower bound, and so is not called checked.
	plan, err = r.RenamePlan(ctx, "Platform", "HostOS")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Verified || !plan.Incomplete || plan.Refused || plan.Patch == "" {
		t.Errorf("Platform -> HostOS: want an incomplete plan with a patch, not checked; got verified=%v incomplete=%v refused=%v", plan.Verified, plan.Incomplete, plan.Refused)
	}
}

// The same comparison with each declaration renamed to its neighbour's name: where gopls's rules refuse and where
// the check of the applied patch does. They are different tests of safety and are not held equal; what must hold is
// that this engine never hands out a patch for a rename gopls refuses unless the patch passes the check (which is
// then run inside the comparison), and that it runs to the end.
func TestTypesRename_refusesClashesOnTheTrickyModule(t *testing.T) {
	needGo(t)
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not on PATH")
	}
	root := writeTree(t, tricky)
	res := runRenameDifferential(t, root, trickyConfigs, renameDiffOptions{step: 1, collide: true, both: true, progress: testing.Verbose()})
	if res.disagree > 0 {
		t.Errorf("the narrow check and the full check disagreed on %d patches", res.disagree)
	}
	for _, n := range res.notes {
		t.Log(n)
	}
	t.Logf("renamed to a neighbour's name: %v", res.class)
	if res.class["both refuse"] == 0 || res.asked < 100 {
		t.Errorf("no clash was refused by both in %d renames: %v", res.asked, res.class)
	}
}

// The rename module against gopls: it holds the cases where this engine departs from gopls on purpose (an embedded
// alias, a method tied to one outside the workspace, an interface whose rename reaches another interface through a
// package that cannot see the first, a constraint and the type a generic function is instantiated with), and the
// comparison must put each in its class.
func TestTypesRename_matchesGoplsOnTheRenameModule(t *testing.T) {
	needGo(t)
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not on PATH")
	}
	root := writeTree(t, ren)
	res := runRenameDifferential(t, root, renConfigs, renameDiffOptions{step: 1, verifyEvery: 1, both: true, progress: testing.Verbose()})
	if res.disagree > 0 {
		t.Errorf("the narrow check and the full check disagreed on %d patches", res.disagree)
	}
	for _, n := range res.notes {
		t.Log(n)
	}
	t.Logf("%d renames: %v", res.asked, res.class)
	if n := res.class["different"] + res.class["gopls edits more"]; n > 0 {
		t.Errorf("%d of %d renames differ from gopls's in a way that is not accounted for: %v", n, res.asked, res.class)
	}
	// (the one patch the two agree on and the check refuses is Shadow.Kind: see TestTypesRename_plansAndChecks)
	if res.class["gopls broken"] == 0 || res.class["ours refuses outside"] == 0 || res.class["ours edits more"] < 2 ||
		res.class["ours refuses, gopls broken"] == 0 ||
		res.class["same, check refuses"] != 1 || res.class["same"] < 15 {
		t.Errorf("the module's cases did not land where they should: %v", res.class)
	}
}

// The same clashes on the rename module, each patch checked twice: the way a plan checks it (only the function
// bodies and packages it can reach) and in full. The two must agree on every one.
func TestTypesRename_theNarrowCheckAgreesWithTheFullOne(t *testing.T) {
	needGo(t)
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not on PATH")
	}
	root := writeTree(t, ren)
	res := runRenameDifferential(t, root, renConfigs, renameDiffOptions{step: 1, collide: true, both: true, progress: testing.Verbose()})
	for _, n := range res.notes {
		t.Log(n)
	}
	t.Logf("%d renames to a neighbour's name: %v", res.asked, res.class)
	if res.disagree > 0 || res.class["both refuse"] == 0 || res.class["both rename"] == 0 {
		t.Errorf("the narrow and the full check disagreed on %d patches (classes %v)", res.disagree, res.class)
	}
}

// A test file that declares a name the universe scope has: the non-test code that spells it means the builtin when
// the package is built and the test's declaration when it is tested. Renaming that declaration edits the non-test
// file, which then names something that exists only under test. The plain package is checked for it.
func TestTypesRename_refusesWhatOnlyTheTestBuildResolves(t *testing.T) {
	needGo(t)
	root := writeTree(t, map[string]string{
		"go.mod":        "module example.com/shadow\n\ngo 1.21\n",
		"p/p.go":        "package p\n\nfunc Small(a, b int) int { return min(a, b) }\n",
		"p/min_test.go": "package p\n\nfunc min(a, b int) int {\n\tif a < b {\n\t\treturn a\n\t}\n\treturn b\n}\n",
	})
	r := &Runner{Repo: root, Config: renConfigs, Gopls: "/nonexistent/gopls", Timeout: 2 * time.Minute}
	plan, err := r.RenamePlan(context.Background(), "min", "least")
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Refused || plan.Patch != "" || !strings.Contains(strings.Join(plan.Conflicts, "\n"), "p/p.go:3: would no longer type-check: undefined: least") {
		t.Errorf("min -> least: want it refused because p/p.go would name something only the test build has; got refused=%v conflicts=%q\n%s",
			plan.Refused, plan.Conflicts, plan.Text(1, 0))
	}
}

// Emptying function bodies before parsing leaves what stripping them after parsing leaves.
func TestBlankBodies_matchesStripBodies(t *testing.T) {
	names := [][]byte{[]byte("Area"), []byte("Sq")}
	for file, src := range ren {
		if !strings.HasSuffix(file, ".go") {
			continue
		}
		parse := func(src []byte) *ast.File {
			f, err := parser.ParseFile(token.NewFileSet(), file, src, parser.SkipObjectResolution)
			if err != nil {
				t.Fatalf("%s: %v", file, err)
			}
			return f
		}
		shape := func(f *ast.File) string { // each function, and whether it kept its body
			var b strings.Builder
			for _, d := range f.Decls {
				if fd, ok := d.(*ast.FuncDecl); ok {
					fmt.Fprintf(&b, "%s:%v ", fd.Name.Name, fd.Body != nil)
				}
			}
			return b.String()
		}
		after := parse([]byte(src))
		extents := bodyExtents(after)
		stripBodies(after, []byte(src), names)

		blankedSrc, blanked := blankBodies([]byte(src), extents, names)
		if len(blankedSrc) != len(src) || strings.Count(string(blankedSrc), "\n") != strings.Count(src, "\n") {
			t.Fatalf("%s: blanking changed the size or the lines of the source", file)
		}
		before := parse(blankedSrc)
		base := int(before.FileStart)
		for _, d := range before.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Body != nil && blanked[int(fd.Body.Lbrace)-base] {
				fd.Body = nil
			}
		}
		if got, want := shape(before), shape(after); got != want || !strings.Contains(want, "false") && file == "p/p.go" {
			t.Errorf("%s: blanked before parsing %q, stripped after %q", file, got, want)
		}
	}
	// extents that are not of this source: nothing is blanked
	if out, blanked := blankBodies([]byte("package p\n\nfunc F() {}\n"), []int32{3, 9}, names); blanked != nil || string(out) != "package p\n\nfunc F() {}\n" {
		t.Errorf("blankBodies trusted extents that do not point at braces")
	}
}
