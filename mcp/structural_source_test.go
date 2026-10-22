package mcp

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/townsendmerino/ken/internal/structural"
)

// fakeStructural is a StructuralSource that records what the bundle asked.
type fakeStructural struct {
	idx     *structural.Index
	pending bool
	closed  atomic.Int32
}

func (f *fakeStructural) StructuralIndex(context.Context) *structural.Index { return f.idx }
func (f *fakeStructural) StructuralPending() bool                           { return f.pending }
func (f *fakeStructural) StructuralIfBuilt() *structural.Index              { return f.idx }
func (f *fakeStructural) Close() error                                      { f.closed.Add(1); return nil }

// A bundle with a StructuralSource (ADR-048: ken-mcp passes its engine
// Workspace) delegates every structural method to it, never runs its own
// StructuralBuilder, and closes the source when reaped.
func TestRepoBundle_StructuralSourceDelegates(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n\nfunc A() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	built, err := structural.Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	src := &fakeStructural{pending: true}
	b := &RepoBundle{
		Structural: src,
		StructuralBuilder: func(context.Context) (*structural.Index, error) {
			t.Error("StructuralBuilder ran although a StructuralSource is set")
			return nil, nil
		},
	}
	if b.StructuralIndex(context.Background()) != nil || !b.StructuralPending() || b.StructuralIfBuilt() != nil {
		t.Error("a building source should read as nil + pending")
	}
	src.idx, src.pending = built, false
	if b.StructuralIndex(context.Background()) != built || b.StructuralPending() || b.StructuralIfBuilt() != built {
		t.Error("the bundle didn't pass the source's index through")
	}
	reapEntry(&cacheEntry{bundle: b})
	if n := src.closed.Load(); n != 1 {
		t.Errorf("reaping the bundle closed the source %d times, want 1", n)
	}
}
