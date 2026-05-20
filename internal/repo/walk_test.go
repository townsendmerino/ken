package repo

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func write(t *testing.T, root, rel string, data []byte) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWalk_GitignoreBinarySizeAndGitDir(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".gitignore", []byte("*.log\n!keep.log\nbuild/\nnode_modules/\n"))
	write(t, root, "a.go", []byte("package main\n"))
	write(t, root, "sub/b.py", []byte("print('hi')\n"))
	write(t, root, "ignored.log", []byte("noise\n"))
	write(t, root, "keep.log", []byte("kept by negation\n"))
	write(t, root, "build/out.txt", []byte("artifact\n"))
	write(t, root, "node_modules/dep/x.js", []byte("module\n"))
	write(t, root, ".git/config", []byte("[core]\n"))
	write(t, root, "bin.dat", []byte{'a', 0x00, 'b'})           // NUL ⇒ binary
	write(t, root, "big.txt", []byte(strings.Repeat("x", 200))) // over the cap

	got, err := Walk(Options{Root: root, MaxFileBytes: 64})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	sort.Strings(got)
	want := []string{".gitignore", "a.go", "keep.log", "sub/b.py"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Walk = %v, want %v", got, want)
	}
}

func TestWalk_NoGitignore(t *testing.T) {
	root := t.TempDir()
	write(t, root, "main.go", []byte("package main\n"))
	write(t, root, ".git/HEAD", []byte("ref: refs/heads/main\n"))

	got, err := Walk(Options{Root: root})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"main.go"}) {
		t.Errorf("Walk = %v, want [main.go] (.git always pruned)", got)
	}
}
