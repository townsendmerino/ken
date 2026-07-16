//go:build bench

// Stage-7a transform #2 — M0d encoder-cosine predictor.
//
// The shippable Arm A: a discriminative match against the corpus's
// own identifier vocabulary. For each eligible identifier, build a
// "context centroid" = the mean of the potion vectors of the chunks
// that contain it. At query time, cosine the (already-computed)
// stage-1 query vector against the centroid matrix, take top-m above
// a threshold, hand them to the existing Predictor injection point.
//
// Locked choices (from the M0c/M0d predictor experiments; design in
// the rerank plan, docs/internal/results/ken-rerank-plan.md):
//
//   - Representation: potion centroid, NOT CodeRankEmbed centroid.
//     CodeRankEmbed would need to re-encode every chunk at index
//     time (~O(corpus)), killing ken's instant-index promise. The
//     potion centroid is essentially free — it's a mean over
//     vectors already in memory.
//
//   - Centroid vocab: DF ∈ [5, max(50, N/100)]. Singletons have
//     centroid = one chunk (degenerate, just retrieves the chunk).
//     Corpus-mean tokens (self, value, data) have near-corpus-mean
//     centroids that point nowhere — they self-filter via low
//     cosine, but capping keeps the matrix small and the intent
//     clean.
//
//   - Top-m=8 (matches oracle/PRF), threshold=0.3 (potion-pair
//     baseline ≈ 0.0–0.2 for unrelated; semantically related
//     0.3–0.5+). Both tunable via env.
//
//   - Full-weight v0 injection (no down-weight). Same path as
//     M0c oracle/PRF. Escalate to dual-retrieval RRF blend (v1)
//     only if the lost set blows up.
//
// Diagnostic the bench memo carries: a histogram of "how many
// identifiers cleared the threshold per query." Disambiguates a
// weak Arm A result. If most queries return 0 identifiers, the
// problem is either the threshold (too high) or potion
// identifier-centroid separability (potion just doesn't
// distinguish identifier contexts well) — not the top-m. If every
// query hits the m=8 cap, the threshold isn't binding.

package ndcg

import (
	"math"
	"sort"
	"sync"

	"github.com/townsendmerino/aikit/ann"
	"github.com/townsendmerino/aikit/bm25"
	"github.com/townsendmerino/aikit/embed"

	"github.com/townsendmerino/ken/internal/search"
)

type encoderPredictor struct {
	model     *embed.StaticModel
	tokens    []string
	centroids *ann.Flat
	threshold float64
	topM      int
	label     string

	// Cache + diagnostic counter (single-threaded bench, but the
	// mutex covers any future concurrency surface).
	mu        sync.Mutex
	cache     map[string][]string
	passDist  map[int]int // # passing identifiers -> # queries
	threshSum float64     // sum of top-1 cosines (avg ⇒ "even when 0 pass, how close was best?")
	queries   int
}

func (p *encoderPredictor) Predict(query string) []string {
	p.mu.Lock()
	if v, ok := p.cache[query]; ok {
		p.mu.Unlock()
		return v
	}
	p.mu.Unlock()

	qv := p.model.Encode(query)
	hits := p.centroids.Query(qv, p.topM)

	var top1 float64
	if len(hits) > 0 {
		top1 = float64(hits[0].Score)
	}

	out := make([]string, 0, p.topM)
	for _, h := range hits {
		if float64(h.Score) < p.threshold {
			break
		}
		out = append(out, p.tokens[h.Index])
	}

	p.mu.Lock()
	if p.passDist == nil {
		p.passDist = make(map[int]int)
	}
	p.passDist[len(out)]++
	p.threshSum += top1
	p.queries++
	p.cache[query] = out
	p.mu.Unlock()
	return out
}

// Histogram returns the diagnostic threshold-pass distribution for the
// memo: how many queries returned 0, 1, 2, ..., m identifiers. Plus
// the mean of the top-1 cosine across all queries — answers "even on
// queries where nothing passed, how close was the best identifier?"
// Empty before the first Predict call.
func (p *encoderPredictor) Histogram() (dist []int, meanTop1 float64, n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	dist = make([]int, p.topM+1)
	for k, v := range p.passDist {
		if k >= 0 && k <= p.topM {
			dist[k] = v
		}
	}
	n = p.queries
	if n > 0 {
		meanTop1 = p.threshSum / float64(n)
	}
	return
}

// newEncoderPredictor builds the centroid matrix from the index's
// already-encoded chunk vectors and BM25 token stats. dfFloor/dfCeil
// bracket the eligible identifier vocab.
//
// Construction cost: ~1 sec on a 14.7k-chunk corpus (the inner loop
// is one sum-into-accumulator per chunk per identifier). The
// centroid matrix is a flat [vocab × 768] f32 slab, L2-normalized
// row-wise so cosine == dot.
func newEncoderPredictor(
	ix *search.Index,
	model *embed.StaticModel,
	topM int,
	threshold float64,
	dfFloor, dfCeil int,
	label string,
) *encoderPredictor {
	chunks := ix.Chunks()
	vecs := ix.Vecs()
	bm := ix.BM25()
	if len(vecs) != len(chunks) {
		panic("encoderPredictor: vecs/chunks length mismatch")
	}
	if model == nil {
		panic("encoderPredictor: model is nil")
	}
	dim := model.Dim()

	// Pass 1: per-token sum and count over the chunks that contain
	// it. Tokens with DF outside [floor, ceil] are filtered EARLY
	// so we don't allocate sums for them. The DF check uses the
	// same bm25.DF the oracle/PRF do, ensuring vocab consistency
	// across arms.
	type accum struct {
		sum   []float64
		count int
	}
	acc := make(map[string]*accum)
	for i, c := range chunks {
		seen := make(map[string]struct{})
		for _, tok := range bm25.Tokenize(c.Text) {
			if _, dup := seen[tok]; dup {
				continue
			}
			seen[tok] = struct{}{}
			df := bm.DF(tok)
			if df < dfFloor || df > dfCeil {
				continue
			}
			if len(tok) <= 2 {
				continue
			}
			if _, stop := oracleStopwords[tok]; stop {
				continue
			}
			a, ok := acc[tok]
			if !ok {
				a = &accum{sum: make([]float64, dim)}
				acc[tok] = a
			}
			a.count++
			v := vecs[i]
			for d := 0; d < dim; d++ {
				a.sum[d] += float64(v[d])
			}
		}
	}

	// Pass 2: mean → L2-normalize. Emit deterministic order
	// (lexical) so the centroid matrix is reproducible.
	keys := make([]string, 0, len(acc))
	for k := range acc {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	tokens := make([]string, 0, len(keys))
	matrix := make([][]float32, 0, len(keys))
	for _, k := range keys {
		a := acc[k]
		v := make([]float32, dim)
		invN := 1.0 / float64(a.count)
		var sumSq float64
		for d := 0; d < dim; d++ {
			f := a.sum[d] * invN
			v[d] = float32(f)
			sumSq += f * f
		}
		if sumSq == 0 {
			continue
		}
		invNorm := float32(1.0 / math.Sqrt(sumSq))
		for d := 0; d < dim; d++ {
			v[d] *= invNorm
		}
		tokens = append(tokens, k)
		matrix = append(matrix, v)
	}

	flat := ann.New(matrix)
	return &encoderPredictor{
		model:     model,
		tokens:    tokens,
		centroids: flat,
		threshold: threshold,
		topM:      topM,
		label:     label,
		cache:     make(map[string][]string),
	}
}
