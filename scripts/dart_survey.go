//go:build ignore

// dart_survey.go — quick per-repo "clean parse %" probe across a
// few Dart corpora. Mirrors swift_survey.go.
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
	entry := grammars.DetectLanguageByName("dart")
	if entry == nil {
		fmt.Fprintln(os.Stderr, "gotreesitter has no dart grammar")
		os.Exit(1)
	}
	lang := entry.Language()
	pool := gotreesitter.NewParserPool(lang)

	for _, repo := range []string{
		"/tmp/ken-dogfood/web",
		"/tmp/ken-dogfood/dart_style",
		"/tmp/ken-dogfood/flutter-samples",
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
			if !strings.HasSuffix(p, ".dart") {
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
			if !root.HasError() {
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
		fmt.Printf("%-30s  sampled=%d clean=%d errored=%d (%.0f%% clean)\n",
			filepath.Base(repo), total, clean, errored, pct)
		for _, e := range errs {
			fmt.Println(e)
		}
	}
}
