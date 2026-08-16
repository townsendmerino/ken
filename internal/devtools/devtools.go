// Package devtools holds the shared logic behind ken's repo-tooling commands
// under tools/ (the Go replacements for the old scripts/*.sh drivers). Keeping
// the decisions here — rather than duplicated across each tool or shelled out
// to from CI — means one source of truth: the slim-build tag set, for instance,
// is parsed once and reused by the subset-tags command, the build-subset
// command, and CI, instead of three greps that could silently drift.
package devtools

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// RepoRoot walks up from the current working directory to the module root (the
// directory containing go.mod). Tools invoke this so they work regardless of
// where they're launched from — matching the `cd "$(dirname "$0")/.."` the shell
// scripts did.
func RepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("devtools: no go.mod found above %s", dir)
		}
		dir = parent
	}
}

// grammarSubsetLine matches a `.goreleaser.yml` tags list item (`  - grammar_subset…`),
// NOT a prose mention of "grammar_subset" in a surrounding comment — the same
// two-stage grep the old subset-tags.sh used.
var grammarSubsetLine = regexp.MustCompile(`(?m)^[[:space:]]*-[[:space:]]*(grammar_subset[a-z_]*)`)

// SubsetTags returns the sorted, de-duplicated gotreesitter `grammar_subset`
// build tags from <root>/.goreleaser.yml — the slim-build tag set the release
// uses (ADR-033). The result matches the old `subset-tags.sh` output byte for
// byte (sorted, space-joined) so CG and local slim builds stay identical.
func SubsetTags(root string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(root, ".goreleaser.yml"))
	if err != nil {
		return nil, fmt.Errorf("devtools: read .goreleaser.yml: %w", err)
	}
	seen := map[string]struct{}{}
	var tags []string
	for _, m := range grammarSubsetLine.FindAllStringSubmatch(string(data), -1) {
		tag := m[1]
		if _, dup := seen[tag]; dup {
			continue
		}
		seen[tag] = struct{}{}
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	return tags, nil
}

// SubsetTagsString is SubsetTags joined with a single space — the exact form the
// build commands pass to `go build -tags`.
func SubsetTagsString(root string) (string, error) {
	tags, err := SubsetTags(root)
	if err != nil {
		return "", err
	}
	return strings.Join(tags, " "), nil
}
