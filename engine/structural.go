package engine

// structural.go — the structural (symbol-level) surface: Definition,
// References, Callers, Outline, Symbols. The lazy/background build state
// machine (structuralState) is ported from mcp/bundle.go's RepoBundle
// unchanged (same budgets, same cooldown) — Stage 8's cold-start finding
// was that the structural build's tree-sitter parse is ~50% of index time
// on PHP-shaped corpora, so paying it eagerly at Open would double the
// cold-start cost for a session that never calls a structural method.
//
// Result types (DefinitionSite, Reference, CallSite, OutlineEntry, and
// their Kind enums) are re-exported type aliases over internal/structural
// so a caller outside this module never needs to import it directly — the
// same pattern mcp/api_aliases.go already uses for search.Result/Mode.

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/townsendmerino/ken/internal/structural"
)

// Re-exported structural result types. 1.0-stable aliases for
// internal/structural's types — the underlying type is unchanged, not
// wrapped, so field access and Kind comparisons work identically to using
// internal/structural directly.
type (
	// DefinitionSite is one place a symbol is defined.
	DefinitionSite = structural.DefinitionSite
	// DefinitionKind classifies a DefinitionSite (function, class, method).
	DefinitionKind = structural.DefinitionKind
	// Reference is one syntactic occurrence of a symbol (call, import, raise, …).
	Reference = structural.Reference
	// ReferenceKind classifies a Reference.
	ReferenceKind = structural.ReferenceKind
	// CallSite is one file containing a call to a symbol; its Calls hold
	// the individual call sites.
	CallSite = structural.CallSite
	// Call is one call site in a CallSite's file: its line and the
	// qualified name of the named function or method it sits in.
	Call = structural.Call
	// OutlineEntry is one function/class/method in a file's structural outline.
	OutlineEntry = structural.OutlineEntry
)

// DefinitionKind values.
const (
	DefinitionKindUnknown  = structural.DefinitionKindUnknown
	DefinitionKindFunction = structural.DefinitionKindFunction
	DefinitionKindClass    = structural.DefinitionKindClass
	DefinitionKindMethod   = structural.DefinitionKindMethod
)

// ReferenceKind values.
const (
	ReferenceKindUnknown    = structural.ReferenceKindUnknown
	ReferenceKindCall       = structural.ReferenceKindCall
	ReferenceKindImport     = structural.ReferenceKindImport
	ReferenceKindRaise      = structural.ReferenceKindRaise
	ReferenceKindAnnotation = structural.ReferenceKindAnnotation
	ReferenceKindName       = structural.ReferenceKindName
)

// ErrStructuralBuilding means the structural index for this Workspace is
// either still building or a fresh call would (re)start a build — the
// caller should retry shortly rather than conclude there's no index.
var ErrStructuralBuilding = errors.New("engine: structural index still building")

// ErrNoStructuralIndex means no structural index is available for this
// repo: no file in the corpus matches a registered structural extractor
// (an unsupported-language corpus genuinely has none), or the last build
// attempt failed and is in its retry cooldown.
var ErrNoStructuralIndex = errors.New("engine: no structural index available")

// OutlineFile pairs a file path with its structural outline, Outline's
// return shape for a directory argument (one entry per file under it).
type OutlineFile struct {
	File    string
	Entries []OutlineEntry
}

// structuralBuildBudget bounds a single background build, detached from
// any caller's context — one client's short-lived call can't kill (and
// endlessly re-trigger) a large whole-corpus parse.
const structuralBuildBudget = 10 * time.Minute

// structuralFirstWait is how long a caller blocks for an in-flight build
// before getting ErrStructuralBuilding back. Small repos finish well
// inside it; large repos hand the caller back promptly and finish in the
// background for the next call.
const structuralFirstWait = 10 * time.Second

// structuralFailCooldown throttles retries after a failed build so a
// caller looping on a structural method can't drive a fresh whole-corpus
// parse on every call. A var (not const) only so tests can shorten it.
var structuralFailCooldown = 60 * time.Second

// structuralState is the lazy/background structural-build state machine,
// embedded in Workspace. A SUCCESSFUL build is cached for the Workspace's
// lifetime; a failed one is not, but is rate-limited by the cooldown
// above.
type structuralState struct {
	builder func(context.Context) (*structural.Index, error)

	mu       sync.Mutex // guards the fields below
	built    atomic.Bool
	idx      *structural.Index
	building bool
	done     chan struct{}
	cancel   context.CancelFunc
	failedAt time.Time
}

// get returns the structural index, or nil if it isn't ready yet (building,
// or in a failure cooldown). ctx bounds how long the caller blocks for an
// in-flight build; the build itself runs under an independent budget and
// keeps going after ctx is done.
func (s *structuralState) get(ctx context.Context) *structural.Index {
	if s.built.Load() {
		return s.idx
	}

	s.mu.Lock()
	if s.built.Load() {
		s.mu.Unlock()
		return s.idx
	}
	if !s.building {
		if !s.failedAt.IsZero() && time.Since(s.failedAt) < structuralFailCooldown {
			s.mu.Unlock()
			return nil // recent failure — don't re-trigger the parse yet
		}
		s.building = true
		s.done = make(chan struct{})
		bctx, cancel := context.WithTimeout(context.Background(), structuralBuildBudget)
		s.cancel = cancel
		go s.run(bctx, s.done)
	}
	done := s.done
	s.mu.Unlock()

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
		return s.idx // nil if the build failed
	case <-timer.C:
		return nil // still building — caller should retry shortly
	case <-ctx.Done():
		return nil // caller gave up; the background build continues
	}
}

func (s *structuralState) run(ctx context.Context, done chan struct{}) {
	idx, err := s.builder(ctx)

	s.mu.Lock()
	if err != nil {
		s.failedAt = time.Now()
	} else {
		s.idx = idx
		s.built.Store(true) // publish last; happens-before close(done)
	}
	s.building = false
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	s.mu.Unlock()
	close(done)
}

// stop cancels an in-flight build and waits for it to exit. No-op when no
// build is running.
func (s *structuralState) stop() {
	s.mu.Lock()
	done := s.done
	if s.cancel != nil {
		s.cancel()
	}
	building := s.building
	s.mu.Unlock()
	if building && done != nil {
		<-done
	}
}

// pending reports whether a structural build is currently running, or a
// fresh call right now would start one (no cached result, no active
// cooldown).
func (s *structuralState) pending() bool {
	if s.built.Load() {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.building {
		return true
	}
	return s.failedAt.IsZero() || time.Since(s.failedAt) >= structuralFailCooldown
}

// structuralIndex returns the structural index, or an error distinguishing
// "still building, retry" (ErrStructuralBuilding) from "genuinely no
// structural index for this repo" (ErrNoStructuralIndex).
func (w *Workspace) structuralIndex(ctx context.Context) (*structural.Index, error) {
	if idx := w.structural.get(ctx); idx != nil {
		return idx, nil
	}
	if w.structural.pending() {
		return nil, ErrStructuralBuilding
	}
	return nil, ErrNoStructuralIndex
}

// Definition returns every known definition site of symbol (exact name
// match; not type-aware — two distinct symbols with the same spelling
// collapse into one result list).
func (w *Workspace) Definition(ctx context.Context, symbol string) ([]DefinitionSite, error) {
	idx, err := w.structuralIndex(ctx)
	if err != nil {
		return nil, err
	}
	return idx.Definition(symbol), nil
}

// References returns every recognized reference to symbol (calls, imports,
// raises) across the corpus.
func (w *Workspace) References(ctx context.Context, symbol string) ([]Reference, error) {
	idx, err := w.structuralIndex(ctx)
	if err != nil {
		return nil, err
	}
	return idx.References(symbol), nil
}

// Callers returns every file containing a call to symbol, sorted by file:
// one CallSite per file, whose Calls list each call in it (line and
// enclosing function), in line order.
func (w *Workspace) Callers(ctx context.Context, symbol string) ([]CallSite, error) {
	idx, err := w.structuralIndex(ctx)
	if err != nil {
		return nil, err
	}
	return idx.Callers(symbol), nil
}

// Outline returns the structural outline (every top-level function, class,
// and method) for path. If path matches an indexed file exactly, the
// result has exactly one entry for it; otherwise path is treated as a
// directory prefix and the result has one entry per indexed file under it
// (empty if the prefix matches nothing).
func (w *Workspace) Outline(ctx context.Context, path string) ([]OutlineFile, error) {
	idx, err := w.structuralIndex(ctx)
	if err != nil {
		return nil, err
	}
	norm := structural.NormalizePath(path)
	if fs := idx.File(norm); fs != nil {
		return []OutlineFile{{File: norm, Entries: idx.Outline(norm)}}, nil
	}
	files := idx.FilesUnderPath(norm)
	out := make([]OutlineFile, 0, len(files))
	for _, f := range files {
		out = append(out, OutlineFile{File: f, Entries: idx.Outline(f)})
	}
	return out, nil
}

// Symbols returns every top-level symbol name under path (a file or
// directory prefix), or every symbol in the corpus if path is "" or ".".
func (w *Workspace) Symbols(ctx context.Context, path string) ([]string, error) {
	idx, err := w.structuralIndex(ctx)
	if err != nil {
		return nil, err
	}
	if path == "" || path == "." {
		return idx.Symbols(), nil
	}
	return idx.SymbolsInPath(structural.NormalizePath(path)), nil
}
