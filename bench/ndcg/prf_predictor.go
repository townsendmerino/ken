//go:build bench

// Stage-7a transform #2 — M0c PRF predictor (Pseudo-Relevance Feedback,
// RM3-style restricted to identifier tokens).
//
// For each query: run baseline retrieval (no expansion, no HyDE) to
// get the top-N chunks; harvest their bm25-tokenized identifiers,
// score each token by Σ_c (idf(t) * chunk_score(c)) over containing
// chunks; drop the stopwords + length-2 tokens that the oracle
// already filters; take top-K by score.
//
// Per the planning instance's caveat: PRF amplifies whatever stage-1
// already saw. On the hard vocab-gap queries — exactly the ones HyDE
// rescues — PRF's top-N is least likely to contain the qrel doc, so
// PRF's identifier harvest comes from sibling docs in the top-N. That
// IS the realistic test: do those sibling identifiers carry the
// right vocab (authenticate appears in many auth functions, even if
// the specific qrel doc didn't make the top-10) or do they pull the
// query off-target?

package ndcg

import (
	"sort"
	"strings"

	"github.com/townsendmerino/aikit/bm25"
	"github.com/townsendmerino/aikit/embed"

	"github.com/townsendmerino/ken/internal/search"
)

// prfPredictor implements search.Predictor by lazily harvesting
// per-query identifier sets from baseline-retrieval top-N. Cached
// after first lookup so repeat Predict(query) calls within a bench
// run are free.
type prfPredictor struct {
	ix    *search.Index
	model *embed.StaticModel
	topN  int
	k     int
	cache map[string][]string
	label string
}

func (p *prfPredictor) Predict(query string) []string {
	if v, ok := p.cache[query]; ok {
		return v
	}

	// Baseline retrieval over the stripped-CSN index: hybrid mode,
	// no expansion, no HyDE. This is exactly what the M0c baseline
	// cell measures; PRF gets the same shortlist to learn from.
	qVec := p.model.Encode(query)
	hits, _ := p.ix.SearchWithQVec(query, qVec, p.topN, search.ModeHybrid)

	// Score each unique token by Σ idf(t) * chunk.Score over the
	// hits that contain it. Sibling-doc identifiers earn weight
	// proportional to how strongly their containing chunk matched
	// the original query.
	scores := make(map[string]float64)
	bm := p.ix.BM25()
	for _, h := range hits {
		seen := make(map[string]struct{})
		for _, t := range bm25.Tokenize(h.Chunk.Text) {
			if _, dup := seen[t]; dup {
				continue
			}
			seen[t] = struct{}{}
			if len(t) <= 2 {
				continue
			}
			if _, stop := oracleStopwords[t]; stop {
				continue
			}
			idf := bm.IDF(t)
			if idf == 0 {
				continue
			}
			scores[t] += idf * h.Score
		}
	}

	// Drop terms already in the query — PRF should only add NEW
	// terms; re-adding a query term is at best a no-op (BM25
	// already counts it) and at worst noise. Also drop bm25-
	// tokenized splits of query words (camelCase → multiple toks).
	queryToks := make(map[string]struct{})
	for _, t := range bm25.Tokenize(query) {
		queryToks[t] = struct{}{}
	}
	for t := range queryToks {
		delete(scores, t)
	}

	// Top-K by score, ties broken lexically for determinism.
	type scored struct {
		tok string
		s   float64
	}
	all := make([]scored, 0, len(scores))
	for t, s := range scores {
		all = append(all, scored{t, s})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].s != all[j].s {
			return all[i].s > all[j].s
		}
		return all[i].tok < all[j].tok
	})
	if len(all) > p.k {
		all = all[:p.k]
	}
	out := make([]string, len(all))
	for i, s := range all {
		out[i] = s.tok
	}
	p.cache[strings.TrimSpace(query)] = out
	return out
}

func newPRFPredictor(ix *search.Index, model *embed.StaticModel, topN, k int, label string) *prfPredictor {
	return &prfPredictor{
		ix:    ix,
		model: model,
		topN:  topN,
		k:     k,
		cache: make(map[string][]string),
		label: label,
	}
}
