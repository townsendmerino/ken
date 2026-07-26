package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/townsendmerino/aikit/chunk"

	"github.com/townsendmerino/ken/internal/search"
	kenmcp "github.com/townsendmerino/ken/mcp"
)

// TestDBExtras_StoreLoad is the basic holder contract.
func TestDBExtras_StoreLoad(t *testing.T) {
	h := &dbExtras{}
	if got := h.load(); got != nil {
		t.Fatalf("fresh holder load = %v, want nil", got)
	}
	cs := []chunk.Chunk{{File: "db://public.users", Text: "CREATE TABLE users"}}
	h.store(cs)
	if got := h.load(); len(got) != 1 || got[0].File != "db://public.users" {
		t.Fatalf("load after store = %v, want the stored chunk", got)
	}
	h.store(nil)
	if got := h.load(); got != nil {
		t.Fatalf("load after store(nil) = %v, want nil", got)
	}
}

// TestTier2_SurvivesEviction is the audit db/mcp §1 regression: a builder
// that re-applies the dbExtras holder (mirroring the production builder's
// apply block) makes a rebuilt-after-eviction default repo re-inherit its
// DB chunks, instead of silently serving FS-only.
func TestTier2_SurvivesEviction(t *testing.T) {
	defaultDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(defaultDir, "a.py"), []byte("def alpha():\n    return 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	otherDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(otherDir, "b.py"), []byte("def beta():\n    return 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	holder := &dbExtras{}
	defaultKey, _, err := kenmcp.NormalizeKey(defaultDir)
	if err != nil {
		t.Fatalf("NormalizeKey: %v", err)
	}

	// Builder mirrors the production one's §1 apply block: after building
	// the index, if this is the pinned default repo, re-apply holder extras.
	builder := func(ctx context.Context, source string) (*kenmcp.RepoBundle, func(), error) {
		ix, err := search.NewWatchedIndex(source, search.ModeBM25, "regex", "", false)
		if err != nil {
			return nil, nil, err
		}
		if source == defaultKey {
			if extras := holder.load(); len(extras) > 0 {
				ix.SetExtraChunks(extras)
			}
		}
		return &kenmcp.RepoBundle{Index: ix}, nil, nil
	}

	cache := kenmcp.NewCache(1, builder) // size 1 → any other repo evicts the default
	t.Cleanup(cache.Close)
	ctx := context.Background()

	// Startup: build default repo (no DB chunks yet).
	if _, err := cache.Get(ctx, defaultDir); err != nil {
		t.Fatalf("initial Get(default): %v", err)
	}

	// Refresher publishes DB chunks: store in holder + apply to the present index.
	dbChunk := chunk.Chunk{File: "db://public.users", Text: "CREATE TABLE users id name email"}
	holder.store([]chunk.Chunk{dbChunk})
	wix, err := cache.Get(ctx, defaultDir) // cached hit — returns the present index
	if err != nil {
		t.Fatalf("Get(default) after store: %v", err)
	}
	wix.SetExtraChunks([]chunk.Chunk{dbChunk})
	if hits := wix.Search("users", 5); len(hits) == 0 {
		t.Fatal("precondition: DB chunk should be searchable before eviction")
	}

	// Evict the default repo by fetching a different repo (cache size 1).
	if _, err := cache.Get(ctx, otherDir); err != nil {
		t.Fatalf("Get(other): %v", err)
	}

	// The bug: next Get(default) rebuilds FS-only and the DB chunk is gone.
	// The fix: the builder re-applies holder extras, so it's still there.
	rebuilt, err := cache.Get(ctx, defaultDir)
	if err != nil {
		t.Fatalf("Get(default) after eviction: %v", err)
	}
	if hits := rebuilt.Search("users", 5); len(hits) == 0 {
		t.Error("DB chunk vanished after eviction+rebuild — audit §1 not fixed")
	}
}
