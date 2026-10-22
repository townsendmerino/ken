package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/townsendmerino/ken/engine"
)

// K11: lines on every structural result.

const linesGo = `package shapes

import "fmt"

var registry = Area(1)

// Shape is anything with an area.
// It has one method.
type Shape interface {
	Area() float64
}

// Square is a square.
type Square struct {
	side float64
}

// Area returns the area.
func (s Square) Area() float64 {
	return s.side * s.side
}

/* Area computes a square's area
   from its side. */
func Area(side float64) float64 {
	sq := Square{side: side}
	f := func() float64 {
		return Area(side) // a call inside a closure counts for Area
	}
	_ = f
	fmt.Println(sq.Area())
	return sq.Area()
}

func Describe() string {
	return fmt.Sprint(Area(2), Area(3))
}
`

const linesPy = `import functools

value = compute(1)


def compute(x):
    return x * 2


@functools.lru_cache
def cached(x):
    return compute(x)


class Outer:
    """An outer class."""

    class Inner:
        def method(self):
            return compute(3)

    def run(self):
        helper = lambda: compute(4)
        return helper()
`

func writeLinesFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range map[string]string{"shapes/shapes.go": linesGo, "py/mod.py": linesPy} {
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

func openBM25(t *testing.T, dir string, watch bool) (*engine.Workspace, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	ws, err := engine.Open(ctx, dir, engine.Options{Mode: "bm25", Watch: watch})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	return ws, ctx
}

// lineOf returns the 1-based line of the first line in src containing sub.
func lineOf(t *testing.T, src, sub string) int {
	t.Helper()
	for i, l := range strings.Split(src, "\n") {
		if strings.Contains(l, sub) {
			return i + 1
		}
	}
	t.Fatalf("%q not in fixture", sub)
	return 0
}

// checkSpan asserts 1 ≤ start ≤ end ≤ the file's line count and that the
// span's text contains name.
func checkSpan(t *testing.T, src, what, name string, start, end int) {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(src, "\n"), "\n")
	if start < 1 || start > end || end > len(lines) {
		t.Errorf("%s %s: span %d-%d outside 1..%d", what, name, start, end, len(lines))
		return
	}
	if !strings.Contains(strings.Join(lines[start-1:end], "\n"), name) {
		t.Errorf("%s %s: span %d-%d doesn't contain the name", what, name, start, end)
	}
}

func TestStructuralSpans(t *testing.T) {
	dir := writeLinesFixture(t)
	ws, ctx := openBM25(t, dir, false)

	type want struct {
		file, symbol, name string
		start, end, doc    int
	}
	wants := []want{
		{"shapes/shapes.go", "Shape", "Shape", lineOf(t, linesGo, "type Shape"), 11, lineOf(t, linesGo, "// Shape is")},
		{"shapes/shapes.go", "Square", "Square", lineOf(t, linesGo, "type Square"), 16, lineOf(t, linesGo, "// Square is")},
		{"shapes/shapes.go", "Square.Area", "Area", lineOf(t, linesGo, "func (s Square)"), lineOf(t, linesGo, "s.side * s.side") + 1, lineOf(t, linesGo, "// Area returns")},
		{"py/mod.py", "compute", "compute", lineOf(t, linesPy, "def compute"), lineOf(t, linesPy, "return x * 2"), 0},
		{"py/mod.py", "cached", "cached", lineOf(t, linesPy, "def cached"), lineOf(t, linesPy, "return compute(x)"), 0},
		{"py/mod.py", "Outer", "Outer", lineOf(t, linesPy, "class Outer"), lineOf(t, linesPy, "return helper()"), 0},
		{"py/mod.py", "Inner", "Inner", lineOf(t, linesPy, "class Inner"), lineOf(t, linesPy, "return compute(3)"), 0},
		{"py/mod.py", "Inner.method", "method", lineOf(t, linesPy, "def method"), lineOf(t, linesPy, "return compute(3)"), 0},
		{"py/mod.py", "Outer.run", "run", lineOf(t, linesPy, "def run"), lineOf(t, linesPy, "return helper()"), 0},
	}
	// Shape and Square's closing-brace lines (11, 16) are literal; check the
	// fixture hasn't shifted under them.
	if got := lineOf(t, linesGo, "type Shape"); got != 9 {
		t.Fatalf("fixture moved: type Shape on line %d", got)
	}

	for _, w := range wants {
		sites, err := ws.Definition(ctx, w.symbol)
		if err != nil {
			t.Fatalf("Definition(%s): %v", w.symbol, err)
		}
		var site *engine.DefinitionSite
		for i := range sites {
			if sites[i].File == w.file {
				site = &sites[i]
			}
		}
		if site == nil {
			t.Errorf("Definition(%s) = %+v, want a site in %s", w.symbol, sites, w.file)
			continue
		}
		src := linesGo
		if strings.HasSuffix(w.file, ".py") {
			src = linesPy
		}
		checkSpan(t, src, "definition", w.name, site.Line, site.EndLine)
		if site.Line != w.start || site.EndLine != w.end || site.DocLine != w.doc {
			t.Errorf("Definition(%s) lines = %d-%d doc %d, want %d-%d doc %d", w.symbol, site.Line, site.EndLine, site.DocLine, w.start, w.end, w.doc)
		}
	}

	// The decorated function's span starts at `def`, not at the decorator
	// (documented in internal/structural's "Spans").
	if sites, _ := ws.Definition(ctx, "cached"); len(sites) == 1 && sites[0].Line != lineOf(t, linesPy, "@functools")+1 {
		t.Errorf("decorated cached starts at %d, want the def line after the decorator", sites[0].Line)
	}

	// Go top-level function with a /* */ doc block.
	sites, err := ws.Definition(ctx, "Area")
	if err != nil {
		t.Fatal(err)
	}
	var fnSite engine.DefinitionSite
	for _, s := range sites {
		if s.Kind == engine.DefinitionKindFunction {
			fnSite = s
		}
	}
	if fnSite.DocLine != lineOf(t, linesGo, "/* Area computes") || fnSite.Line != lineOf(t, linesGo, "func Area(") {
		t.Errorf("func Area site = %+v, want DocLine at the /* block and Line at func Area", fnSite)
	}

	// Every Definition site and Outline entry: a sane span containing its
	// name, and Definition agrees with Outline.
	srcs := map[string]string{"shapes/shapes.go": linesGo, "py/mod.py": linesPy}
	for file, src := range srcs {
		outs, err := ws.Outline(ctx, file)
		if err != nil || len(outs) != 1 {
			t.Fatalf("Outline(%s) = %v, %v", file, outs, err)
		}
		for _, e := range outs[0].Entries {
			checkSpan(t, src, "outline "+file, e.Name, e.StartLine, e.EndLine)
			sym := e.Name
			if e.Container != "" {
				sym = e.Container + "." + e.Name
			}
			defs, err := ws.Definition(ctx, sym)
			if err != nil {
				t.Fatal(err)
			}
			agreed := false
			for _, d := range defs {
				checkSpan(t, srcs[d.File], "definition", e.Name, d.Line, d.EndLine)
				if d.File == file && d.Line == e.StartLine && d.EndLine == e.EndLine && d.DocLine == e.DocLine {
					agreed = true
				}
			}
			if !agreed {
				t.Errorf("Outline(%s) entry %s %d-%d (doc %d) has no matching Definition(%s) site: %+v", file, sym, e.StartLine, e.EndLine, e.DocLine, sym, defs)
			}
		}
	}
}

func TestCallersCallsAndReferenceLines(t *testing.T) {
	dir := writeLinesFixture(t)
	ws, ctx := openBM25(t, dir, false)

	sites, err := ws.Callers(ctx, "Area")
	if err != nil {
		t.Fatal(err)
	}
	if len(sites) != 1 || sites[0].File != "shapes/shapes.go" {
		t.Fatalf("Callers(Area) = %+v, want one CallSite for shapes/shapes.go", sites)
	}
	wantGo := []engine.Call{
		{Line: lineOf(t, linesGo, "var registry"), Caller: ""},
		{Line: lineOf(t, linesGo, "a call inside a closure"), Caller: "Area"},
		{Line: lineOf(t, linesGo, "fmt.Println(sq.Area())"), Caller: "Area"},
		{Line: lineOf(t, linesGo, "return sq.Area()"), Caller: "Area"},
		{Line: lineOf(t, linesGo, "fmt.Sprint(Area(2), Area(3))"), Caller: "Describe"},
	}
	if !reflect.DeepEqual(sites[0].Calls, wantGo) {
		t.Errorf("Callers(Area).Calls =\n %+v\nwant\n %+v", sites[0].Calls, wantGo)
	}

	sites, err = ws.Callers(ctx, "compute")
	if err != nil {
		t.Fatal(err)
	}
	wantPy := []engine.Call{
		{Line: lineOf(t, linesPy, "value = compute(1)"), Caller: ""},
		{Line: lineOf(t, linesPy, "return compute(x)"), Caller: "cached"},
		{Line: lineOf(t, linesPy, "return compute(3)"), Caller: "Inner.method"},
		{Line: lineOf(t, linesPy, "lambda: compute(4)"), Caller: "Outer.run"},
	}
	if len(sites) != 1 || !reflect.DeepEqual(sites[0].Calls, wantPy) {
		t.Errorf("Callers(compute) = %+v, want one site with Calls %+v", sites, wantPy)
	}

	refs, err := ws.References(ctx, "Area")
	if err != nil {
		t.Fatal(err)
	}
	var callRef *engine.Reference
	for i := range refs {
		if refs[i].Kind == engine.ReferenceKindCall {
			callRef = &refs[i]
		}
	}
	wantLines := []int{wantGo[0].Line, wantGo[1].Line, wantGo[2].Line, wantGo[3].Line, wantGo[4].Line}
	if callRef == nil || !reflect.DeepEqual(callRef.Lines, wantLines) {
		t.Errorf("References(Area) call lines = %+v, want %v", callRef, wantLines)
	}
	refs, err = ws.References(ctx, "functools")
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0].Kind != engine.ReferenceKindImport || refs[0].Lines != nil {
		t.Errorf("References(functools) = %+v, want one import reference with nil Lines", refs)
	}
}
