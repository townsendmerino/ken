package gotypes

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// writeTree writes files (path -> content) under a new temporary directory and returns it.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// macOS's temporary directory is a symlink; gopls reports resolved paths.
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	return root
}

// tagged is a module where half the code is behind a build tag and one function has per-platform twins.
var tagged = map[string]string{
	"go.mod": "module example.com/tagged\n\ngo 1.21\n",
	"shape/shape.go": `package shape

// Shape is anything with an area.
type Shape interface {
	Area() float64
}

func Total(ss []Shape) float64 {
	t := 0.0
	for _, s := range ss {
		t += s.Area()
	}
	return t
}
`,
	"shape/square.go": `package shape

type Square struct{ Side float64 }

func (s Square) Area() float64 { return s.Side * s.Side }

func UnitSquare() Shape { return Square{Side: 1} }
`,
	"shape/circle_fancy.go": `//go:build fancy

package shape

// Circle exists only under the fancy tag. See Shape.
type Circle struct{ R float64 }

func (c Circle) Area() float64 { return 3 * c.R * c.R }

func UnitCircle() Shape { return Circle{R: 1} }

func fancyTotal() float64 { return Total([]Shape{UnitCircle()}) + Circle{R: 2}.Area() }
`,
	"shape/name_linux.go":   "package shape\n\nfunc Platform() string { return \"linux\" }\n",
	"shape/name_darwin.go":  "package shape\n\nfunc Platform() string { return \"darwin\" }\n",
	"shape/name_windows.go": "package shape\n\nfunc Platform() string { return \"windows\" }\n",
	"shape/use.go":          "package shape\n\nfunc Describe() string { return Platform() }\n",
	"other/other.go":        "package other\n\n// Total is unrelated to shape.Total.\nfunc Total() int { return 0 }\n",
	"deep/other/other.go":   "package other\n\nfunc Total() int { return 1 }\n",
	"NOTES.md":              "Shape is the interface; Circle is fancy.\n",
}

var taggedConfigs = Config{Configs: []BuildConfig{
	{Name: "linux", Env: map[string]string{"GOOS": "linux", "GOARCH": "amd64", "CGO_ENABLED": "0"}},
	{Name: "linux-fancy", Env: map[string]string{"GOOS": "linux", "GOARCH": "amd64", "CGO_ENABLED": "0", "GOFLAGS": "-tags=fancy"}},
	{Name: "darwin", Env: map[string]string{"GOOS": "darwin", "GOARCH": "arm64", "CGO_ENABLED": "0"}},
}}

func TestCompiles(t *testing.T) {
	linux := BuildConfig{Env: map[string]string{"GOOS": "linux", "GOARCH": "amd64", "GOFLAGS": "-mod=mod -tags=cuda,hooks"}}
	darwin := BuildConfig{Env: map[string]string{"GOOS": "darwin", "GOARCH": "arm64"}}
	cases := []struct {
		bc   BuildConfig
		base string
		expr string
		want bool
	}{
		{linux, "a.go", "", true},
		{linux, "a.go", "cuda && hooks", true},
		{linux, "a.go", "cuda && !hooks", false},
		{darwin, "a.go", "cuda", false},
		{darwin, "a.go", "darwin || linux", true},
		{linux, "a.go", "unix", true},
		{BuildConfig{Env: map[string]string{"GOOS": "windows"}}, "a.go", "unix", false},
		{linux, "a_darwin.go", "", false},
		{linux, "a_linux_test.go", "", true},
		{linux, "a_linux_arm64.go", "", false},
		{darwin, "a_arm64_test.go", "", true},
		{linux, "linux.go", "", true}, // no prefix before the suffix: not a constraint
		{linux, "a.go", "go1.22", true},
		{linux, "a.go", "((", true}, // unparseable: ask rather than skip
	}
	for _, c := range cases {
		if got := c.bc.Compiles(c.base, c.expr); got != c.want {
			t.Errorf("Compiles(%q, %q) under %v = %v, want %v", c.base, c.expr, c.bc.Env, got, c.want)
		}
	}
}

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	if c, found, err := LoadConfig(dir); err != nil || found || len(c.Configs) != 1 || c.Configs[0].Name != "default" {
		t.Fatalf("missing file: got %+v found=%v err=%v, want the one default configuration", c, found, err)
	}
	write := func(s string) {
		if err := os.WriteFile(filepath.Join(dir, ConfigFile), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"configs":[{"name":"a"},{"name":"b","env":{"GOOS":"linux"}}],"wrap":["lock","--"]}`)
	c, found, err := LoadConfig(dir)
	if err != nil || !found || len(c.Configs) != 2 || c.Configs[1].Env["GOOS"] != "linux" || !slices.Equal(c.Wrap, []string{"lock", "--"}) {
		t.Fatalf("got %+v found=%v err=%v", c, found, err)
	}
	for _, bad := range []string{`{"configs":[{"name":"a"},{"name":"a"}]}`, `{"configs":[{"env":{}}]}`, `{`} {
		write(bad)
		if _, _, err := LoadConfig(dir); err == nil {
			t.Errorf("LoadConfig accepted %s", bad)
		}
	}
}

func TestEnviron_replacesAndKeeps(t *testing.T) {
	bc := BuildConfig{Env: map[string]string{"GOOS": "linux", "GOFLAGS": "-tags=x"}}
	got := bc.environ([]string{"PATH=/bin", "GOOS=darwin", "HOME=/h"})
	want := []string{"PATH=/bin", "HOME=/h", "GOFLAGS=-tags=x", "GOOS=linux"}
	if !slices.Equal(got, want) {
		t.Fatalf("environ = %v, want %v", got, want)
	}
}

func TestFindDecls(t *testing.T) {
	root := writeTree(t, tagged)
	find := func(sym string) []string {
		t.Helper()
		ds, err := FindDecls(root, sym)
		if err != nil {
			t.Fatalf("FindDecls(%q): %v", sym, err)
		}
		var out []string
		for _, d := range ds {
			out = append(out, fmt.Sprintf("%s:%d:%d %s %s [%s]", d.File, d.Line, d.Col, d.Kind, d.QName, d.Constraint))
		}
		return out
	}
	check := func(sym string, want ...string) {
		t.Helper()
		if got := find(sym); !slices.Equal(got, want) {
			t.Errorf("FindDecls(%q) =\n  %s\nwant\n  %s", sym, strings.Join(got, "\n  "), strings.Join(want, "\n  "))
		}
	}
	check("Shape", "shape/shape.go:4:6 type shape.Shape []")
	check("Shape.Area", "shape/shape.go:5:2 interface-method shape.Shape.Area []")
	check("Circle.Area", "shape/circle_fancy.go:8:17 method shape.Circle.Area [fancy]")
	check("shape.Square.Side", "shape/square.go:3:21 field shape.Square.Side []")
	// a name declared once per platform is three declarations of one qualified name
	check("Platform",
		"shape/name_darwin.go:3:6 func shape.Platform []",
		"shape/name_linux.go:3:6 func shape.Platform []",
		"shape/name_windows.go:3:6 func shape.Platform []")
	// a bare name shared by two packages is two things; a package or a directory picks one
	check("Total", "deep/other/other.go:3:6 func other.Total []", "other/other.go:4:6 func other.Total []", "shape/shape.go:8:6 func shape.Total []")
	check("shape.Total", "shape/shape.go:8:6 func shape.Total []")
	// two packages named other: the package name still means both, a directory suffix means one
	check("other.Total", "deep/other/other.go:3:6 func other.Total []", "other/other.go:4:6 func other.Total []")
	check("deep/other.Total", "deep/other/other.go:3:6 func other.Total []")
	check("Area") // a method needs its type
	for _, bad := range []string{"a.b.c.d", "x/y", "9x", "a..b"} {
		if _, err := FindDecls(root, bad); err == nil {
			t.Errorf("FindDecls accepted %q", bad)
		}
	}
}

// fakeGopls answers from a table keyed by "<config>|<subcommand>|<file:line>" and records what it was asked.
type fakeGopls struct {
	root  string
	out   map[string]string
	fail  map[string]string
	asked []string
	argv0 []string
}

func (f *fakeGopls) run(_ context.Context, dir string, env, argv []string) ([]byte, []byte, error) {
	cfg := "default"
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "KENTEST_CONFIG="); ok {
			cfg = v
		}
	}
	i := slices.Index(argv, "gopls")
	f.argv0 = append(f.argv0, argv[0])
	args := argv[i+1:]
	sub := args[0]
	posArg := args[1]
	if sub == "rename" {
		posArg = args[2]
	}
	rel, _ := filepath.Rel(dir, posArg)
	parts := strings.Split(filepath.ToSlash(rel), ":")
	key := cfg + "|" + sub + "|" + parts[0] + ":" + parts[1]
	f.asked = append(f.asked, key)
	if msg, bad := f.fail[key]; bad {
		return nil, []byte(msg), errors.New("exit status 2")
	}
	return []byte(strings.ReplaceAll(f.out[key], "ROOT", f.root)), nil, nil
}

func fakeConfigs(names ...string) Config {
	var c Config
	for _, n := range names {
		env := map[string]string{"KENTEST_CONFIG": n, "GOOS": "linux", "GOARCH": "amd64"}
		if n == "fancy" {
			env["GOFLAGS"] = "-tags=fancy"
		}
		c.Configs = append(c.Configs, BuildConfig{Name: n, Env: env})
	}
	return c
}

func TestReferences_mergesConfigurations(t *testing.T) {
	root := writeTree(t, tagged)
	fg := &fakeGopls{root: root, out: map[string]string{
		"plain|references|shape/shape.go:8": "ROOT/shape/shape.go:11:8-12\n",
		"fancy|references|shape/shape.go:8": "ROOT/shape/shape.go:11:8-12\nROOT/shape/circle_fancy.go:12:37-42\n/somewhere/else/dep.go:3:1-2\n",
	}}
	r := &Runner{Repo: root, Config: fakeConfigs("plain", "fancy"), Run: fg.run}
	res, err := r.References(context.Background(), "shape.Total")
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 2 || res.Files != 2 || res.Incomplete {
		t.Fatalf("total %d files %d incomplete %v, want 2, 2, false\n%+v", res.Total, res.Files, res.Incomplete, res)
	}
	// sorted by file; the tagged file's hit names only the configuration that compiles it
	if h := res.Hits[0]; h.File != "shape/circle_fancy.go" || h.Line != 12 || !slices.Equal(h.Configs, []string{"fancy"}) || h.In != "fancyTotal" {
		t.Errorf("hit 0 = %+v", h)
	}
	if h := res.Hits[1]; h.File != "shape/shape.go" || !slices.Equal(h.Configs, []string{"plain", "fancy"}) || h.In != "Total" || h.Text != "t += s.Area()" {
		t.Errorf("hit 1 = %+v", h)
	}
	text := res.Text(2, 0)
	for _, want := range []string{"references of shape.Total: 2 lines in 2 files", "12 fancyTotal [fancy]:", "  11 Total: t += s.Area()"} {
		if !strings.Contains(text, want) {
			t.Errorf("Text lacks %q:\n%s", want, text)
		}
	}
	if capped := res.Text(2, 1); !strings.Contains(capped, "1 more lines not shown") {
		t.Errorf("capped Text does not say what it left out:\n%s", capped)
	}
}

func TestReferences_skipsConfigurationsThatDoNotCompileTheDeclaration(t *testing.T) {
	root := writeTree(t, tagged)
	fg := &fakeGopls{root: root, out: map[string]string{}}
	r := &Runner{Repo: root, Config: fakeConfigs("plain", "fancy"), Run: fg.run}
	res, err := r.References(context.Background(), "Circle.Area")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fg.asked, []string{"fancy|references|shape/circle_fancy.go:8"}) {
		t.Errorf("asked %v, want only the fancy configuration", fg.asked)
	}
	if res.Incomplete {
		t.Error("a declaration one configuration compiles is not an incomplete answer")
	}
	// with no configuration that compiles it, the answer says so instead of reading as "no references"
	r.Config = fakeConfigs("plain")
	fg.asked = nil
	res, err = r.References(context.Background(), "Circle.Area")
	if err != nil {
		t.Fatal(err)
	}
	if len(fg.asked) != 0 || !res.Incomplete {
		t.Fatalf("asked %v incomplete %v, want nothing asked and incomplete", fg.asked, res.Incomplete)
	}
	if text := res.Text(1, 0); !strings.Contains(text, "INCOMPLETE: no listed configuration compiles shape/circle_fancy.go:8 (//go:build fancy)") {
		t.Errorf("Text does not name the uncovered declaration:\n%s", text)
	}
}

func TestReferences_asksEveryPlatformTwin(t *testing.T) {
	root := writeTree(t, tagged)
	fg := &fakeGopls{root: root, out: map[string]string{
		"linux|references|shape/name_linux.go:3":   "ROOT/shape/use.go:3:33-41\n",
		"darwin|references|shape/name_darwin.go:3": "ROOT/shape/use.go:3:33-41\n",
	}}
	cfg := Config{Configs: []BuildConfig{
		{Name: "linux", Env: map[string]string{"KENTEST_CONFIG": "linux", "GOOS": "linux", "GOARCH": "amd64"}},
		{Name: "darwin", Env: map[string]string{"KENTEST_CONFIG": "darwin", "GOOS": "darwin", "GOARCH": "arm64"}},
	}}
	r := &Runner{Repo: root, Config: cfg, Run: fg.run}
	res, err := r.References(context.Background(), "Platform")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"darwin|references|shape/name_darwin.go:3", "linux|references|shape/name_linux.go:3"}
	if !slices.Equal(fg.asked, want) {
		t.Errorf("asked %v, want each twin under its own platform: %v", fg.asked, want)
	}
	if res.Total != 1 || !slices.Equal(res.Hits[0].Configs, []string{"linux", "darwin"}) {
		t.Errorf("hits %+v, want the one call seen by both", res.Hits)
	}
	// the windows twin compiles under neither configuration: reported, not dropped
	if !res.Incomplete || !strings.Contains(res.Text(2, 0), "shape/name_windows.go:3") {
		t.Errorf("the uncovered windows twin is not reported:\n%s", res.Text(2, 0))
	}
}

func TestReferences_failureAndAmbiguity(t *testing.T) {
	root := writeTree(t, tagged)
	fg := &fakeGopls{root: root,
		out:  map[string]string{"plain|references|shape/shape.go:8": "ROOT/shape/shape.go:11:8-12\n"},
		fail: map[string]string{"fancy|references|shape/shape.go:8": "gopls: no package metadata for file"}}
	r := &Runner{Repo: root, Config: fakeConfigs("plain", "fancy"), Run: fg.run}
	res, err := r.References(context.Background(), "shape.Total")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Incomplete || res.Total != 1 || !strings.Contains(res.Text(2, 0), "INCOMPLETE: fancy at shape/shape.go:8: gopls: no package metadata for file") {
		t.Errorf("a failed configuration must mark the answer incomplete and say which:\n%s", res.Text(2, 0))
	}
	if _, err := r.References(context.Background(), "Total"); !errors.Is(err, ErrAmbiguous) || !strings.Contains(err.Error(), "other other.Total") || !strings.Contains(err.Error(), "deep/other other.Total") {
		t.Errorf("bare Total: err = %v, want ErrAmbiguous naming both", err)
	}
	if _, err := r.References(context.Background(), "Nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown symbol: err = %v, want ErrNotFound", err)
	}
}

func TestRun_wrapPrefixesTheCommand(t *testing.T) {
	root := writeTree(t, tagged)
	fg := &fakeGopls{root: root, out: map[string]string{}}
	cfg := fakeConfigs("plain")
	cfg.Wrap = []string{"lockrun", "--shared", "--"}
	r := &Runner{Repo: root, Config: cfg, Run: fg.run}
	if _, err := r.References(context.Background(), "shape.Total"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fg.argv0, []string{"lockrun"}) {
		t.Errorf("argv[0] = %v, want the wrap command", fg.argv0)
	}
}

func TestApplyHunks(t *testing.T) {
	orig := strings.Split("a\nb\nc\nd\ne\n", "\n")
	hunks := strings.Split("@@ -2,2 +2,2 @@\n-b\n+B\n c\n@@ -5 +5 @@\n-e\n+E\n", "\n")
	got, err := applyHunks(orig, hunks)
	if err != nil || strings.Join(got, "\n") != "a\nB\nc\nd\nE\n" {
		t.Fatalf("got %q err %v", strings.Join(got, "\n"), err)
	}
	if _, err := applyHunks(orig, []string{"@@ -2 +2 @@", "-x", "+y"}); err == nil {
		t.Error("a hunk that does not match the file was applied")
	}
}

func renameDiff(file, old, new string, line int) string {
	return fmt.Sprintf("--- ROOT/%s.orig\n+++ ROOT/%s\n@@ -%d +%d @@\n-%s\n+%s\n", file, file, line, line, old, new)
}

func TestRenamePlan_mergesConfigurations(t *testing.T) {
	root := writeTree(t, tagged)
	shapeGo := renameDiff("shape/shape.go", "type Shape interface {", "type Form interface {", 4) +
		"@@ -8 +8 @@\n-func Total(ss []Shape) float64 {\n+func Total(ss []Form) float64 {\n"
	square := renameDiff("shape/square.go", "func UnitSquare() Shape { return Square{Side: 1} }", "func UnitSquare() Form { return Square{Side: 1} }", 7)
	circle := renameDiff("shape/circle_fancy.go", "func UnitCircle() Shape { return Circle{R: 1} }", "func UnitCircle() Form { return Circle{R: 1} }", 10) +
		"@@ -12 +12 @@\n-func fancyTotal() float64 { return Total([]Shape{UnitCircle()}) + Circle{R: 2}.Area() }\n+func fancyTotal() float64 { return Total([]Form{UnitCircle()}) + Circle{R: 2}.Area() }\n"
	fg := &fakeGopls{root: root, out: map[string]string{
		"plain|rename|shape/shape.go:4": shapeGo + square,
		"fancy|rename|shape/shape.go:4": shapeGo + square + circle,
	}}
	r := &Runner{Repo: root, Config: fakeConfigs("plain", "fancy"), Run: fg.run}
	plan, err := r.RenamePlan(context.Background(), "Shape", "Form")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range plan.Files {
		got = append(got, fmt.Sprintf("%s %v %v", f.File, f.Lines, f.Configs))
	}
	want := []string{"shape/circle_fancy.go [10 12] [fancy]", "shape/shape.go [4 8] [plain fancy]", "shape/square.go [7] [plain fancy]"}
	if !slices.Equal(got, want) || plan.Incomplete || len(plan.Conflicts) != 0 {
		t.Fatalf("files %v conflicts %v incomplete %v\nwant %v", got, plan.Conflicts, plan.Incomplete, want)
	}
	// what no rename edits: the doc comments and the markdown that spell the old name
	var left []string
	for _, m := range plan.Mentions {
		left = append(left, fmt.Sprintf("%s %v", m.File, m.Lines))
	}
	if wantLeft := []string{"NOTES.md [1]", "shape/circle_fancy.go [5]", "shape/shape.go [3]"}; !slices.Equal(left, wantLeft) {
		t.Errorf("mentions %v, want %v", left, wantLeft)
	}
	// the merged patch applies to the real tree and leaves the repository itself untouched until then
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH: patch application not checked")
	}
	patch := filepath.Join(t.TempDir(), "rename.patch")
	if err := os.WriteFile(patch, []byte(plan.Patch), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "shape/shape.go")); !strings.Contains(string(b), "type Shape interface") {
		t.Fatal("RenamePlan wrote to the repository")
	}
	for _, args := range [][]string{{"init", "-q"}, {"apply", patch}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s\npatch:\n%s", args, err, out, plan.Patch)
		}
	}
	for file, want := range map[string]string{
		"shape/shape.go":        "func Total(ss []Form) float64 {",
		"shape/circle_fancy.go": "return Total([]Form{UnitCircle()})",
		"shape/square.go":       "func UnitSquare() Form {",
	} {
		if b, _ := os.ReadFile(filepath.Join(root, file)); !strings.Contains(string(b), want) {
			t.Errorf("%s after git apply lacks %q", file, want)
		}
	}
}

func TestRenamePlan_reportsDisagreement(t *testing.T) {
	root := writeTree(t, tagged)
	fg := &fakeGopls{root: root, out: map[string]string{
		"plain|rename|shape/shape.go:4": renameDiff("shape/shape.go", "type Shape interface {", "type Form interface {", 4),
		"fancy|rename|shape/shape.go:4": renameDiff("shape/shape.go", "type Shape interface {", "type Form  interface {", 4),
	}}
	r := &Runner{Repo: root, Config: fakeConfigs("plain", "fancy"), Run: fg.run}
	plan, err := r.RenamePlan(context.Background(), "Shape", "Form")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Conflicts) != 1 || !strings.Contains(plan.Conflicts[0], "shape/shape.go:4: plain and fancy") || !plan.Incomplete {
		t.Fatalf("conflicts %v incomplete %v, want the one disagreement reported", plan.Conflicts, plan.Incomplete)
	}
	if _, err := r.RenamePlan(context.Background(), "Shape", "not an ident"); err == nil {
		t.Error("RenamePlan accepted a new name that is not an identifier")
	}
}

// TestRealGopls runs the three queries through the real gopls on the tagged module: the one test that shows gopls's
// own output formats are the ones this package parses. It needs gopls and go on PATH and skips without them.
func TestRealGopls(t *testing.T) {
	if testing.Short() {
		t.Skip("runs gopls: skipped under -short")
	}
	for _, bin := range []string{"gopls", "go", "git"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not on PATH", bin)
		}
	}
	root := writeTree(t, tagged)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	r := &Runner{Repo: root, Config: taggedConfigs, Timeout: time.Minute}

	lines := func(res *Result) []string {
		var out []string
		for _, h := range res.Hits {
			out = append(out, fmt.Sprintf("%s:%d %v", h.File, h.Line, h.Configs))
		}
		return out
	}
	res, err := r.References(ctx, "shape.Total")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := lines(res), []string{"shape/circle_fancy.go:12 [linux-fancy]"}; !slices.Equal(got, want) || res.Incomplete {
		t.Errorf("references of shape.Total = %v (incomplete %v), want %v\n%s", got, res.Incomplete, want, res.Text(3, 0))
	}

	res, err = r.Implementations(ctx, "Shape")
	if err != nil {
		t.Fatal(err)
	}
	wantImpl := []string{"shape/circle_fancy.go:6 [linux-fancy]", "shape/square.go:3 [linux linux-fancy darwin]"}
	if got := lines(res); !slices.Equal(got, wantImpl) {
		t.Errorf("implementations of Shape = %v, want %v\n%s", got, wantImpl, res.Text(3, 0))
	}

	// a per-platform function: each twin is asked under its platform, and the shared caller is seen by all
	res, err = r.References(ctx, "Platform")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := lines(res), []string{"shape/use.go:3 [linux linux-fancy darwin]"}; !slices.Equal(got, want) {
		t.Errorf("references of Platform = %v, want %v\n%s", got, want, res.Text(3, 0))
	}

	plan, err := r.RenamePlan(ctx, "Platform", "HostOS")
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, f := range plan.Files {
		files = append(files, f.File)
	}
	// the windows twin is under no listed configuration: the plan must say so, since applying it breaks windows
	if want := []string{"shape/name_darwin.go", "shape/name_linux.go", "shape/use.go"}; !slices.Equal(files, want) || !plan.Incomplete || len(plan.Conflicts) != 0 {
		t.Fatalf("rename files %v incomplete %v conflicts %v, want %v and incomplete\n%s", files, plan.Incomplete, plan.Conflicts, want, plan.Text(3, 0))
	}
	patch := filepath.Join(t.TempDir(), "p.patch")
	if err := os.WriteFile(patch, []byte(plan.Patch), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"apply", patch}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s\n%s", args, err, out, plan.Patch)
		}
	}
	for _, goos := range []string{"linux", "darwin"} {
		cmd := exec.Command("go", "vet", "./...")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH=amd64", "CGO_ENABLED=0", "GOFLAGS=-tags=fancy", "GOWORK=off")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("after the merged rename, go vet under %s: %v\n%s", goos, err, out)
		}
	}
}
