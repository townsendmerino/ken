package search

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/townsendmerino/aikit/chunk"
)

// lazyTestReranker is a deterministic Reranker for testing: returns
// 1.0 for every candidate so the tests can verify the inner reranker
// was actually called (vs. nil fallback) without depending on a real
// model load.
type lazyTestReranker struct {
	calls atomic.Int64
}

func (s *lazyTestReranker) Rerank(query string, cands []chunk.Chunk) []float64 {
	s.calls.Add(1)
	out := make([]float64, len(cands))
	for i := range out {
		out[i] = 1.0
	}
	return out
}

// TestLazyReranker_DefersLoadUntilFirstCall pins the core M2
// invariant: NewLazyReranker does NOT run the loader; the first
// Rerank call does.
func TestLazyReranker_DefersLoadUntilFirstCall(t *testing.T) {
	var loaderCalls atomic.Int64
	stub := &lazyTestReranker{}
	loader := func() (Reranker, error) {
		loaderCalls.Add(1)
		return stub, nil
	}
	lr := NewLazyReranker(loader)

	if loaderCalls.Load() != 0 {
		t.Errorf("loader called %d times at construction; want 0", loaderCalls.Load())
	}
	if lr.Loaded() {
		t.Errorf("Loaded() = true at construction; want false")
	}

	// First Rerank call should fire the loader exactly once.
	cands := []chunk.Chunk{{Text: "alpha"}, {Text: "bravo"}}
	scores := lr.Rerank("q", cands)
	if loaderCalls.Load() != 1 {
		t.Errorf("after first call loader was called %d times; want 1", loaderCalls.Load())
	}
	if !lr.Loaded() {
		t.Errorf("Loaded() = false after first call; want true")
	}
	if len(scores) != 2 {
		t.Errorf("scores len = %d; want 2", len(scores))
	}
	if stub.calls.Load() != 1 {
		t.Errorf("stub Rerank called %d times; want 1", stub.calls.Load())
	}

	// Subsequent calls must NOT re-run the loader; they should
	// dispatch straight to the inner.
	lr.Rerank("q2", cands)
	lr.Rerank("q3", cands)
	if loaderCalls.Load() != 1 {
		t.Errorf("loader called %d times after 3 reranks; want 1 (single-shot)", loaderCalls.Load())
	}
	if stub.calls.Load() != 3 {
		t.Errorf("stub Rerank called %d times after 3 calls; want 3", stub.calls.Load())
	}
}

// TestLazyReranker_ConcurrentFirstCallers_LoaderRunsOnce pins the
// sync.Once invariant: 50 goroutines firing the first Rerank call
// concurrently must all see the same loaded inner — no torn state,
// no duplicate loads.
func TestLazyReranker_ConcurrentFirstCallers_LoaderRunsOnce(t *testing.T) {
	var loaderCalls atomic.Int64
	stub := &lazyTestReranker{}
	loader := func() (Reranker, error) {
		loaderCalls.Add(1)
		return stub, nil
	}
	lr := NewLazyReranker(loader)

	const N = 50
	var wg sync.WaitGroup
	cands := []chunk.Chunk{{Text: "x"}}
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lr.Rerank("q", cands)
		}()
	}
	wg.Wait()

	if loaderCalls.Load() != 1 {
		t.Errorf("loader called %d times under N=%d concurrent first-callers; want 1", loaderCalls.Load(), N)
	}
	if stub.calls.Load() != int64(N) {
		t.Errorf("inner Rerank called %d times; want %d (every caller should reach it)", stub.calls.Load(), N)
	}
}

// TestLazyReranker_LoaderErrorPassesThroughAsNil pins the error
// path: if the loader errors, the LazyReranker returns nil from
// Rerank (the orchestrator's signal to skip the rerank pass).
func TestLazyReranker_LoaderErrorPassesThroughAsNil(t *testing.T) {
	loadErr := errors.New("simulated load failure")
	loader := func() (Reranker, error) {
		return nil, loadErr
	}
	lr := NewLazyReranker(loader)

	scores := lr.Rerank("q", []chunk.Chunk{{Text: "x"}})
	if scores != nil {
		t.Errorf("loader-error path: Rerank returned %v; want nil", scores)
	}
	if !lr.Loaded() {
		t.Errorf("Loaded() should be true even when loader errored (we did try)")
	}
	if lr.Err() != loadErr {
		t.Errorf("Err() = %v; want the captured load error %v", lr.Err(), loadErr)
	}

	// Subsequent calls also pass through as nil — without retrying
	// the failed loader.
	for i := 0; i < 5; i++ {
		if got := lr.Rerank("q", []chunk.Chunk{{Text: "x"}}); got != nil {
			t.Errorf("call #%d after error returned %v; want nil", i+2, got)
		}
	}
}

// TestLazyReranker_Inner exposes the loaded inner reranker so the
// shutdown cache-save path can operate on the underlying
// NeuralReranker without re-running the loader.
func TestLazyReranker_Inner(t *testing.T) {
	stub := &lazyTestReranker{}
	loader := func() (Reranker, error) { return stub, nil }
	lr := NewLazyReranker(loader)

	if lr.Inner() != nil {
		t.Errorf("Inner() before load returned %v; want nil", lr.Inner())
	}

	lr.Rerank("q", []chunk.Chunk{{Text: "x"}})

	if lr.Inner() != stub {
		t.Errorf("Inner() after load returned %v; want the stub", lr.Inner())
	}
}

// TestLazyReranker_RerankWithTelemetry_PassesThrough pins that the
// telemetryReranker fast-path routes through the inner when the
// inner supports it, AND falls back to plain Rerank when it doesn't.
func TestLazyReranker_RerankWithTelemetry_FallsBackForNonTelemetryInner(t *testing.T) {
	stub := &lazyTestReranker{} // doesn't implement telemetryReranker
	lr := NewLazyReranker(func() (Reranker, error) { return stub, nil })

	tel := &Telemetry{}
	scores := lr.RerankWithTelemetry("q", []chunk.Chunk{{Text: "x"}}, tel)
	if len(scores) != 1 {
		t.Errorf("scores len = %d; want 1", len(scores))
	}
	if stub.calls.Load() != 1 {
		t.Errorf("inner Rerank should have been called via the fallback path; got %d", stub.calls.Load())
	}
}
