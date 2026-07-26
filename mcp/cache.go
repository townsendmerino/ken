package mcp

import (
	"container/list"
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/townsendmerino/ken/internal/search"
	"github.com/townsendmerino/ken/internal/structural"
)

// DefaultCacheSize is the LRU bound when KEN_MCP_CACHE_SIZE is unset.
const DefaultCacheSize = 16

// RepoBundle is the per-repo data the cache holds: the retrieval-side
// WatchedIndex (BM25 + dense + reranker) plus the Stage-8 structural
// symbol index over the same corpus.
//
// The structural index is built LAZILY on first use (the
// definition/references/callers/outline/symbols tools), not at bundle
// creation. Cold-start M0 (docs/internal/cold-start-M0-findings.md)
// measured that the per-file tree-sitter parse the structural build
// performs is ~50% of index time on PHP corpora — paying it eagerly at
// startup, on top of the enrichment pass that already parses every file,
// doubled the cold-start parse cost for a symbol index most sessions
// never query. Deferring it follows the ADR-036 lazy-rerank precedent.
// StructuralIndex() returns nil (not panic) when there is no builder or
// the build failed/was unsupported — every tool handler defends against
// nil and degrades to a clean error.
//
// Lifecycle: the cache owns both indices. Index.Close() is called
// on eviction to stop the watcher goroutine; the structural index needs
// no teardown (it's plain maps that GC).
type RepoBundle struct {
	Index *search.WatchedIndex

	// StructuralBuilder constructs the structural symbol index over the
	// repo's corpus. nil ⇒ the bundle has no structural support (it stays
	// unset for repos/tests that don't wire one). Invoked by the first
	// StructuralIndex() call; on error it is retried on the next call
	// (audit db/mcp §8) — only a successful build is cached. The context
	// lets a client cancellation / timeout abort the whole-corpus parse.
	StructuralBuilder func(context.Context) (*structural.Index, error)

	structuralMu       sync.Mutex  // guards the build-state fields below
	structuralBuilt    atomic.Bool // set once a build succeeds
	structuralIdx      *structural.Index
	structuralBuilding bool               // a background build is in flight
	structuralDone     chan struct{}      // closed when the in-flight build finishes
	structuralCancel   context.CancelFunc // cancels the in-flight build (audit N7)
	structuralFailedAt time.Time          // last failure; gates the retry cooldown
}

// Structural-build timing knobs (audit R3).
const (
	// structuralBuildBudget bounds a single background build. Detached from
	// any caller's context so one client's short tool-call timeout can't
	// kill (and endlessly re-trigger) a large whole-corpus parse.
	structuralBuildBudget = 10 * time.Minute
	// structuralFirstWait is how long a caller BLOCKS for an in-flight build
	// before degrading to "still building". Small repos finish well inside
	// it (first-call result preserved); large repos hand the caller back
	// promptly and finish in the background for the next call.
	structuralFirstWait = 10 * time.Second
)

// structuralFailCooldown throttles retries after a failed build so an agent
// calling structural tools in a loop can't drive a fresh whole-corpus parse
// on every call. A var (not const) only so tests can shorten it; production
// never reassigns it.
var structuralFailCooldown = 60 * time.Second

// StructuralIndex returns the structural symbol index, or nil if it isn't
// ready yet. A SUCCESSFUL build is cached for the process lifetime; a
// failed/cancelled one is not (audit db/mcp §8) but is rate-limited by a
// cooldown so retries can't hammer the parse (audit R3).
//
// The build runs in a BACKGROUND goroutine under an independent budget
// (structuralBuildBudget), NOT the caller's context (audit R3): otherwise a
// build that outlives one client's tool-call timeout is cancelled, cached
// as nothing, and retried forever — the index never converges on a large
// repo. The caller blocks up to structuralFirstWait (or its own deadline)
// so a fast build still answers on the first call; a slower one returns nil
// ("still building") while the background build finishes for the next call.
// Concurrent callers join the same in-flight build.
//
// Returns nil (never panics) when no builder is wired, the build is still
// running, it failed, or it found no supported files — handlers defend
// against nil and can call StructuralPending to distinguish "building".
func (b *RepoBundle) StructuralIndex(ctx context.Context) *structural.Index {
	if b.StructuralBuilder == nil {
		return nil
	}
	if b.structuralBuilt.Load() {
		return b.structuralIdx
	}

	b.structuralMu.Lock()
	if b.structuralBuilt.Load() {
		b.structuralMu.Unlock()
		return b.structuralIdx
	}
	if !b.structuralBuilding {
		if !b.structuralFailedAt.IsZero() && time.Since(b.structuralFailedAt) < structuralFailCooldown {
			b.structuralMu.Unlock()
			return nil // recent failure — don't re-trigger the parse yet
		}
		b.structuralBuilding = true
		b.structuralDone = make(chan struct{})
		// Own the build's lifecycle (audit N7): a cancelable, background-rooted
		// ctx (NOT the caller's — R3) so reapEntry/Close can stop it before
		// cleanup() rm-rf's the corpus it's parsing.
		bctx, cancel := context.WithTimeout(context.Background(), structuralBuildBudget)
		b.structuralCancel = cancel
		go b.runStructuralBuild(bctx, b.structuralDone)
	}
	done := b.structuralDone
	b.structuralMu.Unlock()

	wait := structuralFirstWait
	if dl, ok := ctx.Deadline(); ok {
		if d := time.Until(dl) - 100*time.Millisecond; d < wait {
			wait = d
		}
	}
	if wait < 0 {
		wait = 0
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-done:
		return b.structuralIdx // nil if the build failed
	case <-timer.C:
		return nil // still building — caller should retry shortly
	case <-ctx.Done():
		return nil // caller gave up; the background build continues
	}
}

// runStructuralBuild executes one build under an independent budget, caches
// success, records failure time, and wakes waiters. Runs in its own
// goroutine (audit R3).
func (b *RepoBundle) runStructuralBuild(ctx context.Context, done chan struct{}) {
	idx, err := b.StructuralBuilder(ctx)

	b.structuralMu.Lock()
	if err != nil {
		b.structuralFailedAt = time.Now()
	} else {
		b.structuralIdx = idx
		b.structuralBuilt.Store(true) // publish last; happens-before close(done)
	}
	b.structuralBuilding = false
	if b.structuralCancel != nil {
		b.structuralCancel() // release the ctx's timer resources
		b.structuralCancel = nil
	}
	b.structuralMu.Unlock()
	close(done)
}

// stopStructuralBuild cancels an in-flight structural build and waits for it
// to exit (audit N7). Called from reapEntry before cleanup() rm-rf's the
// corpus dir the build is parsing, and by Close, so no build goroutine
// outlives the bundle (goleak) or reads a deleted temp clone. No-op when no
// build is running. BuildWithContext honors cancellation at the next file
// boundary, so the wait is bounded.
func (b *RepoBundle) stopStructuralBuild() {
	b.structuralMu.Lock()
	done := b.structuralDone
	if b.structuralCancel != nil {
		b.structuralCancel()
	}
	building := b.structuralBuilding
	b.structuralMu.Unlock()
	if building && done != nil {
		<-done
	}
}

// StructuralPending reports whether a structural build is currently running
// (or a fresh call would start one because there's no cached result and no
// active cooldown). Handlers use it to tell an agent "still building, retry
// shortly" apart from "this corpus has no structural index" (audit R3).
func (b *RepoBundle) StructuralPending() bool {
	if b.StructuralBuilder == nil || b.structuralBuilt.Load() {
		return false
	}
	b.structuralMu.Lock()
	defer b.structuralMu.Unlock()
	if b.structuralBuilding {
		return true
	}
	// No build running: pending iff we're not inside a failure cooldown (a
	// call now would start one).
	return b.structuralFailedAt.IsZero() || time.Since(b.structuralFailedAt) >= structuralFailCooldown
}

// StructuralIfBuilt returns the structural index only if a prior
// StructuralIndex() call already built it; it never triggers a build. The
// status tool uses this so a `status` query doesn't force the lazy (and, on
// large corpora, expensive) structural parse just to report on it.
func (b *RepoBundle) StructuralIfBuilt() *structural.Index {
	if b.structuralBuilt.Load() {
		return b.structuralIdx
	}
	return nil
}

// Builder constructs the per-repo state for an already-normalized
// source identifier (either a canonical http(s) URL or an absolute
// filesystem path). The returned cleanup is called when the entry
// is evicted from the cache — used to rm -rf temp clone dirs;
// pass nil for local-path entries.
//
// As of v0.3, the returned *search.WatchedIndex wraps the index plus
// a file-watcher goroutine. The cache calls (*WatchedIndex).Close()
// on eviction (and on Cache.Close()) to stop the watcher before
// invoking the user-supplied cleanup; without this the goroutine
// outlives the cache entry and the temp clone dir gets rm-rf'd while
// the watcher holds inotify fds pointing into it.
//
// As of Stage 8 the bundle also carries a structural symbol index over
// the same corpus. It was originally built eagerly here; cold-start M0
// (docs/internal/cold-start-M0-findings.md) showed that doubled the
// per-file tree-sitter parse cost at startup (the enrichment pass already
// parses every file), so the Builder now wires RepoBundle.StructuralBuilder
// and the index is built lazily on first structural-tool use instead.
type Builder func(ctx context.Context, source string) (*RepoBundle, func(), error)

type cacheEntry struct {
	key     string
	bundle  *RepoBundle
	cleanup func()
}

// Cache is the per-process repo→Index cache that backs the MCP server.
// Concurrent uncached requests for the same key dedupe via singleflight,
// and entries are LRU-evicted at the configured bound.
type Cache struct {
	mu       sync.Mutex
	max      int
	ll       *list.List // front = most recently used
	items    map[string]*list.Element
	build    Builder
	sf       singleflight.Group
	closed   bool                // M8: set under mu by Close(); checked by builders to avoid use-after-close
	gen      uint64              // audit R7: bumped by Purge(); a build started under an older gen must not repopulate
	inflight map[string]struct{} // audit N6: keys with a build currently in singleflight, so Purge can Forget them
}

// errPurgedDuringBuild is returned when a build's generation no longer
// matches (a Purge ran while it built). GetBundle retries once internally
// on it (audit N6) rather than surfacing it to the caller.
var errPurgedDuringBuild = fmt.Errorf("repo: cache purged during build; retry")

// NewCache creates a cache bound to max entries (≤0 ⇒ DefaultCacheSize).
//
// Stability: 1.0-stable. The signature + [Cache]'s public method set
// (Get / GetBundle / Len / Capacity / Close) are committed across
// 1.0+ minors. The [Builder] callback shape is also stable.
func NewCache(max int, build Builder) *Cache {
	if max <= 0 {
		max = DefaultCacheSize
	}
	return &Cache{max: max, ll: list.New(), items: map[string]*list.Element{}, build: build, inflight: map[string]struct{}{}}
}

// Len is the number of cached entries (used by tests).
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

// Capacity returns the LRU bound. Used by the `status` MCP tool to
// surface the cache's configured size; pairs with Len for the
// in-use / capacity display.
func (c *Cache) Capacity() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.max
}

// detachAll removes every entry from the map + list under c.mu (the caller
// must hold it) and returns them for the caller to reap AFTER unlocking.
// Reaping (Index.Close watcher-drain + cleanup rm -rf) blocks and must never
// run under c.mu — see M2 in GetBundle.
func (c *Cache) detachAll() []*cacheEntry {
	ents := make([]*cacheEntry, 0, len(c.items))
	for e := c.ll.Front(); e != nil; e = e.Next() {
		ents = append(ents, e.Value.(*cacheEntry))
	}
	c.items = map[string]*list.Element{}
	c.ll.Init()
	return ents
}

func reapEntry(ent *cacheEntry) {
	if ent.bundle != nil {
		// Stop any in-flight structural build BEFORE cleanup rm-rf's the
		// corpus dir it's parsing (audit N7) — prevents a wasted 10-min parse
		// of a deleted tree and a goroutine outliving the cache.
		ent.bundle.stopStructuralBuild()
		if ent.bundle.Index != nil {
			_ = ent.bundle.Index.Close()
		}
	}
	if ent.cleanup != nil {
		ent.cleanup()
	}
}

// Purge evicts every cached entry — stopping each watcher (wix.Close())
// and running its cleanup (rm -rf for temp clones) — but, unlike Close,
// leaves the cache OPEN so subsequent Get() calls rebuild on demand.
//
// Used by ken-mcp's auto-fetch upgrade path: once the embedding model
// lands in the background, purging forces the next Get to rebuild each
// repo's index WITH embeddings (hybrid) instead of continuing to serve
// the bm25 index built while the model was absent (search reads the
// index's own mode per query, so a rebuilt hybrid index upgrades search
// automatically). In-flight queries that already hold a bundle keep
// reading their consistent snapshot; only new Get calls rebuild — same
// safety profile as LRU eviction.
func (c *Cache) Purge() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	// Bump the generation (audit R7): a build already in flight when Purge
	// runs must NOT insert its now-stale bundle into the "purged" cache — the
	// post-build critical section in GetBundle compares the gen it started
	// under against this and reaps on mismatch. Without it, autoFetchModel's
	// purge (bm25→hybrid upgrade) could be undone by an in-flight bm25 build
	// landing afterward, serving bm25 for the process lifetime.
	c.gen++
	ents := c.detachAll()
	// Forget the IN-FLIGHT singleflight turns (audit N6): the key that most
	// needs forgetting is a FIRST build not yet in c.items — exactly the
	// autoFetchModel case — which detachAll() (cached entries only) misses.
	// Forgetting under c.mu means new Gets start a fresh turn under the new
	// gen instead of joining the doomed pre-purge build.
	for k := range c.inflight {
		c.sf.Forget(k)
	}
	c.mu.Unlock()
	for _, ent := range ents {
		reapEntry(ent) // M2: blocking close+cleanup outside the lock
	}
}

// scpishURL catches `user@host:path` SCP-form git URLs (semble's MCP
// rejects these; only http(s) is allowed via the MCP boundary).
var scpishURL = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:.+`)

// NormalizeKey canonicalizes a user-supplied repo string into a cache
// key. https/http URLs keep their scheme but get lowercased host + a
// trailing ".git" and "/" stripped; other URL-shaped inputs (anything
// containing "://" or matching SCP-form `user@host:path`) are
// rejected with an error, matching semble/mcp.py's http(s)-only
// guard. Inputs with no `://` are treated as local paths and
// resolved to an absolute path (existence is not checked here;
// defer to the Builder).
//
// L1 hardening: previously this was an allow-list-then-default-to-
// local-path pattern, which meant `file:///etc`, `ftp://host/`, or
// any other unknown scheme silently degraded to a local-path resolve
// (producing a junky absolute path that confused the Builder error).
// The scheme allow-list is now the security boundary: anything URL-
// shaped that isn't https/http is rejected with a typed error.
//
// Stability: best-effort (NOT part of the 1.0 hard-committed
// surface). External consumers writing custom Cache Builders can
// use it; semantics may evolve if the source-key scheme grows
// (e.g. SSH URLs, registry-shaped paths).
func NormalizeKey(source string) (string, bool, error) {
	src := strings.TrimSpace(source)
	if src == "" {
		return "", false, fmt.Errorf("repo: empty source")
	}
	lower := strings.ToLower(src)
	switch {
	case strings.HasPrefix(lower, "https://"), strings.HasPrefix(lower, "http://"):
		u, err := url.Parse(src)
		if err != nil {
			return "", true, fmt.Errorf("repo: parse %q: %w", src, err)
		}
		u.Host = strings.ToLower(u.Host)
		p := strings.TrimSuffix(u.Path, "/")
		p = strings.TrimSuffix(p, ".git")
		u.Path = p
		u.Fragment, u.RawQuery = "", ""
		return u.String(), true, nil
	case strings.Contains(src, "://"):
		// Any URL-shaped input with a non-http(s) scheme — ssh://,
		// git://, git+ssh://, file://, ftp://, anything else — is
		// rejected. Tighter than the prior named-scheme allow-list:
		// unknown schemes no longer fall through to filepath.Abs.
		return "", true, fmt.Errorf("repo: only https://, http://, or local paths are accepted (got %q)", src)
	case scpishURL.MatchString(src):
		return "", true, fmt.Errorf("repo: SCP-form git URLs are not accepted (got %q); use https://", src)
	default:
		abs, err := filepath.Abs(src)
		if err != nil {
			return "", false, fmt.Errorf("repo: resolve %q: %w", src, err)
		}
		return abs, false, nil
	}
}

// Get returns a cached WatchedIndex for source, building it once on
// first access. Concurrent first-access calls for the same key share
// a single build via singleflight. The returned *WatchedIndex is
// shared across all callers and across subsequent Get calls until
// evicted; callers MUST NOT call wix.Close() themselves — the cache
// owns the lifecycle.
//
// Back-compat: returns just the WatchedIndex from the cached bundle,
// matching the pre-Stage-8 signature. Tool handlers that also need
// the structural index call GetBundle instead.
func (c *Cache) Get(ctx context.Context, source string) (*search.WatchedIndex, error) {
	b, err := c.GetBundle(ctx, source)
	if err != nil {
		return nil, err
	}
	return b.Index, nil
}

// GetBundle returns the cached RepoBundle (WatchedIndex + lazy structural
// index) for source, building the retrieval index on first access. Used by
// Track 2 tool handlers that need the structural index alongside the
// retrieval one; they call bundle.StructuralIndex(), which builds the
// symbol index lazily on first use.
//
// Cache + lifecycle semantics are identical to Get — singleflight on
// first build, LRU eviction, c.closed observed during in-flight
// builds. bundle.StructuralIndex() may return nil if the structural build
// produced no entries (unsupported corpus language); handlers must
// defend against that.
func (c *Cache) GetBundle(ctx context.Context, source string) (*RepoBundle, error) {
	key, _, err := NormalizeKey(source)
	if err != nil {
		return nil, err
	}
	// Retry once on a purge-during-build (audit N6): the first attempt's build
	// was invalidated by a concurrent Purge, so a second attempt builds fresh
	// under the new generation instead of surfacing "purged; retry" to the
	// agent. Bounded to 2 tries so a purge storm can't loop.
	for attempt := 0; ; attempt++ {
		b, err := c.getBundleOnce(ctx, key)
		if err == errPurgedDuringBuild && attempt < 1 {
			continue
		}
		return b, err
	}
}

func (c *Cache) getBundleOnce(ctx context.Context, key string) (*RepoBundle, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, fmt.Errorf("repo: cache is closed")
	}
	if e, ok := c.items[key]; ok {
		c.ll.MoveToFront(e)
		b := e.Value.(*cacheEntry).bundle
		c.mu.Unlock()
		return b, nil
	}
	c.mu.Unlock()

	v, err, _ := c.sf.Do(key, func() (any, error) {
		// Snapshot the generation this build starts under (audit R7) and mark
		// the key in-flight so Purge can Forget it (audit N6). If a Purge bumps
		// the gen while we build, the bundle is stale and must not be inserted.
		c.mu.Lock()
		startGen := c.gen
		c.inflight[key] = struct{}{}
		c.mu.Unlock()
		defer func() {
			c.mu.Lock()
			delete(c.inflight, key)
			c.mu.Unlock()
		}()

		bundle, cleanup, err := c.build(ctx, key)
		if err != nil {
			return nil, err
		}
		// M2 (code review §3): Index.Close() blocks draining the watcher
		// goroutine and cleanup() may os.RemoveAll 100s of MB. Never run
		// either under c.mu — every concurrent search/status/GetBundle
		// waits on the lock for the full duration. So under the lock we only
		// mutate the map/list and DETACH the entries to reap; the blocking
		// close+cleanup happens after Unlock via reap().
		reap := func(idx *search.WatchedIndex, clean func()) {
			if idx != nil {
				_ = idx.Close()
			}
			if clean != nil {
				clean()
			}
		}

		c.mu.Lock()
		// M8 / R7: a Close() or Purge() that fired while the build was in
		// flight has already drained the map. Don't repopulate it — reap the
		// just-built watcher + cleanup so they don't outlive the cache (Close)
		// or resurrect a pre-purge bundle (Purge).
		if c.closed {
			c.mu.Unlock()
			reap(bundle.Index, cleanup)
			return nil, fmt.Errorf("repo: cache is closed")
		}
		if c.gen != startGen {
			c.mu.Unlock()
			reap(bundle.Index, cleanup)
			return nil, errPurgedDuringBuild
		}
		// Re-check in case another sf turn populated it. If we lost the
		// race, the cache already has a usable entry; reap the loser.
		if e, ok := c.items[key]; ok {
			c.ll.MoveToFront(e)
			b := e.Value.(*cacheEntry).bundle
			c.mu.Unlock()
			reap(bundle.Index, cleanup)
			return b, nil
		}
		var evicted []*cacheEntry
		for len(c.items) >= c.max {
			tail := c.ll.Back()
			if tail == nil {
				break
			}
			ev := c.ll.Remove(tail).(*cacheEntry)
			delete(c.items, ev.key)
			evicted = append(evicted, ev) // detach only; reap after unlock
		}
		ent := &cacheEntry{key: key, bundle: bundle, cleanup: cleanup}
		c.items[key] = c.ll.PushFront(ent)
		c.mu.Unlock()
		for _, ev := range evicted {
			reap(ev.bundle.Index, ev.cleanup)
		}
		return bundle, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*RepoBundle), nil
}

// Close releases every cached entry. Stops the watcher goroutine for
// each (wix.Close()) and runs the user-supplied cleanup (rm -rf for
// temp clones). Safe to call multiple times.
//
// M8: sets c.closed under c.mu before draining the map. Concurrent
// Get() calls whose singleflight build is in flight observe the
// flag once they re-acquire the lock and reap the just-built
// watcher rather than repopulating the now-cleared map. This
// avoids a use-after-close where a stale entry survives past
// Close() and outlives the cache's intent.
func (c *Cache) Close() {
	c.mu.Lock()
	c.closed = true
	ents := c.detachAll()
	c.mu.Unlock()
	for _, ent := range ents {
		reapEntry(ent) // M2: blocking close+cleanup outside the lock
	}
}
