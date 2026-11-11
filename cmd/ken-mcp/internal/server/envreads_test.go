package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// envReaders are the functions of this package that read the environment:
// the ones main calls before Run (NewLogger, SetupGCHygiene,
// SetupEnrichBudget) and LoadConfig with its helpers. Run and everything it
// calls take their settings from Config.
var envReaders = map[string]bool{
	"NewLogger":              true,
	"SetupGCHygiene":         true,
	"SetupEnrichBudget":      true,
	"LoadConfig":             true,
	"resolveTransport":       true,
	"resolveAuthToken":       true,
	"setupReranker":          true,
	"parseRerankAdaptive":    true,
	"resolveRerankCachePath": true,
	"loadDBConfig":           true,
	"loadIndexOptions":       true,
}

// TestOnlyConfigLoadersReadTheEnvironment fails when a function outside
// envReaders reads a variable, so a new setting is added to Config and read
// in LoadConfig, where its invalid-value warning is logged at startup.
func TestOnlyConfigLoadersReadTheEnvironment(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	seen := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, ok := sel.X.(*ast.Ident)
				if !ok {
					return true
				}
				reads := (pkg.Name == "os" && (sel.Sel.Name == "Getenv" || sel.Sel.Name == "LookupEnv")) ||
					pkg.Name == "envcfg" // every envcfg function reads or parses a variable
				if !reads {
					return true
				}
				seen[fn.Name.Name] = true
				if !envReaders[fn.Name.Name] {
					t.Errorf("%s: %s reads the environment (%s.%s); read it in LoadConfig and pass it through Config",
						fset.Position(sel.Pos()), fn.Name.Name, pkg.Name, sel.Sel.Name)
				}
				return true
			})
		}
	}
	for name := range envReaders {
		if !seen[name] {
			t.Errorf("envReaders names %s, which no longer reads the environment; delete the entry", name)
		}
	}
}
