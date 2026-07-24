package repo

import (
	"errors"
	"strconv"
	"testing"
	"testing/fstest"
)

// TestWalkFS_MaxFilesAdmissionCap pins the §5 admission cap: WalkFS rejects
// (rather than OOMs on) a repo whose indexable file count exceeds
// KEN_MAX_FILES. Uses a low cap so the test needs only a handful of files.
func TestWalkFS_MaxFilesAdmissionCap(t *testing.T) {
	fsys := fstest.MapFS{
		"a.go": {Data: []byte("package a\n")},
		"b.go": {Data: []byte("package b\n")},
		"c.go": {Data: []byte("package c\n")},
		"d.go": {Data: []byte("package d\n")},
	}

	t.Run("exceeding the cap errors", func(t *testing.T) {
		t.Setenv("KEN_MAX_FILES", "2")
		_, err := WalkFS(fsys, Options{})
		if !errors.Is(err, ErrTooManyFiles) {
			t.Fatalf("WalkFS err = %v, want ErrTooManyFiles", err)
		}
	})

	t.Run("at/under the cap succeeds", func(t *testing.T) {
		t.Setenv("KEN_MAX_FILES", "10")
		got, err := WalkFS(fsys, Options{})
		if err != nil {
			t.Fatalf("WalkFS: %v", err)
		}
		if len(got) != 4 {
			t.Errorf("WalkFS returned %d files, want 4", len(got))
		}
	})

	t.Run("0 means unlimited", func(t *testing.T) {
		t.Setenv("KEN_MAX_FILES", "0")
		got, err := WalkFS(fsys, Options{})
		if err != nil {
			t.Fatalf("WalkFS: %v", err)
		}
		if len(got) != 4 {
			t.Errorf("WalkFS returned %d files, want 4", len(got))
		}
	})

	// Sanity: the default is generous (well above any test corpus).
	if DefaultMaxFiles < 100_000 {
		t.Errorf("DefaultMaxFiles = %s, want a generous backstop (>=100k)", strconv.Itoa(DefaultMaxFiles))
	}
}
