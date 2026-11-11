package buildchecks

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// A test that starts one of ken's binaries must do it through
// internal/testenv, which gives the child a temporary HOME and
// KEN_NO_USAGE_STATS=1. Started with the developer's real environment, such
// a test has overwritten ~/.ken/rerank-cache-int8.bin and appended to
// ~/.ken/savings.jsonl on every run.
//
// TestSubprocessTestsUseTestenv parses every _test.go file in the module
// and fails on a direct exec.Command / exec.CommandContext call, unless the
// program is a string literal naming a development tool (directExecTools),
// or the file is listed in directExecFiles with the number of such calls
// and the reason.
var directExecTools = map[string]bool{
	"go":  true, // builds and `go list`; needs the real HOME for its caches
	"git": true,
}

var directExecFiles = map[string]int{
	// Runs tools/build-docs-mcp.sh, which runs `go build`: a development
	// tool, not a ken binary. The server it builds is started through
	// testenv.
	"cmd/ken-mcp-docs/main_test.go": 1,
	// Runs `sh -c "exit 0"` to measure a child's peak RSS.
	"tools/peakrss/main_test.go": 1,
}

func TestSubprocessTestsUseTestenv(t *testing.T) {
	root := repoPath("")
	got := map[string]int{}
	fset := token.NewFileSet()
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
		if !strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		// The local name os/exec is imported under, usually "exec".
		execName := ""
		for _, imp := range f.Imports {
			if imp.Path.Value == `"os/exec"` {
				execName = "exec"
				if imp.Name != nil {
					execName = imp.Name.Name
				}
			}
		}
		if execName == "" {
			return nil
		}
		rel := filepath.ToSlash(strings.TrimPrefix(p, root+string(filepath.Separator)))
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != execName {
				return true
			}
			prog := 0 // index of the program argument
			switch sel.Sel.Name {
			case "Command":
			case "CommandContext":
				prog = 1
			default:
				return true
			}
			if len(call.Args) > prog {
				if lit, ok := call.Args[prog].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if name, err := strconv.Unquote(lit.Value); err == nil && directExecTools[name] {
						return true
					}
				}
			}
			got[rel]++
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("scanning test files: %v", err)
	}
	for file, n := range got {
		want, ok := directExecFiles[file]
		switch {
		case !ok:
			t.Errorf("%s starts a subprocess with os/exec directly (%d calls); use testenv.Command / testenv.CommandContext so it gets a temporary HOME and KEN_NO_USAGE_STATS=1, or list the file in directExecFiles with the reason", file, n)
		case n != want:
			t.Errorf("%s has %d direct os/exec calls, directExecFiles says %d; update the count with the reason", file, n, want)
		}
	}
	for file := range directExecFiles {
		if _, ok := got[file]; !ok {
			t.Errorf("directExecFiles names %s, which has no direct os/exec call; delete the entry", file)
		}
	}
}
