package search

import (
	"fmt"
	"slices"
	"testing"

	"github.com/townsendmerino/aikit/chunk"
	"github.com/townsendmerino/aikit/topk"
)

// TestBM25Ranks_MatchesCount checks the one-pass rank count against the
// direct definition, with ties, zero scores and tombstones.
func TestBM25Ranks_MatchesCount(t *testing.T) {
	const n = 400
	ix := &Index{chunks: make([]chunk.Chunk, n)}
	scores := make([]float64, n)
	for d := range n {
		scores[d] = float64((d*37)%11) / 4 // many ties, some zeros
		ix.chunks[d].Tombstoned = d%13 == 0
	}
	var hits []topk.ItemWithScore[int]
	for d := range n {
		if d%3 == 0 && scores[d] > 0 && !ix.chunks[d].Tombstoned {
			hits = append(hits, topk.ItemWithScore[int]{Item: d, Score: scores[d]})
		}
	}
	slices.SortFunc(hits, topk.ItemCmp[int])
	got := ix.bm25Ranks(scores, hits)
	for j, h := range hits {
		want := 1
		for d, sd := range scores {
			if sd > 0 && !ix.chunks[d].Tombstoned && (sd > h.Score || (sd == h.Score && d < h.Item)) {
				want++
			}
		}
		if got[j] != want {
			t.Errorf("hit %d (doc %d, score %v): rank %d, want %d", j, h.Item, h.Score, got[j], want)
		}
	}
}

// wholeFiltered is SearchFilesMode's contract written the slow way: the
// whole ranking (all, from SearchMode with k = Len()), filtered to keep, cut
// to k.
func wholeFiltered(all []Result, k int, keep func(string) bool) []Result {
	var out []Result
	for _, r := range all {
		if keep(r.Chunk.File) {
			out = append(out, r)
			if len(out) == k {
				break
			}
		}
	}
	return out
}

func spanKey(c chunk.Chunk) string { return fmt.Sprintf("%s:%d-%d", c.File, c.StartLine, c.EndLine) }

// armPositions is each live chunk's 1-based position in the whole
// single-arm ranking, the reference for ArmRanks.
func armPositions(ix *Index, q string, mode Mode) map[string]int {
	all, _ := ix.SearchMode(q, ix.Len(), mode)
	m := make(map[string]int, len(all))
	for i, r := range all {
		m[spanKey(r.Chunk)] = i + 1
	}
	return m
}

// fakeReranker scores by a deterministic function of the text, so the
// rerank stage reorders the stage-1 head in a way SearchFilesMode must
// reproduce.
type fakeReranker struct{}

func (fakeReranker) Rerank(_ string, cands []chunk.Chunk) []float64 {
	out := make([]float64, len(cands))
	for i, c := range cands {
		out[i] = float64(len(c.Text)%97) / 97
	}
	return out
}

var searchFilesQueries = []string{
	"Tokenize",      // symbol query: definition boost + non-candidate scan
	"bm25.Tokenize", // qualified symbol
	"rerankTopK",    // camelCase symbol
	"how does the watcher republish the index",               // NL: stem boost
	"where is SearchMode defined and how is fusion weighted", // NL with embedded symbols
	"tombstone count over fetch",
	"the",
	"zzzzqqq_no_such_token",
}

// checkSearchFilesExact compares SearchFilesMode with wholeFiltered for every
// query × mode × file set × k. quick trims the file sets and ks, for the
// rerank path, whose SearchFilesMode is itself a whole ranking.
func checkSearchFilesExact(t *testing.T, ix *Index, modes []Mode, quick bool) {
	t.Helper()
	var files []string
	seen := map[string]bool{}
	for _, c := range ix.Chunks() {
		if !seen[c.File] {
			seen[c.File] = true
			files = append(files, c.File)
		}
	}
	slices.Sort(files)
	// One-file sets across the corpus, a two-file set, and an unknown file.
	var sets [][]string
	for i := 0; i < len(files); i += max(1, len(files)/12) {
		sets = append(sets, []string{files[i]})
	}
	sets = append(sets, []string{files[0], files[len(files)/2]}, []string{"no/such/file.go"})
	ks := []int{1, 5, 50}
	if quick {
		sets = sets[len(sets)-4:]
		ks = []int{5}
	}

	for _, mode := range modes {
		// For hybrid-rerank, rescored counts checked answers holding a
		// reranked-head chunk (one of the whole ranking's first rerankN)
		// whose score differs from its stage-1 hybrid score, and reordered
		// counts answers holding two head chunks in a different order from
		// stage 1. Both must be non-zero, so the equivalence check is known
		// to cover what the reranker does, not just the stage-1 tail.
		rescored, reordered := 0, 0
		for _, q := range searchFilesQueries {
			bmPos := armPositions(ix, q, ModeBM25)
			all, wantEff := ix.SearchMode(q, ix.Len(), mode)
			var densePos map[string]int
			if ix.Model() != nil {
				densePos = armPositions(ix, q, ModeSemantic)
			}
			qSets := sets
			head := map[string]bool{}
			stage1Score := map[string]float64{}
			stage1Pos := map[string]int{}
			if mode == ModeHybridRerank && len(all) > 0 {
				if wantEff != ModeHybridRerank {
					t.Fatalf("q=%q: SearchMode ran as %v, not hybrid-rerank; no reranker attached?", q, wantEff)
				}
				s1, _ := ix.SearchMode(q, ix.Len(), ModeHybrid)
				for i, r := range s1 {
					stage1Score[spanKey(r.Chunk)] = r.Score
					stage1Pos[spanKey(r.Chunk)] = i
				}
				// The top hit's file, and the files of the first adjacent
				// head pair the reranker inverted relative to stage 1, whose
				// answer shows that reordering.
				n := min(ix.rerankCfg.rerankN, len(all))
				for _, r := range all[:n] {
					head[spanKey(r.Chunk)] = true
				}
				qSets = append(slices.Clone(sets), []string{all[0].Chunk.File})
				for i := 0; i+1 < n; i++ {
					a, b := all[i].Chunk, all[i+1].Chunk
					if stage1Pos[spanKey(a)] > stage1Pos[spanKey(b)] {
						qSets = append(qSets, []string{a.File, b.File})
						break
					}
				}
			}
			for _, set := range qSets {
				keep := func(f string) bool { return slices.Contains(set, f) }
				for _, k := range ks {
					want := wholeFiltered(all, k, keep)
					got, ranks, eff := ix.SearchFilesMode(q, k, mode, keep)
					if eff != wantEff {
						t.Fatalf("mode %v q=%q: effective mode %v, want %v", mode, q, eff, wantEff)
					}
					if len(got) != len(want) || len(ranks) != len(got) {
						t.Errorf("mode %v q=%q files=%v k=%d: %d hits (%d ranks), want %d", mode, q, set, k, len(got), len(ranks), len(want))
						continue
					}
					if slices.ContainsFunc(got, func(r Result) bool {
						key := spanKey(r.Chunk)
						return head[key] && r.Score != stage1Score[key]
					}) {
						rescored++
					}
					var headOrder []int
					for _, r := range got {
						if key := spanKey(r.Chunk); head[key] {
							headOrder = append(headOrder, stage1Pos[key])
						}
					}
					if !slices.IsSorted(headOrder) {
						reordered++
					}
					for i := range got {
						if spanKey(got[i].Chunk) != spanKey(want[i].Chunk) || got[i].Score != want[i].Score {
							t.Errorf("mode %v q=%q files=%v k=%d hit %d: %s %v, want %s %v",
								mode, q, set, k, i, spanKey(got[i].Chunk), got[i].Score, spanKey(want[i].Chunk), want[i].Score)
							break
						}
						key := spanKey(got[i].Chunk)
						wantBM := 0
						if mode != ModeSemantic {
							wantBM = bmPos[key]
						}
						wantDense := 0
						if mode != ModeBM25 {
							wantDense = densePos[key]
						}
						if ranks[i].BM25 != wantBM || ranks[i].Dense != wantDense {
							t.Errorf("mode %v q=%q %s: ranks %+v, want BM25=%d Dense=%d", mode, q, key, ranks[i], wantBM, wantDense)
						}
					}
				}
			}
		}
		if mode == ModeHybridRerank && (rescored == 0 || reordered == 0) {
			t.Errorf("mode %v: %d checked answers held a rescored head chunk and %d a reordered pair; both must be non-zero, or the rerank stage went untested", mode, rescored, reordered)
		}
	}
}

// TestChunksOfFiles_DropsWhollyTombstonedFiles: a file whose chunks are all
// tombstoned can yield no result, so it is not a target; a partly live
// file keeps its tombstoned chunks (the whole hybrid ranking counts them in
// file saturation).
func TestChunksOfFiles_DropsWhollyTombstonedFiles(t *testing.T) {
	ix := &Index{chunks: []chunk.Chunk{
		{File: "a.go"}, {File: "dead.go", Tombstoned: true}, {File: "part.go", Tombstoned: true},
		{File: "dead.go", Tombstoned: true}, {File: "part.go"}, {File: "other.go"},
	}}
	keep := func(f string) bool { return f != "other.go" }
	if got, want := ix.chunksOfFiles(keep), []int{0, 2, 4}; !slices.Equal(got, want) {
		t.Errorf("chunksOfFiles = %v, want %v", got, want)
	}
	if got := ix.chunksOfFiles(func(f string) bool { return f == "dead.go" }); len(got) != 0 {
		t.Errorf("chunksOfFiles(dead.go only) = %v, want none", got)
	}
	// Every accepted file has a live chunk: the no-filter fast path.
	if got, want := ix.chunksOfFiles(func(f string) bool { return f == "a.go" || f == "part.go" }), []int{0, 2, 4}; !slices.Equal(got, want) {
		t.Errorf("chunksOfFiles(a.go, part.go) = %v, want %v", got, want)
	}
}

// TestSearchFilesMode_BM25Exact compares SearchFilesMode with the whole
// ranking filtered, on a real corpus (this repo's internal/ tree), with
// some chunks tombstoned.
func TestSearchFilesMode_BM25Exact(t *testing.T) {
	if testing.Short() {
		t.Skip("indexes internal/")
	}
	ix, err := FromPath("..", ModeBM25, "regex", "")
	if err != nil {
		t.Fatal(err)
	}
	for i := 3; i < len(ix.chunks); i += 41 {
		ix.chunks[i].Tombstoned = true
	}
	checkSearchFilesExact(t, ix, []Mode{ModeBM25, ModeHybrid}, false) // hybrid downgrades to bm25
}

// TestSearchFilesMode_DenseExact is the same for the modes with a dense arm,
// including hybrid-rerank with a reranker attached. Skips without
// testdata/model.
func TestSearchFilesMode_DenseExact(t *testing.T) {
	if testing.Short() {
		t.Skip("indexes internal/")
	}
	model := modelDirOrSkip(t)
	ix, err := FromPath("..", ModeHybrid, "regex", model)
	if err != nil {
		t.Fatal(err)
	}
	for i := 3; i < len(ix.chunks); i += 41 {
		ix.chunks[i].Tombstoned = true
	}
	checkSearchFilesExact(t, ix, []Mode{ModeBM25, ModeSemantic, ModeHybrid}, false)
	ix.SetReranker(fakeReranker{})
	checkSearchFilesExact(t, ix, []Mode{ModeHybridRerank}, true)
}
