package structural

import (
	"context"
	"errors"
	"testing"
)

// TestBuildWithContext_Cancelled: an already-cancelled context aborts the
// build (audit db/mcp §8) — workers skip their jobs and Build returns
// ctx.Err() rather than parsing the whole corpus on an abandoned request.
func TestBuildWithContext_Cancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before the build starts

	ix, err := BuildWithContext(ctx, "../../testdata/repo")
	if err == nil {
		t.Fatal("BuildWithContext with a cancelled context should return an error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if ix != nil {
		t.Errorf("index = %p, want nil on cancellation", ix)
	}
}

// TestBuild_UncancelledStillWorks: the plain Build wrapper (background
// context) is unaffected — it builds normally.
func TestBuild_UncancelledStillWorks(t *testing.T) {
	ix, err := Build("../../testdata/repo")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if ix == nil {
		t.Fatal("Build returned nil index")
	}
}
