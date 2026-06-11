# Changelog

All notable changes to ken are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
As of **1.0**, ken's public API surface (`mcp.Run`, `mcp.NewServer`,
`mcp.NewCache`, `FormatResults`, the `chunk.Chunker` interface, and the
typed JSON-output structs) is stable: breaking changes to it require a
major (2.x) bump; new features land in minor (1.x) releases and fixes in
patch (1.0.x) releases. Best-effort surfaces (noted per-symbol in
[DEVELOPERS.md](docs/DEVELOPERS.md#public-api-surface)) may still evolve
within 1.x. Each release tag has a corresponding GitHub release page with
pre-built binaries.

## [Unreleased]

### Added

- **`recently_changed` now supports `output: "json"`** — the one tool that
  was markdown-only. Returns a typed `RecentlyChangedResponse` (considered
  count + per-commit hash / short_hash / subject / author / RFC3339 when /
  changed_files), built from the same rows the markdown render uses so the
  two can't drift. All nine MCP tools now accept `output: "json"`.

### Changed

- **Bumped `aikit` v1.4.0 → v1.5.0 and made the int8 reranker the default.**
  aikit v1.5.0 fixes the q8 reranker (pooled scratch + dequant-once-then-SIMD
  matmul — the pre-fix path re-widened int8→f32 inside the GEMM and allocated
  4.4 GiB of scratch). On ken's rerank path int8 now reaches **f32 latency
  parity** (50-doc cold: 7.35 s vs 7.75 s, arm64) at **~21× less runtime
  memory** (18 MiB vs 379 MiB) and ¼ the weight storage (~140 MB resident vs
  ~547 MB), with cosine 0.997 vs f32 unchanged. `KEN_MCP_RERANK_QUANT` and the
  `--rerank-quant` CLI flag now default to `int8`; pass `f32` for the
  full-precision path. (Reverses the 1.0.1-era "int8 slower on Apple Silicon"
  note — that was the pre-fix aikit q8 path.) No re-download needed: `LoadQ8`
  quantizes the existing CodeRankEmbed snapshot in-process.

## [1.0.1] — 2026-06-11 — faster hybrid search

A patch release: same retrieval quality, **~3× faster hybrid search** from an
`aikit` bump, plus deserializer fuzzing, a model-download fix, and a docs
accuracy sweep. No API changes — `mcp.Run` / `mcp.NewServer` / `chunk.Chunker`
and the wire format are unchanged from 1.0.0.

### Changed

- **Bumped `aikit` v1.0.0 → v1.4.0 — ~3× faster hybrid search, no quality
  change.** Non-breaking for ken (no surface ken imports changed; the only
  breaking change in the range — `linalg.Workspace` — is transitive-only).
  The `ann.Flat` cosine retriever moved from a scalar-f64 loop to a SIMD-f32
  dot kernel + 8-vectors-per-pass streaming: **`Flat.Query` 11.7× faster** in
  isolation, and end-to-end **hybrid `search` p50 −66 % (4.58 ms → 1.56 ms** on
  a ~13 k-chunk corpus; the scan is O(N), so the win grows with corpus size).
  (The `encoder` reranker also gained vectorized `scores·V`/`forward_q8`, but
  that targets the int8 path — ken's default f32 reranker is unchanged: a
  50-doc cold rerank measured 7.40 s → 7.53 s, p=0.31.) **recall@10
  re-verified identical** (NL 0.969 /
  symbol 0.995) and embedding parity preserved (golden cosine ≥ 1 − 1e-5 vs the
  Python reference). Plus robustness hardening on exactly the paths ken runs: a
  safetensors mmap-lifetime guardrail, two fuzz-fixed untrusted-tensor crashes
  in `embed`, and `bm25`/`chunk` indexing-pipeline fuzzing.
  `chunk/treesitter` stays at its own v1.0.0.

### Added

- **Fuzz coverage for the KEN1 + KNRC binary deserializers**
  (`internal/search`). `FuzzDeserializeIndex` (the untrusted-input parser —
  ken-mcp auto-loads `<repo>/.ken/index.bin` from shallow-cloned remote repos)
  and `FuzzDecodeRerankCache`, validating the hand-written adversarial-input
  defenses. 2.6M executions, zero crashers.

### Fixed

- **`ken download-model` now replaces stub/pointer files instead of skipping
  them.** The "already present" check was existence-only, so a leftover
  Git-LFS / HF-hub pointer stub (~130 B), a broken symlink, an empty file, or a
  truncated run was reported as present — silently leaving a model that fails
  to load. A per-file size floor now re-downloads any present-but-implausibly-
  small artifact (real files clear the floor by orders of magnitude; no real
  file is ever rejected).

## [1.0.0] — 2026-06-06 — ken 1.0

**ken 1.0.** The public API surface — `mcp.Run`, `mcp.NewServer`,
`mcp.NewCache`, `FormatResults`, the `chunk.Chunker` interface, and the
typed JSON-output structs — is frozen and 1.0-stable. The retrieval axis
is at its measured ceiling (default hybrid **~0.97 recall@10** — 0.967 NL
/ 0.995 symbol at ~46× fewer agent tokens than grep+Read); 13 languages;
structural navigation; database-schema indexing; first-run model
auto-fetch; and macOS / Linux / Windows binaries via direct download,
Homebrew, and Scoop.

### Changed

- **Pinned aikit at its 1.0**: `aikit v0.4.1 → v1.0.0` and
  `aikit/chunk/treesitter v0.4.1 → v1.0.0`, so the algorithm-package
  stability tiers compose with ken's own. The ken-imported surface is
  byte-identical — validated: full `go test ./...`, `build_parity`, and
  the `grammar_subset` drift guard all green; encoder cosine parity stays
  1.000000 on aikit's side — so retrieval output is unchanged.

Everything from v0.10.0 (C#, model auto-fetch, Windows binaries) and
v0.10.1 (Homebrew + Scoop) is part of the 1.0 release.

## [0.10.1] — 2026-06-06 — Homebrew cask + Scoop manifest

### Added

- **`brew install --cask townsendmerino/tap/ken` / `scoop install ken`** —
  GoReleaser now publishes a Homebrew cask (macOS/Linux) + Scoop manifest
  (Windows) to `townsendmerino/homebrew-tap` and
  `townsendmerino/scoop-bucket` on each release; both install `ken` +
  `ken-mcp`. (Also: a `windows-latest` CI smoke job — build · slim · run ·
  core tests — guards the Windows surface at PR time.)

## [0.10.0] — 2026-06-06 — C# + auto-fetch onboarding + Windows binaries

### Added — Windows binaries (amd64 + arm64)

- **Release binaries now ship for Windows** (`.zip`) alongside macOS/Linux.
  The code already cross-compiled (`sighup_windows.go` no-ops the Unix-only
  SIGHUP path; the aikit v0.4.1 Windows build fix landed earlier) — this
  enables `windows/amd64`+`arm64` in `.goreleaser.yml` with `.zip`
  archives. Re-opens the previously deferred Windows item. Still open: a
  scoop manifest (needs a bucket repo) + a Windows CI smoke job.

### Added — ken-mcp auto-fetches the embedding model on first run (onboarding)

- **`ken-mcp` now fetches `potion-code-16M` (~60 MB) in the background on
  first run** when a model-needing mode is requested and no model is
  present (`KEN_MCP_AUTO_FETCH`, default on). It serves bm25 immediately,
  downloads the model, then purges the per-repo cache so the next query
  rebuilds with embeddings and search upgrades to hybrid automatically
  (the handler reads `ix.Mode()` per query). This is the doc face of the
  recall decomposition: a fresh install now lands on the ~0.97 hybrid path
  instead of silently sitting on the ~0.84 BM25-only fallback. Also:
  `KEN_MCP_MODEL_DIR` defaults to `~/.ken/model` when unset, so a user who
  ran `ken download-model` gets hybrid without setting the env. Progress
  stays on stderr (the JSON-RPC contract is unchanged); the DB-Tier-2 case
  logs a restart prompt rather than a live swap; `KEN_MCP_AUTO_FETCH=0`
  reverts to downgrade-and-warn. New `Cache.Purge()` (evict-all, keep-open).
  See [ADR-037](docs/internal/DECISIONS.md#adr-037-ken-mcp-auto-fetches-the-embedding-model-on-first-run-background-default-on).

### Added — C# language support (13th language)

- **C# (`.cs`) un-parked and shipped** now that gotreesitter **v0.20.2**
  bounded the C# namespace-recovery sub-parses (#98/#106) whose unbounded
  recursion previously OOM'd ken's indexer (1.7+ GB RSS → SIGKILL on real
  C#; v0.20.0-rc3 retest hit 93+ GB). Re-verified: Dapper's 156 `.cs`
  files parse in ~3s at 89% clean root with no OOM; the former minimal
  OOM reproducer now parses in ~5ms. Coordinated the same three wirepoints
  as the Dart add: aikit `chunk/treesitter` `KenToTreeSitter`
  (`csharp → c_sharp`, bumped to `v0.4.1`), the `.goreleaser.yml`
  `grammar_subset_c_sharp` slim-release tag (drift-guard green), and the
  structural extractor (`extract_csharp.go`, build tag dropped; `.cs →
  c_sharp` in `kenLangToTSLang`, `c_sharp → extractCsharp` in
  `langExtractor`). New regression test exercises the former OOM trigger.
  gotreesitter bumped `v0.20.1 → v0.20.2` (also carries Go/JS/TS/Python
  grammar fixes #100–#103). **Swift stays parked** — v0.20.2's
  license-header fix lifted Alamofire 0%→35% clean but ~65% of real Swift
  still fails and ~20% takes 2–6s/parse.

### Added — Phase 0 of the structural call-graph plan (substrate for function-level edges)

- **`FuncDef.StartLine` / `FuncDef.EndLine` + `ClassDef.StartLine` /
  `ClassDef.EndLine`** — 1-based line spans on every function / method /
  class / interface / mixin / record / enum across all 10 shipping
  languages. Zero values mean "not recorded" (no extractor produces
  them as of this commit). Free at parse time — `nodeText` was already
  reading the bytes; we just record the spans. The immediate
  user-visible effect lives in [`docs/internal/structural-call-graph-plan.md`](docs/internal/structural-call-graph-plan.md)'s
  Phase 0 entry: future `references` / `definition` output gains line
  numbers without further extractor work.
- **`structural.CallRef` + `FileStruct.CallRefs`** — replaces the
  pre-Phase-0 file-scoped `FileStruct.Calls []string` with per-call-site
  records carrying `Callee` (leaf name), `Receiver` (the `obj` text from
  `obj.bar()`; empty for bare calls; retained for Phase 3 type
  resolution), `Line` (1-based source line), and `EnclosingSymbol` (the
  qualified `Type.method` or `func` the call lives inside; empty at file
  top level). NOT deduped — the Phase 0 substrate captures every call
  site in AST order. This is the seam Phase 1+ resolution writes
  against; nothing in v0.9.x consumes it as a typed structure yet.
- **`(*FileStruct).CalleeNames()` accessor** — returns the
  deduped first-appearance-order leaf-name list the pre-Phase-0
  `FileStruct.Calls []string` field used to expose. Arm B enrichment
  (ADR-035) and the corpus-wide `callers` map both call through this
  accessor; `TestEnrich_ArmBBaseline_FormatStability` confirms the
  enrichment output is byte-identical to v0.9.1. This is a hard
  invariant: Phase 0 is a strict *superset* of pre-Phase-0 facts, not
  a reshape of the enrichment input.
- **`enclosingSymbol` threading through every `walk*`** —
  `extract_python.go` / `extract_go.go` / etc. now compute
  `qualifySymbol(enclosingClass, fn.Name)` on each function/method
  entry and thread it through nested recursion so child `CallRef`
  records attribute correctly. Centralized in
  `internal/structural/index.go` as `qualifySymbol` + the `fillSpan`
  receiver methods + the `nodeStartLine` / `nodeEndLine` /
  `appendCall` helpers, so the 10 extractors stay symmetric.
- **Memory budget gate cleared.** Measured on three real corpora:
  jekyll (167 Ruby files, 9350 CallRefs, +29 MiB HeapAlloc), express
  (141 JS files, 10855 CallRefs, +25 MiB), ripgrep (101 Rust files,
  4484 CallRefs, +309 MiB — dominated by gotreesitter parser arenas,
  not Phase 0 data). The CallRef substrate itself adds ~500 KiB on
  each corpus — well inside the plan's ≤2× envelope.
- **Plan doc updated** with Phase 0 SHIPPED status and the Plan-agent
  independent review feedback: Phases 1+4 should ship BUNDLED behind
  one `KEN_STRUCTURAL_GRAPH=on` flag (so the headline `impact` tool
  rides the same flag as function-level `callers`); validation-
  harness scope is now called out as a separately-budgeted
  deliverable on the order of Stage 8 Gate 2 itself; silent
  wrong-answer risk from missed watch-mode invalidation moved to the
  top of the risk register. Trigger to start Phases 1+4: MCP log
  evidence that the agent's current 2-step `callers` → `outline` →
  re-query pattern is in practice 3+ steps in a measurable fraction
  of invocations.

This is a substrate-only ship: no API additions to the MCP tool
surface, no new MCP tools, no behavior change for `callers` /
`references` / `outline` / `symbols` callers (they still return
file-level shapes). The new `CallRef` field is public on `FileStruct`
but unused by any consumer; same for the new `StartLine` / `EndLine`
fields on `FuncDef` / `ClassDef`. Phase 1's tool upgrades will
consume them.

## [0.9.1] — 2026-06-03 — language coverage + upstream-bug diagnostics

Two new structural-extractor languages ship (Kotlin · Dart, taking total coverage to twelve); two languages that were in scope (C# · Swift) get parked behind diagnostic memos ready to file upstream at `gotreesitter`. A new `docs/internal/add-a-language.md` walkthrough captures the end-to-end process for future contributors. Same Stage 8 architecture as v0.9.0 — no API changes, no behavior changes for existing languages.

### Added — Kotlin structural extractor

- **`extract_kotlin.go`** lights up the structural index on `.kt` and `.kts` files. Tree-sitter-kotlin doesn't expose useful field names (`FieldNameForChild` returns `""` on most nodes), so the walker uses positional + `Type()` access — the same fallback pattern documented in [`docs/internal/add-a-language.md`](docs/internal/add-a-language.md) and used by `extract_rust.go`. The grammar lumps `class` / `interface` / `data class` under one `class_declaration` node; `object_declaration` handles singletons. `jump_expression` covers BOTH `return` and `throw`, discriminated via the node's source-text prefix.
- **Dogfood validation against [square/okhttp](https://github.com/square/okhttp):** 537 files indexed in ~1.0 s; 5,017 functions (4,816 methods) + 716 classes. Top calls are real okhttp-builder vocabulary (`build` / `Builder` / `newCall` / `execute` / `url` / `assertThat`); imports correctly bound to rightmost-segment names (`IOException` / `OkHttpClient` / `Request`); raises are real exception types — no noise leakage.

### Added — Dart structural extractor

- **`extract_dart.go`** lights up the structural index on `.dart` files. Notable shape difference from every other language ken covers: there is **no `call_expression` node**. Calls are flat sibling sequences at the parent level — `identifier + selector + selector(argument_part)` — so the extractor's `detectDartCalls` walks each container's children left-to-right, tracks the most-recent identifier candidate, and records a call when an `argument_part` selector follows. Handles bare and dotted calls uniformly. `mixin_declaration` is a top-level class-like declaration treated as a class for outline purposes. Nameless `function_signature` (closures, anonymous functions) is filtered to keep the symbol map clean.
- **Three coupling points wired** (drift guard stays green): `aikit/chunk/treesitter` adds `.dart → dart` to `KenToTreeSitter`; `.goreleaser.yml` adds `grammar_subset_dart` so release binaries embed the Dart grammar; `internal/structural/index.go` registers `.dart → dart` and `dart → extractDart`. The `TestSubsetTagsMatchKenToTreeSitter` drift guard catches any future drift.
- **Cross-corpus parse-health survey** (`scripts/dart_survey.go`) before shipping: 74% / 94% / 72% clean root on [dart-lang/web](https://github.com/dart-lang/web) / [dart-lang/dart_style](https://github.com/dart-lang/dart_style) / [flutter/samples](https://github.com/flutter/samples) — usable across both vanilla Dart and Flutter.
- **Dogfood validation:**
  - **dart_style** (80 files): 1,565 functions (823 methods), 143 classes, build time 2.7 s. Top calls are domain vocabulary (`format` / `pushIndent` / `popIndent` / `space` / `State`); imports correctly bound (`piece` / `code_writer` / `profile`); raises are real exception types (`ArgumentError` / `FormatException` / `StateError`).
  - **flutter/samples** (483 files): top calls are real Flutter widget vocabulary — `Text` / `Center` / `Scaffold` / `AppBar` / `Padding` / `Column` / `MaterialApp` / `pumpWidget` / `runApp`. Imports resolve correctly to package names (`material` / `cupertino` / `flutter_test` / `go_router` / `provider`).

### Documented — C# OOM root cause (upstream-ready memo)

- **`docs/internal/csharp-oom-root-cause.md`** — full diagnostic of the gotreesitter C# grammar's unbounded recursion in its post-parse namespace recovery pass. The 65-byte minimal reproducer (`namespace N { class C { void M(string n) { F(n, E.A | E.B); } } }`) allocates 9M+ objects / 3 GB Go heap in ~3 s before OOM. Root cause: `parser_result_csharp.go`'s `normalizeCSharpRecoveredNamespaces` triggers `parseWithSnippetParser` on a sub-range, which re-enters the same recovery pass with a fresh `recoveryCount = 0`. The `csharpMaxNamespaceRecoveries = 32` cap bounds breadth within one frame, but not depth across frames. Memo includes the captured goroutine stack showing 5 nested recursions on the same 65-byte source range, an `alloc_objects` pprof profile dominated by `NewParser → buildSmallLookup` (>94%), three suggested fix directions (depth counter via `parseWithSnippetParser` opts; range-progress guard; pool-acquisition depth cap), and a test that would have caught it.
- **Three reproducer scripts** in tree under public gotreesitter APIs so upstream can re-run them: [`scripts/csharp_oom_diag.go`](scripts/csharp_oom_diag.go) (`--mode=leak / --mode=per-file / --mode=single`), [`scripts/csharp_bisect.go`](scripts/csharp_bisect.go) (fork-and-budget bisection), [`scripts/csharp_pprof.go`](scripts/csharp_pprof.go) (in-process pprof dump when heap crosses 1.5 GB; HTTP pprof can't be used because the parse goroutine starves the scheduler).
- **`extract_csharp.go`** stays in tree behind a `csharp` build tag (compiles only with `go build -tags=csharp`), so re-enabling C# is a two-line registration change once the upstream fix lands. `kenLangToTSLang` and `langExtractor` map entries are commented out with the rationale inline.

### Documented — Swift parse misbehavior (upstream-ready memo)

- **`docs/internal/swift-parse-root-cause.md`** — diagnostic of the gotreesitter Swift grammar's lexer misbehavior on real-world Swift. The line-comment lexer fails to recognize `//` followed by common English words like "and" / "software" / "associated" / "Permission" — a 35-byte file `//\n//  software\n\nclass Foo {}` already parses to `root=ERROR`. Cross-corpus survey (`scripts/swift_survey.go`) measured **0% / 2% / 8% / 35% clean-parse rates** on Alamofire / swift-nio / swift-collections / Defaults respectively — universally broken on shipping code because every MIT-license-headered file fails. Different failure mode from C# (this one finishes promptly but produces garbage trees), same downstream effect (extractor returns zero useful data).
- **`extract_swift.go`** parked behind a `swift` build tag (the chunker layer keeps the Swift grammar via the slim subset — it still produces some boundaries even from ERROR roots — but the structural extractor needs a clean AST). Memo includes the minimal reproducer, the per-trigger-word list, the cross-corpus failure rates, and a test that would have caught it. Re-enabling is a two-line registration change once upstream fixes the lexer.
- **DESIGN.md §10 risk register** updated with the Swift entry alongside the existing C# and bash entries. The chunker still embeds the Swift grammar in slim binaries (it produces *some* chunks from ERROR roots, which is better than the line-chunker fallback for retrieval); only the structural extractor is gated off.

### Added — `docs/internal/add-a-language.md` walkthrough

- Step-by-step guide for adding a new language to ken's structural index: AST probing via `KEN_DEBUG_AST=1 KEN_DEBUG_LANG=<grammar> go test -run TestDebug_ASTShape` (with `KEN_DEBUG_AST_DEPTH` knob added in this release), writing the extractor, registering in `kenLangToTSLang` + `langExtractor`, fixture tests, dogfood validation, precision-sample check, lint / commit. Documents the field-name-dropping quirk (Rust / Kotlin / Dart fallback pattern), common patterns (method receivers, generic-type instantiation, import binding), and what to do when the dogfood pass kills the language (the C# and Swift parks are the worked examples).
- **DEVELOPERS.md "Adding a structural extractor"** entry now links to the walkthrough instead of carrying the inline summary.

### Changed — gotreesitter v0.20.0-rc2 → v0.20.0-rc3

- Cross-checked the C# OOM against rc3 (which targeted GLR fork-reduction on the C grammar, not C#); rc3 still OOMs the same way. The bump is captured in ken's `go.mod` and aikit's `go.mod` (the sister aikit commit also bumps aikit from v0.19.1 to align). `KEN_DEBUG_LANG=csharp` aliasing added to `debug_ast_test.go::debugLangGrammar` so the alias resolves to gotreesitter's `c_sharp` grammar name.

### Notes — backwards compatibility

- **No behavior change for any existing language.** The Kotlin + Dart additions are pure adds; no other extractor was touched. C# and Swift never shipped in v0.9.0's `kenLangToTSLang` to begin with (C# was already in `chunk/treesitter` only); the build-tag parks in this release codify the existing OFF state with clear re-enable paths.
- **No API additions or removals.** No new tools, no new MCP surface, no new library functions. The structural extractor surface for new languages is the same (`extract_<lang>.go` + two map rows) as documented since v0.9.0.
- **Drift guard stays green.** `internal/buildchecks/subset_test.go::TestSubsetTagsMatchKenToTreeSitter` passes — the three Dart wirepoints (`KenToTreeSitter` / `.goreleaser.yml` slim tag / structural extractor) are in sync.

## [0.9.0] — 2026-06-03 — 1.0 release candidate

The feature-complete 1.0-RC: Stage 8 closes (structural-navigation tools shipped across 10 languages + Arm B enrichment wired into production); the seven-item 1.0 ship-list closes (every tool an agent needs + JSON output mode + status / recently_changed surfaces + first-class USERS / DEVELOPERS docs); the startup + query-latency perf campaign closes (cold-start budget down 55–79% across corpora). Detail below, grouped by theme.

### Added — Stage 8 Track 2 structural-navigation tools across 10 languages

- **Five MCP tools** for tree-sitter-grade, name-resolved (NOT type-resolved) structural navigation:
  - `definition(symbol)` — locate every site where a function / class / method is defined. Bare `symbol` returns top-level AND every method on any type; qualified `Type.method` pins one.
  - `references(symbol)` — every file where a name appears in a recognized syntactic context (call site, import, raise).
  - `callers(symbol)` — files containing a call to the named function. File-level granularity. **Stage 8 Gate 2 precision sample: 100% on 400 edges across 8 languages.**
  - `outline(path)` — top-level functions, classes, methods (with parameter names) per file or directory.
  - `symbols([path])` — every top-level symbol defined in the repo, optionally filtered by directory prefix.
- **Ten languages** via dedicated gotreesitter-backed extractors: Python · Go · TypeScript · JavaScript · Java · Rust · C · C++ · PHP · Ruby. Per-language identifier-resolution rules + noise-filter calibrated against eight popular dogfood repos (excalidraw, express, spring-petclinic, ripgrep, leveldb, redis, laravel, jekyll).
- **Two extractor refinements found via the dogfood pass**: `#include <foo.h>` surfaces as a C/C++ import (basename, sans dir + ext); Node CommonJS `require('mod')` calls are routed to `fs.Imports` instead of leaking `require` into `fs.Calls`.

### Added — Arm B chunk-level enrichment (ADR-035)

- **Deterministic per-file label** (`# func: NAME | calls: A, B | raises: X\n`) prepended to every chunk's text before BM25 tokenization + embedding. Pure-Go, no extra model. Default-on; opt out via `KEN_ENRICH=off` or `FSOptions.DisableEnrichment=true`. **+0.0208 NDCG@10 hybrid on csn-python-nl-stripped (N=500); +0.0321 on CoSQA dev** — both within 0.002 of the validated Gate-1 numbers on the production code path.
- **Single source of truth**: `structural.EnrichFromFileStruct` (per-file, index-free) and `(Index).Enrich` (with optional cross-file callers) both delegate to `enrichCore`. Future bench materializers route through the same function; the Python bench materializer remains as a drift cross-check reference.
- **Closes Stage 8**, with the two validation gates published in `outputs/stage8-gate-1-cosqa-armb-results.md` and `outputs/stage8-gate-2-call-edge-precision.md`. Track 1 (callers-as-chunk-enrichment) closed negative under M0e; query-time graph expansion explored separately, closed negative (`outputs/stage8-qgraph-expansion-results.md`); ColBERT MaxSim probe explored, parked (`outputs/stage8-maxsim-probe-parked.md`).

### Added — 1.0 ship-list user-facing surfaces

- **`callers(name)` MCP tool** — Stage 8 Gate 2 recommendation; described above.
- **Search filters on `search`**: `languages` / `path_contains` / `exclude_path_contains`. Over-fetch (10× top_k, capped at 200) + post-filter + ratio-in-header response. Honest empty-after-filter message names the cause rather than silently returning fewer than top_k.
- **`ken status` CLI + MCP tool** — build identity, model availability, Arm B enrichment state, token-savings summary (today / 7d / all-time, with chars + ~tokens at chars/4), and (when `repo` is passed via the MCP variant) live index + structural + cache stats. Markdown default; `--json` / `output:"json"` for machine-readable.
- **`recently_changed(n)` MCP tool** — git-aware (go-git PlainOpen on the working tree); returns the last N commits with the files each touched. Args: `n` (default 10, max 100), `repo` (local path), `path` prefix filter. Local repos only; URL repos get a friendly "clone first" error.
- **JSON output mode** on `search` / `find_related` / `definition` / `references` / `callers` / `outline` / `symbols` / `status`. Each tool gains an `Output` arg; `output:"json"` returns a typed response struct (response shapes defined in `mcp/json_responses.go` — 1.0-stable surface). Unknown values return a friendly error rather than silent fallback.
- **First-class user-facing docs**: [`docs/USERS.md`](docs/USERS.md) (install per agent, ken-vs-grep, the 9 tools, common config, troubleshooting) and [`docs/DEVELOPERS.md`](docs/DEVELOPERS.md) (mcp.Run library, prebuilt indices, public API stability table, custom chunkers, tuning rerank, performance expectations).

### Changed — perf-startup-query campaign (ADR-036)

- **M2 — Lazy rerank model load.** New `internal/search/LazyReranker` defers `encoder.Load` + persistent cache hydration until the first hybrid+rerank query. **−491 ms on ken-mcp startup when `KEN_MCP_RERANK=on`** — rerank-on startup is now indistinguishable from unset (~30 ms median). For users who don't immediately issue a rerank query, the load is never paid at startup.
- **M4 — Parallel `structural.Build`.** Refactored Pass 1 of structural.Build to `runtime.NumCPU()` workers using the per-file pattern from ADR-030. Determinism preserved by writing per-file results into idx-aligned slots + merging in lexical order before Pass 2. **3.5× on jekyll (−1,127 ms), 4.5× on ken itself (−360 ms).**
- **Cumulative cold-start budget reduction**: tiny 627 ms → 134 ms (−79%); medium (ken) 1,405 ms → 555 ms (−60%); large (jekyll) 2,927 ms → 1,309 ms (−55%). Warm-search p50 unchanged (already sub-millisecond at M0; H4 confirmed).
- **M1, M3, M5 killed**: M2 superseded M1 (Q8 rerank default); H2 refuted M3 (warm-up Encode penalty <0.3 ms); H4 confirmed M5 (query-path already sub-ms).
- **Campaign closure** in [ADR-036](docs/internal/DECISIONS.md#adr-036-close-the-startup--query-latency-perf-campaign). Out-of-band trigger: real user latency report against a hot path none of M0's measurements touched.

### Changed — extracted reusable packages into separate `aikit` module (ADR-034)

- **New module [`github.com/townsendmerino/aikit`](https://github.com/townsendmerino/aikit) at v0.1.0** (now pinned at `v0.1.1`). ken's reusable algorithm packages — `topk`, `ann`, `bm25`, `embed`, `coderank` (renamed `encoder`), and `chunk` (+ `regex` / `markdown` / `treesitter`) — moved out of `internal/` (and out of public `chunk/`) into a second module that any project can import. ken now consumes them via `require github.com/townsendmerino/aikit`. Closes the "reusable by another project" path that `internal/` foreclosed. See [ADR-034](docs/internal/DECISIONS.md#adr-034-extract-reusable-algorithm-packages-into-a-separate-aikit-module).
- **`coderank` renamed to `encoder`** on the move. The package is a transformer encoder, not a PageRank-style ranker; the old name misled. The rename ripples through error prefixes, the env var (`KEN_MCP_RERANK_QUANT` and friends unchanged — those name the operator-facing knob), `testdata/coderank-model` → `testdata/encoder-model`, scripts (`scripts/pin_coderank.py` → `scripts/pin_encoder.py`, etc.), and ken's `.gitignore`. HuggingFace model name (`nomic-ai/CodeRankEmbed`) and on-disk cache scope key are unchanged.
- **Breaking change for the public `chunk` path**: was `github.com/townsendmerino/ken/chunk` (added in ADR-032 two days prior, never tagged in a release); now `github.com/townsendmerino/aikit/chunk`. GitHub code search returns zero downstream consumers — hard break, no shim.

**Calibration:** numerically bit-identical. `aikit/encoder` golden cosine = 1.000000 preserved on all 18 fixtures. `TestForwardBatch_matchesSingle` exact. `TestMatmulBT_blockedMatchesNaive` exact on all 8 shapes. v0.6.0 binary-size contract still satisfied.

### Changed — slim release binaries via gotreesitter `grammar_subset` (ADR-033)

- **gotreesitter `v0.19.1` → `v0.20.0-rc2`**, and ken's release binaries (`ken`, `ken-mcp`) now build **slim** — embedding only the 17 tree-sitter grammars `chunk/treesitter` actually dispatches (per `kenToTreeSitter`) instead of all 206 — via the `grammar_subset` build tags in `.goreleaser.yml`. **Measured: `ken-mcp` 52.3 → 38.3 MB, `ken` 36.0 → 22.0 MB** (M1 Pro / `CGO_ENABLED=0`). The C-only `ken-demo-postgres` similarly drops ~16 MB of download. Resolves the embed-layer limitation [ADR-023](docs/internal/DECISIONS.md#adr-023) flagged in v0.8.2.
- **Library `go build` / `mcp.Run` is unaffected** — build tags are set by whoever compiles, not by importers; external embedded-corpus authors who don't pass the tags still get all 206 grammars. Slim is opt-in to ken's own release pipeline.
- **Drift guard**: `chunk/treesitter`'s `TestSubsetTagsMatchKenToTreeSitter` asserts the `.goreleaser.yml` subset tag set equals `kenToTreeSitter` (both directions); a CI compile-smoke builds the slim binaries on every PR.

### Changed — public `chunk` package (ADR-032)

- **`internal/chunk` → `chunk` (public)** — and then to `aikit/chunk` two days later under ADR-034. The `Chunker` interface is the **hard, 1.0-committed** seam; concrete chunkers (especially `treesitter`, backed by pre-1.0 `gotreesitter`) are **best-effort**: valid contiguous byte-faithful chunks guaranteed, exact boundaries not (~0.1% wobble under load, [#35](https://github.com/townsendmerino/ken/issues/35)).

### Added — ken-mcp prebuilt-index auto-load (#32, ADR-024 close)

- **`ken-mcp` auto-loads `<repo>/.ken/index.bin`** when present, matching the convention `mcp.Run` already did. Cold start drops from full live walk+chunk+embed to ~1-2 s index hydration. Mode/chunker mismatch is a hard startup failure (exit 1); corrupt / format-version / missing-model fall back to a live build with a stderr warning.
- **Eager startup validation for the default repo** (`KEN_MCP_DEFAULT_REPO`): index loaded + validated at startup so the first query is instant.

### Added — treesitter per-reason fallback counters (#31)

- `chunk/treesitter` reports per-reason fallback counts via `Stats()`; `ken perf index` JSON surface exposes them under `treesitter` when `--chunker=treesitter`. **Observability only**; ADR-010's silent-fallback semantics are preserved (no behavior change, no stderr noise).

### Changed — public API discipline

- **Un-deprecated `search.FromPath` and `repo.Walk`** for 1.0. Both are thin wrappers around the `fs.FS` variants and are useful at ~10 call sites in our test/bench/script code. The deprecation marker was dropped after the 1.0 audit confirmed both signatures stable. Doc-comments now describe them as "1.0-stable" with rationale (string path vs fs.FS choice).
- **Best-effort markers added** to `CloneShallow`, `NormalizeKey`, `ValidateEnum` — useful for custom Cache `Builder` implementations but signature/semantics may evolve. Hard-1.0 surfaces enumerated in [DEVELOPERS.md → Public API surface](docs/DEVELOPERS.md#public-api-surface).
- **Stale doc-comments fixed**: language coverage claims now accurately list ten languages (was "Stage 8 v0 supports Python only"); MCP tool descriptions audit confirmed consistent in voice and accurate against the implementation.

### Other

- **MaxSim probe parked** with a closing memo and reopen-trigger list (`outputs/stage8-maxsim-probe-parked.md`). Slim N=25 sample shows MaxSim consistently underperforms CLS-pool on CodeRankEmbed's token vectors; the cheap-reuse path for ColBERT is killed.
- **Windows binary deferred until pressure** — pure-Go cross-compile is technically trivial but the support surface (CRLF, path separators, MCP quirks) isn't free; re-opens on real user reports.
- **CI Docker-pulls-Postgres flake documented** with mitigation (`gh run rerun --failed`) and the permanent-fix path (mirror service images to ghcr.io). Deferred until load-bearing.
- **Aikit alignment**: ken's CHANGELOG notes that ken 1.0 requires aikit at a tagged 1.0 (or clearly within a 1.0-RC window) for the stability promises to compose. Coordination point, not a blocker.

## [0.8.8] — 2026-05-27

**DB introspection sample-loop parallelism + treesitter fallback counters + ken-mcp prebuilt-index auto-load.** Not separately changelogged in detail at the time; covered in retrospect under [0.9.0] above (the #31 / #32 / ADR-031 entries). Tag exists at commit 7efdbde with corresponding GitHub release artifacts.

## [0.8.7] — 2026-05-27

**Parallel index build — per-file workers for chunk + embed.** [ADR-030](docs/internal/DECISIONS.md#adr-030-indexing-pipeline-parallelism--phase-a-per-file-workers-for-chunk--embed-v087): hybrid INDEX wall 165.3 s → 45.4 s (3.64× speedup); GC share on hybrid 38% → 8%; bm25 1.15×. N=20 determinism stress confirmed bit-identical (one unique SHA per mode). NDCG@10 exact-match all three modes. Tag exists at commit b3ec110 with corresponding GitHub release artifacts. (Calibration retrospectively documented in `outputs/project-v087.md`-equivalent memory entry; not separately changelogged at the time.)

## [0.8.3] — 2026-05-26

**Cold-start optimization for the v0.6.0 embedded-corpus build pattern.** Narrative: "v0.6.0 shipped embedded corpora; v0.8.3 makes their cold start fast." [ADR-024](docs/internal/DECISIONS.md#adr-024-pre-built-embedded-indices-for-mcprun-v083) closes the optimization gap [ADR-016](docs/internal/DECISIONS.md#adr-016-embedded-corpus-mcp-build-pattern-via-mcprun-library-function) left open. SDK authors using `mcp.Run` can now pre-build their search index at `go generate` / build time and ship it inside their `//go:embed` corpus; `mcp.Run` auto-loads it at startup and skips the per-launch walk + chunk + embed pass. Closes [#10](https://github.com/townsendmerino/ken/issues/10).

The v0.6.0 single-static-binary contract is preserved: pre-built indices live inside the SDK author's `//go:embed corpus`, not as sidecar assets. Lazy fallback on any load failure (corrupt bytes, format-version mismatch, mode/chunker mismatch, missing file) keeps the optimization opt-in to the upside, not opt-in to the failure mode — a stale or corrupt pre-built file produces a slower-but-still-working binary with a stderr warning naming the reason, not a deployment outage.

**Calibration discipline.** Cold-start time is the specific gap closed. This is **NOT** a retrieval-quality improvement, **NOT** a recall improvement, **NOT** a search-ranking change. `docs/BENCH.md`'s hybrid-retrieval recall@10 numbers measure a different system and are unaffected by this work. Same calibration shape as v0.8.1's "Tier-1 SQL chunk fidelity, NOT recall" framing and v0.8.2's "investigation outcome, NOT feature ship" framing, applied here to "cold-start optimization, NOT search-quality change."

### Added (ADR-024; closes #10)

- **`search.BuildAndSerializeIndex(fsys fs.FS, opts BuildOptions) ([]byte, error)`** — library function. Builds the full `*search.Index` from `fsys` and returns the serialized bytes ready to embed via `//go:embed` or write to disk. SDK authors who script the build call this directly; the `ken build-index` subcommand below is a thin operator-facing wrapper around it.
- **`search.LoadSerializedIndex(data []byte, opts LoadOptions) (*Index, error)`** — library function. Reconstructs a fully-usable `*Index` from serialized bytes (verifies header + CRC32, decodes chunks + embedding matrix, calls existing `BuildIndex` to re-tokenize for BM25 + wire up `ann.Flat`). Returns typed errors `ErrCorrupt` / `ErrFormatVersion` / `ErrModeMismatch` / `ErrChunkerMismatch` / `ErrModelRequired` so callers can decide whether to fall back or hard-fail.
- **`search.BuildOptions` + `search.LoadOptions`** — public option structs. `BuildOptions` mirrors `FromFSWithModel`'s argument shape (Mode, Chunker, Model). `LoadOptions.ExpectedMode` / `ExpectedChunker` are sanity checks against the on-disk header; `LoadOptions.Model` is required for semantic / hybrid mode (the loaded `*Index` carries it, so `WithExtraChunks` works on loaded indices the same way it works on freshly-built ones).
- **`ken build-index <corpus_dir> -o <output_path> [--mode ...] [--chunker ...] [--model DIR]`** — new CLI subcommand of the existing `ken` binary. Model resolution priority order matches `ken index` / `ken search`: `--model` flag → `KEN_MODEL_DIR` → `~/.ken/model` → `./testdata/model`. Writes atomically (`<output>.tmp` + `os.Rename`); auto-creates the parent directory (typical case: `.ken/` inside the corpus on first run).
- **`mcp.Options.PrebuiltIndex []byte`** — optional explicit override. When non-nil, `mcp.Run` loads the supplied bytes instead of consulting the corpus FS for the conventional path. For SDK authors using a non-conventional layout (pre-built bytes in a sibling `embed.FS`, downloaded at runtime, etc.).
- **`mcp.Run` convention-over-configuration auto-discovery.** When `Options.PrebuiltIndex` is nil, `mcp.Run` reads `corpus/.ken/index.bin` from the supplied `fs.FS`. SDK authors who follow the convention (`ken build-index ./corpus -o ./corpus/.ken/index.bin` before `go build`) get the cold-start improvement with zero `main.go` changes from v0.6.0.
- **Walker `.ken/` prune.** `internal/repo`'s walker (both `WalkFS` and `Matcher.ShouldIndex`) skips `.ken/` directories analogously to the existing `.git/` prune. Without this, the pre-built index file would be re-chunked into the corpus on the lazy-fallback build path. No env var; convention.
- **Binary format spec.** Custom binary with `"KEN1"` magic + uint32 format-version gate + informational ken-version string + len-prefixed chunks/vecs sections + CRC32 IEEE corruption trailer. Format reference lives at the top of `internal/search/index_serialize.go`. Internal-only — not a public API; ken's own serialization for its own use.
- **11 serialization unit tests** in `internal/search/index_serialize_test.go` covering full roundtrip for BM25 / Semantic / Hybrid modes, each typed error (magic / format-version / mode / chunker / CRC mismatch + missing-model), forward-compat of the ken-version field, and the determinism regression guard (two builds of the same corpus produce byte-identical bytes).
- **5 CLI subcommand tests** in `cmd/ken/build_index_test.go` covering happy path (build + load + search), `.ken/` auto-creation (and double-build to verify the walker prune), missing corpus error, invalid mode error, missing `-o` error.
- **6 `mcp.Run` integration tests** in `mcp/run_prebuilt_test.go` covering the no-prebuilt baseline (silent fallback), valid auto-discovered pre-built (info log + functional index), explicit override via `Options.PrebuiltIndex`, corrupt pre-built (warn + fallback + index still works), mode downgrade interaction, and chunker mismatch.

### Notes (backwards compatibility)

- **`mcp.Run` is byte-identical for v0.6.0 callers who don't opt in.** SDK authors who don't ship a pre-built index see no behavior change — the same v0.6.0 build-from-corpus pass runs. The only addition is a debug-level log entry naming the missing `.ken/index.bin` path (suppressed unless `LogLevel=debug`).
- **`*search.Index` API unchanged.** `Search` / `FindRelated` / `ResolveChunk` / `WithExtraChunks` / `Len` / `Chunks` all behave the same on loaded-from-serialized indices as on freshly-built ones. The model-reference handling threading is internal to the load path.
- **Stdout-cleanliness invariants unchanged.** All seven stdout-clean variants (stock / Postgres / SQLite / MySQL / LISTEN/NOTIFY / reindex_db / MariaDB) remain in the test suite. The stock + SQLite variants run on every checkout's `go test ./cmd/ken-mcp/` and continue to pass byte-identically there. The other five require service containers via the `test-db-integration` job and run CI-only — `KEN_DB_TEST_DSN` / `KEN_DB_MYSQL_TEST_DSN` / `KEN_DB_MARIADB_TEST_DSN` gate them locally, so "byte-identical" applies on the CI runner that actually executes them, not on a local checkout that skips them. `mcp.Run` reads from the supplied `fs.FS`; no new stdout-clean variant is required because no new daemon-path component was added.
- **Existing dep tree unchanged.** Custom binary serialization uses `encoding/binary`, `hash/crc32`, and the existing `io/fs` plumbing. No new third-party deps.
- **v0.6.0 single-static-binary contract preserved.** Pre-built indices live inside the SDK author's `//go:embed corpus`, not as sidecar assets. The `cmd/ken-mcp-docs` worked example continues to be a single 74 MB binary.

### Calibration amendment (post-v0.8.3 audit; supersedes the v0.8.2 entry below)

A post-v0.8.3 re-measurement against `gotreesitter` v0.18.0 (cross-checked against v0.19.1) found that the v0.8.2 stale-claim entry below got one figure wrong. **The v0.8.2 line "DESIGN.md §1's '19 MB' grammar-bundle size updated to '~26 MB'" conflated two distinct quantities.** The actual figures, with units of measurement made explicit:

- **On-disk grammar bundle: ~19 MB** (20,011,313 bytes; 206 `*.bin` files in `gotreesitter/grammars/grammar_blobs/`). Stable across v0.18.0 and v0.19.1 — did NOT grow from ~19 MB to ~26 MB despite the grammar count growth from 17 to ~206.
- **Linked-binary cost when treesitter is imported: ~26 MB** (`cmd/ken-mcp` darwin/arm64: 55,712,704 B with the blank import vs 28,726,752 B without — delta 26,985,952 B). Includes the embed payload + parser runtime + symbol bookkeeping.

v0.8.2's amendment merged both into one number. The v0.8.3 sweep corrected the three in-code comments (`cmd/ken-mcp/main.go`, `cmd/ken-mcp-docs/main.go`, `internal/search/index.go`), DESIGN.md §1, and added the [calibration-amendment block to ADR-023](docs/internal/DECISIONS.md#calibration-amendment-post-v083-audit). This CHANGELOG note completes the sweep by amending the v0.8.2 entry (the original source of the conflation) without overwriting its body — same amend-don't-overwrite discipline that v0.8.2 itself applied to v0.6.0.

**Canonical figures going forward**: ~19 MB on-disk grammar bundle, ~26 MB binary cost when treesitter is imported. See ADR-023's calibration amendment for the full audit trail.

## [0.8.2] — 2026-05-25

**Investigation outcome release.** v0.8.2 ships no new features. v0.8.x's calibration-release discipline applied to [#16](https://github.com/townsendmerino/ken/issues/16) (selective tree-sitter grammar embedding for smaller binaries): we investigated whether `gotreesitter`'s package shape permits per-language binary-size reduction via source-file build tags, found that the embed layer's monolithic `//go:embed grammar_blobs/*.bin` glob defeats the approach at the v0.18.0 layout, documented the finding honestly, named the specific upstream change that would unblock the feature, and closed #16 as wontfix-without-upstream-cooperation. The 74 MB `cmd/ken-mcp-docs` binary measured in [ADR-016](docs/internal/DECISIONS.md#adr-016-embedded-corpus-mcp-build-pattern-via-mcprun-library-function) stays 74 MB after v0.8.2.

This release is the kind of outcome v0.8.x's calibration framing makes legitimate: when an investigation closes a door, ship the honest answer instead of pretending the work is more than it is. Same calibration discipline v0.8.1 Part C applied to Tier-1 SQL chunk fidelity vs retrieval recall, and v0.8.1 Part B applied to chunk-rendering consistency vs search-ranking improvement, now applied to "the answer is no, here's why, here's what would change it."

### Documented (ADR-023; closes #16)

- **`gotreesitter` v0.18.0 package-shape investigation finding** ([ADR-023](docs/internal/DECISIONS.md#adr-023-gotreesitter-grammar_subset-machinery--binary-size-reduction-outcome-v082-investigation-outcome)). Registration layer is per-language-gateable via the existing `grammar_subset` + `grammar_subset_<lang>` tag pair (cooperative). Embed layer is monolithic via a single `//go:embed grammar_blobs/*.bin` glob in `blob_source_embedded.go` (uncooperative — the per-language source split needed to make build-tag gating actually shrink the binary does not exist upstream at v0.18.0). The `grammar_blobs_external` runtime-load tag exists but breaks ken's single-static-binary value proposition by requiring operators to ship grammar blobs as sidecar assets. ADR-023 documents the finding + the concrete upstream-PR proposal that would change the answer (split `blob_source_embedded.go` into per-language source files, each with `//go:embed grammar_blobs/<lang>.bin` + `//go:build !grammar_blobs_external && grammar_subset_<lang>`).
- **[ADR-016](docs/internal/DECISIONS.md#adr-016-embedded-corpus-mcp-build-pattern-via-mcprun-library-function) amended** with the v0.8.2 source-tag-layer finding — the original linker-DCE rejection of per-language tree-sitter sub-packages stands as written for v0.5.0; the amendment adds the parallel rejection at the source-file-build-tag layer under the same alternative. Both layers fail for the same fundamental reason at the current `gotreesitter` layout: the `embed.FS` payload is shared, so neither package-import-level nor source-file-tag-level mechanisms can drop unreferenced grammar bytes.
- **Vendor + patch stop-gap documented as available, not pursued.** ADR-023's alternatives section names the maintenance-cost analysis (one-patch maintenance vs ongoing upstream-tracking against an actively-developed dependency) for when (if) size pain becomes acute enough to justify a vendor fork. The trigger is concrete: an SDK author or operator filing an issue with measured size pain from a use case that needs the treesitter chunker AND a smaller binary AND cannot use sidecar assets.

### Documented (stale-claim correction)

- **[`docs/DESIGN.md`](docs/DESIGN.md) §2 / Option A** previously claimed `gotreesitter`'s `grammar_blobs_external` build tag is "used by `ken-mcp` releases — see §8." Audit confirmed `grammar_blobs_external` is not referenced in `.goreleaser.yml`, `.github/workflows/`, or any build script, and ADR-016's alternatives section already rejected the tag for breaking the single-static-binary contract. The DESIGN.md claim was stale aspirational text from earlier in the project's life that survived ADR-016's settlement. v0.8.2 corrects it to "available upstream but NOT used by ken's releases" with a cross-reference to ADR-023.
- **DESIGN.md §1's "19 MB" `gotreesitter/grammars` bundle size** updated to "~26 MB" to reflect `gotreesitter` v0.18.0's growth from 17 to ~206 grammars between v0.6.0 (ADR-016's original measurement) and v0.8.2 (Phase 1's measurement). The chunker-registration refactor that keeps the bundle out of `cmd/ken-mcp-docs` continues to work as documented; only the bundle's absolute size has grown.
- **README "Choosing a chunker"** gets a tight cross-reference to ADR-023 so readers curious why grammars are linked into every build land on the investigation outcome.

### Notes (framing discipline)

- **No new features.** ken's binary size is unchanged. Tree-sitter chunker behavior is unchanged. The `--chunker=treesitter` flag continues to opt operators into tree-sitter parsing; the default regex chunker continues to be the lighter-weight path. No env vars, no flags, no test surface, no exposed API surface changes.
- **v0.8.2 is what shipping an investigation outcome looks like.** When the Phase 1 investigation closed the door, ken's release process named the door (this CHANGELOG, ADR-023), named the specific change that would open it (the upstream embed-split PR proposal in ADR-023's alternatives), and closed the issue honestly (#16 wontfix-without-upstream-cooperation, with the proposal as the closing comment's reopening trigger). Future investigation-outcome releases follow this shape: name the actual gap, name the upstream / external trigger that would change the answer, document the stop-gap that's available if pain becomes acute, close the issue with the trigger as the reopening condition.
- **#16 closes with a concrete reopening trigger.** If the upstream `gotreesitter` PR proposed in ADR-023's alternatives lands (whether sent by ken's team or anyone else), ken can ship build-tag-gated grammars in a future v0.8.x+ release without forking. The closing comment on #16 names the specific upstream change so the path is unambiguous; the issue's reopening trigger is "upstream merged the embed-split," not "we should look at this again someday."

### Notes (backwards compatibility)

- **No code changes.** No new dependencies; no new env vars; no new build tags consumed by ken's own build; no test invariants regressed. `go build`, `go test`, `go vet`, and `gofmt` produce identical output to v0.8.1 modulo the documentation-only diffs in [`docs/internal/DECISIONS.md`](docs/internal/DECISIONS.md), [`docs/DESIGN.md`](docs/DESIGN.md), and [README.md](README.md).
- **Stdout-cleanliness invariants unchanged.** All seven stdout-clean variants (stock / Postgres / SQLite / MySQL / LISTEN/NOTIFY / reindex_db / MariaDB) continue to pass byte-identically to v0.8.1.

## [0.8.1] — 2026-05-25

**The calibration release.** Three Parts that each close a gap between something ken claimed and what ken actually delivered. The through-line is calibration credibility: when a doc, an instruction string, or an ADR says ken does X, ken should *load-bearing-do* X — and when ken can't, the docs should name the gap honestly. v0.8.1 walks three of those gaps and closes them.

- **Part A — Cleanup pass + agent-instruction polish.** ADR-018-era `mcp/server.go` default instructions told agent planners ken is suitable for code search, with a hardcoded "82–91% recall at K=10" line steering them toward grep for exhaustive enumeration. Hardcoded numbers date the planner-visible guidance the moment ken's pipeline improves — an agent reading stale instructions over-defers to grep. Part A replaces the line with qualitative framing ("ken is relevance-optimized; fall back to your native grep / file-search tool for refactors, renames, or any operation that must be exhaustive — grep gives 100% recall on literal matches") and adds a regression-guard test that asserts the banned hardcoded numbers don't sneak back in. Part A also lands two pure refactors (`internal/repo/walk.go` → sibling `ignore.go`; `internal/sql/fold.go` → sibling `migrations.go`) that reduce mental load when reading + give Part C's rename-map work a clean dedicated file to land in.

- **Part B — MariaDB first-class engine** ([ADR-021](docs/internal/DECISIONS.md#adr-021-mariadb-first-class-engine-support-v081-part-b)). [ADR-019](docs/internal/DECISIONS.md#adr-019-mysql-engine--schema-filtering-for-multi-schema-dev-databases) documented MariaDB as "compatible-but-not-CI-tested" in v0.7.2. v0.8.1 Part B makes that load-bearing-tested via a `mariadb:11-jammy` service container in the existing `test-db-integration` job + a divergence audit + targeted normalization of the one substantive INFORMATION_SCHEMA divergence the audit found (integer display widths: MariaDB 11.x still emits the legacy `bigint(20)` / `int(11)` syntax MySQL 8.0 deprecated; ken now strips `(N)` from integer-family types unconditionally so cross-engine chunks are byte-identical). ADR-021 documents the Tier 2 (DEFAULT-expression quote-stripping + function-name normalization) and Tier 3 (view-body parenthesization) divergences as deliberately-non-normalized cosmetic differences — quote-stripping carries mangling risk on escaped values, view bodies need a SQL parser. No operator-visible config changes; `KEN_DB_DSN` continues to route both engines identically.

- **Part C — RENAME COLUMN + RENAME CONSTRAINT folding** ([ADR-022](docs/internal/DECISIONS.md#adr-022-rename-column--rename-constraint-folding-via-eager-application-v081-part-c), closes [#14](https://github.com/townsendmerino/ken/issues/14)). [ADR-018](docs/internal/DECISIONS.md#adr-018-sqlite-engine--migration-history-folding-via-lightweight-alter-replay) listed RENAME as out-of-scope for v0.7.1's Tier-1 migration-folding pass. The BOTH-chunks fallback preserved correctness, but folded chunks showed pre-rename column names while the live database (via Tier 2) had post-rename names — "fold gives current schema shape" wasn't quite true for RENAME-heavy migration histories. v0.8.1 Part C closes that gap via eager application: `applyColumnRename` mutates the in-flight `foldedTable` directly so subsequent ALTERs see the post-rename state, and `renameInFirstParens` rewrites this-table column references inside constraint strings via a word-boundary regex scoped to the first parenthesized group (FK source-side rewritten; FK target-side `REFERENCES other(remote)` left verbatim per the per-table scope ADR-022 documents). 13 new tests cover the chain (A→B→C), the cycle round-trip (A→B→A → A), the rename-then-re-add interaction, drop-then-re-add-then-rename, cross-table FK source-side-rewritten / target-untouched, multi-column constraint participation, the word-boundary regex regression guard, the named-constraint rename, the anonymous-constraint BOTH-chunks fallback, the missing-source BOTH-chunks fallback, the idempotence regression guard, SQLite syntax variant, and the MySQL `CHANGE` BOTH-chunks fallback.

  **Framing discipline (load-bearing for the calibration narrative).** RENAME folding is a **Tier-1 SQL chunk-content fidelity** improvement. It is **NOT** a recall / search-ranking improvement — `docs/BENCH.md`'s hybrid-retrieval recall@10 numbers (82–91%) measure a completely different system. v0.8.1 Part C closes the chunk-content gap; the retrieval-pipeline numbers are unchanged.

**All three Parts shipped on branch `v0.8.1-cleanup`; v0.8.1 ready to tag.**

### Added (Part A: cleanup + instructions polish)

- **`internal/repo/walk.go` → `ignore.go` split.** Pure refactor; gitignore-rule plumbing (rule, gitignore, scopedGitignore, loadGitignoreFS, parseGitignore, pruneScopes, matchScopes, relToScope, collectGitignores, compileRule) moves into a sibling file in the same package. Zero behavior change.
- **`internal/sql/fold.go` → `migrations.go` split.** Pure refactor; migration-directory detection (migrationPatterns, classifyMigrationName, minMigrationFiles, IsMigrationDir) moves into a sibling file. Load-bearing for Part C — the RENAME helpers land in `migrations.go`. Zero behavior change.
- **`mcp/server.go` default instructions polish.** Hardcoded "82–91% recall at K=10" line removed; replaced with qualitative framing that won't date when ken's pipeline improves. The polished text steers agent planners toward grep for refactors / renames / exhaustive operations while keeping ken for conceptual queries + locating definitions + surface explorations.
- **`TestDefaultInstructions_ContainsRecallGuidance`** — sanity test that the polished default-instructions text contains the load-bearing tokens ("ken", "grep", "100% recall", "isn't designed for exhaustive enumeration") AND that the banned hardcoded numbers ("82", "91%", "K=10") are absent. If a future commit reintroduces them, this test catches the regression.

### Added (Part B: MariaDB first-class engine; ADR-021)

- **`mariadb:11-jammy` service container** in the `test-db-integration` job, alongside `postgres:16-alpine` + `mysql:8`. The existing `internal/db/mysql_integration_test.go` suite is parameterized over both engines (5 tests × 2 engines = 10 subtests); same fixture, same assertions, byte-identical chunks across engines.
- **`KEN_DB_MARIADB_TEST_DSN`** — CI / development-only env var for the integration suite to run against a live MariaDB container in parallel with `KEN_DB_MYSQL_TEST_DSN`. End users continue using `KEN_DB_DSN`; no operator-visible config change.
- **`TestBinary_StdoutIsCleanJSONRPC_WithMariaDB`** — seventh sibling of the stdout-cleanliness contract suite (stock / Postgres / SQLite / MySQL / LISTEN/NOTIFY / reindex_db / MariaDB). Spawns the real `ken-mcp` binary via `sdk.CommandTransport`, drives a search call against the live MariaDB service, asserts stdout stays JSON-RPC-clean and stderr proves "Tier 2: indexed" ran.
- **`normalizeMySQLIntType`** — strips legacy `(N)` integer-family display widths (`bigint(20)` → `bigint`, `int(11)` → `int`). Applied unconditionally at three read sites in `mysqlListTablesAndColumns` + `mysqlListRoutines` (column types, function return types, parameter types). Idempotent on MySQL 8.x output; corrective on MariaDB output. Modifiers downstream preserved (`bigint(20) unsigned` → `bigint unsigned`); non-integer families left alone (their `(N)` is semantic — `varchar(255)`, `decimal(10,2)`, etc.).
- **18-case `TestNormalizeMySQLIntType`** pins the regex on every integer family + modifier preservation + non-integer families untouched + idempotence on MySQL 8.x form.

### Added (Part C: RENAME COLUMN + RENAME CONSTRAINT folding; ADR-022; closes #14)

- **`applyColumnRename(t *foldedTable, oldName, newName string) bool`** in `internal/sql/migrations.go`. Mutates `columnDef.name` in place AND rewrites this-table column references inside constraint strings via `renameInFirstParens` (word-boundary regex scoped to the first parenthesized group). Returns false iff source column not found — caller emits the BOTH-chunks fallback.
- **`applyConstraintRename(t *foldedTable, oldName, newName string) bool`** in `internal/sql/migrations.go`. Walks constraint strings, finds one with a leading `CONSTRAINT <name>` prefix matching `oldName`, rewrites name in place. Anonymous constraints (no leading name) return false → BOTH-chunks fallback.
- **`renameInFirstParens(s, oldName, newName string) string`** — replaces word-boundary occurrences of `oldName` with `newName` inside the FIRST parenthesized group of `s`. Nested parens handled via `matchingParen`. `\b` anchors via Go regex; QuoteMeta'd `oldName` so identifiers with regex metachars don't break the replace.
- **New RENAME branch in `applyOneAction`** (`internal/sql/fold.go`). Dispatches to `applyRename` → `applyColumnRename` / `applyConstraintRename`. Handles `RENAME COLUMN old TO new` and `RENAME CONSTRAINT old TO new` (canonical convergent syntax across Postgres, MySQL, SQLite ≥3.25.0, MariaDB). `RENAME TO` (table rename) and MySQL `CHANGE` syntax fall through to BOTH-chunks fallback; ADR-022 documents both as known deferrals.
- **13 new tests in `internal/sql/fold_test.go`** covering all 8 enumerated edge cases from #14 + engine-syntax variants (SQLite, MySQL CHANGE) + the idempotence regression guard. The existing `TestFold15_RenameColumnOutOfScope` is renamed to `TestFold15_RenameColumnFolds` and updated to assert the new "post-rename name shows in folded chunk, no warn" behavior.

### Notes (calibration discipline)

- **Tier-1 SQL chunk fidelity vs retrieval-pipeline recall.** Part C's RENAME folding makes Tier-1 chunks contain post-RENAME column names. It does NOT improve `docs/BENCH.md`'s hybrid-retrieval recall@10 numbers (82–91%) — that's a completely different system measuring a completely different thing. Honest framing: "Tier-1 chunks now reflect post-RENAME schema names; partial fold preserved when RENAME source is missing." This framing discipline is upheld across CHANGELOG / README / ADR / DESIGN.md / commit messages.
- **Part B Tier 2 + Tier 3 deliberately not normalized.** DEFAULT-expression rendering (`'guest'` vs `guest`, `current_timestamp()` vs `CURRENT_TIMESTAMP`) and view-body parenthesization (`on((cond))` vs `on(cond)`) are documented in ADR-021 as known cosmetic differences ken doesn't normalize today. Quote-stripping risks mangling escaped values; view-body parenthesization needs a SQL parser. v0.8.x+ can extend if signal arrives.
- **Cross-table FK target-side column renames remain out of scope** for Part C's per-table rename machinery. ADR-022 documents the rationale (full migration-DAG analysis needed) + the real-world-frequency observation (FK targets are typically primary keys, rarely renamed) that makes this an acceptable v0.8.1 scope ceiling.
- **The probe-and-branch mechanism stays available as future-extension** for Part B-style engine divergences that genuinely need engine-specific code paths. v0.8.1 didn't need it (unconditional normalization was engine-agnostic-safe); future divergences in v0.9.0+ can land it then with ADR-021 as the precedent.

### Notes (backwards compatibility)

- **No new env vars for end users.** `KEN_DB_MARIADB_TEST_DSN` is CI / development-only. The polished default instructions, the integer-width normalization, and the RENAME folding all apply transparently — operators see no required config change.
- **Existing fold behavior unchanged for ADD / DROP / ALTER COLUMN TYPE / ADD / DROP CONSTRAINT.** Only the RENAME branch in `applyOneAction` changes from BOTH-chunks-fallback to fold-eagerly. Operators who relied on the v0.7.1 RENAME-as-per-file-chunk behavior (none expected; the fallback is for failure modes, not success) see the folded chunk now contains the post-rename name; the rename action is no longer emitted as a separate ALTER chunk on the happy path.
- **`KEN_SQL_NO_AUTO_MIGRATIONS=1` continues to fully disable Tier-1 migration folding.** Operators who maintain a canonical `schema/current.sql` and don't want any migration-history-folded chunks (with or without RENAME folding) set this and get v0.7.0 per-file behavior.
- **Stdout-cleanliness invariants unchanged.** All seven variants pass (stock / Postgres / SQLite / MySQL / LISTEN/NOTIFY / reindex_db / MariaDB). The pgx-tracer-must-stay-nil discipline (feedback_pgx_tracer memory) extends identically to the MariaDB code path — same driver as MySQL, same audit applies.

## [0.8.0] — 2026-05-25

The operator-control-loop release. Three features round out the database-integration story started in v0.7.0 — push-based schema change detection, agent-initiated reindex, and DB support in `mcp.Run`. **All three parts shipped; v0.8.0 ready to tag.**

**Engine scope (Part 1):** Postgres only. MySQL and SQLite log debug + no-op; their operators should continue using `KEN_DB_REINDEX_INTERVAL`.

### Added (Part 1: LISTEN/NOTIFY push notifications)

- **Postgres LISTEN/NOTIFY push-based schema change detection** (operator-provided event trigger; ken does NOT modify your database without explicit consent — run the script once). Activate with `KEN_DB_LISTEN=1` after the one-time setup: `ken-mcp print-listen-script | psql $KEN_DB_DSN`. Schema changes now propagate to ken's index within ~100ms instead of waiting for the next `KEN_DB_REINDEX_INTERVAL` tick. Closes Part 1 of [#12](https://github.com/townsendmerino/ken/issues/12). ([ADR-020](docs/internal/DECISIONS.md#adr-020-listennotify-push-based-schema-change-detection-v080-part-1))
- **`KEN_DB_LISTEN`** env var (`1` / `true` / `yes` activates; default off). Validated via the existing `envBool` helper. Non-Postgres DSNs log debug and silently no-op — MySQL and SQLite have no equivalent push mechanism; interval polling continues to work for them.
- **`ken-mcp print-listen-script`** CLI subcommand — emits the SQL setup script to stdout, embedded into the binary via `//go:embed` (so it's versioned with the release). Script installs a single schema-level event trigger (`ken_schema_changed_trigger`) that fires `pg_notify('ken_schema_changed', ...)` on tracked DDL (`CREATE / ALTER / DROP` for `TABLE`, `INDEX`, `VIEW`, `MATERIALIZED VIEW`, `FUNCTION`, `TRIGGER`, `TYPE`). Idempotent (`DROP IF EXISTS` + `CREATE`); safe to re-run.
- **`internal/db.Listener` type** — dedicated `pgx.Conn` separate from the introspection pool (so a long `WaitForNotification` call doesn't tie up the connection introspection needs). Exponential-backoff reconnect (100ms → 30s cap), reset on each successful re-LISTEN. Debounced notification handling (50ms window coalesces bursts into one refresh). Trigger-existence check on every (re)connect; missing trigger logs a clear warn naming the fix command and idles until the next reconnect.
- **`internal/db.ErrListenNotSupported`** — sentinel error returned by `NewListener` for non-Postgres DSNs; `cmd/ken-mcp` distinguishes this from real connection failures (debug-and-skip vs warn-and-retry).
- **`TestBinary_StdoutIsCleanJSONRPC_WithListen`** — fifth stdout-cleanliness variant (sibling of stock / Postgres / SQLite / MySQL). Confirms the listener's dedicated pgx connection doesn't leak to stdout.
- **`TestBinary_PrintListenScript_StdoutIsScript`** — confirms the subcommand short-circuits cleanly before the MCP server starts and emits only the SQL script (no startup chatter, no stderr leakage).
- **`internal/db/listen_integration_test.go`** (dbintegration build tag) — four scenarios against live Postgres: happy path (DDL → notification within ~200ms), debounce (multi-statement transaction → exactly one onNotify call), missing trigger (warn lines + no panic + idle), reconnect (backend killed → exponential backoff → re-LISTEN → fresh DDL observed).
- **CI extension.** The `test-db-integration` job now installs the event trigger via `psql -f` before running Postgres integration tests, and adds a `cmd/ken-mcp stdout audit with LISTEN/NOTIFY` step that pins the fifth stdout-cleanliness variant.

### Notes (Part 1)

- LISTEN/NOTIFY **supplements** `KEN_DB_REINDEX_INTERVAL` rather than replacing it. Both can be active; interval polling continues as defense-in-depth backstop in case the NOTIFY connection drops silently (network partition, brief reconnect window). The `Refresher`'s internal mutex serializes — a NOTIFY arriving mid-tick collapses cleanly.
- **Engine scope: Postgres only.** MySQL has no native push notifications; SQLite is in-process by design. `KEN_DB_LISTEN=1` with a non-Postgres DSN logs debug + no-op, consistent with the v0.7.2 "SQLite ignores schema filtering" pattern.
- **Setup is mandatory.** Without running the script, the listener logs a clear warn (`event trigger "ken_schema_changed_trigger" is not installed. Run: ken-mcp print-listen-script | psql $KEN_DB_DSN`) and idles. `KEN_DB_REINDEX_INTERVAL` continues to work; the listener will catch up on the next reconnect once the operator runs the script.
- **Backwards compatibility:** stock `cmd/ken-mcp` with `KEN_DB_LISTEN` unset behaves byte-identically to v0.7.2. All five stdout-cleanliness variants pass after every commit. Parts 2 and 3 (forthcoming) add additive surface only; no v0.7.x or v0.8.0 Part 1 behavior changes.

**Engine scope (Part 2):** all three engines (Postgres, MySQL, SQLite). The `reindex_db` tool is engine-agnostic — it's a thin wrapper around `Refresher.TryRefresh`. Available whenever `KEN_DB_DSN` is configured.

### Added (Part 2: `reindex_db` MCP tool)

- **`reindex_db` MCP tool** (operator-zero-config; the tool registers automatically when `KEN_DB_DSN` is set). Agents call it to refresh ken's view of the database schema mid-conversation — typically after running a migration through psql, an ORM CLI, or a separate MCP server. Returns `Reindexed in Nms.` on success, `Reindex already in progress; nothing to do.` if another refresh holds the Refresher mutex (interval ticker, SIGHUP, LISTEN/NOTIFY listener, or a prior `reindex_db` call), or `Reindex failed: <err>` on connection / introspection failure. Closes Part 2 of [#12](https://github.com/townsendmerino/ken/issues/12). ([ADR-020 Part 2](docs/internal/DECISIONS.md#part-2-agent-callable-reindex-via-reindex_db-mcp-tool-v080-part-2))
- **`internal/db.Refresher.TryRefresh`** + **`internal/db.ErrReindexInProgress`** sentinel — the fail-fast variant of `Refresh`. Uses `sync.Mutex.TryLock` so a concurrent caller sees the in-flight signal immediately instead of queuing. The existing four trigger sources (startup / `KEN_DB_REINDEX_INTERVAL` ticker / SIGHUP / LISTEN/NOTIFY listener) keep using `Refresh` — their semantics genuinely want to serialize, not skip. `TryRefresh` is specifically the fifth, agent-callable path. `Refresh` and `TryRefresh` share an internal `doRefresh` body so the introspection + swap semantics stay exactly 1:1.
- **`mcp.ReindexFunc` + `mcp.ReindexResult`** — callback shape `NewServer` uses to register `reindex_db`. Returns `ReindexResult{InProgress, Elapsed, Err}`. The result-struct shape (rather than an error sentinel) keeps the `mcp` package free of `internal/db` imports — `cmd/ken-mcp` bridges `db.ErrReindexInProgress` into `ReindexResult{InProgress: true}` via a small closure. The same callback shape will be the seam for Part 3's `mcp.Run` DBSource path.
- **`cmd/ken-mcp` rearrangement.** `wireDBTier2` now runs BEFORE `NewServer` and returns the `*db.Refresher` so the tool can be registered with the configured Reindexer. When `wireDBTier2` returns nil (no DSN, http(s) default repo, connect failure, etc.), `reindexCallback` returns nil too and `NewServer` skips `reindex_db` registration entirely — tools/list stays honest for FS-only deployments.
- **`mcp.ReindexDBArgs`** — argument-free struct (the tool takes no parameters in v0.8.0 by design). Future v0.8.x+ refinements (async return, per-engine selectors, repo selector) can extend this struct without breaking the wire format.
- **5 new unit/tool tests + 2 integration tests + sixth stdout-cleanliness variant.**
  - `TestRefresher_TryRefresh_NoContention` / `_InFlightReturnsError` / `_ReleasesOnError` (internal/db unit tests, no DB needed — use an empty SQLite temp file).
  - `TestReindexDBTool_Registered` / `_NoDB` / `_Success` / `_InProgress` / `_Error` (mcp package tool tests with caller-supplied `ReindexFunc`).
  - `TestReindexDB_IntegrationE2E` + `TestReindexDB_IntegrationInProgress` (live Postgres via `dbintegration` build tag — CREATE → reindex → assert chunk present, then ALTER → reindex → assert new column present; in-flight test fires a concurrent TryRefresh against a slow Refresh).
  - `TestBinary_StdoutIsCleanJSONRPC_WithReindexDB` (sixth sibling of the stdout-cleanliness suite — drives a `reindex_db` tool call through `sdk.CommandTransport` to catch any tool-registration-related stdout leak the existing five tests miss).

### Notes (Part 2)

- **Fail-fast on contention, not queueing.** A concurrent `reindex_db` call during an in-flight refresh (interval tick, LISTEN burst, SIGHUP, prior tool call) returns `Reindex already in progress; nothing to do.` immediately — the agent decides whether to retry, back off, or proceed with stale data. No time-based cooldown env vars; the natural cost of one in-flight refresh IS the rate limit. See ADR-020 Part 2 for the alternatives rejected (cooldown / queue / async-return / per-tool-disable env var / auto-call-from-search heuristic).
- **No new env vars.** The tool is registered whenever `KEN_DB_DSN` is set; not registered otherwise. Operators who want to disable the tool unset `KEN_DB_DSN` (no DB tier at all) or run a separate ken-mcp process for agents that need reindex separate from agents that shouldn't have it.
- **Five trigger sources now converge on `Refresher`.** Startup (once via `wireDBTier2`), `KEN_DB_REINDEX_INTERVAL` ticker, SIGHUP, LISTEN/NOTIFY listener (Part 1), and `reindex_db` tool. The first four call `Refresh(ctx)` (blocking on mutex); the fifth calls `TryRefresh(ctx)` (mutex.TryLock; returns `ErrReindexInProgress` on contention). Internal serialization is unchanged from v0.7.0.
- **`mcp.Run` does not yet support live DB.** Embedded-corpus binaries built via `mcp.Run` (v0.6.0) get no `reindex_db` tool — Part 3 of v0.8.0 lifts this by adding `mcp.Options.DBSource`.
- **Backwards compatibility:** stock `cmd/ken-mcp` with `KEN_DB_DSN` unset behaves byte-identically to v0.7.2 + v0.8.0 Part 1. `reindex_db` simply isn't in the tools list. All six stdout-cleanliness variants pass.

**Engine scope (Part 3):** SDK authors using `mcp.Run` (the v0.6.0 embedded-corpus entrypoint). All three engines (Postgres, MySQL, SQLite) supported via the same `Config.DSN` scheme routing as `cmd/ken-mcp`.

### Added (Part 3: opt-in `mcp/db` package for SDK authors)

- **`mcp/db` opt-in package** — SDK authors using `mcp.Run` can now wire Tier 2 DB support via `mcpdb.Setup(ctx, mcpdb.Config{DSN: ..., EnableListen: true, ReindexInterval: 5 * time.Minute, ...})`, which returns a `*mcpdb.Refresher` (satisfies the new `mcp.DBIntegration` interface) to pass as `opts.DB`. Activates schema introspection + LISTEN/NOTIFY (Part 1) + interval reindex + `reindex_db` MCP tool (Part 2) **AND chunk integration into the embedded search results** — DB chunks become searchable in the next `search`/`find_related` call after a successful refresh. **v0.6.0 binary-size contract preserved** — SDK authors who don't import `mcp/db` get a binary identical in dep tree to v0.7.2's `mcp.Run` use case. DB driver tree (pgx + sqlite + mysql + internal/db) is opt-in by import. Closes Part 3 of [#12](https://github.com/townsendmerino/ken/issues/12). ([ADR-020 Part 3](docs/internal/DECISIONS.md#part-3-opt-in-mcpdb-package-preserving-v060-binary-size-contract-v080-part-3))
- **`mcp.DBIntegration` interface** — the new public seam that bundles tool invocation (`TryRefresh`) and chunk integration (`Start(ctx, onExtras)`) into one interface. `*mcpdb.Refresher` implements it; mock impls in tests. Replaces Part 2's `mcp.ReindexFunc` (unreleased-branch API churn: ReindexFunc shipped to branch `v0.8.0-listen` but not to tagged main).
- **`mcp.Options.DB DBIntegration` + `mcp.Config.DB DBIntegration`** — replace Part 2's `Reindex ReindexFunc` field on both embedded-corpus (`mcp.Run`) and cache-backed (`mcp.NewServer`) paths. Conditional `reindex_db` tool registration switches from "Reindex != nil" to "DB != nil"; same wire behavior.
- **`*search.Index.WithExtraChunks([]chunk.Chunk) *Index`** — the new primitive that powers `mcp.Run`'s chunk integration. Returns a freshly-built immutable Index containing original ∪ extras chunks; receiver is unchanged. `*Index` now retains a vecs `[][]float32` field alongside the existing `model *embed.StaticModel` so the rebuild path can re-embed extras under hybrid/semantic mode without re-encoding the original corpus. `WatchedIndex.SetExtraChunks` (cmd/ken-mcp's fsnotify-rooted in-place mutation path) is unaffected — the asymmetry (`Set` on `WatchedIndex`, `With` on `Index`) reflects the different mutation models.
- **`internal/db.SetupTier2`** — pure-Tier-2-mechanics lifecycle orchestration extracted into `internal/db`. Both `cmd/ken-mcp`'s `wireDBTier2` (env-var-driven CLI wiring) and `mcp/db.Setup` (config-struct-driven SDK author wiring) now share one implementation: same initial IndexSchema + Refresher + interval-ticker + LISTEN/NOTIFY listener lifecycle, different swap-target wiring. CLI-specific concerns (env vars, SIGHUP, WatchedIndex pre-warm) stay in `cmd/ken-mcp`; SDK-specific concerns (Config validation, log-only swap target) stay in `mcp/db`. One implementation, two surfaces.
- **`mcpdb.ListenNotifyScript`** — re-export of `internal/db.ListenNotifyScript` (the embedded SQL setup script for v0.8.0 Part 1 LISTEN/NOTIFY) so SDK authors building their own CLI can expose a `print-listen-script` subcommand without depending on `internal/db` directly.
- **Binary-size invariant tests** — `TestBinary_MCPPackageStaysDBFree` shells out to `go list -deps` and asserts the `mcp` package's transitive import set is free of pgx / modernc.org/sqlite / go-sql-driver/mysql / `internal/db`. Sibling test `TestBinary_MCPDBPackageBringsExpectedDeps` confirms the opt-in `mcp/db` package does bring those deps in. The pair enforces the v0.6.0 contract at CI time — a future commit accidentally adding a DB import to `mcp/` fails CI before merging.
- **9 new unit/integration tests across `internal/db`, `mcp/db`, and `mcp`:**
  - `internal/db/setup_test.go` (6 tests) — `SetupTier2` happy path, DSN-required, onSwap-required, IndexSchema-failure-returns-error, non-Postgres-with-enableListen-no-ops, interval-goroutine-exits-on-ctx-cancel.
  - `mcp/db/setup_test.go` (7 tests) — empty-DSN-nil-nil-nil, invalid-DSN-errors, happy path with v0.9.0 deferral log, non-Postgres-with-EnableListen, both-schema-lists-allow-list-wins, ListenNotifyScript re-export consistency, cleanup-exits-on-ctx-cancel.
  - `mcp/db/run_integration_test.go` (2 tests) — full SDK-author binary path via `sdk.CommandTransport`: with DSN → 3 tools + reindex_db round-trip; without DSN → 2 tools.
  - `mcp/binary_contract_test.go` (2 tests) — the v0.6.0 contract guards.

### Notes (Part 3)

- **Chunk integration is now end-to-end.** `mcp/db.Setup` runs introspection on each refresh (initial introspection on `Refresher.Start`, then interval ticks, LISTEN/NOTIFY pushes, and agent-callable `reindex_db` invocations). The chunks captured land in `mcp.Run`'s embedded `*search.Index` via the new `*search.Index.WithExtraChunks` rebuild path; `mcp.Run` holds an `atomic.Pointer[search.Index]` so search handlers see the latest snapshot on their next `.Load()`. An agent calling `reindex_db` and then `search` in the same conversation sees the post-introspection schema in the search results. (v0.8.0 Part 3's initial ship deferred this to v0.9.0; the addendum closed the gap before v0.8.0 tagged.)
- **v0.6.0 binary-size contract preserved.** SDK authors building docs-only embedded-corpus binaries continue to get the v0.6.0 small-binary dep posture — `mcp` package's import set is unchanged across v0.6.0 → v0.8.0. The contract is enforced by the `TestBinary_MCPPackageStaysDBFree` test (`go list -deps` grep on the `mcp` package's transitive set); a future commit accidentally adding a DB import to `mcp/` fails CI before merging.
- **All three v0.8.0 parts converge on one implementation.** The same `Refresher` + `TryRefresh` + `reindex_db` tool + LISTEN/NOTIFY path serves both `cmd/ken-mcp` (env-var-driven) and `mcp.Run` SDK authors (config-struct-driven). The `internal/db.SetupTier2` extraction makes the convergence load-bearing: one source of truth for the Tier-2 lifecycle, two thin wrappers (`cmd/ken-mcp/wireDBTier2` and `mcp/db.Setup`) for the surface-specific concerns.
- **`cmd/ken-mcp` refactored to use `SetupTier2`.** The `wireDBTier2` function shrunk by ~50 lines as the interval-ticker + listener wiring moved into `SetupTier2`. SIGHUP wiring stays in `cmd/ken-mcp` (it's a CLI concern, not an SDK concern).
- **Backwards compatibility:** stock `cmd/ken-mcp` behaves byte-identically to v0.8.0 Part 2 — same env vars, same log lines (the refactor preserved the `Tier 2: indexed N DB chunks into %q` line by composing it via the caller's `onSwap` wrapper). `mcp.Run` with `opts.DB == nil` (the v0.6.0 → v0.7.2 default) behaves byte-identically to v0.7.2's `mcp.Run` — same two tools (search + find_related), same dep tree. All seven stdout-cleanliness variants pass (the six from Parts 1 + 2, plus the new `mcp/db` integration binary tests which implicitly audit stdout via the JSON-RPC round-trip).

## [0.7.2] — 2026-05-25

The "complete the v0.7.x Tier-2 polish" release. MySQL engine + `KEN_DB_SCHEMAS` / `KEN_DB_EXCLUDE_SCHEMAS` schema filtering, paired in one ship because both are Tier-2 ergonomic improvements that close out the engine-completion track started in v0.7.0. After v0.7.2 the v0.7.x track is done: Postgres + SQLite + MySQL all supported, schema filtering available for the engines that need it, migration folding for Tier 1. v0.8.0 becomes the next-features release (LISTEN/NOTIFY + agent `reindex_db` tool + `mcp.Run` DB support) without engine-completion overhang.

- **Added: MySQL engine in Tier 2.** New file `internal/db/mysql.go` (sibling to `introspect.go` / `sqlite.go`). Pure Go via `github.com/go-sql-driver/mysql` — no cgo, single static binary preserved. Engine routing inside `internal/db.IndexSchema` dispatches on the DSN scheme (or the `@tcp(` / `@unix(` substring for the native go-sql-driver form). Three accepted DSN forms:
  - URL: `KEN_DB_DSN=mysql://user:pass@host:3306/db?parseTime=true`
  - native TCP: `KEN_DB_DSN=user:pass@tcp(host:3306)/db?parseTime=true`
  - native Unix socket: `KEN_DB_DSN=user:pass@unix(/var/run/mysqld/mysqld.sock)/db`

  Compatible with MySQL 5.7+, MySQL 8.x, and MariaDB 10.x+ (MariaDB documented compatible, not first-class CI-tested). `parseTime=true` is force-set internally so DATE/DATETIME/TIMESTAMP columns render cleanly in row samples; VARCHAR / CHAR / TEXT cells (which the driver returns as `[]byte` by default, unlike pgx) are converted to strings at the scan boundary. Same chunk shape as Postgres + SQLite; same `Refresher` / `SetExtraChunks` / SIGHUP machinery. Closes the engine half of [#11](https://github.com/townsendmerino/ken/issues/11). ([ADR-019](docs/internal/DECISIONS.md#adr-019-mysql-engine--schema-filtering-for-multi-schema-dev-databases))
- **Added: `KEN_DB_SCHEMAS` allow-list + `KEN_DB_EXCLUDE_SCHEMAS` deny-list.** Comma-separated schema names for Postgres + MySQL filtering. Default exclusions (`pg_catalog`, `information_schema`, `mysql`, `performance_schema`, `sys`) always apply; user's deny-list extends, doesn't replace. Both env vars set → stderr warn and allow-list wins; deny-list ignored. SQLite ignores the env vars with a debug-level log (single-schema engine). The canonical filter source is `internal/db.filterSchema`, applied per-row in each engine's introspection path. Closes the filtering half of [#11](https://github.com/townsendmerino/ken/issues/11).
- **Added: `db.Options.IncludeSchemas` + `db.Options.ExcludeSchemas`.** Library-level threading for the new env vars. Empty defaults; behavior matches v0.7.1 byte-for-byte when both empty.
- **Added: `envCommaList` helper** in `cmd/ken-mcp/env.go`. Whitespace-trimming around comma-separated env values. Used by `KEN_DB_SCHEMAS` + `KEN_DB_EXCLUDE_SCHEMAS`; would extend cleanly to any future comma-list env var.
- **Added: `TestBinary_StdoutIsCleanJSONRPC_WithMySQL`** confirms the MySQL code path keeps stdout clean for the JSON-RPC contract. Third sibling of the Postgres + SQLite variants (4 total stdout-cleanliness tests after v0.7.2). Skipped when `KEN_DB_MYSQL_TEST_DSN` is unset; CI sets it via the `mysql:8` service container.
- **Added: CI's `test-db-integration` job extended with a `mysql:8` service container** alongside the existing `postgres:16-alpine`. The job runs all three engines' integration suites (`dbintegration` build tag); SQLite still needs no container. Renamed to `ubuntu / DB integration (Postgres + SQLite + MySQL)`.
- **Changed: `envDSN` accepted-scheme allow-list.** Now `postgres://`, `postgresql://`, `sqlite://`, `sqlite3://`, `mysql://`, plus the native MySQL `user:pass@tcp(...)/db` or `user:pass@unix(/sock)/db` form (detected by `@tcp(` / `@unix(` substring on a string without a `://` prefix). Other inputs log the existing warn-and-fallback message and disable Tier 2.
- **Changed: `internal/db.IndexSchema`'s engine-dispatch helper.** New `dsnEngine(dsn)` returns `"postgres"` / `"sqlite"` / `"mysql"` / `""` — handles both URL-scheme dispatch (v0.7.1's `schemeOf` shape) AND the native-form substring detection introduced for MySQL. `schemeOf` retained for diagnostic logging on the unknown-engine error path.
- **Note: Default-exclusion schemas are inviolable in BOTH directions.** `KEN_DB_EXCLUDE_SCHEMAS=public` does NOT remove `pg_catalog` from the always-excluded list (deny-list extends, doesn't replace); `KEN_DB_SCHEMAS=pg_catalog` does NOT add `pg_catalog` back to the index (allow-list filter runs after default exclusions). Same for `mysql`, `information_schema`, `performance_schema`, `sys`. Operators who genuinely need to index system schemas should not point ken at the DB.
- **Note: SQLite Tier 2 + Postgres Tier 2 + Tier 1 migration folding (v0.7.1) are byte-identical when the new env vars are unset.** Stock `cmd/ken-mcp` with no DB env vars behaves byte-identically to v0.7.1.
- **Note: `mcp.Run` (v0.6.0 embedded-corpus library API) is unaffected.** No new `mcp.Options` fields. Live DB support there remains v0.8.0+.
- **Note: Wildcards in schema filtering (e.g. `tenant_*`) are deferred.** Multi-tenant SaaS operators can fall back to explicit `KEN_DB_SCHEMAS=tenant_001,tenant_002,...` until field signal calls for wildcard syntax. See ADR-019's alternatives audit for the rationale.

### New dependencies

- **`github.com/go-sql-driver/mysql` v1.10.0** — standard pure-Go MySQL driver (no cgo). Package-level logger writes to stderr by audit (`log.New(os.Stderr, "[mysql] ", ...)`); no protocol logging to stdout. Audited via `TestBinary_StdoutIsCleanJSONRPC_WithMySQL`. The pgx-tracer-must-stay-nil discipline extends identically: any future wiring of `mysql.SetLogger` routes through `Options.LogWriter` (stderr), never stdout.

Backwards compatibility: stock `cmd/ken-mcp` with no DB env vars behaves byte-identically to v0.7.1. All four `TestBinary_StdoutIsCleanJSONRPC*` variants (stock, Postgres, SQLite, MySQL) pass after every commit. The `envDSN` allow-list extension is additive — every v0.7.1 valid DSN continues to parse.

## [0.7.1] — 2026-05-25

The "make SQLite-based dev workflows great" release. SQLite support in Tier 2 + migration-history folding in Tier 1, paired in one ship because the migration-driven workflows on SQLite are exactly where the v0.7.0 per-file chunk explosion hurt most.

- **Added: SQLite engine in Tier 2.** New file `internal/db/sqlite.go` (sibling to `introspect.go`). Pure Go via `modernc.org/sqlite` — no cgo, single static binary preserved. Engine routing inside `internal/db.IndexSchema` dispatches on the DSN scheme. `KEN_DB_DSN=sqlite:///abs/path.db` or `KEN_DB_DSN=sqlite://./rel/path.db` (relative to `KEN_MCP_DEFAULT_REPO`) activates introspection. Both `sqlite://` and `sqlite3://` schemes accepted. Same chunk shape as Postgres; same `Refresher` / `SetExtraChunks` / SIGHUP machinery. Closes the engine half of [#9](https://github.com/townsendmerino/ken/issues/9). ([ADR-018](docs/internal/DECISIONS.md#adr-018-sqlite-engine--migration-history-folding-via-lightweight-alter-replay))
- **Added: Tier-1 migration-history folding.** New `internal/sql.FoldMigrations` + `internal/sql.IsMigrationDir`. When `internal/search`'s walker detects a directory of numbered `.sql` files (Goose / dbmate / Rails-4 `\d+_*.sql`, Flyway `V\d+__*.sql`, Rails-5 / Alembic `\d{14}_*.sql`), it folds CREATE TABLE + later ALTER TABLE statements into a single "current state" chunk per table. Covers `ADD COLUMN`, `DROP COLUMN`, `ALTER COLUMN ... TYPE`, `ADD CONSTRAINT`, `DROP CONSTRAINT`. Closes the folding half of [#9](https://github.com/townsendmerino/ken/issues/9).
- **Added: `KEN_SQL_NO_AUTO_MIGRATIONS`** env var (`1` / `true` / `yes` to disable). Restores v0.7.0 per-file behavior for operators who maintain a canonical `schema/current.sql` and don't want migration history surfaced separately. Default: folding enabled.
- **Added: `search.FSOptions` + `search.FromFSWithOptions` + `search.NewWatchedIndexWithOptions`.** Threading point for the migration-folding opt-out plus a logger writer for fold-skip diagnostics. The zero value matches the v0.7.0 default exactly, so existing `FromFS` / `FromPath` / `NewWatchedIndex` callers (including all the bench harnesses and CLI sub-commands) get folding transparently without source changes.
- **Added: `TestBinary_StdoutIsCleanJSONRPC_WithSQLite`** confirms the SQLite code path keeps stdout clean for the JSON-RPC contract. Sibling of v0.7.0's `_WithDB`; runs in the default `go test ./...` (no service container, fixture .db materialized at test runtime).
- **Added: CI matrix.** The `test-db-integration` job now runs SQLite tests alongside Postgres (same `dbintegration` build tag, no new service container needed). Renamed to `ubuntu / DB integration (Postgres + SQLite)`.
- **Changed: `envDSN` accepted-scheme allow-list.** Now `postgres://`, `postgresql://`, `sqlite://`, `sqlite3://`. Other schemes log the existing warn-and-fallback message and disable Tier 2. Pattern extends cleanly for MySQL in v0.7.2.
- **Changed: `db.Options` gains `DefaultRepoPath`.** Used by the SQLite engine to anchor relative DSN paths (`sqlite://./dev.db` → join(defaultRepo, "dev.db")). Postgres ignores it.
- **Note: Partial-fold failures emit BOTH chunks.** When an ALTER can't be applied cleanly (unknown column, type conflict, missing CREATE TABLE for the referenced ALTER), ken keeps the original per-file ALTER chunk in the output AND emits a folded chunk for what could be resolved. Net: the agent sees the union; never less than v0.7.0.
- **Note: `mcp.Run` (v0.6.0 embedded-corpus library API) is unaffected.** No DB code added. Tier 1's migration folding DOES benefit `mcp.Run` if embedded `.sql` files happen to be in a migration directory — filesystem-based, not DB-based.

### New dependencies

- **`modernc.org/sqlite` v1.50.1** — pure-Go SQLite driver (cgo-free, transpiled from C). Default behavior is silent — no protocol logging to stdout. Audited via `TestBinary_StdoutIsCleanJSONRPC_WithSQLite`.

Backwards compatibility: stock `cmd/ken-mcp` with no DB env vars and no migration directories behaves byte-identically to v0.7.0. `TestBinary_StdoutIsCleanJSONRPC` (Postgres + the v0.7.0 stock variant) continues to pass.

## [0.7.0] — 2026-05-25

The database-schema indexing release. Two tiers, both shipping together:

1. **Tier 1 — Static SQL parsing.** ken now parses `.sql` files in the
   corpus (`CREATE TABLE` / `INDEX` / `VIEW`, `ALTER TABLE`) and emits
   one denormalized "for retrieval" chunk per database object. Activates
   automatically when `.sql` files are present. No opt-in, no new env
   var; the structural chunks are additive to the regular file
   chunking, so existing BM25 hits on raw SQL still work.
2. **Tier 2 — Live Postgres introspection.** When `KEN_DB_DSN` is set,
   ken introspects via `information_schema` / `pg_catalog` and emits
   one chunk per table / view / index / function. Every chunk carries
   a freshness header (`-- indexed at <UTC> from postgres@<host>`); no
   credentials in chunk text. Postgres only for v0.7.0; MySQL +
   SQLite are planned. Closes [#8](https://github.com/townsendmerino/ken/issues/8).

Design rationale, alternatives considered (column-exclusion DSL,
per-call introspection, LISTEN/NOTIFY, agent-triggerable reindex tool),
and the PII stance are in
[ADR-017](docs/internal/DECISIONS.md#adr-017-database-schema-indexing--two-tier-static-sql--live-postgres-with-documented-pii-stance).

### Added

- **`internal/sql` package** — pure-Go parser for the DDL subset above.
  Statement splitter is aware of line/block/nested comments,
  single/double quotes, Postgres dollar-quoting (`$$ ... $$` and
  `$tag$ ... $tag$`), and paren depth. Wired into `internal/search`'s
  chunk dispatch so the build-once path (`FromFS`) and the watch path
  (`WatchedIndex.appendFile`) both emit structural SQL chunks for
  `.sql` files. 14 unit-test scenarios pin every shape including
  malformed statements, dollar-quoting, multi-comma column lists, and
  empty/DML-only files. Exports `IsSQLFile(path) bool` and
  `ParseFile(path, content, logger) ([]chunk.Chunk, error)`.

- **`internal/db` package** — Postgres introspection via
  `github.com/jackc/pgx/v5`. `IndexSchema(ctx, opts) ([]chunk.Chunk, error)`
  is the build-once entry; `Refresher` (with `Run(ctx)` periodic + `Refresh(ctx)`
  manual) orchestrates the three reindex layers. Internal serialization
  via mutex makes concurrent triggers safe to spam. Chunk emission
  shares the denormalized "for retrieval" shape with Tier 1 (header
  line + `TABLE name` + columns + indexes + `FK referenced by:` reverse
  navigation + optional sample rows). Three integration tests gated
  by build tag `dbintegration` and `KEN_DB_TEST_DSN` env var.

- **`WatchedIndex.SetExtraChunks(chunks)`** in `internal/search` — the
  composition seam Tier 2 uses to inject DB chunks into the published
  Index without disturbing the FS chunks the fsnotify watch path
  manages. The published snapshot is always FS-chunks ∪ extras; both
  sources update it via the same ADR-012 atomic-swap path. Calling
  with `nil` clears the extras ("DB unreachable, keep serving FS").

- **`KEN_DB_DSN`** env var — Postgres connection string. Empty (default)
  keeps Tier 2 off. Format must be parseable URL (`postgres://` or
  `postgresql://`). Invalid scheme / missing host / unparseable form
  logs a stderr warning and disables Tier 2 rather than crashing.

- **`KEN_DB_SAMPLE_ROWS`** env var — rows-per-table to sample (default
  0 = schema only). When > 0, ken pulls N rows per table deterministically
  (`ORDER BY` first PK column; fallback `ORDER BY 1`) and appends them
  to the table's chunk. Long cells truncated at 80 chars with `…`.

  > Intended for development databases. Do not point this at production
  > data — sample rows are sent to the agent as part of search results
  > and thus to your LLM provider. See the README and ADR-017 for the
  > PII stance.

- **`KEN_DB_REINDEX_INTERVAL`** env var — Go duration string (e.g.
  `5m`, `1h`) for periodic DB refresh. Empty/zero (default) means no
  periodic polling — refresh only at startup or via `SIGHUP`. Tick-time
  failures log a warn and don't kill the goroutine.

- **`SIGHUP` handler** in `cmd/ken-mcp` (unix-only; no-op on Windows).
  Each `SIGHUP` triggers `Refresher.Refresh`, which rebuilds the DB
  chunks and atomically swaps them in. Standard `migrate-up` ergonomics:
  ```
  migrate-up:
      psql -f migrations/$$NEXT.sql
      kill -HUP $$(pgrep ken-mcp)
  ```

- **New env helpers** in `cmd/ken-mcp/env.go`: `envDuration` (parses Go
  duration strings) and `envDSN` (validates `postgres://` URL form
  without logging the raw DSN). Both follow the existing ADR-009
  warn-and-fallback pattern; 17 new env-helper subtests pin the
  behavior.

- **CI Postgres service container** — a new `test-db-integration` job
  on `ubuntu-latest` spins up `postgres:16-alpine` and runs the
  dbintegration tests plus `TestBinary_StdoutIsCleanJSONRPC_WithDB`
  against it. macos-latest runs the default `go test ./...` which
  skips the dbintegration tests cleanly via the build tag.

### Changed

- **`internal/search/index.go`** and **`internal/search/watch.go`** now
  route every `.sql` file through `sql.ParseFile` in addition to the
  configured chunker via the new `chunkOneFile` helper. The structural
  chunks join the index alongside the line-chunked file bytes; both
  forms are queryable.

- **`internal/search/WatchedIndex`** gains an `extraChunks` slot
  alongside `chunks` for orchestrator-injected non-FS chunks. The
  published Index is built from `chunks ∪ extraChunks` on every flush;
  `compactCorpus` only touches `chunks` so DB extras survive
  fsnotify-driven snapshot republishes.

- **`cmd/ken-mcp` startup** gains the conditional `wireDBTier2` block.
  Behavior with no `KEN_DB_DSN` set is byte-identical to v0.6.0; the
  existing `TestBinary_StdoutIsCleanJSONRPC` test confirms this and a
  new `TestBinary_StdoutIsCleanJSONRPC_WithDB` test confirms the full
  Tier-2 code path (DSN parse → connect → IndexSchema → SetExtraChunks
  → Refresher → SIGHUP handler) doesn't leak anything to stdout when
  enabled.

### Dependencies

- **`github.com/jackc/pgx/v5` v5.9.2** — pure-Go Postgres driver. Default
  `Tracer` is nil; no protocol logging to stdout.

### Notes

- **`mcp.Run` (v0.6.0 embedded-corpus library API) is unaffected.** DB
  support there is planned for v0.8.0+ via `mcp.Options.DBSource` or
  similar; no `mcp.Options` changes in v0.7.0. Tier 1's static SQL
  parsing DOES benefit `mcp.Run` because it lives at the
  `internal/search` layer — filesystem-based, not DB-based.
- **PII stance is documentation + sane defaults.** Schema-only is the
  default. The opt-in sampling env var is unambiguous. Freshness
  metadata in every chunk surfaces provenance. No engineered redaction
  controls — operators who need those should not point ken at the DB.
- **DB chunks attach to the default repo only.** `KEN_DB_DSN` requires
  `KEN_MCP_DEFAULT_REPO` (and that the default repo is a local path,
  not an http(s) URL). When unset, Tier 2 logs a warn and stays off.
- **Migration-history folding is out of scope** for v0.7.0. CREATE
  TABLE + later ALTER TABLE statements across files emit separate
  chunks; agents see the union of historical state. Documented in
  ADR-017 alternatives as a future refinement.

## [0.6.0] — 2026-05-24

The embedded-corpus release. The library form of ken-mcp lands: SDK
authors can `//go:embed` their docs + the Model2Vec model and ship a
single static MCP server binary with high-quality local retrieval, no
backend, no per-query network egress, and structural agent sandboxing
(no path resolution → no path-traversal escape). Closes
[#7](https://github.com/townsendmerino/ken/issues/7); design rationale
in [ADR-016](docs/internal/DECISIONS.md#adr-016-embedded-corpus-mcp-build-pattern-via-mcprun-library-function).

The stock `cmd/ken-mcp` binary is **unchanged**: same env vars, same
tool surface, same watch mode, same multi-repo path/URL resolution.
v0.6.0 is purely additive — the new pattern coexists with the existing one
rather than replacing it.

### Added

- **`github.com/townsendmerino/ken/mcp.Run(ctx, fsys, opts) error`** —
  public library API for the embedded-corpus build pattern. Serves
  `search` and `find_related` over a single fixed `fs.FS` corpus,
  blocking until ctx is canceled or the client closes the transport.
  Tool wire format and arg schemas are identical to `cmd/ken-mcp`'s,
  so agents trained against semble's or ken's MCP server work
  unchanged. `Options.ModelFS` lets the Model2Vec snapshot live in an
  `fs.FS` (typically `//go:embed model/*`) rather than on disk;
  `Options.ModelDir` is the path-based alternative. Typoed enum
  values (`Mode`, `ChunkerName`, `LogLevel`) log a stderr warning and
  fall back to documented defaults — same ADR-009 contract `cmd/ken-mcp`
  uses for env vars.
- **`cmd/ken-mcp-docs`** — worked example: a 20-line `main.go` baking
  ken's own `docs/*.md` and the Model2Vec model into a single static
  74 MB binary. Builds via `scripts/build-docs-mcp.sh` (which stages
  the model + docs into the package's directory then runs `go build
  -tags=embed_corpus`). Gated by the `embed_corpus` build tag so a
  fresh clone — where the embed dirs don't yet exist — still builds
  cleanly via `go build ./...`.
- **`internal/chunk/markdown` chunker** — heading-aware boundaries
  (ATX + setext), atomic fenced-code / tables / lists, frontmatter
  handling (YAML `---` and TOML `+++`), byte-fidelity preserved.
  Registers as `"markdown"` in the chunker registry. Auto-falls back
  to the line chunker for non-markdown files in mixed-content corpora.
  No new third-party deps — handwritten pure-Go scanner.
- **`embed.LoadFromFS(fs.FS, dir) (*StaticModel, error)`** — canonical
  entry point for loading a Model2Vec snapshot from any `fs.FS`. Same
  for the helpers it composes: `LoadTokenizerFromFS` and
  `OpenSafetensorsFromFS`. The disk-path counterparts (`Load`,
  `LoadTokenizer`, `OpenSafetensors`) become thin wrappers — `Load`
  is now formally deprecated, the others remain useful for callers
  reading individual files.
- **`internal/search.FromFSWithModel(fsys, mode, chunkerName, model)`** —
  index-build entry point that takes a pre-loaded model (rather than
  resolving a `modelDir` internally). Used by `mcp.Run` so the model
  can come from `Options.ModelFS` instead of a path.
- **`mcp.Logger`, `mcp.LogLevel`, `mcp.ParseLogLevel`, `mcp.LogLevelNames`,
  `mcp.ValidateEnum`** — the leveled logger and validation helper
  moved out of `cmd/ken-mcp` into the `mcp` package so both the
  Cache-backed server and `mcp.Run` share one logger type. Stdout is
  never written from any of these — the JSON-RPC contract is enforced.

### Changed

- **Side-effect chunker imports moved to binaries.** Previously
  `internal/search/index.go` blank-imported both `regex` and
  `treesitter`, which meant any program transitively importing
  `search` pulled in `gotreesitter/grammars` and its 19 MB
  `//go:embed grammar_blobs/*.bin` payload — the Go linker cannot
  dead-code-eliminate `embed.FS` contents. `internal/search` now only
  blank-imports `regex` (the universal default); `cmd/ken` and
  `cmd/ken-mcp` add `treesitter` and `markdown` explicitly. The stock
  binaries' chunker availability is unchanged. `cmd/ken-mcp-docs`
  imports only `markdown`, which is how the 19 MB grammar bundle
  stays out of it.
- **`cmd/ken-mcp` uses `mcp.Logger`** instead of its old internal
  `leveledLogger`. Visible log format is unchanged. `env.go`'s
  `envEnum` is now a thin wrapper around `mcp.ValidateEnum` so the
  validation message format stays consistent between env-var and
  Options-field validation.

### Deprecated

- **`embed.Load(modelDir)`** — use `LoadFromFS(os.DirFS(modelDir), ".")`
  instead. The wrapper still works; will be removed in a future minor
  release (pre-1.0 semver permits this).

### Binary sizes (built 2026-05-24, darwin/arm64)

| Binary | Size | Notes |
|---|---|---|
| `bin/ken` | 36 MB | CLI; includes all chunkers |
| `bin/ken-mcp` | 42 MB | Stock MCP server; includes all chunkers + 19 MB grammar bundle |
| `bin/ken-mcp-docs` | 74 MB | Demo embedded-corpus server; no grammar bundle but +62 MB embedded model + 144 KB embedded docs |

## [0.5.0] — 2026-05-25

The library-API + monorepo-correctness release. Two changes bundled
into one tag:

1. **`fs.FS` is the canonical walker/indexer surface** — ken can now
   index any `fs.FS` (`embed.FS`, `fstest.MapFS`, tarball-backed, git
   tree object, in-memory snapshot) — not just a directory on disk.
   Unlocks agent sandboxing (`ken-mcp` over a chroot-y `fs.FS` view,
   no syscall-level escape) and offline analysis (index a tarball
   without unpacking). Prompted by an r/golang commenter on the v0.4
   release post; tracked in [#6](https://github.com/townsendmerino/ken/issues/6).

2. **Nested `.gitignore` support** — the walker now reads `.gitignore`
   files in every directory, not just the root, matching git's
   behavior. Field-driven: a gobe monorepo user reported `node_modules/`
   polluting their results because their exclusion was in per-package
   `.gitignore` files. Tracked in [#5](https://github.com/townsendmerino/ken/issues/5).

(Date is a placeholder — set on tag day.)

### Added

- **`repo.WalkFS(fs.FS, Options)` and `search.FromFS(fs.FS, Mode, …)`** —
  the canonical filesystem surface as of this release. Either one
  accepts any `fs.FS` implementation. Parity tests (`Walk` vs `WalkFS`,
  `FromPath` vs `FromFS`) pin that the `os.DirFS`-wrapped behavior
  matches the historical real-FS implementation byte-for-byte. Zero
  new deps; `fs` and `testing/fstest` are stdlib. Closes [#6](https://github.com/townsendmerino/ken/issues/6).

- **Nested `.gitignore` support.** The walker now respects `.gitignore`
  files in every directory, not just the root, matching git's behavior:
  outer scopes evaluate first, inner scopes last, last-match-wins
  across the union; inner scopes can both add new ignores and
  re-include via `!pattern`. Implemented as a per-directory scope
  stack on top of the existing handwritten rule engine — no new
  dependency, no swap to `github.com/sabhiram/go-gitignore` (kept as
  a documented future option for edge-case pathspec parity inside the
  rule engine itself; see [ADR-015](docs/internal/DECISIONS.md#adr-015-nested-gitignore-support-via-scope-stack-on-existing-rule-engine)).
  `Matcher` (the watch-path seam) gains the same nested awareness via
  a one-shot tree walk at construction. Closes [#5](https://github.com/townsendmerino/ken/issues/5).

### Deprecated

- **`repo.Walk(opts)` and `search.FromPath(root, …)`** are now thin
  one-line wrappers around their `FS` siblings (`WalkFS(os.DirFS(opts.Root), opts)` /
  `FromFS(os.DirFS(root), …)`). They remain functional and tested but
  are marked `// Deprecated:` in source; removal is scheduled for a
  future minor release. Pre-1.0 semver permits this.

### Fixed

- **`Matcher.ShouldIndex` now performs an ancestor walk** so directory-prune
  rules (like `build/`) correctly exclude files inside them. Surfaced while
  wiring nested `.gitignore` through the watch path: `WalkFS` had always
  gotten this right via `fs.SkipDir`, but `Matcher` (used by `--watch`)
  answered paths directly with no walk, so a `build/` rule was silently
  failing to exclude `build/x.txt` when queried. Anyone running
  `ken index --watch` against a repo with dir-only ignore rules may have
  been bitten by this without noticing — the watcher would re-add files
  inside ignored directories on every event.

### Note

- **`--watch` (fsnotify) remains real-FS-only by construction**, so
  `fs.FS`-backed indexes are build-once. fsnotify itself doesn't
  abstract over `fs.FS` (the kernel APIs it wraps are real-FS-only),
  and neither sandboxing nor offline analysis needs incremental
  reindex. Same goes for `repo.Matcher` (used only by the watch path).
- **`ken-mcp` env-var config stays path-based for v0.5.0.** An
  MCP-side `fs.FS` integration (sandboxed-FS-only mode, exposed via
  new config) is a future change tracked separately.
- **`.gitignore` freshness during watch.** `Matcher` collects every
  `.gitignore` once at construction. Files added, modified, or removed
  after the watcher starts are NOT picked up — a full re-index
  (restart `ken index`) is required. Tracked for a future release.

See [ADR-014](docs/internal/DECISIONS.md#adr-014-fsfs-as-canonical-walkerindexer-surface)
and [ADR-015](docs/internal/DECISIONS.md#adr-015-nested-gitignore-support-via-scope-stack-on-existing-rule-engine)
for the alternatives considered and the full consequences lists.

## [0.4.0] — 2026-05-21

The release that closes the gap between ken's pure-Go claim and its
actual end-user experience. A user can now `go install` ken, run
`ken download-model`, and reach hybrid retrieval without ever
touching a Python interpreter — the "single static binary"
positioning is finally 100% true end-to-end, not 80%-with-Python-
bootstrap. Agent-side routing also gets honest: the MCP tool
instructions now name the recall/token tradeoff explicitly so AI
agents know to fall back to grep for exhaustive enumeration. Plus
incremental-indexing memory hygiene (tombstone compaction) and the
disciplined close of ADR-013 after the validation precondition
caught a misread of the CSN-Python benchmark.

### Added

- **`ken download-model`** — pure-Go fetch of the three Model2Vec files
  (`model.safetensors`, `tokenizer.json`, `config.json`) directly from
  HuggingFace's CDN. Closes the gap between ken's "single static binary"
  claim and the previous Quickstart's `huggingface-cli` dependency.
  Defaults to `minishlab/potion-code-16M` into `~/.ken/model`; pass
  `--model ORG/NAME` / `--to DIR` to override; `--force` re-downloads
  files already on disk. Public models only — gated/private models
  still need `huggingface-cli`. Atomic-rename writes so a cancelled
  download leaves no partial files.

### Changed

- **Model directory resolution.** The CLI now searches a priority list
  for the Model2Vec snapshot — `--model <DIR>` → `$KEN_MODEL_DIR` →
  `~/.ken/model` (canonical end-user location) → `./testdata/model`
  (repo-developer fallback) — instead of defaulting to a single
  repo-relative `testdata/model` path. A user who `go install`ed ken
  and ran `ken download-model` now gets working hybrid/semantic search
  out of the box; repo developers can either follow the same public
  convention or pass `--model testdata/model` for local-only iteration.
  The "model not found" error continues to point at whichever path
  was tried and includes the exact `ken download-model --to <path>`
  command to resolve it.

- **ken-mcp tool instructions surface the recall/token tradeoff.** The
  `Instructions` string every MCP agent reads on startup gained a
  sentence directing exhaustive-enumeration tasks (every callsite,
  pre-rename audits) at grep instead of ken — ken caps at ~82–91%
  recall at K=10 and isn't built for that. Mirrors the "honest
  tradeoff" framing already in `README.md`, grounded in the
  token-budget bench's measured ceilings. Closes the gap where agents
  were told "prefer ken over grep" without naming the exception.

- **Tombstone compaction.** Watched indexes now drop tombstoned chunks
  during every debounced snapshot rebuild instead of accumulating
  them. Memory plateaus at live-chunk working-set size; multi-day
  agent sessions on actively-edited corpora no longer grow unbounded.
  No user-visible behavior change; query results, `ResolveChunk`, and
  `FindRelated` are unchanged. The `OnFlush` message format gains an
  optional `(compacted N tombstones)` suffix that's emitted only when
  N > 0, so pure-write flushes keep the v0.3.0 format. Closes the
  v0.3.x compaction trigger named in
  [`docs/internal/DECISIONS.md` ADR-012](docs/internal/DECISIONS.md#adr-012-incremental-indexing-via-fsnotify--atomic-snapshot-swap).

### Documentation

- ADR-013 closed as **Deprecated** (Proposed → Deprecated). Prompt 22's
  precondition inspection of the CoIR-CSN-Python dataset revealed the
  motivating empirical anchor was misread: CSN queries are full Python
  function sources, not English docstrings; documents are docstrings
  extracted from those same functions, so BM25's win on this benchmark
  is a substring-leak artifact of dataset reframing, not a query-class
  signal an α-routing lever could exploit. `docs/BENCH.md`,
  `docs/DESIGN.md`, `docs/internal/DECISIONS.md`, and `README.md` corrected
  correspondingly. The CSN-Python NDCG and token-budget numbers
  themselves are unchanged; only the causal explanation shifts. See
  [`docs/internal/DECISIONS.md` ADR-013](docs/internal/DECISIONS.md#adr-013-corpus-adaptive-α--adding-a-third-query-class-branch).

## [0.3.0] — 2026-05-21

Two stories: incremental indexing, and the first measurement of ken's
agent-input-cost claim. `ken-mcp` now watches the indexed repo and
re-publishes a snapshot 2s after any edit, so an agent editing files
mid-session sees its own edits without a restart; `ken index` defaults
to the same watch mode. Separately, a new token-budget benchmark
quantifies how many agent-input tokens ken needs to recall the target
chunk vs the grep+Read baseline — the headline ratios are in the
README; methodology and per-query-class numbers are in
[`docs/BENCH.md`](docs/BENCH.md#token-budget-recall--agent-side-efficiency).

### Added

- **Incremental indexing.** `WatchedIndex`
  ([`internal/search/watch.go`](internal/search/watch.go)) wraps the
  immutable `Index` with an fsnotify watcher, a 2s edit-burst debounce,
  and an `atomic.Pointer[Index]` snapshot swap. Reader path is one
  atomic load per query and never blocks; deletes are tombstones (no
  compaction in v0.3, monotonic-growth is bounded by edit volume).
  Design rationale in [`docs/internal/DECISIONS.md` ADR-012](docs/internal/DECISIONS.md#adr-012-incremental-indexing-via-fsnotify--atomic-snapshot-swap).
- `ken index --watch` / `--no-watch` flags. `--watch` is the default in
  v0.3+; `--no-watch` restores the v0.2 build-once-and-exit behavior for
  batch / CI / huge-corpus scenarios.
- `ken-mcp` always watches the indexed repo; cache entries refresh on
  file change. No env var to disable in v0.3.
- `WatchedIndex.SetOnFlush(func(msg string))` — per-flush callback so
  `ken index --watch` can emit interactive "re-indexed N files" lines
  on stderr without `internal/search` taking a dependency on a logger.
- **External-reference NDCG benchmark** on
  [CoIR-CSN-Python](https://github.com/CoIR-team/coir). 1000-query
  subsample, regex chunker: BM25 0.8743 / hybrid 0.7839 / semantic
  0.7405. Independent of semble's bench; published code-IR baselines
  give readers a comparable anchor.
  [`docs/BENCH.md`](docs/BENCH.md#external-benchmark--coir-csn-python).
- **Token-budget recall benchmark** (`bench/tokens/`, build tag
  `bench`). First measurement of agent-input token cost at fixed
  recall@10 vs a grep+Read baseline. cl100k_base tokenization via
  `pkoukk/tiktoken-go` as a universal LLM-input-cost proxy. The
  `bench` tag keeps the tiktoken-go dep out of released binaries.
  [`docs/BENCH.md`](docs/BENCH.md#token-budget-recall--agent-side-efficiency).
- `mcp.FormatResults` exported so the token-budget bench measures
  tokens against the same wire format ken-mcp emits, not a separate
  formatter.

### Changed

- README opening reframed: the two-axis value claim (runtime properties
  + measured agent-input efficiency) replaces the previous "runtime
  properties, not retrieval quality" framing now that the
  token-budget bench quantifies the agent-side win.
- [`internal/ann/flat.go`](internal/ann/flat.go) package comment
  updated to reflect the atomic-snapshot pattern (Flat stays immutable;
  new Flats are built per snapshot under v0.3 incremental indexing)
  instead of the previous "would require a lock" caveat.

### Fixed

- `THIRD_PARTY_LICENSES.md` regenerated to include `fsnotify v1.10.1`
  (BSD-3-Clause, new v0.3 dep) and `gotreesitter v0.18.0` (MIT, v0.2
  dep that was previously missing). Attribution chain now matches the
  binaries actually shipped.
- Doc-currency sweep ahead of the tag: `KEN_MCP_CHUNKER` enum in
  `docs/DESIGN.md` now correctly lists `regex/treesitter/line` (was
  `regex/line`); incremental-indexing subsection re-tensed to past
  with ADR-012 cross-reference; `internal/search/watch.go` added to
  the project layout; Sources updated (wazero relabeled "superseded by
  gotreesitter per ADR-010").
- `CLAUDE.md` parity-harness claim updated: the 11,447-input
  `transformers.AutoTokenizer` parity test is done with zero drift;
  `CLAUDE.md` previously said it was "still owed."

### Dependencies

- Added: `github.com/fsnotify/fsnotify v1.10.1` (runtime, BSD-3-Clause).
- Added: `github.com/pkoukk/tiktoken-go` (bench-only, MIT).

## [0.2.0] — 2026-05-20

Tree-sitter chunker lands (opt-in), the BM25 tokenizer is rewritten as
a verbatim port of semble's `tokens.py`, and every `KEN_MCP_*` env var
is validated at startup instead of silently falling through. The
chunker default stays `regex` — the per-language NDCG swap between
regex and tree-sitter is within bench noise overall, with wins on
Kotlin/Zig/TypeScript/Java/PHP and losses on Python/C/Rust/Lua/Scala.

### Added

- **Tree-sitter chunker** via [`gotreesitter`](https://github.com/odvcencio/gotreesitter)
  ([`internal/chunk/treesitter/`](internal/chunk/treesitter/)), opt-in
  via `--chunker=treesitter` or `KEN_MCP_CHUNKER=treesitter`. Runs cAST
  split-then-merge from [arXiv 2506.15655](https://arxiv.org/html/2506.15655).
  206 grammars embedded. [`docs/internal/DECISIONS.md` ADR-010](docs/internal/DECISIONS.md#adr-010-pure-go-tree-sitter-via-gotreesitter).
- "Choosing a chunker" subsection in the README — per-language
  recommendation table based on the full 63-repo bench.
- "How this was built" notice on the README — frames the human/AI
  division of labor honestly up-front so readers aren't left guessing.
- "Tuning ken's routing for your repo" README subsection — guidance
  for agents on when to prefer ken vs grep.
- 11,447-input tokenizer parity harness (`scripts/parity_dump.py` +
  `internal/embed/parity_test.go` under `-tags=parity`). Zero drift
  across normalize / pre_tokenize / wordpiece / other on first run;
  surfaced and fixed three real tokenizer bugs the 18-case golden
  fixture had missed.
- `KEN_MCP_*` env-var validation: every typoed enum / non-integer /
  bad path now logs a stderr warning and falls back to the documented
  default, replacing the silent-fallthrough mode where `Atoi("of")`
  returned 0 and disabled the cache. [`docs/internal/DECISIONS.md` ADR-009](docs/internal/DECISIONS.md#adr-009-env-var-validation-instead-of-silent-fallthrough).
- `regen_golden.sh` — idempotent helper that bootstraps `.venv/`,
  pip-installs the Python reference deps, regenerates the embedding
  golden fixture, prints a sanity summary.
- Dependabot config covering Go modules and GitHub Actions.

### Changed

- **BM25 tokenizer** rewritten as a verbatim port of semble's
  `tokens.py` — snake-case compound preservation, ASCII-only run
  extraction matching `_TOKEN_RE`, compound-first emission order.
  Moved hybrid NDCG +0.002 and BM25-raw +0.002 on semble's bench.
  [`docs/internal/DECISIONS.md` ADR-008](docs/internal/DECISIONS.md#adr-008-bm25-tokenizer-as-verbatim-port-of-sembles-tokenspy).
- C# and bash grammars in the tree-sitter chunker route through the
  line chunker (C# OOMs on real-world files at ~1.7 GB RSS; bash is
  pathologically slow on real bash-it content). Auto-fallback;
  documented in the README "Choosing a chunker" notes.

### Decided (no code change)

- **Default chunker stays `regex` in v0.2.0**; tree-sitter is opt-in.
  Net NDCG difference is within bench noise (0.838 vs 0.842, Δ −0.004);
  per-language wins on Kotlin/Zig/TypeScript/Java/PHP, losses on
  Python/C/Rust/Lua/Scala. [`docs/internal/DECISIONS.md` ADR-011](docs/internal/DECISIONS.md#adr-011-default-chunker-stays-regex-in-v020-treesitter-is-opt-in).

### Dependencies

- Added: `github.com/odvcencio/gotreesitter v0.18.0` (MIT).

## [0.1.0] — 2026-05-19

Initial public release. End-to-end pure-Go, no-cgo port of semble's
hybrid code-retrieval pipeline — walk → chunk → BM25 + Model2Vec
semantic → α-weighted RRF fusion → rerank → penalties — with an MCP
server speaking the same `search` / `find_related` schema and wire
format as semble's, so existing semble-trained agents work unchanged.

### Added

- Pure-Go, no-cgo port of semble's hybrid retrieval pipeline. Single
  static binary; `GOOS`/`GOARCH` cross-compiles for free.
  [`docs/internal/DECISIONS.md` ADR-001](docs/internal/DECISIONS.md#adr-001-pure-go-no-cgo).
- [`internal/repo/walk.go`](internal/repo/walk.go) — gitignore-respecting
  filesystem walk (common-subset matcher), with binary-file (NUL sniff)
  and oversized-file skips.
- [`internal/chunk/`](internal/chunk/) — `Chunker` interface (registry
  via `database/sql`-style blank imports to avoid an import cycle).
  Line chunker (50-line / 5-overlap) and per-language regex chunkers
  for Python / Go / TypeScript / Java / Rust (JavaScript routes through
  TypeScript). [`docs/internal/DECISIONS.md` ADR-005](docs/internal/DECISIONS.md#adr-005-regex-chunkers-as-stage-2-default).
- [`internal/bm25/`](internal/bm25/) — Lucene-variant BM25 (`k1=1.5`,
  `b=0.75`, non-negative IDF; ATIRE TF formula — rank-equivalent
  cosmetic divergence vs Lucene). [`docs/internal/DECISIONS.md` ADR-006](docs/internal/DECISIONS.md#adr-006-bm25-formula-choice-lucene-variant).
- [`internal/embed/`](internal/embed/) — Model2Vec inference: hand-rolled
  WordPiece tokenizer ([ADR-003](docs/internal/DECISIONS.md#adr-003-hand-rolled-wordpiece-tokenizer)),
  hand-rolled safetensors mmap reader ([ADR-004](docs/internal/DECISIONS.md#adr-004-hand-rolled-safetensors-reader)),
  three-tensor pooling (`Σ embeddings[mapping[id]]·weights[id] / Σ
  weights[id]`) with float64 accumulators (float32 silently fails ≥1−1e-5
  cosine on longer inputs).
- [`internal/ann/flat.go`](internal/ann/flat.go) — exact brute-force
  cosine retriever over the dense embedding matrix.
- [`internal/search/`](internal/search/) — orchestration: α-weighted
  RRF fusion + file-coherence / definition / embedded-symbol /
  stem-match boosts + three-tier path penalties + file-saturation
  decay. Ported verbatim from semble's `search.py` +
  `ranking/{boosting,penalties,weighting}.py` + `tokens.py`.
  [`docs/internal/DECISIONS.md` ADR-002](docs/internal/DECISIONS.md#adr-002-retrieval-algorithm-verbatim-from-semble).
- [`cmd/ken`](cmd/ken/) — CLI subcommands `index` / `search` / `bench`.
- [`cmd/ken-mcp`](cmd/ken-mcp/) — MCP server speaking JSON-RPC over
  stdio. Two tools (`search`, `find_related`) with arg shapes ported
  verbatim from `/tmp/semble/src/semble/mcp.py`. Same markdown-string
  return format as semble's `_format_results`, so existing
  semble-trained agents work unchanged.
  [`docs/internal/DECISIONS.md` ADR-007](docs/internal/DECISIONS.md#adr-007-mcp-server-as-drop-in-replacement-for-semble).
- Per-process repo→Index cache in [`mcp/`](mcp/) (LRU + singleflight
  dedup); `http(s)://` URLs shallow-clone via `go-git` to
  `$TMPDIR/ken-mcp/<sha256>/`.
- 18-case embedding golden fixture (`testdata/golden.json`, generated
  by `scripts/pin_inference.py`). Asserts ≥1−1e-5 cosine parity against
  `StaticModel.encode()` on non-degenerate inputs; zero-vector
  contract on empty / all-`[UNK]` inputs.
- First NDCG measurement against semble's bench: 0.842 hybrid (gap
  0.012 vs published 0.854), 0.624 BM25-raw, 0.647 semantic-raw
  (within 0.003 of semble's 0.650 — algorithm port validated).
  [`docs/BENCH.md`](docs/BENCH.md#empirical-findings-v010).

### Dependencies (runtime)

- `github.com/go-git/go-git/v5` (Apache-2.0; MCP shallow-clone).
- `github.com/modelcontextprotocol/go-sdk` (Apache-2.0).
- `golang.org/x/sync` (singleflight for the MCP cache).
- `golang.org/x/text` (Unicode normalization for the embed tokenizer).
