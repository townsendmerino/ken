package engine

// aliases.go — escape hatches beyond the core K1 method list
// (Search/FindRelated/Definition/References/Callers/Outline/Symbols/
// Status/Close), needed so ken-mcp can migrate its reranker and DB-Tier-2
// wiring onto Workspace too instead of holding a second, unexported
// *search.WatchedIndex reference alongside it. Reranker/RerankerOption are
// 1.0-stable type aliases over internal/search (same pattern as
// mcp/api_aliases.go) — an external implementer never needs to import
// internal/search to satisfy the Reranker interface, since its only method
// is expressed in terms of aikit's public chunk.Chunk.

import (
	"github.com/townsendmerino/aikit/chunk"
	"github.com/townsendmerino/ken/internal/search"
	"github.com/townsendmerino/ken/internal/structural"
)

// Index is a point-in-time index snapshot — what WatchedIndex.Load
// returns. 1.0-stable alias for search.Index.
type Index = search.Index

// WatchedIndex is the live, file-watched index a Workspace wraps.
// 1.0-stable alias for search.WatchedIndex. Exposed via
// Workspace.WatchedIndex for advanced/in-module callers (ken-mcp's
// existing query pipeline: per-call telemetry, filters, token budgeting)
// that need internal/search's lower-level surface beyond what
// Search/SearchMode/FindRelated model. Most callers should prefer those.
type WatchedIndex = search.WatchedIndex

// WatchedIndex returns the underlying watched index this Workspace wraps.
func (w *Workspace) WatchedIndex() *WatchedIndex { return w.index }

// StructuralIndex is the raw structural (symbol) index snapshot.
// 1.0-stable alias for structural.Index. Exposed via
// Workspace.StructuralIfBuilt for callers (ken-mcp's `status` tool) that
// need Files()/Stats() beyond what Definition/References/Callers/
// Outline/Symbols model.
type StructuralIndex = structural.Index

// StructuralIfBuilt returns the structural index only if a prior
// Definition/References/Callers/Outline/Symbols call already finished
// building it; unlike those methods, it never triggers a build itself.
func (w *Workspace) StructuralIfBuilt() *StructuralIndex {
	if w.structural.built.Load() {
		return w.structural.idx
	}
	return nil
}

// VecCache is a persistent content-hash→vector cache consulted on every
// embed (cold-start M3). 1.0-stable alias for search.VecCache.
type VecCache = search.VecCache

// Reranker re-scores a hybrid stage-1 candidate list for ModeHybridRerank.
// 1.0-stable alias for search.Reranker.
type Reranker = search.Reranker

// RerankerOption configures a Reranker attachment (candidate-count,
// score-blend weight, …). 1.0-stable alias for search.RerankerOption.
type RerankerOption = search.RerankerOption

// WithRerankN overrides the default rerank candidate count.
func WithRerankN(n int) RerankerOption { return search.WithRerankN(n) }

// WithRerankBlendBeta overrides the default stage-1/reranker score-blend weight.
func WithRerankBlendBeta(beta float64) RerankerOption { return search.WithRerankBlendBeta(beta) }

// WithAdaptiveRerankN overrides the default adaptive rerank-N threshold/floor.
func WithAdaptiveRerankN(threshold float64, minN int) RerankerOption {
	return search.WithAdaptiveRerankN(threshold, minN)
}

// SetReranker attaches (or, with r == nil, detaches) a neural reranker for
// ModeHybridRerank queries. Safe to call at any time; takes effect on the
// next published snapshot without waiting for a reindex.
func (w *Workspace) SetReranker(r Reranker, opts ...RerankerOption) {
	w.index.SetReranker(r, opts...)
}

// SetExtraChunks replaces the set of orchestrator-injected chunks unioned
// into the index alongside the filesystem corpus (ken-mcp's DB-Tier-2
// introspection chunks are the current use case). Chunks here survive
// file-watch republishes; call again with a new set (or nil) to replace
// them wholesale.
func (w *Workspace) SetExtraChunks(chunks []chunk.Chunk) {
	w.index.SetExtraChunks(chunks)
}
