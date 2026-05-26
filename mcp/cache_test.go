package mcp

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/townsendmerino/ken/internal/search"
)

// makeFakeBuilder returns a Builder that produces a unique WatchedIndex
// per key (no real chunks; we don't use Search), counts how often it's
// invoked, and records cleanups. The watcher is enabled (watch=true) so
// the test exercises the real lifecycle path — cache.Close() / eviction
// must call wix.Close() on every entry, and these tests would leak
// goroutines if it didn't.
func makeFakeBuilder(t *testing.T) (Builder, *atomic.Int64, *atomic.Int64) {
	t.Helper()
	var builds, cleanups atomic.Int64
	b := func(_ context.Context, _ string) (*search.WatchedIndex, func(), error) {
		builds.Add(1)
		// Build a trivial BM25-only WatchedIndex over a tiny tempdir.
		// watch=true so the cache exercises the Close-on-eviction
		// path; no test waits on swaps, so debounce default is fine.
		dir := t.TempDir()
		ix, err := search.NewWatchedIndex(dir, search.ModeBM25, "line", "", true)
		if err != nil {
			return nil, nil, err
		}
		return ix, func() { cleanups.Add(1) }, nil
	}
	return b, &builds, &cleanups
}

func TestCache_HitMiss(t *testing.T) {
	build, builds, _ := makeFakeBuilder(t)
	c := NewCache(8, build)
	path := t.TempDir()
	if _, err := c.Get(context.Background(), path); err != nil {
		t.Fatalf("first Get: %v", err)
	}
	if _, err := c.Get(context.Background(), path); err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if got := builds.Load(); got != 1 {
		t.Errorf("builds=%d, want 1 (second Get must hit cache)", got)
	}
	if got := c.Len(); got != 1 {
		t.Errorf("Len=%d, want 1", got)
	}
}

// TestCache_SingleflightDedupesConcurrentBuilds: two simultaneous Gets
// for the same uncached repo must coalesce into one Build invocation.
func TestCache_SingleflightDedupesConcurrentBuilds(t *testing.T) {
	var builds atomic.Int64
	start := make(chan struct{})
	b := func(ctx context.Context, _ string) (*search.WatchedIndex, func(), error) {
		builds.Add(1)
		<-start // hold until both goroutines are in-flight
		dir := t.TempDir()
		ix, _ := search.NewWatchedIndex(dir, search.ModeBM25, "line", "", true)
		return ix, nil, nil
	}
	c := NewCache(8, b)
	path := t.TempDir()

	var wg sync.WaitGroup
	wg.Add(2)
	for range 2 {
		go func() {
			defer wg.Done()
			if _, err := c.Get(context.Background(), path); err != nil {
				t.Errorf("Get: %v", err)
			}
		}()
	}
	// Let both goroutines reach the cache before releasing the build.
	time.Sleep(50 * time.Millisecond)
	close(start)
	wg.Wait()

	if got := builds.Load(); got != 1 {
		t.Errorf("builds=%d, want 1 (singleflight should coalesce)", got)
	}
}

func TestCache_KeySeparationURLvsLocalPath(t *testing.T) {
	build, builds, _ := makeFakeBuilder(t)
	c := NewCache(8, build)
	// Use a real local path AND a URL with a similar tail; they MUST
	// normalize to distinct keys.
	local := t.TempDir() // e.g. /var/folders/.../foo
	url := "https://github.com/foo/bar"
	if _, err := c.Get(context.Background(), local); err != nil {
		t.Fatalf("local Get: %v", err)
	}
	if _, err := c.Get(context.Background(), url); err != nil {
		t.Fatalf("url Get: %v", err)
	}
	if got := builds.Load(); got != 2 {
		t.Errorf("builds=%d, want 2 (local vs URL must not collide)", got)
	}
	if got := c.Len(); got != 2 {
		t.Errorf("Len=%d, want 2", got)
	}
}

func TestCache_LRUEvictionRunsCleanup(t *testing.T) {
	build, builds, cleanups := makeFakeBuilder(t)
	c := NewCache(2, build) // bound = 2
	a, b, d := t.TempDir(), t.TempDir(), t.TempDir()
	for _, p := range []string{a, b, d} {
		if _, err := c.Get(context.Background(), p); err != nil {
			t.Fatalf("Get %s: %v", p, err)
		}
	}
	if got := builds.Load(); got != 3 {
		t.Errorf("builds=%d, want 3", got)
	}
	if got := c.Len(); got != 2 {
		t.Errorf("Len=%d, want 2 (bounded)", got)
	}
	if got := cleanups.Load(); got != 1 {
		t.Errorf("cleanups=%d, want 1 (a evicted)", got)
	}
}

func TestNormalizeKey(t *testing.T) {
	// http(s) URLs: lowercase host, strip trailing slash and .git.
	got, isURL, err := NormalizeKey("https://GitHub.com/Foo/Bar.git/")
	if err != nil || !isURL || got != "https://github.com/Foo/Bar" {
		t.Errorf("URL normalize: got=%q isURL=%v err=%v", got, isURL, err)
	}
	// ssh:// / SCP-form are rejected at the MCP boundary.
	if _, _, err := NormalizeKey("ssh://git@github.com/foo/bar.git"); err == nil {
		t.Error("ssh:// URL must be rejected")
	}
	if _, _, err := NormalizeKey("git@github.com:foo/bar.git"); err == nil {
		t.Error("SCP-form URL must be rejected")
	}
	// Local paths become absolute.
	tmp := t.TempDir()
	rel := filepath.Base(tmp)
	got, isURL, err = NormalizeKey(tmp)
	if err != nil || isURL || !strings.HasSuffix(got, rel) {
		t.Errorf("local normalize: got=%q isURL=%v err=%v", got, isURL, err)
	}

	// L1 hardening: any URL-shaped input with a non-http(s) scheme
	// is rejected (not silently degraded to filepath.Abs of the
	// junky string). The scheme allow-list is the security
	// boundary; previously file:// / ftp:// fell through to
	// filepath.Abs and produced a confusing local-path error.
	for _, badURL := range []string{
		"file:///etc/passwd",
		"ftp://example.com/path",
		"gopher://example.com/",
		"customscheme://anything",
	} {
		if _, _, err := NormalizeKey(badURL); err == nil {
			t.Errorf("URL with non-http(s) scheme should be rejected: %q", badURL)
		}
	}
}
