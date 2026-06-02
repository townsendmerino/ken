//go:build ignore

// probe_rust_field_name.go inspects whether gotreesitter resolves
// ChildByFieldName("name") on a Rust function_item the way we expect.
package main

import (
	"fmt"
	"os"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

func main() {
	// Read the actual ripgrep file that's failing.
	srcBytes, err := os.ReadFile("/tmp/ken-dogfood/ripgrep/crates/cli/src/human.rs")
	if err != nil {
		fmt.Println(err)
		return
	}
	src := string(srcBytes)
	_ = `impl std::fmt::Display for ParseSizeError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "x")
    }
}

/// Doc comment
/// continuation
fn strip_from_match_ascii(expr: Hir, byte: u8) -> Result<Hir, Error> {
    Ok(expr)
}
`
	entry := grammars.DetectLanguageByName("rust")
	lang := entry.Language()
	pool := gotreesitter.NewParserPool(lang)
	tree, _ := pool.Parse([]byte(src))
	root := tree.RootNode()

	probeFn(src, root, lang)
}

func probeFn(src string, n *gotreesitter.Node, lang *gotreesitter.Language) {
	for i := 0; i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		if c.Type(lang) != "function_item" {
			probeFn(src, c, lang)
			continue
		}
		fmt.Printf("\n--- function_item at idx %d ---\n", i)
		fmt.Printf("text: %s\n", string(src[c.StartByte():c.EndByte()]))
		nameNode := c.ChildByFieldName("name", lang)
		fmt.Printf("ChildByFieldName(\"name\"): %v\n", nameNode)
		if nameNode != nil {
			fmt.Printf("  text: %s\n", string(src[nameNode.StartByte():nameNode.EndByte()]))
		}
		fmt.Printf("FieldNameForChild for each named child:\n")
		for j := 0; j < c.NamedChildCount(); j++ {
			cc := c.NamedChild(j)
			if cc == nil {
				continue
			}
			field := c.FieldNameForChild(j, lang)
			fmt.Printf("  [%d] type=%-30s field=%-15q text=%.40s\n", j, cc.Type(lang), field, string(src[cc.StartByte():cc.EndByte()]))
		}
	}
}
