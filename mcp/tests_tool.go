package mcp

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/townsendmerino/ken/engine"
)

// TestsForArgs is the argument schema for tests_for.
type TestsForArgs struct {
	Symbol string `json:"symbol" jsonschema:"The function or method whose tests you want (exact name). 'Type.Method' matches every call to Method (receiver types aren't resolved); for Go, 'pkg.Func' narrows to calls written pkg.Func(...) plus bare Func(...) inside package pkg."`
	repoArg
	Depth  int    `json:"depth,omitempty" jsonschema:"1: tests that call the symbol. 2 (default): also tests that call a function that calls it."`
	Output string `json:"output,omitempty" jsonschema:"Output format: 'markdown' (default) or 'json'."`
}

// TestsForResponse is the JSON reply of tests_for: the tests that call a symbol, and the commands that run them.
type TestsForResponse struct {
	Symbol   string    `json:"symbol"`
	Tests    []TestOut `json:"tests"`
	Commands []string  `json:"commands"`
}

// TestOut is one test that calls the symbol.
type TestOut struct {
	Name  string `json:"name"`
	File  string `json:"file"`
	Line  int    `json:"line"`
	Dir   string `json:"dir"`
	Depth int    `json:"depth"`         // 1: calls the symbol; 2: calls Via, which calls it
	Via   string `json:"via,omitempty"` // depth 2: the function in between
}

func handleTestsFor(ctx context.Context, cfg *Config, args TestsForArgs) (*sdk.CallToolResult, any, error) {
	sidx, errResult := resolveStructuralIndex(ctx, cfg, args.Repo, args.Output)
	if errResult != nil {
		return errResult, nil, nil
	}
	sym := strings.TrimSpace(args.Symbol)
	if sym == "" {
		return errorResult(args.Output, "symbol is required"), nil, nil
	}
	depth := args.Depth
	if depth != 1 {
		depth = 2
	}
	sites, _ := sidx.ws.TestsFor(ctx, sym, depth)
	resp := TestsForResponse{Symbol: sym, Tests: make([]TestOut, 0, len(sites)), Commands: testCommands(sites)}
	for _, s := range sites {
		resp.Tests = append(resp.Tests, TestOut{Name: s.QName, File: s.File, Line: s.StartLine, Dir: s.Dir, Depth: s.Depth, Via: s.Via})
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Tests for `%s`\n\n", sym)
	if len(sites) == 0 {
		fmt.Fprintf(&b, "No test calls %q by name, directly or through one function. Matching is by name in Go and Python test files (name-resolved, not type-resolved); a test that reaches it through an interface or more than one call away is not found.\n", sym)
		return dispatchOutput(args.Output, resp, b.String())
	}
	for _, t := range resp.Tests {
		fmt.Fprintf(&b, "- `%s` %s:%d", t.Name, t.File, t.Line)
		if t.Via != "" {
			fmt.Fprintf(&b, " (through %s)", t.Via)
		}
		b.WriteByte('\n')
	}
	b.WriteString("\nRun them:\n\n")
	for _, c := range resp.Commands {
		b.WriteString("    " + c + "\n")
	}
	b.WriteString("\nMatched by name, not by type: a same-named function elsewhere brings its tests too.\n")
	return dispatchOutput(args.Output, resp, b.String())
}

// testCommands returns the commands that run tests: one `go test -run` per Go package directory, and one pytest
// line for the Python tests. A Go benchmark is not run by -run and is left out.
func testCommands(sites []engine.TestSite) []string {
	goTests := map[string][]string{}
	var goDirs, pyIDs []string
	for _, s := range sites {
		switch {
		case strings.HasSuffix(s.File, ".go"):
			if strings.HasPrefix(s.QName, "Benchmark") {
				continue
			}
			if goTests[s.Dir] == nil {
				goDirs = append(goDirs, s.Dir)
			}
			goTests[s.Dir] = append(goTests[s.Dir], s.QName)
		case strings.HasSuffix(s.File, ".py"):
			pyIDs = append(pyIDs, s.File+"::"+strings.ReplaceAll(s.QName, ".", "::"))
		}
	}
	sort.Strings(goDirs)
	var out []string
	for _, dir := range goDirs {
		names := goTests[dir]
		sort.Strings(names)
		pkg := "./" + dir
		if dir == "." || dir == "" {
			pkg = "."
		}
		out = append(out, fmt.Sprintf("go test -run '^(%s)$' %s", strings.Join(names, "|"), pkg))
	}
	if len(pyIDs) > 0 {
		sort.Strings(pyIDs)
		out = append(out, "pytest "+strings.Join(pyIDs, " "))
	}
	return out
}

// EditTests is what an edit check adds about tests: the functions the edit changes, by the structural index, and
// the commands that run the tests calling them. It is there for the server's default repository, whose index the
// server already holds, and for an edit given as old_string and new_string (a whole-file edit names no function).
type EditTests struct {
	// Symbols are the functions and methods the edit's old_string lies in.
	Symbols []string `json:"symbols"`
	// Commands run the tests that call them, directly or through one function. Matched by name.
	Commands []string `json:"commands,omitempty"`
	// Tests is how many tests those commands run; Uncovered are the symbols no test calls by name.
	Tests     int      `json:"tests"`
	Uncovered []string `json:"uncovered,omitempty"`
}

// editSpot is one replacement of an edit: the file, relative to the repository, and the text it replaces.
type editSpot struct{ file, oldString string }

// maxEditTestSymbols and maxEditTests bound what a check says about tests: it is a pointer to what to run, not a
// report.
const (
	maxEditTestSymbols = 8
	maxEditTests       = 40
)

// testsForEdit finds the functions an edit changes and the tests that call them. nil when there is nothing to say:
// another repository than the default, no structural index, or no replacement inside a function.
func testsForEdit(ctx context.Context, cfg *Config, repoArg, root string, spots []editSpot) *EditTests {
	if repoArg != "" || cfg.Cache == nil || len(spots) == 0 {
		return nil
	}
	sidx, errResult := resolveStructuralIndex(ctx, cfg, "", "")
	if errResult != nil {
		return nil
	}
	var syms []string
	seen := map[string]bool{}
	for _, sp := range spots {
		if sp.oldString == "" {
			continue
		}
		rel := filepath.ToSlash(sp.file)
		abs := sp.file
		if filepath.IsAbs(abs) {
			if r, err := filepath.Rel(root, abs); err == nil {
				rel = filepath.ToSlash(r)
			}
		} else {
			abs = filepath.Join(root, filepath.FromSlash(sp.file))
		}
		src, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		at := strings.Index(string(src), sp.oldString)
		if at < 0 {
			continue
		}
		first := strings.Count(string(src[:at]), "\n") + 1
		last := first + strings.Count(strings.TrimSuffix(sp.oldString, "\n"), "\n")
		for line := first; line <= last; line++ {
			spans, err := sidx.ws.SymbolAt(ctx, rel, line)
			if err != nil || len(spans) == 0 {
				continue
			}
			name := spans[0].QName // the innermost
			// a Go function is narrowed by its package, which Go names after its directory more often than not
			if strings.HasSuffix(rel, ".go") && !strings.Contains(name, ".") {
				if dir := path.Base(path.Dir(rel)); dir != "." && dir != "/" {
					name = dir + "." + name
				}
			}
			if !seen[name] && len(syms) < maxEditTestSymbols {
				seen[name] = true
				syms = append(syms, name)
			}
		}
	}
	if len(syms) == 0 {
		return nil
	}
	out := &EditTests{Symbols: syms}
	var all []engine.TestSite
	have := map[string]bool{}
	for _, sym := range syms {
		sites, _ := sidx.ws.TestsFor(ctx, sym, 2)
		if len(sites) == 0 {
			out.Uncovered = append(out.Uncovered, sym)
		}
		for _, s := range sites {
			if k := s.File + "\x00" + s.QName; !have[k] && len(all) < maxEditTests {
				have[k] = true
				all = append(all, s)
			}
		}
	}
	out.Tests, out.Commands = len(all), testCommands(all)
	return out
}

// text renders what a check says about tests, after its verdict.
func (t *EditTests) text() string {
	if t == nil {
		return ""
	}
	var b strings.Builder
	if len(t.Commands) > 0 {
		fmt.Fprintf(&b, "tests that call what the edit changes (%s; matched by name, so a same-named function brings its tests too):\n", strings.Join(t.Symbols, ", "))
		for _, c := range t.Commands {
			b.WriteString("  " + c + "\n")
		}
	}
	if len(t.Uncovered) > 0 {
		fmt.Fprintf(&b, "no test calls %s by name, directly or through one function\n", strings.Join(t.Uncovered, ", "))
	}
	return b.String()
}
