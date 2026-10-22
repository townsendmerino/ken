package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/townsendmerino/aikit/chunk"
	"github.com/townsendmerino/ken/internal/search"

	_ "github.com/townsendmerino/aikit/chunk/regex"
)

// repoBuilder.Build was extracted from a ~15-variable closure inside main
// specifically so it could get isolated tests like its neighbors
// (loadOrBuildWatched, resolveStartupMode, setupReranker). These exercise the
// local-path bm25 path — no model, no network — end to end.

// A local bm25 build returns a usable bundle: non-empty retrieval index, nil
// cleanup (local repo, so no temp dir to rm-rf), and a wired structural
// builder.
func TestRepoBuilder_Build_LocalBM25(t *testing.T) {
	dir := writeCorpus(t)
	rb := &repoBuilder{
		logger:   quietLogger(),
		bs:       &buildState{mode: search.ModeBM25, modeStr: "bm25"},
		chunker:  "regex",
		dbExtras: &dbExtras{},
	}
	bundle, cleanup, err := rb.Build(context.Background(), dir)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if cleanup != nil {
		t.Errorf("local-path build should have nil cleanup (no temp clone to reap)")
		cleanup()
	}
	if bundle == nil || bundle.Index == nil {
		t.Fatal("Build returned a nil bundle/index")
	}
	defer func() { _ = bundle.Index.Close() }()
	if bundle.Index.Len() == 0 {
		t.Errorf("built index is empty")
	}
	if bundle.Structural == nil {
		t.Errorf("a live build's bundle should take its structural index from the engine Workspace")
	}
}

// ken-mcp's structural tools follow edits (ADR-048): the bundle's structural
// index comes from the engine Workspace, which every watcher flush updates,
// so a span moved by an edit is seen without a restart. Before, the bundle
// built its structural index once and kept it.
func TestRepoBuilder_Build_StructuralFollowsEdits(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	if err := os.WriteFile(path, []byte("package a\n\nfunc Target() int { return 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rb := &repoBuilder{
		logger:   quietLogger(),
		bs:       &buildState{mode: search.ModeBM25, modeStr: "bm25"},
		chunker:  "regex",
		dbExtras: &dbExtras{},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	bundle, _, err := rb.Build(ctx, dir)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer func() { _ = bundle.Structural.Close() }()

	line := func() int {
		sidx := bundle.StructuralIndex(ctx)
		if sidx == nil {
			t.Fatal("no structural index")
		}
		sites := sidx.Definition("Target")
		if len(sites) != 1 {
			t.Fatalf("Definition(Target) = %+v", sites)
		}
		return sites[0].Line
	}
	if got := line(); got != 3 {
		t.Fatalf("before the edit: Target at line %d, want 3", got)
	}
	if bundle.StructuralIfBuilt() == nil {
		t.Error("StructuralIfBuilt is nil after a structural call")
	}

	if err := os.WriteFile(path, []byte("package a\n\n// moved\n// down\nfunc Target() int { return 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for line() != 5 {
		if time.Now().After(deadline) {
			t.Fatalf("the watcher never moved Target in ken-mcp's structural index (still line %d)", line())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// When this build is for the pinned default repo, DB chunks already published
// into the shared dbExtras holder are re-applied to the freshly built index
// (the Tier-2 self-heal path). A non-default source must NOT inherit them.
func TestRepoBuilder_Build_ReappliesDBExtrasForDefaultRepo(t *testing.T) {
	dir := writeCorpus(t)
	extras := &dbExtras{}
	extras.store([]chunk.Chunk{{File: "db://schema", Text: "CREATE TABLE t (id int)"}})

	rb := &repoBuilder{
		logger:       quietLogger(),
		bs:           &buildState{mode: search.ModeBM25, modeStr: "bm25"},
		chunker:      "regex",
		dbDefaultKey: dir, // Build compares source against this
		dbExtras:     extras,
	}

	// source == dbDefaultKey ⇒ extras re-applied.
	bundle, _, err := rb.Build(context.Background(), dir)
	if err != nil {
		t.Fatalf("Build(default): %v", err)
	}
	defer func() { _ = bundle.Index.Close() }()
	withExtras := bundle.Index.Len()

	// A different source ⇒ extras NOT applied, so a strictly smaller corpus.
	other := writeCorpus(t)
	rb2 := &repoBuilder{
		logger:       quietLogger(),
		bs:           &buildState{mode: search.ModeBM25, modeStr: "bm25"},
		chunker:      "regex",
		dbDefaultKey: dir, // still the first dir, so `other` is non-default
		dbExtras:     extras,
	}
	bundle2, _, err := rb2.Build(context.Background(), other)
	if err != nil {
		t.Fatalf("Build(other): %v", err)
	}
	defer func() { _ = bundle2.Index.Close() }()
	withoutExtras := bundle2.Index.Len()

	if withExtras <= withoutExtras {
		t.Errorf("default-repo build (%d chunks) should exceed non-default build (%d) by the DB extras",
			withExtras, withoutExtras)
	}
}
