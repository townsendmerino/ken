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

	"golang.org/x/sync/singleflight"

	"github.com/townsendmerino/ken/internal/search"
)

// DefaultCacheSize is the LRU bound when KEN_MCP_CACHE_SIZE is unset.
const DefaultCacheSize = 16

// Builder constructs an Index for an already-normalized source identifier
// (either a canonical http(s) URL or an absolute filesystem path). The
// returned cleanup is called when the entry is evicted from the cache —
// used to rm -rf temp clone dirs; pass nil for local-path entries.
//
// As of v0.3, the returned *search.WatchedIndex wraps the index plus a
// file-watcher goroutine. The cache calls (*WatchedIndex).Close() on
// eviction (and on Cache.Close()) to stop the watcher before invoking
// the user-supplied cleanup; without this the goroutine outlives the
// cache entry and the temp clone dir gets rm-rf'd while the watcher
// holds inotify fds pointing into it.
type Builder func(ctx context.Context, source string) (*search.WatchedIndex, func(), error)

type cacheEntry struct {
	key     string
	ix      *search.WatchedIndex
	cleanup func()
}

// Cache is the per-process repo→Index cache that backs the MCP server.
// Concurrent uncached requests for the same key dedupe via singleflight,
// and entries are LRU-evicted at the configured bound.
type Cache struct {
	mu    sync.Mutex
	max   int
	ll    *list.List // front = most recently used
	items map[string]*list.Element
	build Builder
	sf    singleflight.Group
}

// NewCache creates a cache bound to max entries (≤0 ⇒ DefaultCacheSize).
func NewCache(max int, build Builder) *Cache {
	if max <= 0 {
		max = DefaultCacheSize
	}
	return &Cache{max: max, ll: list.New(), items: map[string]*list.Element{}, build: build}
}

// Len is the number of cached entries (used by tests).
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
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
// first access. Concurrent first-access calls for the same key share a
// single build via singleflight. The returned *WatchedIndex is shared
// across all callers and across subsequent Get calls until evicted;
// callers MUST NOT call wix.Close() themselves — the cache owns the
// lifecycle.
func (c *Cache) Get(ctx context.Context, source string) (*search.WatchedIndex, error) {
	key, _, err := NormalizeKey(source)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	if e, ok := c.items[key]; ok {
		c.ll.MoveToFront(e)
		ix := e.Value.(*cacheEntry).ix
		c.mu.Unlock()
		return ix, nil
	}
	c.mu.Unlock()

	v, err, _ := c.sf.Do(key, func() (any, error) {
		ix, cleanup, err := c.build(ctx, key)
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		// Re-check in case another sf turn populated it (cheap; sf
		// coalesces same-key calls but being defensive is harmless).
		// If we lost the race, close the just-built watcher AND run
		// its cleanup — the cache already has a usable entry.
		if e, ok := c.items[key]; ok {
			_ = ix.Close()
			if cleanup != nil {
				cleanup()
			}
			c.ll.MoveToFront(e)
			return e.Value.(*cacheEntry).ix, nil
		}
		for len(c.items) >= c.max {
			tail := c.ll.Back()
			if tail == nil {
				break
			}
			ev := c.ll.Remove(tail).(*cacheEntry)
			delete(c.items, ev.key)
			_ = ev.ix.Close()
			if ev.cleanup != nil {
				ev.cleanup()
			}
		}
		ent := &cacheEntry{key: key, ix: ix, cleanup: cleanup}
		c.items[key] = c.ll.PushFront(ent)
		return ix, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*search.WatchedIndex), nil
}

// Close releases every cached entry. Stops the watcher goroutine for
// each (wix.Close()) and runs the user-supplied cleanup (rm -rf for
// temp clones). Safe to call multiple times.
func (c *Cache) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for e := c.ll.Front(); e != nil; e = e.Next() {
		ent := e.Value.(*cacheEntry)
		_ = ent.ix.Close()
		if ent.cleanup != nil {
			ent.cleanup()
		}
	}
	c.items = map[string]*list.Element{}
	c.ll.Init()
}
