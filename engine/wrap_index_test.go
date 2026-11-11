package engine_test

import (
	"context"
	"errors"
	"testing"

	"github.com/townsendmerino/ken/engine"
)

// A WrapIndex view answers like the Workspace that owns the index, takes
// its structural index from the source it was given on every call, and
// owns nothing.
func TestWrapIndex_IsAViewOverSomeoneElsesIndex(t *testing.T) {
	ctx := context.Background()
	owner, _ := openBM25Tree(t, labelFiles, engine.Options{})
	if _, err := owner.Definition(ctx, "Alpha"); err != nil { // build the structural index
		t.Fatal(err)
	}

	view := engine.WrapIndex(owner.WatchedIndex(), structuralOf{owner})
	if view.Root() != owner.Root() || view.CurrentMode() != engine.ModeBM25 || view.Warming() {
		t.Errorf("view root/mode/warming = %q/%v/%v", view.Root(), view.CurrentMode(), view.Warming())
	}

	want, _, err := owner.SearchResults(ctx, "Alpha Beta", 5, engine.ModeBM25, engine.SearchOptions{})
	if err != nil || len(want) == 0 {
		t.Fatalf("owner search: %d results, %v", len(want), err)
	}
	got, _, err := view.SearchResults(ctx, "Alpha Beta", 5, engine.ModeBM25, engine.SearchOptions{})
	if err != nil || len(got) != len(want) {
		t.Fatalf("view search: %d results, %v; want %d", len(got), err, len(want))
	}
	for i := range got {
		if got[i].Chunk.File != want[i].Chunk.File || got[i].Score != want[i].Score || got[i].Chunk.Text != want[i].Chunk.Text {
			t.Errorf("result %d differs between view and owner", i)
		}
	}
	if st := view.IndexStatus(); st.ChunkCount != owner.IndexStatus().ChunkCount || st.EnrichedChunks == 0 {
		t.Errorf("view IndexStatus = %+v", st)
	}

	sites, err := view.Definition(ctx, "Alpha")
	if err != nil || len(sites) != 1 || sites[0].File != "a.go" {
		t.Errorf("view Definition(Alpha) = %+v, %v", sites, err)
	}
	if entries, indexed, err := view.OutlineOfFile(ctx, "a.go"); err != nil || !indexed || len(entries) == 0 {
		t.Errorf("view OutlineOfFile(a.go) = %d entries, indexed=%v, %v", len(entries), indexed, err)
	}
	if _, indexed, _ := view.OutlineOfFile(ctx, "nope.go"); indexed {
		t.Error("OutlineOfFile reports a file the index does not hold")
	}
	if files, err := view.StructuralFilesUnder(ctx, "."); err != nil || len(files) != 1 {
		t.Errorf("view StructuralFilesUnder(.) = %v, %v", files, err)
	}
	if st, ok := view.StructuralStatus(); !ok || st.FilesByExtension[".go"] != 1 {
		t.Errorf("view StructuralStatus = %+v, %v", st, ok)
	}

	if err := view.Refresh(ctx, []string{"a.go"}, nil); !errors.Is(err, engine.ErrWrappedIndex) {
		t.Errorf("Refresh on a view = %v, want ErrWrappedIndex", err)
	}
	if err := view.WriteSnapshot(); !errors.Is(err, engine.ErrWrappedIndex) {
		t.Errorf("WriteSnapshot on a view = %v, want ErrWrappedIndex", err)
	}
	if err := view.Close(); err != nil {
		t.Fatal(err)
	}
	if res, _, _ := owner.SearchResults(ctx, "Alpha Beta", 5, engine.ModeBM25, engine.SearchOptions{}); len(res) == 0 {
		t.Error("closing the view closed the owner's index")
	}

	// With no structural source the view has no structural index, settled.
	bare := engine.WrapIndex(owner.WatchedIndex(), nil)
	if _, err := bare.Definition(ctx, "Alpha"); !errors.Is(err, engine.ErrNoStructuralIndex) {
		t.Errorf("Definition on a view with no source = %v, want ErrNoStructuralIndex", err)
	}
}

// structuralOf adapts a Workspace to engine.StructuralSource.
type structuralOf struct{ ws *engine.Workspace }

func (s structuralOf) StructuralIndex(ctx context.Context) *engine.StructuralIndex {
	idx, _ := s.ws.Structural(ctx)
	return idx
}
func (s structuralOf) StructuralPending() bool                    { return s.ws.StructuralPending() }
func (s structuralOf) StructuralIfBuilt() *engine.StructuralIndex { return s.ws.StructuralIfBuilt() }
