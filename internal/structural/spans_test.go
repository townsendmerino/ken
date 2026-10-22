package structural

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
)

// TestMain installs extractCheck so every file any test in this package
// extracts — the extract_<lang>_test.go fixtures for all 15 enrichment
// languages among them — has its spans checked (K11): each recorded span
// lies within its file, starts no later than it ends, and its text holds
// the declaration's name; DocLine (Go) is at or above the start. A full
// run (no -run filter) also fails if any of the 15 languages went
// unchecked, so a language whose fixture stops reaching extractGuarded
// can't silently drop out of the net.
func TestMain(m *testing.M) {
	var (
		mu       sync.Mutex
		problems []string
		seen     = map[string]int{}
	)
	extractCheck = func(gram string, data []byte, fs *FileStruct) {
		bad := spanProblems(data, fs)
		mu.Lock()
		defer mu.Unlock()
		seen[gram]++
		for _, p := range bad {
			problems = append(problems, gram+" "+fs.Path+": "+p)
		}
	}
	code := m.Run()

	if len(problems) > 0 {
		sort.Strings(problems)
		fmt.Fprintf(os.Stderr, "FAIL: %d span problems across the extractor fixtures:\n  %s\n", len(problems), strings.Join(problems, "\n  "))
		code = 1
	}
	if f := flag.Lookup("test.run"); code == 0 && (f == nil || f.Value.String() == "") {
		var missing []string
		for _, g := range []string{"python", "go", "typescript", "javascript", "java", "rust", "cpp", "c",
			"php", "ruby", "kotlin", "dart", "swift", "scala", "elixir"} {
			if seen[g] == 0 {
				missing = append(missing, g)
			}
		}
		if len(missing) > 0 {
			fmt.Fprintf(os.Stderr, "FAIL: no test extracted a file in %v, so their spans went unchecked\n", missing)
			code = 1
		}
	}
	os.Exit(code)
}

// spanProblems checks every recorded span in fs against the source.
func spanProblems(data []byte, fs *FileStruct) []string {
	lines := strings.SplitAfter(string(data), "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	var out []string
	check := func(what, name string, s span) {
		if what == "func" && name == "" {
			what = "constructor"
		}
		if s.start == 0 && s.end == 0 {
			return // not recorded (a Go shell, or a ClassDef.Methods copy)
		}
		if s.start < 1 || s.start > s.end || s.end > len(lines) {
			out = append(out, fmt.Sprintf("%s %q span %d-%d outside 1..%d", what, name, s.start, s.end, len(lines)))
			return
		}
		if s.doc != 0 && s.doc > s.start {
			out = append(out, fmt.Sprintf("%s %q DocLine %d below its start %d", what, name, s.doc, s.start))
		}
		if name != "" && !strings.Contains(strings.Join(lines[s.start-1:s.end], ""), name) {
			out = append(out, fmt.Sprintf("%s %q span %d-%d doesn't contain its name", what, name, s.start, s.end))
		}
	}
	for _, fn := range fs.Functions {
		name := fn.Name
		if fn.IsMethod && fn.Name == fn.EnclosingClass {
			// A constructor recorded under its class's name: Swift's
			// `init` carries no name in its own span.
			name = ""
		}
		check("func", name, funcSpan(fn))
	}
	for _, c := range fs.Classes {
		check("class", c.Name, classSpan(c))
		for _, m := range c.Methods {
			check("method "+c.Name+".", m.Name, funcSpan(m))
		}
	}
	return out
}
