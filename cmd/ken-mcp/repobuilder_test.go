package main

import (
	"context"
	"testing"

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
	if bundle.StructuralBuilder == nil {
		t.Errorf("bundle should carry a structural builder")
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
