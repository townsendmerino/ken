package bm25

// BM25 defaults. These are the Lucene / bm25s defaults; docs/DESIGN.md Stage 1
// validates ranking against semble's SearchMode.BM25, so the exact variant
// is pinned to what bm25s uses by default (the Lucene IDF, see query.go).
const (
	DefaultK1 = 1.5
	DefaultB  = 0.75
)

type posting struct {
	doc int
	tf  int
}

// Index is an immutable BM25 inverted index over a fixed document set.
// Documents are referenced by their position in the slice passed to Build.
type Index struct {
	K1, B    float64
	docLen   []int
	avgdl    float64
	postings map[string][]posting
	df       map[string]int
}

// Build constructs the index from already-tokenized documents (use
// Tokenize). docs[i] is document i's token stream; empty docs are allowed
// and simply score zero.
func Build(docs [][]string) *Index {
	ix := &Index{
		K1:       DefaultK1,
		B:        DefaultB,
		docLen:   make([]int, len(docs)),
		postings: make(map[string][]posting),
		df:       make(map[string]int),
	}
	var total int
	for d, toks := range docs {
		ix.docLen[d] = len(toks)
		total += len(toks)
		tf := make(map[string]int, len(toks))
		for _, t := range toks {
			tf[t]++
		}
		for term, f := range tf {
			ix.postings[term] = append(ix.postings[term], posting{doc: d, tf: f})
			ix.df[term]++
		}
	}
	if len(docs) > 0 {
		ix.avgdl = float64(total) / float64(len(docs))
	}
	return ix
}

// N is the number of indexed documents.
func (ix *Index) N() int { return len(ix.docLen) }
