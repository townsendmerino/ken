//go:build ignore

// maxsim_probe.go — slim version of the ColBERT MaxSim probe.
//
// Question: does CodeRankEmbed's token geometry carry late-interaction
// signal at the rerank stage, or is the cheap reuse path dead?
//
// Setup: for each query in csn-python-nl-stripped (first N), run
// hybrid search → top-50 shortlist (rerank-style). Then re-score each
// shortlist doc TWO ways from the SAME forward pass:
//
//   - cls    : L2-normalize position 0 of query/doc token matrices,
//              cosine. Identical to today's neural reranker baseline
//              (modulo per-query telemetry niceties we don't need here).
//   - maxsim : L2-normalize EVERY token, then score =
//                  Σ over query tokens q_i,  max over doc tokens d_j  (q_i · d_j)
//              Excludes [CLS], the QueryPrefix tokens (query side), and
//              [SEP] from both sides. [PAD] is impossible — these are
//              single-sequence forwards, not batched.
//
// Recall is invariant by construction (same shortlist). The decision
// metric is mean NDCG@10 + per-query qrel-position delta within the
// shortlist.
//
// Run:
//
//	KEN_MAXSIM_N=50 \
//	KEN_BENCH_DIR=$PWD/testdata/bench/csn-python-nl-stripped \
//	  go run scripts/maxsim_probe.go
//
// Defaults: N=25, bench=csn-python-nl-stripped, rerankN=50.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/townsendmerino/aikit/encoder"
	"github.com/townsendmerino/ken/internal/search"
)

type queryRow struct {
	QueryID string `json:"query_id"`
	Text    string `json:"text"`
}
type qrelRow struct {
	QueryID string  `json:"query_id"`
	DocID   string  `json:"doc_id"`
	Score   float64 `json:"score"`
}

func main() {
	benchDir := envS("KEN_BENCH_DIR", "testdata/bench/csn-python-nl-stripped")
	corpusDir := filepath.Join(benchDir, "corpus")
	queriesPath := filepath.Join(benchDir, "queries.jsonl")
	qrelsPath := filepath.Join(benchDir, "qrels.jsonl")
	N := envI("KEN_MAXSIM_N", 25)
	rerankN := envI("KEN_MAXSIM_RERANK_N", 50)
	includePrefix := envI("KEN_MAXSIM_INCLUDE_PREFIX", 0) == 1

	for _, p := range []string{corpusDir, queriesPath, qrelsPath} {
		if _, err := os.Stat(p); err != nil {
			die("missing %s: %v", p, err)
		}
	}

	queries := loadQueries(queriesPath)
	qrels := loadQrels(qrelsPath)
	sort.Slice(queries, func(i, j int) bool { return queries[i].QueryID < queries[j].QueryID })
	keep := make([]queryRow, 0, N)
	for _, q := range queries {
		if len(qrels[q.QueryID]) == 0 {
			continue
		}
		keep = append(keep, q)
		if len(keep) >= N {
			break
		}
	}
	fmt.Printf("queries: %d (limit %d)\n", len(keep), N)

	modelDir := envS("KEN_MODEL_DIR", filepath.Join(os.Getenv("HOME"), ".ken", "model"))
	rerankModelDir := envS("KEN_RERANK_MODEL_DIR", filepath.Join(os.Getenv("HOME"), ".ken", "rerank-model"))

	fmt.Printf("loading models — bm25/hybrid from %s, rerank from %s\n", modelDir, rerankModelDir)
	rm, err := encoder.Load(rerankModelDir)
	if err != nil {
		die("encoder.Load(%s): %v", rerankModelDir, err)
	}
	D := rm.HiddenDim()

	// Precompute the QueryPrefix token count once — we strip those
	// positions from the query side of MaxSim. EncodeQuery prepends
	// QueryPrefix to the user text, then wraps with [CLS]/[SEP]. The
	// IDs slice we get from EncodeTokensWithIDs is therefore
	// [CLS, prefix_1..prefix_p, q_1..q_n, SEP] (truncated from the
	// right at maxSeqLength).
	// We need: prefix_p = number of subword tokens the QueryPrefix
	// produces. Tokenize the prefix alone via the model's Encode pass
	// — we only want the length, not the embeddings, so we use the
	// fact that EncodeQuery("") gives us [CLS, prefix_tokens..., SEP]
	// → prefixLen = L_empty - 2.
	_, emptyIDs, err := rm.EncodeTokensWithIDs("", true)
	if err != nil {
		die("EncodeTokensWithIDs(empty query): %v", err)
	}
	prefixLen := len(emptyIDs) - 2 // strip [CLS] and [SEP]
	if prefixLen < 0 {
		prefixLen = 0
	}
	fmt.Printf("model hidden dim D=%d, query-prefix token length=%d, include_prefix=%v\n",
		D, prefixLen, includePrefix)

	t0 := time.Now()
	fmt.Printf("building search index over %s...\n", corpusDir)
	ix, err := search.FromPath(corpusDir, search.ModeHybrid, "regex", modelDir)
	if err != nil {
		die("search.FromPath: %v", err)
	}
	fmt.Printf("  built in %.1fs (%d chunks)\n", time.Since(t0).Seconds(), ix.Len())

	type cell struct {
		label   string
		ndcgSum float64
		nPos    int
	}
	cls := cell{label: "cls"}
	mx := cell{label: "maxsim"}

	movedUp, movedDown, unchanged := 0, 0, 0

	for qi, q := range keep {
		t1 := time.Now()
		shortlist := ix.Search(q.Text, rerankN)
		_ = t1
		if len(shortlist) == 0 {
			continue
		}

		// Encode query once. With prefix exclusion, the "real"
		// query tokens span [1+prefixLen, L_q-1).
		qVec, _, err := rm.EncodeTokensWithIDs(q.Text, true)
		if err != nil {
			fmt.Printf("[%s] query encode failed: %v\n", q.QueryID, err)
			continue
		}
		Lq := len(qVec) / D
		// Query span: always skip [CLS] (position 0) and [SEP]
		// (position Lq-1). Skip the QueryPrefix range only when
		// includePrefix is false (the v0 default per the kickoff;
		// ablation flag flips this).
		qStart := 1
		if !includePrefix {
			qStart = 1 + prefixLen
		}
		qEnd := Lq - 1
		if qStart >= qEnd {
			fmt.Printf("[%s] query has no body tokens after prefix-strip (Lq=%d prefixLen=%d)\n",
				q.QueryID, Lq, prefixLen)
			continue
		}
		qVecs := l2NormRows(qVec[qStart*D:qEnd*D], D)

		clsScores := make([]float64, len(shortlist))
		mxScores := make([]float64, len(shortlist))
		clsQ := l2Norm(qVec[:D]) // [CLS] of query — used for `cls` cell

		for di, r := range shortlist {
			dVec, _, err := rm.EncodeTokensWithIDs(r.Chunk.Text, false)
			if err != nil {
				continue
			}
			Ld := len(dVec) / D
			if Ld < 2 {
				continue
			}
			// cls: cosine(query[CLS], doc[CLS]).
			clsScores[di] = dot(clsQ, l2Norm(dVec[:D]))

			// maxsim: real tokens span [1, Ld-1).
			dStart, dEnd := 1, Ld-1
			if dStart >= dEnd {
				continue
			}
			dVecs := l2NormRows(dVec[dStart*D:dEnd*D], D)
			// Sum over query tokens of max over doc tokens of dot.
			var sum float64
			nQ := qEnd - qStart
			nD := dEnd - dStart
			for i := 0; i < nQ; i++ {
				qi := qVecs[i*D : (i+1)*D]
				best := float32(-2.0)
				for j := 0; j < nD; j++ {
					dj := dVecs[j*D : (j+1)*D]
					s := dotF32(qi, dj)
					if s > best {
						best = s
					}
				}
				sum += float64(best)
			}
			mxScores[di] = sum
		}

		// Build (file → best score) per cell.
		clsRanked := topByScore(shortlist, clsScores)
		mxRanked := topByScore(shortlist, mxScores)

		clsNDCG := ndcg10(clsRanked, qrels[q.QueryID])
		mxNDCG := ndcg10(mxRanked, qrels[q.QueryID])
		cls.ndcgSum += clsNDCG
		mx.ndcgSum += mxNDCG
		cls.nPos++
		mx.nPos++

		// Per-query qrel position delta within shortlist.
		clsPos := qrelPos(clsRanked, qrels[q.QueryID])
		mxPos := qrelPos(mxRanked, qrels[q.QueryID])
		switch {
		case mxPos < clsPos:
			movedUp++
		case mxPos > clsPos:
			movedDown++
		default:
			unchanged++
		}

		if qi < 5 || qi%25 == 0 {
			fmt.Printf("  [%3d] %-40s clsPos=%2d ndcg=%.3f | mxPos=%2d ndcg=%.3f | Δndcg=%+.3f\n",
				qi+1, trunc(q.QueryID+": "+q.Text, 40),
				clsPos, clsNDCG, mxPos, mxNDCG, mxNDCG-clsNDCG)
		}
	}

	fmt.Println("\n=== ColBERT MaxSim probe — slim ===")
	fmt.Printf("bench:          %s\n", benchDir)
	fmt.Printf("queries:        %d (limit %d)\n", cls.nPos, N)
	fmt.Printf("rerankN:        %d\n", rerankN)
	fmt.Printf("cls NDCG@10:    %.4f (baseline — same as today's reranker)\n", cls.ndcgSum/float64(cls.nPos))
	fmt.Printf("maxsim NDCG@10: %.4f\n", mx.ndcgSum/float64(mx.nPos))
	fmt.Printf("Δ NDCG@10:      %+.4f\n", (mx.ndcgSum-cls.ndcgSum)/float64(mx.nPos))
	fmt.Printf("per-query qrel position within shortlist:\n")
	fmt.Printf("  moved up (maxsim better):   %d\n", movedUp)
	fmt.Printf("  moved down (maxsim worse):  %d\n", movedDown)
	fmt.Printf("  unchanged:                  %d\n", unchanged)
}

// -------------------- helpers --------------------

func loadQueries(path string) []queryRow {
	f, err := os.Open(path)
	if err != nil {
		die("open %s: %v", path, err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var out []queryRow
	for sc.Scan() {
		var q queryRow
		if err := json.Unmarshal(sc.Bytes(), &q); err != nil {
			die("decode queries: %v", err)
		}
		out = append(out, q)
	}
	return out
}

func loadQrels(path string) map[string]map[string]float64 {
	f, err := os.Open(path)
	if err != nil {
		die("open %s: %v", path, err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	out := make(map[string]map[string]float64)
	for sc.Scan() {
		var r qrelRow
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			die("decode qrels: %v", err)
		}
		if r.Score <= 0 {
			continue
		}
		m := out[r.QueryID]
		if m == nil {
			m = map[string]float64{}
			out[r.QueryID] = m
		}
		m[r.DocID] = r.Score
	}
	return out
}

type fileRanked struct {
	File  string
	Score float64
}

func topByScore(shortlist []search.Result, scores []float64) []fileRanked {
	best := map[string]float64{}
	for i, r := range shortlist {
		f := r.Chunk.File
		if s, ok := best[f]; !ok || scores[i] > s {
			best[f] = scores[i]
		}
	}
	out := make([]fileRanked, 0, len(best))
	for f, s := range best {
		out = append(out, fileRanked{File: f, Score: s})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}

func ndcg10(ranked []fileRanked, qrels map[string]float64) float64 {
	if len(qrels) == 0 {
		return 0
	}
	// csn doc_ids land as "<docid>.py" on disk.
	gold := make(map[string]float64, len(qrels)*2)
	for d, s := range qrels {
		gold[d] = s
		gold[d+".py"] = s
	}
	var dcg, ideal float64
	limit := 10
	if limit > len(ranked) {
		limit = len(ranked)
	}
	for i := 0; i < limit; i++ {
		if rel, ok := gold[filepath.Base(ranked[i].File)]; ok {
			dcg += rel / math.Log2(float64(i+2))
		}
	}
	scores := make([]float64, 0, len(qrels))
	for _, s := range qrels {
		scores = append(scores, s)
	}
	sort.Slice(scores, func(i, j int) bool { return scores[i] > scores[j] })
	for i := 0; i < limit && i < len(scores); i++ {
		ideal += scores[i] / math.Log2(float64(i+2))
	}
	if ideal == 0 {
		return 0
	}
	return dcg / ideal
}

func qrelPos(ranked []fileRanked, qrels map[string]float64) int {
	gold := make(map[string]bool, len(qrels)*2)
	for d := range qrels {
		gold[d] = true
		gold[d+".py"] = true
	}
	for i, r := range ranked {
		if gold[filepath.Base(r.File)] {
			return i + 1
		}
	}
	return 999
}

// l2Norm L2-normalizes a single D-dim row and returns a new float64
// slice. Zero-norm input → zero output.
func l2Norm(v []float32) []float64 {
	out := make([]float64, len(v))
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return out
	}
	inv := 1.0 / math.Sqrt(sum)
	for i, x := range v {
		out[i] = float64(x) * inv
	}
	return out
}

func l2NormRows(rows []float32, D int) []float32 {
	N := len(rows) / D
	out := make([]float32, N*D)
	for i := 0; i < N; i++ {
		var sum float64
		for j := 0; j < D; j++ {
			x := float64(rows[i*D+j])
			sum += x * x
		}
		if sum == 0 {
			continue
		}
		inv := float32(1.0 / math.Sqrt(sum))
		for j := 0; j < D; j++ {
			out[i*D+j] = rows[i*D+j] * inv
		}
	}
	return out
}

func dot(a, b []float64) float64 {
	var sum float64
	for i := range a {
		sum += a[i] * b[i]
	}
	return sum
}

func dotF32(a, b []float32) float32 {
	var sum float32
	for i := range a {
		sum += a[i] * b[i]
	}
	return sum
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}

func envI(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
func envS(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}
func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
