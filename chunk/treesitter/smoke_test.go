package treesitter

import (
	"testing"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// TestSmoke_GoParse confirms gotreesitter is wired up correctly: the
// "go" grammar resolves via DetectLanguageByName, NewParser returns a
// working parser, and the root node exposes named children with byte
// offsets. If this fails, the chunker is unbuildable regardless of any
// algorithm bugs.
func TestSmoke_GoParse(t *testing.T) {
	src := []byte(`package demo

func Add(a, b int) int { return a + b }
func Sub(a, b int) int { return a - b }

type Pair struct { X, Y int }
`)
	entry := grammars.DetectLanguageByName("go")
	if entry == nil {
		t.Fatal("gotreesitter has no \"go\" grammar")
	}
	lang := entry.Language()
	tree, err := gotreesitter.NewParser(lang).Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	root := tree.RootNode()
	if got := root.NamedChildCount(); got < 3 {
		t.Errorf("root named children = %d, want ≥3 (package + 2 funcs + type)", got)
	}
	// Each top-level child should have a non-empty byte range inside src.
	for i := range root.NamedChildCount() {
		c := root.NamedChild(i)
		if c == nil {
			t.Errorf("named child %d is nil", i)
			continue
		}
		s, e := c.StartByte(), c.EndByte()
		if e <= s || int(e) > len(src) {
			t.Errorf("child %d (%s) bad range [%d, %d) src len=%d",
				i, c.Type(lang), s, e, len(src))
		}
	}
}
