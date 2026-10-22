package engine_test

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/townsendmerino/ken/engine"
)

// K14: qualified names, and Go method shells aren't definitions.

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range files {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

type site struct {
	File string
	Line int
}

func TestQualifiedNames(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"pull/types.go":   pullTypes,
		"pull/methods.go": pullMethods,
		"pull/use.go": `package pull

func local(m Model) int {
	return Select(m)
}
`,
		"app/app.go": `package app

import "example.com/pull"

type runner struct{}

func (runner) Select(x int) int { return x }

func Run(m pull.Model) int {
	var r runner
	return pull.Select(m) + r.Select(1)
}
`,
		"cfg1/config/config.go": "package config\n\nfunc Load() int { return 1 }\n",
		"cfg2/config/config.go": "package config\n\nfunc Load() int { return 2 }\n",
		"late/late.go": `package late

func (t T) Get() int { return 1 }

type T struct{}
`,
		"py/mod.py": "def func():\n    return 1\n",
	})
	ws, ctx := openBM25(t, dir, false)
	defs := func(sym string) []site {
		t.Helper()
		got, err := ws.Definition(ctx, sym)
		if err != nil {
			t.Fatalf("Definition(%s): %v", sym, err)
		}
		var out []site
		for _, s := range got {
			out = append(out, site{s.File, s.Line})
		}
		return out
	}

	for sym, want := range map[string][]site{
		"pull.Select":      {{"pull/types.go", 5}},
		"pull.Model.Size":  {{"pull/methods.go", 3}},
		"Model":            {{"pull/types.go", 3}}, // not methods.go, which only holds Size
		"Model.Size":       {{"pull/methods.go", 3}},
		"pull.Model":       {{"pull/types.go", 3}},
		"config.Load":      {{"cfg1/config/config.go", 3}, {"cfg2/config/config.go", 3}},
		"other.Load":       nil,
		"app.Select":       nil,                   // a method of runner, not a package-level Select
		"T":                {{"late/late.go", 5}}, // declared after its method in the same file
		"late.T.Get":       {{"late/late.go", 3}},
		"mod.func":         nil, // Python module qualifiers aren't resolved
		"pull.Model.Nope":  nil,
		"nopkg.Model.Size": nil,
	} {
		if got := defs(sym); !reflect.DeepEqual(got, want) {
			t.Errorf("Definition(%s) = %v, want %v", sym, got, want)
		}
	}

	// The shell still groups Size in methods.go's outline, with span 0.
	outs, err := ws.Outline(ctx, "pull/methods.go")
	if err != nil || len(outs) != 1 {
		t.Fatalf("Outline = %v, %v", outs, err)
	}
	var shell *engine.OutlineEntry
	for i, e := range outs[0].Entries {
		if e.Name == "Model" && e.Kind == engine.DefinitionKindClass {
			shell = &outs[0].Entries[i]
		}
	}
	if shell == nil || shell.StartLine != 0 || shell.EndLine != 0 {
		t.Errorf("methods.go outline shell = %+v, want class Model with span 0", shell)
	}

	// Symbols read defs, so the shell's file doesn't list Model.
	if syms, _ := ws.Symbols(ctx, "pull/methods.go"); slices.Contains(syms, "Model") {
		t.Errorf("Symbols(pull/methods.go) = %v, want no Model (a shell)", syms)
	}
	if syms, _ := ws.Symbols(ctx, "pull"); !slices.Contains(syms, "Model") {
		t.Errorf("Symbols(pull) = %v, want Model (types.go declares it)", syms)
	}

	// Qualified callers: pull.Select from another package and bare Select
	// inside pull, not r.Select on a local receiver.
	callers := func(sym string) map[string][]int {
		t.Helper()
		sites, err := ws.Callers(ctx, sym)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string][]int{}
		for _, s := range sites {
			for _, c := range s.Calls {
				out[s.File] = append(out[s.File], c.Line)
			}
		}
		return out
	}
	if got, want := callers("pull.Select"), map[string][]int{"app/app.go": {11}, "pull/use.go": {4}}; !reflect.DeepEqual(got, want) {
		t.Errorf("Callers(pull.Select) = %v, want %v", got, want)
	}
	// A bare name still matches every call by leaf name, r.Select included.
	if got := callers("Select"); len(got["app/app.go"]) != 1 || len(got["pull/use.go"]) != 1 {
		t.Errorf("Callers(Select) = %v, want app.go and use.go", got)
	}
	if got := callers("other.Select"); len(got) != 0 {
		t.Errorf("Callers(other.Select) = %v, want none", got)
	}
	// References use the same matching for calls.
	refs, _ := ws.References(ctx, "pull.Select")
	if len(refs) != 2 {
		t.Errorf("References(pull.Select) = %+v, want the two call references", refs)
	}
}
