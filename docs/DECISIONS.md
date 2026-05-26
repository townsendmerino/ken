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
| [ADR-016](#adr-016-embedded-corpus-mcp-build-pattern-via-mcprun-library-function) | Embedded-corpus MCP build pattern via `mcp.Run` library function | Accepted |
| [ADR-017](#adr-017-database-schema-indexing--two-tier-static-sql--live-postgres-with-documented-pii-stance) | Database schema indexing — two-tier (static SQL + live Postgres) with documented PII stance | Accepted |
| [ADR-018](#adr-018-sqlite-engine--migration-history-folding-via-lightweight-alter-replay) | SQLite engine + migration-history folding via lightweight ALTER replay | Accepted (extended by ADR-022) |
| [ADR-019](#adr-019-mysql-engine--schema-filtering-for-multi-schema-dev-databases) | MySQL engine + schema filtering for multi-schema dev databases | Accepted (extended by ADR-021) |
| [ADR-020](#adr-020-listennotify-push-based-schema-change-detection-v080-part-1) | LISTEN/NOTIFY push-based schema change detection + `reindex_db` MCP tool + opt-in `mcp/db` package for SDK authors (v0.8.0) | Accepted |
| [ADR-021](#adr-021-mariadb-first-class-engine-support-v081-part-b) | MariaDB first-class engine support (v0.8.1 Part B) | Accepted |
| [ADR-022](#adr-022-rename-column--rename-constraint-folding-via-eager-application-v081-part-c) | RENAME COLUMN + RENAME CONSTRAINT folding via eager application (v0.8.1 Part C) | Accepted |

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

---

## ADR-016: Embedded-corpus MCP build pattern via `mcp.Run` library function

**Status:** Accepted
**Date:** 2026-05-24

### Context

v0.5.0 (ADR-014) made `fs.FS` the canonical filesystem surface at ken's library layer — `repo.WalkFS`, `search.FromFS`, `embed.FS` and `fstest.MapFS` corpora all work. But the shipping MCP server (`cmd/ken-mcp`) still resolves agents' `repo` argument to a real path via `os.DirFS(path)` and indexes that, optionally cloning http(s) URLs to `$TMPDIR` first. The library affordance for agent-sandboxed retrieval and zero-infrastructure distribution exists; the product affordance — "an SDK author runs `go build` and ships a single static binary that serves search/find_related over their docs corpus, no model download, no per-query network egress" — does not.

Tracking issue: [#7](https://github.com/townsendmerino/ken/issues/7). Three downstream properties drove the framing:

- **Zero-infrastructure distribution.** `go install`, push a binary, users add one line to their MCP config. No backend, no vector DB, no "is the cache stale" question — the binary IS the corpus, version-pinned by build artifact.
- **Agent sandboxing by construction.** No path resolution code exists in the embedded-corpus build → no path-traversal escape is possible. The corpus is structurally sealed.
- **Air-gapped / restricted-egress dev environments.** All queries answered locally. For enterprise users this is the difference between "we can use this" and "we can't."

### Decision

Add a new public package, `github.com/townsendmerino/ken/mcp`, exposing **`Run(ctx context.Context, fsys fs.FS, opts Options) error`** as a **purely additive** library API for the embedded-corpus build pattern. `cmd/ken-mcp` retains its multi-repo / per-call URL-clone / file-watching behavior unchanged — the two modes coexist by design.

The two-mode framing:
- **Code search (`cmd/ken-mcp`).** Multi-repo, per-call path/URL resolution, fsnotify-driven live re-indexing (ADR-012), LRU cache + singleflight dedup. The agent's `repo` argument is honored. Existing behavior, env vars, tool surface, watch mode all preserved.
- **Docs serving (`mcp.Run`).** Single fixed corpus rooted at the caller-supplied `fs.FS`, model loaded from `Options.ModelFS` (typical use: `//go:embed model/*`) or `Options.ModelDir`, no watch (the corpus is static-by-construction at `Run` time), no cache. The agent's `repo` argument is accepted (wire schema unchanged so agents work) but logged-and-ignored.

Three supporting changes land alongside the new package:

1. **`embed.LoadFromFS(fs.FS, dir) (*StaticModel, error)`** — canonical entry point for model loading. The path-based `embed.Load(modelDir)` becomes a deprecated wrapper around `LoadFromFS(os.DirFS(modelDir), ".")`. Enables `Options.ModelFS` to load a Model2Vec snapshot baked into a binary via `//go:embed`.
2. **`internal/chunk/markdown` package** — a handwritten pure-Go scanner. Heading-aware boundaries (ATX + setext), atomic fenced-code / tables / lists, frontmatter (YAML `---` and TOML `+++`), byte-fidelity preserved. Registers as `"markdown"` in the chunker registry. Auto-falls back to the line chunker for non-markdown files in mixed-content corpora.
3. **Side-effect chunker imports move to binaries.** Previously `internal/search/index.go` blank-imported both `regex` and `treesitter`, which meant any package transitively importing `search` pulled in `gotreesitter/grammars` (a single `embed.FS` containing 19 MB of grammar blobs). Now `internal/search` only blank-imports `regex` (the default); `cmd/ken` and `cmd/ken-mcp` add `treesitter` and `markdown` explicitly. `cmd/ken-mcp-docs` blank-imports only `markdown`, so the docs binary doesn't carry the grammar bundle.

`cmd/ken-mcp-docs` is the canonical worked example: ~20 lines of `main.go`, gated by build tag `embed_corpus` (so a fresh clone — where `cmd/ken-mcp-docs/{model,docs}/` don't yet exist — still builds cleanly via `go build ./...`), staged + built via `scripts/build-docs-mcp.sh`.

### Alternatives considered

- **Shim path: refactor `cmd/ken-mcp` to be a thin wrapper around `mcp.Run`, dropping its multi-repo / per-call-URL-clone / watch / LRU-cache machinery.** Rejected. Would silently drop live watch behavior (real feature loss — agents lose visibility into mid-session file edits, ADR-012's whole purpose), force every existing `ken-mcp` deployment to migrate config or accept regressions, and replace a working dual-purpose binary with two binaries that together cover the same ground. The additive framing is also more honest: code search wants runtime flexibility + watch; docs serving wants embedded + sandboxed. They're different products living in the same library — better named separately than papered over.

- **Multi-corpus `mcp.Run(ctx, map[string]fs.FS, opts)`.** Rejected for v0.6.0: no field signal, complicates `Options` + tool semantics (the agent's `repo` arg would need to be honored as a key lookup, undoing the sandboxing-by-construction story), reversible (can add a `mcp.RunMulti` variant non-breakingly in v0.7+ if signal materializes), and hurts the marketing pitch ("a single corpus per binary" is the sharp one-liner). The rare "SDK + examples + changelog all bundled" case has an escape hatch: put everything in one `fs.FS` and accept un-scoped ranking.

- **Per-language treesitter sub-packages (the prompt's original §4).** Rejected as infeasible. `gotreesitter/grammars` embeds **all 17 grammar blobs** in a single package via `//go:embed grammar_blobs/*.bin`. The Go linker cannot dead-code-eliminate `embed.FS` payloads, so splitting our `internal/chunk/treesitter` wrapper into per-language sub-packages would still pull in the full 19 MB regardless of which sub-packages a binary imported. The binary-size win the prompt wanted lands instead via the chunker-registration refactor (item 3 above): the docs binary simply doesn't import `treesitter` at all. Per-language splitting would only pay off if `gotreesitter` itself were restructured upstream — out of scope, documented here for the next person tempted to retry it. The relevant upstream file is `gotreesitter/grammars/blob_source_embedded.go`; the `//go:embed grammar_blobs/*.bin` directive is line one. `go tool nm bin/ken-mcp-docs | grep grammar_blobs` confirms the symbols are present or absent.

  - *Investigated and rejected:* gotreesitter's `grammar_blobs_external` and `grammar_set_core` build tags. The first moves grammars out of the binary but breaks the single-static-binary promise that motivates ken-mcp-docs. The second is a whole-program tag (not per-import), targets an undocumented "core" subset, and would force every ken consumer to opt into the reduced grammar set. Neither addresses the actual goal (per-binary grammar selection).

- **Goldmark (or other CommonMark parser) for the markdown chunker.** Rejected. A ~100 KB compiled dependency for what a ~150-line handwritten scanner handles. CommonMark/GFM edge cases (HTML blocks, footnotes, link-reference definitions, def-lists) are renderer concerns, not chunking concerns — when our scanner misses them they degrade gracefully to text lines and either flow into the current section or trigger a paragraph split. Documented as a future swap if real-world markdown surfaces patterns the handwritten scanner mis-handles. Keeping the chunker handwritten preserves ADR-001's no-cgo / minimal-deps discipline.

- **CLI flag for embedded-corpus mode (`ken serve-embedded ...`).** Rejected as a category error: `mcp.Run` is the library form of an MCP server, not a search command. There is no "embedded mode" of the `ken` CLI — the CLI's job is to index real directories. The embedded-corpus pattern is for SDK authors writing their own `package main`, which then `go build`s into a redistributable binary.

### Consequences

- **`mcp.Run` is purely additive — no breaking changes.** Stock `cmd/ken-mcp` behaves byte-identically to v0.5.0: same env vars, same tool surface (`search` + `find_related` with the same arg schemas and the same semble-format output strings), same watch mode, same multi-repo path/URL resolution. `TestBinary_StdoutIsCleanJSONRPC` enforces the stdout-clean contract across both build paths.
- **`cmd/ken-mcp-docs` is 74 MB total** (build measured 2026-05-24). Stock `cmd/ken-mcp` is 42 MB; the docs binary trades ~17 MB savings from skipping the treesitter grammar bundle for the 62 MB of embedded model + 144 KB of embedded docs. The prompt's ~30-60 MB target was aspirational and didn't account for the Model2Vec snapshot's actual size (62 MB for `potion-code-16M`); shipping smaller would require a different / smaller model, tracked separately. For comparison context: a Docker image with the same capabilities would be larger by 100s of MB, and an Electron-wrapped equivalent would be larger by GBs.
- **SDK authors can ship docs as single-binary MCP servers.** The product story (`go build` → `brew install acme-docs-mcp` → users add one line to their agent config) works end-to-end as of v0.6.0. The integration smoke test (`cmd/ken-mcp-docs/main_test.go`, build tag `integration`) demonstrates the loop: build via the script, exec the binary, query via `sdk.CommandTransport`, get back hits from `docs/DESIGN.md` for a "Model2Vec embedding" query.
- **Markdown chunker improves docs retrieval quality vs the line chunker.** Heading-bounded sections + atomic code/tables/lists give the embedding model and BM25 cleaner units than 50-line windows. Quantifying the NDCG@10 improvement on a real docs corpus is a follow-up; for now the test suite (12 scenarios + byte-fidelity assertion per scenario) pins the structural correctness.
- **Code duplication between `cmd/ken-mcp` and `mcp.Run` is minimal.** Both share `internal/` infrastructure (search, embed, chunk, repo) and the tool handlers themselves (`runSearch` / `runFindRelated` in `mcp/server.go` are called from both the Cache-backed `handleSearch` / `handleFindRelated` and the embedded-corpus `newServerForIndex`). The divergence is just lifecycle/startup logic (~300 LOC).
- **The leveled logger (`mcp.Logger`, `mcp.LogLevel`, etc.) was moved from `cmd/ken-mcp/main.go` to the `mcp` package** as part of giving `mcp.Run` somewhere to send its diagnostics. `cmd/ken-mcp/env.go` now uses `kenmcp.Logger` and `kenmcp.ValidateEnum`; the env-var-helper test surface is unchanged.
- **Multi-corpus stays an open option for v0.7+.** If field reports surface (the "SDK + examples + tutorials bundled" case is the most likely shape), `mcp.RunMulti(ctx, map[string]fs.FS, opts)` or an `Options.Corpora` variant is the natural extension. The current single-corpus shape doesn't paint us into a corner.
- **No new third-party dependencies.** Everything lands in pure Go via `io/fs`, `embed`, the existing `go-sdk` MCP package, and the existing chunker / search / embed plumbing.

---

## ADR-017: Database schema indexing — two-tier (static SQL + live Postgres) with documented PII stance

**Status:** Accepted
**Date:** 2026-05-25

### Context

Agents working on a real codebase need schema context alongside code. An agent answering "how do users get authenticated" should retrieve, in one ranked result list, the Go function doing auth, the SQL queries it executes, the `users` table definition, and the FK relationships from `sessions.user_id`. Without ken, that's three separate tool calls — code search + DB connector schema lookup + (sometimes) query log inspection — and the results aren't co-ranked.

ken's differentiator vs. standalone Postgres-MCP connectors that give live state but don't rank with code: ken's hybrid BM25 + semantic ranking treats every retrievable unit as homogeneous text, so schema chunks compete with code chunks in the same scoring function. Tracking issue: [#8](https://github.com/townsendmerino/ken/issues/8).

v0.6.0 (ADR-016) established "ken indexes docs alongside code"; v0.7.0 extends to "ken indexes the context developers actually use — code, docs, schemas."

### Decision

**Two tiers, both shipping in v0.7.0:**

1. **Tier 1 — Static SQL parsing.** New `internal/sql` package parses `.sql` files in the corpus (CREATE TABLE / INDEX / VIEW, ALTER TABLE) and emits one denormalized "for retrieval" chunk per object. Activates automatically when `.sql` files are present; no opt-in. The structural chunks are ADDITIVE to whatever chunker is configured (so `.sql` files are still line-chunked too — agents can hit either form). Wired in `internal/search/chunkOneFile`, so both `cmd/ken-mcp`'s watch path and `mcp.Run`'s build-once path benefit.

2. **Tier 2 — Live Postgres introspection.** New `internal/db` package connects to `KEN_DB_DSN`, introspects via `pg_catalog` / `information_schema`, emits one chunk per table / view / index / function with a freshness header (`-- indexed at <UTC> from postgres@<host>`). Opt-in row sampling via `KEN_DB_SAMPLE_ROWS=N` (default 0 = schema-only). Three reindex layers: build-once-at-startup (default), periodic via `KEN_DB_REINDEX_INTERVAL`, manual via `SIGHUP` (unix-only; no-op on Windows). All three reuse ADR-012's atomic snapshot-swap machinery via the new `WatchedIndex.SetExtraChunks` method.

**Postgres only for v0.7.0.** Via `github.com/jackc/pgx/v5` — mature pure-Go driver, default `Tracer` is nil so no stdout-pollution risk, fits ADR-001's no-cgo discipline. MySQL (`github.com/go-sql-driver/mysql`) and SQLite (`modernc.org/sqlite`) share the same `internal/db` shape and land in follow-on point releases.

**DB chunks attach to the default repo's index, not multi-repo.** `KEN_DB_DSN` requires `KEN_MCP_DEFAULT_REPO` to be set and to be a local path (not an http(s) URL). When DSN is set but no default repo is configured, Tier 2 logs a warn and stays off — multi-repo searches (where each agent call names its own repo) don't have a clear semantics for "which repo should DB chunks attach to," and forcing operators to pick a default for DB integration is cleaner than guessing.

**PII stance: documentation + sane defaults, not engineered controls.**
- Schema-only is the default (`KEN_DB_SAMPLE_ROWS=0` ships unset).
- The opt-in env var name is unambiguous (`KEN_DB_SAMPLE_ROWS` reads as "rows you're choosing to expose").
- Every Tier-2 chunk carries a freshness header naming the engine + host but never credentials, so agent output naturally surfaces provenance.
- The README's DB section opens with a prominent callout: "intended for development databases. Do not point this at production data; sample rows will be visible to agents and thus to your LLM provider."

Engineered redaction controls (column-exclusion DSL, redaction modes, row synthesis) are NOT shipping. Operators who need those should not point ken at the DB.

### Alternatives considered

- **Column-exclusion DSL / redaction modes / row synthesis.** Rejected. Complexity tax for a single concern (PII), false sense of security (operators trust the controls instead of avoiding the integration entirely), inconsistent with ken's "small surface area" ethos. If you can't trust the database with the LLM, don't connect ken to it — there is no middle ground we can land safely.

- **One engine at a time across releases (Postgres v0.7.0, MySQL v0.7.1, SQLite v0.7.2).** Accepted. Scope discipline: one engine landing cleanly with full introspection + sampling + reindex layers + integration tests + CI service container > three half-baked. The `internal/db` package shape doesn't bake in Postgres-specifics outside `introspect.go` queries; adding MySQL means a sibling file with the equivalent SHOW/INFORMATION_SCHEMA queries.

- **Per-call DB introspection (lazy).** Rejected. Would tie tool-call latency to DB roundtrip + introspection cost; the agent's query result has to wait for the DB to respond on EVERY search. Build-once + atomic-snapshot-swap is consistent with the rest of ken (fsnotify-driven for FS, ticker-driven for DB).

- **LISTEN/NOTIFY for push-based change detection.** Deferred to v0.8.0. Requires DB-side event triggers and engine-specific wire-up. The three reindex layers we landed (startup + periodic + SIGHUP) cover the practical workflows (migrate-up + `kill -HUP $(pgrep ken-mcp)`); LISTEN/NOTIFY is the next ergonomic step but not the difference between "works" and "doesn't."

- **Agent-triggerable `reindex_db` MCP tool.** Deferred to v0.8.0. The tool needs rate-limiting / cooldown / opt-in toggle so an over-eager agent doesn't hammer the DB on every loop iteration. Better to wait for field signal on how agents actually use DB integration before designing the tool surface.

- **`mcp.Run` (embedded-corpus) DB support.** Deferred to v0.8.0+. Embedded-corpus binaries are static-by-construction; live DB connections don't obviously fit the "single static binary, no per-query egress" product story. If a use case materializes (e.g. an SDK author wanting to ship docs + a connection to their dev DB), `mcp.Options.DBSource` is the natural extension.

- **DB chunks shared across all repos** (one DB → many WatchedIndexes). Rejected. The composition gets ambiguous: agents searching repo A would see DB chunks irrelevant to that repo, and the snapshot-swap path would have to broadcast to every cache entry. The "default repo gets DB chunks; other repos don't" rule is the simplest semantics that's also the one most operators want (the default repo IS their working tree).

- **Migration-history folding** (assembling current state from CREATE TABLE + later ALTER TABLE across files). Out of scope for v0.7.0. Each statement becomes its own chunk; agents see the union of historical state. Devs who want a single "current state" view either use Tier 2 or maintain a canonical `schema/current.sql`. Documented future refinement.

### Consequences

- **New dependency: `github.com/jackc/pgx/v5`.** Pure Go, no cgo. Default Tracer is nil — no protocol logging to stdout, so the JSON-RPC stdout contract holds. `TestBinary_StdoutIsCleanJSONRPC_WithDB` enforces this by spawning `cmd/ken-mcp` with all `KEN_DB_*` env vars set and driving a real MCP session.
- **`cmd/ken-mcp` startup gains a conditional DB code path.** No-op when `KEN_DB_DSN` is unset (the existing `TestBinary_StdoutIsCleanJSONRPC` test confirms byte-identical v0.6.0 behavior). When set, the path pre-builds the default repo's `WatchedIndex`, runs an initial `db.IndexSchema`, and (optionally) spawns a periodic `Refresher.Run` + installs a SIGHUP handler. All failure modes are non-fatal — Tier 2 going dark is logged and the server continues with FS-only.
- **PII responsibility lives with operators.** README is explicit; freshness metadata in every chunk surfaces "this came from postgres@dev-pg" naturally in agent output. Schema-only is the default everyone sees on first launch.
- **Three reindex triggers share one atomic-swap path.** `WatchedIndex.SetExtraChunks` is the seam — both `db.Refresher` (periodic + SIGHUP) and the startup one-shot call it. No new snapshot-state machinery; ADR-012's invariants (reader sees a complete snapshot, writer publishes via atomic pointer) extend cleanly.
- **Tier 1 doesn't fold migration history across files.** Documented and intentional. Each statement is its own chunk; agents see the historical mutation as a retrievable unit, not as a synthesized "current state."
- **CI grows a Postgres service container** (ubuntu-only — GitHub Actions services don't run on macos-latest). The default `go test ./...` still works without one; the dbintegration tag gates the live tests and the CI yaml separates them into a second `test-db-integration` job.
- **`mcp.Run` (v0.6.0 embedded-corpus) is unchanged.** `mcp.Options` adds no DB fields. The `mcp/` tests still pass; the embedded-corpus path has no DB code reachable from it. Future DB support there is a separate v0.8.0+ ADR.
- **Tier 1's `.sql` structural parsing benefits `mcp.Run` too.** Lives in `internal/search` (which both binaries use), so an embedded-corpus binary that includes `.sql` files among its docs gets the structural chunks for free. Filesystem-based, not DB-based — doesn't violate the "no DB code in the embedded-corpus path" rule.

---

## ADR-018: SQLite engine + migration-history folding via lightweight ALTER replay

**Status:** Accepted. *RENAME COLUMN + RENAME CONSTRAINT folding shipped in v0.8.1 Part C; see [ADR-022](#adr-022-rename-column--rename-constraint-folding-via-eager-application-v081-part-c).*

**Date:** 2026-05-25

**Issue:** [townsendmerino/ken#9](https://github.com/townsendmerino/ken/issues/9)

### Context

[ADR-017](#adr-017-database-schema-indexing--two-tier-static-sql--live-postgres-with-documented-pii-stance) shipped v0.7.0 with two explicit out-of-scope items: Tier 2 supported only Postgres, and Tier 1 emitted one chunk per CREATE/ALTER statement rather than folding them into a "current state" view. Both gaps cluster on the same audience: SQLite-backed development workflows (Rails, Django, Phoenix, Laravel, FastAPI, embedded apps) are exactly where migration-driven schema management is most common, so projects that didn't get Tier 2 also got the worst per-file chunk explosion. Pairing the two pieces in one release lets the SQLite story land with the folding fix already in place.

v0.7.1 closes both gaps. SQLite indexing covers the missing engine; migration folding shrinks N+1 chunks per table down to one "current state" chunk.

### Decision

**Two pieces shipping together in v0.7.1:**

1. **SQLite engine in Tier 2.** New file `internal/db/sqlite.go` (sibling to `introspect.go`/`emit.go`/`sample.go`) implements `indexSchemaSQLite(ctx, opts, defaultRepoPath)` via `modernc.org/sqlite` — the C SQLite engine transpiled to Go, no cgo, single static binary preserved. Engine routing happens inside `IndexSchema` by parsing the DSN scheme: `postgres://` / `postgresql://` → `indexSchemaPostgres`; `sqlite://` / `sqlite3://` → `indexSchemaSQLite`. The shared `Options` + `Refresher` + `WatchedIndex.SetExtraChunks` machinery is engine-agnostic and reused unchanged.

   DSN forms: `sqlite:///abs/path.db` for absolute paths (triple slash: scheme + empty host + absolute path); `sqlite://./rel/path.db` for paths relative to `KEN_MCP_DEFAULT_REPO`. The relative form is the convenient case — SQLite files usually live in-repo. Introspection uses PRAGMA queries (`table_info`, `index_list`, `index_info`, `foreign_key_list`) for structured access, producing the same `tableInfo` / `viewInfo` shape as Postgres so `emit.go`'s renderers work without engine-specific branches. The freshness header shows the file basename only (`sqlite@dev.db`), not the full path, so chunks don't leak local filesystem layout.

2. **Tier-1 migration-history folding.** New file `internal/sql/fold.go` adds `IsMigrationDir(fsys, dir)` and `FoldMigrations(fsys, dir, logger)`. When `internal/search`'s walker detects a directory of numbered `.sql` files matching a recognized migration naming pattern (Goose / dbmate / Rails-4 `\d+_*.sql`, Flyway `V\d+__*.sql`, Rails-5 / Alembic `\d{14}_*.sql`), it replays the CREATE TABLE + later ALTER TABLE statements into a single "current state" chunk per table. The lightweight algorithm (parse ALTER → mutate in-memory column list → re-render) covers `ADD COLUMN`, `DROP COLUMN`, `ALTER COLUMN ... TYPE` (Postgres) / `SET DATA TYPE` (ANSI), `ADD CONSTRAINT`, `DROP CONSTRAINT`.

   **Partial-fold failures emit BOTH chunks.** When an ALTER can't be applied cleanly (unknown column from a RENAME elsewhere, missing CREATE TABLE for the referenced name, out-of-scope action like `RENAME COLUMN`), ken logs a warn AND keeps the original per-file ALTER chunk in the output, while still emitting the folded chunk for what could be resolved. Net: the agent sees the union; never less information than v0.7.0.

   **Opt-out:** `KEN_SQL_NO_AUTO_MIGRATIONS=1` (or `true` / `yes`) restores v0.7.0 per-file behavior. Operators who maintain a canonical `schema/current.sql` and don't want migration history surfaced separately set this. Default is folding-enabled.

**Validation surface added:**
- `envDSN` becomes a scheme-allow-list (`postgres`, `postgresql`, `sqlite`, `sqlite3`) rather than postgres-specific. The pattern extends cleanly for MySQL in v0.7.2.
- New `envBool` helper for `KEN_SQL_NO_AUTO_MIGRATIONS` validation, matching the warn-and-fallback pattern of the other env-var helpers.
- New `TestBinary_StdoutIsCleanJSONRPC_WithSQLite` confirms `modernc.org/sqlite` stays silent on stdout when the full Tier 2 code path runs in the spawned `cmd/ken-mcp` binary. Sibling of v0.7.0's `_WithDB` test; needs no service container (SQLite is file-based).

### Alternatives considered

- **AST-aware migration folding** (build a full in-memory schema model and replay every DDL operation against it). Rejected. AST folding would need a pure-Go SQL DDL parser that covers Postgres + MySQL + SQLite dialects, and **no such library exists today**. ANTLR-generated grammars require runtime grammar evaluation (heavyweight, large binary cost). `pg_query.go` wraps the libpg_query C library, violating ADR-001 (no cgo). Hand-rolling a DDL AST across three dialects is the work of a release on its own. The lightweight ALTER replay covers the 80% case (ADD/DROP/ALTER COLUMN, ADD/DROP CONSTRAINT) with zero dialect-specific parsing — every CREATE/ALTER routes through ken's existing `internal/sql.ParseFile`, which is dialect-agnostic by design. AST is the foundation for a future DDL linter; revisit when one of those pure-Go cross-dialect parsers materializes or when field signal demands the operations only AST can replay (RENAME COLUMN, complex CHECK replays).

- **Explicit `KEN_SQL_MIGRATIONS_DIR` env var instead of auto-detect.** Rejected. Safer (no wrong guesses), but adds a config step for every project. Auto-detection with `KEN_SQL_NO_AUTO_MIGRATIONS=1` opt-out handles ~80% of projects with zero config; an explicit override can land if real-world signal calls for it.

- **Wildcards in migration directory detection** (globbing across multiple subdirectories per repo, e.g. matching both `db/migrate/` and `services/x/migrations/`). Rejected: two directories each containing `\d+_*.sql` files produce **ambiguous folding order** — does `migrations/users/0042_alter.sql` come before or after `migrations/billing/0041_alter.sql`? Lexical-sort-across-dirs is undefined when the dirs are siblings rather than a single chain, and a wrong guess silently produces a folded chunk that doesn't match either dir's real schema state. If real monorepo signal arrives, the right recovery is **explicit naming** via something like `KEN_SQL_MIGRATIONS_DIR=migrations/users,migrations/billing` (each dir folded independently into its own table chunks) rather than guessing the cross-dir order from filesystem traversal. Auto-detection handles the common single-dir case at zero config cost; the explicit-list escape hatch is the v0.7.x-or-later release if needed.

- **AST-aware "current schema" view that fully replaces per-file chunks.** Rejected. Removes information — the migration history itself is sometimes the right answer to "when was this column added?". The folded chunk is ADDITIVE in failure modes (BOTH chunks on partial fold); in success modes the per-file ALTER chunks aren't emitted as structural chunks but the raw `.sql` is still line-chunked and BM25-searchable. Agents that want history can grep the raw text; agents that want current state get one clean chunk.

- **One engine per release (SQLite v0.7.1, then MySQL v0.7.2, then folding v0.7.3).** Rejected. Migration-driven workflows are most concentrated on SQLite — shipping the engine without the folding fix would deliver the worst experience to the audience that most needs it. Pairing them lets SQLite land with the per-file chunk explosion already addressed.

- **`mcp.Run` SQLite support** (embedded-corpus binary that opens a SQLite file on first request). Deferred to v0.8.0+ via `mcp.Options.DBSource`. The embedded-corpus product story is "single static binary, no per-query egress," which a live DB connection muddles. Tier 1's migration folding DOES benefit `mcp.Run` for embedded `.sql` files (filesystem-based, no DB code reachable).

- **DSN normalization** (rewrite `sqlite://./dev.db` to its absolute path inside `IndexSchema` so the rendered chunk shows the same path on every refresh). Rejected for v0.7.1. The freshness header already uses the basename only; the full path is internal. Relative paths can resolve differently if the operator moves their working dir between starts, but that's a one-line stderr warn ("file missing") rather than silent breakage.

### Consequences

- **New dependency: `modernc.org/sqlite`.** Pure Go, transpiled from C SQLite. Default behavior is silent on stdout (no Tracer-equivalent the way pgx has) — `TestBinary_StdoutIsCleanJSONRPC_WithSQLite` enforces this. Any future logging wiring routes through `Options.LogWriter` (stderr), never stdout. ADR-001 (no-cgo) preserved.
- **SQLite users get Tier 2 indexing.** Rails / Django / Phoenix / Laravel / FastAPI / embedded apps — the audience the v0.7.0 Postgres-only release explicitly didn't serve. Same chunk shape, same row-sampling / refresh / SIGHUP machinery, no operator-facing differences beyond the DSN scheme.
- **Migration-driven workflows on any engine get folded chunks.** Tier 1's folding is filesystem-based, not DB-based — Postgres / SQLite / future-MySQL Tier 2 paths all benefit because they index against the same `internal/search` walker.
- **The engine-routing pattern is established.** MySQL in v0.7.2 follows the same shape: sibling file `internal/db/mysql.go`, DSN scheme dispatch in `IndexSchema`. The `envDSN` allow-list extends with one entry.
- **Partial-fold failures never lose data.** The BOTH-chunks rule means agents never see less than they did under v0.7.0. The worst case is one extra chunk for the agent to disambiguate (per affected ALTER); the best case is N+1 chunks collapse to 1. **v0.8.1 Part C follow-on:** RENAME COLUMN + RENAME CONSTRAINT — explicitly listed in the v0.7.1 out-of-scope set above — are now folded eagerly in v0.8.1 Part C; the BOTH-chunks pattern carries over for genuine failures (missing source column from operator typo; anonymous constraint with no name to match; MySQL `CHANGE` syntax). See [ADR-022](#adr-022-rename-column--rename-constraint-folding-via-eager-application-v081-part-c) for the eager-application design + 7 rejected alternatives + the calibration-credibility framing (Tier-1 chunk fidelity, not retrieval recall).
- **CI: SQLite tests run in the existing `test-db-integration` job.** No new service container needed (SQLite is file-based). The job's name updates to reflect both engines (`ubuntu / DB integration (Postgres + SQLite)`). The default `go test ./...` job runs the SQLite stdout-clean test too (which uses a temp .db file, no env required).
- **`mcp.Run` (v0.6.0 embedded-corpus) is unchanged.** No new `mcp.Options` fields. Tier 1's migration folding DOES apply when embedded corpora include `.sql` files in a migration directory; no operator action required.
- **Freshness header policy for SQLite.** Basename only (`sqlite@dev.db`), never the full path. Operators who need full provenance grep stderr logs. Audited by `TestSQLiteIntegration_FreshnessHeader_BasenameOnly`.

---

## ADR-019: MySQL engine + schema filtering for multi-schema dev databases

**Status:** Accepted. *MariaDB compatibility promoted to first-class in v0.8.1; see [ADR-021](#adr-021-mariadb-first-class-engine-support-v081-part-b).*

**Date:** 2026-05-25

**Issue:** [townsendmerino/ken#11](https://github.com/townsendmerino/ken/issues/11)

### Context

[ADR-017](#adr-017-database-schema-indexing--two-tier-static-sql--live-postgres-with-documented-pii-stance) shipped v0.7.0 with Postgres-only Tier 2 and everything-in-the-database getting indexed. [ADR-018](#adr-018-sqlite-engine--migration-history-folding-via-lightweight-alter-replay) added SQLite and folded migrations in v0.7.1. Two follow-ons remain in the "polishes Tier 2" track:

1. **MySQL.** The third common dev engine (after Postgres and SQLite) and the obvious next one after v0.7.1 established the engine-routing pattern. Rails / Django / Laravel / .NET / classic LAMP-stack workflows are all MySQL-shaped, and operators in those communities have been waiting since v0.7.0 for Tier 2 to cover them.

2. **Schema filtering.** Production-cloned dev DBs accumulate noise — `audit` schemas with append-only log tables, `cron` / `queue` schemas from background-job machinery, `legacy` / `archived` schemas of deprecated tables, per-tenant schemas in multi-tenant SaaS. Today ken indexes all of them, which adds chunks the agent doesn't want and creates ranking pressure from tables the agent shouldn't suggest using.

v0.7.2 pairs them because both are Tier 2 ergonomic improvements that ship cleanly together. After v0.7.2 the v0.7.x engine + Tier-2-polish track is complete: Postgres + SQLite + MySQL all supported, schema filtering available for the engines that need it, migration folding for Tier 1. v0.8.0 becomes the next-features release (LISTEN/NOTIFY for Postgres push-based change detection, agent-triggerable `reindex_db` MCP tool, `mcp.Run` DB support) without engine-completion overhang.

### Decision

**Two pieces shipping together in v0.7.2:**

1. **MySQL engine in Tier 2.** New file `internal/db/mysql.go` (sibling to `postgres.go`-equivalent `introspect.go` / `sqlite.go`) implements `indexSchemaMySQL(ctx, opts)` via `github.com/go-sql-driver/mysql` — the standard pure-Go MySQL driver, no cgo, mature and well-maintained. Driver default logger writes to stderr by audit (`log.New(os.Stderr, "[mysql] ", ...)`); `cmd/ken-mcp` additionally reroutes the stdlib `log` package as belt-and-suspenders. `TestBinary_StdoutIsCleanJSONRPC_WithMySQL` pins the stdout-cleanliness contract — any future driver upgrade that switches to stdout would fail loudly.

   **DSN forms — accept both URL and native:** `mysql://user:pass@host:3306/db?parseTime=true` (URL, canonical, matches Postgres pattern) AND `user:pass@tcp(host:3306)/db?parseTime=true` or `user:pass@unix(/sock)/db?...` (native go-sql-driver shape). The native form has no scheme prefix; `envDSN` detects it via the `@tcp(` / `@unix(` substring markers that the driver documents. Both produce the same internal `*mysql.Config` — operators paste whatever their tooling gave them. URL form is rewritten to native via a small parser in `mysqlURLToNative` before handoff to `mysql.ParseDSN`. `parseTime=true` is force-set on the config regardless of operator input, since otherwise DATE/DATETIME/TIMESTAMP columns return `[]byte` and don't render cleanly in row samples.

   **Compatibility:** the same driver works against MySQL 5.7+, MySQL 8.x, and MariaDB 10.x+. CI tests against `mysql:8`; MariaDB is documented as wire-compatible without first-class CI testing.

   **Introspection via `INFORMATION_SCHEMA`:** tables, columns (including `column_type` for full forms like `varchar(255)`, `extra` for AUTO_INCREMENT), primary keys + foreign keys + UNIQUE markers (`table_constraints` + `key_column_usage`), indexes (`statistics` aggregated by index_name), views (`views.view_definition`, truncated at 50 lines per the Postgres policy), stored procedures + functions (`routines` + `parameters`; signature only, no body — same policy as Postgres). Triggers fold into the parent table's chunk (same as SQLite v0.7.1; triggers are almost always table-scoped). Chunk shape identical to Postgres / SQLite modulo the engine label in the freshness header.

   Engine routing in `internal/db.IndexSchema` uses a small `dsnEngine(dsn)` helper that returns `"postgres"`, `"sqlite"`, `"mysql"`, or `""`. The native MySQL form's lack of a scheme prefix is the only reason this helper exists — for v0.7.0 + v0.7.1 a simple `schemeOf` was sufficient.

2. **Schema filtering via `KEN_DB_SCHEMAS` + `KEN_DB_EXCLUDE_SCHEMAS`.** New `internal/db.Options` fields `IncludeSchemas` and `ExcludeSchemas` plus a canonical `filterSchema(name, engine, opts) bool` helper. Resolution order:

   1. Engine default exclusions (`pg_catalog`, `information_schema`, `mysql`, `performance_schema`, `sys`, plus the `pg_*` prefix family for Postgres temp / toast schemas) — ALWAYS rejected. Not user-controllable.
   2. `opts.IncludeSchemas` non-empty → keep iff schema is in the list.
   3. `opts.ExcludeSchemas` non-empty → reject iff in the list.
   4. Otherwise → keep.

   When both env vars are set, `cmd/ken-mcp` logs a stderr warn (`"KEN_DB_SCHEMAS and KEN_DB_EXCLUDE_SCHEMAS both set; allow-list wins, deny-list ignored"`) and zeros `ExcludeSchemas` before passing to `db.Options`. `filterSchema` also enforces this precedence library-side so callers bypassing `cmd/ken-mcp` get the documented behavior. Non-existent schema names in `IncludeSchemas` are NOT errors — introspection queries return zero rows for them, allowing operators to pre-configure for schemas that will exist after a migration.

   Postgres and MySQL introspection paths run every schema name through `filterSchema` per-row (in table/view/function listing AND in inverse-FK annotation, so a filtered-out schema's tables never appear as `FK referenced by:` entries on kept tables). SQLite is a single-schema engine and ignores the env vars; `cmd/ken-mcp` logs a debug message when they're set with a SQLite DSN so operators see that ken noticed.

**Validation surface added:**
- `envDSN` allow-list extends from 4 schemes (v0.7.1) to 5 schemes + native MySQL form. Validation: native form requires `@tcp(` or `@unix(` substring; URL form requires a non-empty host for `postgres` / `postgresql` / `mysql` schemes (SQLite continues to allow empty host).
- New `envCommaList` helper for the schema-list env vars. Whitespace trimmed around each element; empty elements (from `"a,,b"` or trailing commas) dropped silently — no warn path since well-formed input is the common case.
- New `TestBinary_StdoutIsCleanJSONRPC_WithMySQL` confirms `go-sql-driver/mysql` stays silent on stdout when the full Tier 2 code path runs in the spawned `cmd/ken-mcp` binary. Third sibling of the Postgres + SQLite tests; needs the same `mysql:8` service container the integration suite uses.

### Alternatives considered

- **Replace default exclusions instead of extend** (operator's `KEN_DB_EXCLUDE_SCHEMAS` would replace the engine system-schema list rather than be added to it). Rejected. Operators don't know what the system schemas are. Allowing `KEN_DB_EXCLUDE_SCHEMAS=public` to also remove `pg_catalog` would silently break introspection for anyone who pastes a "schemas to exclude" list without understanding what's already excluded — and re-add the entire `mysql.user` schema (which contains credentials) to the index for any operator who pastes a deny-list of just the schemas they personally don't care about. Extending preserves the safety floor while giving operators the additive control they actually want. Future v0.7.x+ `KEN_DB_INCLUDE_SYSTEM_SCHEMAS=1` escape hatch is a one-line ADR if field signal asks for it; speculative complexity until then.

- **Wildcards in v0.7.2** (e.g. `KEN_DB_SCHEMAS=tenant_*`). Rejected for v0.7.2; documented for later. Mechanism: glob syntax has many flavors (shell vs SQL-LIKE vs regex), and choosing one without field signal risks shipping the wrong one — operators will end up with subtly different syntax across the schema-filter env vars and any future `KEN_DB_INCLUDE_TABLES`-style wildcards. Multi-tenant SaaS operators can fall back to explicit `KEN_DB_SCHEMAS=tenant_001,tenant_002,...` lists as a workaround until v0.7.x+ adds wildcards informed by real usage.

- **Both env vars compose as intersection** (deny-list filters subset of allow-list — keep only schemas in `IncludeSchemas` AND not in `ExcludeSchemas`). Rejected. Concrete failure mechanism: under intersection semantics, `KEN_DB_SCHEMAS=public,billing` + `KEN_DB_EXCLUDE_SCHEMAS=billing,audit` silently produces a one-schema index of just `public` — the operator's `audit` exclusion gets credit for filtering a schema that was already excluded by the allow-list, and the operator's `billing` allow-list entry gets silently overridden by the deny-list. Two lists pasted from different sources (an ops doc + a CLAUDE.md) compose into a result neither author intended, with no warning. Allow-list-wins + stderr warn surfaces the conflict at startup loudly, so the operator either fixes their config or accepts the documented precedence — the same loud-failure shape ADR-009 establishes for every other env-var conflict. If real field signal arrives that intersection is what operators actually want, ADR-020 can add a `KEN_DB_FILTER_MODE=intersection` opt-in.

- **Per-table inclusion/exclusion** (`KEN_DB_INCLUDE_TABLES=users,sessions`-style filtering). Rejected. Two mechanism-level problems. **First, the FK-graph consistency tax**: filtering at the table level produces dangling references — keep `sessions` but exclude `users`, and the `sessions` chunk's `→ users(id)` arrow points at a name absent from the index, defeating the "agent sees the whole shape" thesis. Schema-level filtering avoids this because FK targets across filtered-out schemas are also filtered out via `annotateFKReferences`'s `filterSchema` pass (which already exists). Table-level filtering would need a new "rewrite dangling FKs as `→ <filtered>`" code path that no operator has asked for. **Second, the cross-engine identity problem**: a table name like `users` exists in many schemas in any multi-tenant dev DB; `KEN_DB_INCLUDE_TABLES=users` would need a schema-qualifier syntax (`tenant_001.users`) that recapitulates schema-level filtering with extra punctuation, OR match the bare name across schemas with ambiguous results. Schema-level filtering is the 80% case for production-cloned dev DBs (the audit / cron / per-tenant / legacy pattern); operators with table-level needs can either (a) move noisy tables into a `legacy` schema and filter that, or (b) accept the noise. If a real signal arrives — e.g. a multi-tenant operator who genuinely wants to index three tables out of three hundred per tenant — the right design is probably a glob-on-fully-qualified-name syntax that solves both problems together, not bare table names.

- **Treating MariaDB as a separate engine** (separate `mariadb://` scheme + sibling `internal/db/mariadb.go`). Rejected. Wire-compatible via the same `go-sql-driver/mysql` driver; the `INFORMATION_SCHEMA` queries used by `indexSchemaMySQL` are standard SQL. Adds CI matrix cost (separate service container) for marginal correctness benefit. If MariaDB-specific `INFORMATION_SCHEMA` differences surface in practice (e.g. `routines.data_type` semantics diverge), file separately and consider a `mariadb://` scheme alias at that point — the engine-dispatch shape supports adding one in a few lines.

- **`mcp.Run` MySQL support** (embedded-corpus binary that opens a live MySQL on first request). Deferred to v0.8.0+ via `mcp.Options.DBSource` — same scope rule as v0.7.0's Postgres and v0.7.1's SQLite deferrals. The embedded-corpus product story is "single static binary, no per-query egress," which a live DB connection muddles. Tier 1's migration folding DOES benefit `mcp.Run` for embedded `.sql` files (filesystem-based, no DB code reachable).

- **Native MySQL DSN form detection by `mysql.ParseDSN` round-trip** (try to parse every input that doesn't have a `://` prefix with `mysql.ParseDSN`; accept iff it parses). Rejected. Too permissive: a random string like `host=foo` would `ParseDSN`-parse as a database name with no user, no host, and we'd disable Tier 2 only after attempting a connection at runtime. The `@tcp(` / `@unix(` substring check is the documented native-DSN marker and provides a loud rejection at startup — matching the libpq-key-value rejection from v0.7.1.

### Consequences

- **New dependency: `github.com/go-sql-driver/mysql` v1.10.0.** Pure Go, no cgo, mature and widely used. Default package-level logger writes to stderr via `log.New(os.Stderr, ...)`; no protocol-level logging to stdout. ADR-001 (no-cgo) preserved. The pgx-tracer-must-stay-nil discipline (`feedback_pgx_tracer` memory) extends identically — any future wiring of `mysql.SetLogger` must route through `Options.LogWriter` (stderr), never stdout.

- **MySQL users get Tier 2 indexing.** Rails / Django / Laravel / .NET / classic LAMP-stack dev workflows — the audience the v0.7.0 + v0.7.1 releases explicitly didn't serve. Same chunk shape, same row-sampling / refresh / SIGHUP machinery, no operator-facing differences beyond the DSN scheme. Operators paste either URL or native DSN forms; both work.

- **Operators with multi-schema dev databases get clean filtering.** Production-cloned dev DBs (audit / cron / per-tenant schemas) get a clean way to filter without losing the default-exclusion safety floor. Both env vars set → loud warn + allow-list wins, no silent intersection surprise.

- **Default exclusions are inviolable.** `KEN_DB_SCHEMAS=pg_catalog` does NOT add `pg_catalog` to the index — `filterSchema` rejects it before consulting the user list. Same for `mysql`, `information_schema`, `performance_schema`, `sys`. Operators who genuinely need to index system schemas should not point ken at the DB.

- **CI matrix expands by one service container.** `mysql:8` service alongside `postgres:16-alpine` in the existing `test-db-integration` job. SQLite still needs no container. The job name updates to `ubuntu / DB integration (Postgres + SQLite + MySQL)`. Adding a sixth engine in v1.x+ follows the same pattern: sibling service entry + `KEN_DB_<ENGINE>_TEST_DSN` env var + `dbintegration` test file + `_With<Engine>` stdout test.

- **`envDSN` becomes a five-scheme-plus-native-form parser.** The native-form path (substring check, no scheme prefix) is the first time we accept a DSN without a `://` — it's the only engine for which the native form is widely used in operator-facing tooling. Adding a sixth engine in v1.x+ that uses URL form follows the same `dsnAcceptedSchemes` allow-list extension.

- **v0.7.x track complete after this release.** Postgres + SQLite + MySQL Tier 2; migration folding for Tier 1; schema filtering for the engines that need it. v0.8.0 design starts without engine-completion overhang.

- **`mcp.Run` (v0.6.0 embedded-corpus) is unchanged.** No new `mcp.Options` fields. Live DB support for `mcp.Run` is v0.8.0+ scope.

- **MariaDB compatibility is documented, not first-class tested.** Operators pointing ken at MariaDB 10.x+ should hit the same code path with the same chunk shape. If `INFORMATION_SCHEMA` differences surface (likely candidate: `routines.dtd_identifier` rendering for parameterized return types), file separately and consider a per-engine variant. **v0.8.1 Part B follow-on:** the predicted `dtd_identifier` divergence was real (along with column-level integer display widths from the same MariaDB-vs-MySQL-8.0 fork lineage); [ADR-021](#adr-021-mariadb-first-class-engine-support-v081-part-b) records the audit + the unconditional-normalization fix + the CI matrix expansion that makes MariaDB first-class.

---

## ADR-020: LISTEN/NOTIFY push-based schema change detection (v0.8.0 Part 1)

**Status:** Accepted (Parts 1, 2, and 3 all shipped; v0.8.0 ready to tag)

**Date:** 2026-05-25

**Issue:** [townsendmerino/ken#12](https://github.com/townsendmerino/ken/issues/12)

### Context

[ADR-017](#adr-017-database-schema-indexing--two-tier-static-sql--live-postgres-with-documented-pii-stance) shipped Tier 2's three reindex layers in v0.7.0 — startup-once, periodic via `KEN_DB_REINDEX_INTERVAL`, and manual via SIGHUP — all of which are pull-based. Long-lived deployments waiting for the next interval tick see stale schemas in the meantime. Operators running an active migration cycle and then asking an agent "does this match the schema?" hit the window between the migration committing and the next tick firing, where the agent's view is the pre-migration shape.

Postgres has native push notifications via `LISTEN` / `NOTIFY` + event triggers (since 9.3). We should use them.

v0.8.0 is the "operator-control-loop" release; this ADR's section covers Part 1 (LISTEN/NOTIFY). The other two v0.8.0 features — an agent-callable `reindex_db` MCP tool, and `mcp.Run` DB support via `Options.DBSource` — extend this ADR in their own sections in follow-on commits.

### Decision

**Postgres-only LISTEN/NOTIFY push notifications, supplementing the interval ticker.**

1. **Operator-provided SQL setup script.** ken does NOT modify the operator's database without explicit consent. The setup is a one-time `ken-mcp print-listen-script | psql $KEN_DB_DSN` that installs a single schema-level event trigger (`ken_schema_changed_trigger`) firing `pg_notify('ken_schema_changed', ...)` on tracked DDL (`CREATE / ALTER / DROP` for `TABLE`, `INDEX`, `VIEW`, `MATERIALIZED VIEW`, `FUNCTION`, `TRIGGER`, `TYPE`). The script is embedded into the binary via `//go:embed scripts/postgres_listen_notify.sql` so it's versioned with the release; idempotent via `DROP IF EXISTS` + `CREATE` so re-running is safe.

2. **Single schema-level event trigger.** Postgres event triggers fire on `ddl_command_end`; one trigger covers the entire database for the lifetime of the install. No per-table setup, no maintenance burden when new tables are added. The trigger's plpgsql function walks `pg_event_trigger_ddl_commands()` and emits one `NOTIFY` per object — informational only; the listener doesn't parse the payload (always re-introspects everything on any notify).

3. **`internal/db.Listener` type with dedicated pgx connection.** Listener uses `pgx.Connect` to open a single dedicated `*pgx.Conn` separate from the introspection path. A long-running `WaitForNotification` call must not tie up a connection that `IndexSchema` needs.

4. **Activation via `KEN_DB_LISTEN=1`.** Default off — operators who haven't run the setup script shouldn't accidentally activate the listener and hit the confusing "trigger missing" warn. `envBool` validation; non-Postgres DSNs return `ErrListenNotSupported` from `NewListener` and `cmd/ken-mcp` debug-logs and skips (per the v0.7.2 "SQLite ignores schema filtering" pattern).

5. **Supplements `KEN_DB_REINDEX_INTERVAL`, doesn't replace.** Both can run concurrently. The `Refresher`'s internal mutex serializes refreshes, so a NOTIFY arriving mid-tick collapses cleanly. Interval polling continues as defense-in-depth backstop catching missed notifications (network partition, reconnect window).

6. **Exponential-backoff reconnect (100ms → 30s cap).** On any connection-level error, log a warn naming the error + the next backoff interval, sleep, and reconnect. Backoff resets on a successful "LISTEN active" — so a flaky network that disconnects mid-loop every minute doesn't drift to 30s waits. The reconnect re-runs the trigger-existence check (handles DB-restore-from-backup-without-trigger and operator-dropped-trigger scenarios).

7. **Setup-not-run handling.** Trigger-existence check via `SELECT EXISTS(SELECT 1 FROM pg_event_trigger WHERE evtname = 'ken_schema_changed_trigger')`. If missing, log the warn once per (re)connect attempt (NOT in a hot loop) naming the exact fix command, then idle until ctx done. Operators see the warn at startup, fix it, and on the next reconnect (which fires when the connection eventually drops for any reason) listening resumes.

8. **50ms debounce window.** First notification in a burst starts a 50ms drain window; additional notifications arriving within the window are collapsed into the same refresh. A migration file with `CREATE TABLE foo; CREATE INDEX foo_idx ON foo(email); ALTER TABLE foo ADD COLUMN ...` fires three notifications in fast succession; debouncing produces one refresh, halving DB load for no observable agent-side latency change.

### Alternatives considered

- **Auto-install the event trigger on first connect.** Rejected. ken would modify the operator's DB without explicit consent — same loud-failure-over-silent-success principle ADR-009 establishes for env-var validation. Operators should know what's being installed in their DB; the `print-listen-script` subcommand makes it scriptable + reviewable + auditable (the operator can read the script before piping to psql, version-control the install in their migrations dir, or apply it via their own migration tooling). The friction of one extra command is the right trade for the "ken doesn't surprise you" posture.

- **Per-table opt-in via `KEN_DB_LISTEN_TABLES=users,sessions`.** Rejected. Requires per-table state we don't maintain — we re-introspect everything on any notify, so per-table opt-in would only affect which tables can FIRE notifications, not which are indexed. A user querying "what about the audit table?" after a migration would get stale data because the listener filtered the audit table's NOTIFY out. The single schema-level trigger is the right scope for our refresh strategy.

- **Replace `KEN_DB_REINDEX_INTERVAL` entirely.** Rejected. NOTIFY connections can drop silently — network partition, server restart with brief window before reconnect, mid-NOTIFY connection abort during a deploy. Without interval polling as defense-in-depth, an undetected silent drop produces unbounded stale-schema windows ("ken hasn't refreshed since the listener died last Tuesday"). Interval polling at e.g. 5m is a small cost; pairing it with LISTEN/NOTIFY gives the operator best-of-both: ~100ms latency in the happy path + bounded staleness in the failure path.

- **No debouncing — refresh on every notification.** Rejected. A real-world migration file with `CREATE TABLE foo; CREATE INDEX ...; ALTER TABLE ...` emits three notifications within ~10ms. Without debouncing we'd run three full introspection passes in ~100ms (each refresh's `IndexSchema` does a network round-trip to query `information_schema.*`), doubling+ DB load for no observable agent-side benefit. The 50ms window is short enough that "live during agent conversation" feels instant and long enough to coalesce the typical migration-file burst.

- **Faked MySQL LISTEN via polling triggers.** Rejected. MySQL has no native push notification mechanism (it has triggers, but they fire only on row-level events, not DDL; even if you wrote a row-level trigger on a "schema_changes_log" table that operators were supposed to populate manually, you'd be re-implementing event triggers in user space with worse ergonomics). The right path for MySQL operators is `KEN_DB_REINDEX_INTERVAL` — and they already have it. Adding a half-baked MySQL "listen" emulation would be more confusing than the honest "Postgres only; MySQL users use interval polling" doc note.

- **Debounce window as a tunable env var (`KEN_DB_LISTEN_DEBOUNCE=100ms`).** Deferred. 50ms is a reasonable default; if real-world workloads need different (e.g. an operator with a multi-stage migration pipeline that batches 200 DDLs across 30 seconds), file separately. Speculative complexity until field signal arrives.

- **Parse the NOTIFY payload to do per-object incremental refresh** (only re-introspect the named table). Rejected for v0.8.0. The IndexSchema pass is already cheap (~hundreds of ms on typical dev DBs); the engineering complexity of per-object incremental indexing — keeping track of which chunks correspond to which object, handling cross-object dependencies like FK arrows, atomic-swap correctness when only some objects changed — is large. The payload is parsed for diagnostics (logged at debug level) but not for refresh routing. Revisit if a real-world DB scales to thousands of tables where full refresh becomes noticeable.

### Consequences

- **Postgres dev workflows with active migration cycles see ~100ms schema-change-to-refresh latency** instead of waiting up to `KEN_DB_REINDEX_INTERVAL`. "I just ran the migration, please check this query" works without an interval-tick wait.

- **One-time setup friction.** Operators must run the setup script once. The friction is the right trade for the consent posture; the README's "Indexing database schemas" section calls out the command prominently.

- **New dedicated pgx connection per ken-mcp process when `KEN_DB_LISTEN=1`.** Postgres handles thousands of idle LISTEN connections easily; the resource cost is negligible. The connection is isolated from the introspection path so a stuck `WaitForNotification` can't starve `IndexSchema`.

- **Stdout-cleanliness discipline extends to the listener's connection.** Audited via `TestBinary_StdoutIsCleanJSONRPC_WithListen` (fifth sibling of the stock / Postgres / SQLite / MySQL stdout tests). The pgx-tracer-must-stay-nil rule from the `feedback_pgx_tracer` memory applies identically — listener's `pgx.Connect` defaults Tracer to nil; any future wiring routes through `Options.LogWriter` (stderr), never stdout.

- **Single event trigger covers future tables for free.** Adding `tenant_005` schema after install needs no per-tenant re-install — the trigger fires on any tracked DDL anywhere in the database.

- **Engine-routing pattern preserved.** MySQL and SQLite go through the same `dsnEngine` check; `NewListener` returns `ErrListenNotSupported` for them and `cmd/ken-mcp` debug-logs + skips. Adding a future engine that DOES support push notifications follows the same shape: a new `Listener` variant with engine-specific connection + watch loop, dispatch in `NewListener`.

- **Three reindex triggers converge on one entry point.** Interval ticker, SIGHUP handler, and LISTEN listener all call `Refresher.Refresh(ctx)`. The Refresher's mutex serializes — no concurrent IndexSchema calls, no swap-callback races. v0.8.0's `reindex_db` MCP tool (Part 2) will be the fourth trigger source on the same path.

### Part 2: agent-callable reindex via `reindex_db` MCP tool (v0.8.0 Part 2)

**Date:** 2026-05-25

### Decision (Part 2)

**Agent-callable reindex via a new `reindex_db` MCP tool. The tool calls `Refresher.TryRefresh(ctx)` — fail-fast on contention via `sync.Mutex.TryLock`, not block-and-wait.**

1. **New `Refresher.TryRefresh(ctx) error` method** in `internal/db`. Uses `r.mu.TryLock()`; returns `ErrReindexInProgress` (new exported sentinel) if the mutex is held; otherwise behaves identically to `Refresh`. Refactor splits the shared body into an unexported `doRefresh(ctx)` so `Refresh` and `TryRefresh` differ ONLY in lock-acquisition strategy.

2. **Five trigger sources, one entry point.** Startup (one-shot in `wireDBTier2`), `KEN_DB_REINDEX_INTERVAL` ticker, SIGHUP, LISTEN/NOTIFY listener — all four call `Refresh` (blocking on mutex; their semantics genuinely want to serialize, not skip). The fifth, `reindex_db`, calls `TryRefresh` — agent-callable, fail-fast.

3. **No new env vars.** The tool is registered automatically whenever `KEN_DB_DSN` is set (more precisely: whenever `wireDBTier2` returns a non-nil `*db.Refresher`). When no DB is configured, the tool is NOT registered — `tools/list` won't show it, keeping the agent's tool surface honest. Operators who want to disable the tool unset `KEN_DB_DSN` (no DB tier at all) or run a separate ken-mcp process.

4. **`mcp.ReindexFunc` callback shape.** New `mcp.Config.Reindex ReindexFunc` field. `cmd/ken-mcp` wires a closure that calls `*db.Refresher.TryRefresh` and translates `db.ErrReindexInProgress` into `ReindexResult{InProgress: true}`. The result-struct shape (rather than an error sentinel) keeps the `mcp` package free of `internal/db` imports — Part 3's `mcp.Run` DBSource will reuse the same callback shape with its own implementation.

5. **`cmd/ken-mcp/main.go` order change.** `wireDBTier2` now runs BEFORE `NewServer` (instead of after) and returns the `*db.Refresher` so it can be wired into the server's Config at construction time. The five other args / env vars / behaviors are unchanged; only the call order moved.

6. **Plain-text response shape, matching `search` / `find_related`.** Three message templates — `Reindexed in Nms.` (success), `Reindex already in progress; nothing to do.` (contention), `Reindex failed: <err>` (real failure). semble's MCP wire format is plain text, not structured JSON; we match.

7. **Argument-free for v0.8.0.** `ReindexDBArgs` is an empty struct. Async return / per-engine selectors / repo selectors are deferred per the alternatives below — adding them later is additive (extending the args struct without breaking the wire format).

### Alternatives considered (Part 2)

- **Time-based cooldown (`KEN_DB_REINDEX_MIN_INTERVAL=10s` or similar) instead of an in-flight lock.** Rejected. A cooldown either drops calls during the cooldown window or queues them; both produce a worse agent experience than the honest "already in progress" signal. Concrete failure: an agent running `reindex_db` in a poll-then-search loop hits a 10s cooldown and either (a) silently sees stale results during the wait (the agent thinks the refresh ran), or (b) gets queued calls landing 10s + queue-depth seconds later (the agent thinks "refreshed" was instant). The in-flight lock is fundamentally different from a cooldown — it ONLY blocks during an actual in-flight refresh, which is itself rate-limited by the natural cost of `IndexSchema` (~hundreds of ms for typical dev DBs). Agents that hammer the tool see a recoverable, named signal they can adapt to.

- **Queue reindex requests during in-flight refresh.** Rejected. Queue depth is unbounded by design (an agent in a loop could submit hundreds of calls in seconds), and bounding it just shifts the rate-limit problem to "what happens at the cap?" Returning `Reindex already in progress` short-circuits the entire queue-management surface — the agent decides whether to retry, back off, or ignore. Concrete failure mode prevented: an agent that calls `reindex_db` in a tight loop while watching for a column to appear would queue thousands of refresh requests in seconds; each refresh then runs against a slightly different DB state, the swap callback fires thousands of times, and the agent's "next search" sees the same intermediate state regardless. Fail-fast makes the agent's loop self-rate-limit on the recoverable signal.

- **Async return + poll (`reindex_db` returns `task_id`; separate `reindex_status` tool to poll).** Rejected for v0.8.0. Concrete cost: two MCP tools instead of one, two round-trips per reindex (call + poll), agent-side state machine for polling and retry. Concrete benefit: agent doesn't block during refresh. The typical refresh is <500ms on dev-scale DBs — blocking the agent for that long is acceptable in exchange for the simpler protocol. If a real-world DB has >5s reindex times where async matters, the right path is incremental refresh (per ADR-020 Part 1's deferred payload-parsing-for-incremental-refresh alternative), not async return — async just papers over slow refresh with worse UX.

- **Env var to disable the tool (`KEN_DB_DISABLE_REINDEX_TOOL=1`).** Deferred. Operators who don't want agents triggering reindexes today have two paths: unset `KEN_DB_DSN` entirely (drops the whole Tier 2 surface), or run a separate ken-mcp process for agents that need reindex separate from agents that shouldn't. If a real-world deployment surfaces a "partial DB exposure" need (agents can read schema but not refresh), a `KEN_MCP_REINDEX_DB_TOOL=0` env var is a cheap follow-on — but speculative complexity until field signal arrives.

- **Auto-call `reindex_db` from inside `search` / `find_related` when the query mentions DDL-shaped keywords.** Rejected. Heuristic-based auto-refresh fires on false positives — an agent asking `search "find the CREATE TABLE statement for users"` wants the existing chunk where that statement is recorded (in a migration file), NOT a refresh of the live DB schema. Concrete failure: a refresh inside what the agent thinks is a read-only query adds 100-500ms of unexpected latency, possibly breaks the agent's timeout budget, and surprises the agent's user with stderr "Tier 2: reindexed" lines they didn't expect. Explicit-tool-invocation keeps refresh under the agent's deliberate control.

- **Have `reindex_db` BLOCK on the mutex instead of fail-fast.** Rejected. Concrete failure: an agent calls `reindex_db` while a long-running refresh is in flight (e.g. a Postgres DB with hundreds of tables where introspection takes 30s). Block-and-wait means the agent's tool call hangs for 30s before returning a result, very likely tripping the agent's per-tool timeout and surfacing as "tool failed" — worse than the honest `Reindex already in progress; nothing to do.` (the response matches the actual tool wording — the in-flight refresh lands soon; the agent should proceed with its next `search` / `find_related` read rather than retrying `reindex_db` in a loop). Fail-fast lets the agent make an informed choice (proceed with the upcoming-fresh data, ignore, or surface to user).

### Consequences (Part 2)

- **Five trigger sources now converge on the Refresher** (was three at v0.7.0, four after Part 1, five after Part 2): startup, interval ticker, SIGHUP, LISTEN/NOTIFY listener, and `reindex_db` MCP tool. The first four call `Refresh(ctx)` (blocking on mutex); the fifth calls `TryRefresh(ctx)` (mutex.TryLock; `ErrReindexInProgress` on contention). The Refresher's internal serialization is unchanged from v0.7.0 — `TryRefresh` is a thin wrapper over `r.mu.TryLock + doRefresh`.

- **Agents that hammer `reindex_db` in tight loops see `already in progress` responses** — a recoverable signal the agent (or its author) can adapt to. No silent queueing, no unbounded memory growth, no hidden rate-limit semantics. The agent's planner chooses between back-off-and-retry, ignore-and-proceed-with-stale-data, or surface-to-user. Bugs in agent code (poll-then-search loops without sleep) become loud at the rate-limit signal level rather than silently producing inconsistent results.

- **`mcp.Run` (v0.6.0 embedded-corpus) does not yet support live DB**, so `reindex_db` is not exposed in embedded-corpus deployments built via `mcp.Run`. Part 3 of v0.8.0 lifts this limitation by adding `mcp.Options.DBSource` — the same `ReindexFunc` callback shape Part 2 introduced will be the seam.

- **`mcp` package stays free of `internal/db` imports.** `Config.Reindex` is typed as `ReindexFunc` (a function returning `ReindexResult`), not `*db.Refresher` or an interface backed by sentinel-error matching. cmd/ken-mcp does the small bridging closure (`reindexCallback`). This preserves the layering invariant that `mcp` is the user-facing library API and `internal/db` is the concrete Tier 2 implementation — neither imports the other directly.

- **Tool surface scales honestly with operator capability.** When `KEN_DB_DSN` is unset, `reindex_db` is not in `tools/list` at all. Agents that haven't been told ken has a DB don't see a tool that would always return "no DB" — they simply see two tools (`search`, `find_related`) instead of three.

- **Stdout-cleanliness count rises to six.** `TestBinary_StdoutIsCleanJSONRPC_WithReindexDB` drives a real `reindex_db` tool call through `sdk.CommandTransport` against postgres:16-alpine; the existing five tests only call `search`. Defense-in-depth against a future regression where someone adds a `log.Print` to the reindex callback without thinking about which writer it goes to.

- **No new dependencies.** Part 2 is pure-Go stdlib additions (`sync.Mutex.TryLock` exists since Go 1.18); no new modules in `go.mod`.

- **Backwards compatibility:** stock `cmd/ken-mcp` with `KEN_DB_DSN` unset is byte-identical to v0.7.2 + v0.8.0 Part 1. All six stdout-cleanliness variants pass; all existing integration tests still pass; the v0.8.0 Part 1 listener path is unchanged (still uses `Refresh`, not `TryRefresh`).

### Part 3: opt-in `mcp/db` package preserving v0.6.0 binary-size contract (v0.8.0 Part 3)

**Date:** 2026-05-25

### Decision (Part 3)

**A new opt-in `mcp/db` package — separate import path from `mcp` — that lets SDK authors using `mcp.Run` (the v0.6.0 embedded-corpus entrypoint) wire Tier 2 DB support without forcing every `mcp.Run` user to pay the DB driver binary-size cost.**

1. **New `mcp/db` package** (import path: `github.com/townsendmerino/ken/mcp/db`, package name: `mcpdb`). Exposes `Config` struct (mirrors the v0.7.x env-var surface from `cmd/ken-mcp`), `Setup(ctx, cfg) → (mcp.ReindexFunc, func(), error)`, and `ListenNotifyScript` (re-export of `internal/db.ListenNotifyScript`). The package name `mcpdb` avoids collision with `internal/db` in any file that imports both for testing.

2. **`mcp` package stays DB-free.** Zero new imports in `mcp`. The Part 2 `mcp.ReindexFunc` callback type from `mcp/server.go` is the seam; Part 3 adds an `mcp.Options.Reindex ReindexFunc` field (zero-cost — same type, just a new field on `Options`) and conditional `reindex_db` tool registration in `newServerForIndex`. No DB driver, no `internal/db` import. The v0.6.0 binary-size contract is preserved by construction.

3. **`internal/db.SetupTier2` extraction** — the pure-Tier-2-mechanics lifecycle (initial IndexSchema + Refresher construction + interval ticker + LISTEN/NOTIFY listener + cleanup) extracted into a shared helper. Both `cmd/ken-mcp/wireDBTier2` (env-var driven) and `mcp/db.Setup` (config-struct driven) call it; the CLI- and SDK-specific concerns (env-var parsing, SIGHUP, swap-target wiring, logger interface) stay in their respective callers. One source of truth, two surfaces.

4. **Empty-DSN safety net.** `Setup(ctx, Config{DSN: ""})` returns `(nil, nil, nil)` — not an error. SDK authors with conditional DB configuration (`if os.Getenv("MY_DB_DSN") != "" { ... }`) can call `Setup` unconditionally and let nil DSN gate the behavior. The returned nil `ReindexFunc` propagates through `mcp.Options.Reindex` to a nil cfg.Reindex inside `mcp.Run`'s `newServerForIndex`, which skips `reindex_db` tool registration.

5. **`mcp/db.ListenNotifyScript` re-exports `internal/db.ListenNotifyScript`.** SDK authors building their own CLI binary can expose a `print-listen-script` subcommand without depending on `internal/db` directly — the re-export honors the `internal/` boundary while giving SDK authors a stable public API surface for the script bytes.

6. **Chunk integration into `mcp.Run`'s embedded `*search.Index` is end-to-end** (addendum landed before v0.8.0 tagged). The pipeline: `mcp.Run` wraps the build-time `*search.Index` in `atomic.Pointer[search.Index]` and, when `opts.DB != nil`, calls `opts.DB.Start(ctx, onExtras)` with an `onExtras` closure that calls `baseIx.WithExtraChunks(extras)` and atomic-stores the result. Search handlers read via `ixPtr.Load()` so each agent query sees the latest snapshot, including DB chunks from the most recent refresh. The "replace, not accumulate" semantics of `WithExtraChunks` mean each refresh produces a clean union of the original corpus + the latest DB chunks (not a chained accumulation). See the `Index.WithExtraChunks` godoc for the rebuild contract.

7. **Binary-size invariant enforced by Go test.** `TestBinary_MCPPackageStaysDBFree` in `mcp/binary_contract_test.go` shells out to `go list -deps github.com/townsendmerino/ken/mcp` and asserts none of `github.com/jackc/pgx`, `modernc.org/sqlite`, `github.com/go-sql-driver/mysql`, or `github.com/townsendmerino/ken/internal/db` appear in the transitive dep set. Sibling test asserts `mcp/db` DOES bring those deps (catches a future refactor that accidentally detaches the opt-in package from the implementation). Both run in the default `go test ./...` invocation; CI catches a contract violation before merging.

### Alternatives considered (Part 3)

- **Single-import API: `mcp.Options.DBSource *DBSource` in the `mcp` package itself.** Rejected. The `mcp` package would import `internal/db`, which transitively pulls pgx + modernc.org/sqlite + go-sql-driver/mysql into every binary that uses `mcp.Run`. Go's linker can't dead-code-eliminate a referenced package even behind a nil check at the call site, and the SQL drivers register via `init()` so they bloat the binary unconditionally. Concrete impact: SDK authors building docs-only embedded-corpus binaries (the v0.6.0 use case) would see ~10MB+ binary bloat for code they don't run. The package-split design preserves the binary-size contract at the cost of one extra import line and one extra function call for DB-using SDK authors — the right trade. Verified via `TestBinary_MCPPackageStaysDBFree`.

- **Blank-import provider registration (the `database/sql` pattern: `import _ "github.com/lib/pq"`).** Rejected. The pattern is genuinely clever for SQL driver registration but loses static type checking — SDK authors who forget the blank import get a runtime "DB support not registered" error instead of a compile-time signal. The explicit-helper pattern (`mcpdb.Setup(cfg)`) keeps everything statically typed: missing import means missing symbol means compile error. SDK authors grep-friendly; `mcpdb.Setup` is a clear API call. Concrete failure prevented: a copy-pasted SDK example without the blank import that compiles and runs but silently lacks DB support.

- **Build tag (`-tags mcpdb` for the opt-in).** Rejected. Cross-cutting build tags are surprising for SDK authors who follow standard `go build ./...` invocations — they'd get the "wrong" behavior (no DB) without an obvious signal in the source. Build tags are right for "test-only" code (`-tags integration`) or "OS-specific" code (`//go:build linux`); they're wrong for "opt-in feature" gating where the SDK author's import-time choice should be the activation signal. Concrete failure prevented: an SDK author copies the README's Setup example, runs `go build`, and gets a "symbol not defined" error because they didn't pass `-tags mcpdb`.

- **Require SDK author to construct `*internal/db.Refresher` directly and pass it in.** Rejected. Leaks the `internal/db` package boundary into the public API. Go's `internal/` convention exists precisely so the package's surface can change without breaking external callers; making it the public seam invalidates that protection — a future v0.8.x change to `internal/db.Refresher`'s shape would break every SDK author. The `mcp/db.Setup` helper owns the public surface; `internal/db` stays internal and can evolve freely.

- **Per-engine sub-packages (`mcp/db/postgres`, `mcp/db/mysql`, `mcp/db/sqlite`).** Deferred. Engines aren't separable today — `Refresher` is engine-agnostic via the `dsnEngine` dispatch in `internal/db.IndexSchema`, and SDK authors who want all three engines would be back to ~10MB+ binary cost anyway. Splitting `mcp/db` into three sub-packages would multiply the API surface (3× `Config`, 3× `Setup`) without obvious benefit. Concrete failure prevented: SDK authors confused about which sub-package to import when they don't know in advance whether their operators' DSN will be Postgres / MySQL / SQLite. If a future release adds an engine with substantially different setup requirements (e.g. a streaming-only engine with no INFORMATION_SCHEMA equivalent), revisit the sub-package split then.

- **Hybrid: `mcp.Options.DBSource *DBSource` as a convenience wrapper that internally calls `mcp/db.Setup`.** Rejected. Two paths to document, two surfaces SDK authors choose between, and the convenience path STILL requires `mcp` to import `mcp/db` (which imports `internal/db`) — defeating the entire binary-size purpose. The package-split design has a single SDK author surface; the cost is one import + one function call. Operator confusion would also increase: which path is "official"? When do you use which?

### Consequences (Part 3)

- **v0.6.0 binary-size contract preserved.** `mcp` package's transitive dep tree is unchanged across the v0.6.0 → v0.8.0 arc — same imports, same drivers, same binary size for SDK authors building docs-only embedded-corpus binaries. The contract is enforced by `TestBinary_MCPPackageStaysDBFree`; a future commit accidentally adding a DB import to `mcp/` fails CI before merging.

- **One additional import + one function call** is the SDK author cost for DB-using `mcp.Run` deployments. Documented in the README's "Indexing database schemas → Embedded DB support" subsection with the canonical example.

- **All three v0.8.0 parts converge on one implementation.** The `internal/db.SetupTier2` extraction makes the convergence load-bearing: same Refresher + TryRefresh + reindex_db + LISTEN/NOTIFY path serves both `cmd/ken-mcp` (env-var-driven CLI) and `mcp.Run` SDK authors (config-struct-driven). One source of truth for Tier-2 lifecycle behavior; two thin wrappers for the surface-specific concerns.

- **`mcp/db.Setup` is the single SDK author seam for Tier 2.** Interval ticker, LISTEN/NOTIFY listener, reindex_db tool registration via the returned `ReindexFunc`, cleanup lifecycle — all flow through it. SDK authors don't see `Refresher`, `Listener`, or any `internal/db` type directly.

- **`mcp/db.ListenNotifyScript` re-exports `internal/db.ListenNotifyScript`** so SDK authors can build their own `print-listen-script` CLI subcommand without depending on `internal/db` directly. Matches the pattern `cmd/ken-mcp` established in Part 1 — same script content, exposed through whichever public API the caller is at.

- **Chunk integration into `mcp.Run`'s embedded `*search.Index` is end-to-end** (addendum landed before v0.8.0 tagged). Concrete state in v0.8.0:
  - `Refresher.Start` runs the initial IndexSchema synchronously, firing `onExtras` with the initial chunks before returning.
  - The Refresher's swap callback receives subsequent chunk sets on interval / LISTEN / reindex_db invocations. In `cmd/ken-mcp` the callback is `WatchedIndex.SetExtraChunks` (chunks land in the searchable snapshot via the pre-warm cache's `WatchedIndex`); in `mcp.Run` the callback wraps `*search.Index.WithExtraChunks` + atomic-pointer store (the addendum's new primitive).
  - The `reindex_db` tool returns the standard `Reindexed in Nms.` response. Across both `cmd/ken-mcp` and `mcp.Run + mcp/db.Setup` binaries, the agent's next `search` call sees the post-refresh schema in results.

  **How the addendum closed the gap:** Part 3's initial ship sketched the API surface (`Setup` returns a `ReindexFunc` that registers `reindex_db`) but the swap callback was a no-op stand-in — chunks captured but not searchable. The addendum (a) added `*search.Index.WithExtraChunks([]chunk.Chunk) *Index` as the rebuild primitive (returns a freshly-built immutable Index containing original ∪ extras; receiver unchanged), (b) added `model *embed.StaticModel` + `vecs [][]float32` retention on `*Index` so the rebuild can re-encode extras under hybrid/semantic mode, (c) replaced Part 2's `mcp.ReindexFunc` with a new `mcp.DBIntegration` interface bundling chunk-integration (`Start`) and tool-invocation (`TryRefresh`), and (d) wired `mcp.Run` to hold the Index in `atomic.Pointer[search.Index]` and store the rebuilt snapshot on each swap. `WatchedIndex.SetExtraChunks` (cmd/ken-mcp's fsnotify-rooted in-place mutation path) is unaffected — the asymmetry (`Set` on `WatchedIndex`, `With` on `Index`) reflects the different mutation models (in-place mutex-protected vs immutable atomic-swap).

- **`cmd/ken-mcp` refactored to use `SetupTier2`.** The `wireDBTier2` function shrunk by ~50 lines as the interval-ticker + listener wiring moved into `SetupTier2`. SIGHUP wiring stays in `cmd/ken-mcp` (it's a CLI concern, not an SDK concern). Backwards compatibility verified: same env vars, same log lines (the `Tier 2: indexed N DB chunks into %q` line is preserved by composing it via the caller's `onSwap` wrapper).

- **Stdout-cleanliness contract extends to the SDK author path.** `mcp/db/run_integration_test.go`'s `TestRun_WithMCPDBReindex_Binary` is the seventh stdout-cleanliness audit in spirit (sixth in name; the sixth slot is Part 2's `_WithReindexDB`). The test builds the mini-binary at `mcp/db/testdata/embedded-with-db/main.go`, spawns it via `sdk.CommandTransport`, and drives a real `reindex_db` call — if anything in the `mcp.Run + mcp/db.Setup` code path writes to stdout, the JSON-RPC roundtrip fails and the test fails loudly.

- **9 new tests across 3 packages.** `internal/db/setup_test.go` (6 SetupTier2 unit tests), `mcp/db/setup_test.go` (7 unit tests for Setup, Config validation, and ListenNotifyScript re-export), `mcp/db/run_integration_test.go` (2 binary integration tests covering the SDK-author path with-DSN + without-DSN), `mcp/binary_contract_test.go` (2 binary-size invariant tests). Plus 1 new mini-binary at `mcp/db/testdata/embedded-with-db/main.go` (~80 lines including comments) — the canonical SDK author example, used as the test subject.

- **No new dependencies.** Part 3 is pure-Go internal refactoring + new package; `mcp/db` brings in `internal/db` which already had pgx + modernc.org/sqlite + go-sql-driver/mysql at v0.7.2. No `go.mod` changes.

- **Backwards compatibility:** stock `cmd/ken-mcp` behaves byte-identically to v0.8.0 Part 2; `mcp.Run` with `opts.Reindex == nil` (the v0.6.0 → v0.7.2 default) behaves byte-identically to v0.7.2's `mcp.Run`. All six stdout-cleanliness variants from Parts 1 + 2 pass; the new mcp/db binary tests are additive.

- **v0.8.0 ready to tag.** Parts 1 (LISTEN/NOTIFY), 2 (reindex_db tool), and 3 (mcp/db package) all shipped on branch `v0.8.0-listen`; the three reindex-trigger sources + agent-callable refresh + SDK-author opt-in package compose into the "operator-control-loop" release narrative.

---

## ADR-021: MariaDB first-class engine support (v0.8.1 Part B)

**Status:** Accepted

**Date:** 2026-05-25

**Issue:** v0.8.0 release notes pre-announce ("v0.8.1 — MariaDB first-class engine"); ADR-019's deferred-with-rationale MariaDB compatibility claim.

### Context

[ADR-019](#adr-019-mysql-engine--schema-filtering-for-multi-schema-dev-databases) shipped v0.7.2's MySQL engine with MariaDB compatibility documented but not CI-tested. The reasoning at the time: MariaDB is wire-compatible via the same `go-sql-driver/mysql`, `INFORMATION_SCHEMA` is standard SQL across both, and the CI matrix cost of a third service container was speculatively-justified for a claim no operator had asked us to load-bearing-prove. ADR-019's Consequences section explicitly flagged the bet — "If `INFORMATION_SCHEMA` differences surface (likely candidate: `routines.dtd_identifier` rendering for parameterized return types), file separately and consider a per-engine variant."

v0.8.0's release notes pre-announced v0.8.1 as the release that would convert ADR-019's compatibility claim into load-bearing-tested first-class support. v0.8.1's calibration-release framing names the gap explicitly: ADR-019 says "MariaDB compatible," and v0.8.1 makes that testable + verified.

### Decision

**Three pieces shipping together in v0.8.1 Part B:**

1. **CI matrix expansion** (always, regardless of audit findings). New `mariadb:11-jammy` service container alongside `postgres:16-alpine` + `mysql:8` in the `test-db-integration` job. New `KEN_DB_MARIADB_TEST_DSN` env var parallel to `KEN_DB_MYSQL_TEST_DSN`. The existing `internal/db/mysql_integration_test.go` suite is parameterized over both engines via subtests — same fixture, same assertions, byte-identical chunks across engines. New `TestBinary_StdoutIsCleanJSONRPC_WithMariaDB` (seventh sibling of the stdout-cleanliness contract suite) drives a full MCP session through `sdk.CommandTransport` against the live MariaDB service.

2. **Divergence audit + finding classification.** Loaded an identical fixture (tables + indexes + views + scalar functions + procedures, the last two exercising the ADR-019-flagged `routines.dtd_identifier` path) into MySQL 8.4 and MariaDB 11.3, ran `IndexSchema` against both, diffed the rendered chunks character-by-character. Three buckets of divergence emerged:

   - **Tier 1 — Integer display widths.** MariaDB 11.x still emits the legacy `bigint(20)` / `int(11)` syntax on integer-family columns + scalar-function return types + procedure parameter types. MySQL 8.0 deprecated and removed them (MySQL bug #80094); MySQL 8.4 returns the bare `bigint` / `int` form. ADR-019's predicted `dtd_identifier` divergence was real — and it extended to every integer column in every table chunk, not just routines. **Highest-impact tier**: affects almost every chunk that has integer columns.

   - **Tier 2 — DEFAULT expression rendering.** MariaDB preserves SQL-literal fidelity: a column declared `DEFAULT 'guest'` is reported as `'guest'` (quoted); `DEFAULT CURRENT_TIMESTAMP` is normalized to `current_timestamp()` (lowercase + parens). MySQL 8.x post-processes both: strips simple-string quotes (`'guest'` → `guest`), normalizes function names (`current_timestamp()` → `CURRENT_TIMESTAMP`). Affects DEFAULT-bearing columns only — a smaller chunk fraction than Tier 1.

   - **Tier 3 — View body parenthesization.** MariaDB's view-rewrite pass strips redundant parens (`on(cond)`); MySQL keeps them (`on((cond))`). Affects view chunks only.

3. **Targeted normalization on Tier 1 only.** `normalizeMySQLIntType()` strips `(N)` from integer-family type strings via a regex (`\b(bigint|int|mediumint|smallint|tinyint)\(\d+\)`). Applied unconditionally at three read sites in `mysqlListTablesAndColumns` + `mysqlListRoutines` (column types, function return types, parameter types). Idempotent on MySQL 8.x output (regex matches nothing); corrective on MariaDB output. Modifiers downstream of the type are preserved (`bigint(20) unsigned` → `bigint unsigned`). Non-integer families left alone — their `(N)` is semantic (`varchar(255)` size, `decimal(10,2)` precision/scale, etc.).

   Tier 2 + Tier 3 divergences are documented as known cosmetic-but-substantive differences ken does NOT currently normalize. Reasoning: quote-stripping risks mangling escaped values; function-name normalization risks clashing with user-defined function names; view-body parenthesization needs a SQL parser. The cost of getting any of these wrong is silent-corruption of chunk text. If field signal shows agents are confused by the Tier 2/3 differences, v0.8.x+ can add narrower normalization.

### Alternatives considered

- **Separate `EngineMariaDB` constant in `dsnEngine` dispatch, with separate `mariadb://` DSN scheme.** Rejected. Most operators don't know whether their "MySQL" deployment is MariaDB until something breaks; forcing a per-variant DSN scheme makes the operator perform engine-detection that ken can do for itself. Wire-compatibility means a single DSN works; the (only) divergence we found can be normalized at the introspection-result-handling layer without operator-visible config changes.

- **Treat MariaDB as a separate engine entirely** (separate `internal/db/mariadb` package). Rejected. Two engine implementations 99% identical — the maintenance burden of keeping them in sync outweighs the cleanliness of separation. The divergence we found is per-query-result, not per-query — same query against both engines returns the same row count + shape, only the string-rendering of certain columns differs.

- **Probe engine variant at connection time via `SELECT VERSION()`, branch introspection on the cached variant.** Rejected even though the audit found substantive divergence. Mechanism: `MariaDB`-containing version string → cache `EngineMariaDB` on the pool struct → wherever the divergent column gets read, branch on cached variant and apply MariaDB-specific normalization. Why rejected: the chosen normalization (`normalizeMySQLIntType`) is engine-agnostic-safe — idempotent on MySQL output, corrective on MariaDB output. A probe-and-branch would make the variant branch dead code today, since both branches would call the same normalization. The probe mechanism stays available as a documented extension hook for v0.9.0+ if a divergence emerges that genuinely needs engine-specific code paths (different INFORMATION_SCHEMA queries, fundamentally-different value semantics).

- **Pre-emptive per-engine dispatch without finding divergence first.** Rejected by ADR-019's setup — speculative complexity until field signal arrives. The audit gave us field signal; the audit's findings showed unconditional normalization beats per-engine-branch on the divergence type we found.

- **Normalize all three tiers** (strip DEFAULT-expression quotes + normalize function names + reformat view bodies). Rejected for v0.8.1. The integer-width normalization is a single-regex transform with a well-defined input domain and zero risk of mangling semantically-significant content. The Tier 2 transforms have mangling risk — a column default of `'O\'Hara'` could get its quote-stripping wrong, a user-defined function named `current_timestamp` could get its call site replaced. The Tier 3 transform needs SQL-grammar awareness to know when to drop parens. ADR-021's calibration-release framing favors honest documentation of known cosmetic differences over risky-normalization-now; v0.8.x+ can extend if real signal shows the cosmetic differences hurt agent task completion.

- **Test against MariaDB locally but skip CI matrix expansion.** Rejected. ADR-019's "compatible but not first-class tested" claim sits in the same shape as ADR-001's "no cgo" — both are load-bearing claims that need CI enforcement to stay true. Local-only testing leaves the claim in the same state ADR-019 left it: aspirational. The whole calibration-release point is converting aspirational claims into load-bearing-tested ones.

- **Document the divergence in ADR-019 without code changes.** Rejected. The Tier 1 divergence affects almost every chunk — leaving it unnormalized means operators switching between MySQL and MariaDB get visibly-different chunks for the same schema, which breaks the cross-engine consistency v0.7.2's chunk-shape contract implicitly promised.

### Consequences

- **ADR-019's MariaDB compatibility claim is now CI-tested.** Future commits that break MariaDB compatibility fail CI before merge. The audit + this ADR record both the predicted divergence (`dtd_identifier`, correctly predicted) AND the previously-unpredicted broader integer-width divergence at the column level.

- **MariaDB users get the same chunk shape as MySQL users.** No operator-visible config changes — `KEN_DB_DSN` continues to route both engines identically. The normalization makes `bigint(20)` from MariaDB look identical to `bigint` from MySQL 8.x, so the same fixture produces byte-identical chunks regardless of engine.

- **`KEN_DB_MARIADB_TEST_DSN` is CI / testing only.** End users continue using `KEN_DB_DSN`. The new env var exists solely so the integration test suite can run against both engines in CI without requiring operators to know two DSN env vars.

- **CI matrix slot added.** `mariadb:11-jammy` joins `postgres:16-alpine` + `mysql:8` in the `test-db-integration` job. Small CI time cost (<2 minutes added). Healthcheck uses `mariadb-admin ping` (the version-stable name post-fork; `mysqladmin` would still work but tracks an older lineage).

- **Documented audit findings.** ADR-021 records all three tiers — Tier 1's normalization, Tier 2 + Tier 3's deliberate non-normalization. Future engine additions (a MariaDB-fork variant, e.g.) follow the same audit-then-classify-then-normalize-narrowly shape.

- **MariaDB-specific features remain out of scope.** Galera cluster introspection, columnar engine specifics, virtual columns, scheduled events (the `events` table MariaDB has but MySQL only minimally exposes), and the row-format extensions are NOT indexed. v0.9.0+ if anyone files an issue with a real-world use case.

- **The probe-and-branch mechanism stays available as future-extension.** Documented as rejected for v0.8.1 because the chosen normalization didn't need it, NOT because the mechanism is wrong in principle. If a future divergence requires engine-specific code paths, the probe can land then with this ADR as the precedent.

- **Calibration credibility upheld.** v0.8.1's narrative ("each Part closes a specific gap between claim and delivery") holds: ADR-019 said "MariaDB compatible"; v0.8.1 makes that load-bearing-tested + documents what "compatible" actually means at the chunk-rendering level. No mis-framing as a recall or search-ranking improvement; this is a chunk-content fidelity + cross-engine consistency improvement.

---

## ADR-022: RENAME COLUMN + RENAME CONSTRAINT folding via eager application (v0.8.1 Part C)

**Status:** Accepted

**Date:** 2026-05-25

**Issue:** [townsendmerino/ken#14](https://github.com/townsendmerino/ken/issues/14)

### Context

[ADR-018](#adr-018-sqlite-engine--migration-history-folding-via-lightweight-alter-replay) shipped v0.7.1's Tier-1 migration folding with `RENAME COLUMN` explicitly listed as out of scope. The BOTH-chunks fallback preserved correctness — the raw migration `.sql` files were still line-chunked and emitted alongside any folded chunk, so an agent reading either surface saw the rename action. The cost was fold *quality*: folded chunks showed pre-rename column names while the live database (via Tier 2 introspection) had post-rename names. For projects with long migration histories that rename columns over time, the folded chunk and the live chunk disagreed about identifiers — the agent could read either and reach a defensible answer, but the disagreement was load-bearing for the "fold gives current schema shape" claim ADR-018 implicitly made.

v0.8.1's calibration-release framing names this exact gap: ADR-018 said "fold gives current schema shape." For RENAME, it didn't. Part C closes that gap.

**Critical framing discipline.** RENAME folding is a **Tier-1 SQL chunk-content fidelity** improvement. It is **NOT** a recall / search-ranking improvement. `docs/BENCH.md`'s hybrid-retrieval recall@10 numbers (82-91%) measure a completely different system — they're about whether ken's hybrid BM25 + Model2Vec + RRF pipeline surfaces the right chunks at the top of search results. RENAME folding is about whether the chunks ken indexes contain the post-RENAME column names rather than the pre-RENAME names. Different system; different number; **do not conflate**. Every surface this ADR touches (CHANGELOG, commit messages, release notes, README) uses "Tier-1 chunk fidelity" language. Mis-framing as "improves recall" or "closes the recall gap" would erode the calibration credibility this release is built on.

### Decision

**Eager application during ALTER replay**, not lazy resolution at chunk emission. When a `RENAME COLUMN old TO new` statement fires during the per-statement walk, `applyColumnRename` mutates the in-flight `foldedTable` in place — `columnDef.name` gets the new value, and `renameInFirstParens` rewrites column references inside this-table constraint strings via word-boundary regex (`\b` anchors so renaming `email` doesn't touch `email_verified` or `current_email`). Subsequent ALTERs see the post-rename state.

`RENAME CONSTRAINT old TO new` follows the same eager pattern via `applyConstraintRename`: walk the constraint strings, find one whose leading `CONSTRAINT <name>` prefix matches, rewrite the name in place. Anonymous constraints (`PRIMARY KEY (id)` with no name) have nothing to match and fall back to BOTH-chunks.

**Scope choices:**

- **Per-table only.** The rename map's scope is the table being mutated. FK constraints' source-side column lists (the columns OF THIS table that appear in `FOREIGN KEY (col) REFERENCES other(remote)`) ARE rewritten via the first-parens-only rewrite. FK target-side columns (the `other(remote)` portion) are NOT rewritten — those belong to a different table and propagating the rename across tables would need full migration-DAG analysis. Operators who rename FK target columns mid-migration-history see the FK chunk pointing at the old target name. Real-world frequency is low (FK targets are typically primary keys + rarely renamed); the per-table scope is the right complexity ceiling.

- **First-parens-only constraint rewrite.** `renameInFirstParens` rewrites the FIRST parenthesized group of each constraint string only. This catches `PRIMARY KEY (cols)` / `UNIQUE (cols)` / `FOREIGN KEY (source_cols) REFERENCES ...` / `CHECK (expr)` / `CONSTRAINT name <body-with-first-parens>` — all the shapes where THIS-TABLE column lists appear. The FK target-side column list lives in a LATER paren group (after `REFERENCES other`) and is left verbatim.

- **MySQL `CHANGE` syntax NOT decoded.** `ALTER TABLE foo CHANGE old new INT NOT NULL` renames AND retypes in a single statement. Decoding requires composing two operations (rename + alter-column-type) and the existing applyAlterColumn doesn't naturally compose with applyColumnRename. v0.8.1 leaves CHANGE on the BOTH-chunks fallback path — operators using CHANGE see the v0.7.1-era behavior (folded chunk shows pre-CHANGE state; raw migration file's CHANGE action is preserved separately). If field signal asks for CHANGE folding, v0.8.x+ can add it as a small extension.

- **RENAME TO (table rename) NOT decoded.** `ALTER TABLE foo RENAME TO bar` renames the table itself. Folding requires a per-database table rename map to propagate to FK target references in OTHER tables. v0.8.1's per-table scope doesn't cover this. v0.9.0+ if requested.

### Alternatives considered

- **Lazy resolution at chunk emission via a per-table `map[oldCol]newCol` rename map.** Rejected. The lazy approach accumulates renames during the per-statement walk and applies them once at emission time when the folded chunk is rendered. This handles simple A→B cases cleanly but mishandles edge case #4 in issue #14: "RENAME A→B in file 5, ADD COLUMN A in file 7." At lazy-emission time, the rename map says `A→B`, but `foldedTable.columns` contains BOTH the now-renamed-to-B former A AND the freshly-added file-7 A. Lazy resolution can't tell which is which without per-file ordering state — it would either rename both (wrong: the file-7 A should stay A) or neither (wrong: the file-5 A should be B). Eager application avoids this entirely because the foldedTable state evolves through the per-statement walk: after file 5's rename, A *is* B; file 7's ADD COLUMN A creates a fresh A; no resolution needed at emission. The cleaner state machine + the naturally-correct edge-case-#4 handling are why eager won.

- **Cycle detection in the rename map (BOTH-chunks fallback for A→B→A patterns).** Rejected as unnecessary under eager. With eager application, A→B→A naturally round-trips back to A — the final state matches the initial state, which is what the live DB would actually show for an intentional revert. If the operator intended A→B→C but typo'd C as A, the typo surfaces in the raw migration files (which are line-chunked separately by the FS walker) — the agent sees both surfaces and can reason about the discrepancy. Adding explicit cycle detection would treat intentional reverts and unintentional typos identically, which would mark intentional reverts as "uncertain" and emit unhelpful BOTH-chunks where the eager round-trip is actually correct.

- **Full SQL DDL AST parser.** Rejected for the same reason ADR-018 rejected it for the original fold-replay: no pure-Go cross-dialect DDL parser exists today, and `pg_query.go` wraps libpg_query (C; violates ADR-001's no-cgo invariant). The existing tokenizer-and-dispatcher pattern in `internal/sql` handles RENAME COLUMN / RENAME CONSTRAINT parsing at the per-statement level — the canonical convergent syntax (`RENAME COLUMN old TO new`) is identical across Postgres, MySQL, SQLite (since 3.25.0), and MariaDB. No AST needed.

- **Apply renames to FK target-side columns** (rewrite the column reference after `REFERENCES other(remote)`). Rejected. The per-table rename map's scope doesn't safely cover cross-table references — a FK declared `REFERENCES users.id` should still say `REFERENCES users.id` even if THIS table's `id` was renamed to `user_id` in a separate migration, because the `users.id` reference points to a different table's column. Cross-table rename propagation requires migration-DAG analysis: track which renames happen to which tables in which order, and propagate target-side references when the rename happened to a referenced table. Out of scope for the per-table approach; documented as a known limitation operators should expect.

- **Substring text-replace instead of word-boundary regex.** Rejected. A rename of `email` to `email_address` via substring replace would corrupt `email_verified` → `email_addressed`, `current_email` → `current_email_address`, etc. The `\b` word-boundary anchors in Go's regex package correctly fire between word chars (incl. underscore) and non-word chars, so `\bemail\b` matches the standalone identifier without touching the substring occurrences. `TestFoldRename_WordBoundary_DoesNotMatchSubstrings` pins this contract.

- **Decode MySQL `CHANGE` syntax (rename + retype in one statement).** Rejected for v0.8.1. CHANGE is structurally `RENAME COLUMN + ALTER COLUMN TYPE` in one statement; decoding it cleanly requires both code paths to compose at the per-statement level. The existing applyAlterColumn returns a boolean for "fully applied"; composing it with applyColumnRename across a single CHANGE statement would need new wiring. Defer — operators using CHANGE see the BOTH-chunks fallback (correct behavior; just less polished than the RENAME COLUMN path). If field signal arrives that CHANGE-heavy migration histories are common, v0.8.x can extend in a small follow-on.

- **Skip RENAME entirely (status quo from ADR-018).** Rejected by v0.8.1's calibration-release framing. The whole point of this Part is closing the documented-but-not-true gap in ADR-018's "fold gives current schema shape" claim. Keeping the status quo would leave the gap open and erode the calibration credibility this release is built on.

### Consequences

- **ADR-018's "fold gives current schema shape" claim is now true for RENAME** (with the documented per-table scope + the MySQL CHANGE + RENAME TO deferrals). Tier-1 chunks reflect post-RENAME column + constraint names; the previously-deferred case is no longer the "this works in theory but the chunks show stale names" exception.

- **BOTH-chunks fallback unchanged.** The existing ADR-018 graceful-degradation pattern fires for: missing source column (operator typo in migrations); RENAME CONSTRAINT on an anonymous constraint (no name to match); MySQL CHANGE syntax (not decoded in v0.8.1); RENAME TO (table rename out of scope). In every fallback case the per-file ALTER chunk is preserved AND the folded table chunk is emitted with what could be applied — the agent never sees less than v0.7.0 surfaced. Eager-cycle-handling means BOTH-chunks does NOT fire for A→B→A patterns (those round-trip naturally to A).

- **FK target-side column renames remain out of scope.** Operators who rename a column that's the target of FKs in other tables see the FK chunks in those other tables continue to point at the old target name. Real-world frequency is low (FK targets are typically primary keys; primary keys are rarely renamed); the per-table scope is the right complexity ceiling for v0.8.1. v0.9.0+ if signal arrives that cross-table propagation is needed.

- **MySQL `CHANGE` syntax falls back to BOTH-chunks.** Documented as a known deferral. Operators using `CHANGE` instead of `RENAME COLUMN` see the v0.7.1-era behavior; the folded chunk retains pre-CHANGE state and the raw migration file's CHANGE action is preserved via the per-file ALTER chunk. v0.8.x extension if needed.

- **13 new tests in `internal/sql/fold_test.go`.** Cover chain resolution (A→B→C), the eager-cycle round-trip (A→B→A → A; no BOTH-chunks), the rename-then-re-add interaction (eager naturally distinguishes the post-rename A from the freshly-added A), drop-then-re-add-then-rename, cross-table FK source-side-rewritten / target-untouched, multi-column constraint participation, the word-boundary regex regression guard, the named-constraint rename happy path, the anonymous-constraint BOTH-chunks fallback, the missing-source BOTH-chunks fallback, the idempotence regression guard (run-twice byte-identical), SQLite syntax variant, and the MySQL CHANGE BOTH-chunks fallback. The existing `TestFold15_RenameColumnFolds` (renamed from the v0.7.1-era `_OutOfScope` assertion) covers the simple A→B happy path.

- **Calibration credibility upheld.** The CHANGELOG, this ADR, the README, the DESIGN.md update, and the release-notes language all use **"Tier-1 SQL chunk fidelity"** framing throughout. No surface conflates this work with retrieval-recall improvement — `docs/BENCH.md`'s 82-91% hybrid-retrieval-recall@10 number is unaffected by this work; that's a different system and a different metric. Future engine additions that find rendering divergences (cf. ADR-021's MariaDB integer-display-width finding) should follow the same calibration-discipline framing: name the actual gap, normalize narrowly, document what's deliberately not normalized, do not over-claim across system boundaries.

- **v0.8.1 ready to tag** after this lands. The three-part calibration-release ships as one fast-forward merge: Parts A (cleanup pass + instructions polish), B (MariaDB first-class), C (RENAME folding). The branch `v0.8.1-cleanup` accumulates all 9 commits in chronological order; v0.8.1 narrative is "three claim-vs-delivery gaps closed."
