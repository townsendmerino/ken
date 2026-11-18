package engine

import (
	"path/filepath"
	"strings"
	"sync"

	"github.com/townsendmerino/ken/internal/search"
)

// Scope restricts a search to part of the index. The zero Scope is the whole
// index. The fields are AND-ed: a file is in scope when it passes every one
// that is set. It is applied inside both retrieval arms (a chunk outside it is
// skipped before it is scored), so a scoped top-k is exact over the scope
// whatever the scope's size, and a chunk scores the same as it does unscoped.
//
// New criteria (a code/docs kind, a callers filter) are new fields here; the
// compile step below is the only place that decides a file.
type Scope struct {
	// Languages are file extensions, with or without the leading dot
	// ("go", ".py"); a file matches when its extension is one of them,
	// compared case-insensitively.
	Languages []string

	// PathContains keeps files whose path contains this substring
	// (case-sensitive).
	PathContains string

	// ExcludePathContains drops files whose path contains this substring
	// (case-sensitive).
	ExcludePathContains string
}

// IsZero reports whether s restricts nothing.
func (s Scope) IsZero() bool {
	return !s.hasLanguages() && s.PathContains == "" && s.ExcludePathContains == ""
}

func (s Scope) hasLanguages() bool {
	for _, l := range s.Languages {
		if strings.TrimSpace(l) != "" {
			return true
		}
	}
	return false
}

// Match reports whether the file (a repo-relative path) is in scope. It is the
// same per-file decision a search compiles, for callers that filter a list of
// files themselves (the callers tool's path filters).
func (s Scope) Match(file string) bool { return s.keep()(file) }

// keep compiles the per-file decision.
func (s Scope) keep() func(file string) bool {
	var ext map[string]bool
	if s.hasLanguages() {
		ext = make(map[string]bool, len(s.Languages))
		for _, l := range s.Languages {
			l = strings.TrimSpace(l)
			if l == "" {
				continue
			}
			if !strings.HasPrefix(l, ".") {
				l = "." + l
			}
			ext[strings.ToLower(l)] = true
		}
	}
	return func(file string) bool {
		if ext != nil && !ext[strings.ToLower(filepath.Ext(file))] {
			return false
		}
		if s.PathContains != "" && !strings.Contains(file, s.PathContains) {
			return false
		}
		if s.ExcludePathContains != "" && strings.Contains(file, s.ExcludePathContains) {
			return false
		}
		return true
	}
}

// compile turns s into the per-query chunk set a search takes: nil for the
// zero Scope (every chunk), otherwise one pass over the chunk table.
func (s Scope) compile(ix *search.Index) (set *search.ChunkSet, files, chunks int) {
	if s.IsZero() {
		return nil, 0, 0
	}
	return ix.ScopeOfFiles(s.keep())
}

// ScopeSize reports how many files and live chunks of the published index are
// in scope. For the zero Scope that is the whole index. A caller can answer an
// empty scope (0 chunks) without searching.
func (w *Workspace) ScopeSize(s Scope) (files, chunks int) {
	ix := w.index.Load()
	if s.IsZero() {
		return countUniqueFiles(ix), ix.LiveLen()
	}
	_, files, chunks = w.scopes.get(ix, s)
	return files, chunks
}

// scopeCache remembers the compiled form of the last few scopes for one index
// snapshot, so an agent narrowing repeated queries to the same directory pays
// the pass over the chunk table once. It is dropped whenever the published
// index changes (a new snapshot has new chunk ids). The zero value is ready.
type scopeCache struct {
	mu      sync.Mutex
	ix      *search.Index
	entries []scopeEntry
}

type scopeEntry struct {
	key    string
	set    *search.ChunkSet
	files  int
	chunks int
}

const scopeCacheSize = 8

func (s Scope) cacheKey() string {
	return strings.Join(s.Languages, ",") + "\x00" + s.PathContains + "\x00" + s.ExcludePathContains
}

// get returns s compiled against ix, from the cache when it holds it.
func (c *scopeCache) get(ix *search.Index, s Scope) (*search.ChunkSet, int, int) {
	if s.IsZero() {
		return nil, 0, 0
	}
	key := s.cacheKey()
	c.mu.Lock()
	if c.ix != ix {
		c.ix, c.entries = ix, nil
	}
	for _, e := range c.entries {
		if e.key == key {
			c.mu.Unlock()
			return e.set, e.files, e.chunks
		}
	}
	c.mu.Unlock()
	set, files, chunks := s.compile(ix)
	c.mu.Lock()
	if c.ix == ix {
		if len(c.entries) >= scopeCacheSize {
			c.entries = c.entries[1:]
		}
		c.entries = append(c.entries, scopeEntry{key, set, files, chunks})
	}
	c.mu.Unlock()
	return set, files, chunks
}
