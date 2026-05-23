package repo

import (
	"os"
	"reflect"
	"testing"
)

// TestWalkFS_ParityWithWalk is the regression gate that catches any
// divergence between the deprecated real-FS Walk and the canonical
// WalkFS over os.DirFS of the same root. Since Walk is now implemented
// in terms of WalkFS, this test effectively also pins the os.DirFS
// adapter contract.
func TestWalkFS_ParityWithWalk(t *testing.T) {
	tmp := t.TempDir()
	// Materialize a fixture that exercises every prune/skip codepath:
	// gitignore (anchored + dir-only + negation), .git/, binary, oversize.
	write(t, tmp, ".gitignore", []byte("*.log\n!keep.log\nbuild/\nnode_modules/\n"))
	write(t, tmp, "a.go", []byte("package main\n"))
	write(t, tmp, "sub/b.py", []byte("print('hi')\n"))
	write(t, tmp, "ignored.log", []byte("noise\n"))
	write(t, tmp, "keep.log", []byte("kept by negation\n"))
	write(t, tmp, "build/out.txt", []byte("artifact\n"))
	write(t, tmp, "node_modules/dep/x.js", []byte("module\n"))
	write(t, tmp, ".git/config", []byte("[core]\n"))
	write(t, tmp, "bin.dat", []byte{'a', 0x00, 'b'})
	write(t, tmp, "big.txt", make([]byte, 200))

	opts := Options{Root: tmp, MaxFileBytes: 64}

	got, err := Walk(opts)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	gotFS, err := WalkFS(os.DirFS(tmp), opts)
	if err != nil {
		t.Fatalf("WalkFS: %v", err)
	}
	if !reflect.DeepEqual(got, gotFS) {
		t.Fatalf("Walk vs WalkFS diverged:\n  Walk:   %#v\n  WalkFS: %#v", got, gotFS)
	}
}
