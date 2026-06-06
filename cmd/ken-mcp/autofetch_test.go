package main

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/townsendmerino/ken/internal/search"
	kenmcp "github.com/townsendmerino/ken/mcp"
)

// fakeCache builds a trivial bm25 WatchedIndex per key and counts builds,
// so a test can observe whether autoFetchModel's Purge + re-warm forced a
// rebuild. watch=false keeps it goroutine-free.
func fakeCache(t *testing.T) (*kenmcp.Cache, *atomic.Int64) {
	t.Helper()
	var builds atomic.Int64
	c := kenmcp.NewCache(8, func(_ context.Context, _ string) (*kenmcp.RepoBundle, func(), error) {
		builds.Add(1)
		ix, err := search.NewWatchedIndex(t.TempDir(), search.ModeBM25, "line", "", false)
		if err != nil {
			return nil, nil, err
		}
		return &kenmcp.RepoBundle{Index: ix}, nil, nil
	})
	t.Cleanup(c.Close)
	return c, &builds
}

func TestAutoFetchModel_Success_FlipsHybridAndRebuilds(t *testing.T) {
	cache, builds := fakeCache(t)
	repo := t.TempDir()
	if _, err := cache.Get(context.Background(), repo); err != nil { // warm: builds==1
		t.Fatal(err)
	}
	bs := &buildState{mode: search.ModeBM25, modeStr: "bm25", modelDir: ""}
	logger, buf := newCapturedLogger()
	fetch := func(context.Context, string) (int, error) { return 3, nil }

	autoFetchModel(context.Background(), bs, search.ModeHybrid, "hybrid", "/tmp/m", false, cache, repo, fetch, logger)

	if m, ms, md := bs.snapshot(); m != search.ModeHybrid || ms != "hybrid" || md != "/tmp/m" {
		t.Errorf("buildState = %v/%q/%q, want hybrid//tmp/m", m, ms, md)
	}
	// Purge evicted the bm25 entry; background re-warm rebuilt it → builds==2.
	if got := builds.Load(); got != 2 {
		t.Errorf("builds=%d, want 2 (purge + re-warm)", got)
	}
	if !strings.Contains(buf.String(), "hybrid enabled") {
		t.Errorf("expected an upgrade log line; got %q", buf.String())
	}
}

func TestAutoFetchModel_FetchFailure_StaysBM25(t *testing.T) {
	cache, builds := fakeCache(t)
	repo := t.TempDir()
	if _, err := cache.Get(context.Background(), repo); err != nil {
		t.Fatal(err)
	}
	bs := &buildState{mode: search.ModeBM25, modeStr: "bm25", modelDir: ""}
	logger, buf := newCapturedLogger()
	fetch := func(context.Context, string) (int, error) { return 0, errors.New("offline") }

	autoFetchModel(context.Background(), bs, search.ModeHybrid, "hybrid", "/tmp/m", false, cache, repo, fetch, logger)

	if m, _, _ := bs.snapshot(); m != search.ModeBM25 {
		t.Errorf("buildState mode = %v, want bm25 (fetch failed)", m)
	}
	if got := builds.Load(); got != 1 {
		t.Errorf("builds=%d, want 1 (no purge on failure)", got)
	}
	if !strings.Contains(buf.String(), "download failed") {
		t.Errorf("expected a failure log line; got %q", buf.String())
	}
}

func TestAutoFetchModel_WithDB_FlipsButDoesNotPurge(t *testing.T) {
	cache, builds := fakeCache(t)
	repo := t.TempDir()
	if _, err := cache.Get(context.Background(), repo); err != nil {
		t.Fatal(err)
	}
	bs := &buildState{mode: search.ModeBM25, modeStr: "bm25", modelDir: ""}
	logger, buf := newCapturedLogger()
	fetch := func(context.Context, string) (int, error) { return 3, nil }

	autoFetchModel(context.Background(), bs, search.ModeHybrid, "hybrid", "/tmp/m", true /* hasDB */, cache, repo, fetch, logger)

	if m, _, _ := bs.snapshot(); m != search.ModeHybrid {
		t.Errorf("buildState mode = %v, want hybrid (flip happens even with DB)", m)
	}
	// DB attached: no purge/re-warm, so the cached index is untouched.
	if got := builds.Load(); got != 1 {
		t.Errorf("builds=%d, want 1 (DB case skips purge)", got)
	}
	if !strings.Contains(buf.String(), "restart ken-mcp") {
		t.Errorf("expected a restart-prompt log line; got %q", buf.String())
	}
}
