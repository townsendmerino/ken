package search

import (
	"container/list"
	"hash/fnv"
	"math"
	"sync"
	"time"

	"github.com/townsendmerino/ken/chunk"
	"github.com/townsendmerino/ken/internal/coderank"
)

// DefaultRerankerCacheSize bounds the per-reranker doc-embedding LRU.
// 4096 chunks × 768 f32 ≈ 12 MB resident — cheap compared to the 547
// MB model itself. Plan §8 calls the cache "the perf keystone";
// matching ken-mcp's KEN_MCP_RERANK_CACHE_SIZE default in plan §10.
const DefaultRerankerCacheSize = 4096

// NeuralReranker scores candidates with the CodeRankEmbed forward
// pass. Holds an Encoder interface so the caller can supply either the
// f32 Model (default precision) or the M8 int8 ModelQ8 — the
// reranker's logic is identical either way.
//
// Goroutine-safety: the underlying Encoder is immutable and per-forward
// buffers are local, so concurrent Rerank calls on the SAME instance
// are safe. The LRU cache uses an internal mutex for the map / list
// updates around each forward pass.
//
// Per-call concurrency: Rerank fans missing candidates across
// runtime.NumCPU() workers via Encoder.EncodeBatch (M3 + M7). For
// rerankN=50 the M3 measurement is ~15s cold / ~2s warm on an 8-core
// M1 Pro for f32; ModelQ8 lands ~similar (single-thread) or marginally
// faster (multi-thread; see outputs/m8b-results.md).
type NeuralReranker struct {
	model coderank.Encoder
	cache *embeddingLRU
}

// NeuralRerankerOption configures the reranker at construction.
type NeuralRerankerOption func(*neuralRerankerConfig)

type neuralRerankerConfig struct {
	cacheSize int
}

// WithCacheSize overrides the LRU bound (default
// DefaultRerankerCacheSize = 4096). 0 disables caching (every call
// re-encodes every candidate); useful for tests but a perf disaster
// in production.
func WithCacheSize(n int) NeuralRerankerOption {
	return func(c *neuralRerankerConfig) {
		if n >= 0 {
			c.cacheSize = n
		}
	}
}

// NewNeuralReranker wraps a loaded encoder (coderank.Model f32 or
// coderank.ModelQ8 int8) as a search.Reranker. Construction is cheap
// (the encoder is already loaded); the only allocation here is the
// empty LRU.
func NewNeuralReranker(model coderank.Encoder, opts ...NeuralRerankerOption) *NeuralReranker {
	cfg := neuralRerankerConfig{cacheSize: DefaultRerankerCacheSize}
	for _, o := range opts {
		o(&cfg)
	}
	return &NeuralReranker{
		model: model,
		cache: newEmbeddingLRU(cfg.cacheSize),
	}
}

// Rerank returns one cosine score per candidate, encoding the query
// once (with prefix) and each missing candidate via EncodeBatch. The
// returned slice has length len(cands); on any encoding error it
// returns nil so applyReranker falls through to the stage-1 order.
//
// Cache key: 64-bit FNV-1a of the candidate text. Plan §8: keyed by
// content hash, decoupled from tombstones — stale entries simply
// never get hit again, and the LRU bounds memory regardless.
func (r *NeuralReranker) Rerank(query string, cands []chunk.Chunk) []float64 {
	return r.RerankWithTelemetry(query, cands, nil)
}

// RerankWithTelemetry is the telemetry-recording variant. When t is
// non-nil, fills t.RerankerQueryEncode + t.RerankerCandidateEncode +
// t.RerankerCacheHits + t.RerankerCacheMisses (the t.RerankerN
// figure is set by applyRerankerWithTelemetry, not here).
//
// Pipelining and caching behavior is identical to Rerank — t is a
// passive observer.
func (r *NeuralReranker) RerankWithTelemetry(query string, cands []chunk.Chunk, t *Telemetry) []float64 {
	if len(cands) == 0 {
		return nil
	}

	// M8c PIPELINE: fire the query encode concurrently with the
	// cache-lookup + candidate batch. Query encode is a single
	// sequential forward (~150-500ms depending on query length);
	// without pipelining the candidate batch can't start until
	// the query returns. With pipelining they overlap, saving up
	// to one query-encode worth of latency per cold query.
	type qResult struct {
		vec []float32
		dur time.Duration
		err error
	}
	qCh := make(chan qResult, 1)
	go func() {
		var t0 time.Time
		if t != nil {
			t0 = time.Now()
		}
		v, err := r.model.Encode(query, true)
		var d time.Duration
		if t != nil {
			d = time.Since(t0)
		}
		if err != nil {
			qCh <- qResult{err: err, dur: d}
			return
		}
		qCh <- qResult{vec: l2NormalizeCopy(v), dur: d}
	}()

	// Identify which candidates need encoding (cache miss). The hits
	// fill in directly from the LRU; misses go through EncodeBatch.
	hashes := make([]uint64, len(cands))
	cached := make([][]float32, len(cands))
	var missTexts []string
	var missIdx []int
	for i, c := range cands {
		h := fnvHash(c.Text)
		hashes[i] = h
		if v, ok := r.cache.get(h); ok {
			cached[i] = v
			continue
		}
		missTexts = append(missTexts, c.Text)
		missIdx = append(missIdx, i)
	}
	if t != nil {
		t.RerankerCacheHits = len(cands) - len(missTexts)
		t.RerankerCacheMisses = len(missTexts)
	}

	if len(missTexts) > 0 {
		var candT0 time.Time
		if t != nil {
			candT0 = time.Now()
		}
		isQ := make([]bool, len(missTexts)) // all docs, no query prefix
		out, err := r.model.EncodeBatch(missTexts, isQ, 0)
		if t != nil {
			t.RerankerCandidateEncode = time.Since(candT0)
		}
		if err != nil {
			// Drain the in-flight query goroutine so it doesn't leak
			// and its scratch buffer returns to the pool.
			<-qCh
			return nil
		}
		for j, idx := range missIdx {
			v := l2NormalizeCopy(out[j])
			cached[idx] = v
			r.cache.put(hashes[idx], v)
		}
	}

	// Wait for the query encode (overlapped with the candidate work above).
	qr := <-qCh
	if t != nil {
		t.RerankerQueryEncode = qr.dur
	}
	if qr.err != nil {
		return nil
	}
	qVec := qr.vec

	// Cosine = dot product of L2-normalized vectors.
	scores := make([]float64, len(cands))
	for i, v := range cached {
		scores[i] = dot64(v, qVec)
	}
	return scores
}

// CacheStats returns hit / miss / size counters since construction.
// Useful for ken-mcp diagnostics and the M0-style warm-vs-cold
// distinction in M4 docs.
func (r *NeuralReranker) CacheStats() (hits, misses, size int) {
	return r.cache.stats()
}

// l2NormalizeCopy returns a freshly L2-normalized copy of v so the
// cache entries are pre-normalized (cosine = dot product). Zero-norm
// vectors round-trip to all-zero (degenerate inputs stay safe).
func l2NormalizeCopy(v []float32) []float32 {
	out := make([]float32, len(v))
	var sumSq float64
	for _, x := range v {
		sumSq += float64(x) * float64(x)
	}
	if sumSq == 0 {
		return out
	}
	inv := float32(1.0 / math.Sqrt(sumSq))
	for i, x := range v {
		out[i] = x * inv
	}
	return out
}

// dot64 returns the float64 dot product of two equal-length float32
// slices. Used for cosine on L2-normalized inputs (so the result is
// in [-1, 1]). Differs in length silently returns 0 — only callable
// from internal code where length equality is invariant.
func dot64(a, b []float32) float64 {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	var s float64
	for i := 0; i < n; i++ {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}

// fnvHash is the 64-bit FNV-1a hash of s. Stdlib, no dep. FNV is a
// trivial-collision risk on adversarial inputs (the chunker doesn't
// produce those), and the consequence of a collision here is at most
// returning a stale cosine for one chunk — not a correctness bug.
func fnvHash(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}

// ── LRU embedding cache ─────────────────────────────────────────────

// embeddingLRU is a bounded content-hash → embedding map. Touching an
// entry moves it to the front (most-recent); on overflow the back
// (least-recent) is evicted. Plan §8 expressed this in terms that the
// stdlib container/list + map combo implements directly — no
// third-party dep needed.
type embeddingLRU struct {
	mu     sync.Mutex
	cap    int
	ll     *list.List
	items  map[uint64]*list.Element
	hits   int
	misses int
}

type lruEntry struct {
	key uint64
	vec []float32
}

func newEmbeddingLRU(capacity int) *embeddingLRU {
	if capacity < 0 {
		capacity = 0
	}
	return &embeddingLRU{
		cap:   capacity,
		ll:    list.New(),
		items: make(map[uint64]*list.Element),
	}
}

func (c *embeddingLRU) get(key uint64) ([]float32, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		c.ll.MoveToFront(el)
		c.hits++
		return el.Value.(*lruEntry).vec, true
	}
	c.misses++
	return nil, false
}

func (c *embeddingLRU) put(key uint64, vec []float32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cap == 0 {
		return
	}
	if el, ok := c.items[key]; ok {
		// Already present (race: another goroutine put it between
		// the miss-check and now). Refresh and exit.
		el.Value.(*lruEntry).vec = vec
		c.ll.MoveToFront(el)
		return
	}
	el := c.ll.PushFront(&lruEntry{key: key, vec: vec})
	c.items[key] = el
	for c.ll.Len() > c.cap {
		back := c.ll.Back()
		if back == nil {
			break
		}
		c.ll.Remove(back)
		delete(c.items, back.Value.(*lruEntry).key)
	}
}

func (c *embeddingLRU) stats() (hits, misses, size int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits, c.misses, c.ll.Len()
}

// snapshot returns (key, vec) pairs in least-recent → most-recent
// order, so a downstream serializer can write them out and have a
// later restore() reproduce the same eviction priority. The returned
// slices are freshly allocated; the underlying vec memory is shared
// with the live cache (callers must NOT mutate).
func (c *embeddingLRU) snapshot() (keys []uint64, vecs [][]float32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.ll.Len()
	if n == 0 {
		return nil, nil
	}
	keys = make([]uint64, 0, n)
	vecs = make([][]float32, 0, n)
	for el := c.ll.Back(); el != nil; el = el.Prev() {
		entry := el.Value.(*lruEntry)
		keys = append(keys, entry.key)
		vecs = append(vecs, entry.vec)
	}
	return keys, vecs
}

// restore re-populates the LRU from a least-recent → most-recent
// ordered pair of slices (the snapshot() shape). Entries beyond cap
// are silently dropped from the front (least-recent first), matching
// put()'s eviction policy. Replaces any existing contents.
func (c *embeddingLRU) restore(keys []uint64, vecs [][]float32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ll.Init()
	c.items = make(map[uint64]*list.Element, len(keys))
	c.hits, c.misses = 0, 0
	if c.cap == 0 {
		return
	}
	// Drop oldest entries if the snapshot exceeds cap. The snapshot is
	// ordered oldest→newest, so we skip from the front.
	start := 0
	if len(keys) > c.cap {
		start = len(keys) - c.cap
	}
	for i := start; i < len(keys); i++ {
		el := c.ll.PushFront(&lruEntry{key: keys[i], vec: vecs[i]})
		c.items[keys[i]] = el
	}
}
