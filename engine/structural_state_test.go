package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/townsendmerino/ken/internal/structural"
)

// F2: during a rebuild after a resync, callers get the last good index (and
// StructuralStale says so) rather than nothing.
func TestStructuralServesLastGoodIndexDuringRebuild(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n\nfunc A() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	first := true
	w := &Workspace{}
	w.structural = structuralState{
		builder: func(ctx context.Context) (*structural.Index, error) {
			if !first {
				<-release
			}
			first = false
			return structural.BuildWithContext(ctx, dir)
		},
		update: func(idx *structural.Index, paths []string) (*structural.Index, bool) { return idx, false },
	}
	ctx := context.Background()
	if _, err := w.structuralIndex(ctx); err != nil {
		t.Fatal(err)
	}
	w.structural.apply(nil, true)
	start := time.Now()
	idx, err := w.structuralIndex(ctx)
	if err != nil || idx == nil {
		t.Fatalf("during rebuild: idx=%v err=%v, want the last good index", idx, err)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("waited %v for the rebuild despite a stale index", time.Since(start))
	}
	if !w.StructuralStale() {
		t.Error("StructuralStale = false during the rebuild")
	}
	close(release)
	deadline := time.Now().Add(10 * time.Second)
	for w.StructuralStale() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if w.StructuralStale() {
		t.Error("still stale after the rebuild published")
	}
}

// F2: a failed build is reported as ErrStructuralFailed carrying the cause.
func TestStructuralFailedNamesTheBuildError(t *testing.T) {
	w := &Workspace{}
	w.structural = structuralState{
		builder: func(context.Context) (*structural.Index, error) { return nil, errors.New("disk on fire") },
	}
	_, err := w.structuralIndex(context.Background())
	if !errors.Is(err, ErrStructuralFailed) || !strings.Contains(err.Error(), "disk on fire") {
		t.Fatalf("err = %v, want ErrStructuralFailed naming the cause", err)
	}
	if errors.Is(err, ErrNoStructuralIndex) {
		t.Error("a failed build must not read as an unsupported corpus")
	}
}

// StructuralState reports building / failed / none / built without blocking.
func TestStructuralState(t *testing.T) {
	w := &Workspace{}
	w.structural = structuralState{
		builder: func(context.Context) (*structural.Index, error) { return nil, errors.New("disk on fire") },
	}
	// Never built, no cooldown: a lookup would start one.
	if err := w.StructuralState(); !errors.Is(err, ErrStructuralBuilding) {
		t.Fatalf("before any build: %v, want ErrStructuralBuilding", err)
	}
	_, _ = w.structuralIndex(context.Background()) // the build fails
	if err := w.StructuralState(); !errors.Is(err, ErrStructuralFailed) || !strings.Contains(err.Error(), "disk on fire") {
		t.Fatalf("after a failed build: %v", err)
	}
	w.structural.failedAt = time.Now().Add(-2 * structuralFailCooldown)
	if err := w.StructuralState(); !errors.Is(err, ErrStructuralBuilding) {
		t.Fatalf("after the cooldown: %v, want a retry to be pending", err)
	}
	w.structural.cur.Store(&structural.Index{})
	if err := w.StructuralState(); err != nil {
		t.Fatalf("with an index: %v, want nil", err)
	}
}
