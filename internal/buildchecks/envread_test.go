package buildchecks

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Environment reads stay at the edge.
//
// ken's library packages don't read the process environment. A program's
// edge reads the KEN_* variables and passes the values down as options, so
// an in-process caller (tack importing ken/engine) gets exactly the
// configuration it passes, and nothing changes behind it when a variable
// happens to be set. TestEnvReadsStayAtTheEdge fails on any os.Getenv,
// os.LookupEnv or os.ExpandEnv reference in a non-test file outside the
// edges, naming the file and line.
//
// The edges:
//
//   - every `package main` (cmd/*, tools/*, demos/*, scripts, the harness
//     binaries): a program is its own edge;
//   - internal/envcfg, the shared readers and their parsing;
//   - mcp, the server library. A third-party mcp.Run or mcp.NewServer
//     binary honors the documented variables without wiring each one, so
//     the package reads them itself (mcp/env.go lists which);
//   - bench/, the benchmark harnesses (//go:build bench, never linked into
//     a released binary), whose runs are configured by environment.
//
// os.Environ is not flagged: passing the whole environment to a child
// process is not reading configuration from it.
var envEdgeDirs = []string{
	"internal/envcfg",
	"mcp",
}

// envEdgeTrees are edges that include every package beneath them.
var envEdgeTrees = []string{
	"bench",
}

var envReadFuncs = map[string]bool{"Getenv": true, "LookupEnv": true, "ExpandEnv": true}

func isEnvEdge(dir, pkgName string) bool {
	if pkgName == "main" {
		return true
	}
	for _, d := range envEdgeDirs {
		if dir == d {
			return true
		}
	}
	for _, d := range envEdgeTrees {
		if dir == d || strings.HasPrefix(dir, d+"/") {
			return true
		}
	}
	return false
}

// envReadsIn returns "file:line: os.Func" for each reference to an
// env-reading function of package os in the parsed file.
func envReadsIn(fset *token.FileSet, f *ast.File) []string {
	osNames := map[string]bool{}
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || path != "os" {
			continue
		}
		name := "os"
		if imp.Name != nil {
			name = imp.Name.Name
		}
		osNames[name] = true
	}
	if len(osNames) == 0 {
		return nil
	}
	var found []string
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if x, ok := sel.X.(*ast.Ident); ok && osNames[x.Name] && envReadFuncs[sel.Sel.Name] {
			pos := fset.Position(sel.Pos())
			found = append(found, fmt.Sprintf("%s:%d: os.%s", pos.Filename, pos.Line, sel.Sel.Name))
		}
		return true
	})
	return found
}

func TestEnvReadsStayAtTheEdge(t *testing.T) {
	root := repoPath("")
	fset := token.NewFileSet()
	var violations []string
	files := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if p != root && (name == "testdata" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if err != nil {
			return fmt.Errorf("parse %s: %w", p, err)
		}
		files++
		rel, err := filepath.Rel(root, filepath.Dir(p))
		if err != nil {
			return err
		}
		if isEnvEdge(filepath.ToSlash(rel), f.Name.Name) {
			return nil
		}
		violations = append(violations, envReadsIn(fset, f)...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files < 100 {
		t.Fatalf("parsed only %d Go files under %s; the walk is not seeing the module", files, root)
	}
	sort.Strings(violations)
	for _, v := range violations {
		t.Errorf("%s: a library package reads the environment. Read the variable at the edge (cmd/, internal/envcfg, mcp) and pass the value down as an option", v)
	}
}
