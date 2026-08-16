package devtools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSubsetTags_ParsesAndSorts(t *testing.T) {
	dir := t.TempDir()
	// A goreleaser fixture with list items (kept) + a prose mention (ignored) +
	// a duplicate (deduped) + out-of-order entries (sorted).
	yml := `# grammar_subset appears in this comment and must NOT be picked up.
builds:
  - flags:
      - -tags
    tags:
      - grammar_subset
      - grammar_subset_go
      - grammar_subset_c
      - grammar_subset_go
`
	if err := os.WriteFile(filepath.Join(dir, ".goreleaser.yml"), []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := SubsetTagsString(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := "grammar_subset grammar_subset_c grammar_subset_go" // sorted, deduped
	if got != want {
		t.Errorf("SubsetTagsString = %q, want %q", got, want)
	}
}

// TestSubsetTags_MatchesRealGoreleaser is a light guard that the parser works on
// the repo's actual .goreleaser.yml: non-empty, sorted, all grammar_subset*.
func TestSubsetTags_MatchesRealGoreleaser(t *testing.T) {
	root, err := RepoRoot()
	if err != nil {
		t.Skip("no repo root")
	}
	if _, err := os.Stat(filepath.Join(root, ".goreleaser.yml")); err != nil {
		t.Skip("no .goreleaser.yml")
	}
	tags, err := SubsetTags(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) == 0 {
		t.Fatal("expected ≥1 grammar_subset tag")
	}
	for i, tag := range tags {
		if !strings.HasPrefix(tag, "grammar_subset") {
			t.Errorf("tag %d = %q, want grammar_subset prefix", i, tag)
		}
		if i > 0 && tags[i-1] > tag {
			t.Errorf("tags not sorted at %d: %q > %q", i, tags[i-1], tag)
		}
	}
}
