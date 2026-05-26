// Package treesitter is ken's v0.2.0 chunker: a pure-Go tree-sitter
// runtime via github.com/odvcencio/gotreesitter, with chunk boundaries
// determined by the cAST split-then-merge algorithm (arXiv 2506.15655 —
// the same algorithm Chonkie uses). Registers itself as "treesitter" in
// the chunk registry on import; blank-imported from internal/search like
// the regex chunker is.
//
// Why this exists (docs/DESIGN.md §2 + §10): v0.1.0 measured a 0.012
// hybrid NDCG@10 gap vs semble (0.842 vs 0.854) localized to languages
// where ken's regex chunker draws different boundaries than semble's
// tree-sitter chunker. Python with hand-tuned regex tracked semble
// exactly (+0.003); go/rust/zig sat at −0.05. This package closes that
// gap by replacing per-language regex with AST traversal. The regex
// chunker stays registered ("--chunker=regex") for users who don't want
// the +20 MB grammar bloat or the gotreesitter dep.
//
// Load-bearing invariants the rest of the codebase depends on:
//
//   - **cAST split-then-merge** (cast.go). Pass 1 walks the AST top-
//     down: for each named node whose byte span exceeds chunkSize,
//     recurse into its named children instead of emitting it as a
//     whole. Pass 2 walks the resulting span list in order, greedily
//     merging adjacent spans while their combined span ≤ chunkSize.
//     Inter-span gaps (whitespace, comments, blank lines) are absorbed
//     into whichever chunk straddles them, preserving the byte-fidelity
//     invariant from internal/chunk (concat == source). Changing either
//     pass's loop structure breaks byte-fidelity; tests cross-check it
//     per language.
//   - **gotreesitter dep is pinned at the major.minor in go.mod.** It
//     is pre-1.0 with a single maintainer (bus-factor: 1, ADR-010 risk).
//     The chunker.Chunker interface (ADR-005) is the swap-out path if
//     the dep ever needs to be replaced. Don't lift internal types
//     across the interface boundary — keep all gotreesitter-specific
//     state inside this package.
//   - **Per-language ParserPool cache.** Indexing a real repo calls
//     Chunk() once per file (thousands of files of the same language).
//     Allocating a fresh gotreesitter.Parser on every call burned tens
//     of minutes per bench repo (measured during v0.2.0 development).
//     ParserPool is a sync.Pool-backed reusable parser designed for
//     this; we cache one *ParserPool per gotreesitter language, lazily
//     created on first use, in a sync.Map so the Chunker stays safe to
//     share across goroutines.
//   - **Per-parse timeout (parseTimeoutMicros, 1s).** Some grammars
//     have pathological inputs (the gotreesitter v0.18.0 bash grammar
//     hangs indefinitely on real bash-it content). The timeout aborts
//     a runaway parse and falls through to fallback(), which routes
//     the file to the registered "line" chunker instead of emitting a
//     single whole-file chunk (whole-file fallback regressed bash NDCG
//     by 0.119 in measurement; the current behavior matches what the
//     regex chunker does for unsupported languages).
//   - **Deliberate language omissions** (languages.go). The C# grammar
//     OOMs on real-world C# (1.7+ GB RSS → SIGKILL); bash grammar is
//     too slow even with the timeout. Both are absent from
//     kenToTreeSitter and auto-fall-back to the line chunker, matching
//     the regex chunker's behavior for those languages. Revisit when
//     gotreesitter ships bounded-memory C# or a faster bash grammar.
package treesitter

import (
	"sync"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"

	"github.com/townsendmerino/ken/internal/chunk"
)

// Chunker implements chunk.Chunker over gotreesitter grammars + cAST.
// Stateless conceptually (no mutable user-visible state) but caches
// ParserPool/Language values internally so repeated calls don't repay
// the parser-allocation or grammar-lookup cost per file.
type Chunker struct {
	// pools maps a kenLanguageName (e.g. "go", "rust") to a *ParserPool
	// initialized lazily on first use. sync.Map is the right shape
	// because the read-heavy access pattern (one read per file, one
	// write per first-seen language) is what sync.Map is optimized for.
	pools sync.Map // map[string]*gotreesitter.ParserPool

	// lineFallback is the universally-registered "line" chunker, used
	// when a treesitter parse times out or fails. Cached on first use
	// (the chunk package's init() registers "line" before any
	// sub-package code runs, so the lookup is safe at any call time).
	lineOnce     sync.Once
	lineCh       chunk.Chunker
	lineLookupOK bool
}

// New returns a tree-sitter chunker. The internal caches are empty;
// they fill lazily as files in each language are encountered.
func New() *Chunker { return &Chunker{} }

func init() { chunk.Register("treesitter", New()) }

func (*Chunker) Name() string { return "treesitter" }

// SupportedLanguages returns ken's canonical language names for which
// a gotreesitter grammar exists. ChunkFile uses this set to decide
// whether to route a file here or fall back to the line chunker — so
// this is exhaustive of what the treesitter chunker can handle, not a
// curated subset.
func (*Chunker) SupportedLanguages() []string {
	out := make([]string, 0, len(kenToTreeSitter))
	for k := range kenToTreeSitter {
		out = append(out, k)
	}
	return out
}

// poolFor returns a cached ParserPool for the given ken language, or
// creates one (and caches it) on first use. Returns nil if the
// language isn't supported by the treesitter chunker or its grammar
// isn't registered in gotreesitter.
//
// The lazy-init handles two cost components per language:
//  1. grammars.DetectLanguageByName + entry.Language() — decompresses
//     the embedded grammar blob on first call (one-time, then cached
//     inside gotreesitter itself).
//  2. gotreesitter.NewParserPool — allocates the sync.Pool. Cheap.
//
// All subsequent Chunk() calls for the same language hit the cache
// and just check out a parser from the pool (atomic op, no allocation
// on the hot path).
func (c *Chunker) poolFor(kenLang string) *gotreesitter.ParserPool {
	if v, ok := c.pools.Load(kenLang); ok {
		return v.(*gotreesitter.ParserPool)
	}
	tsName, ok := kenToTreeSitter[kenLang]
	if !ok {
		return nil
	}
	entry := grammars.DetectLanguageByName(tsName)
	if entry == nil {
		return nil
	}
	lang := entry.Language()
	if lang == nil {
		return nil
	}
	// Per-parse timeout: bound the worst-case parse to bail out of a
	// pathological input rather than hang the indexer. Some grammars
	// have rare quadratic-or-worse inputs (bash-it's lib/helpers.bash
	// at v0.2.0 indexing was the discovery case — 36+ minutes on one
	// file in the bash grammar). gotreesitter's published per-parse
	// time is ~2ms; parseTimeoutSeconds gives ~5000× slack for genuinely
	// large real files, and any parse exceeding it falls through to a
	// whole-file chunk via the err return from pool.Parse.
	pool := gotreesitter.NewParserPool(lang,
		gotreesitter.WithParserPoolTimeoutMicros(parseTimeoutMicros),
	)
	// LoadOrStore handles the race where two goroutines both miss
	// the cache for the same first-seen language: the second one
	// returns the first one's pool (its own is discarded).
	actual, _ := c.pools.LoadOrStore(kenLang, pool)
	return actual.(*gotreesitter.ParserPool)
}

// parseTimeoutMicros bounds the worst-case tree-sitter parse so a
// pathological file can't hang the indexer for the whole repo. See
// poolFor for the rationale.
//
// Empirically calibrated: gotreesitter's published per-parse time is
// ~2 ms. Bash on real bash-it content (esp. lib/helpers.bash and the
// completion/* tree) hit a quadratic-or-worse parse path that the
// initial 10-second budget failed to recover from in any reasonable
// total time (17 min for bash-it alone, 989 chunks). 1 second is still
// 500× the published parse time, generous enough for legitimately large
// real files, and tight enough to cap a runaway parse at <1% of total
// indexing time per pathological file.
const parseTimeoutMicros uint64 = 1_000_000

// Chunk parses source with the appropriate grammar (via the cached
// per-language ParserPool), runs cAST, and converts the resulting
// byte spans into chunk.Chunks. Concatenating the returned Text
// fields in order reproduces source exactly (the byte-fidelity
// invariant from internal/chunk/regex).
//
// If the language is not in kenToTreeSitter, the grammar isn't
// registered, or the parser times out / fails, the chunker degrades
// to the registered "line" chunker — NOT to a single whole-file
// chunk. Whole-file fallback is catastrophic for BM25 (the entire
// file becomes one token bag and IDF/TF break), measured empirically
// during v0.2.0 development on bash-it. The line chunker preserves
// roughly the same retrieval quality as the regex chunker's fallback.
func (c *Chunker) Chunk(source []byte, language string, chunkSize int) ([]chunk.Chunk, error) {
	if len(source) == 0 {
		return nil, nil
	}
	if chunkSize <= 0 {
		chunkSize = chunk.DefaultChunkSize
	}

	pool := c.poolFor(language)
	if pool == nil {
		// Language unsupported or grammar unavailable. Degrade to
		// the line chunker rather than emit a single whole-file chunk.
		return c.fallback(source, language, chunkSize)
	}

	tree, err := pool.Parse(source)
	if err != nil {
		// Parse errors here are tree-sitter's "I gave up" — typically
		// a timeout firing on a pathological input. Fall back to the
		// line chunker so the file still produces useful BM25 docs.
		return c.fallback(source, language, chunkSize)
	}
	root := tree.RootNode()
	if root == nil {
		return c.fallback(source, language, chunkSize)
	}

	spans := cAST(root, uint32(len(source)), uint32(chunkSize))
	// Belt-and-braces: layer 1 (splitNode's end<start guard in cast.go)
	// catches degenerate single nodes from gotreesitter ERROR-recovery;
	// this layer catches sibling-ordering defects the cAST re-derive
	// pass can still produce when gotreesitter emits siblings whose
	// start bytes aren't monotonic. If anything still looks wrong, fall
	// back to the line chunker rather than panic in the slice op below.
	if !spansValid(spans, uint32(len(source))) {
		return c.fallback(source, language, chunkSize)
	}
	out := make([]chunk.Chunk, len(spans))
	for i, sp := range spans {
		text := string(source[sp.start:sp.end])
		out[i] = chunk.Chunk{
			StartLine: 1 + bytesBefore('\n', source, sp.start),
			EndLine:   1 + bytesBefore('\n', source, sp.end) - trailingNewlineCorrection(text),
			Text:      text,
		}
	}
	return out, nil
}

// fallback delegates to the registered "line" chunker. This is the
// graceful-degrade path when (a) the language isn't in kenToTreeSitter
// (e.g. csharp, deliberately omitted), (b) the grammar isn't
// registered, (c) the parse timed out, or (d) the parse returned a nil
// tree. Whole-file fallback was the original v0.2.0 behavior; it
// regressed bash NDCG by 0.118 because BM25 collapses a giant single
// document into noise, and was replaced with line chunking after the
// first bench run.
//
// If chunk.Get("line") somehow fails (only possible if the registry
// hasn't initialized — shouldn't happen since chunk's init() registers
// "line" before any sub-package code runs), we degrade further to a
// single whole-file chunk so the call still returns valid output.
func (c *Chunker) fallback(source []byte, language string, chunkSize int) ([]chunk.Chunk, error) {
	c.lineOnce.Do(func() {
		if ch, err := chunk.Get("line"); err == nil {
			c.lineCh = ch
			c.lineLookupOK = true
		}
	})
	if !c.lineLookupOK {
		return wholeFileChunk(source), nil
	}
	return c.lineCh.Chunk(source, language, chunkSize)
}

// wholeFileChunk is the last-resort fallback when even chunk.Get("line")
// fails. Preserved as a safety net but no longer the primary fallback
// path — see fallback() above.
func wholeFileChunk(source []byte) []chunk.Chunk {
	text := string(source)
	end := 1 + bytesBefore('\n', source, uint32(len(source))) - trailingNewlineCorrection(text)
	return []chunk.Chunk{{StartLine: 1, EndLine: end, Text: text}}
}

// bytesBefore counts occurrences of c in source[:end]. We use it to
// convert a byte offset into a 1-indexed line number without depending
// on an external "byte→line" library.
func bytesBefore(c byte, source []byte, end uint32) int {
	if end > uint32(len(source)) {
		end = uint32(len(source))
	}
	n := 0
	for i := uint32(0); i < end; i++ {
		if source[i] == c {
			n++
		}
	}
	return n
}

// trailingNewlineCorrection accounts for chunks that end with '\n':
// a chunk like "foo\nbar\n" spans two lines (1, 2), but counting all
// '\n' in source up to the chunk's end-byte would yield 2 newlines and
// suggest EndLine=3. Subtract 1 when the chunk's last byte is '\n' so
// the displayed range matches what semble and the regex chunker emit.
func trailingNewlineCorrection(text string) int {
	if len(text) > 0 && text[len(text)-1] == '\n' {
		return 1
	}
	return 0
}
