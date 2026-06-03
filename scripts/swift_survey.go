//go:build ignore

// swift_survey.go — quick per-repo "clean parse %" probe across a
// few Swift corpora to gauge whether the gotreesitter swift grammar
// works on real-world Swift or only toy fixtures. Caps each repo at
// the first 50 .swift files so a swift-nio-sized corpus doesn't
// stall the survey.
package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

func main() {
	entry := grammars.DetectLanguageByName("swift")
	lang := entry.Language()
	pool := gotreesitter.NewParserPool(lang)

	for _, repo := range []string{
		"/tmp/ken-dogfood/alamofire",
		"/tmp/ken-dogfood/swift-collections",
		"/tmp/ken-dogfood/swift-nio",
		"/tmp/ken-dogfood/Defaults",
	} {
		var total, clean, errored int
		var errs []string
		filepath.WalkDir(repo, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if strings.HasPrefix(d.Name(), ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(p, ".swift") {
				return nil
			}
			if total >= 50 {
				return filepath.SkipAll
			}
			src, _ := os.ReadFile(p)
			total++
			t, _ := pool.Parse(src)
			if t == nil {
				return nil
			}
			root := t.RootNode()
			if root.Type(lang) == "source_file" && !root.HasError() {
				clean++
			} else {
				errored++
				if len(errs) < 5 {
					errs = append(errs, fmt.Sprintf("    %s (root=%s err=%v)", p, root.Type(lang), root.HasError()))
				}
			}
			t.Release()
			return nil
		})
		pct := 0.0
		if total > 0 {
			pct = float64(clean) * 100 / float64(total)
		}
		fmt.Printf("%-45s  sampled=%d clean=%d errored=%d (%.0f%% clean)\n",
			filepath.Base(repo), total, clean, errored, pct)
		for _, e := range errs {
			fmt.Println(e)
		}
	}
}
