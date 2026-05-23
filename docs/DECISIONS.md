# Decisions

Architecture Decision Records for `ken`, in chronological order. Each entry captures the decision, the alternatives considered, and the consequences — so future readers can understand *why* the codebase is shaped the way it is, not just *what* it does. Companion to [`docs/DESIGN.md`](DESIGN.md) (the atemporal design spec) and [`docs/BENCH.md`](BENCH.md) (empirical findings).

ADR statuses: **Proposed** (documenting design alternatives; no implementation decision yet), **Accepted**, **Superseded** (replaced by a later ADR), **Deprecated** (no longer applies but kept for history).

| # | Decision | Status |
|---|---|---|
| [ADR-001](#adr-001-pure-go-no-cgo) | Pure-Go runtime, no cgo | Accepted |
| [ADR-002](#adr-002-verbatim-port-of-sembles-algorithm) | Verbatim port of semble's algorithm (not independent re-implementation) | Accepted |
| [ADR-003](#adr-003-hand-rolled-wordpiece-tokenizer) | Hand-rolled WordPiece tokenizer | Accepted |
| [ADR-004](#adr-004-hand-rolled-safetensors-reader) | Hand-rolled safetensors reader | Accepted |
| [ADR-005](#adr-005-chunker-interface-with-three-pluggable-options-ship-c-first) | Chunker interface with three pluggable options; ship C first | Accepted |
| [ADR-006](#adr-006-bm25-pinned-to-bm25s-defaults-tf-formula-divergence-left-cosmetic) | BM25 pinned to bm25s defaults; TF-formula divergence left cosmetic | Accepted |
| [ADR-007](#adr-007-drop-in-mcp-compatibility-with-semble-stdout-clean-contract) | Drop-in MCP compatibility with semble; stdout-clean contract | Accepted |
| [ADR-008](#adr-008-bm25-tokenizer-verbatim-port-of-sembles-tokenspy) | BM25 tokenizer = verbatim port of semble's `tokens.py` (snake-case compound preservation) | Accepted |
| [ADR-009](#adr-009-env-var-validation-fail-loud-not-silent) | Env-var validation in ken-mcp: fail-loud, not silent | Accepted |
| [ADR-010](#adr-010-tree-sitter-via-gotreesitter-instead-of-wazerowasm) | Tree-sitter via `gotreesitter` (pivot from wazero+WASM) | Accepted |
| [ADR-011](#adr-011-default-chunker-stays-regex-in-v020-treesitter-is-opt-in) | Default chunker stays `regex` in v0.2.0; treesitter is opt-in | Accepted |
| [ADR-012](#adr-012-incremental-indexing-via-fsnotify--atomic-snapshot-swap) | Incremental indexing via fsnotify + atomic snapshot swap | Accepted |
| [ADR-013](#adr-013-corpus-adaptive-α--adding-a-third-query-class-branch) | Corpus-adaptive α — adding a third query-class branch | Deprecated |
| [ADR-014](#adr-014-fsfs-as-canonical-walkerindexer-surface) | `fs.FS` as canonical walker/indexer surface | Accepted |
| [ADR-015](#adr-015-nested-gitignore-support-via-scope-stack-on-existing-rule-engine) | Nested `.gitignore` support via scope stack on existing rule engine | Accepted |

---

## ADR-001: Pure-Go, no cgo

**Status:** Accepted
**Date:** 2026-05-19

### Context
semble is a Python tool with native dependencies (Rust-backed `tokenizers`, etc.). Porting to Go invites two paths: use cgo bindings to the same C-level libraries (tree-sitter, HF tokenizers, safetensors), or stay pure-Go and accept that some primitives must be hand-rolled or replaced.

### Decision
**No cgo.** The entire build must compile with the standard Go toolchain on darwin/linux × amd64/arm64 (and ideally wasip1) without a C cross-toolchain.

### Alternatives considered
- **cgo bindings for tree-sitter, HF tokenizers, safetensors.** Faster initial implementation; mature upstream libraries. Rejected because (a) every cgo dep blocks `GOOS`/`GOARCH` cross-compile without a C cross-toolchain, (b) `go test -race` cannot see across the cgo boundary, (c) CI images would need `gcc` + per-platform native libs, breaking the "single binary" thesis.
- **Mixed: cgo for tokenizer only.** Halfway house. Rejected — once cgo is in the build graph, every downstream invariant (race testing, cross-compile, single static binary) is gone.

### Consequences
- Single static binary, cross-compiles to any Go target.
- Several primitives that would have been "use the library" become hand-rolled (see ADR-003, ADR-004). ~600 LOC of additional code.
- Tree-sitter required novel work (see ADR-010); the cgo-binding shortcut was unavailable.
- `go test -race` covers the whole codebase.

---

## ADR-002: Verbatim port of semble's algorithm

**Status:** Accepted
**Date:** 2026-05-19

### Context
The retrieval pipeline (BM25 + Model2Vec + RRF fusion + rerank heuristics + path penalties) has many magic constants — α-fusion weights, RRF k=60, boost multipliers, penalty tiers, file-saturation decay, query-type detection rules. Each could plausibly be re-derived or tuned independently.

### Decision
**Port verbatim from semble's live source** (`/tmp/semble/src/semble/search.py`, `ranking/{boosting,penalties,weighting}.py`, `tokens.py`). Constants are not hyperparameters to tune; they are upstream values to preserve.

### Alternatives considered
- **Reconstruction from the published paper / blog post.** Rejected — the published description was high-level; the actual constants and edge-case ordering differed (Stage 4's first reconstruction had material algorithm bugs that the verbatim re-port from `/tmp/semble` caught).
- **Independent reimplementation with our own tuning.** Rejected — without a benchmark in hand (semble's wasn't initially available), there was no defensible tuning target, and any divergence would be untraceable to either implementation or hyperparameter.

### Consequences
- Any divergence from semble's published NDCG is by construction a port bug or a chunker effect, never a hyperparameter difference. Makes regressions tractable to diagnose (see ADR-008 result: tokenizer fix moved +0.002; gap therefore must be chunker-bound).
- `docs/DESIGN.md` §7 cites the specific file:line pairs in semble's source for each ported constant, so future updates have an audit trail.
- Cosmetic divergences (see ADR-006) are explicitly retained as cosmetic, not silently introduced.

---

## ADR-003: Hand-rolled WordPiece tokenizer

**Status:** Accepted
**Date:** 2026-05-19

### Context
`potion-code-16M` uses a WordPiece tokenizer (61,826 vocab × 256 dim) configured with `BertNormalizer` (`clean_text:true`, `handle_chinese_chars:true`, `strip_accents:null` with `lowercase:true`) and `BertPreTokenizer`. No `[CLS]`/`[SEP]`/`[MASK]`, only `[PAD]=0` and `[UNK]=1`. Output is just `text → []int32`; no offsets, no decoding, no special-token templating.

### Decision
**Hand-roll WordPiece** in `internal/embed/tokenize.go` (~400 LOC). Validate against `transformers.AutoTokenizer` via the 11,447-input parity harness (`scripts/parity_dump.py` + `internal/embed/parity_test.go` under `-tags=parity`).

### Alternatives considered
- **`sugarme/tokenizer`** (or similar pure-Go tokenizer libraries). Rejected — they target the full HF tokenizer feature surface (offsets, decoding, BPE, special-token templating, alignment) and pull in proportionate complexity. We need a 200-line algorithm; using a library means ~4000 LOC of dependency we don't use and a port-drift risk if upstream evolves.
- **cgo binding to HF `tokenizers`.** Rejected by ADR-001.

### Consequences
- Three real tokenizer bugs caught by the parity harness on first run (Unicode whitespace handling, control-vs-whitespace ordering in `clean_text`, `[PAD]`/`[UNK]` carve-out before normalization). All documented in `docs/DESIGN.md` §3 risk register.
- 11,447 inputs, 0 drift across normalize/pre_tokenize/wordpiece/other categories.
- The harness itself is now the regression bar; any future tokenizer change is rejected if drift becomes non-zero.

---

## ADR-004: Hand-rolled safetensors reader

**Status:** Accepted
**Date:** 2026-05-19

### Context
`model.safetensors` for Model2Vec is a 64 MB file with **three** tensors (`embeddings` F32, `mapping` I64, `weights` F64) and a fixed format: 8-byte little-endian length, JSON header naming the tensors and giving their byte offsets, then raw bytes. ken reads exactly these three tensors at startup; the file never changes shape.

### Decision
**Hand-roll the safetensors reader** in `internal/embed/safetensors.go` (~80 LOC). `mmap` the file; expose zero-copy slices of the underlying bytes for the F32 tensor (the embeddings matrix).

### Alternatives considered
- **`github.com/nlpodyssey/safetensors`** (or similar). Rejected — the library's abstraction (typed `Tensor` interfaces with method calls per element) prevents the zero-copy view ken needs for the 61826×256 embeddings matrix. Cited in `docs/DESIGN.md` Sources as a format reference but not a code dep.
- **Always read into Go slices** (give up `mmap`). Rejected — adds 64 MB of memory at startup with no parser-vs-direct-access ergonomic win.

### Consequences
- ken `mmap`s the safetensors file; the F32 embeddings matrix is read by direct slice indexing without any per-element library calls.
- 80 LOC vs ~2k LOC for a generalized safetensors lib; trivially reviewable.
- The reader supports exactly the three tensor types we need. Anything else errors. Acceptable because the model format is fixed.

---

## ADR-005: Chunker interface with three pluggable options; ship C first

**Status:** Accepted (Option A later landed via ADR-010)
**Date:** 2026-05-19

### Context
semble chunks via Chonkie's tree-sitter-based code chunker. The pure-Go options at the time of v1 design were:
- **Option C:** hand-rolled per-language regex (zero deps, fastest, narrow language coverage)
- **Option B:** Chroma lexers (~200 languages, lexer not parser, no AST)
- **Option A:** tree-sitter via WASM/wazero (full AST, ~3-10× slower than cgo, large binary)

We needed *some* chunker to validate Stages 3–5 of the pipeline. The choice of which to start with shaped the order of risk.

### Decision
**Define a `Chunker` interface up front; ship Option C in v1; document B and A as future paths gated by triggers.** The interface lives in `internal/chunk/registry.go`; registration is via a `database/sql`-style blank import to avoid an import cycle (the `chunk` package must not import its sub-chunker packages).

### Alternatives considered
- **Start with Option A (tree-sitter via WASM).** Rejected — would have meant ~1-2 weeks of WASM-grammar plumbing before any of Stages 3-5 (tokenizer, retrieval, MCP) had a chunker to validate against. Highest-fidelity choice, worst-fit for "validate the pipeline end-to-end first."
- **Start with Option B (Chroma).** Rejected — token-stream heuristics are quick to write but bring lower parity than C for the 5 hand-tuned languages, with no AST upside vs A.
- **Skip the interface; hard-code Option C.** Rejected — even v1 needs the line-chunker fallback, which uses the same interface shape, so the seam was load-bearing from day one. Adding B/A later would require interface invention under pressure.

### Consequences
- v1 shipped with the 5 hand-tuned regex chunkers (Python/Go/TS/Java/Rust) and the line-chunker fallback; Stages 3–5 validated against it.
- The Chunker interface stayed narrow (no `ctx`, no filename — the orchestration layer stamps `Chunk.File`); narrowed deliberately during Stage 2.
- ADR-010 added Option A later via gotreesitter (the route changed from WASM, but the interface didn't).
- Option B (Chroma) remains in the risk register, never triggered.

---

## ADR-006: BM25 pinned to bm25s defaults; TF-formula divergence left cosmetic

**Status:** Accepted
**Date:** 2026-05-19

### Context
semble uses `bm25s.BM25()` with default args: `k1=1.5`, `b=0.75`, `method="lucene"`. The Lucene IDF is `ln(1 + (N-df+0.5)/(df+0.5))` (non-negative); the Lucene TF formula (delegating to robertson) is `tf / (k1*(1-b+b*ld/lavg) + tf)`. ken's implementation uses the same IDF but the **ATIRE** TF formula `(tf*(k1+1)) / (tf + k1*(1-b+b*ld/lavg))` — they differ by a constant `k1+1 = 2.5` factor at fixed k1.

### Decision
**Pin k1/b/IDF to bm25s defaults exactly. Leave the ATIRE TF-formula divergence in place** with a `docs/BENCH.md` note explaining why it's cosmetic.

### Alternatives considered
- **Fix the TF formula to Lucene/Robertson** for strict verbatim parity. Rejected — the constant `k1+1` factor applies uniformly to every term in every doc for every query, so rank order is bit-identical to Lucene's. Scores get fed through `_rrf_scores` (rank-based) before hybrid fusion, so even absolute-score consumers are unaffected. The change would be ~3 lines; the *risk* of regression on the precision contract is greater than the value.
- **Re-tune k1/b empirically against ken's chunker.** Rejected — see ADR-002. Verbatim ports don't tune, they preserve.

### Consequences
- ken's BM25 absolute scores are 2.5× semble's. Documented; never matters in practice.
- Rank order matches semble bit-for-bit on identical token streams.
- Any future BM25 divergence on real corpora is therefore tokenizer- or chunker-driven, never scorer-driven (validated empirically: ADR-008 + the chunker findings in `docs/BENCH.md`).

---

## ADR-007: Drop-in MCP compatibility with semble; stdout-clean contract

**Status:** Accepted
**Date:** 2026-05-19

### Context
Agents (Claude Code, Cursor, Codex, OpenCode, VS Code, GitHub Copilot CLI) already speak semble's MCP tool surface. If ken-mcp exposes the same tools with the same argument shapes and the same wire format, those agents work unchanged.

### Decision
**ken-mcp is a drop-in replacement for semble-mcp.** Two tools (`search`, `find_related`), arg schemas and return format ported verbatim from `/tmp/semble/src/semble/mcp.py`. Return is a markdown string via semble's `_format_results`, not structured objects. Install snippets in CLAUDE.md mirror semble's exactly with `semble` swapped for `ken-mcp`.

**Sub-decision: enforce stdout/stderr cleanliness via a load-bearing test.** stdin and stdout *are* the JSON-RPC channel; any write to stdout from any dep corrupts the stream and the agent disconnects with a cryptic JSON-decode error (the #1 way new MCP servers fail). `cmd/ken-mcp/main.go` does `log.SetOutput(os.Stderr)` at init; `TestBinary_StdoutIsCleanJSONRPC` builds the real binary and drives an actual MCP session through `sdk.CommandTransport`. Every new dep must be audited for default writers pointed at stdout.

### Alternatives considered
- **Different (better?) tool surface.** Rejected — friction-free agent migration is worth more than a marginally improved API.
- **Trust the SDK to keep stdout clean.** Rejected — the failure mode is silent and catastrophic. The subprocess test is the only thing that catches a regressing dep (e.g. go-git's progress writer in some versions).

### Consequences
- Anyone using semble-mcp can swap to ken-mcp by replacing one binary path.
- `TestBinary_StdoutIsCleanJSONRPC` is mandatory on every change to ken-mcp; if it fails, stdout is polluted and agents will disconnect.
- Adding deps to ken-mcp requires reviewing their default I/O behavior.

---

## ADR-008: BM25 tokenizer = verbatim port of semble's tokens.py

**Status:** Accepted
**Date:** 2026-05-20

### Context
v0.1.0 NDCG bench measured a 0.012 hybrid gap vs semble (0.842 vs 0.854) and a 0.053 BM25-raw gap (0.622 vs 0.675). The original ken BM25 tokenizer split snake-case identifiers at the run-extraction layer, never producing the compound form: `validate_user` tokenized to `["validate", "user"]` instead of semble's `["validate_user", "validate", "user"]`.

### Decision
**Rewrite `internal/bm25/tokenize.go` as a verbatim port of semble's `tokens.py`.** Three real divergences fixed:
1. snake-case compound preservation (the suspected dominant fix);
2. standalone digit-only runs dropped (matching `_TOKEN_RE = [a-zA-Z_][a-zA-Z0-9_]*`);
3. non-ASCII letters dropped (ASCII-only `_TOKEN_RE`).
Order of emission matches semble exactly: `[compound, *parts]` when ≥2 parts, just `[compound]` when 1.

### Alternatives considered
- **Custom rules tuned for our chunker.** Rejected by ADR-002.
- **Leave the tokenizer; assume the gap is elsewhere.** Rejected — verbatim parity was a design contract; fixing the divergence was correct regardless of NDCG impact.

### Consequences
- All 30 tokenizer tests green; new test cases pinning the snake-case compound and the ASCII-only behavior.
- **Empirical outcome (documented in `docs/BENCH.md` "Empirical findings"):** hybrid moved 0.840 → 0.842 (+0.002); BM25-raw moved 0.622 → 0.624 (+0.002). Per-repo deltas mixed (nlohmann-json +0.039, aiohttp −0.018) consistent with reshuffling, not systematic improvement.
- This **ruled out the tokenizer as the dominant cause of the residual gap** and localized it to the chunker. ADR-010 followed as the next step.

---

## ADR-009: Env-var validation in ken-mcp: fail-loud, not silent

**Status:** Accepted
**Date:** 2026-05-20

### Context
`cmd/ken-mcp/main.go` originally parsed env vars with patterns like `strconv.Atoi(envOr("KEN_MCP_CACHE_SIZE", ...))` that discarded the error. A typo like `KEN_MCP_CACHE_SIZE=of` produced size=0 → cache disabled → silent "why is ken-mcp re-indexing every query?" footgun. `parseLevel("verbose")` had the same shape (silent fallthrough to warn).

### Decision
**Validate every `KEN_MCP_*` env var at startup; log a stderr warning on bad input; fall back to the documented default.** Implemented as four helpers in `cmd/ken-mcp/env.go`:
- `envInt(name, fallback, logger) int` — parse error → warn + fallback. Negative caller-decides (CACHE_SIZE rejects).
- `envEnum(name, allowed, fallback, logger) string` — case-sensitive match against allow-list; mismatch → warn + fallback.
- `envPath(name, logger) string` — must be an existing directory if set; warn but preserve value (lets downstream auto-downgrade run).
- `envPathOrURL(name, logger) string` — directory or `http(s)://` URL; warn-and-keep otherwise.

LOG_LEVEL bootstraps a default-warn logger first so the logger itself can warn about a bad LOG_LEVEL value (chicken-and-egg resolved by ordering).

### Alternatives considered
- **Error out on bad input.** Rejected — for an MCP server that survives many user sessions, refusing to start on a typo is worse than starting with a documented default and a clear warning.
- **Silent fallback only.** This was the bug; rejected.

### Consequences
- Every invalid env var produces a single, parseable stderr warning at startup.
- The stdout/stderr contract from ADR-007 is preserved: all warnings go to stderr, stdout stays JSON-RPC-clean.
- New env vars get the same treatment by convention; the helpers make it easy.
- `TestBinary_StdoutIsCleanJSONRPC` confirms the validation pass didn't leak anything to stdout.

---

## ADR-010: Tree-sitter via gotreesitter instead of wazero+WASM

**Status:** Accepted (supersedes the Option A description in `docs/DESIGN.md` §2)
**Date:** 2026-05-20

### Context
v0.1.0 NDCG measurements localized the 0.012 hybrid gap to the chunker: Python tracked semble +0.003 (our regex chunker handles it well); go/rust/zig/cpp/typescript sat at −0.02 to −0.05 (regex chunker draws different boundaries than semble's tree-sitter chunker via Chonkie). ADR-008 confirmed the tokenizer wasn't responsible. The chunker was the lever.

`docs/DESIGN.md` §2 originally specified Option A as **tree-sitter via WASM (wazero + tree-sitter's WASM core + per-language `.wasm` grammars)**. By May 2026 that plan no longer matched the landscape:
- The only wazero-based wrapper (`malivvan/tree-sitter`) was dormant (3 stars, last commit Jan 2025).
- Building our own wazero wrapper would have been ~1-2 weeks of plumbing before any chunking algorithm could run.
- `github.com/odvcencio/gotreesitter` appeared as a **pure-Go reimplementation** of the tree-sitter runtime (parser, lexer, GLR, queries, cursor) with 205+ grammars embedded; cgo-parity benchmarks; MIT license.

### Decision
**Adopt `gotreesitter` for Option A.** Implement in `internal/chunk/treesitter/` with three files (`chunker.go` + `cast.go` + `languages.go`). Chunking algorithm = cAST split-then-merge (arXiv 2506.15655), the same algorithm Chonkie uses via `tree-sitter-language-pack`'s Rust `process()`. Register as `"treesitter"` in the chunk registry; coexists with `"regex"`. `docs/DESIGN.md` §2 records the pivot rationale; the WASM discussion is preserved in the historical paragraph there.

### Alternatives considered
- **Stick with wazero + WASM as originally planned.** Rejected — would have meant writing the wrapper that `malivvan/tree-sitter` didn't finish, paying ~1-2 weeks of plumbing before any cAST work could start. The pure-Go-no-cgo constraint (ADR-001) is satisfied just as well by gotreesitter; WASM was a means, not an end.
- **cgo binding to upstream tree-sitter.** Rejected by ADR-001.
- **Stay with regex chunker; close the gap some other way.** Rejected — the per-language data unambiguously points at the chunker, and the regex chunker's maintenance cost scales linearly with language count, while gotreesitter ships 206 grammars at zero per-language cost.

### Consequences
- v0.2.0 adds a `treesitter` chunker behind the existing `Chunker` interface; the regex chunker stays registered for users who don't want the new dep or the binary-size cost.
- Binary size: `ken` 3.9 MB → 30 MB; `ken-mcp` 16 MB → 42 MB (embedded grammars). Externalization via gotreesitter's `grammar_blobs_external` build tag is available for distributions that need slim binaries.
- `gotreesitter` is pre-1.0 with one maintainer (bus-factor: 1). Mitigated by the swap-out path: the chunker interface lets us replace it without touching downstream code if needed. Risk recorded in `docs/DESIGN.md` §10.
- Whether to default `--chunker=treesitter` in v0.2.0 is a separate decision pending NDCG measurement on the full benchmark; see ADR-011 below.

---

## ADR-011: Default chunker stays regex in v0.2.0; treesitter is opt-in

**Status:** Accepted
**Date:** 2026-05-20

### Context
ADR-010 landed the tree-sitter chunker via gotreesitter, motivated by v0.1.0's measured 0.012 hybrid NDCG gap vs semble — the bench data localized the gap to non-Python regex-chunker boundaries (go/rust/zig/cpp/typescript at −0.02 to −0.05 each). The hypothesis going in: AST-aware chunking via tree-sitter should close most of that gap.

After three bench iterations on the full corpus (63 repos, 1251 queries), the data did not support that hypothesis:

| Config | BM25-raw | Hybrid | vs regex baseline |
|---|---:|---:|---:|
| regex (v0.1.0 baseline) | 0.624 | **0.842** | — |
| treesitter v1 (all 19 langs incl. bash + csharp) | 0.587 | 0.831 | −0.011 |
| treesitter v2 (skip bash + csharp, chunkSize=3000 floor) | 0.616 | 0.834 | −0.008 |
| treesitter v3 (skip bash + csharp, chunkSize=1500 default) | tbd | **0.838** | **−0.004** |

Two grammar issues surfaced during iteration: the gotreesitter v0.18.0 **C# grammar** OOMs on real-world C# (1.7+ GB RSS during dapper indexing → SIGKILL on all 3 csharp bench repos); the **bash grammar** is pathologically slow on real bash-it content (~39% of files hit the 1s per-parse timeout). Both are skipped at the language-map level and fall back to the line chunker, matching what the regex chunker did for them anyway.

The chunkSize=3000-byte floor was an iteration attempt at "cAST is over-splitting because Chonkie's defaults are in tokens not bytes." That hypothesis was empirically wrong — bigger chunks dilute BM25 IDF and average out embeddings; the existing 1500-byte budget tuned for the regex chunker turned out to be the right value for cAST too.

The final v0.2.0 hybrid number (0.838) is **within noise of the regex baseline (0.842)** — net Δ −0.004. Per-language: clear wins on kotlin (+0.011), zig (+0.013), typescript (+0.009), java (+0.006), php (+0.005); losses on python (−0.009), c (−0.017), rust (−0.013), lua (−0.022); the rest within ±0.005.

### Decision
**Default chunker stays `regex` in v0.2.0. The `treesitter` chunker is registered and selectable via `--chunker=treesitter` (CLI) or `KEN_MCP_CHUNKER=treesitter` (ken-mcp), but is not the default.**

This is the "ship as opt-in" outcome: the work is real, the chunker is correct, but the NDCG case for swapping the default is not there.

### Alternatives considered
- **Default to treesitter** (the original hypothesis). Rejected — net NDCG regression of 0.004 doesn't justify the +26 MB binary cost, the new dep on a pre-1.0 single-maintainer library, or the per-language losses on python/rust/c/lua.
- **Back out treesitter entirely.** Rejected — the per-language wins on kotlin/zig/java/typescript/php are real, and the chunker is correctly implemented and tested. Users who index those languages heavily have a defensible reason to opt in. Removing the code wastes that option for no upside.
- **Default to treesitter for only the languages where it wins.** Rejected as too clever for v0.2.0 — would require per-language chunker selection plumbing, and the wins are small (≤+0.013). The user can already pick a chunker globally; per-language routing is a v0.3.0 question if it becomes worth it.
- **More cAST tuning** (different chunk sizes, separator weighting, merge thresholds). Considered, deferred — the iteration cycle is ~17 minutes per bench, and we'd already established the chunkSize hypothesis was wrong. Could come back in v0.3.0 with more carefully designed experiments.

### Consequences
- v0.2.0 ships with two registered chunkers: `regex` (default) and `treesitter` (opt-in). The Chunker interface from ADR-005 has now been validated by a second consumer.
- `docs/BENCH.md` documents the treesitter NDCG numbers explicitly so users can decide whether the per-language wins matter for their corpus.
- Binary size for the default build is unaffected (the treesitter dep is only pulled in via the blank-import in `internal/search`; the chunker code itself doesn't compile in a meaningfully expensive way — but gotreesitter's embedded grammars *do* add ~26 MB to both binaries). **TODO for a later ADR:** whether to put treesitter behind a build tag so the default binary stays slim. For v0.2.0 the +26 MB is acceptable because users who built ken at HEAD will get the treesitter option immediately.
- The C# and bash grammar issues are recorded in `docs/DESIGN.md` §10 risk register. Both auto-fall-back to the line chunker; users won't see crashes.

---

## ADR-012: Incremental indexing via fsnotify + atomic snapshot swap

**Status:** Accepted
**Date:** 2026-05-20

### Context
Through v0.2.x, every `ken` invocation walked the tree from scratch and `ken-mcp` cached the built index in a per-process LRU but never invalidated entries on file changes. An agent editing files mid-session got stale results until the LRU evicted that repo or the process restarted — a real correctness gap for the headline use case (Claude Code-style agents querying their own working tree).

The design conversation locked five sub-decisions before code:
1. **Default-on**, no opt-in. `ken-mcp` watches always; `ken index` defaults to `--watch` with `--no-watch` as the v0.2-compatible escape.
2. **Pure lazy IDF.** Recompute from current df + N at query time; no IDF cache.
3. **2-second fixed debounce**, not configurable. Above editor save-on-keystroke timescales; small enough that an agent doesn't notice.
4. **Tombstone deletes**, no compaction. Deleted file's chunks stay in `chunks` with `Tombstoned=true`; query paths skip them. Memory grows monotonically until compaction lands as a v0.3.x trigger.
5. **No reader-side lock.** Writers must not block readers, and readers must not pay per-query copy cost.

The implementation correction surfaced during drafting: "writer mutates in-place" isn't safe in Go. Slice-header writes and map writes have no memory-model guarantees for concurrent readers — a query iterating `ann.Flat.vecs` while the writer appends could read garbage or panic. The correct realization of property #5 is an immutable-snapshot model.

### Decision
**Implement #5 as `atomic.Pointer[Index]` swap of fully-built immutable snapshots.** Writers (the debouncer goroutine) keep the mutable corpus state (`chunks []chunk.Chunk` parallel to `vecs [][]float32`) under an internal mutex that only writers contend on; they build a brand-new `*Index` (with a new `*bm25.Index` and `*ann.Flat`) from the current corpus state, then publish via `atomic.Pointer.Store`. Readers (every query path) do exactly one `atomic.Pointer.Load` at query entry and use that pointer for the entire call. A new snapshot published mid-call doesn't affect the in-flight reader. The properties locked in design — readers never wait, no per-query data copy — hold; the immutable-snapshot model adds a property "for free": snapshot consistency for the duration of a single Search/FindRelated/ResolveChunk.

File changes are detected via `github.com/fsnotify/fsnotify` (pure Go, no cgo, OS-specific backends abstracted). The watcher loop accumulates dirty events into a map, debounces 2s, then re-chunks/re-embeds only the changed files, tombstones removed files, and publishes a new snapshot.

### Alternatives considered
- **RWMutex over a mutable Index.** Rejected — reader-side latency would be unbounded by the writer's turn (a 100ms reindex pauses every reader). Fundamentally violates property #5.
- **Per-query snapshot copy of postings + embedding matrix.** Rejected — O(corpus) per query, killing the lock-free design's value.
- **Coarse-grained full reindex on every change** (no debounce). Rejected — `git checkout` and even `git status` fire hundreds of events; a cascade of full rebuilds would burn CPU and bury actual reindex work behind queue depth.
- **In-place mutation with `sync/atomic` on the slice header.** Considered briefly. Rejected — Go's memory model doesn't make this safe; an in-flight reader can observe a partially-updated slice header or read past the new length into garbage. The correct expression of "no lock on reads" in Go is atomic.Pointer swap of an immutable value, which is what we did.

### Consequences
- **Snapshot consistency for free.** Each Search/FindRelated/ResolveChunk call uses one snapshot start-to-end. Stronger than the "best-effort" property we required.
- **Compaction landed post-v0.3.0.** v0.3.0 accumulated tombstones in the `chunks` slice for index stability across rebuilds; compaction (drop tombstoned entries during every snapshot rebuild, before publish) landed once the precondition was confirmed — chunk indices are entirely internal to a snapshot, so renumbering on compaction doesn't break any caller. Memory now plateaus at live-chunk working-set size instead of growing with cumulative edit volume; a 20-flush burst on `testdata/repo` showed RSS settling within the first few flushes and staying flat (32 KB delta across the last 5 flushes vs ~9 KB/edit-batch monotonic under v0.3.0). The mutator path is unchanged — deletes still tombstone in place — only the rebuild step now drops them. See `internal/search/watch.go::compactCorpus`.
- **Reader latency unchanged from v0.2.** No lock on the query path; the single `atomic.Pointer.Load` is sub-nanosecond.
- **Writer latency ~ debounce + rebuild time.** Edit → query latency is 2s (debounce) + O(corpus) (BM25 rebuild + chunk-count incremental embed). On `testdata/repo` with 6 chunks the rebuild is sub-millisecond; on a 100K-chunk corpus the rebuild itself is in the seconds range, embed cost dominates and incremental embed (only changed files) keeps it bounded.
- **`*Flat` stays immutable.** The "No Add/Remove API today, by design" property from `internal/ann/flat.go` is preserved — incremental indexing builds new `*Flat` values; it never mutates an existing one. The `internal/ann/flat.go` package comment was updated to point at this pattern instead of the old "would require a lock" caveat.
- **`Chunk.Tombstoned` field added.** Every read path (Search / FindRelated / ResolveChunk) checks it. Over-fetch by the current tombstone count keeps result lists stable as tombstone density grows.
- **`ken index --watch` and `--no-watch` flags.** `--watch` (default) keeps the process alive watching; `--no-watch` restores v0.2 behavior (build once, print, exit). `ken search` and `ken bench` accept the flags but the values are no-ops since the processes are one-shot.
- **fsnotify dep.** Pure Go, MIT, ~14KB compiled, well-maintained (Kubernetes / Hugo / VS Code use it). Backends: inotify on Linux, FSEvents on macOS, ReadDirectoryChangesW on Windows. v0.3 ships at v1.10.1.

### Empirical confirmation

**2 s debounce — confirmed correct at v0.3.** The locked decision picked 2 s as "above editor save-on-keystroke timescales (VS Code, vim temp-file rename) but small enough that an agent doesn't notice it." Both halves landed empirically:

- *Editor noise absorbed.* Bulk writes from vim's atomic-rename save flow and VS Code's save-on-keystroke (250 ms typing-pause heuristic) both collapse into a single debounce-flush. `TestWatchedIndex_Debounce_BatchedWrites` pins this at the synthetic level (5 writes in 500 ms → 1 publish); manual `ken index --watch` sessions on real editors confirm in practice.
- *Below agent think-time.* Claude Code's typical edit-then-query cadence has hundreds of milliseconds of model + tool-call latency between any two edits an agent makes, so 2 s consistently lands before the next query that would observe the previous edit. No user-visible debounce delay in interactive use.
- *Memory growth bounded by edit rate.* In a 100-edit synthetic burst on `testdata/repo`, RSS grew by ~884 KB (≈9 KB/edit-batch, dominated by tombstone accumulation and BM25 postings rebuild). Within v0.3's tolerance; compaction trigger is a multi-day session — not a per-edit concern.

**OnFlush feedback hook added late v0.3.** Initial v0.3 had no per-flush user-visible signal — fine for ken-mcp (agents query whenever and get fresh results) but a gap for interactive `ken index --watch` users who couldn't tell whether the watcher was alive. Added `WatchedIndex.SetOnFlush(func(msg string))` so callers wire stderr / leveled-logger output without `internal/search` knowing about either. CLI logs each flush at one line; ken-mcp routes it at info-level so warn-default runs stay quiet.

---

## ADR-013: Corpus-adaptive α — adding a third query-class branch

**Status:** Deprecated
**Date:** 2026-05-22 (Proposed) / 2026-05-21 (Deprecated via Prompt 23)

### Context

semble's bench shows hybrid retrieval ahead of BM25 by a clear margin — semble published 0.854 hybrid vs 0.675 BM25-raw; ken measures 0.842 vs 0.624 on the same 63-repo × 1251-query corpus. The CoIR-CSN-Python external benchmark reverses this: ken BM25 0.8743 > hybrid 0.7839 > semantic 0.7405 (1000-query subsample, regex chunker). The cause this ADR was originally built on — and which turned out to be a misread of the data — is documented at [`docs/BENCH.md` "Why BM25 beats hybrid on CSN-Python"](BENCH.md#why-bm25-beats-hybrid-on-csn-python): CSN-Python's queries (as CoIR re-hosts the dataset) are actually full **Python function sources**, and the relevant document for each query is the **docstring extracted from that same function**. Because the docstring lives inside the function source as a literal substring, any lexical retriever with identifier-aware tokenization is effectively doing substring-match — BM25 has the answer string as input.

**This misdescription was the load-bearing premise this ADR was built on.** The original Context paragraph described CSN queries as English docstring-shaped natural-language questions answered by the function being described, and framed α-routing as a way to recover hybrid performance on a docstring-shaped NL query class. Prompt 22's precondition step — read `scripts/bench_coir.py`, inspect a sample query/document pair — surfaced the actual direction (queries = code, docs = docstrings) and replaced the identifier-overlap diagnosis with the sharper substring-leak diagnosis above. No α value beats "the answer string is literally in the query"; the structural finding doesn't generalize past CoIR's reframing of CodeSearchNet. See the "Validation outcome" section below.

semble's `resolveAlpha` (verbatim in [`internal/search/adaptive.go`](../internal/search/adaptive.go)) recognizes two query classes: symbol (α=0.3) and NL (α=0.5). There is no third class for "docstring-shaped NL" — long English queries whose lexical overlap with the answer doc is unusually high. A third branch with a lower α (lean harder on BM25, perhaps 0.1–0.2) is the conservative extension that would recover the CSN performance without touching the existing branches' constants. The lever exists; the question is whether to pull it, with what detection signal, and whether the classifier risk justifies the gain.

### Decision sought

Whether to add a third query-class branch to `resolveAlpha`, and if so, what detection signal and α value. **Explicitly out of scope**: tuning the existing 0.3 / 0.5 constants. ADR-002 protects those. The question is whether ken adds a new branch for a query class semble's classifier doesn't recognize, while leaving the existing branches mathematically identical to the verbatim port for inputs semble's classifier matches.

### Alternatives considered

**A. Status quo / do nothing.** Accept CoIR loss as a documented limitation; the README routing-advice section adds a one-liner suggesting `--mode bm25` for docstring-heavy corpora.
- *If it works:* users on docstring-heavy corpora are slightly worse off out-of-box but have a one-flag escape.
- *If it doesn't:* nobody reads the routing advice; reports of "hybrid is broken" against CSN-shaped corpora.
- *ADR-002 position:* preserves verbatim port strictly — the reference position.

**B. Per-repo offline tuning.** Expose α as a config knob / env var; users tune for their corpus.
- *If it works:* users with labeled data get the best α for their corpus.
- *If it doesn't:* most users don't have labeled data; ken-mcp is a black box to most agents that won't propagate config; default α=0.5 remains the out-of-box experience.
- *ADR-002 position:* preserves the default branches verbatim; adds an override surface. Neutral, but multiplies config without improving the default.

**C. Per-query classifier (hand-tuned).** Extend `adaptive.go` with a "docstring-shaped" detector — e.g. (query token count > N) AND (English word ratio > P) AND (identifier-token ratio < Q). Route matched queries to a third α (likely 0.1–0.2 to lean strongly on BM25).
- *If it works:* zero user friction, out-of-box improvement on CSN-shaped corpora; extends the existing hand-tuned `is_symbol_query` pattern.
- *If it doesn't:* misclassifications hurt — a concept-NL query misrouted to docstring-NL gets α=0.1, i.e. "ignore semantic, which has the answer." Threshold choice is itself a tuning knob.
- *ADR-002 position:* extends the verbatim port — new branch, existing constants untouched. The recommended path *if* the validation gate (below) confirms the signal is real.

**D. Per-query classifier (learned).** Train a small model on (query features, optimal-α) pairs.
- *If it works:* highest generalization across query shapes we haven't named.
- *If it doesn't:* training data needed; model weights to ship; the no-cgo constraint (ADR-001) complicates the training pipeline (inference is fine; the training loop isn't).
- *ADR-002 position:* clearly retunes the verbatim port. Strict reading rejects.

**E. Multi-armed bandit at query time.** Run hybrid with two or three α values, ensemble.
- *If it works:* no upfront classifier; α is implicit in the ensemble.
- *If it doesn't:* 2–3× query latency (~1.5 ms → ~3–5 ms); ensembling is itself a tuning surface.
- *ADR-002 position:* doesn't change α values, but introduces a new orchestration layer. Soft tension.

### ADR-002 tension — addressed explicitly

Two framings have to be weighed:

1. **"New branch, not retuning."** semble's `resolveAlpha` returns 0.3 for symbol and 0.5 for NL. A ken extension that adds a third return value for a third query class leaves both of semble's branches mathematically unchanged; inputs that match semble's classifier produce identical α values to a verbatim port. Precedent: ADR-012 extended semble's algorithm with tombstone semantics and atomic-snapshot swap without violating ADR-002, because the extensions are additive on top of the port.
2. **"Once we tune α at all, the contract is bent."** The verbatim-port discipline is also about *the principle* that we don't apply our judgment to algorithm constants. Adding a new branch is still applying our judgment — about query classification, about the α value, about whether the bench evidence justifies the branch. Under this reading, A is the only ADR-002-consistent path; B–E are deviations.

**Position:** the first reading is the right one for this ADR. ADR-002 protects against ad-hoc retuning of semble's existing constants; it does not prohibit extending the algorithm with new branches that recognize cases semble doesn't. The verbatim port stays verbatim for inputs semble's classifier matches; ken adds an additive layer for the docstring class. The discipline that matters is "no silent divergence" — and a documented ADR with a validation gate that can *kill* the proposal is the opposite of silent divergence.

### Pre-implementation validation plan

What we'd do BEFORE writing any classifier code (this is the gate that turns ADR-013 from Proposed → Accepted, or kills it):

1. **Define a query-shape signal manually.** Pick 2–3 feature axes — probably (query length in BM25-tokens), (English-word ratio), maybe (identifier-token ratio). No training, just hand-picked thresholds.
2. **Manually classify a 200-query sample.** Stratified across semble's bench (mixed) and CoIR-CSN-Python (docstring-heavy). Three buckets: symbol, concept-NL, docstring-NL. Inter-annotator agreement is just you eyeballing twice with a day in between to catch consistency drift.
3. **Per-bucket α sweep.** For each bucket, run the NDCG bench at α ∈ {0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7} and find the per-bucket optimum. **Decision gate:**
   - If the optima differ by ≥ 0.1 across buckets → the lever is real; ADR-013 advances to Accepted and a follow-up prompt implements C. Recommended α value for the docstring branch is set from the sweep, not pre-committed.
   - If they don't differ meaningfully → ADR-013 closes as "investigated, no signal; α-routing is not the right lever for this." Status changes to Deprecated.
   - If they differ but the classifier signal is noisy enough that misclassifications would erase the wins → ADR-013 closes as "investigated, signal exists but unreliable; reopen if a better classifier signal is found." Status: Deprecated, with a note.

The pre-implementation validation can run on top of the existing `bench/ndcg/` and `bench/tokens/` harnesses with one extra script (e.g. `scripts/alpha_sweep.py`) — no new test infrastructure, far less work than a full implementation.

### Consequences (forecast)

- **If A (do nothing) is chosen:** CSN-Python remains a documented win-for-BM25 case; README routing-advice adds a one-liner suggesting `--mode bm25` for docstring-heavy corpora. No code change.
- **If C is implemented (post-validation gate):**
  - One additional branch in `internal/search/adaptive.go::resolveAlpha`.
  - CoIR-CSN-Python hybrid: target within 0.01 of BM25's 0.8743 (0.7839 → ~0.87).
  - semble bench hybrid: within ±0.005. Conservative thresholds bias toward false negatives, leaving the legacy α=0.5 active for ambiguous queries.
  - New failure mode: concept-NL misrouted to docstring-NL (α=0.1 means "ignore semantic, which has the answer"). Validation watches for semble-bench regressions.
  - One CHANGELOG / README line about ken's adaptive α recognizing docstring-shaped queries.
- **If the gate kills it:** Status changes Proposed → Deprecated; [`docs/BENCH.md`](BENCH.md#external-benchmark--coir-csn-python) updates the CSN paragraph with the no-signal finding.

### Validation outcome — Prompt 22 reconnaissance (2026-05-21)

Prompt 22 paused before any labeling work after reading [`scripts/bench_coir.py`](../scripts/bench_coir.py) and inspecting a sample query/document pair from `testdata/bench/coir-csn-python/`. The motivating empirical claim that built this ADR — that CSN-Python queries are English docstring-shaped NL inputs and the relevant document is the function those queries describe — had the direction **backwards**: CoIR's CSN-Python reframing makes queries the full Python function sources and documents the docstrings extracted from those same functions. The docstring lives *inside* the query as a literal substring (the function's own `"""..."""` block). The BM25-beats-hybrid result on CSN-Python is therefore a **substring-leak artifact** of CoIR's dataset construction, not evidence of a query-class signal that an α-routing lever could exploit.

Concretely, query `q265734` is a 12-line Python function whose docstring is `str->list / Convert XML to URL List. / From Biligrab.`; document `c265608` (the matching qrel, score 1.0) is exactly those three lines. BM25 with identifier-aware tokenization wins because the answer string is in its input — no choice of α changes that.

With no alternative measured corpus showing the underlying phenomenon (a real "long, identifier-heavy NL query whose semantic ranking is genuinely weak"), the ADR closes as **Deprecated** without running the pre-implementation validation gate. This is a valid Proposed-ADR outcome: the validation discipline killed the proposal at the precondition step instead of the per-bucket-α-sweep step, which is cheaper and equally honest.

ADR-013 may be revisited if a future NL-to-code benchmark (CodeSearchNet's original NL-query split, or a curated workflow corpus where users paste code looking for tests) provides a motivating empirical anchor that survives precondition inspection. The Alternatives / Consequences sections above remain as the record of the design conversation — they're useful for whoever picks this up later, even though no implementation will follow from *this* ADR.

---

## ADR-014: `fs.FS` as canonical walker/indexer surface

**Status:** Accepted
**Date:** 2026-05-23

### Context
r/golang commenter on the ken release post asked whether the walker/indexer could take an `fs.FS` so the library could index any data store — `embed.FS`, `fstest.MapFS`, a tarball-backed FS, a git tree object, an in-memory snapshot — instead of only a directory on disk. Two named use cases were real:

- **Agent sandboxing.** `ken-mcp` exposes a search surface to LLM agents. If the underlying corpus is an `fs.FS` (e.g. `embed.FS`, a chroot-y view), the agent gets retrieval with no syscall-level escape path.
- **Offline analysis.** Index a tarball, a git tree, an in-memory snapshot without unpacking to disk first.

The pre-v0.5.0 walker (`internal/repo/walk.go`) and indexer (`internal/search/index.go`) called `filepath.WalkDir` + `os.ReadFile` + `os.Stat` directly. The gitignore matcher itself was already FS-agnostic; only file reads and directory traversal needed swapping. Tracking issue: [#6](https://github.com/townsendmerino/ken/issues/6).

### Decision
**Option B: `fs.FS` is the canonical surface; the real-FS entry points are kept as deprecated wrappers around `os.DirFS`.**

- `repo.WalkFS(fs.FS, Options) ([]string, error)` and `search.FromFS(fs.FS, Mode, chunker, modelDir) (*Index, error)` become the canonical API.
- `repo.Walk(opts)` and `search.FromPath(root, ...)` remain as one-line wrappers (`return WalkFS(os.DirFS(opts.Root), opts)` / `return FromFS(os.DirFS(root), ...)`) with `// Deprecated:` doc comments, scheduled for removal in a future minor release (pre-1.0 semver permits this).
- `Matcher` and `--watch` stay real-FS-only by construction (fsnotify is real-FS-only; ADR-012's watch path doesn't need an `fs.FS` lift).
- Private helpers (`isBinary`, `loadGitignore`) gain `FS` siblings (`isBinaryFS`, `loadGitignoreFS`); both real-FS variants stay since `Matcher.ShouldIndex` uses them.

### Alternatives considered
- **Option A: parallel funcs, both first-class.** Keep `Walk` and add `WalkFS`, both supported indefinitely. Rejected — doubles the public-API surface area forever, every future option needs to be added in two places, and the gap between which one "is the real API" decays into bit-rot. Deprecation gives the same backward compatibility for callers while keeping one canonical entry point.
- **No-op (status quo).** Implicit rejection — the use cases (sandboxing, offline analysis) are real and the cost (one wrapper, two helper twins) is small.

### Consequences
- **One canonical entry point.** New code goes through `WalkFS` / `FromFS`; old callers keep working via the wrappers.
- **`--watch` and `Matcher` remain real-FS-only.** Acceptable — neither sandboxing nor offline analysis needs incremental reindex. If a future use case wants `fs.FS` + watch (e.g. a polling driver over an `fs.FS` that doesn't support fsnotify), that's a separate ADR.
- **`ken-mcp` env-var config stays path-based for v0.5.0.** An MCP-side `fs.FS` integration (sandboxed-FS-only mode, exposed via a new env var or config) is a future change tracked separately.
- **`Options.Root` is now ignored by `WalkFS`.** Kept on the struct so the deprecated `Walk(opts)` signature stays stable; documented as "used only by the deprecated wrapper".
- **Zero new deps.** `fs` and `testing/fstest` are stdlib. Test surface gains an `fstest.MapFS` happy-path test and a `WalkFS` vs `Walk` parity test (and the equivalent pair for `FromFS` vs `FromPath`) to pin that the `os.DirFS` adapter doesn't drift from the historical real-FS behavior.

---

## ADR-015: Nested `.gitignore` support via scope stack on existing rule engine

**Status:** Accepted
**Date:** 2026-05-23

### Context
Field signal: a gobe user noticed `node_modules/` was polluting their ken search results in a monorepo. Their tree had no root `.gitignore`; instead, every package had its own `pkg/<name>/.gitignore` containing `node_modules/`. ken's walker — by explicit design, called out in the package doc comment since day one ("Only the root .gitignore is read; nested .gitignore files are not yet honored") — saw none of those per-package ignores and indexed every JS file under every node_modules tree. Tracking issue: [#5](https://github.com/townsendmerino/ken/issues/5).

With `fs.FS` now the canonical walker surface (ADR-014), the walker is the natural place to fix this. The existing handwritten `compileRule` regex engine already handles every pattern the gobe case (and every reported case so far) needs — what was missing was per-directory orchestration: which `.gitignore` files apply to which paths, with what precedence.

### Decision
**Extend the existing handwritten rule engine with a per-directory scope stack inside `WalkFS`.** Each `.gitignore` is loaded lazily as `fs.WalkDir` descends into its directory; rules are evaluated relative to the gitignore's directory; outer scopes evaluate first, inner scopes last, last-match-wins across the union. `Matcher` (used only by the watch path) gains the same nested awareness via a one-shot tree walk at construction time.

The new public/private surface:
- `scopedGitignore{dir, gi}` — a `*gitignore` paired with the slash-separated dir (`""` for root) whose patterns it owns.
- `pruneScopes(scopes, path)` — truncates the active scope stack to those whose `dir` is an ancestor of `path` (DFS-order guarantees this is a single `scopes[:i]` slice).
- `matchScopes(scopes, path, isDir)` — evaluates every applicable scope's rules against path-relative-to-scope, last-match-wins across the union. The per-rule loop is **deliberately inlined** rather than calling `(*gitignore).match` per scope: that helper resets its `ignored` state at every call, so calling it per scope would lose the union semantics (an outer "ignore `*.log`" would be silently forgotten by an inner scope that has no matching rule).
- `relToScope(path, scopeDir)` — returns path relative to scope, or `""` if path is not strictly under scopeDir.
- `collectGitignores(fsys)` — one-shot snapshot used by `NewMatcher`; respects outer-scope pruning so it doesn't descend into gitignored subtrees.

`Matcher.ShouldIndex` additionally simulates `WalkFS`'s `fs.SkipDir` behavior by asking `matchScopes` for each ancestor directory of the queried path with `isDir=true`. This fixes a pre-existing latent bug (independent of nested support): a root-level `build/` dir-only rule would historically NOT exclude `build/x.txt` when asked via `ShouldIndex` directly, because the dir-only filter skipped the rule on file paths. `WalkFS` never hit this because its dir-prune fired at the directory entry — `Matcher`, with no walk, had to synthesize the same pruning.

### Alternatives considered
- **Swap to `github.com/sabhiram/go-gitignore`** (the swap previously noted in `docs/DESIGN.md` §1 as a future option for "full pathspec parity"). Rejected — sabhiram parses a single `.gitignore` file. The per-directory orchestration (loading files, scope precedence, last-match-wins across the union) is still on us either way; sabhiram's win is edge-case pathspec parity *inside* the rule engine, not the nested behavior itself. With no field reports yet of patterns the handwritten engine actually misses, paying a new dependency for hypothetical edge-case parity isn't justified. Remains a documented future option.
- **Use `github.com/go-git/go-git/v5`'s `plumbing/format/gitignore`.** Rejected — its matcher expects `billy.Filesystem`, the wrong abstraction for an `fs.FS`-canonical walker. An adapter would be more code than the scope-stack solution.
- **No-op (status quo, document as a known limitation).** Implicit rejection — the gobe case is a real, reported field issue; the cost of a fix (one scope-stack pattern, ~60 LOC + tests) is well below any cost of having ken silently produce poor results on monorepos.

### Consequences
- **Closes [#5](https://github.com/townsendmerino/ken/issues/5).** The gobe scenario (per-package `node_modules/` in nested `.gitignore` files) is correctly pruned.
- **`Matcher` freshness caveat.** `NewMatcher` collects every `.gitignore` once at construction. `.gitignore` files added, modified, or removed after the watcher starts are **not** reflected in subsequent `ShouldIndex` calls — a full re-index (restart `ken index`) is required. Documented in the `Matcher` doc comment. The watch path is not the place to redo a tree walk on every event; an explicit watch-the-`.gitignore`-files mechanism is a future change if the need arises.
- **Pre-existing dir-only-on-files bug fixed.** `Matcher.ShouldIndex` now correctly excludes files inside dir-only-rule directories (e.g. `build/x.txt` under root `build/`). No CHANGELOG bump because the bug had no test surface and no field report; the fix lands as part of this ADR.
- **Full pathspec parity still a documented future option.** If a real-world `.gitignore` pattern surfaces that the handwritten engine misses (e.g. character classes, certain `**` corner cases), the sabhiram swap remains the planned path. The scope-stack orchestration designed here is engine-agnostic — sabhiram would slot in as a drop-in replacement for `parseGitignore`/`(*gitignore).rules`.
- **No new dependency.** Zero deps added; this entire change is one file edit in `internal/repo/walk.go` plus tests.
