# Decisions

Architecture Decision Records for `ken`, in chronological order. Each entry captures the decision, the alternatives considered, and the consequences — so future readers can understand *why* the codebase is shaped the way it is, not just *what* it does. Companion to [`docs/DESIGN.md`](DESIGN.md) (the atemporal design spec) and [`docs/BENCH.md`](BENCH.md) (empirical findings).

ADR statuses: **Accepted**, **Superseded** (replaced by a later ADR), **Deprecated** (no longer applies but kept for history).

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
- **Monotonic memory growth with edit volume.** Tombstoned chunks stay in `chunks` slice for index stability. A long-running ken-mcp session that edits every file many times accumulates tombstones at O(cumulative-edit-volume). Compaction is a v0.3.x trigger; for v0.3's short-session use the growth is acceptable. The bm25 docs for tombstoned chunks are emitted as nil token slices so df isn't bumped.
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
