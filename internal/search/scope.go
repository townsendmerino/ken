package search

import (
	"slices"

	"github.com/townsendmerino/aikit/ann"
	"github.com/townsendmerino/aikit/bm25"
	"github.com/townsendmerino/aikit/topk"
)

// ChunkSet is a per-query set of chunk indexes: the scope a search is
// restricted to. A nil *ChunkSet means every chunk, so the unfiltered path
// takes no branch beyond a nil check. Immutable once built; safe for the
// concurrent reads the dense scan makes.
type ChunkSet struct {
	bits []uint64
	n    int // chunks in the set
}

// Has reports whether chunk i is in the set. A nil set holds every chunk.
func (s *ChunkSet) Has(i int) bool {
	if s == nil {
		return true
	}
	w := i >> 6
	return i >= 0 && w < len(s.bits) && s.bits[w]&(1<<(uint(i)&63)) != 0
}

// Len is the number of chunks in the set. It is meaningless for a nil set
// (which stands for all of them); callers hold the index's own count there.
func (s *ChunkSet) Len() int {
	if s == nil {
		return 0
	}
	return s.n
}

// ScopeOfFiles compiles a file predicate into a ChunkSet over the live
// chunks: each distinct file is decided once, then its chunks' bits are set.
// It returns the set and how many files and live chunks it holds. Tombstoned
// chunks are left out, so a scoped search never has to over-fetch for them.
func (ix *Index) ScopeOfFiles(keep func(file string) bool) (set *ChunkSet, files, chunks int) {
	set = &ChunkSet{bits: make([]uint64, (len(ix.chunks)+63)/64)}
	// A file's chunks are normally adjacent, so decide on a file change and
	// reuse the answer for the run; a file split by incremental updates is
	// simply decided again (keep is pure). Distinct in-scope files are counted
	// through a set touched only on a change.
	var (
		last    string
		lastOK  bool
		counted = make(map[string]struct{}, 64)
	)
	for i := range ix.chunks {
		c := &ix.chunks[i]
		if i == 0 || c.File != last {
			last, lastOK = c.File, keep(c.File)
		}
		if !lastOK || c.Tombstoned {
			continue
		}
		set.bits[i>>6] |= 1 << (uint(i) & 63)
		set.n++
		if _, ok := counted[c.File]; !ok {
			counted[c.File] = struct{}{}
		}
	}
	return set, len(counted), set.n
}

// filterableDense is a dense retriever that can skip rows before scoring them
// (aikit's Flat, FlatI8 and FlatBinaryI8 all have QueryFilter).
type filterableDense interface {
	QueryFilter(q []float32, k int, keep func(id int) bool) []ann.Hit
}

// queryDense is flat.Query restricted to allow. A nil allow is exactly Query.
// A retriever without QueryFilter is queried for every row and filtered, which
// is the same ranking at whole-index cost.
func queryDense(flat denseRetriever, q []float32, k int, allow *ChunkSet) []ann.Hit {
	if allow == nil {
		return flat.Query(q, k)
	}
	if f, ok := flat.(filterableDense); ok {
		return f.QueryFilter(q, k, allow.Has)
	}
	hits := flat.Query(q, len(allow.bits)*64)
	out := hits[:0]
	for _, h := range hits {
		if allow.Has(h.Index) {
			out = append(out, h)
			if len(out) == k {
				break
			}
		}
	}
	return out
}

// bm25TopK is bm.TopK restricted to allow: the k best-scoring in-scope
// documents with a positive score, ties broken by ascending document id.
// Corpus statistics (IDF, length norm) are the whole corpus's, so a chunk
// scores the same with or without the scope. A nil allow is exactly TopK.
func bm25TopK(bm *bm25.Index, terms []string, k int, allow *ChunkSet) []bm25.Result {
	if allow == nil {
		return bm.TopK(terms, k)
	}
	if k <= 0 {
		return nil
	}
	// aikit's TopK has no filter, so score every document (whole-corpus IDF and
	// length norms, as unscoped) and select over the in-scope ones. A widening
	// TopK(kk) that keeps aikit's pruning was tried and measured slower here.
	scores := bm.Scores(terms)
	sel := topk.New[int](k)
	th := sel.Threshold()
	for d, s := range scores {
		if s > 0 && s > th && allow.Has(d) {
			sel.Push(d, s)
			th = sel.Threshold()
		}
	}
	items := sel.Result()
	slices.SortFunc(items, topk.ItemCmp[int])
	out := make([]bm25.Result, len(items))
	for i, it := range items {
		out[i] = bm25.Result{Doc: it.Item, Score: it.Score}
	}
	return out
}
