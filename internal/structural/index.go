// Package structural builds a name-based structural index over a code
// corpus, using gotreesitter as the parser. It is the shared foundation
// for ken's Stage 8 work:
//
//  1. Retrieval enrichment (Track 1): produces deterministic per-chunk
//     label prefixes — function name + calls + raises + (optionally)
//     callers / imports / signature / siblings — that get prepended to
//     chunks at index time. Lifts recall by surfacing structurally-
//     related identifiers into the indexed text.
//
//  2. Exact-answer MCP tools (Track 2): exposes `definition`,
//     `references` / `callers`, `outline`, `symbols` over the same
//     index. Fast structural navigation, ranked best-guess, no LSP
//     required.
//
// **Tree-sitter-grade, not compiler-grade.** All resolution is by
// identifier name; we do not do type inference, cross-file overload
// resolution, or scope-precise shadow analysis. For repos with
// name collisions, a single name may resolve to multiple definition
// sites — results are ranked and clearly labeled as ambiguous.
// This is the same relevance-over-completeness trade ken makes for
// retrieval; honest in both directions.
//
// Stage 8 supports twelve languages via dedicated extractors:
// Python, Go, TypeScript, JavaScript, Java, Rust, C, C++, PHP, Ruby,
// Kotlin, Dart. Adding another language is a new extract_<lang>.go
// file plus a row in the kenLangToTSLang and langExtractor maps; the
// surface stays the same. Languages whose grammar fails — C#
// (unbounded recursion in the post-parse namespace recovery pass),
// Swift (lexer misparses real-world header comments), bash
// (pathologically slow) — silently fall through to the chunker's
// line fallback. Extractors for those exist in tree under
// build-tag gates so re-enabling is a flag flip once upstream
// grammar fixes land; see DESIGN.md §10.
package structural

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"

	"github.com/townsendmerino/ken/internal/repo"
)

// FileStruct captures the structural facts a single corpus file
// contains. One per file. For the CSN bench convention each Python
// file IS one chunk; for multi-chunk-per-file repos a future
// revision will split FileStruct per-chunk.
type FileStruct struct {
	// Path is the file path relative to the corpus root. The corpus
	// root prefix is the caller's responsibility — Build() always
	// stores paths as the relative form it walked.
	Path string

	// Functions: every function or method defined in this file, in
	// AST order. Includes both top-level defs and methods nested
	// inside class bodies.
	Functions []FuncDef

	// Classes: every class defined in this file. Each Class.Methods
	// duplicates the corresponding FuncDef entries from Functions
	// (with the same Name); the duplication keeps "list a class's
	// members" a single lookup rather than requiring a re-scan.
	Classes []ClassDef

	// Imports: every imported module / symbol referenced from this
	// file's import statements. Both `import foo` and `from foo
	// import X` contribute; the value is the leaf name as it appears
	// in the local namespace (X in the latter case, foo in the
	// former).
	Imports []string

	// CallRefs is every call site invoked anywhere in this file, in
	// AST order, NOT deduped. Each record carries the callee leaf
	// name, the receiver expression (empty for bare calls), the
	// 1-based line the call sits on, and the enclosing function /
	// method qualified name (empty if at file top level).
	//
	// This is the Phase 0 substrate (see docs/structural-call-graph-plan.md):
	// later phases will resolve Callee against the corpus-wide
	// `defs` / `methods` maps to produce function-level edges. For
	// the existing Arm B enrichment (ADR-035) and the pass-2
	// callers-by-file index, use the CalleeNames() accessor — it
	// returns the same deduped leaf-name list FileStruct.Calls used
	// to expose, byte-identical to the pre-Phase-0 behaviour.
	CallRefs []CallRef

	// Raises: every exception type the file's `raise X(...)`
	// statements name. Dedup'd; order matches first occurrence.
	Raises []string
}

// CalleeNames returns the deduped list of callee leaf names invoked
// anywhere in this file, in first-appearance order. This is the
// drop-in replacement for the pre-Phase-0 `FileStruct.Calls []string`
// field: Arm B enrichment and the pass-2 callers-by-file index both
// consume this surface and must keep producing identical output.
func (fs *FileStruct) CalleeNames() []string {
	if fs == nil || len(fs.CallRefs) == 0 {
		return nil
	}
	out := make([]string, 0, len(fs.CallRefs))
	seen := make(map[string]struct{}, len(fs.CallRefs))
	for _, r := range fs.CallRefs {
		if r.Callee == "" {
			continue
		}
		if _, dup := seen[r.Callee]; dup {
			continue
		}
		seen[r.Callee] = struct{}{}
		out = append(out, r.Callee)
	}
	return out
}

// FuncDef is one function or method definition.
type FuncDef struct {
	Name           string
	Params         []string // parameter names, in declaration order
	ReturnType     string   // text of the return-type annotation, "" if none
	IsMethod       bool     // true if defined inside a class body
	EnclosingClass string   // name of the enclosing class, "" if top-level

	// StartLine, EndLine are 1-based line numbers spanning the
	// definition (inclusive). Zero values for either field mean
	// "not recorded" — the extractor that produced this FuncDef
	// didn't capture spans. Captured by every extractor as of
	// Phase 0 (docs/structural-call-graph-plan.md).
	StartLine int
	EndLine   int
}

// ClassDef is one class definition.
type ClassDef struct {
	Name    string
	Methods []FuncDef

	// StartLine, EndLine are 1-based line numbers spanning the
	// class definition (inclusive). Same semantics as FuncDef:
	// zero values mean "not recorded." Captured as of Phase 0.
	StartLine int
	EndLine   int
}

// CallRef is one call site recorded by the extractor. Phase 0
// substrate: per-call-site facts that later phases will resolve into
// function-level call-graph edges.
//
// Callee is the leaf name of the call target (`obj.bar(...)` records
// "bar", not "obj.bar" — that matches the BM25 tokenizer's split and
// the pre-Phase-0 file-scoped behaviour). Receiver is the text of
// the receiver expression ("obj" in the example above), empty for
// bare calls; retained because Phase 3 type resolution will use it.
// Line is the 1-based line number the call sits on. EnclosingSymbol
// is the qualified name of the function or method this call lives
// inside (e.g. "SessionManager.Login" or "verifyToken"); empty for
// file-top-level calls (rare; exists for some scripting-language
// files).
type CallRef struct {
	Callee          string
	Receiver        string
	Line            int
	EnclosingSymbol string
}

// CallSite represents one file calling a particular name. Used by the
// reverse call-graph index.
type CallSite struct {
	File string
}

// Index is the corpus-wide structural index. Goroutine-safe for reads
// after Build returns; mutation only happens during Build.
type Index struct {
	files map[string]*FileStruct // path → struct

	// Reverse call graph: callee name → files that contain a call
	// to that name. Sorted by file path for stable iteration.
	callers map[string][]CallSite

	// Top-level definition lookup: name → files where the name is
	// defined as a top-level function or class. Sorted by file
	// path. Symbols() and SymbolsInPath() iterate this map ONLY —
	// methods are tracked separately so they don't pollute the
	// symbol-list output.
	defs map[string][]string

	// Method definition lookup: name → files where the name is
	// defined as a method. Keyed by BOTH the bare method name
	// ("Login") AND the qualified Type.method form ("User.Login").
	// The qualified key disambiguates when the same bare method
	// name lives on multiple types in the same file.
	//
	// Stage 8 v0+: methods are queryable by either form. Bare
	// lookup returns every class's method with that name across
	// the corpus; qualified lookup pins down a single type.
	methods map[string][]string
}

// EnrichOptions toggles which structural facts each variant arm
// surfaces in the per-chunk label. Arm B (the M0d winner) maps to
// EnrichOptions{} — all-false except for the always-on baseline of
// func/calls/raises. Each additional arm in Track 1 sets exactly one
// of the booleans below.
//
// The combined-survivors arm sets multiple booleans true after Track 1
// determines which additions earned their place.
type EnrichOptions struct {
	// Callers prepends "called by: A, B, C" (the reverse call graph
	// — files that call this file's defining functions). Brings
	// caller-vocabulary into the doc.
	Callers bool

	// Imports prepends "imports: foo, bar". Brings dependency
	// vocabulary into the doc — useful when the query mentions a
	// library or downstream concept the function uses.
	Imports bool

	// Signature prepends "params: a, b, c | returns: T". Surfaces
	// the interface vocabulary (parameter names, return type) that
	// callers will reference by name.
	Signature bool

	// Siblings prepends "siblings: method_a, method_b" — other
	// members of the same enclosing class. Class-cohesion
	// vocabulary; only fires for methods (top-level functions
	// have no siblings).
	Siblings bool
}

// kenLangToTSLang maps file extensions to gotreesitter grammar names.
// Extending to other languages is a matter of adding extract_<lang>.go
// and adding rows here.
var kenLangToTSLang = map[string]string{
	".py":   "python",
	".go":   "go",
	".ts":   "typescript",
	".tsx":  "typescript",
	".js":   "javascript",
	".jsx":  "javascript",
	".mjs":  "javascript",
	".cjs":  "javascript",
	".java": "java",
	".rs":   "rust",
	".cpp":  "cpp",
	".cc":   "cpp",
	".cxx":  "cpp",
	".hpp":  "cpp",
	".hh":   "cpp",
	".hxx":  "cpp",
	".c":    "c",
	".h":    "c",
	".php":  "php",
	".rb":   "ruby",
	".kt":   "kotlin",
	".kts":  "kotlin",
	".dart": "dart",
	".cs":   "c_sharp",
	// .swift intentionally OMITTED — the gotreesitter v0.20.2
	// tree-sitter-swift grammar still misparses real-world Swift
	// (header-comment fix in v0.20.2 lifted Alamofire 0%→35% clean,
	// but ~65% of files still fail and ~20% take 2–6s per parse —
	// not production-viable). The extractor at extract_swift.go is
	// parked behind the `swift` build tag; re-enable once the grammar
	// improves further. See DESIGN.md §10.
	// .cs (C#) un-parked 2026-06-06: gotreesitter v0.20.2 bounded the
	// namespace-recovery sub-parses that OOM'd on v0.20.0-rc3 (Dapper
	// retest: 89% clean, ~3s/156 files, no SIGKILL). extractCsharp is
	// registered below; see DESIGN.md §10.
}

// langExtractor maps a gotreesitter grammar name to its AST-walking
// extractor. Adding a new language is a new function that fills
// FileStruct and a new entry here.
//
// The extractor receives the source bytes, the root *gotreesitter.Node,
// the *gotreesitter.Language handle (needed for every Type() and
// ChildByFieldName() call — gotreesitter requires the language to
// resolve node type names + field-name indices), and the FileStruct
// to populate.
var langExtractor = map[string]func([]byte, *gotreesitter.Node, *gotreesitter.Language, *FileStruct){
	"python":     extractPython,
	"go":         extractGo,
	"typescript": extractTypeScript,
	"javascript": extractTypeScript,
	"java":       extractJava,
	"rust":       extractRust,
	"cpp":        extractCpp,
	"c":          extractCpp,
	"php":        extractPhp,
	"ruby":       extractRuby,
	"kotlin":     extractKotlin,
	"dart":       extractDart,
	"c_sharp":    extractCsharp,
	// "swift": extractSwift — registered but parked; see the
	// kenLangToTSLang block above. Uncomment when the grammar
	// is fixed upstream.
}

// langCache holds the pool + language handle per grammar. Both are
// needed at extraction time: pool.Parse(...) returns the tree, and
// nodes' Type()/ChildByFieldName() calls all take *Language. Cached
// together so the extractor doesn't re-resolve the grammar.
type langCache struct {
	pool *gotreesitter.ParserPool
	lang *gotreesitter.Language
}

// parserPools caches one langCache per grammar across the process.
// sync.Map for the read-heavy access pattern (same shape as the
// treesitter chunker — allocating a fresh parser per file is
// measurably expensive on real corpora).
var (
	parserPools sync.Map // map[string]*langCache
)

// parseTimeoutMicros caps any single file's parse time. 0 disables.
//
// Originally set to 1s as a defensive bound against pathological
// grammars (C# / some bash) that could hang forever in a single parse.
// That budget broke the Arm B enrichment label's determinism contract:
// under contention (CI -race + low GOMAXPROCS) a healthy parse could
// brush the 1s budget, gotreesitter would *silently* return the
// partial tree (pool.Parse returns (tree, nil) regardless of timeout —
// stop reason is on the Tree), and the extractor would walk it as if
// complete, producing a label missing a function or call. Two runs of
// the same corpus then produce different bytes, and
// TestBuildDeterminism_CrossRun/contention-bm25 flakes.
//
// Disabled because (a) the cited pathological grammars (C# / Swift)
// are parked and not in the dispatch table, (b) the supported set
// (Python, Go, TS/JS, Java, Rust, C/C++, PHP, Ruby, Kotlin, Dart) has
// no known hang-forever inputs at file scale, and (c) ExtractFile is
// per-file work — one slow parse blocks one worker, not the whole
// build. If a real hang ever surfaces, prefer adding a watchdog-style
// cancellation flag (deterministic skip on cancel) over a time budget.
//
// Defense in depth: ExtractFile also checks tree.ParseStopReason() and
// returns nil for any non-accepted parse — so even if a future tweak
// re-enables a timeout, the silent partial-tree path is closed.
const parseTimeoutMicros uint64 = 0

// langCacheFor returns the cached pool + language handle for a
// grammar, or nil if the grammar isn't registered. Lazy-initialized.
// Same pattern as aikit/chunk/treesitter/chunker.go's poolFor, but
// also caches the *Language handle that all node-API methods need.
func langCacheFor(grammarName string) *langCache {
	if v, ok := parserPools.Load(grammarName); ok {
		return v.(*langCache)
	}
	entry := grammars.DetectLanguageByName(grammarName)
	if entry == nil {
		return nil
	}
	lang := entry.Language()
	if lang == nil {
		return nil
	}
	c := &langCache{
		pool: gotreesitter.NewParserPool(lang,
			gotreesitter.WithParserPoolTimeoutMicros(parseTimeoutMicros),
		),
		lang: lang,
	}
	actual, _ := parserPools.LoadOrStore(grammarName, c)
	return actual.(*langCache)
}

// Build walks corpusDir, parses every supported source file, and
// returns the populated index. Errors propagate up only for the
// directory walk itself; per-file parse failures are silently
// recorded as missing entries (same graceful-degrade as the
// treesitter chunker).
//
// Walk semantics: uses internal/repo.WalkFS, the SAME gitignore-
// respecting walker the regex+treesitter chunkers use. This is a
// real change from the v0 prototype that called filepath.WalkDir
// directly — that walked every file including build artifacts,
// node_modules, and (the discovery case) ken's own gitignored
// testdata/bench/* corpora of 400k+ tiny .py files. Honoring
// gitignore aligns the structural index's notion of "this repo"
// with what the chunker already considers it to be.
func Build(corpusDir string) (*Index, error) {
	ix := &Index{
		files:   make(map[string]*FileStruct),
		callers: make(map[string][]CallSite),
		defs:    make(map[string][]string),
		methods: make(map[string][]string),
	}

	// Gitignore-aware walk via the same internal/repo.WalkFS path
	// the chunker uses. WalkFS returns repo-relative slash paths
	// in deterministic lexical order; binary files and oversized
	// files are already skipped at the walker layer.
	relPaths, err := repo.WalkFS(os.DirFS(corpusDir), repo.Options{})
	if err != nil {
		return nil, fmt.Errorf("structural.Build: walk %s: %w", corpusDir, err)
	}

	// Pass 1: parse + extract per-file structure. Parallelized in
	// M4 (docs/perf-campaign-startup-query.md) — single-threaded
	// build was the dominant cold-start cost on large multi-language
	// corpora (M0: 1577 ms for 167 Ruby files on jekyll). Per-file
	// work is independent: each goroutine reads its file, borrows
	// from gotreesitter's per-grammar parser pool (thread-safe by
	// design), and extracts into a fresh FileStruct. Results are
	// written into an indexed slice and merged into ix.files in
	// lexical order after the fan-in, preserving the deterministic
	// iteration order that pass 2's lookup maps rely on.
	type fileResult struct {
		rel string
		fs  *FileStruct
	}
	results := make([]*fileResult, len(relPaths))
	type job struct {
		idx int
		rel string
	}
	numWorkers := runtime.NumCPU()
	jobs := make(chan job, numWorkers*2)
	var wg sync.WaitGroup
	for range numWorkers {
		wg.Go(func() {
			for j := range jobs {
				ext := filepath.Ext(j.rel)
				gram, ok := kenLangToTSLang[ext]
				if !ok {
					continue
				}
				lc := langCacheFor(gram)
				if lc == nil {
					continue
				}
				abs := filepath.Join(corpusDir, j.rel)
				src, err := os.ReadFile(abs)
				if err != nil {
					continue
				}
				tree, err := lc.pool.Parse(src)
				if err != nil || tree == nil {
					continue
				}
				root := tree.RootNode()
				if root == nil {
					continue
				}
				fs := &FileStruct{Path: j.rel}
				langExtractor[gram](src, root, lc.lang, fs)
				results[j.idx] = &fileResult{rel: j.rel, fs: fs}
			}
		})
	}
	for i, rel := range relPaths {
		jobs <- job{idx: i, rel: rel}
	}
	close(jobs)
	wg.Wait()
	// Merge results in lexical order (results[i] aligned with
	// relPaths[i] which itself came from repo.WalkFS sorted output).
	// Deterministic merge ⇒ deterministic pass 2 iteration ⇒ the
	// methods/defs maps' append ordering is reproducible across runs.
	for _, r := range results {
		if r == nil {
			continue
		}
		ix.files[r.rel] = r.fs
	}

	// Pass 2: build the lookup maps.
	//
	//   defs[name]      → files where `name` is a top-level def
	//                     (function or class). Used by Symbols(),
	//                     SymbolsInPath(), and the unqualified
	//                     Definition() path for top-level matches.
	//   methods[name]   → files where `name` is a method. Indexed
	//                     under BOTH the bare method name AND the
	//                     qualified Type.method form. Methods are
	//                     queryable by either; the qualified form
	//                     disambiguates when the bare name lives
	//                     on multiple types.
	//   callers[name]   → files that invoke `name` (call graph
	//                     reverse). Bare callee names; selector
	//                     expressions like `obj.foo()` contribute
	//                     under "foo".
	//
	// Stage 8 v0+ (this Build): methods land in the methods map so
	// the Track 2 `definition` tool can resolve method lookups.
	// Same file may appear under both bare and qualified keys —
	// the Definition() reader dedupes across the two when merging.
	for path, fs := range ix.files {
		for _, fn := range fs.Functions {
			if fn.IsMethod {
				ix.methods[fn.Name] = append(ix.methods[fn.Name], path)
				if fn.EnclosingClass != "" {
					qual := fn.EnclosingClass + "." + fn.Name
					ix.methods[qual] = append(ix.methods[qual], path)
				}
			} else {
				ix.defs[fn.Name] = append(ix.defs[fn.Name], path)
			}
		}
		for _, cls := range fs.Classes {
			ix.defs[cls.Name] = append(ix.defs[cls.Name], path)
		}
		for _, callee := range fs.CalleeNames() {
			ix.callers[callee] = append(ix.callers[callee], CallSite{File: path})
		}
	}

	// Sort + dedupe the slices for stable iteration and to
	// collapse same-file double-indexing (e.g. a method indexed
	// under both bare and qualified keys lands one file once per
	// key by construction; defs/methods cross-collision would
	// dedupe here).
	for k := range ix.defs {
		ix.defs[k] = uniqStringsSorted(ix.defs[k])
	}
	for k := range ix.methods {
		ix.methods[k] = uniqStringsSorted(ix.methods[k])
	}
	for k := range ix.callers {
		sort.Slice(ix.callers[k], func(i, j int) bool {
			return ix.callers[k][i].File < ix.callers[k][j].File
		})
	}

	return ix, nil
}

// uniqStringsSorted sorts the slice and removes consecutive
// duplicates in place. Returns a slice of length ≤ len(in).
func uniqStringsSorted(in []string) []string {
	if len(in) <= 1 {
		return in
	}
	sort.Strings(in)
	out := in[:1]
	for i := 1; i < len(in); i++ {
		if in[i] != in[i-1] {
			out = append(out, in[i])
		}
	}
	return out
}

// Files returns the per-file structures the index contains, keyed by
// path. Read-only.
func (ix *Index) Files() map[string]*FileStruct { return ix.files }

// File returns the per-file struct for path, or nil if the index has
// no entry for it (unsupported language, parse failure, etc.).
func (ix *Index) File(path string) *FileStruct { return ix.files[path] }

// Callers returns the files that call the given name, sorted by path.
// Used by the Track 1 "callers" enrichment and the Track 2
// `references` tool.
func (ix *Index) Callers(name string) []CallSite { return ix.callers[name] }

// Defs returns the files that define the given top-level name, sorted
// by path. Used by the Track 2 `definition` tool.
func (ix *Index) Defs(name string) []string { return ix.defs[name] }

// Stats reports per-language fall-through counts and total files
// indexed, for the bench memo's cost ledger.
type Stats struct {
	TotalFiles    int
	IndexedFiles  int
	UniqueSymbols int // distinct names with at least one definition
	UniqueCallees int // distinct names called by at least one file
}

func (ix *Index) Stats() Stats {
	return Stats{
		IndexedFiles:  len(ix.files),
		UniqueSymbols: len(ix.defs),
		UniqueCallees: len(ix.callers),
	}
}

// dedupAppend is a small helper used by the Python extractor and the
// Enrich path to keep slice order stable while filtering duplicates.
func dedupAppend(dst []string, s string) []string {
	if slices.Contains(dst, s) {
		return dst
	}
	return append(dst, s)
}

// trimAndJoin truncates a comma-joined identifier list to at most n
// entries and returns the joined string. Used by Enrich to keep label
// lines bounded even on chunks with hundreds of callees.
func trimAndJoin(items []string, n int) string {
	if len(items) > n {
		items = items[:n]
	}
	return strings.Join(items, ", ")
}

// nodeStartLine returns the 1-based line number a node starts on, or 0
// if the node is nil. gotreesitter's StartPoint().Row is 0-based; the
// +1 produces the conventional human-readable line number.
func nodeStartLine(n *gotreesitter.Node) int {
	if n == nil {
		return 0
	}
	return int(n.StartPoint().Row) + 1
}

// nodeEndLine returns the 1-based line number a node ends on, or 0 if
// the node is nil.
func nodeEndLine(n *gotreesitter.Node) int {
	if n == nil {
		return 0
	}
	return int(n.EndPoint().Row) + 1
}

// fillSpan sets StartLine/EndLine on a FuncDef from a tree-sitter node.
// Zero-line FuncDef fields mean "the extractor that produced this didn't
// capture a span" — every shipping extractor sets both as of Phase 0.
func (fn *FuncDef) fillSpan(n *gotreesitter.Node) {
	fn.StartLine = nodeStartLine(n)
	fn.EndLine = nodeEndLine(n)
}

// fillSpan sets StartLine/EndLine on a ClassDef from a tree-sitter node.
func (cls *ClassDef) fillSpan(n *gotreesitter.Node) {
	cls.StartLine = nodeStartLine(n)
	cls.EndLine = nodeEndLine(n)
}

// appendCall records one call site. callee is the leaf name (empty
// callees are dropped — they happen on unresolvable call shapes like
// `(f or g)()` or `arr[i]()`). receiver is the text of the receiver
// expression in `obj.bar()` ("obj"), empty for bare calls. callNode
// is the call expression itself, used to compute the 1-based line.
// enclosingSymbol is the qualified name of the enclosing function /
// method ("Type.method" if a method, "func" if top-level), empty at
// file scope.
func (fs *FileStruct) appendCall(callee, receiver string, callNode *gotreesitter.Node, enclosingSymbol string) {
	if callee == "" {
		return
	}
	fs.CallRefs = append(fs.CallRefs, CallRef{
		Callee:          callee,
		Receiver:        receiver,
		Line:            nodeStartLine(callNode),
		EnclosingSymbol: enclosingSymbol,
	})
}

// qualifySymbol composes "Type.method" if enclosingClass is non-empty,
// "method" otherwise. Used by every extractor to compute the
// enclosingSymbol string threaded into its walk function so call sites
// can record which function they live inside.
func qualifySymbol(enclosingClass, name string) string {
	if enclosingClass != "" {
		return enclosingClass + "." + name
	}
	return name
}
