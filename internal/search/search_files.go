package search

import (
	"slices"

	"github.com/townsendmerino/aikit/bm25"
	"github.com/townsendmerino/aikit/fuse"
	"github.com/townsendmerino/aikit/topk"
)

// ArmRanks is a chunk's 1-based rank in each retrieval arm's whole-index
// ranking for a query, counting only live (non-tombstoned) chunks. 0 means
// the chunk is not in that arm (a zero BM25 score, or the arm didn't run for
// the mode).
type ArmRanks struct {
	BM25  int
	Dense int
}

// SearchFilesMode is SearchMode restricted to the chunks of files for which
// keep(file) is true, exactly: it returns what SearchMode(query, n, mode)
// would, for any n ≥ Len(), filtered to those files and cut to k — the same
// chunks, scores and order — at any index size. ranks[i] is results[i]'s
// whole-index rank in each arm.
//
// How each mode gets there without ranking the whole index:
//
//   - ModeBM25 scores every document with the index's global statistics
//     (bm25.Index.Scores, the same scoring TopK uses) and keeps the
//     requested files' positive scores; order is score desc then chunk
//     index asc, TopK's tie-break. No sort over the whole index.
//   - ModeSemantic runs the dense arm at the whole ranking's depth (the same
//     retriever call, so the same scores and tie-breaks for every retriever
//     kind) and filters it.
//   - ModeHybrid runs both arms and their RRF fusion at the whole ranking's
//     depth, then the file-coherence boost over every candidate, since its
//     scale (the max score and the largest file aggregate) is global. The
//     query boosts, path penalties and file saturation are per chunk given
//     that scale, or per file, so they run on the requested files' chunks
//     only, with the global max passed in. That skips the definition-regex
//     scan and the final sort over the rest of the index, which dominate a
//     whole-index hybrid ranking.
//   - ModeHybridRerank with a reranker attached reranks the global stage-1
//     head, whose membership depends on every chunk, so it runs the whole
//     ranking and filters it: exact, at whole-ranking cost.
//
// Like SearchMode, mode is downgraded when the index lacks the retriever it
// needs, and the effective mode is returned.
func (ix *Index) SearchFilesMode(query string, k int, mode Mode, keep func(file string) bool) ([]Result, []ArmRanks, Mode) {
	mode = ix.resolveMode(mode)
	if k <= 0 || len(ix.chunks) == 0 {
		return nil, nil, mode
	}
	target := ix.chunksOfFiles(keep)
	if len(target) == 0 {
		return nil, nil, mode
	}
	// The whole ranking's depth: SearchMode(query, Len(), mode) over-fetches
	// by the tombstone count; any n ≥ Len() gives the same ranking.
	whole := len(ix.chunks) + ix.tombstoneCount()

	switch mode {
	case ModeSemantic:
		hits := ix.flat.Query(ix.model.Encode(query), whole)
		var res []Result
		var ranks []ArmRanks
		live := 0
		for _, h := range hits {
			c := ix.chunks[h.Index]
			if c.Tombstoned {
				continue
			}
			live++
			if !keep(c.File) {
				continue
			}
			res = append(res, Result{Chunk: c, Score: h.Score})
			ranks = append(ranks, ArmRanks{Dense: live})
			if len(res) == k {
				break
			}
		}
		return res, ranks, mode

	case ModeHybrid:
		return ix.searchFilesHybrid(query, k, whole, target, keep)

	case ModeHybridRerank:
		qVec := ix.model.Encode(query)
		all := ix.searchCore(query, qVec, nil, whole, mode, AdaptiveAlphas, RankAblation{})
		f := runArms(query, qVec, ix.flat, ix.bm, whole, AdaptiveAlphas, nil)
		bmRank, denseRank := ix.liveRanks(f.bmOrder), ix.liveRanks(f.semOrder)
		var res []Result
		var ranks []ArmRanks
		for _, r := range all {
			if !keep(r.Chunk.File) {
				continue
			}
			res = append(res, r)
			ranks = append(ranks, ArmRanks{})
			if len(res) == k {
				break
			}
		}
		// all carries chunks by value; recover their indices to look up ranks.
		// Results are live, so only live chunks can match a span.
		idxOf := make(map[chunkSpan]int, len(target))
		for _, i := range target {
			if !ix.chunks[i].Tombstoned {
				idxOf[spanOf(i, ix)] = i
			}
		}
		for j, r := range res {
			if i, ok := idxOf[chunkSpan{r.Chunk.File, r.Chunk.StartLine, r.Chunk.EndLine}]; ok {
				ranks[j] = ArmRanks{BM25: bmRank[i], Dense: denseRank[i]}
			}
		}
		return res, ranks, mode

	default: // ModeBM25
		scores := ix.bm.Scores(bm25.Tokenize(query))
		cand := make([]topk.ItemWithScore[int], 0, len(target))
		for _, i := range target {
			if s := scores[i]; s > 0 && !ix.chunks[i].Tombstoned {
				cand = append(cand, topk.ItemWithScore[int]{Item: i, Score: s})
			}
		}
		slices.SortFunc(cand, topk.ItemCmp[int])
		if len(cand) > k {
			cand = cand[:k]
		}
		res := make([]Result, len(cand))
		ranks := make([]ArmRanks, len(cand))
		for j, c := range cand {
			res[j] = Result{Chunk: ix.chunks[c.Item], Score: c.Score}
			ranks[j] = ArmRanks{BM25: ix.bm25RankOf(scores, c.Item)}
		}
		return res, ranks, mode
	}
}

// searchFilesHybrid is SearchFilesMode's ModeHybrid branch; see its doc.
// It mirrors hybridSearch for the shipped pipeline (AdaptiveAlphas, no
// predicted identifiers, no ablation): the same arm calls (runArms), then the
// RRF fusion and the file-coherence boost over every candidate, computed over
// dense per-chunk slices rather than fuse.RRFWeighted's map and sort and
// boostMultiChunkFiles' map — the same float operations in the same order,
// so the same bits, at a fraction of the cost on a large index. Only the
// requested files' chunks go on to the query boosts, penalties and
// saturation. TestSearchFilesMode_DenseExact pins the equivalence.
func (ix *Index) searchFilesHybrid(query string, k, whole int, target []int, keep func(string) bool) ([]Result, []ArmRanks, Mode) {
	f := runArms(query, ix.model.Encode(query), ix.flat, ix.bm, whole, AdaptiveAlphas, nil)
	n := len(ix.chunks)

	// fuse.RRFWeighted: each key's score starts at 0 and adds w/(k+rank) for
	// the semantic list, then the BM25 list.
	fused := make([]float64, n)
	in := make([]bool, n)
	wSem, wBM := f.alpha, 1.0-f.alpha
	for r, i := range f.semOrder {
		fused[i] += wSem / (fuse.DefaultK + float64(r+1))
		in[i] = true
	}
	for r, i := range f.bmOrder {
		fused[i] += wBM / (fuse.DefaultK + float64(r+1))
		in[i] = true
	}
	hasCand := false
	for _, ok := range in {
		if ok {
			hasCand = true
			break
		}
	}
	if !hasCand {
		return nil, nil, ModeHybrid
	}

	// boostMultiChunkFiles: per file, the candidates' score sum (ascending
	// chunk index) and best chunk (first maximum); each file's best gains
	// maxScore*0.2*fileSum/maxFileSum.
	if ms0 := denseMax(fused, in); ms0 != 0 {
		fileID := make(map[string]int, 1024)
		var fileSum []float64
		var best []int
		for i := range n {
			if !in[i] {
				continue
			}
			fp := ix.chunks[i].File
			id, ok := fileID[fp]
			if !ok {
				id = len(fileSum)
				fileID[fp] = id
				fileSum = append(fileSum, 0)
				best = append(best, i)
			} else if fused[i] > fused[best[id]] {
				best[id] = i
			}
			fileSum[id] += fused[i]
		}
		maxFileSum := 0.0
		for _, v := range fileSum {
			if v > maxFileSum {
				maxFileSum = v
			}
		}
		boostUnit := ms0 * fileCoherenceBoostFrac
		for id, b := range best {
			fused[b] += boostUnit * fileSum[id] / maxFileSum
		}
	}
	ms := denseMax(fused, in)

	sub := make(map[int]float64, len(target))
	for _, i := range target {
		if in[i] {
			sub[i] = fused[i]
		}
	}
	// The scans see only the requested files' chunks; membership in sub is
	// membership in the whole candidate set for them, so a non-candidate
	// here is a non-candidate in the whole ranking.
	applyQueryBoostWithMax(sub, ms, query, ix.chunks, nil, target)
	ranked := rerankTopK(sub, ix.chunks, len(sub), f.alpha < 1.0, fileSaturationThresh, fileSaturationDecay)

	bmRank, denseRank := ix.liveRanks(f.bmOrder), ix.liveRanks(f.semOrder)
	var res []Result
	var ranks []ArmRanks
	for _, it := range ranked {
		c := ix.chunks[it.idx]
		if c.Tombstoned || !keep(c.File) {
			continue
		}
		res = append(res, Result{Chunk: c, Score: it.score})
		ranks = append(ranks, ArmRanks{BM25: bmRank[it.idx], Dense: denseRank[it.idx]})
		if len(res) == k {
			break
		}
	}
	return res, ranks, ModeHybrid
}

// denseMax is maxScore over the chunks marked in.
func denseMax(scores []float64, in []bool) float64 {
	hi, first := 0.0, true
	for i, ok := range in {
		if ok && (first || scores[i] > hi) {
			hi, first = scores[i], false
		}
	}
	return hi
}

// chunksOfFiles returns, ascending, the indices of every chunk whose file
// keep accepts. keep is called once per distinct file.
func (ix *Index) chunksOfFiles(keep func(string) bool) []int {
	var out []int
	seen := map[string]bool{}
	for i := range ix.chunks {
		fp := ix.chunks[i].File
		ok, done := seen[fp]
		if !done {
			ok = keep(fp)
			seen[fp] = ok
		}
		if ok {
			out = append(out, i)
		}
	}
	return out
}

// liveRanks maps each chunk index in order (best first) to its 1-based rank
// among order's live chunks; 0 for a chunk not in order.
func (ix *Index) liveRanks(order []int) []int {
	m := make([]int, len(ix.chunks))
	r := 0
	for _, i := range order {
		if ix.chunks[i].Tombstoned {
			continue
		}
		r++
		m[i] = r
	}
	return m
}

// bm25RankOf is doc's 1-based rank among live documents with a positive
// score, under TopK's order (score desc, then doc asc): one plus the number
// that rank ahead of it. A count over the scores, not a sort.
func (ix *Index) bm25RankOf(scores []float64, doc int) int {
	s := scores[doc]
	ahead := 0
	for d, sd := range scores {
		if sd <= 0 || ix.chunks[d].Tombstoned {
			continue
		}
		if sd > s || (sd == s && d < doc) {
			ahead++
		}
	}
	return ahead + 1
}

type chunkSpan struct {
	file       string
	start, end int
}

func spanOf(i int, ix *Index) chunkSpan {
	c := ix.chunks[i]
	return chunkSpan{c.File, c.StartLine, c.EndLine}
}
