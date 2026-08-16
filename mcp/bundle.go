package mcp

// bundle.go — the RepoBundle the cache holds, and its lazy/background
// structural-index build state machine.
//
// Split out of cache.go (which is now just the generic LRU + singleflight
// cache): the structural build is a self-contained concern — background
// goroutine, independent budget, retry cooldown, cancellation — that was
// bolted onto the cache value. Keeping it here mirrors how status_tool.go /
// structural_tools.go were pulled out of server.go, so cache.go reads as "the
// cache" and this file reads as "the per-repo bundle + its structural build".

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/townsendmerino/ken/internal/search"
	"github.com/townsendmerino/ken/internal/structural"
)

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
