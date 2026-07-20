package repo

import "testing"

// TestMatcher_NestedGitignore_Gobe pins that Matcher (the seam used by
// the watch path) honors nested .gitignore files the same way WalkFS
// does. ShouldIndex must agree with WalkFS's would-have-indexed answer
// for the gobe field scenario; otherwise the watcher could re-add
// node_modules entries an interactive `ken index --watch` user
// expects to stay out of the index.
func TestMatcher_NestedGitignore_Gobe(t *testing.T) {
	tmp := t.TempDir()
	write(t, tmp, "pkg/foo/.gitignore", []byte("node_modules/\n"))
	write(t, tmp, "pkg/foo/node_modules/lodash.js", []byte("module.exports = {}\n"))
	write(t, tmp, "pkg/foo/src/index.ts", []byte("export {}\n"))

	m := NewMatcher(Options{Root: tmp})

	if got := m.ShouldIndex("pkg/foo/node_modules/lodash.js"); got != false {
		t.Errorf("ShouldIndex(pkg/foo/node_modules/lodash.js) = %v, want false (pkg/foo/.gitignore excludes node_modules/)", got)
	}
	if got := m.ShouldIndex("pkg/foo/src/index.ts"); got != true {
		t.Errorf("ShouldIndex(pkg/foo/src/index.ts) = %v, want true (not gitignored)", got)
	}
	if got := m.ShouldIndex("pkg/foo/.gitignore"); got != true {
		t.Errorf("ShouldIndex(pkg/foo/.gitignore) = %v, want true (.gitignore files are themselves indexable)", got)
	}
}

// TestMatcher_KenIgnore pins that Matcher — the seam the watch path
// (internal/search/watch.go) filters fsnotify events through — honors the
// .kenignore / .sembleignore family (ADR-038) exactly like WalkFS, so a
// watch-driven flush can't re-add a file a .kenignore excludes. Also pins
// the .sembleignore fallback and the no-cross-file-re-include guarantee.
func TestMatcher_KenIgnore(t *testing.T) {
	t.Run("kenignore excludes built assets; sembleignore is the fallback", func(t *testing.T) {
		tmp := t.TempDir()
		write(t, tmp, ".kenignore", []byte("build/\n"))
		write(t, tmp, ".sembleignore", []byte("vendor/\n"))
		write(t, tmp, "build/bundle.js", []byte("/*built*/\n"))
		write(t, tmp, "vendor/lib.php", []byte("<?php\n"))
		write(t, tmp, "src/main.go", []byte("package main\n"))

		m := NewMatcher(Options{Root: tmp})
		if got := m.ShouldIndex("build/bundle.js"); got != false {
			t.Errorf("ShouldIndex(build/bundle.js) = %v, want false (.kenignore build/)", got)
		}
		// .kenignore present ⇒ .sembleignore rules are NOT loaded, so
		// vendor/ is indexed.
		if got := m.ShouldIndex("vendor/lib.php"); got != true {
			t.Errorf("ShouldIndex(vendor/lib.php) = %v, want true (.sembleignore not loaded when .kenignore exists)", got)
		}
		if got := m.ShouldIndex("src/main.go"); got != true {
			t.Errorf("ShouldIndex(src/main.go) = %v, want true", got)
		}
	})

	t.Run("sembleignore applies when no kenignore", func(t *testing.T) {
		tmp := t.TempDir()
		write(t, tmp, ".sembleignore", []byte("vendor/\n"))
		write(t, tmp, "vendor/lib.php", []byte("<?php\n"))
		write(t, tmp, "src/app.php", []byte("<?php\n"))

		m := NewMatcher(Options{Root: tmp})
		if got := m.ShouldIndex("vendor/lib.php"); got != false {
			t.Errorf("ShouldIndex(vendor/lib.php) = %v, want false (.sembleignore vendor/)", got)
		}
		if got := m.ShouldIndex("src/app.php"); got != true {
			t.Errorf("ShouldIndex(src/app.php) = %v, want true", got)
		}
	})

	t.Run("kenignore negation cannot re-include a git-ignored path", func(t *testing.T) {
		tmp := t.TempDir()
		write(t, tmp, ".gitignore", []byte("secret.txt\n"))
		write(t, tmp, ".kenignore", []byte("!secret.txt\n"))
		write(t, tmp, "secret.txt", []byte("shh\n"))

		m := NewMatcher(Options{Root: tmp})
		if got := m.ShouldIndex("secret.txt"); got != false {
			t.Errorf("ShouldIndex(secret.txt) = %v, want false (no cross-file re-include)", got)
		}
	})
}

// TestMatcher_RootGitignore_DirOnlyAppliesToContents pins the
// pre-existing dir-only-on-files behavior fix: a root-level
// `build/` rule must exclude files inside build/ when asked via
// ShouldIndex, not just the bare `build` directory entry. Walk got
// this right via fs.SkipDir; Matcher had to synthesize the same
// pruning via ancestor-walk (see ShouldIndex implementation).
func TestMatcher_RootGitignore_DirOnlyAppliesToContents(t *testing.T) {
	tmp := t.TempDir()
	write(t, tmp, ".gitignore", []byte("build/\n"))
	write(t, tmp, "build/out.txt", []byte("artifact\n"))
	write(t, tmp, "main.go", []byte("package main\n"))

	m := NewMatcher(Options{Root: tmp})

	if got := m.ShouldIndex("build/out.txt"); got != false {
		t.Errorf("ShouldIndex(build/out.txt) = %v, want false (build/ dir-only rule excludes contents)", got)
	}
	if got := m.ShouldIndex("main.go"); got != true {
		t.Errorf("ShouldIndex(main.go) = %v, want true", got)
	}
}
