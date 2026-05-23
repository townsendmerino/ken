package repo

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// TestWalkFS_NestedGitignoreRealFSParity confirms the gobe scenario
// behaves identically on a real filesystem (via os.DirFS) and pins
// that node_modules cannot leak into the indexed result. This is the
// regression gate for the field issue that motivated ADR-015.
func TestWalkFS_NestedGitignoreRealFSParity(t *testing.T) {
	tmp := t.TempDir()
	write(t, tmp, "pkg/foo/.gitignore", []byte("node_modules/\n"))
	write(t, tmp, "pkg/foo/node_modules/lodash.js", []byte("module.exports = {}\n"))
	write(t, tmp, "pkg/foo/src/index.ts", []byte("export {}\n"))

	got, err := Walk(Options{Root: tmp})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	gotFS, err := WalkFS(os.DirFS(tmp), Options{})
	if err != nil {
		t.Fatalf("WalkFS: %v", err)
	}
	if !reflect.DeepEqual(got, gotFS) {
		t.Fatalf("Walk vs WalkFS diverged on nested gitignore:\n  Walk:   %#v\n  WalkFS: %#v", got, gotFS)
	}
	for _, p := range got {
		if strings.Contains(p, "node_modules") {
			t.Fatalf("node_modules leaked into index: %s (full result: %v)", p, got)
		}
	}
}
