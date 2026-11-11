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
//
// Index, WatchedIndex and StructuralIndex are different: they alias
// concrete internal types, and an alias carries every exported method of
// its target. Their method sets are internal/search's and
// internal/structural's, which change without notice, so these three
// aliases and the Workspace accessors that return them (WatchedIndex,
// Structural, StructuralIfBuilt) are best-effort and outside engine's
// stability promise.

import (
	"context"

	"github.com/townsendmerino/aikit/chunk"
	"github.com/townsendmerino/ken/internal/search"
	"github.com/townsendmerino/ken/internal/structural"
)

// Index is a point-in-time index snapshot — what WatchedIndex.Load
// returns. Alias for search.Index; best-effort, outside engine's stability
// promise: its methods are internal/search's and may change in any release.
type Index = search.Index

// WatchedIndex is the live, file-watched index a Workspace wraps. Alias
// for search.WatchedIndex; best-effort, outside engine's stability promise:
// its methods are internal/search's and may change in any release. Exposed
// via Workspace.WatchedIndex for advanced/in-module callers (ken-mcp's
// existing query pipeline: per-call telemetry, filters, token budgeting)
// that need internal/search's lower-level surface beyond what
// Search/SearchMode/FindRelated model. Most callers should prefer those.
type WatchedIndex = search.WatchedIndex

// WatchedIndex returns the underlying watched index this Workspace wraps.
// Best-effort, outside engine's stability promise, like the WatchedIndex
// type it returns.
func (w *Workspace) WatchedIndex() *WatchedIndex { return w.index }

// StructuralIndex is the raw structural (symbol) index snapshot. Alias for
// structural.Index; best-effort, outside engine's stability promise: its
// methods are internal/structural's and may change in any release. Exposed
// via Workspace.Structural and Workspace.StructuralIfBuilt for callers
// (ken-mcp's structural tools and `status` tool) that need Files()/Stats()
// or their own lookups beyond what Definition/References/Callers/
// Outline/Symbols model.
type StructuralIndex = structural.Index

// Structural returns the current structural index, starting the lazy build
// on first use and waiting for it as Definition does — the raw-index escape
// hatch for a caller (ken-mcp's structural tools) that runs its own lookups
// over it. Errors are ErrStructuralBuilding and ErrNoStructuralIndex, as
// for the other structural methods. The index is immutable; after a file
// change (Refresh or the watcher) the next call returns a new one.
// Best-effort, outside engine's stability promise, like the StructuralIndex
// type it returns.
func (w *Workspace) Structural(ctx context.Context) (*StructuralIndex, error) {
	return w.structuralIndex(ctx)
}

// StructuralIfBuilt returns the current structural index only if a prior
// Definition/References/Callers/Outline/Symbols call already finished
// building it; unlike those methods, it never triggers a build itself. The
// index is immutable; a later file change publishes a new one, so call
// again rather than holding the result. Best-effort, outside engine's
// stability promise, like the StructuralIndex type it returns.
func (w *Workspace) StructuralIfBuilt() *StructuralIndex {
	return w.structural.ifBuilt()
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
