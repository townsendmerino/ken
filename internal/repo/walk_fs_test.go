package repo

import (
	"reflect"
	"sort"
	"testing"
	"testing/fstest"
)

// TestWalkFS_MapFS exercises WalkFS directly against an in-memory
// fstest.MapFS — no real filesystem involved. Mirrors the coverage in
// TestWalk_GitignoreBinarySizeAndGitDir: gitignore (anchored + dir-only
// + negation), .git/ prune, binary (NUL-byte) skip, and the size cap.
func TestWalkFS_MapFS(t *testing.T) {
	fsys := fstest.MapFS{
		".gitignore":            {Data: []byte("*.log\n!keep.log\nbuild/\nnode_modules/\n")},
		"a.go":                  {Data: []byte("package main\n")},
		"sub/b.py":              {Data: []byte("print('hi')\n")},
		"ignored.log":           {Data: []byte("noise\n")},
		"keep.log":              {Data: []byte("kept by negation\n")},
		"build/out.txt":         {Data: []byte("artifact\n")},
		"node_modules/dep/x.js": {Data: []byte("module\n")},
		".git/config":           {Data: []byte("[core]\n")},
		"bin.dat":               {Data: []byte{'a', 0x00, 'b'}}, // NUL ⇒ binary
		"big.txt":               {Data: make([]byte, 200)},      // over the cap
	}

	got, err := WalkFS(fsys, Options{MaxFileBytes: 64})
	if err != nil {
		t.Fatalf("WalkFS: %v", err)
	}
	sort.Strings(got)
	want := []string{".gitignore", "a.go", "keep.log", "sub/b.py"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("WalkFS = %v, want %v", got, want)
	}
}

// TestWalkFS_NoGitignore confirms the .git/ prune still fires without
// a root .gitignore (mirrors TestWalk_NoGitignore).
func TestWalkFS_NoGitignore(t *testing.T) {
	fsys := fstest.MapFS{
		"main.go":   {Data: []byte("package main\n")},
		".git/HEAD": {Data: []byte("ref: refs/heads/main\n")},
	}

	got, err := WalkFS(fsys, Options{})
	if err != nil {
		t.Fatalf("WalkFS: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"main.go"}) {
		t.Errorf("WalkFS = %v, want [main.go] (.git always pruned)", got)
	}
}
