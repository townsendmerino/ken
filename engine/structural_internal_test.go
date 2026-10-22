package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/townsendmerino/ken/internal/structural"
)

// TestStructuralApplyDuringBuild: a change that arrives while the
// structural build is in flight is applied to the build's result before it
// publishes, so the first index a reader sees is already current (K13).
func TestStructuralApplyDuringBuild(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	write := func(body string) {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("package a\n\nfunc Old() {}\n")

	release := make(chan struct{})
	started := make(chan struct{})
	s := &structuralState{
		builder: func(ctx context.Context) (*structural.Index, error) {
			idx, err := structural.BuildWithContext(ctx, dir) // reads the old file
			close(started)
			<-release
			return idx, err
		},
		update: func(idx *structural.Index, paths []string) (*structural.Index, bool) {
			return idx.Update(dir, paths, nil, nil)
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	go s.get(ctx) // starts the build
	<-started

	write("package a\n\nfunc New() {}\n")
	s.apply([]string{"a.go"}, false) // queued: the build is in flight
	if s.built() {
		t.Fatal("published before the build finished")
	}
	close(release)

	idx := s.get(ctx)
	if idx == nil {
		t.Fatal("no index after the build")
	}
	if got := idx.Defs("New"); len(got) != 1 {
		t.Errorf("Defs(New) = %v: the change queued during the build wasn't applied", got)
	}
	if got := idx.Defs("Old"); len(got) != 0 {
		t.Errorf("Defs(Old) = %v: the build's stale read was published", got)
	}
	if g := s.gen.Load(); g != 1 {
		t.Errorf("gen = %d after the first publish, want 1", g)
	}
}

// TestStructuralApplyResync: a watcher overflow (resync) drops the built
// index so the next call rebuilds over the current tree.
func TestStructuralApplyResync(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n\nfunc A() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &structuralState{
		builder: func(ctx context.Context) (*structural.Index, error) { return structural.BuildWithContext(ctx, dir) },
		update: func(idx *structural.Index, paths []string) (*structural.Index, bool) {
			return idx.Update(dir, paths, nil, nil)
		},
	}
	ctx := context.Background()
	if s.get(ctx) == nil {
		t.Fatal("first build failed")
	}
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("package a\n\nfunc B() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.apply(nil, true)
	if s.built() {
		t.Fatal("resync kept the stale index")
	}
	idx := s.get(ctx)
	if idx == nil || len(idx.Defs("B")) != 1 {
		t.Fatalf("rebuild after resync didn't see b.go: %v", idx)
	}
}
