// Package topk is a min-heap-of-size-K selector for "keep the K
// highest-scoring items from a stream" without sorting the full input.
// O(N log K) vs sort.Slice's O(N log N); the win grows with N relative
// to K.
//
// Use in place of "score everything into a slice, sort by score
// descending, take the first K" — a pattern that's invisible at toy
// scale but dominates search CPU at production scale (per ADR-025).
//
// The implementation hand-rolls heap up/down rather than wrapping
// container/heap because (a) container/heap takes interface{} via
// heap.Interface which would force boxing and defeat the perf goal,
// and (b) the heap-of-fixed-K pattern is small enough that direct
// implementation is clearer than the heap.Interface dance.
//
// Generics over interface{}: ken is on Go 1.26 (go.mod toolchain pin),
// so generics are fully available. Generic typing avoids the boxing
// allocations that interface{} would impose and avoids per-callsite
// concrete-type copies. The two production callers (internal/ann.Flat.Query
// and internal/bm25.Index.TopK) use different item types, so generics
// are the right shape.
//
// Placement decision (ADR-026 alternatives): standalone leaf at
// internal/topk rather than internal/search/topk. If a future caller
// (a reranker, a find_related reimplementation, etc.) needs top-K
// selection, the import path doesn't suggest false coupling to BM25
// or ANN.
package topk

// scored is the internal heap element: an item plus its score. Kept
// non-generic-friendly (struct-of-T) so the slice backing the heap
// is contiguous and benefits from CPU-cache locality on the up/down
// sift loops.
type scored[T any] struct {
	item  T
	score float64
}

// Selector is a min-heap of fixed capacity. Push items as you score
// them; the heap retains the K highest-scoring observations seen so
// far. Read via Result() — returned in descending-score order.
//
// Tie-breaking: a strict greater-than comparison in Push ensures that
// when a new item ties the current minimum's score, the new item is
// discarded and the older one stays. Callers that iterate input in
// some natural order (ascending index, etc.) thereby inherit a stable
// "first-seen wins on tie" behavior without paying a secondary-key
// sort cost.
type Selector[T any] struct {
	k    int
	heap []scored[T]
}

// New returns a Selector that keeps the top k items by score.
// k=0 is valid (Result returns an empty slice; Push always discards).
// Negative k panics — caller error, not a runtime condition.
func New[T any](k int) *Selector[T] {
	if k < 0 {
		panic("topk.New: k must be non-negative")
	}
	return &Selector[T]{
		k:    k,
		heap: make([]scored[T], 0, k),
	}
}

// Push offers (item, score) to the selector. If the heap hasn't reached
// capacity, item is added. Otherwise item replaces the current minimum
// iff score > min_score (strict; ties favor the older item per the
// tie-breaking note above). Returns true if item was retained, false
// if discarded.
func (s *Selector[T]) Push(item T, score float64) bool {
	if s.k == 0 {
		return false
	}
	if len(s.heap) < s.k {
		s.heap = append(s.heap, scored[T]{item: item, score: score})
		s.siftUp(len(s.heap) - 1)
		return true
	}
	// At capacity: only retain if strictly greater than current minimum.
	// Strict > preserves "older wins on tie" for callers that iterate
	// input in their natural order — see the doc comment on Selector.
	if score <= s.heap[0].score {
		return false
	}
	s.heap[0] = scored[T]{item: item, score: score}
	s.siftDown(0)
	return true
}

// ItemWithScore is the public read shape returned by Result.
type ItemWithScore[T any] struct {
	Item  T
	Score float64
}

// Result returns the retained items in descending-score order. May be
// shorter than k if Push was called fewer than k times. Returned slice
// is freshly allocated; safe for callers to retain or mutate.
//
// Sort cost is O(K log K) — by construction K is small (typically 10
// for ken's search), so this is cheap relative to the N pushes that
// fed the heap.
//
// Tie-breaking on Result: the heap's internal ordering on ties is not
// defined, but because Push uses strict > (see Push comment), the
// retained K items at the tie boundary are the first-seen of any tied
// group. Result then sorts strictly by score; equal-score items emerge
// in heap-internal order, which is deterministic for a given input
// sequence but not lexically ordered by item.
func (s *Selector[T]) Result() []ItemWithScore[T] {
	out := make([]ItemWithScore[T], len(s.heap))
	// Repeated extract-min would give ascending order — we reverse-fill
	// the output slice to land descending without a second pass.
	n := len(s.heap)
	tmp := make([]scored[T], n)
	copy(tmp, s.heap)
	for i := n - 1; i >= 0; i-- {
		min := tmp[0]
		tmp[0] = tmp[len(tmp)-1]
		tmp = tmp[:len(tmp)-1]
		siftDownSlice(tmp, 0)
		out[i] = ItemWithScore[T]{Item: min.item, Score: min.score}
	}
	return out
}

// Len is the current number of retained items (0 ≤ Len() ≤ k).
func (s *Selector[T]) Len() int { return len(s.heap) }

// ── heap operations ───────────────────────────────────────────────
// Min-heap: parent ≤ children. heap[0] is the smallest score.

func (s *Selector[T]) siftUp(i int) {
	for i > 0 {
		parent := (i - 1) / 2
		if s.heap[i].score >= s.heap[parent].score {
			return
		}
		s.heap[i], s.heap[parent] = s.heap[parent], s.heap[i]
		i = parent
	}
}

func (s *Selector[T]) siftDown(i int) { siftDownSlice(s.heap, i) }

// siftDownSlice is shared by Selector.siftDown and Result's
// extract-min loop (which operates on a scratch slice).
func siftDownSlice[T any](heap []scored[T], i int) {
	n := len(heap)
	for {
		l, r := 2*i+1, 2*i+2
		smallest := i
		if l < n && heap[l].score < heap[smallest].score {
			smallest = l
		}
		if r < n && heap[r].score < heap[smallest].score {
			smallest = r
		}
		if smallest == i {
			return
		}
		heap[i], heap[smallest] = heap[smallest], heap[i]
		i = smallest
	}
}
