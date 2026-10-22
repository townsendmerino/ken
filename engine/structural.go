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
	"fmt"
	"sort"
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
	// SymbolSpan is one symbol and its lines, as SymbolAt returns it.
	SymbolSpan = structural.SymbolSpan
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

// ErrFileNotIndexed means SymbolAt's path isn't in the structural index:
// a language with no structural extractor, a file the walk skipped, or no
// such file. Distinct from file scope, which is an empty result.
var ErrFileNotIndexed = errors.New("engine: file not in the structural index")

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
// embedded in Workspace. A SUCCESSFUL build is kept for the Workspace's
// lifetime and brought up to date file by file as files change (apply,
// K13); a failed one is not kept, but is rate-limited by the cooldown
// above.
type structuralState struct {
	builder func(context.Context) (*structural.Index, error)
	// update brings an index up to date for the given repo-relative
	// paths, returning the new index and whether anything changed
	// (structural.Index.Update with the Workspace's root and admission
	// rules).
	update func(idx *structural.Index, paths []string) (*structural.Index, bool)

	// cur is the published index, nil until the first build succeeds.
	// Readers load it without a lock; every replacement is a whole new
	// immutable *structural.Index, so a reader sees the old index or the
	// new one, never a mix.
	cur atomic.Pointer[structural.Index]
	// gen counts published structural indexes: 1 after the first build,
	// +1 for every update that changed something.
	gen atomic.Uint64

	mu       sync.Mutex // guards the fields below; held across an update
	building bool
	done     chan struct{}
	cancel   context.CancelFunc
	failedAt time.Time
	// Changes that arrived while a build was in flight: the build may have
	// read those files before they changed, so run applies them to its
	// result before publishing. pendingReset means a change of unknown
	// extent (a watcher overflow): run discards its result instead.
	queued       map[string]struct{}
	pendingReset bool
}

// built reports whether a structural index has been published.
func (s *structuralState) built() bool { return s.cur.Load() != nil }

// get returns the structural index, or nil if it isn't ready yet (building,
// or in a failure cooldown). ctx bounds how long the caller blocks for an
// in-flight build; the build itself runs under an independent budget and
// keeps going after ctx is done.
func (s *structuralState) get(ctx context.Context) *structural.Index {
	if idx := s.cur.Load(); idx != nil {
		return idx
	}

	s.mu.Lock()
	if idx := s.cur.Load(); idx != nil {
		s.mu.Unlock()
		return idx
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
		return s.cur.Load() // nil if the build failed
	case <-timer.C:
		return nil // still building — caller should retry shortly
	case <-ctx.Done():
		return nil // caller gave up; the background build continues
	}
}

func (s *structuralState) run(ctx context.Context, done chan struct{}) {
	idx, err := s.builder(ctx)

	s.mu.Lock()
	switch {
	case err != nil:
		s.failedAt = time.Now()
	case s.pendingReset:
		// Files changed in ways the watcher couldn't enumerate while this
		// build ran; it may hold stale files. Drop it — the next call
		// starts a fresh build over the current tree.
	default:
		if len(s.queued) > 0 {
			paths := make([]string, 0, len(s.queued))
			for p := range s.queued {
				paths = append(paths, p)
			}
			sort.Strings(paths)
			idx, _ = s.update(idx, paths)
		}
		s.cur.Store(idx) // publish last; happens-before close(done)
		s.gen.Add(1)
	}
	s.queued = nil
	s.pendingReset = false
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

// apply brings the structural index up to date for paths that changed on
// disk (repo-relative; changed, created or deleted — update reads the disk
// to tell which). It never starts a build: with no index yet, the lazy
// build reads the current files anyway. During a build the paths are
// queued for run to apply before it publishes. resync means the change's
// extent is unknown (a watcher event-queue overflow): the index is dropped
// and the next structural call rebuilds it.
func (s *structuralState) apply(paths []string, resync bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.building {
		if resync {
			s.pendingReset = true
			return
		}
		if s.queued == nil {
			s.queued = make(map[string]struct{}, len(paths))
		}
		for _, p := range paths {
			s.queued[p] = struct{}{}
		}
		return
	}
	idx := s.cur.Load()
	if idx == nil {
		return
	}
	if resync {
		s.cur.Store(nil)
		return
	}
	if nidx, changed := s.update(idx, paths); changed {
		s.cur.Store(nidx)
		s.gen.Add(1)
	}
}

// pending reports whether a structural build is currently running, or a
// fresh call right now would start one (no cached result, no active
// cooldown).
func (s *structuralState) pending() bool {
	if s.built() {
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

// SymbolAt returns the indexed symbols whose spans contain line in path,
// innermost first. A line inside a Python method gives the method, then
// its class. A Go method is not nested in its type's span, so it gives the
// method alone. A line at file scope (imports, package-level declarations,
// comments between functions) or past the end of the file gives an empty
// slice. Closures and anonymous functions aren't indexed, so a line inside
// one gives the named function around it. A path the structural index
// doesn't hold returns ErrFileNotIndexed. Answers are as current as the
// structural index (see Refresh). Cost is proportional to the definitions
// in that one file.
func (w *Workspace) SymbolAt(ctx context.Context, path string, line int) ([]SymbolSpan, error) {
	idx, err := w.structuralIndex(ctx)
	if err != nil {
		return nil, err
	}
	syms, ok := idx.SymbolsAt(structural.NormalizePath(path), line)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrFileNotIndexed, path)
	}
	return syms, nil
}

// TestSite is one test function that exercises a symbol, and where to run
// it (TestsFor).
type TestSite = structural.TestSite

// Callee is one call made inside a symbol (Callees).
type Callee = structural.Callee

// TestsFor returns the test functions that call symbol: directly (Depth
// 1), or, when maxDepth >= 2, through one non-test function (Depth 2,
// with Via naming it). Sorted by Depth, then File, then StartLine.
// Matching is by name, as in Callers, with its package qualifier
// narrowing where it applies; a call inside a closure (a t.Run subtest)
// counts for the enclosing test. Dir is the directory to run the test
// from — for Go, the package directory (`go test -run '^TestX$' ./<Dir>`).
//
// Conventions: Go *_test.go files with top-level Test/Benchmark/Fuzz/
// Example functions; Python test_*.py and *_test.py files with test_*
// functions and test_* methods of Test* classes. Other languages give an
// empty result, not an error. Depth 2 matches the intermediate function
// by its leaf name, so a common name fans out; it follows at most 64
// intermediates, skips one called from more than 200 files, and the whole
// result is capped at 500 — a missing Depth-2 test is the price of a
// bounded answer.
func (w *Workspace) TestsFor(ctx context.Context, symbol string, maxDepth int) ([]TestSite, error) {
	idx, err := w.structuralIndex(ctx)
	if err != nil {
		return nil, err
	}
	return idx.TestsFor(symbol, maxDepth), nil
}

// Callees returns the calls made inside symbol's span, in line order (by
// file, then line, when the name has several definitions). A call inside a
// closure counts. symbol resolves as in Definition, qualified forms
// included; a class has no callees. Calls the extractors drop as noise —
// Go builtins and fmt's Printf/Sprintf/Errorf family, Python builtins —
// don't appear.
func (w *Workspace) Callees(ctx context.Context, symbol string) ([]Callee, error) {
	idx, err := w.structuralIndex(ctx)
	if err != nil {
		return nil, err
	}
	return idx.Callees(symbol), nil
}
