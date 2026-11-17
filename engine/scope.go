package engine

import (
	"path/filepath"
	"strings"

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
	_, files, chunks = s.compile(ix)
	return files, chunks
}
