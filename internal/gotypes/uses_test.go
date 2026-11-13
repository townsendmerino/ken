package gotypes

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

// surface is a module where package app names package lib four ways (plainly, through an alias, through a dot
// import, and from test files), part of it only under a build tag, beside a local value that shares the package's name.
var surface = map[string]string{
	"go.mod": "module example.com/surface\n\ngo 1.21\n",
	"lib/lib.go": `package lib

type Options struct {
	Seed int
	Name string
}

type Model struct{}

func (m *Model) Forward() int { return 0 }

type Runner interface{ Run() }

func Load(o Options) *Model { return &Model{} }

func Fancy() int { return 2 }

const Version = 1

var Default = Options{}
`,
	"deep/lib/lib.go": "package lib\n\nfunc Deep() int { return 3 }\n",
	"app/app.go": `package app

import "example.com/surface/lib"

func Start() int {
	m := lib.Load(lib.Options{
		Seed: 1,
	})
	return m.Forward() + lib.Version
}
`,
	"app/alias.go": `package app

import l "example.com/surface/lib"

func viaAlias() l.Options {
	return l.Default
}
`,
	"app/dot.go": `package app

import . "example.com/surface/lib"

func viaDot() *Model {
	return Load(Options{Name: "x"})
}
`,
	// no import here: lib is a local value, and its Options and Load are its own fields
	"app/shadow.go": `package app

type fake struct{ Options, Load int }

func shadow() int {
	lib := fake{}
	return lib.Options + lib.Load
}
`,
	"app/fancy.go": `//go:build fancy

package app

import "example.com/surface/lib"

func fancy() int {
	return lib.Fancy() + lib.Version
}
`,
	"app/broken.go": `//go:build broken

package app

import "example.com/surface/lib"

func broken() int { return lib.Missing }
`,
	"app/app_test.go": `package app

import "example.com/surface/lib"

func run(r lib.Runner) {
	r.Run()
	_ = lib.Options{}
}
`,
	"app/x_test.go": `package app_test

import (
	"example.com/surface/app"
	"example.com/surface/lib"
)

var _ = app.Start() + lib.Version
`,
	"only/only.go":   "//go:build fancy\n\npackage only\n\nimport \"example.com/surface/lib\"\n\nvar V = lib.Version\n",
	"both/both.go":   "package both\n\nimport (\n\tdeep \"example.com/surface/deep/lib\"\n\t\"example.com/surface/lib\"\n)\n\nvar V = deep.Deep() + lib.Version\n",
	"x/util/util.go": "package util\n",
	"y/util/util.go": "package util\n",
	"cmd/tool/m.go":  "package main\n\nfunc main() {}\n",
}

func surfaceConfigs(names ...string) Config {
	var c Config
	for _, n := range names {
		env := map[string]string{"GOOS": "linux", "GOARCH": "amd64", "CGO_ENABLED": "0", "GOWORK": "off", "GOFLAGS": "-mod=mod"}
		if n != "plain" {
			env["GOFLAGS"] += " -tags=" + n
		}
		c.Configs = append(c.Configs, BuildConfig{Name: n, Env: env})
	}
	return c
}

func useRows(res *UsesResult) []string {
	var out []string
	for _, u := range res.Uses {
		out = append(out, fmt.Sprintf("%s %s %d/%d %v", u.Name, u.Kind, u.NonTest, u.Test, u.Configs))
	}
	return out
}

func needGo(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("runs go list: skipped under -short")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not on PATH")
	}
}

func TestResolvePackageDir(t *testing.T) {
	root := writeTree(t, surface)
	for spec, want := range map[string]string{
		"app": "app", "./app/": "app", "lib": "lib", "deep/lib": "deep/lib", "x/util": "x/util", "tool": "cmd/tool", "main": "cmd/tool", "only": "only",
	} {
		if got, err := ResolvePackageDir(root, spec); err != nil || got != want {
			t.Errorf("ResolvePackageDir(%q) = %q, %v; want %q", spec, got, err, want)
		}
	}
	if _, err := ResolvePackageDir(root, "util"); !errors.Is(err, ErrAmbiguous) || !strings.Contains(err.Error(), "x/util; y/util") {
		t.Errorf("util: err = %v, want ErrAmbiguous naming both directories", err)
	}
	if _, err := ResolvePackageDir(root, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("nope: err = %v, want ErrNotFound", err)
	}
}

func TestUses_mergesConfigurations(t *testing.T) {
	needGo(t)
	root := writeTree(t, surface)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	r := &Runner{Repo: root, Config: surfaceConfigs("plain", "fancy"), Timeout: time.Minute}
	res, err := r.Uses(ctx, "app", "lib")
	if err != nil {
		t.Fatal(err)
	}
	// Lines, not occurrences. Options: app.go:6, alias.go:5, dot.go:6 and app_test.go:7. Version: app.go:9, plus
	// fancy.go:8 under the tag only, plus x_test.go:8 in the external test package. Fancy is seen by one
	// configuration and says so. shadow.go's lib.Options and lib.Load are a local struct's fields: not counted.
	want := []string{
		"Options type 3/1 [plain fancy]",
		"Version const 2/1 [plain fancy]",
		"Load func 2/0 [plain fancy]",
		"Default var 1/0 [plain fancy]",
		"Fancy func 1/0 [fancy]",
		"Model type 1/0 [plain fancy]",
		"Model.Forward method 1/0 [plain fancy]",
		"Options.Name field 1/0 [plain fancy]",
		"Options.Seed field 1/0 [plain fancy]",
		"Runner type 0/1 [plain fancy]",
		"Runner.Run method 0/1 [plain fancy]",
	}
	if got := useRows(res); !slices.Equal(got, want) {
		t.Errorf("uses =\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	if res.Incomplete || res.Total != 11 || res.PackageLevel != 7 || res.PackageLevelNonTest != 6 || res.Members != 4 || res.MembersNonTest != 3 ||
		res.Lines != 17 || res.Files != 6 {
		t.Errorf("totals: %+v", res)
	}
	if res.Importer != "app" || res.ImporterPath != "example.com/surface/app" || res.Imported != "example.com/surface/lib" || res.ImportedDir != "lib" {
		t.Errorf("packages: importer %q (%q), imported %q (%q)", res.Importer, res.ImporterPath, res.Imported, res.ImportedDir)
	}
	// the configuration without the tag saw fewer lines than the one with it
	if len(res.Runs) != 2 || res.Runs[0].Hits != 15 || res.Runs[1].Hits != 17 {
		t.Errorf("per-configuration lines: %+v, want plain 15 and fancy 17", res.Runs)
	}
	text := res.Text(2, 0)
	for _, w := range []string{
		"uses of lib by app: 11 identifiers, 17 lines in 6 files\n",
		"import paths: example.com/surface/lib <- example.com/surface/app\n",
		"package-level: 7 (6 named by non-test code) · methods and fields: 4 (3 named by non-test code)\n",
		"per configuration (lines): plain 15 (", "fancy 17 (",
		"Options        type    3 non-test  1 test\n",
		"Fancy          func    1 non-test  0 test  [fancy]\n",
		"Runner.Run     method  0 non-test  1 test\n",
	} {
		if !strings.Contains(text, w) {
			t.Errorf("Text lacks %q:\n%s", w, text)
		}
	}
	if capped := res.Text(2, 2); !strings.Contains(capped, "… 9 more identifiers not shown") {
		t.Errorf("capped Text:\n%s", capped)
	}

	// the imported package by import path, and one outside the importer's dependencies
	if byPath, err := r.Uses(ctx, "app", "example.com/surface/lib"); err != nil || byPath.Total != 11 {
		t.Errorf("by import path: %v, %v", byPath, err)
	}
	if _, err := r.Uses(ctx, "app", "deep/lib"); !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "does not depend on it") {
		t.Errorf("a repository package app does not import: err = %v", err)
	}
	if _, err := r.Uses(ctx, "app", "nosuch"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown imported package: err = %v", err)
	}
	// two packages named lib: the directory given in full is the top one, and its tail picks the other
	both, err := r.Uses(ctx, "both", "deep/lib")
	if err != nil || !slices.Equal(useRows(both), []string{"Deep func 1/0 [plain fancy]"}) {
		t.Errorf("both -> deep/lib: %v, %v", useRows(both), err)
	}
	if both, err = r.Uses(ctx, "both", "lib"); err != nil || !slices.Equal(useRows(both), []string{"Version const 1/0 [plain fancy]"}) {
		t.Errorf("both -> lib: %v, %v", useRows(both), err)
	}
}

func TestUses_lowerBoundsAreLabelled(t *testing.T) {
	needGo(t)
	root := writeTree(t, surface)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// a package only one configuration builds: the other is skipped, and the answer is complete
	r := &Runner{Repo: root, Config: surfaceConfigs("plain", "fancy"), Timeout: time.Minute}
	res, err := r.Uses(ctx, "only", "lib")
	if err != nil {
		t.Fatal(err)
	}
	if res.Incomplete || !slices.Equal(useRows(res), []string{"Version const 1/0 [fancy]"}) || res.Runs[0].Skipped == "" {
		t.Errorf("only: incomplete %v rows %v runs %+v", res.Incomplete, useRows(res), res.Runs)
	}
	// no listed configuration builds it: said, not read as "no uses"
	r.Config = surfaceConfigs("plain")
	res, err = r.Uses(ctx, "only", "lib")
	if err != nil {
		t.Fatal(err)
	}
	if text := res.Text(1, 0); !res.Incomplete || res.Total != 0 || !strings.Contains(text, "INCOMPLETE: no listed configuration builds only; add one to .ken-go.json") {
		t.Errorf("unbuilt importer:\n%s", text)
	}

	// a configuration whose files do not type-check gives what resolved, labelled
	r.Config = surfaceConfigs("plain", "broken")
	res, err = r.Uses(ctx, "app", "lib")
	if err != nil {
		t.Fatal(err)
	}
	if text := res.Text(2, 0); !res.Incomplete || res.Total != 10 || res.Lines != 15 || !strings.Contains(text, "broken 15, partial (") ||
		!strings.Contains(text, "INCOMPLETE: broken: 1 type errors, so some uses may be missing; first: broken.go:7: undefined: lib.Missing") {
		t.Errorf("type errors under one configuration:\n%s", text)
	}

	// a configuration the go command fails under
	r.Config = surfaceConfigs("plain", "fancy")
	r.Run = func(ctx context.Context, dir string, env, argv []string) ([]byte, []byte, error) {
		if slices.ContainsFunc(env, func(kv string) bool { return strings.HasPrefix(kv, "GOFLAGS=") && strings.Contains(kv, "fancy") }) {
			return nil, []byte("go: cannot find main module"), errors.New("exit status 1")
		}
		real := &Runner{Repo: dir, Environ: env, Timeout: time.Minute} // the real go command, same environment
		return real.exec(ctx, BuildConfig{}, argv[0], argv[1:]...)
	}
	res, err = r.Uses(ctx, "app", "lib")
	if err != nil {
		t.Fatal(err)
	}
	if text := res.Text(2, 0); !res.Incomplete || res.Total != 10 || !strings.Contains(text, "fancy FAILED (go list: go: cannot find main module)") ||
		!strings.Contains(text, "INCOMPLETE: fancy: go list: go: cannot find main module") {
		t.Errorf("failed configuration:\n%s", text)
	}
}

func TestUses_wrapPrefixesGoList(t *testing.T) {
	root := writeTree(t, surface)
	var argvs [][]string
	cfg := surfaceConfigs("plain")
	cfg.Wrap = []string{"lockrun", "--"}
	r := &Runner{Repo: root, Config: cfg, Run: func(_ context.Context, dir string, _, argv []string) ([]byte, []byte, error) {
		argvs = append(argvs, argv)
		if dir != root {
			t.Errorf("ran in %s, want the repository root (the wrap command is relative to it)", dir)
		}
		return nil, []byte("stop"), errors.New("exit status 1")
	}}
	res, err := r.Uses(context.Background(), "app", "lib")
	if err != nil || !res.Incomplete {
		t.Fatalf("res %+v err %v", res, err)
	}
	if len(argvs) != 1 || !slices.Equal(argvs[0][:5], []string{"lockrun", "--", "go", "-C", root + "/app"}) || argvs[0][5] != "list" {
		t.Errorf("argv = %v", argvs)
	}
}
