package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/townsendmerino/ken/engine"
	"github.com/townsendmerino/ken/internal/search"
	"github.com/townsendmerino/ken/internal/structural"
)

// testsRepo is a module with a function, a function that calls it, and a test of each.
var testsRepo = map[string]string{
	"go.mod": "module example.com/tt\n\ngo 1.21\n",
	"a/a.go": `package a

func Scale(n int) int {
	return n * 2
}

func Twice(n int) int {
	return Scale(Scale(n))
}

func Lonely() int { return 7 }
`,
	"a/a_test.go": `package a

import "testing"

func TestScale(t *testing.T) {
	if Scale(1) != 2 {
		t.Fail()
	}
}

func TestTwice(t *testing.T) {
	if Twice(1) != 4 {
		t.Fail()
	}
}

func BenchmarkScale(b *testing.B) {
	for range b.N {
		Scale(1)
	}
}
`,
	"py/shape.py":      "def area(side):\n    return side * side\n",
	"py/test_shape.py": "from shape import area\n\n\nclass TestArea:\n    def test_square(self):\n        assert area(2) == 4\n",
}

func testsConfig(t *testing.T) (*Config, string) {
	t.Helper()
	root := writeTSRepo(t, testsRepo)
	ix, err := search.NewWatchedIndex(root, search.ModeBM25, "regex", "", false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ix.Close() })
	sx, err := structural.Build(root)
	if err != nil {
		t.Fatal(err)
	}
	cache := NewCache(2, func(context.Context, string) (*RepoBundle, func(), error) {
		return &RepoBundle{Index: ix, StructuralBuilder: func(context.Context) (*structural.Index, error) { return sx, nil }}, nil, nil
	})
	t.Cleanup(cache.Close)
	return &Config{Cache: cache, DefaultRepo: root}, root
}

func TestTestsFor(t *testing.T) {
	cfg, _ := testsConfig(t)
	call := func(args TestsForArgs) string {
		res, _, err := handleTestsFor(context.Background(), cfg, args)
		if err != nil {
			t.Fatal(err)
		}
		return resultText(t, res)
	}
	// both tests reach Scale: one calls it, the other calls Twice, which does. The benchmark is not run by -run.
	text := call(TestsForArgs{Symbol: "a.Scale"})
	for _, want := range []string{"go test -run '^(TestScale|TestTwice)$' ./a", "`TestTwice` a/a_test.go:11 (through Twice)", "Matched by name"} {
		if !strings.Contains(text, want) {
			t.Errorf("tests_for a.Scale lacks %q:\n%s", want, text)
		}
	}
	if text = call(TestsForArgs{Symbol: "a.Scale", Depth: 1}); strings.Contains(text, "TestTwice") || !strings.Contains(text, "go test -run '^(TestScale)$' ./a") {
		t.Errorf("depth 1 should leave out the test one call away:\n%s", text)
	}
	if text = call(TestsForArgs{Symbol: "area"}); !strings.Contains(text, "pytest py/test_shape.py::TestArea::test_square") {
		t.Errorf("a Python test:\n%s", text)
	}
	if text = call(TestsForArgs{Symbol: "a.Lonely"}); !strings.Contains(text, `No test calls "a.Lonely" by name`) {
		t.Errorf("a function no test calls:\n%s", text)
	}
	if js := call(TestsForArgs{Symbol: "a.Scale", Output: "json"}); !strings.Contains(js, `"commands": [`) || !strings.Contains(js, `"via": "Twice"`) {
		t.Errorf("json: %s", js)
	}
	if text = call(TestsForArgs{}); !strings.Contains(text, "symbol is required") {
		t.Errorf("no symbol: %s", text)
	}
}

// An edit check on the default repository ends with the tests that call the functions the edit's old_string lies in.
func TestEditCheck_namesTheTestsToRun(t *testing.T) {
	cfg, root := testsConfig(t)
	ctx := context.Background()
	got := testsForEdit(ctx, cfg, "", root, []editSpot{{"a/a.go", "return n * 2"}, {"a/a.go", "func Lonely() int { return 7 }"}})
	if got == nil || strings.Join(got.Symbols, " ") != "a.Scale a.Lonely" || got.Tests != 3 || strings.Join(got.Uncovered, " ") != "a.Lonely" {
		t.Fatalf("tests for the edit = %+v", got)
	}
	text := got.text()
	for _, want := range []string{"tests that call what the edit changes (a.Scale, a.Lonely;", "  go test -run '^(TestScale|TestTwice)$' ./a", "no test calls a.Lonely by name"} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
	// nothing is said for another repository (its index may not be built), or for an edit outside any function
	if testsForEdit(ctx, cfg, root, root, []editSpot{{"a/a.go", "return n * 2"}}) != nil {
		t.Error("a repo argument should get no tests section")
	}
	if testsForEdit(ctx, cfg, "", root, []editSpot{{"a/a.go", "package a"}}) != nil {
		t.Error("an edit at file scope names no function")
	}
	if (*EditTests)(nil).text() != "" {
		t.Error("no tests section should render as nothing")
	}

	if engine.GoPath() == "" {
		t.Skip("go is not on PATH")
	}
	res, _, err := handleGoCheckEdit(ctx, cfg, GoCheckEditArgs{Edits: []GoEditArg{{File: "a/a.go", OldString: "return n * 2", NewString: "return n * 3"}}})
	if err != nil {
		t.Fatal(err)
	}
	if out := resultText(t, res); !strings.Contains(out, "checked: the edit was applied in memory") || !strings.Contains(out, "go test -run '^(TestScale|TestTwice)$' ./a") {
		t.Errorf("go_check_edit does not end with the tests to run:\n%s", out)
	}
	res, _, _ = handleGoCheckEdit(ctx, cfg, GoCheckEditArgs{Output: "json", Edits: []GoEditArg{{File: "a/a.go", OldString: "return n * 2", NewString: "return n * 3"}}})
	if js := resultText(t, res); !strings.Contains(js, `"clean": true`) || !strings.Contains(js, `"tests": {`) {
		t.Errorf("json lacks the check or the tests: %s", js)
	}
}
