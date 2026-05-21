package treesitter

// kenToTreeSitter maps ken's canonical language names (from
// internal/chunk/languages.go) to the grammar names registered in
// github.com/odvcencio/gotreesitter/grammars.
//
// Cover at minimum the 5 ken's regex chunker supports (python, go,
// typescript, java, rust) plus every language in semble's NDCG benchmark
// (cpp, scala, ruby, php, swift, kotlin, c, javascript, zig, elixir,
// haskell, lua, bash, csharp). gotreesitter ships them — the incremental
// cost is zero so we map them all.
//
// Naming quirks:
//   - ken "csharp" ↔ gotreesitter "c_sharp"
//   - ken "shell" ↔ gotreesitter "bash" (bash is the only shell variant
//     in the benchmark; treating shell-as-bash gives us tree-sitter chunks
//     for the bash repos rather than falling through to the line chunker).
//   - ken "javascript" maps to the JS grammar directly so .js files get
//     proper AST chunking. (ken's extLang historically routed .js → ken
//     "typescript" because the regex chunker reuses TS rules for JS; the
//     treesitter chunker handles each grammar natively and supports both
//     ken names.)
//
// gotreesitter exposes its grammars via grammars.DetectLanguageByName, so
// these strings are the same identifiers the upstream registry uses. A
// missing entry means: language not supported by this chunker; ChunkFile
// falls back to the line chunker, same as the regex chunker behavior.
var kenToTreeSitter = map[string]string{
	"python":     "python",
	"go":         "go",
	"typescript": "typescript",
	"javascript": "javascript",
	"java":       "java",
	"rust":       "rust",
	"c":          "c",
	"cpp":        "cpp",
	"ruby":       "ruby",
	"php":        "php",
	"swift":      "swift",
	"kotlin":     "kotlin",
	"scala":      "scala",
	"haskell":    "haskell",
	"elixir":     "elixir",
	"lua":        "lua",
	"zig":        "zig",

	// Deliberately omitted:
	//   csharp ("c_sharp") — the gotreesitter v0.18.0 C# grammar's parse
	//     tables grow unboundedly on real-world C# (1.7+ GB RSS during
	//     dapper indexing → SIGKILL on all 3 csharp bench repos). Falls
	//     back to the line chunker until either the grammar improves or
	//     we add per-parse memory bounds. Tracked in DESIGN.md §10.
	//   shell ("bash") — the gotreesitter v0.18.0 bash grammar parses
	//     real bash-it content extremely slowly: at chunkSize=1500 with
	//     a 1s per-parse timeout, ~39% of bash files time out and
	//     produce no AST chunks. Measured NDCG impact at v0.2.0 was
	//     −0.119 vs the regex chunker's line-fallback baseline. Falls
	//     back to the line chunker; tracked in DESIGN.md §10.
}
