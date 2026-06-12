package mcp_test

import (
	"go/ast"
	"go/doc"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestPublicSurfaceDocumented asserts that every exported symbol in ken's
// public MCP packages (mcp + mcp/db) carries a doc comment. pkg.go.dev
// completeness is part of the SDK-author pitch (DEVELOPERS.md describes this
// surface as 1.0-stable), so an undocumented export is a regression. Roadmap
// #28. Runs from the mcp/ package dir, so "." is mcp and "db" is mcp/db.
//
// Parses files individually via parser.ParseFile rather than the deprecated
// parser.ParseDir (SA1019); each dir is a single non-test package once
// _test.go files are filtered, so collecting the files by hand is equivalent.
func TestPublicSurfaceDocumented(t *testing.T) {
	for _, dir := range []string{".", "db"} {
		fset := token.NewFileSet()
		goFiles, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatalf("glob %s: %v", dir, err)
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
			t.Fatalf("no non-test .go files found in %s", dir)
		}
		dp, err := doc.NewFromFiles(fset, files, "x/"+dir)
		if err != nil {
			t.Fatalf("doc %s: %v", dir, err)
		}
		check := func(kind, name, d string) {
			if strings.TrimSpace(d) == "" {
				t.Errorf("%s: exported %s %q has no doc comment (shows blank on pkg.go.dev)", dp.Name, kind, name)
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
}
