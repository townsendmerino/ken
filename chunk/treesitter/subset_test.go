package treesitter

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"

	"github.com/odvcencio/gotreesitter/grammars"
)

// TestSubsetTagsMatchKenToTreeSitter is the ADR-033 drift guard. ken's
// release binaries are built slim — only the grammars the treesitter
// chunker actually dispatches (kenToTreeSitter) are embedded, gated by
// grammar_subset_<lang> build tags listed in .goreleaser.yml. If that tag
// set drifts from kenToTreeSitter in EITHER direction, the slim binary
// silently misbehaves:
//
//   - tag missing for a mapped language → DetectLanguageByName returns
//     nil → poolFor returns nil → that language silently line-falls-back
//     (no AST chunks — the whole reason we ship treesitter, gone).
//   - tag present for an unmapped language → a grammar blob nothing
//     requests is embedded (wasted binary bytes).
//
// This test runs in the DEFAULT (fat) build — it doesn't need the subset
// tags set, it just reads the goreleaser tag list (the single source of
// truth) and compares it to the map. So `go test ./...` enforces it on
// every PR.
func TestSubsetTagsMatchKenToTreeSitter(t *testing.T) {
	goreleaser := filepath.Join("..", "..", ".goreleaser.yml")
	data, err := os.ReadFile(goreleaser)
	if err != nil {
		t.Fatalf("read %s: %v", goreleaser, err)
	}

	// Extract `  - grammar_subset_<lang>` YAML list items (not prose
	// mentions in the comment block). The master `grammar_subset` gate
	// has no <lang> suffix and is excluded from the language set.
	re := regexp.MustCompile(`(?m)^\s*-\s*grammar_subset_([a-z_]+)\s*$`)
	tagLangs := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(string(data), -1) {
		tagLangs[m[1]] = true
	}
	if len(tagLangs) == 0 {
		t.Fatalf("no grammar_subset_<lang> tags found in %s — did the slim build config move?", goreleaser)
	}

	// The grammar_subset tags use gotreesitter blob names; kenToTreeSitter's
	// VALUES are those blob names. (Keys are ken's canonical language names;
	// for ken's 17 they happen to equal the values, but compare on values
	// to be correct if a key ever differs from its blob name.)
	mapBlobs := map[string]bool{}
	for _, blob := range kenToTreeSitter {
		mapBlobs[blob] = true
	}

	missingTag := diff(mapBlobs, tagLangs) // mapped but no slim tag → silent fallback
	extraTag := diff(tagLangs, mapBlobs)   // tagged but not mapped → wasted bytes
	if len(missingTag) > 0 || len(extraTag) > 0 {
		t.Errorf("grammar_subset tags drifted from kenToTreeSitter:\n"+
			"  mapped languages with NO slim tag (would silently line-fall-back): %v\n"+
			"  slim tags for UNMAPPED languages (wasted embed bytes):              %v\n"+
			"Fix: keep .goreleaser.yml's grammar_subset tag list == kenToTreeSitter values.",
			missingTag, extraTag)
	}
}

// TestKenToTreeSitterGrammarsResolve asserts every grammar ken claims to
// support actually exists in the pinned gotreesitter version — catching a
// dependency bump that renamed or dropped a grammar blob. Runs in the
// default build (all grammars embedded), so resolution is a presence check.
func TestKenToTreeSitterGrammarsResolve(t *testing.T) {
	for kenLang, blob := range kenToTreeSitter {
		entry := grammars.DetectLanguageByName(blob)
		if entry == nil || entry.Language() == nil {
			t.Errorf("kenToTreeSitter[%q]=%q does not resolve to a loadable grammar in the pinned gotreesitter — renamed/dropped upstream?", kenLang, blob)
		}
	}
}

func diff(a, b map[string]bool) []string {
	var out []string
	for k := range a {
		if !b[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
