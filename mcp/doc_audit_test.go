package mcp_test

import (
	"go/ast"
	"go/doc"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// TestPublicSurfaceDocumented asserts that every exported symbol in ken's
// public MCP packages (mcp + mcp/db) carries a doc comment. pkg.go.dev
// completeness is part of the SDK-author pitch (DEVELOPERS.md describes this
// surface as 1.0-stable), so an undocumented export is a regression. Roadmap
// #28. Runs from the mcp/ package dir, so "." is mcp and "db" is mcp/db.
func TestPublicSurfaceDocumented(t *testing.T) {
	for _, dir := range []string{".", "db"} {
		fset := token.NewFileSet()
		pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
			return !strings.HasSuffix(fi.Name(), "_test.go")
		}, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", dir, err)
		}
		for _, pkg := range pkgs {
			var files []*ast.File
			for _, f := range pkg.Files {
				files = append(files, f)
			}
			dp, err := doc.NewFromFiles(fset, files, "x/"+dir)
			if err != nil {
				t.Fatalf("doc %s: %v", dir, err)
			}
			check := func(kind, name, d string) {
				if strings.TrimSpace(d) == "" {
					t.Errorf("%s: exported %s %q has no doc comment (shows blank on pkg.go.dev)", pkg.Name, kind, name)
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
}
