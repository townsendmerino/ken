// embed_cache.go — cold-start M3: content-hash embedding cache (interface).
//
// Model2Vec encoding is deterministic (a pure function of text+model), so a
// persistent hash→vector cache lets a full rebuild re-embed only never-seen
// chunk text. This is the SECOND line of defense behind M1 (a snapshot load
// skips embedding entirely) — it helps the rebuilds M1 can't: heavy drift, a
// mode change (bm25→hybrid), or a deleted snapshot with an intact cache.
//
// IMPORTANT — no DB driver here. The concrete cache is SQLite-backed, but that
// impl lives in internal/embedcache (imported only by cmd/ken-mcp). This file
// defines only the interface, so the mcp package (imported by the
// embedded-corpus SDK path) stays free of modernc.org/sqlite — the v0.6.0
// binary-size contract (ADR-020), enforced by mcp's binary_contract_test.go.

package search

import (
	"crypto/sha256"

	"github.com/townsendmerino/aikit/embed"
)

// VecCache is a content-hash → embedding cache. The key is a hash of the chunk
// text (see hashText); the value is the Model2Vec vector for that text under the
// current model. Implementations must be safe for concurrent use (parallel
// build workers hit it). A cache miss or transient error returns (nil, false),
// which the caller handles by encoding directly.
type VecCache interface {
	Get(key []byte) ([]float32, bool)
	Put(key []byte, vec []float32)
}

// hashText is the key for the PERSISTENT embed cache. sha256 (not the cheaper
// maphash/fnv the two in-memory caches use — see hashText64 in index.go) is
// deliberate: this key is written to disk and read back across processes and
// ken versions, so it must be a stable, seed-independent digest with negligible
// collision risk, not a fast per-process hash. Not an oversight.
func hashText(text string) []byte {
	h := sha256.Sum256([]byte(text))
	return h[:]
}

// encodeCached returns the embedding for text: a cache hit is returned as-is; a
// miss is encoded by the model and written back. A nil cache (or nil model)
// falls straight through to model.Encode.
func encodeCached(c VecCache, model *embed.StaticModel, text string) []float32 {
	if model == nil {
		return nil
	}
	if c == nil {
		return model.Encode(text)
	}
	key := hashText(text)
	if v, ok := c.Get(key); ok {
		return v
	}
	v := model.Encode(text)
	c.Put(key, v)
	return v
}
