package buildchecks

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// engine's escape hatches are the raw-index accessors and the aliases they
// return: Workspace.WatchedIndex, Workspace.Structural,
// Workspace.StructuralIfBuilt, and the types engine.Index,
// engine.WatchedIndex and engine.StructuralIndex. They expose
// internal/search's and internal/structural's methods, so every caller is
// code that breaks when those packages change.
//
// The composition-root plan's end state is that ken itself has no such
// caller, apart from the places below. escapeHatchUsers maps a file to the
// number of uses it may hold; TestEscapeHatchesHaveNoNewCallers fails when
// a file's count differs, in either direction, so a new use needs a
// reviewed edit here and a removed one can't quietly return.
//
// The check is textual (a regexp over non-test source outside engine/), not
// type-aware: it would also count another type's method of the same name.
// Comment lines are skipped.
var escapeHatchUsers = map[string]int{
	// mcp's public API names these types: RepoBundle.Index,
	// RepoBundle.StructuralBuilder, the StructuralProvider interface and
	// RepoBundle's own StructuralIndex / StructuralIfBuilt methods. For a
	// bundle built on a Workspace those methods forward to the Workspace's
	// accessors, which is the one place ken calls them.
	"mcp/bundle.go": 12,
	// Cache.Get has returned *WatchedIndex since v0.3; the signature is
	// public API.
	"mcp/cache.go": 1,
	// A bench harness (//go:build bench): the ablation arm calls
	// internal/search's SearchModeAblated, which engine does not model.
	"bench/contextbench/arms.go": 1,
}

var escapeHatchUse = regexp.MustCompile(`\.(WatchedIndex|StructuralIfBuilt)\(\)|\.Structural\(|\bengine\.(Index|WatchedIndex|StructuralIndex)\b`)

func TestEscapeHatchesHaveNoNewCallers(t *testing.T) {
	root := repoPath("")
	got := map[string]int{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := filepath.ToSlash(strings.TrimPrefix(p, root+string(filepath.Separator)))
		if d.IsDir() {
			name := d.Name()
			if p != root && (name == "testdata" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || rel == "engine") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			got[rel] += len(escapeHatchUse.FindAllString(line, -1))
		}
		if got[rel] == 0 {
			delete(got, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking module sources: %v", err)
	}
	for file, n := range got {
		want, ok := escapeHatchUsers[file]
		switch {
		case !ok:
			t.Errorf("%s uses engine's escape hatches (%d places); work through *engine.Workspace's own methods instead, or add the file to escapeHatchUsers with the reason", file, n)
		case n != want:
			t.Errorf("%s uses engine's escape hatches in %d places, escapeHatchUsers says %d; update the count with the reason for the change", file, n, want)
		}
	}
	for file := range escapeHatchUsers {
		if _, ok := got[file]; !ok {
			t.Errorf("escapeHatchUsers names %s, which no longer uses an escape hatch; delete the entry", file)
		}
	}
}
