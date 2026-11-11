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

// NewNeuralReranker wraps an encoder model the caller loaded (aikit's
// encoder.Load or encoder.LoadQ8).
func NewNeuralReranker(model encoder.Encoder) *NeuralReranker {
	return &NeuralReranker{r: search.NewNeuralReranker(model), dim: model.HiddenDim()}
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
