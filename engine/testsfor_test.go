package engine_test

import (
	"reflect"
	"testing"

	"github.com/townsendmerino/ken/engine"
)

// K15: the tests and the callees of a symbol.

const calcGo = `package calc

import "fmt"

func F() int { return 1 }

func h() int { return F() + 1 }

func Caller() string {
	g := func() int {
		return F()
	}
	return fmt.Sprint(g(), h())
}
`

const calcTestGo = `package calc

import "testing"

func TestA(t *testing.T) { _ = F() }

func TestB(t *testing.T) { _ = h() }

func TestC(t *testing.T) {}

func TestD(t *testing.T) {
	t.Run("sub", func(t *testing.T) {
		_ = F()
	})
}

// Not a test: go test needs a non-lowercase letter after the prefix.
func Testimony(t *testing.T) { _ = F() }
`

const modPy = `def f():
    return 1
`

const testModPy = `from mod import f


def test_x():
    assert f() == 1


class TestK:
    def test_y(self):
        assert f() == 1

    def helper(self):
        return f()


def check_f():
    return f()
`

type testRow struct {
	QName string
	Depth int
	Via   string
	Dir   string
}

func testRows(sites []engine.TestSite) []testRow {
	var out []testRow
	for _, s := range sites {
		out = append(out, testRow{s.QName, s.Depth, s.Via, s.Dir})
	}
	return out
}

func TestTestsForAndCallees(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"calc/calc.go":      calcGo,
		"calc/calc_test.go": calcTestGo,
		"py/mod.py":         modPy,
		"py/test_mod.py":    testModPy,
		"js/a.test.js":      "function test_f() { return F(); }\n",
	})
	ws, ctx := openBM25(t, dir, false)

	got, err := ws.TestsFor(ctx, "F", 1)
	if err != nil {
		t.Fatal(err)
	}
	want := []testRow{{"TestA", 1, "", "calc"}, {"TestD", 1, "", "calc"}}
	if !reflect.DeepEqual(testRows(got), want) {
		t.Errorf("TestsFor(F, 1) = %+v, want %+v", testRows(got), want)
	}
	for _, s := range got {
		if s.File != "calc/calc_test.go" || s.StartLine == 0 || s.EndLine < s.StartLine {
			t.Errorf("TestsFor(F, 1) site %+v: want calc/calc_test.go with a span", s)
		}
	}

	got, err = ws.TestsFor(ctx, "F", 2)
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, testRow{"TestB", 2, "h", "calc"})
	if !reflect.DeepEqual(testRows(got), want) {
		t.Errorf("TestsFor(F, 2) = %+v, want %+v", testRows(got), want)
	}

	got, err = ws.TestsFor(ctx, "f", 2)
	if err != nil {
		t.Fatal(err)
	}
	wantPy := []testRow{{"test_x", 1, "", "py"}, {"TestK.test_y", 1, "", "py"}}
	if !reflect.DeepEqual(testRows(got), wantPy) {
		t.Errorf("TestsFor(f, 2) = %+v, want %+v (helper and check_f aren't tests, and no test calls them)", testRows(got), wantPy)
	}

	if got, err := ws.TestsFor(ctx, "Nope", 2); err != nil || len(got) != 0 {
		t.Errorf("TestsFor(Nope) = %+v, %v; want none", got, err)
	}

	callees, err := ws.Callees(ctx, "Caller")
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		Name, Receiver string
		Line           int
	}
	var rows []row
	for _, c := range callees {
		rows = append(rows, row{c.Name, c.Receiver, c.Line})
		if c.File != "calc/calc.go" {
			t.Errorf("Callees(Caller) entry in %s, want calc/calc.go", c.File)
		}
	}
	wantCallees := []row{{"F", "", 11}, {"Sprint", "fmt", 13}, {"g", "", 13}, {"h", "", 13}}
	if !reflect.DeepEqual(rows, wantCallees) {
		t.Errorf("Callees(Caller) = %+v, want %+v (the closure's F call included)", rows, wantCallees)
	}
	if c, err := ws.Callees(ctx, "calc.Caller"); err != nil || len(c) != len(wantCallees) {
		t.Errorf("Callees(calc.Caller) = %+v, %v; want the same %d calls", c, err, len(wantCallees))
	}
	if c, err := ws.Callees(ctx, "TestK.helper"); err != nil || len(c) != 1 || c[0].Name != "f" {
		t.Errorf("Callees(TestK.helper) = %+v, %v; want f", c, err)
	}
}
