package engine_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/townsendmerino/ken/engine"
)

// K12: the symbol at a line.

const symGo = `package p

import "fmt"

// F does things.
func F() {
	fmt.Println(1)
	g := func() {
		_ = 1
	}
	g()
}

type T struct {
	a int
}

func (t T) M() int {
	return t.a
}
`

// symGoWant is the QName chain SymbolAt must give for each line of symGo.
var symGoWant = [][]string{
	1: nil, 2: nil, 3: nil, 4: nil,
	5: nil, // the doc comment is outside F's span
	6: {"F"}, 7: {"F"}, 8: {"F"}, 9: {"F"}, 10: {"F"}, 11: {"F"}, 12: {"F"},
	13: nil,
	14: {"T"}, 15: {"T"}, 16: {"T"},
	17: nil,
	18: {"T.M"}, 19: {"T.M"}, 20: {"T.M"}, // not nested in T's span
	21: nil, 22: nil, // past the end
}

const symPy = `import os

def top(x):
    return x

class K:
    """doc"""
    def m(self):
        return 1

    def n(self):
        return 2

X = 1
`

var symPyWant = [][]string{
	1: nil, 2: nil,
	3: {"top"}, 4: {"top"},
	5: nil,
	6: {"K"}, 7: {"K"},
	8: {"K.m", "K"}, 9: {"K.m", "K"},
	10: {"K"},
	11: {"K.n", "K"}, 12: {"K.n", "K"},
	13: nil, 14: nil, 15: nil,
}

func TestSymbolAt(t *testing.T) {
	dir := t.TempDir()
	for rel, body := range map[string]string{"p/p.go": symGo, "m/mod.py": symPy, "notes.txt": "notes\n"} {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ws, ctx := openBM25(t, dir, false)

	for file, want := range map[string][][]string{"p/p.go": symGoWant, "m/mod.py": symPyWant} {
		for line := 1; line < len(want); line++ {
			syms, err := ws.SymbolAt(ctx, file, line)
			if err != nil {
				t.Fatalf("SymbolAt(%s, %d): %v", file, line, err)
			}
			var got []string
			for _, s := range syms {
				got = append(got, s.QName)
				if line < s.StartLine || line > s.EndLine {
					t.Errorf("SymbolAt(%s, %d) gave %s spanning %d-%d", file, line, s.QName, s.StartLine, s.EndLine)
				}
			}
			if !reflect.DeepEqual(got, want[line]) {
				t.Errorf("SymbolAt(%s, %d) = %v, want %v", file, line, got, want[line])
			}
		}
	}

	// Kinds and names on a method.
	syms, _ := ws.SymbolAt(ctx, "m/mod.py", 9)
	if len(syms) != 2 || syms[0].Name != "m" || syms[0].Kind != engine.DefinitionKindMethod || syms[1].Kind != engine.DefinitionKindClass {
		t.Errorf("SymbolAt(mod.py, 9) = %+v, want method m then class K", syms)
	}

	for _, p := range []string{"notes.txt", "nope.go", "p/missing.py"} {
		if _, err := ws.SymbolAt(ctx, p, 1); !errors.Is(err, engine.ErrFileNotIndexed) {
			t.Errorf("SymbolAt(%s) = %v, want ErrFileNotIndexed", p, err)
		}
	}

	// Every Outline entry with a span is found at its first and last line.
	for _, file := range []string{"p/p.go", "m/mod.py"} {
		outs, err := ws.Outline(ctx, file)
		if err != nil || len(outs) != 1 {
			t.Fatalf("Outline(%s) = %v, %v", file, outs, err)
		}
		for _, e := range outs[0].Entries {
			if e.StartLine == 0 {
				continue
			}
			for _, line := range []int{e.StartLine, e.EndLine} {
				syms, err := ws.SymbolAt(ctx, file, line)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, s := range syms {
					if s.Name == e.Name && s.StartLine == e.StartLine && s.EndLine == e.EndLine {
						found = true
					}
				}
				if !found {
					t.Errorf("SymbolAt(%s, %d) = %+v, missing outline entry %s %d-%d", file, line, syms, e.Name, e.StartLine, e.EndLine)
				}
			}
		}
	}
	if !strings.HasSuffix(symGo, "}\n") || strings.Count(symGo, "\n") != 20 {
		t.Fatalf("symGo should be 20 lines; symGoWant assumes it")
	}
}
