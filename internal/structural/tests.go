package structural

// tests.go — TestsFor and Callees (K15): the test functions that exercise
// a symbol, and the calls a symbol makes. Both are built from the same
// per-file facts as Callers and Outline — CallRefs, FuncDef spans — so
// they cost in proportion to the matches, not the corpus.

import (
	"path"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// TestSite is one test function and where to run it.
type TestSite struct {
	File      string // "internal/serveapp/banner_test.go"
	QName     string // "TestBanner_contextAndKV"; for a Python method, "TestK.test_y"
	StartLine int
	EndLine   int
	Dir       string // the test file's directory, "." at the root: for Go, the package directory
	Depth     int    // 1: calls the symbol; 2: calls Via, which calls the symbol
	Via       string // Depth 2 only: the intermediate function's qualified name
}

// Callee is one call made inside a symbol.
type Callee struct {
	File     string // the file of the symbol's definition the call is in
	Name     string // the callee's leaf name, as CallRef.Callee
	Receiver string // the receiver text ("pkg", "s.store"); "" for a bare call
	Line     int
}

// Depth-2 caps. Depth 2 matches the intermediate function by its leaf name,
// so a common one (Run, Get, New) would fan out across the corpus; these
// bound the work, and a missing Depth-2 result is better than a slow
// answer.
const (
	// testsForMaxIntermediates is how many distinct intermediate functions
	// Depth 2 follows; the rest are skipped.
	testsForMaxIntermediates = 64
	// testsForMaxCallerFiles skips an intermediate whose leaf name is
	// called from more files than this.
	testsForMaxCallerFiles = 200
	// testsForMaxResults caps the whole result (Depth 1 included).
	testsForMaxResults = 500
)

// isTestFile reports whether rel is a test file under the conventions
// TestsFor knows: Go `*_test.go`; Python `test_*.py` and `*_test.py`.
func isTestFile(rel string) bool {
	base := path.Base(rel)
	switch {
	case strings.HasSuffix(base, "_test.go"):
		return true
	case strings.HasSuffix(base, ".py"):
		return strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py")
	}
	return false
}

// isTestFunc reports whether fn, in test file rel, is a test function:
// Go top-level Test*/Benchmark*/Fuzz*/Example* (go test's rule: the prefix
// alone, or followed by a non-lowercase letter); Python test_* functions,
// and test_* methods of Test* classes.
func isTestFunc(rel string, fn FuncDef) bool {
	if strings.HasSuffix(rel, ".go") {
		if fn.IsMethod {
			return false
		}
		for _, p := range []string{"Test", "Benchmark", "Fuzz", "Example"} {
			if rest, ok := strings.CutPrefix(fn.Name, p); ok {
				if rest == "" {
					return true
				}
				r, _ := utf8.DecodeRuneInString(rest)
				return !unicode.IsLower(r)
			}
		}
		return false
	}
	if !strings.HasPrefix(fn.Name, "test_") {
		return false
	}
	return !fn.IsMethod || strings.HasPrefix(fn.EnclosingClass, "Test")
}

// testsAt returns the test functions in fs whose spans contain line.
func testsAt(rel string, fs *FileStruct, line int) []FuncDef {
	var out []FuncDef
	for _, fn := range fs.Functions {
		if fn.StartLine > 0 && line >= fn.StartLine && line <= fn.EndLine && isTestFunc(rel, fn) {
			out = append(out, fn)
		}
	}
	return out
}

// TestsFor returns the test functions that call symbol: directly (Depth
// 1), or, when maxDepth >= 2, through one non-test function (Depth 2).
// Sorted by Depth, then File, then StartLine; a test appears once, at its
// lowest depth.
//
// Matching is by name, as in Callers, with its qualifier narrowing for Go
// package-qualified names (pull.Select). A call counts for the test whose
// span contains it, so a call inside a closure (a t.Run subtest) counts for
// the enclosing test. A name with several definitions gives the tests of
// all of them. Depth 2 finds a non-test function f whose span contains a
// call to symbol (f may live in a test file: a helper), then the tests
// that call f by its leaf name — so a common name fans out, bounded by the
// testsForMax* caps. Test conventions: Go *_test.go files and top-level
// Test/Benchmark/Fuzz/Example functions; Python test_*.py and *_test.py
// files, test_* functions, and test_* methods of Test* classes. Other
// languages give an empty result.
func (ix *Index) TestsFor(symbol string, maxDepth int) []TestSite {
	var out []TestSite
	seen := make(map[[2]string]bool) // (file, test qname)
	add := func(file string, fn FuncDef, depth int, via string) {
		q := qualifySymbol(fn.EnclosingClass, fn.Name)
		k := [2]string{file, q}
		if seen[k] || len(out) >= testsForMaxResults {
			return
		}
		seen[k] = true
		out = append(out, TestSite{
			File: file, QName: q, StartLine: fn.StartLine, EndLine: fn.EndLine,
			Dir: path.Dir(file), Depth: depth, Via: via,
		})
	}

	sites := ix.Callers(symbol)
	for _, cs := range sites {
		fs := ix.files[cs.File]
		if fs == nil || !isTestFile(cs.File) {
			continue
		}
		for _, c := range cs.Calls {
			for _, fn := range testsAt(cs.File, fs, c.Line) {
				add(cs.File, fn, 1, "")
			}
		}
	}

	if maxDepth >= 2 {
		type via struct {
			leaf, qname string
		}
		var vias []via
		seenVia := make(map[string]bool)
		for _, cs := range sites {
			fs := ix.files[cs.File]
			if fs == nil {
				continue
			}
			for _, c := range cs.Calls {
				syms, _ := ix.SymbolsAt(cs.File, c.Line)
				for _, s := range syms {
					if s.Kind == DefinitionKindClass {
						continue
					}
					// The innermost named function holding the call.
					fn := FuncDef{Name: s.Name, IsMethod: s.Kind == DefinitionKindMethod}
					if fn.IsMethod {
						fn.EnclosingClass = strings.TrimSuffix(s.QName, "."+s.Name)
					}
					if isTestFile(cs.File) && isTestFunc(cs.File, fn) {
						break // a test: Depth 1 has it
					}
					if !seenVia[s.QName] && len(vias) < testsForMaxIntermediates {
						seenVia[s.QName] = true
						vias = append(vias, via{s.Name, s.QName})
					}
					break
				}
			}
		}
		for _, v := range vias {
			files := ix.callers[v.leaf]
			if len(files) > testsForMaxCallerFiles {
				continue
			}
			for _, c := range files {
				if !isTestFile(c.File) {
					continue
				}
				fs := ix.files[c.File]
				for _, call := range callsTo(fs, v.leaf, nil) {
					for _, fn := range testsAt(c.File, fs, call.Line) {
						add(c.File, fn, 2, v.qname)
					}
				}
			}
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Depth != b.Depth {
			return a.Depth < b.Depth
		}
		if a.File != b.File {
			return a.File < b.File
		}
		return a.StartLine < b.StartLine
	})
	return out
}

// Callees returns the calls made inside symbol's span, in line order, for
// every function or method definition the name resolves to (as Definition
// resolves it, qualified forms included), sorted by file then line. A call
// inside a closure or a nested function is inside the span, so it counts.
// Calls the extractors drop as noise (Go builtins and fmt's Errorf/Sprintf
// family; Python builtins) aren't recorded, so they don't appear. Classes
// have no callees here.
func (ix *Index) Callees(symbol string) []Callee {
	var out []Callee
	type spanKey struct {
		file       string
		start, end int
	}
	done := make(map[spanKey]bool) // one pass per span (Dart records a method twice)
	for _, d := range ix.funcDefs(symbol) {
		k := spanKey{d.file, d.fn.StartLine, d.fn.EndLine}
		if done[k] {
			continue
		}
		done[k] = true
		fs := ix.files[d.file]
		for _, r := range fs.CallRefs {
			if r.Line >= d.fn.StartLine && r.Line <= d.fn.EndLine {
				out = append(out, Callee{File: d.file, Name: r.Callee, Receiver: r.Receiver, Line: r.Line})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out
}

type fileFunc struct {
	file string
	fn   FuncDef
}

// funcDefs resolves symbol to the function and method definitions it
// names, with their spans: a bare name gives every function and method of
// that name; a dotted name gives what Definition resolves it to.
func (ix *Index) funcDefs(symbol string) []fileFunc {
	var out []fileFunc
	if indexOfDot(symbol) < 0 {
		files := uniqStringsSorted(append(append([]string(nil), ix.defs[symbol]...), ix.methods[symbol]...))
		for _, f := range files {
			for _, fn := range ix.files[f].Functions {
				if fn.Name == symbol && fn.StartLine > 0 {
					out = append(out, fileFunc{f, fn})
				}
			}
		}
		return out
	}
	leaf := symbol[strings.LastIndexByte(symbol, '.')+1:]
	for _, s := range ix.qualifiedDefinition(symbol) {
		if s.Kind == DefinitionKindClass {
			continue
		}
		for _, fn := range ix.files[s.File].Functions {
			if fn.Name == leaf && fn.StartLine == s.Line && fn.StartLine > 0 {
				out = append(out, fileFunc{s.File, fn})
				break
			}
		}
	}
	return out
}
