package repo

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMatcher_SymlinkNotIndexed pins M6: the watch-path Matcher must reject
// symlinks the same way the batch WalkFS does, so a symlink added during an
// active watch (e.g. a branch checkout adding `link.go -> ../../etc/passwd`)
// can't get an out-of-root file's contents indexed.
func TestMatcher_SymlinkNotIndexed(t *testing.T) {
	tmp := t.TempDir()
	write(t, tmp, "main.go", []byte("package main\n"))

	// A target OUTSIDE the indexed root, reached via a symlink inside it.
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(outside, []byte("out-of-root secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(tmp, "link.go")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unsupported on this platform: %v", err)
	}

	m := NewMatcher(Options{Root: tmp})
	if m.ShouldIndex("link.go") {
		t.Error("ShouldIndex(link.go) = true, want false (symlink to an out-of-root file must not be indexed)")
	}
	if !m.ShouldIndex("main.go") {
		t.Error("ShouldIndex(main.go) = false, want true (regular file)")
	}
}
