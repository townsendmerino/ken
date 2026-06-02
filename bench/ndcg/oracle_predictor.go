//go:build bench

// Stage-7a transform #2 — M0c oracle predictor.
//
// The oracle "predicts" the K identifier tokens that are most useful
// for retrieval, by reading them straight out of the known-relevant
// chunks for each query. It's not a predictor in the realistic sense
// — it's the **ceiling** every realistic predictor is calibrated
// against: if even the oracle can't move the flip-set, the mechanism
// has no headroom on this bench.
//
// Two variants per the planning instance's review:
//
//   - "max" — top-K by descending IDF, no DF cap. Pure mechanism
//     ceiling. Skews toward near-hapax tokens (rare in the corpus,
//     near-perfect BM25 keys to the exact qrel doc — but impossible
//     prediction targets). Use only as the go/no-go gate.
//
//   - "df-floor" — same ranking, but filter to df(token) ≥ floor
//     before taking top-K. Realistic ceiling a predictor could
//     plausibly reach (mid-frequency, semantically meaningful
//     identifiers like authenticate / session — exactly what
//     transform #2 is supposed to predict). This is the number
//     PRF / encoder-cosine / Claude-generated arms get compared to.

package ndcg

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/townsendmerino/aikit/bm25"
	"github.com/townsendmerino/aikit/chunk"
)

// oracleStopwords are tokens dropped before ranking. Python language
// keywords + a small "boilerplate" set of identifiers that appear
// across virtually every function and carry no retrieval signal. The
// DF floor catches most of these on its own, but the explicit list
// also cleans up the "max" variant which has no DF cap.
var oracleStopwords = map[string]struct{}{
	"def": {}, "return": {}, "if": {}, "elif": {}, "else": {}, "for": {},
	"while": {}, "try": {}, "except": {}, "finally": {}, "with": {},
	"as": {}, "in": {}, "is": {}, "not": {}, "and": {}, "or": {},
	"none": {}, "true": {}, "false": {}, "lambda": {}, "yield": {},
	"raise": {}, "pass": {}, "break": {}, "continue": {}, "import": {},
	"from": {}, "class": {}, "self": {}, "cls": {}, "args": {},
	"kwargs": {}, "init": {}, "str": {}, "int": {}, "list": {},
	"dict": {}, "set": {}, "tuple": {}, "bool": {}, "type": {},
}

// oraclePredictor implements search.Predictor by looking up a
// precomputed per-query identifier set. Goroutine-safe: the map is
// read-only after construction.
type oraclePredictor struct {
	byQuery map[string][]string
	label   string // for log lines
}

func (p *oraclePredictor) Predict(query string) []string {
	// The bench loop calls Predict with the query text, not the
	// query id — so we need a way to map text→id. Building two
	// instances (one indexed by qid, one by text) is the cleanest;
	// we use text as the key here since the harness already has
	// q.QueryID + q.Text in scope at the call site and constructs
	// the predictor with text keys. See newOraclePredictor.
	return p.byQuery[query]
}

// newOraclePredictor precomputes per-query oracle identifier sets
// from the qrel-positive chunks. df < dfFloor tokens are filtered out
// (set dfFloor=0 for the "max" variant). The top-K tokens by IDF
// remain. Returns one predictor instance ready for the bench loop.
//
// The implementation tokenizes each qrel-positive chunk's text via
// bm25.Tokenize — the same splitter the index used at build time, so
// every emitted token is a real key in the BM25 inverted index (no
// camel/snake mismatch). The DF / IDF lookups go through the new
// aikit accessors on bm25.Index.
//
// label is a short identifier (e.g. "oracle-max", "oracle-df5") used
// in log lines.
func newOraclePredictor(
	queries []queryRow,
	qrelsByQuery map[string]map[string]float64,
	chunks []chunk.Chunk,
	bm *bm25.Index,
	corpusDir string,
	k int,
	dfFloor int,
	label string,
) *oraclePredictor {
	// Build a doc_id → []chunk-index map so we can find chunks for
	// each qrel. doc_id matches aggregateByDoc's derivation: strip
	// .py from the chunk's File basename.
	chunksByDoc := make(map[string][]int, len(chunks))
	for i, c := range chunks {
		docID := strings.TrimSuffix(filepath.Base(c.File), ".py")
		chunksByDoc[docID] = append(chunksByDoc[docID], i)
	}

	type scoredTok struct {
		tok string
		idf float64
		df  int
	}

	byQuery := make(map[string][]string, len(queries))
	for _, q := range queries {
		rels := qrelsByQuery[q.QueryID]
		if len(rels) == 0 {
			continue
		}

		// Collect unique tokens across every qrel-positive chunk
		// for this query. Tracking per-token in a set avoids
		// double-counting tokens that appear in multiple qrel
		// docs.
		seen := make(map[string]struct{})
		var toks []scoredTok
		for docID, rel := range rels {
			if rel <= 0 {
				continue
			}
			// Prefer chunks already in the index — fast path —
			// fall back to reading the file off disk if the
			// chunker happened to split it in a way that
			// doesn't preserve the docID basename.
			cIdxs, ok := chunksByDoc[docID]
			var text string
			if ok && len(cIdxs) > 0 {
				var sb strings.Builder
				for _, idx := range cIdxs {
					sb.WriteString(chunks[idx].Text)
					sb.WriteByte('\n')
				}
				text = sb.String()
			} else {
				b, err := os.ReadFile(filepath.Join(corpusDir, docID+".py"))
				if err != nil {
					continue
				}
				text = string(b)
			}
			for _, t := range bm25.Tokenize(text) {
				if _, dup := seen[t]; dup {
					continue
				}
				if len(t) <= 2 {
					continue
				}
				if _, stop := oracleStopwords[t]; stop {
					continue
				}
				df := bm.DF(t)
				if df < dfFloor {
					continue
				}
				idf := bm.IDF(t)
				if idf == 0 {
					continue
				}
				seen[t] = struct{}{}
				toks = append(toks, scoredTok{tok: t, idf: idf, df: df})
			}
		}

		// Rank by descending IDF (= ascending DF). Take top-K.
		// Ties broken by lexical order for determinism.
		sort.Slice(toks, func(i, j int) bool {
			if toks[i].idf != toks[j].idf {
				return toks[i].idf > toks[j].idf
			}
			return toks[i].tok < toks[j].tok
		})
		if len(toks) > k {
			toks = toks[:k]
		}
		out := make([]string, len(toks))
		for i, s := range toks {
			out[i] = s.tok
		}
		byQuery[q.Text] = out
	}

	return &oraclePredictor{byQuery: byQuery, label: label}
}
