package search

import (
	"sync"
	"sync/atomic"

	"github.com/townsendmerino/aikit/chunk"
)

// LazyReranker defers a Reranker's underlying-model load + cache
// hydration until the FIRST Rerank call. Designed for the
// startup/query-latency M2 milestone: ken-mcp's default mode is
// hybrid (not hybrid+rerank), so users with KEN_MCP_RERANK=on who
// never actually hit a rerank-requiring query were paying the
// ~491 ms rerank-model load at startup for nothing. With LazyReranker,
// that cost moves to whenever the first rerank query lands — and
// stays at zero if no rerank query ever lands.
//
// Thread safety: the loader runs exactly once via sync.Once, so
// concurrent first-callers cooperate without redundant work.
// Subsequent calls do a plain pointer load + dispatch.
//
// Error model: if the loader returns an error, ALL subsequent
// Rerank calls return nil — the orchestrator's documented pass-
// through-stage-1 path (applyRerankerWithTelemetry treats nil as
// "no reranker"). The error is observable via Err().
type LazyReranker struct {
	loader func() (Reranker, error)

	once   sync.Once
	inner  Reranker
	err    error
	loaded atomic.Bool // observable flag — set after once.Do completes
}

// NewLazyReranker captures the loader closure. The closure is NOT
// called yet — that happens on the first Rerank/RerankWithTelemetry
// call. Loader must be goroutine-safe (sync.Once guarantees single
// invocation, but the loader's own resources — file reads, mmaps —
// should follow the usual rules).
func NewLazyReranker(loader func() (Reranker, error)) *LazyReranker {
	return &LazyReranker{loader: loader}
}

// Rerank implements Reranker. First call triggers the loader; nil
// result if loader errored.
func (lr *LazyReranker) Rerank(query string, cands []chunk.Chunk) []float64 {
	lr.ensureLoaded()
	if lr.err != nil || lr.inner == nil {
		return nil
	}
	return lr.inner.Rerank(query, cands)
}

// RerankWithTelemetry implements telemetryReranker. When the loaded
// inner reranker itself supports telemetry, the call routes through
// it; otherwise the LazyReranker falls back to plain Rerank (same
// fallback applyRerankerWithTelemetry would do).
func (lr *LazyReranker) RerankWithTelemetry(query string, cands []chunk.Chunk, t *Telemetry) []float64 {
	lr.ensureLoaded()
	if lr.err != nil || lr.inner == nil {
		return nil
	}
	if tr, ok := lr.inner.(telemetryReranker); ok {
		return tr.RerankWithTelemetry(query, cands, t)
	}
	return lr.inner.Rerank(query, cands)
}

func (lr *LazyReranker) ensureLoaded() {
	lr.once.Do(func() {
		lr.inner, lr.err = lr.loader()
		lr.loaded.Store(true)
	})
}

// Loaded returns true once the loader has run (whether successfully
// or not). Useful for status surfaces and tests.
func (lr *LazyReranker) Loaded() bool { return lr.loaded.Load() }

// Err returns the error from the loader call, or nil if not yet
// loaded or loaded successfully. Use after Loaded() to distinguish
// the "loaded successfully" and "loaded but errored" states.
func (lr *LazyReranker) Err() error { return lr.err }

// Inner returns the loaded reranker, or nil if not yet loaded /
// loader errored. Used by callers (e.g. the persistent-cache save
// path in cmd/ken-mcp) that need to operate on the underlying
// *NeuralReranker — at shutdown the cache is saved only if loading
// actually happened.
func (lr *LazyReranker) Inner() Reranker { return lr.inner }
