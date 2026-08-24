//go:build bench

// Package chunkdiff measures chunker TRACEABILITY — does a retrieved
// span map cleanly onto one symbol? — independently of any query.
//
// Why this exists (docs/internal/rag-thread-followups.md item 2): the
// treesitter chunker's aggregate NDCG delta on the semble bench is
// −0.004, inside noise, and ADR-011 keeps regex as the default on that
// basis. The r/Rag thread's objection was that aggregate NDCG is the
// wrong instrument for the question — a chunker can leave ranking
// unchanged while making every returned span materially more useful to
// an agent, because the agent has to READ the span, not just rank it.
//
// Two query-independent properties capture that:
//
//   - SPLIT: a definition whose [start,end] lines are not contained in
//     any single chunk. Retrieval can surface it, but no single result
//     shows the whole thing — the agent gets half a function.
//   - MIXED: a chunk containing the start of two or more TOP-LEVEL
//     definitions. The span doesn't map to one symbol.
//
// Deliberate asymmetry in those two definitions. Splitting is measured
// over LEAF definitions (functions and methods): a method cut in half
// is a real defect. Mixing is measured over TOP-LEVEL definitions
// (top-level functions and classes) because a chunk holding a whole
// class with five methods does map to one symbol — counting its five
// method starts as "mixing" would penalize exactly the outcome we want.
//
// Definition spans come from internal/structural (the same extractor
// Arm B enrichment already runs), so this is a join against data ken
// computes anyway, not new parsing. Files whose extension has no
// registered extractor are skipped — with no definitions there is
// nothing to be traceable to.
package chunkdiff

import (
	"sort"

	"github.com/townsendmerino/aikit/chunk"
	"github.com/townsendmerino/ken/internal/structural"
)

// Span is an inclusive 1-based line range.
type Span struct {
	Start, End int
}

// valid reports whether the extractor actually recorded this span.
// Zero values mean "not recorded" per FuncDef/ClassDef's contract, and
// a zero span silently counted as split would manufacture defects.
func (s Span) valid() bool { return s.Start > 0 && s.End >= s.Start }

// FileReport is one file's traceability counts under one chunker.
type FileReport struct {
	Path     string `json:"path"`
	Language string `json:"language"`
	Chunker  string `json:"chunker"`

	Chunks int `json:"chunks"`

	// LeafDefs is every function and method with a recorded span.
	// SplitDefs is how many of those no single chunk fully contains.
	LeafDefs  int `json:"leaf_defs"`
	SplitDefs int `json:"split_defs"`

	// TopLevelDefs is top-level functions plus classes.
	// ChunksWithTopLevel is how many chunks contain at least one such
	// definition's START line; MixedChunks how many contain two or more.
	TopLevelDefs       int `json:"top_level_defs"`
	ChunksWithTopLevel int `json:"chunks_with_top_level"`
	MixedChunks        int `json:"mixed_chunks"`
}

// Analyze chunks one file with the named chunker and joins the result
// against its definition spans. Returns ok=false when the file has no
// registered extractor or yields no spanned definitions — nothing to
// measure, and counting it as perfectly traceable would dilute every
// rate with files that never had a symbol in them.
func Analyze(chunkerName, rel string, data []byte) (FileReport, bool) {
	fs := structural.ExtractFile(rel, data)
	if fs == nil {
		return FileReport{}, false
	}
	leaf, topLevel := definitionSpans(fs)
	if len(leaf) == 0 && len(topLevel) == 0 {
		return FileReport{}, false
	}

	chunks, err := chunk.ChunkFile(chunkerName, rel, data, chunk.DefaultChunkSize)
	if err != nil || len(chunks) == 0 {
		return FileReport{}, false
	}

	rep := FileReport{
		Path:         rel,
		Language:     chunk.Language(rel),
		Chunker:      chunkerName,
		Chunks:       len(chunks),
		LeafDefs:     len(leaf),
		TopLevelDefs: len(topLevel),
	}

	for _, d := range leaf {
		if !containedInSome(d, chunks) {
			rep.SplitDefs++
		}
	}
	for _, c := range chunks {
		starts := 0
		for _, d := range topLevel {
			if d.Start >= c.StartLine && d.Start <= c.EndLine {
				starts++
			}
		}
		if starts >= 1 {
			rep.ChunksWithTopLevel++
		}
		if starts >= 2 {
			rep.MixedChunks++
		}
	}
	return rep, true
}

// containedInSome reports whether any single chunk fully covers d.
//
// "Any single chunk" rather than "the chunk containing d.Start" is
// deliberate: the line chunker emits OVERLAPPING windows precisely so
// a definition straddling one boundary still appears whole in a
// neighbour, and scoring it as split would punish a chunker for the
// feature that fixes the problem being measured.
func containedInSome(d Span, chunks []chunk.Chunk) bool {
	for _, c := range chunks {
		if c.StartLine <= d.Start && c.EndLine >= d.End {
			return true
		}
	}
	return false
}

// definitionSpans splits a FileStruct's definitions into the leaf set
// (functions + methods) and the top-level set (non-method functions +
// classes). Spans the extractor didn't record are dropped rather than
// guessed.
func definitionSpans(fs *structural.FileStruct) (leaf, topLevel []Span) {
	for _, fn := range fs.Functions {
		s := Span{fn.StartLine, fn.EndLine}
		if !s.valid() {
			continue
		}
		leaf = append(leaf, s)
		if !fn.IsMethod {
			topLevel = append(topLevel, s)
		}
	}
	for _, cls := range fs.Classes {
		s := Span{cls.StartLine, cls.EndLine}
		if !s.valid() {
			continue
		}
		topLevel = append(topLevel, s)
	}
	sort.Slice(leaf, func(i, j int) bool { return leaf[i].Start < leaf[j].Start })
	sort.Slice(topLevel, func(i, j int) bool { return topLevel[i].Start < topLevel[j].Start })
	return leaf, topLevel
}

// Totals aggregates file reports into one row.
type Totals struct {
	Chunker            string `json:"chunker"`
	Files              int    `json:"files"`
	Chunks             int    `json:"chunks"`
	LeafDefs           int    `json:"leaf_defs"`
	SplitDefs          int    `json:"split_defs"`
	TopLevelDefs       int    `json:"top_level_defs"`
	ChunksWithTopLevel int    `json:"chunks_with_top_level"`
	MixedChunks        int    `json:"mixed_chunks"`
}

// SplitRate is the fraction of definitions no single chunk contains
// whole. Lower is better.
func (t Totals) SplitRate() float64 {
	if t.LeafDefs == 0 {
		return 0
	}
	return float64(t.SplitDefs) / float64(t.LeafDefs)
}

// MixedRate is the fraction of definition-bearing chunks that carry
// two or more top-level definitions. Lower is better.
func (t Totals) MixedRate() float64 {
	if t.ChunksWithTopLevel == 0 {
		return 0
	}
	return float64(t.MixedChunks) / float64(t.ChunksWithTopLevel)
}

// Add folds one file report into the totals.
func (t *Totals) Add(r FileReport) {
	t.Files++
	t.Chunks += r.Chunks
	t.LeafDefs += r.LeafDefs
	t.SplitDefs += r.SplitDefs
	t.TopLevelDefs += r.TopLevelDefs
	t.ChunksWithTopLevel += r.ChunksWithTopLevel
	t.MixedChunks += r.MixedChunks
}
