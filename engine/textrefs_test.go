package engine_test

import (
	"reflect"
	"testing"
)

// F6: which files read a file as text.

const scanTestGo = `package scan

import (
	"os"
	"strings"
	"testing"
)

// residency.go is only mentioned in a comment here.
func residencyGo() string { return "unrelated" }

func TestScansSource(t *testing.T) {
	b, _ := os.ReadFile("../backend/webgpu/residency.go")
	if strings.Contains(string(b), "x") {
		t.Fail()
	}
}

var byBase = map[string]string{"k": "residency.go"}

func TestNoMatch(t *testing.T) {
	_ = "other/file.go"
}

func TestOtherDirSameBase(t *testing.T) {
	_ = "../gpu/residency.go" // a different file with the same base name
}
`

const embedGo = `package scan

import _ "embed"

//go:embed data/residency.go.tmpl
var tmpl string
`

const residencyGo = `package webgpu

func Resident() {}
`

const pyScan = `def test_scan():
    open("backend/webgpu/residency.go").read()
`

type refRow struct {
	File string
	Line int
	Func string
}

func TestTextRefs(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"go.mod":                      "module example.com/m\n\ngo 1.21\n",
		"backend/webgpu/residency.go": residencyGo,
		"scan/scan_test.go":           scanTestGo,
		"scan/embed.go":               embedGo,
		"scan/data/residency.go.tmpl": "x\n",
		"py/test_scan.py":             pyScan,
	})
	ws, ctx := openBM25(t, dir, false)

	got, err := ws.TextRefs(ctx, "backend/webgpu/residency.go")
	if err != nil {
		t.Fatal(err)
	}
	var rows []refRow
	for _, r := range got {
		rows = append(rows, refRow{r.File, r.Line, r.Func})
	}
	want := []refRow{
		{"py/test_scan.py", 2, "test_scan"},
		{"scan/scan_test.go", 13, "TestScansSource"},
		{"scan/scan_test.go", 19, ""}, // package-level var, no enclosing function
	}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("TextRefs(full path) = %+v, want %+v", rows, want)
	}

	// The base name also matches, and a //go:embed pattern counts.
	got, err = ws.TextRefs(ctx, "scan/data/residency.go.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].File != "scan/embed.go" || got[0].Line != 5 || got[0].Kind != "embed" {
		t.Errorf("TextRefs(tmpl) = %+v, want one embed ref at scan/embed.go:5", got)
	}
}
