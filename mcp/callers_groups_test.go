package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/townsendmerino/ken/engine"
)

func callersFixture(t *testing.T, files map[string]string) *Config {
	t.Helper()
	dir := t.TempDir()
	for n, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(n))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ws, err := engine.Open(context.Background(), dir, engine.Options{Mode: "bm25"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	b := &RepoBundle{Workspace: ws}
	cache := NewCache(2, func(context.Context, string) (*RepoBundle, func(), error) { return b, nil, nil })
	return &Config{Cache: cache, DefaultRepo: dir}
}

func callersText(t *testing.T, cfg *Config, args CallersArgs) string {
	t.Helper()
	r, _, err := handleCallers(context.Background(), cfg, args)
	if err != nil {
		t.Fatal(err)
	}
	return extractText(t, r)
}

const callerSrc = "package %s\n\nfunc use() { Shared() }\n"

func backendFiles() map[string]string {
	return map[string]string{
		"go.mod":                 "module example.com/x\n\ngo 1.22\n",
		"shared.go":              "package x\n\nfunc Shared() {}\n",
		"backend/cuda/cuda.go":   "package cuda\n\nimport \"example.com/x\"\n\nfunc use() { x.Shared() }\n",
		"backend/metal/metal.go": "package metal\n\nimport \"example.com/x\"\n\nfunc use() { x.Shared() }\n",
		"scripts/run.py":         "def go():\n    Shared()\n",
	}
}

// F4: Go callers group by package import path, other languages by directory.
func TestCallers_GroupsByPackageImportPath(t *testing.T) {
	got := callersText(t, callersFixture(t, backendFiles()), CallersArgs{Symbol: "Shared"})
	for _, want := range []string{"### example.com/x/backend/cuda", "### example.com/x/backend/metal", "### scripts/"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing group heading %q:\n%s", want, got)
		}
	}
	if i, j := strings.Index(got, "backend/cuda\n"), strings.Index(got, "cuda.go"); i < 0 || j < i {
		t.Errorf("cuda.go should sit under the cuda heading:\n%s", got)
	}
}

// F4: under a go.work with several modules, groups also name the module.
func TestCallers_GroupsNameTheModuleUnderGoWork(t *testing.T) {
	cfg := callersFixture(t, map[string]string{
		"go.work":      "go 1.22\n\nuse (\n\t./a\n\t./b\n)\n",
		"a/go.mod":     "module example.com/a\n\ngo 1.22\n",
		"a/lib/lib.go": "package lib\n\nfunc Shared() {}\n\nfunc use() { Shared() }\n",
		"b/go.mod":     "module example.com/b\n\ngo 1.22\n",
		"b/lib/lib.go": "package lib\n\nfunc use() { Shared() }\n",
	})
	got := callersText(t, cfg, CallersArgs{Symbol: "Shared"})
	for _, want := range []string{"example.com/a/lib", "(module example.com/a)", "example.com/b/lib", "(module example.com/b)"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	j := callersText(t, cfg, CallersArgs{Symbol: "Shared", Output: "json"})
	if !strings.Contains(j, `"groups"`) || !strings.Contains(j, `"module": "example.com/b"`) {
		t.Errorf("json lacks groups with modules:\n%s", j)
	}
}

// F4: path_contains / exclude_path_contains filter the callers (engine.Scope).
func TestCallers_PathFilters(t *testing.T) {
	cfg := callersFixture(t, backendFiles())
	got := callersText(t, cfg, CallersArgs{Symbol: "Shared", PathContains: "backend/"})
	if !strings.Contains(got, "cuda.go") || !strings.Contains(got, "metal.go") || strings.Contains(got, "run.py") {
		t.Errorf("path_contains=backend/:\n%s", got)
	}
	got = callersText(t, cfg, CallersArgs{Symbol: "Shared", ExcludePathContains: "metal"})
	if !strings.Contains(got, "cuda.go") || strings.Contains(got, "metal.go") {
		t.Errorf("exclude_path_contains=metal:\n%s", got)
	}
	got = callersText(t, cfg, CallersArgs{Symbol: "Shared", PathContains: "nowhere/"})
	if !strings.Contains(got, "No callers") || !strings.Contains(got, "nowhere/") {
		t.Errorf("a filter that removes everything should say so and name the filter:\n%s", got)
	}
}
