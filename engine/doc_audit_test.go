package engine_test

import (
	"go/ast"
	"go/doc"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestPublicSurfaceDocumented asserts every exported const/var/func/type/
// method in ken/engine carries a doc comment — the same audit
// mcp/doc_audit_test.go runs over mcp + mcp/db (roadmap #28). engine is
// ken's newest 1.0-stable public surface (ADR-046), so it gets the same
// bar from day one rather than accumulating undocumented exports first.
func TestPublicSurfaceDocumented(t *testing.T) {
	fset := token.NewFileSet()
	goFiles, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	var files []*ast.File
	for _, gf := range goFiles {
		if strings.HasSuffix(gf, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, gf, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", gf, err)
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		t.Fatal("no non-test .go files found in engine/")
	}
	dp, err := doc.NewFromFiles(fset, files, "github.com/townsendmerino/ken/engine")
	if err != nil {
		t.Fatalf("doc: %v", err)
	}
	check := func(kind, name, d string) {
		if strings.TrimSpace(d) == "" {
			t.Errorf("engine: exported %s %q has no doc comment (shows blank on pkg.go.dev)", kind, name)
		}
	}
	for _, c := range dp.Consts {
		check("const", strings.Join(c.Names, ","), c.Doc)
	}
	for _, v := range dp.Vars {
		check("var", strings.Join(v.Names, ","), v.Doc)
	}
	for _, f := range dp.Funcs {
		check("func", f.Name, f.Doc)
	}
	for _, tp := range dp.Types {
		check("type", tp.Name, tp.Doc)
		for _, c := range tp.Consts {
			check("const", strings.Join(c.Names, ","), c.Doc)
		}
		for _, v := range tp.Vars {
			check("var", strings.Join(v.Names, ","), v.Doc)
		}
		for _, f := range tp.Funcs {
			check("func", f.Name, f.Doc)
		}
		for _, m := range tp.Methods {
			check("method", tp.Name+"."+m.Name, m.Doc)
		}
	}
}
