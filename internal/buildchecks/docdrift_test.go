package buildchecks

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Doc-drift guards (roadmap #26). The 1.0.1 docs-currency findings (#3/#4)
// shared a root cause: hand-maintained indexes + cross-file links with no
// check. These tests are the cheap guards.

func repoPath(rel string) string { return filepath.Join("..", "..", rel) }

var (
	adrIndexRow = regexp.MustCompile(`(?m)^\|\s*\[ADR-0*(\d+)\]`)
	adrSection  = regexp.MustCompile(`(?m)^#{2,4}\s+ADR-0*(\d+)[:\s]`)
)

// TestADRIndexMatchesSections guards DECISIONS.md's hand-maintained index
// table against its ADR sections — the exact drift that left ADR-034..037
// out of the index (1.0.1 finding #4). Every ADR section must have an index
// row and vice versa.
func TestADRIndexMatchesSections(t *testing.T) {
	data, err := os.ReadFile(repoPath("docs/internal/DECISIONS.md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	idx := map[string]bool{}
	for _, m := range adrIndexRow.FindAllStringSubmatch(s, -1) {
		idx[m[1]] = true
	}
	sec := map[string]bool{}
	for _, m := range adrSection.FindAllStringSubmatch(s, -1) {
		sec[m[1]] = true
	}
	if len(idx) == 0 || len(sec) == 0 {
		t.Fatalf("parsed 0 ADRs (index=%d sections=%d) — the regexes drifted from DECISIONS.md's format", len(idx), len(sec))
	}
	for n := range sec {
		if !idx[n] {
			t.Errorf("ADR-%s has a section but NO index-table row (DECISIONS.md index drift)", n)
		}
	}
	for n := range idx {
		if !sec[n] {
			t.Errorf("ADR-%s is in the index table but has NO section", n)
		}
	}
}

var mdLink = regexp.MustCompile(`\]\(([^)]+)\)`)

// TestDocLinksResolve walks the repo's markdown and verifies every relative
// link points at a file that exists — catching the moved/renamed-file drift
// the 1.0.1 review found (e.g. the dead internal/coderank link, the relocated
// outputs/ memos). External (http/mailto), pure-anchor, and gitignored-scratch
// (outputs/) links are skipped; fenced code blocks are ignored.
func TestDocLinksResolve(t *testing.T) {
	root := repoPath("")
	skipDir := map[string]bool{
		".git": true, "outputs": true, "node_modules": true,
		".venv": true, "bench_out": true, "dist": true, "vendor": true,
		// cmd/ken-mcp-docs embeds a COPY of docs/ for the demo binary; its
		// relative links resolve against the embed root, not the repo, so
		// they're not browsable links — skip the whole demo tree.
		"ken-mcp-docs": true,
	}
	// Files whose links are intentionally not repo-file links: CHANGELOG is
	// an append-only archive (old entries point at then-current paths), and
	// docs/index.md is the GitHub Pages landing page (site routes, not files).
	skipFile := map[string]bool{
		"CHANGELOG.md":  true,
		"docs/index.md": true,
	}
	var mdFiles []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipDir[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(p, ".md") {
			mdFiles = append(mdFiles, p)
		}
		return nil
	})
	if len(mdFiles) == 0 {
		t.Fatal("found no markdown files to check")
	}

	for _, mf := range mdFiles {
		rel, _ := filepath.Rel(root, mf)
		if skipFile[filepath.ToSlash(rel)] {
			continue
		}
		data, err := os.ReadFile(mf)
		if err != nil {
			t.Errorf("read %s: %v", mf, err)
			continue
		}
		dir := filepath.Dir(mf)
		inCode := false
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "```") {
				inCode = !inCode
				continue
			}
			if inCode {
				continue
			}
			for _, m := range mdLink.FindAllStringSubmatch(line, -1) {
				link := strings.TrimSpace(m[1])
				// Drop an optional `(path "title")` suffix.
				if i := strings.IndexAny(link, " \t"); i >= 0 {
					link = link[:i]
				}
				// Skip external, mailto, pure-anchor, and empty.
				if link == "" || strings.HasPrefix(link, "#") ||
					strings.HasPrefix(link, "mailto:") || strings.Contains(link, "://") {
					continue
				}
				// Strip #anchor / ?query.
				if i := strings.IndexAny(link, "#?"); i >= 0 {
					link = link[:i]
				}
				if link == "" {
					continue
				}
				// outputs/ is gitignored local scratch — docs reference it
				// informally; not a tracked link target (roadmap #11).
				if strings.HasPrefix(link, "outputs/") || strings.Contains(link, "/outputs/") {
					continue
				}
				target := filepath.Join(dir, link)
				if _, err := os.Stat(target); err != nil {
					t.Errorf("%s: broken link %q → %q does not exist", rel, m[1], filepath.Clean(filepath.Join(filepath.Dir(rel), link)))
				}
			}
		}
	}
}
