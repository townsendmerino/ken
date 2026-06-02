//go:build ignore

// probe_rust_empty_names.go finds the function_item / function_signature_item
// nodes in a Rust corpus whose `name` field is empty — that's the
// "extraction bug — investigate" signal from dogfood_languages.go.
//
// Usage: KEN_DOGFOOD_DIR=/tmp/ken-dogfood go run scripts/probe_rust_empty_names.go
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

func main() {
	root := os.Getenv("KEN_DOGFOOD_DIR")
	if root == "" {
		root = "/tmp/ken-dogfood"
	}
	target := filepath.Join(root, "ripgrep", "crates")

	entry := grammars.DetectLanguageByName("rust")
	if entry == nil {
		fmt.Fprintln(os.Stderr, "no rust grammar")
		os.Exit(1)
	}
	lang := entry.Language()
	pool := gotreesitter.NewParserPool(lang)

	found := 0
	filepath.WalkDir(target, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".rs") {
			return nil
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		tree, perr := pool.Parse(src)
		if perr != nil {
			return nil
		}
		walk(src, tree.RootNode(), lang, path, &found)
		return nil
	})
	fmt.Printf("found %d empty-Name rust fns\n", found)
}

func walk(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, path string, found *int) {
	if n == nil {
		return
	}
	t := n.Type(lang)
	if t == "function_item" || t == "function_signature_item" {
		name := n.ChildByFieldName("name", lang)
		nameText := ""
		if name != nil {
			nameText = strings.TrimSpace(string(src[name.StartByte():name.EndByte()]))
		}
		if nameText == "" {
			// Print the file + a snippet
			text := string(src[n.StartByte():n.EndByte()])
			if len(text) > 200 {
				text = text[:200] + "..."
			}
			fmt.Printf("\n--- %s (type=%s) ---\n%s\n", path, t, text)
			// Dump immediate children
			nc := n.NamedChildCount()
			for i := 0; i < nc; i++ {
				c := n.NamedChild(i)
				if c == nil {
					continue
				}
				field := n.FieldNameForChild(i, lang)
				ctext := string(src[c.StartByte():c.EndByte()])
				if len(ctext) > 60 {
					ctext = ctext[:60] + "..."
				}
				fmt.Printf("  [%d field=%s] %s = %q\n", i, field, c.Type(lang), ctext)
			}
			*found++
		}
	}
	nc := n.NamedChildCount()
	for i := 0; i < nc; i++ {
		walk(src, n.NamedChild(i), lang, path, found)
	}
}
