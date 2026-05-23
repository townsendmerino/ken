package repo

import (
	"reflect"
	"sort"
	"testing"
	"testing/fstest"
)

// TestWalkFS_NestedGitignore exercises ADR-015's six core semantics
// against an fstest.MapFS. Each subtest constructs a minimal tree, runs
// WalkFS, and asserts the indexed file list against an expected literal.
func TestWalkFS_NestedGitignore(t *testing.T) {
	t.Run("gobe scenario — nested rule prunes node_modules", func(t *testing.T) {
		// Load-bearing case: the gobe user's monorepo had per-package
		// .gitignore files excluding node_modules/, no root ignore.
		// Pre-ADR-015 the walker missed this and indexed every JS file
		// under every node_modules tree.
		fsys := fstest.MapFS{
			"pkg/foo/.gitignore":             {Data: []byte("node_modules/\n")},
			"pkg/foo/node_modules/lodash.js": {Data: []byte("module.exports = {}\n")},
			"pkg/foo/src/index.ts":           {Data: []byte("export {}\n")},
		}
		got, err := WalkFS(fsys, Options{})
		if err != nil {
			t.Fatalf("WalkFS: %v", err)
		}
		sort.Strings(got)
		want := []string{
			"pkg/foo/.gitignore",
			"pkg/foo/src/index.ts",
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("WalkFS = %v, want %v", got, want)
		}
	})

	t.Run("inner inherits root scope", func(t *testing.T) {
		fsys := fstest.MapFS{
			".gitignore":     {Data: []byte("*.log\n")},
			"pkg/.gitignore": {Data: []byte("\n")}, // empty, no rules pushed
			"pkg/app.log":    {Data: []byte("noise\n")},
			"pkg/main.go":    {Data: []byte("package main\n")},
		}
		got, err := WalkFS(fsys, Options{})
		if err != nil {
			t.Fatalf("WalkFS: %v", err)
		}
		sort.Strings(got)
		want := []string{".gitignore", "pkg/.gitignore", "pkg/main.go"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("WalkFS = %v, want %v (pkg/.gitignore is itself a regular file and indexed; app.log inherited from root scope)", got, want)
		}
	})

	t.Run("inner re-includes via negation", func(t *testing.T) {
		fsys := fstest.MapFS{
			".gitignore":        {Data: []byte("*.log\n")},
			"pkg/.gitignore":    {Data: []byte("!important.log\n")},
			"pkg/important.log": {Data: []byte("kept\n")},
			"pkg/debug.log":     {Data: []byte("dropped\n")},
		}
		got, err := WalkFS(fsys, Options{})
		if err != nil {
			t.Fatalf("WalkFS: %v", err)
		}
		sort.Strings(got)
		want := []string{".gitignore", "pkg/.gitignore", "pkg/important.log"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("WalkFS = %v, want %v (pkg/.gitignore's !important.log re-includes from outer *.log)", got, want)
		}
	})

	t.Run("inner-scoped pattern does not leak outward", func(t *testing.T) {
		fsys := fstest.MapFS{
			"subdir/.gitignore": {Data: []byte("secret.txt\n")},
			"subdir/secret.txt": {Data: []byte("hidden\n")},
			"secret.txt":        {Data: []byte("root-level, kept\n")},
		}
		got, err := WalkFS(fsys, Options{})
		if err != nil {
			t.Fatalf("WalkFS: %v", err)
		}
		sort.Strings(got)
		want := []string{"secret.txt", "subdir/.gitignore"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("WalkFS = %v, want %v (subdir's secret.txt rule must not match root-level secret.txt)", got, want)
		}
	})

	t.Run("anchored pattern inside nested scope", func(t *testing.T) {
		fsys := fstest.MapFS{
			"subdir/.gitignore":         {Data: []byte("/build/\n")},
			"subdir/build/x.txt":        {Data: []byte("pruned\n")},
			"subdir/nested/build/y.txt": {Data: []byte("kept — anchored to subdir/\n")},
		}
		got, err := WalkFS(fsys, Options{})
		if err != nil {
			t.Fatalf("WalkFS: %v", err)
		}
		sort.Strings(got)
		want := []string{"subdir/.gitignore", "subdir/nested/build/y.txt"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("WalkFS = %v, want %v (/build/ is anchored to subdir/, doesn't match subdir/nested/build/)", got, want)
		}
	})

	t.Run("directory prune still works under nested gitignore", func(t *testing.T) {
		fsys := fstest.MapFS{
			"pkg/.gitignore":          {Data: []byte("tmp/\n")},
			"pkg/tmp/anything.txt":    {Data: []byte("pruned\n")},
			"pkg/tmp/sub/another.txt": {Data: []byte("also pruned\n")},
			"pkg/keep.txt":            {Data: []byte("kept\n")},
		}
		got, err := WalkFS(fsys, Options{})
		if err != nil {
			t.Fatalf("WalkFS: %v", err)
		}
		sort.Strings(got)
		want := []string{"pkg/.gitignore", "pkg/keep.txt"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("WalkFS = %v, want %v (tmp/ rule prunes the entire subtree)", got, want)
		}
	})
}
