package buildchecks

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"

	"github.com/odvcencio/gotreesitter/grammars"

	"github.com/townsendmerino/aikit/chunk/treesitter"
)

// TestSubsetTagsMatchKenToTreeSitter is the ADR-033 drift guard. ken's
// release binaries are built slim — only the grammars the treesitter
// chunker actually dispatches (aikit/chunk/treesitter's Chunker.Grammars();
// the underlying language→grammar map was made unexported in the 2026-07-25
// audit) are embedded, gated by grammar_subset_<grammar> build tags listed in
// .goreleaser.yml. If that tag set drifts from the chunker's grammar set in
// EITHER direction, the slim binary silently misbehaves:
//
//   - tag missing for a mapped language → DetectLanguageByName returns
//     nil → poolFor returns nil → that language silently line-falls-back
//     (no AST chunks — the whole reason we ship treesitter, gone).
//   - tag present for an unmapped language → a grammar blob nothing
//     requests is embedded (wasted binary bytes).
//
// This test runs in the DEFAULT (fat) build — it doesn't need the subset
// tags set, it just reads the goreleaser tag list (the single source of
// truth) and compares it to the map. `go test ./internal/buildchecks/`
// enforces it on every PR.
func TestSubsetTagsMatchKenToTreeSitter(t *testing.T) {
	goreleaser := filepath.Join("..", "..", ".goreleaser.yml")
	data, err := os.ReadFile(goreleaser)
	if err != nil {
		t.Fatalf("read %s: %v", goreleaser, err)
	}

	re := regexp.MustCompile(`(?m)^\s*-\s*grammar_subset_([a-z_]+)\s*$`)
	tagLangs := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(string(data), -1) {
		tagLangs[m[1]] = true
	}
	if len(tagLangs) == 0 {
		t.Fatalf("no grammar_subset_<lang> tags found in %s — did the slim build config move?", goreleaser)
	}

	mapBlobs := map[string]bool{}
	for _, blob := range (&treesitter.Chunker{}).Grammars() {
		mapBlobs[blob] = true
	}

	missingTag := diff(mapBlobs, tagLangs) // mapped but no slim tag → silent fallback
	extraTag := diff(tagLangs, mapBlobs)   // tagged but not mapped → wasted bytes
	if len(missingTag) > 0 || len(extraTag) > 0 {
		t.Errorf("grammar_subset tags drifted from the treesitter chunker's grammars:\n"+
			"  grammars with NO slim tag (would silently line-fall-back): %v\n"+
			"  slim tags for UNKNOWN grammars (wasted embed bytes):        %v\n"+
			"Fix: keep .goreleaser.yml's grammar_subset tag list == (&treesitter.Chunker{}).Grammars().",
			missingTag, extraTag)
	}
}

// TestKenToTreeSitterGrammarsResolve asserts every grammar ken claims to
// support actually exists in the pinned gotreesitter version — catching a
// dependency bump that renamed or dropped a grammar blob. Runs in the
// default build (all grammars embedded), so resolution is a presence check.
func TestKenToTreeSitterGrammarsResolve(t *testing.T) {
	for _, blob := range (&treesitter.Chunker{}).Grammars() {
		entry := grammars.DetectLanguageByName(blob)
		if entry == nil || entry.Language() == nil {
			t.Errorf("grammar %q does not resolve to a loadable grammar in the pinned gotreesitter — renamed/dropped upstream?", blob)
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
