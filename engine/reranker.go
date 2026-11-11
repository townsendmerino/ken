package engine

import (
	"github.com/townsendmerino/aikit/chunk"
	"github.com/townsendmerino/aikit/encoder"

	"github.com/townsendmerino/ken/internal/search"
)

// NeuralReranker is ken's second-stage reranker for ModeHybridRerank: it
// scores candidates with an aikit encoder model and keeps a content-hash
// cache of document vectors, which can be saved to and loaded from a file.
// Attach it with Workspace.SetReranker; one NeuralReranker can serve any
// number of Workspaces, and they then share its cache.
//
// Best-effort, outside the stability promise: this is the wiring the ken
// and ken-mcp binaries use, exposed so they need nothing from
// internal/search.
type NeuralReranker struct {
	r   *search.NeuralReranker
	dim int
}

// DefaultRerankCacheSize is how many document vectors a NeuralReranker's
// cache holds unless NewNeuralRerankerSized says otherwise.
const DefaultRerankCacheSize = search.DefaultRerankerCacheSize

// NewNeuralReranker wraps an encoder model the caller loaded (aikit's
// encoder.Load or encoder.LoadQ8), with the default cache size.
func NewNeuralReranker(model encoder.Encoder) *NeuralReranker {
	return &NeuralReranker{r: search.NewNeuralReranker(model), dim: model.HiddenDim()}
}

// NewNeuralRerankerSized is NewNeuralReranker with an explicit
// document-vector cache size; 0 disables the cache.
func NewNeuralRerankerSized(model encoder.Encoder, cacheSize int) *NeuralReranker {
	return &NeuralReranker{r: search.NewNeuralReranker(model, search.WithCacheSize(cacheSize)), dim: model.HiddenDim()}
}

// LazyReranker defers loading a NeuralReranker until the first query that
// needs it, so a server that rarely reranks does not pay the model load at
// startup. If the load fails, every rerank call passes the stage-1 ranking
// through unchanged (the query is served as plain hybrid). Attach it with
// Workspace.SetReranker, like a NeuralReranker.
type LazyReranker struct {
	lr *search.LazyReranker
}

// NewLazyReranker returns a LazyReranker that calls load once, on first
// use. load must be safe to call from any goroutine.
func NewLazyReranker(load func() (*NeuralReranker, error)) *LazyReranker {
	return &LazyReranker{lr: search.NewLazyReranker(func() (search.Reranker, error) {
		r, err := load()
		if err != nil || r == nil {
			return nil, err // an untyped nil, so the pipeline sees "no reranker"
		}
		return r, nil
	})}
}

// Rerank implements Reranker.
func (l *LazyReranker) Rerank(query string, cands []chunk.Chunk) []float64 {
	return l.lr.Rerank(query, cands)
}

// RerankWithTelemetry is Rerank that also fills t's reranker timings.
func (l *LazyReranker) RerankWithTelemetry(query string, cands []chunk.Chunk, t *Telemetry) []float64 {
	return l.lr.RerankWithTelemetry(query, cands, t)
}

// Loaded reports whether the load has been attempted.
func (l *LazyReranker) Loaded() bool { return l.lr.Loaded() }

// Inner returns the loaded reranker, or nil if it has not loaded or the
// load failed.
func (l *LazyReranker) Inner() *NeuralReranker {
	nr, _ := l.lr.Inner().(*NeuralReranker)
	return nr
}

// Rerank implements Reranker.
func (n *NeuralReranker) Rerank(query string, cands []chunk.Chunk) []float64 {
	return n.r.Rerank(query, cands)
}

// RerankWithTelemetry is Rerank that also fills t's reranker timings and
// cache counts; the search pipeline calls it when a query asks for
// telemetry.
func (n *NeuralReranker) RerankWithTelemetry(query string, cands []chunk.Chunk, t *Telemetry) []float64 {
	return n.r.RerankWithTelemetry(query, cands, t)
}

// CacheStats reports the document-vector cache's hits, misses and size.
func (n *NeuralReranker) CacheStats() (hits, misses, size int) { return n.r.CacheStats() }

// LoadCache fills the cache from the file at path and returns how many
// entries it loaded. modelName and quant ("f32" or "int8") scope the file:
// one written for another model, quantization or vector width is refused
// with ErrRerankCacheScopeMismatch or ErrRerankCacheDimMismatch. A missing
// file is an os.ErrNotExist error.
func (n *NeuralReranker) LoadCache(path, modelName, quant string) (int, error) {
	return search.LoadCacheFromFile(n.r, path, search.CacheScopeKey(modelName, quant, n.dim), n.dim)
}

// SaveCache writes the cache to path, scoped as LoadCache describes.
func (n *NeuralReranker) SaveCache(path, modelName, quant string) error {
	return search.SaveCacheToFile(n.r, path, search.CacheScopeKey(modelName, quant, n.dim), n.dim)
}

// Errors LoadCache returns for a cache file it will not use; test with
// errors.Is. Each means "start with an empty cache", not a failure.
var (
	ErrRerankCacheScopeMismatch = search.ErrCacheScopeMismatch
	ErrRerankCacheDimMismatch   = search.ErrCacheEmbedDimMismatch
	ErrRerankCacheCorrupt       = search.ErrCacheCorrupt
	ErrRerankCacheFormatVersion = search.ErrCacheFormatVersion
)

// InspectRerankCache reports how many entries the rerank cache file at
// path holds, reading only its header.
func InspectRerankCache(path string) (int, error) { return search.PeekRerankCacheCount(path) }
