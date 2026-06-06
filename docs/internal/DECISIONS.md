# Decisions

Architecture Decision Records for `ken`, in chronological order. Each entry captures the decision, the alternatives considered, and the consequences — so future readers can understand *why* the codebase is shaped the way it is, not just *what* it does. Companion to [`docs/DESIGN.md`](../DESIGN.md) (the atemporal design spec) and [`docs/BENCH.md`](../BENCH.md) (empirical findings).

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
| [ADR-016](#adr-016-embedded-corpus-mcp-build-pattern-via-mcprun-library-function) | Embedded-corpus MCP build pattern via `mcp.Run` library function | Accepted (per-language-tree-sitter-split alternative extended by ADR-023) |
| [ADR-017](#adr-017-database-schema-indexing--two-tier-static-sql--live-postgres-with-documented-pii-stance) | Database schema indexing — two-tier (static SQL + live Postgres) with documented PII stance | Accepted |
| [ADR-018](#adr-018-sqlite-engine--migration-history-folding-via-lightweight-alter-replay) | SQLite engine + migration-history folding via lightweight ALTER replay | Accepted (extended by ADR-022) |
| [ADR-019](#adr-019-mysql-engine--schema-filtering-for-multi-schema-dev-databases) | MySQL engine + schema filtering for multi-schema dev databases | Accepted (extended by ADR-021) |
| [ADR-020](#adr-020-listennotify-push-based-schema-change-detection-v080-part-1) | LISTEN/NOTIFY push-based schema change detection + `reindex_db` MCP tool + opt-in `mcp/db` package for SDK authors (v0.8.0) | Accepted |
| [ADR-021](#adr-021-mariadb-first-class-engine-support-v081-part-b) | MariaDB first-class engine support (v0.8.1 Part B) | Accepted |
| [ADR-022](#adr-022-rename-column--rename-constraint-folding-via-eager-application-v081-part-c) | RENAME COLUMN + RENAME CONSTRAINT folding via eager application (v0.8.1 Part C) | Accepted |
| [ADR-023](#adr-023-gotreesitter-grammar_subset-machinery--binary-size-reduction-outcome-v082-investigation-outcome) | `gotreesitter` `grammar_subset` machinery + binary-size reduction outcome (v0.8.2 investigation outcome) | Accepted |
| [ADR-024](#adr-024-pre-built-embedded-indices-for-mcprun-v083) | Pre-built embedded indices for `mcp.Run` (v0.8.3) | Accepted |
| [ADR-025](#adr-025-perf-campaign-phase-1-investigation-outcome--hotspot-identification-across-small--medium-workloads) | Perf-campaign Phase 1 investigation outcome — hotspot identification across small + medium workloads | Accepted |
| [ADR-026](#adr-026-paired-heap-refactor-for-annflatquery--bm25indextopk-v084) | Paired heap refactor for `ann.Flat.Query` + `bm25.Index.TopK` via shared `internal/topk` (v0.8.4) | Accepted |
| [ADR-027](#adr-027-bm25-tokenizer-allocation-reduction--rune--byte--syncpool-scratch--lowercase-fast-path-v085) | BM25 tokenizer allocation reduction (`[]rune` → `[]byte` + `sync.Pool` scratch + lowercase fast-path) (v0.8.5) | Accepted |
| [ADR-028](#adr-028-bm25-tokenizer-parts-slice-pooling-via-tokbuffers-struct-v086) | BM25 tokenizer `parts`-slice pooling via `tokBuffers` struct (v0.8.6) | Accepted |
| [ADR-029](#adr-029-v08x-perf-campaign-capstone--allocationgc-ceiling-reached-indexing-is-single-threaded-parallelism-is-the-next-frontier) | v0.8.x perf campaign capstone — allocation/GC ceiling reached; indexing is single-threaded; parallelism is the next frontier | Accepted (extended by ADR-030) |
| [ADR-030](#adr-030-indexing-pipeline-parallelism--phase-a-per-file-workers-for-chunk--embed-v087) | Indexing pipeline parallelism — Phase A (per-file workers for chunk + embed) (v0.8.7) | Accepted |
| [ADR-031](#adr-031-mysql-introspection-sample-loop-parallelism-postgres-deferred-with-trigger-v088) | MySQL introspection sample-loop parallelism; Postgres deferred-with-trigger (v0.8.8) | Accepted |
| [ADR-032](#adr-032-promote-the-chunk-package-to-public-chunk--chunkers-chunker-interface-is-the-10-boundary) | Promote the chunk package to public (`chunk/` + chunkers); Chunker interface is the 1.0 boundary | Accepted |
| [ADR-033](#adr-033-adopt-gotreesitter-grammarsubset-slim-release-binaries-v0200-rc2) | Adopt gotreesitter `grammar_subset`; slim release binaries (v0.20.0-rc2) | Accepted (resolves ADR-023) |

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

semble's bench shows hybrid retrieval ahead of BM25 by a clear margin — semble published 0.854 hybrid vs 0.675 BM25-raw; ken measures 0.842 vs 0.624 on the same 63-repo × 1251-query corpus. The CoIR-CSN-Python external benchmark reverses this: ken BM25 0.8743 > hybrid 0.7839 > semantic 0.7405 (1000-query subsample, regex chunker). The cause this ADR was originally built on — and which turned out to be a misread of the data — is documented at [`docs/BENCH.md` "Why BM25 beats hybrid on CSN-Python"](../BENCH.md#why-bm25-beats-hybrid-on-csn-python): CSN-Python's queries (as CoIR re-hosts the dataset) are actually full **Python function sources**, and the relevant document for each query is the **docstring extracted from that same function**. Because the docstring lives inside the function source as a literal substring, any lexical retriever with identifier-aware tokenization is effectively doing substring-match — BM25 has the answer string as input.

**This misdescription was the load-bearing premise this ADR was built on.** The original Context paragraph described CSN queries as English docstring-shaped natural-language questions answered by the function being described, and framed α-routing as a way to recover hybrid performance on a docstring-shaped NL query class. Prompt 22's precondition step — read `scripts/bench_coir.py`, inspect a sample query/document pair — surfaced the actual direction (queries = code, docs = docstrings) and replaced the identifier-overlap diagnosis with the sharper substring-leak diagnosis above. No α value beats "the answer string is literally in the query"; the structural finding doesn't generalize past CoIR's reframing of CodeSearchNet. See the "Validation outcome" section below.

semble's `resolveAlpha` (verbatim in [`internal/search/adaptive.go`](../../internal/search/adaptive.go)) recognizes two query classes: symbol (α=0.3) and NL (α=0.5). There is no third class for "docstring-shaped NL" — long English queries whose lexical overlap with the answer doc is unusually high. A third branch with a lower α (lean harder on BM25, perhaps 0.1–0.2) is the conservative extension that would recover the CSN performance without touching the existing branches' constants. The lever exists; the question is whether to pull it, with what detection signal, and whether the classifier risk justifies the gain.

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
- **If the gate kills it:** Status changes Proposed → Deprecated; [`docs/BENCH.md`](../BENCH.md#external-benchmark--coir-csn-python) updates the CSN paragraph with the no-signal finding.

### Validation outcome — Prompt 22 reconnaissance (2026-05-21)

Prompt 22 paused before any labeling work after reading [`scripts/bench_coir.py`](../../scripts/bench_coir.py) and inspecting a sample query/document pair from `testdata/bench/coir-csn-python/`. The motivating empirical claim that built this ADR — that CSN-Python queries are English docstring-shaped NL inputs and the relevant document is the function those queries describe — had the direction **backwards**: CoIR's CSN-Python reframing makes queries the full Python function sources and documents the docstrings extracted from those same functions. The docstring lives *inside* the query as a literal substring (the function's own `"""..."""` block). The BM25-beats-hybrid result on CSN-Python is therefore a **substring-leak artifact** of CoIR's dataset construction, not evidence of a query-class signal that an α-routing lever could exploit.

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

  - *v0.8.2 follow-on (mechanism-distinct retry; closes [#16](https://github.com/townsendmerino/ken/issues/16)):* Issue #16 proposed a different mechanism — build tags at the source-file level — on the premise that excluding a file via `//go:build` removes its `//go:embed` payload from the binary. Phase 1 of v0.8.2 investigated `gotreesitter` v0.18.0 (current pinned version; grammar count has grown from 17 to ~206 and the embedded payload from ~19 MB to ~26 MB; the conclusion is unchanged). Finding: registration layer is per-language-gateable via the existing `grammar_subset` + `grammar_subset_<lang>` tag pair (cooperative), but the embed layer is still one shared `//go:embed grammar_blobs/*.bin` glob in `blob_source_embedded.go` (uncooperative — the per-language source split needed to make build-tag gating actually shrink the binary does not exist upstream at v0.18.0). The original linker-DCE finding restated at the source-tag layer; both layers fail for the same fundamental reason at this version of gotreesitter. See [ADR-023](#adr-023-gotreesitter-grammar_subset-machinery--binary-size-reduction-outcome-v082-investigation-outcome) for the full alternatives matrix, the named upstream-PR proposal that would change the answer, and the vendor + patch stop-gap that v0.8.2 documents-but-doesn't-pursue.

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

**Critical framing discipline.** RENAME folding is a **Tier-1 SQL chunk-content fidelity** improvement. It is **NOT** a recall / search-ranking improvement. `docs/BENCH.md`'s token-budget recall@10 numbers (82–91%, ken's BM25-only fallback floor; the default hybrid mode measures ~0.97) measure a completely different system — they're about whether ken's hybrid BM25 + Model2Vec + RRF pipeline surfaces the right chunks at the top of search results. RENAME folding is about whether the chunks ken indexes contain the post-RENAME column names rather than the pre-RENAME names. Different system; different number; **do not conflate**. Every surface this ADR touches (CHANGELOG, commit messages, release notes, README) uses "Tier-1 chunk fidelity" language. Mis-framing as "improves recall" or "closes the recall gap" would erode the calibration credibility this release is built on.

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

- **Calibration credibility upheld.** The CHANGELOG, this ADR, the README, the DESIGN.md update, and the release-notes language all use **"Tier-1 SQL chunk fidelity"** framing throughout. No surface conflates this work with retrieval-recall improvement — `docs/BENCH.md`'s 82–91% token-budget recall@10 number (the BM25-only fallback floor; default hybrid is ~0.97) is unaffected by this work; that's a different system and a different metric. Future engine additions that find rendering divergences (cf. ADR-021's MariaDB integer-display-width finding) should follow the same calibration-discipline framing: name the actual gap, normalize narrowly, document what's deliberately not normalized, do not over-claim across system boundaries.

- **v0.8.1 ready to tag** after this lands. The three-part calibration-release ships as one fast-forward merge: Parts A (cleanup pass + instructions polish), B (MariaDB first-class), C (RENAME folding). The branch `v0.8.1-cleanup` accumulates all 9 commits in chronological order; v0.8.1 narrative is "three claim-vs-delivery gaps closed."

---

## ADR-023: `gotreesitter` `grammar_subset` machinery + binary-size reduction outcome (v0.8.2 investigation outcome)

**Status:** Accepted

**Date:** 2026-05-25

**Issue:** [townsendmerino/ken#16](https://github.com/townsendmerino/ken/issues/16); extends [ADR-016](#adr-016-embedded-corpus-mcp-build-pattern-via-mcprun-library-function)'s per-language-treesitter-sub-packages alternative.

### Context

[ADR-016](#adr-016-embedded-corpus-mcp-build-pattern-via-mcprun-library-function) closed the per-language tree-sitter sub-packages door at the linker-DCE layer in v0.6.0: splitting `internal/chunk/treesitter` into per-language wrappers wouldn't shrink the binary because Go's linker cannot dead-code-eliminate `embed.FS` payloads. The mitigation that shipped instead — moving chunker side-effect imports from `internal/search` into the binaries that want each chunker — keeps the full ~26 MB `gotreesitter/grammars` bundle out of the docs binary (`cmd/ken-mcp-docs`, which only imports the `markdown` chunker) without trying to shrink the bundle itself.

Issue [#16](https://github.com/townsendmerino/ken/issues/16) proposed a mechanism-distinct retry: build tags **at the source-file level**. The premise is that `//go:build` excludes a file from compilation entirely, so if a grammar's `//go:embed` directive lives in its own source file with a `//go:build lang_go` tag, omitting the tag means the file (and its `embed.FS` payload) never enters the binary. Mechanism-distinct from ADR-016's import-time rejection — different layer, different question. v0.8.2 Phase 1 investigated the `gotreesitter` v0.18.0 package shape to determine whether the upstream layout permits per-grammar build-tag gating.

**Phase 1 findings (concrete; pinned version was v0.18.0 / ~206 grammars / ~26 MB compressed payload):**

- **Embed layer is monolithic.** Only two `//go:embed` directives exist in the entire `gotreesitter/grammars` package — both in `blob_source_embedded.go` (or its sibling `blob_source_embedded_core.go` under the `grammar_set_core` build tag). The default form is a single `//go:embed grammar_blobs/*.bin` glob backed by one shared `var grammarBlobFS embed.FS`. No per-grammar embed file exists; no `grammar_subset`-gated embed file exists.

- **Registration layer IS per-language-gateable.** `gotreesitter` upstream already implements partial subset machinery: the `grammar_subset` build tag swaps the bulk-default `registerBuiltinLanguages()` (~206 `Register(LangEntry{...})` calls in `registry_builtin_gen.go`) for an empty stub (`registry_builtin_subset.go`). Languages then opt-in per-language via `grammar_subset_<lang>` build tags — each `z_subset_registry_register_<lang>.go` file (gated `//go:build grammar_subset && grammar_subset_<lang>`) has its own `init()` calling `Register()`. Scanner attachments mirror this in `z_subset_scanner_register_<lang>.go`. Ken's hot languages (go, java, python, typescript, rust) all have z_subset files.

- **External-blob escape hatch.** The `grammar_blobs_external` build tag removes the embed entirely; grammars are loaded at runtime from `$GOTREESITTER_GRAMMAR_BLOB_DIR`. This is the only path that actually strips the embed payload from the binary, but it requires operators to ship grammar blobs as sidecar assets — incompatible with ken's single-static-binary contract (already rejected in [ADR-016](#adr-016-embedded-corpus-mcp-build-pattern-via-mcprun-library-function)'s alternatives).

- **ken-side coupling is cooperative.** `internal/chunk/treesitter/chunker.go` dispatches through `grammars.DetectLanguageByName(tsName)` only — string-keyed, no per-grammar symbol imports. A missing-from-registry entry already falls back to the line chunker via the existing `Chunker` interface. If upstream gained per-language embed gating, ken's dispatch needs no refactor; the `kenToTreeSitter` map in `internal/chunk/treesitter/languages.go` is the only ken-side surface that names per-language grammars.

The shape classification: **partially cooperative, fundamentally limited for the binary-size goal**. Build tags work at the registration layer (the smaller half of the cost). They don't work at the embed layer (the larger half) because the embed.FS bytes stay in the binary regardless of which `grammar_subset_<lang>` tags are set. This is exactly ADR-016's linker-DCE finding restated one layer up: the layer changed, the conclusion didn't.

**Critical framing discipline.** v0.8.2 is an **investigation outcome release**, not a feature ship. The honest claim is "we investigated the build-tag path; here's why it doesn't work; here's the specific upstream change that would unblock us." Not "v0.8.2 shipped selective tree-sitter grammars" or "v0.8.2 reduced the binary size." Same calibration shape as [ADR-022](#adr-022-rename-column--rename-constraint-folding-via-eager-application-v081-part-c) (Tier-1 chunk fidelity vs retrieval recall) and [ADR-021](#adr-021-mariadb-first-class-engine-support-v081-part-b) (chunk-rendering consistency, not search-ranking improvement): name the actual scope, do not over-claim across system boundaries. Every CHANGELOG / README / commit-message / release-notes surface this work touches uses "investigation outcome release" framing. The 74 MB binary stays 74 MB after v0.8.2.

### Decision

**Do not ship build-tag-gated tree-sitter grammars in v0.8.2.** Document the Phase 1 finding honestly. Name the specific upstream change that would make build-tag gating actually shrink the binary. Close [#16](https://github.com/townsendmerino/ken/issues/16) as wontfix-without-upstream-cooperation, with the concrete upstream-PR proposal as the closing comment so the reopening trigger is unambiguous.

**Three documentation surfaces ship:**

1. **ADR-016 forward-pointer.** The existing "Per-language treesitter sub-packages" alternative inside ADR-016 gets a v0.8.2 follow-on bullet naming the Phase 1 finding + the v0.18.0 package-shape facts + the cross-reference to this ADR. The original ADR-016 text is preserved (the linker-DCE finding stands as written for v0.5.0's package-import-level rejection); the amendment adds the source-file-build-tag-level rejection underneath.

2. **This ADR (ADR-023).** Full alternatives matrix per ken's established ADR pattern. Names the upstream PR proposal that would change the answer; documents the vendor + patch stop-gap as available but not pursued in v0.8.2.

3. **CHANGELOG v0.8.2 entry.** Investigation-outcome framing. No surface conflates documentation-only work with shipped binary-size reduction. The v0.8.x calibration-release shape ("each Part closes a specific gap between claim and delivery") extends naturally to "v0.8.2 closes the open #16 investigation without conflating the closure with a feature ship."

**Stale-claim correction inside DESIGN.md.** Section §2 / Option A previously claimed `grammar_blobs_external` is "used by `ken-mcp` releases — see §8." Audit confirms `grammar_blobs_external` is referenced nowhere in `.goreleaser.yml`, `.github/workflows/`, or any build script; ADR-016's alternatives section already rejected the build tag for breaking the single-static-binary contract. The DESIGN.md claim is stale aspirational text from earlier in the project's life that survived ADR-016's settlement. v0.8.2 corrects it to "available upstream but not used by ken's releases" and cross-references this ADR. Also updates the stale "19 MB" grammar-bundle size in §1 (`gotreesitter` grew from 17 to ~206 grammars between v0.6.0 and v0.18.0; the bundle is now ~26 MB).

### Alternatives considered

- **Use `grammar_subset` + `grammar_subset_<lang>` tags alone** (the obvious mechanism if the goal is reduced binary size). Rejected. Per Phase 1's finding, gates the registration layer but not the embed layer — the `//go:embed grammar_blobs/*.bin` glob still pulls every blob into the binary. Net binary size is unchanged; only the dispatch table shrinks (irrelevant to the binary-size goal). Worth doing only if a future ken use case wanted to restrict which languages the chunker considers without caring about binary size — not a use case we have today. Documented here so future contributors don't redo the investigation expecting a different result.

- **Use `grammar_blobs_external` runtime-load tag** (the only upstream-supported path that actually strips the embed payload). Rejected for the same reason ADR-016 already rejected it: reads from `$GOTREESITTER_GRAMMAR_BLOB_DIR` at runtime, requires operators to ship grammar blobs alongside the ken binary, breaks ken's single-binary value proposition (one static cross-compiled executable; no per-platform asset bundles; embedded-corpus SDK authors who would `go build` and push a binary cannot push sidecar assets alongside it). The binary-size benefit isn't worth the deployment-complexity cost. Documented in [ADR-016](#adr-016-embedded-corpus-mcp-build-pattern-via-mcprun-library-function)'s alternatives + here as available-but-not-blessed.

- **Combine `grammar_subset` + `grammar_blobs_external`** (the smoke-test pattern in `gotreesitter`'s own `grammar_subset_test.go`). Rejected. This is the upstream-supported way to actually get a smaller binary from the package, but it inherits `grammar_blobs_external`'s sidecar-asset requirement — the registration shrinks AND the embed disappears, but operators still need `$GOTREESITTER_GRAMMAR_BLOB_DIR` set at runtime with the right blobs on disk. Same single-binary-contract violation as the previous alternative.

- **Vendor `gotreesitter` into ken, patch `blob_source_embedded.go` to split per-grammar, ship build-tag-gated grammars from the vendored copy.** Available as a stop-gap if size pain becomes acute; **NOT pursued in v0.8.2**. Vendoring brings ongoing maintenance burden — track upstream changes (`gotreesitter` is actively developed; grammars and runtime have evolved meaningfully even between v0.6.0 and v0.18.0), re-apply the patch on each upgrade, handle any divergent behavior introduced by upstream restructuring that interacts with the patch. ken's current binary-size pain isn't acute enough to justify this: the treesitter chunker is opt-in via `--chunker=treesitter` (default is the regex chunker, which doesn't pull the grammar weight at all); embedded-corpus SDK authors who care about binary size mostly use the regex or markdown chunker, neither of which imports `gotreesitter`; the `cmd/ken-mcp-docs` binary already excludes grammars via ADR-016's chunker-registration refactor. Revisit if a future SDK author files an issue with concrete size-pain numbers from a use case that genuinely needs treesitter chunking AND a smaller binary. The maintenance-cost analysis lives here as a future-ADR seed.

- **Submit upstream PR splitting `blob_source_embedded.go` into per-language files.** Recommended path forward, but separate from v0.8.2's release commits. The proposed upstream change: split `blob_source_embedded.go` into per-language source files — one `grammar_blob_<lang>.go` per grammar (~206 files), each with `//go:embed grammar_blobs/<lang>.bin` + `//go:build !grammar_blobs_external && grammar_subset_<lang>`. Each file declares its own `var <lang>BlobFS embed.FS` (or contributes to a shared keyed map via `init()`). Pure source rearrangement; the public API stays identical (`grammars.DetectLanguageByName(name)` continues to return a `*LangEntry` whose `Language()` closure lazy-loads the per-language blob through `loadEmbeddedLanguage`). Once upstream merges this, ken can add build-tag flags in a future point release without forking — `go build -tags=grammar_subset,grammar_subset_go,grammar_subset_python,...` would produce a binary embedding only the named blobs, and the registration-layer machinery already exists. Francis decides separately whether to send this PR to `odvcencio/gotreesitter`; v0.8.2's release commits don't depend on it. If the PR lands (whether sent by ken's team or anyone else), [#16](https://github.com/townsendmerino/ken/issues/16) is the natural reopening trigger.

- **Fork `gotreesitter` as a permanent ken-specific dependency.** Rejected. Owning a fork means owning the maintenance burden indefinitely — every upstream `gotreesitter` release becomes a manual rebase, every parser fix or grammar update has to be pulled across, divergence accumulates. The vendor + patch alternative above is the lighter-weight version of the same idea if size pain ever becomes acute: vendor without forking, carry one small targeted patch, drop it once upstream merges the embed-split. A fork would only be justified if ken's needs diverged structurally from upstream (e.g., we needed a fundamentally different grammar registration model), which Phase 1's finding does not support.

- **Replace `gotreesitter` with a different tree-sitter Go binding that supports per-grammar opt-in natively.** Investigated as part of [ADR-010](#adr-010-tree-sitter-via-gotreesitter-instead-of-wazerowasm)'s original binding selection (which pivoted from `wazero` + WASM to `gotreesitter` for the right reasons: pure Go, no cgo, no per-platform vendored artifacts). At the time of v0.2.0, no pure-Go tree-sitter binding supported per-grammar opt-in natively. As of v0.8.2 (2026-05-25) the binding landscape has not visibly changed — `gotreesitter` remains the dominant pure-Go option. Re-evaluating the binding choice is a separate larger investigation gated on a real alternative emerging; this ADR assumes we stick with `gotreesitter` and addresses binary-size via the upstream-collaboration path above instead.

- **Status quo: leave #16 open as long-running investigation.** Rejected by the v0.8.x calibration-release discipline. v0.8.2's investigation pass IS the investigation outcome — Phase 1 named the package-shape facts, Phase 2 documents them in this ADR + CHANGELOG. Leaving #16 open implies we'll come back to it without a concrete trigger; the right closure is wontfix-without-upstream-cooperation with the upstream-PR proposal as the reopening trigger. Same shape as [ADR-016](#adr-016-embedded-corpus-mcp-build-pattern-via-mcprun-library-function) closing the per-language-sub-packages door at the linker-DCE layer in v0.6.0 — once an investigation hits bedrock, the right move is to document the bedrock and close, not leave the issue open as a perpetual "we should look at this again."

### Consequences

- **#16 closes wontfix-without-upstream-cooperation.** The closing comment names the concrete upstream-PR proposal so the reopening trigger is unambiguous: if `gotreesitter` upstream merges the per-language embed split described in this ADR's alternatives (whether the PR comes from ken's team or another downstream user), v0.8.x+ can ship build-tag-gated grammars without forking. Until then, ken's binary embeds the full `gotreesitter/grammars` payload, mitigated only by ADR-016's chunker-registration refactor that keeps the bundle out of the docs binary.

- **The `grammar_subset` machinery upstream remains available but unused by ken.** Operators who use `-tags=grammar_subset,grammar_subset_go,...` get a smaller registration table but identical binary size — the embed.FS bytes are still present. This ADR documents the gap explicitly so future contributors don't redo Phase 1's investigation expecting a different outcome. If a non-ken use case wants the smaller registration table (e.g., restricting which languages the chunker considers without caring about binary size), the tags work as documented upstream; ken doesn't bless or oppose this usage.

- **The `grammar_blobs_external` runtime-load path remains available but unused by ken's releases.** Operators who genuinely need the smaller binary AND can ship grammar blobs as sidecar assets can use the tag without ken's involvement — set `grammar_blobs_external` at build time, set `$GOTREESITTER_GRAMMAR_BLOB_DIR` at runtime, ship the blobs alongside the binary. DESIGN.md §2's claim that this is "used by ken-mcp releases" was stale aspirational text and is corrected to "available upstream but not used by ken's releases" as part of this ADR's docs commit. ken's `.goreleaser.yml` continues to build the default (embedded) form for both `ken` and `ken-mcp` binaries.

- **Vendor + patch stop-gap available as future-ADR work** if a real-world size-pain report arrives. The maintenance-cost analysis in this ADR's alternatives section names the trade (one-patch maintenance vs ongoing upstream-tracking) so the decision can be made quickly when the time comes. The trigger is concrete: an SDK author or operator filing an issue with measured size pain from a use case that needs the treesitter chunker AND a smaller binary AND cannot use sidecar assets. Until that trigger fires, the vendor + patch path stays documented-but-not-pursued.

- **DESIGN.md stale-claim correction lands as part of this docs commit.** Section §2 / Option A's "(used by `ken-mcp` releases — see §8)" parenthetical is corrected; §1's "19 MB" grammar-bundle size is updated to "~26 MB" to reflect `gotreesitter` v0.18.0's 206-grammar count. Both corrections are downstream consequences of the v0.8.2 investigation surfacing the discrepancy; cleaning them up alongside the new ADR keeps the docs internally consistent.

- **Calibration credibility upheld.** v0.8.2's release narrative is "we investigated; here's the answer; here's what would change the answer." No surface (CHANGELOG, this ADR, README, release notes, commit messages, the #16 closing comment) conflates documentation-only work with shipped binary-size reduction. The 74 MB binary measured in ADR-016 stays 74 MB after v0.8.2. Future investigation-outcome releases — the natural shape any time ken investigates a feature path and finds the path closed — follow this template: name the actual gap, name the upstream / external trigger that would change the answer, document the stop-gap that's available if pain becomes acute, close the issue with the trigger as the reopening condition. Same discipline as [ADR-021](#adr-021-mariadb-first-class-engine-support-v081-part-b)'s tiered-divergence framing and [ADR-022](#adr-022-rename-column--rename-constraint-folding-via-eager-application-v081-part-c)'s Tier-1-chunk-fidelity-vs-recall framing, applied to a "the answer is no, here's why, here's what would change it" outcome rather than a feature ship.

- **v0.8.2 ready to tag** after this lands. The investigation-outcome release ships as one fast-forward merge of branch `v0.8.2-investigation` — two commits (the docs commit carrying this ADR + the ADR-016 amendment + DESIGN.md correction, plus a separate CHANGELOG commit for reviewability of the user-facing narrative). v0.8.x calibration-release shape extends naturally: each Part / each point release closes a specific gap between claim and delivery, including the "the claim doesn't work yet and here's why" case.

### Calibration amendment (post-v0.8.3 audit)

**The "~26 MB" figure that v0.8.2 introduced into ADR-016 / ADR-023 / DESIGN.md §1 conflated two different measurements.** A post-v0.8.3 audit re-measured against `gotreesitter` v0.18.0 (and cross-checked against v0.19.1, the current upstream — same shape). The actual numbers, with units of measurement made explicit:

| Quantity | Value | What it means |
|---|---|---|
| On-disk grammar bundle | **19.1 MB** (20,011,313 bytes) | Sum of the 206 `*.bin` files in `gotreesitter/grammars/grammar_blobs/`. The actual embed.FS payload. **Stable across v0.18.0 and v0.19.1.** |
| Linked-binary cost when treesitter is imported | **~26 MB** (26,985,952 bytes delta) | Difference between `cmd/ken-mcp` built with the `_ "github.com/townsendmerino/ken/internal/chunk/treesitter"` import (53.13 MB) and the same binary without it (27.40 MB). Includes the embed payload, the gotreesitter parser runtime, and Go's symbol bookkeeping for both. Measured darwin/arm64 with default `-ldflags` (debug info included). |

**What the prior text got wrong:** v0.8.2's stale-claim audit read v0.6.0's "19 MB" reference, observed that gotreesitter had grown from 17 to ~206 grammars between v0.6.0 and v0.18.0, and inferred that the on-disk bundle must have grown proportionally to "~26 MB." It didn't — the v0.6.0 "19 MB" was the on-disk-bundle measurement, and the on-disk bundle is *still* ~19 MB at v0.18.0. The ~26 MB figure that v0.8.2 propagated through ADR-016 amendments, ADR-023 Phase 1 findings, the DESIGN.md §1 update, and the in-code comments is plausibly the binary-cost figure (which matches the post-v0.8.3 re-measurement), but the v0.8.2 text framed it as the bundle size — two different quantities collapsed into one label.

**What's been corrected in the post-v0.8.3 sweep:**

- The three in-code comments (`cmd/ken-mcp/main.go`, `cmd/ken-mcp-docs/main.go`, `internal/search/index.go`) now read "the linked binary inflates by ~26 MB ... the gotreesitter/grammars embed.FS payload is ~19 MB on-disk for 206 grammar blobs." Both numbers are named with their units.
- DESIGN.md §1 amended to the same dual-number framing.
- This calibration amendment in ADR-023 names the prior conflation explicitly so future readers don't redo the same mismeasurement.

**What's deliberately NOT rewritten:** the prior text of ADR-016, ADR-023's earlier sections, and the v0.8.2 amendment block itself remain as the historical record. ken's calibration discipline is to amend, not overwrite — same pattern as v0.8.2 amending v0.6.0 originally.

**Resolution:** for any future reader, the canonical figures are **~19 MB on-disk grammar bundle** and **~26 MB binary cost when treesitter is imported**. The earlier "the bundle is now ~26 MB" framing is wrong about the bundle but right about the binary cost; the two were conflated.

---

## ADR-024: Pre-built embedded indices for `mcp.Run` (v0.8.3)

**Status:** Accepted

**Date:** 2026-05-26

**Issue:** [townsendmerino/ken#10](https://github.com/townsendmerino/ken/issues/10); closes the cold-start loop left open by [ADR-016](#adr-016-embedded-corpus-mcp-build-pattern-via-mcprun-library-function).

### Context

[ADR-016](#adr-016-embedded-corpus-mcp-build-pattern-via-mcprun-library-function) shipped the embedded-corpus build pattern (`mcp.Run` + `embed.LoadFromFS` + the markdown chunker) in v0.6.0: SDK authors `go build` once and ship a single static binary that serves search / find_related over their `//go:embed` corpus. The product story works end-to-end as of v0.6.0; the canonical worked example (`cmd/ken-mcp-docs`) is a ~20-line `main.go` that bakes ken's own docs + the Model2Vec model into a 74 MB binary.

Every `mcp.Run` startup walks the embedded corpus, chunks every indexable file, and (for semantic / hybrid mode) calls `model.Encode` on every chunk to produce the dense embedding matrix. The embedding pass is linear in corpus size and dominates cold-start time — for a small docs corpus (~100 files / ~500 chunks) it's modest; for a larger embedded corpus (~10K+ chunks) it's seconds-to-minutes of CPU on each process launch. v0.6.0's "binary IS the corpus" pitch is undermined when starting the binary takes meaningfully longer than starting any other agent backend.

Narrative: **"v0.6.0 shipped embedded corpora; v0.8.3 makes their cold start fast."** Same calibration shape as v0.7.x → v0.8.x: each release closes a specific gap the prior release left open.

### Decision

Serialize the built `*search.Index` artifact (chunks slice + parallel embedding matrix) to a custom binary format SDK authors can ship inside their `//go:embed` corpus. Three new surfaces + two integrations:

**Three library / CLI surfaces:**

1. **`search.BuildAndSerializeIndex(fsys fs.FS, opts BuildOptions) ([]byte, error)`** — library function. Wraps the existing `walkAndChunkFSWithModel` + a new `serializeIndex` internal helper. SDK authors who script the build in their `go generate` or Makefile call this directly; the CLI subcommand below is just a thin operator-facing wrapper around it.

2. **`search.LoadSerializedIndex(data []byte, opts LoadOptions) (*Index, error)`** — library function. Validates the on-disk header + CRC32 trailer, reconstructs the chunks slice + embedding matrix, calls the existing `BuildIndex` to re-tokenize for BM25 + wire up the `ann.Flat` index. Returns one of `ErrCorrupt` / `ErrFormatVersion` / `ErrModeMismatch` / `ErrChunkerMismatch` / `ErrModelRequired` on any mismatch so callers can take the lazy-fallback path.

3. **`ken build-index <corpus_dir> -o <output_path>`** — operator-facing CLI subcommand of the existing `ken` binary. Resolves the model the same way `ken index` / `ken search` do (`--model` flag → `KEN_MODEL_DIR` → `~/.ken/model` → `./testdata/model`). Writes the bytes atomically (`<output>.tmp` + `os.Rename`).

**Two `mcp.Run` integrations:**

4. **Convention-over-configuration auto-discovery.** `mcp.Run` reads `corpus/.ken/index.bin` from the supplied `fs.FS` (if present). The literal path `.ken/index.bin` is the convention; no env var, no `Options` path field. SDK authors who follow the convention add zero lines to their `main.go` from the v0.6.0 baseline.

5. **`Options.PrebuiltIndex []byte` explicit override.** For SDK authors using a non-conventional layout (index in a sibling `embed.FS`, index outside the corpus root, etc.). When set, the explicit bytes win and the auto-discovery path is skipped.

**Lazy fallback on any load failure.** Corrupt bytes, format-version mismatch, mode mismatch, chunker mismatch, missing-from-FS — all of these log a stderr warning naming the reason + suggest `ken build-index` to refresh, and fall back to the v0.6.0 build-from-corpus path (`search.FromFSWithModel`). The pre-built path is purely an optimization, never a requirement; an SDK author who deploys a stale or corrupt pre-built file gets a slower-but-still-working binary, not a crash.

**Walker exclusion.** `internal/repo`'s walker (both `WalkFS` and `Matcher.ShouldIndex`) prunes `.ken/` directories analogously to the existing `.git/` prune. Without this, the pre-built index file would be chunked as part of the corpus on the lazy-fallback path. No env var; convention.

**Binary format.** Custom binary with explicit version header, len-prefixed sections, CRC32 IEEE trailer. Format reference lives in the file header of `internal/search/index_serialize.go`; the on-disk layout is internal-only (ken's own serialization for its own use), not a public API. Format version gate (currently `1`) detects incompatible upgrades; the ken-version field is informational only.

**BM25 internals are NOT serialized.** BuildIndex re-tokenizes every chunk on load and rebuilds postings + df + docLen. Deterministic by construction (chunks come from `repo.WalkFS` in lexical order; `BuildIndex` iterates the slice directly with no map iteration). The cost is negligible compared to the embedding matrix the optimization actually saves.

**Model-reference handling.** Semantic / hybrid `ix.Search` re-encodes the user's query string at query time to compare against the precomputed corpus matrix; the model is required at query time, not just at build time. `LoadOptions.Model` is therefore mandatory for non-BM25 modes (returns `ErrModelRequired` if missing). The resulting `*Index` carries the supplied model, so `WithExtraChunks` works on loaded indices the same way it works on freshly-built ones — the combination "pre-built index + mcp/db chunk integration" works out of the box.

### Alternatives considered

- **Sidecar index files alongside the binary** (write the bytes to `~/.ken/cache/<corpus-hash>.bin`; load from disk at startup; rebuild + cache on miss). Rejected. Breaks the v0.6.0 single-static-binary contract — SDK authors lose the "one executable, no per-platform assets, push it and users `go install`" property. The pitch is "binary IS the corpus, version-pinned by build artifact;" sidecars push complexity back into the deployment story. Embedded via `//go:embed` keeps the contract intact. SDK authors who want sidecars can call `BuildAndSerializeIndex` themselves and write the bytes wherever they like — not the blessed path, but mechanically supported.

- **JSON serialization format.** Rejected. JSON inflates the embedding matrix dramatically (a 256-dim `float32` chunk becomes 256 stringified-decimal numbers + commas + brackets, roughly 5× the size; with base64 it's still ~33% larger than raw little-endian bytes); slower to parse (lexer + decoder + value boxing vs `math.Float32frombits` on a slice); no compact format for binary blob sections; line numbers are ints serialized as decimal. Custom binary keeps the file tight and parsing essentially free. The cost is writing serialization tests; the benefit is no third-party format dependency.

- **`encoding/gob` serialization format.** Rejected. `gob` is Go-idiomatic and ergonomic, but the format's stability stance is "the program that wrote the data is responsible for ensuring it can be read" — explicitly not designed for cross-build / cross-release stability. ken's pre-built indices are produced at `go generate` time and consumed at runtime by potentially different ken builds; using `gob` would couple the on-disk format to whatever the Go toolchain happens to emit on the build machine. Custom binary with an explicit format version is more robust + easier to reason about.

- **Protobuf or other schema language.** Rejected for v0.8.3. Adds a dependency (the `google.golang.org/protobuf` runtime + the schema build step + the generated `.pb.go` file in the tree); schema-evolution overhead (every format change is two PRs: schema + code); cross-language interop is irrelevant here (the format is internal). Custom binary keeps the dep tree clean and the format under ken's control. If a future use case actually needs cross-language interop (Python SDK consuming ken-built indices, for instance), revisit then.

- **Strict-version-only load (no forward-compatibility for the ken-version field).** Rejected. The format-version field IS strictly gated (mismatch → `ErrFormatVersion`); the ken-version field is informational only. Forward-compatible header reads let ken patch / minor releases that don't change the format load older indices without forcing SDK authors to re-run `ken build-index` on every ken upgrade. Strict-version-only would multiply the maintenance burden on SDK authors for no benefit. The trade is the format-version gate carrying the load — it's the field that actually says "the bytes mean what this code thinks they mean."

- **Hard-error fallback (refuse to start on missing / corrupt index).** Rejected. The lazy-fallback path ensures the binary always works — a deployed SDK author binary with a stale or corrupt pre-built file is slower-to-start, never broken. Stderr warning makes the fallback visible to operators without breaking the deployment. The hard-error variant would treat the cold-start optimization as a configuration requirement, escalating "the optimization went stale" from a slow-start warning to a deployment outage. Optimizations should be opt-in to the upside, not opt-in to the failure mode.

- **Configurable fallback mode (`Options.IndexFallback strict|lazy`).** Rejected for v0.8.3. Doubles the test surface (each pre-built code path now has two variants) for a binary decision a single deployment shape per release suffices for. If a deployment scenario emerges where strict-fail is genuinely needed — e.g., the SDK author treats a stale pre-built index as a configuration error worth halting startup over — revisit in a future release. The alternative is reversible: add the field non-breakingly, default to lazy, off-by-default-strict for opt-in.

- **Pre-built indices for `cmd/ken-mcp`'s watch path.** Deferred to v0.9.0+. The watch path's optimization profile is fundamentally different: fsnotify-triggered incremental rebuilds (ADR-012), atomic snapshot swap on each rebuild, per-process LRU cache + singleflight dedup across repo paths. Pre-built indices in that world would need invalidation logic on fsnotify events, cache-key handling for multi-repo state, and per-process lifetime management — a separate design conversation. v0.8.3 focuses on `mcp.Run`'s cold start where the corpus is `//go:embed`-static-by-construction and the optimization shape is clean: build once at `go generate` time, load once at startup. If `cmd/ken-mcp` startup also turns out to be too slow, the v0.9.0+ design picks up the open question.

- **Serialize the BM25 internal state (postings + df + docLen + tokenizer settings).** Rejected. The embedding pass dominates cold-start by 2-3 orders of magnitude; rebuilding BM25 from already-decoded chunks via the existing `BuildIndex` is ~1% of the optimization budget. Serializing BM25 would require exposing the `internal/bm25` package's private state through a `Marshal`/`Unmarshal` API, sorting map keys for determinism, and writing BM25-specific serialization tests — meaningful complexity for a sub-1% optimization. Skipping it keeps the format simple and the `internal/bm25` package untouched. Documented here so future contributors don't redo the cost analysis expecting a different answer.

- **Bundle the model into the pre-built index file.** Rejected. The model is already independent of the index per the v0.6.0 design (`Options.ModelFS` / `Options.ModelDir` is its own loading path; SDK authors who bake the model into their binary do so via a separate `//go:embed model/*`). Coupling them into one file would mean SDK authors using `Options.ModelDir` (e.g., a CI image with the model pre-installed) couldn't use the pre-built index. Keeping them decoupled preserves both layouts.

### Consequences

- **Cold-start time drops for `mcp.Run` SDK authors who opt-in via `ken build-index`.** Concrete numbers depend on corpus size: small docs corpora (~100 files, ~500 chunks) see modest improvements; large embedded corpora (~10K+ chunks) save the embedding pass's seconds-to-minutes of CPU on each process launch. The v0.6.0 "binary IS the corpus, instantly servable" pitch holds at scale, not just at docs-site size. The exact numbers will land in the v0.8.3 GitHub Release notes once measured against `cmd/ken-mcp-docs` (small) and a synthetic larger fixture.

- **The v0.6.0 single-binary contract is preserved.** Pre-built indices live inside the SDK author's `//go:embed` corpus, not as sidecar assets shipped alongside the binary. The contract that motivated [ADR-016](#adr-016-embedded-corpus-mcp-build-pattern-via-mcprun-library-function) — "one static cross-compiled executable, no per-platform assets, `go install` it and forget it" — survives v0.8.3 intact.

- **The convention-over-configuration `.ken/index.bin` path keeps SDK author code identical to v0.6.0.** Auto-discovery means an existing v0.6.0 SDK author adds two lines to their build script (`ken build-index ./corpus -o ./corpus/.ken/index.bin` before `go build`) and gets the cold-start improvement without touching their Go code. `Options.PrebuiltIndex` is the escape hatch for non-conventional layouts.

- **Pre-built indices + live DB chunks (`mcp/db.Setup`) works out of the box.** The loaded `*Index` carries the model reference supplied via `LoadOptions.Model`, so `WithExtraChunks` — the v0.8.0 Part 3 chunk-integration primitive that powers the `mcp/db.Refresher → mcp.Run` `Start` callback — works on loaded indices the same way it works on freshly-built ones. SDK authors who use both can ship a binary that boots fast AND integrates live DB schemas into search results.

- **Walker now skips `.ken/` by default** in both `WalkFS` and `Matcher.ShouldIndex`. Aligns with the existing `.git/` prune (same pattern, same rationale: "this directory holds build artifacts, not corpus content"). Operators with a literal `.ken/` directory of indexable content lose those files; this is documented as a convention but expected to be rare (the name was chosen specifically for the v0.8.3 optimization, not borrowed from an existing widely-used convention).

- **Calibration credibility upheld.** Cold-start time is the specific gap closed; this is not a retrieval-quality improvement, not a recall improvement, not a search-ranking change. The lazy-fallback path preserves the v0.6.0 behavior on any failure mode, so operators can't end up worse off than before. The release narrative ("v0.6.0 shipped embedded corpora; v0.8.3 makes their cold start fast") is honest about scope and doesn't conflate the optimization with system behavior changes downstream of indexing. Same discipline as [ADR-022](#adr-022-rename-column--rename-constraint-folding-via-eager-application-v081-part-c) (Tier-1 chunk fidelity, NOT retrieval recall) and [ADR-021](#adr-021-mariadb-first-class-engine-support-v081-part-b) (chunk-rendering consistency, NOT ranking).

- **Format version is at 1 for v0.8.3.** Any future incompatible change (new fields in the header, restructured sections, different vec encoding) bumps the format-version constant; mismatched files trigger `ErrFormatVersion` and the lazy-fallback rebuild path. SDK authors re-run `ken build-index` on each ken upgrade that bumps the format version. The ken-version field IS NOT version-gating — same format version + different ken version loads cleanly. This keeps the upgrade surface narrow: only format-bumping releases force a rebuild.

- **No new third-party dependencies.** Custom binary serialization uses `encoding/binary`, `hash/crc32`, and the existing `io/fs` plumbing. The dep tree stays clean.

- **v0.8.3 ready to tag after this lands.** Four-commit cadence on `v0.8.3-prebuilt-indices`: (1) serialize/deserialize primitives + walker prune, (2) `ken build-index` CLI subcommand, (3) `mcp.Run` auto-discovery + `Options.PrebuiltIndex`, (4) docs (this ADR + CHANGELOG + README + DESIGN.md). Closes [#10](https://github.com/townsendmerino/ken/issues/10).

---

## ADR-025: Perf-campaign Phase 1 investigation outcome — hotspot identification across small + medium workloads

**Status:** Accepted

**Date:** 2026-05-26

**Extends:** [ADR-010](#adr-010-tree-sitter-via-gotreesitter-instead-of-wazerowasm) (gotreesitter selection), [ADR-011](#adr-011-default-chunker-stays-regex-in-v020-treesitter-is-opt-in) (regex stays default), [ADR-016](#adr-016-embedded-corpus-mcp-build-pattern-via-mcprun-library-function) (mcp.Run pattern), [ADR-023](#adr-023-gotreesitter-grammar_subset-machinery--binary-size-reduction-outcome-v082-investigation-outcome) (gotreesitter selectivity investigation), [ADR-024](#adr-024-pre-built-embedded-indices-for-mcprun-v083) (pre-built indices).

**Related:** `outputs/treesitter-port-considerations.md` (future-ADR seed; the arena finding here motivates that seed's Tier 2 ranking).

## Context

The v0.8.x release cadence settled the calibration story; v0.9 (or rolling point releases — release shape is task #11, decided after this ADR) is the perf-campaign cycle. [Phase 0](PERF.md) built the measurement infrastructure (`ken perf` subcommand + per-package benchmarks + `scripts/perf_collect.sh` + `docs/internal/PERF.md` methodology). Phase 1 ran the harness against the small (ken itself, ~150 Go files) and medium (semble bench corpus, ~378k chunks, ~715 MB of source) workloads on native arm64 darwin / Go 1.26.3 / M1 Pro.

Phase 1 was an **investigation pass**, not a publication pass. Discipline carries over from `docs/BENCH.md` and ADR-023:

- The "Headline numbers" section in `docs/internal/PERF.md` remains empty until N≥10 median + second-machine confirmation per the documented acceptance threshold.
- The "Empirical findings" section, by contrast, holds investigation-pass observations with explicit single-run / single-machine caveats — same shape as the per-version findings in `docs/BENCH.md`.
- Predictions were written down in `outputs/perf-investigation-plan.md` **before** the harness was built. Phase 1 grades them rather than retrofitting.

Workloads measured:

- **Small** (`ken@97feb5e`): 1,560 chunks across 9 mode × chunker combos. ~26 s wall total. Cross-product complete.
- **Medium** (`~/.cache/semble-bench`, ~19 repos aggregate per upstream `repos.json`): 378k–380k chunks. 4 combos run (bm25/hybrid × regex/treesitter). 2h 49m wall total (treesitter combos dominated). Cross-product *not* complete; targeted via `scripts/perf_collect.sh --modes --chunkers` filter.
- **Large + giant — explicitly skipped this pass.** Reasoning under "Alternatives considered" below.

One incidental finding the harness exposed before any measurement could complete: a panic in `internal/chunk/treesitter/Chunker.Chunk` when gotreesitter produces a parse-error-recovery node with `EndByte() < StartByte()`. Fixed in commit `120fc06` before Phase 1 medium proceeded. **The perf campaign surfaced a latent correctness bug** — a documented Phase-0 expectation that materialized.

## Phase 1 findings — predictions vs evidence

The seven predictions from `outputs/perf-investigation-plan.md`, graded:

| # | Prediction | Status | Evidence |
| --- | --- | --- | --- |
| 1 | Embed inference dominates indexing CPU | ✓ confirmed at small (46% of hybrid-treesitter index CPU) | Medium-scale CPU profile needed to compute exact share at 378k chunks; the wall-time signature (semantic mode adds ~140s on top of bm25 mode at medium-regex) is consistent. |
| 2 | BM25 tokenizer allocates heavily | ✓ **strongly** confirmed; 45% of bm25-regex indexing allocs at medium (8.7 GB out of 19.3 GB) | Tokenizer split: `Tokenize.func1` 5.7 GB + `camelSplit` 2.6 GB + `Tokenize-range1` 0.4 GB. Per-chunk alloc grew from 35 KB (small) → 53 KB (medium). |
| 3 | ANN flat dominates search at scale | ✓ **strongly** confirmed | Hybrid p50 = 11.5 ms at small (1,560 chunks) → 193 ms at medium (378,524 chunks). bm25 p50 stayed at ~12 ms; the entire delta is the semantic + ANN-flat work. **Medium hybrid-regex pprof: `ann.Flat.Query` is 78.56% of search CPU (cum) at medium scale.** Inside that: ~68% is vector cosine (`Flat.Query.func1`), ~32% is `sort.Slice` over the candidate-score list. |
| 4 | File walk is NOT hot | ✓ confirmed (essentially invisible in CPU profiles at both scales) | At medium bm25-regex: walk is dwarfed by tokenizer + GC. |
| 5 | Watch swap is NOT hot | not measured (no medium-scale watch profile) | `ken perf watch` exists; the medium-scale watch-mode latency is an open question Phase 1 didn't answer. |
| 6 | ParserPool is NOT hot | ✓ confirmed | No pool overhead visible in either small or medium treesitter profiles. The ADR-010 pool-reuse pattern is doing its job. |
| 7 | Rerank is NOT hot at k=20 | ✗ falsified at small; ✓ **re-confirmed at medium** | 28% of hybrid-regex search CPU at small goes through `rerankTopK` (24% `regexp.tryBacktrack` inside `filePathPenalty`). **At medium, `filePathPenalty` does not appear in the top 30 cumulative frames** (cutoff ~0.5%). At scale, ANN flat (78.56%) so dominates search that the rerank+penalty work is invisible in the noise. The prediction "rerank not hot" holds at production scale even though it failed at toy scale. |
| (new) | GC dominates CPU at scale | ✓ **new finding** | At medium bm25-regex: `runtime.gcBgMarkWorker` + `tryDeferToSpanScan` + `scanObject` consume ~50% of indexing CPU and ~36% of search CPU. Direct consequence of the allocation pattern, but only visible at scale where the heap stabilizes at 7–8 GB. |
| (new) | Treesitter scales super-linearly | ✓ **new finding** | Per-chunk arena allocation: 450 KB at small → 2.9 MB at medium (6.4× growth per chunk). 1.09 TB cumulative allocation to index 379k chunks via bm25-treesitter. 37-minute wall time. The gotreesitter arena pattern (per `outputs/treesitter-port-considerations.md`) compounds badly with file complexity in the semble corpus. |
| (new) | `bm25.Index.TopK` uses `sort.Slice` instead of a heap | ✓ **new actionable finding** | At medium bm25-regex search: `sort.Slice` + `sort.pdqsort_func` = 36% of search CPU. At 378k chunks with K=10, the O(N log N) sort over all candidates is the dominant in-tree search cost. Classic top-K heap (O(N log K)) is the canonical fix. |

**Two predictions were strongly confirmed, two were falsified or refined, three new findings emerged.** This is the calibration discipline working as intended — writing predictions before measurement makes the surprises visible.

## Hotspot ranking — ordered by ROI × confidence × in-tree-actionability

| # | Hotspot | Location | Confidence | Expected impact | Status |
| --- | --- | --- | --- | --- | --- |
| 1 | **ANN flat scan dominates hybrid search** | `internal/ann/` | **Strongly confirmed at medium (78.56% of hybrid-regex search CPU)** | Search latency −80%+ at giant scale with HNSW; ~30% achievable inside flat via candidate-selection refactor (see #1a) | Two-track. Real algorithmic fix (HNSW) requires dep choice or in-tree impl; in-tree quick-win (#1a) is the candidate-selection refactor inside the existing flat scan. |
| 1a | `ann.Flat.Query` uses `sort.Slice` over ALL candidates for top-K | `internal/ann/` | High (medium-scale CPU profile — 30.88% of hybrid search CPU is `sort.Slice` inside `Flat.Query`) | Hybrid search CPU −20–30% at medium scale without changing the algorithm; complementary to eventual HNSW | **Worth shipping immediately.** Same min-heap-of-size-K refactor as #2; in fact the two changes share a code template. |
| 2 | `bm25.Index.TopK` uses `sort.Slice` over all candidates | `internal/bm25/` | High (medium-scale CPU profile — 36% of bm25 search CPU is `sort.Slice`) | Search CPU −25–40% on bm25 path at corpus sizes ≥ ~100k chunks | Worth shipping. Same min-heap pattern as #1a; sister refactor. |
| 3 | BM25 tokenizer string allocations | `internal/bm25/Tokenize` + `camelSplit` | High (medium-scale alloc profile — 45% of bm25-regex indexing allocs) | Indexing allocs −45%, GC time −proportional, indexing wall time −20–30% | Worth shipping. Use `[]byte` slices into source rather than allocating new strings; pool scratch buffers. |
| 4 | Embed `weightedMeanPoolSafe` accumulator | `internal/embed/` | High at small (23% of hybrid-treesitter CPU); medium-scale share not separately computed but the embed pass scales linearly with chunk count | Indexing CPU −10–20% with SIMD; pure-Go-no-cgo constraint shapes the implementation | Worth investigating. `golang.org/x/sys/cpu` + per-GOARCH assembly stubs is the established pattern (`crypto/sha256` etc. do this). Defer until #1a–#3 land — the relative share will shift. |
| 5 | gotreesitter node arena | upstream | High (468 MB / 53.6% of allocations at small; 1.09 TB cumulative at medium) | Bounded by gotreesitter upstream + ADR-023's selectivity work | Future-ADR seed exists (`outputs/treesitter-port-considerations.md`). Tier 0–3 mitigation hierarchy documented. Don't port; consider targeted upstream PR after the open selective-grammar issue resolves. |
| 6 | HNSW (replacement for ANN flat) | `internal/ann/` | Documented in `docs/DESIGN.md` §10 | Search latency −80%+ at giant scale | Documented future work. Real dependency (pure-Go HNSW choice) or in-tree impl. Trigger to elevate: real-user latency complaint, OR a pure-Go HNSW dep emerges that meets the no-cgo bar with good maintenance signal. |
| (demoted) | `filePathPenalty` regex backtracking | `internal/search/penalties.go` | High at small (24% of hybrid search CPU); **falsified at medium** (does not appear in top-30 frames; <0.5% cumulative) | Search CPU −up to 24% at small scale only; negligible at production scale | **Demoted from Ship-tier.** At small scale the regex matters; at scale it's lost in ANN-flat noise. Only worth fixing if a real user reports search-latency pain on a small-corpus deployment (e.g., a few-thousand-chunk SDK docs server). |

## Decision

**Three categories of follow-on:**

### A. Ship (subject to NDCG calibration discipline)

- **#1a `ann.Flat.Query` → min-heap of size K** (NEW Ship-tier — highest priority after medium pprof). 30.88% of hybrid search CPU at medium goes to `sort.Slice` inside `Flat.Query` sorting all 378k candidate scores to take K=10. Min-heap-of-size-K is the canonical fix. **NDCG regression risk:** none expected — top-K by cosine score is deterministic regardless of full-sort vs heap-based selection, so the same K results emerge in the same order. NDCG@10 re-run is the safety check but expected to be a no-op.
- **#2 `bm25.Index.TopK` → min-heap of size K.** Sister refactor to #1a — same pattern, same code template, same NDCG-risk profile (none expected). 36% of bm25 search CPU at medium. Often shipped together with #1a as a single PR since the two share a heap utility.
- **#3 BM25 tokenizer alloc reduction.** Pool allocations; use `[]byte` slices into source for camelCase/PascalCase splits rather than allocating fresh strings; share scratch buffers across calls. **NDCG regression risk:** real — different tokenization changes IDF, which changes BM25 ranking. The discipline is: run the semble NDCG benchmark before and after, accept ≤ ±0.005 shift per `docs/BENCH.md`, write a calibration-discipline ADR if the shift is bigger and the perf win is large enough to justify.

**`filePathPenalty` is NOT in the Ship tier** despite being a clear hotspot at small scale, because the medium pprof showed it disappears into ANN-flat noise (<0.5% of cumulative search CPU at 378k chunks). Documented as demoted in the hotspot table; revisit only on a real small-corpus user report.

These three are independent and ship as **rolling point releases** (decided alongside this ADR; no v0.9.0 themed campaign):

- **v0.8.4** — #1a + #2 paired heap refactor. Lower risk (no NDCG impact expected), immediate user-impact win, natural to ship together since they share a heap-of-size-K code template.
- **v0.8.5** — #3 BM25 tokenizer alloc reduction. Carries real NDCG-regression risk; ships with its own calibration-discipline ADR if the shift exceeds ±0.005 per `docs/BENCH.md`.

The rolling shape was chosen over a v0.9.0 themed campaign because each fix is independently valuable and there's no narrative coherence benefit to gating them on a single release. Future fixes (#4 embed SIMD, #6 HNSW) can also ship rolling when their triggers fire.

### B. Defer with named triggers

- **#4 HNSW for ANN flat replacement.** Gated on choosing a pure-Go HNSW dependency or accepting an in-tree implementation. Documented in `docs/DESIGN.md` §10. Trigger to revisit: a real user reports search latency as actionable pain on a 100k+ chunk corpus (already on the threshold for some users), OR a pure-Go HNSW dep emerges that meets the no-cgo bar with good maintenance signal.
- **#5 Embed SIMD accumulator.** Gated on #1–#3 landing first (the relative share will shift) and on a pure-Go-no-cgo-compatible SIMD pattern matching the `crypto/sha256` precedent for per-GOARCH assembly. Trigger to revisit: post-Phase-1-fixes re-measurement still shows embed accumulator > 20% of indexing CPU.
- **#6 gotreesitter arena scaling.** Per `outputs/treesitter-port-considerations.md` Tier 2: targeted upstream PR after the open selective-grammar issue resolves. Triggers documented in that seed.

### C. Architectural ceilings — document, don't try to "fix"

- **GC dominates CPU at scale.** This is the *consequence* of the allocation pattern, not a separate hotspot. Reducing allocations (#2 above) reduces GC pressure proportionally; that's the actionable lever. Documented in `docs/internal/PERF.md` "Empirical findings" so users running ken-mcp on corpora ≥ ~100k chunks know that the steady-state heap is 5–10 GB and to size their deployment hardware accordingly.
- **Heap inuse at medium = 7 GB.** Not a bug, a property — the BM25 postings list + chunk source storage + (for hybrid) embed matrix all scale linearly with chunk count. Document the per-chunk memory cost in `docs/internal/PERF.md` so SDK authors using `mcp.Run` can estimate their binary's runtime memory ahead of release.

### D. Calibration discipline carries forward

- **`docs/internal/PERF.md` "Headline numbers" stays empty** until N≥10 median + second-machine confirmation per the documented acceptance threshold. Phase 1 was investigation, not publication.
- **`docs/internal/PERF.md` "Empirical findings" updates** to summarize the corroborated predictions, the falsified ones, and the new findings — with cross-reference to this ADR. Includes the single-run / single-machine caveats and the Rosetta vs native arm64 footnote.
- **Reconcile `docs/internal/PERF.md` workload table.** The "~19 repos aggregate" entry for the medium workload is inconsistent with `docs/BENCH.md`'s 63-repo CoIR mention; the actual indexed chunk count (~378k) is closer to the full corpus than to a 19-repo subset. Cleanup ships in the same docs commit as the "Empirical findings" update.

## Alternatives considered

- **Run large + giant workloads before this ADR.** Rejected. Per the analysis at the end of Phase 1: small + medium gave decisive evidence of the scaling pattern; large would mostly confirm linear-or-worse extrapolation already visible. Per-chunk treesitter alloc at large scale projects to ~10–20 MB/chunk extrapolating the small→medium 6.4× growth ratio; verifying that exactly would cost ~12+ hours of wall time for marginal new information. Re-runnable at any time via the harness (the targeted `--modes`/`--chunkers` filter makes it tractable when justified). Trigger to revisit: an SDK author or operator reports actionable pain at large scale, or a perf-regression check needs a baseline.
- **Run the full mode × chunker cross-product at medium.** Rejected during Phase 1 itself (the original `scripts/perf_collect.sh medium` was killed at ~1:28 elapsed when extrapolation showed 4–8 hours wall time). vscode-claude added `--modes` / `--chunkers` filters; the targeted 4-combo run took 2h 49m. Full cross-product wasn't necessary because semantic mode's wall time at small was indistinguishable from hybrid mode (the BM25 indexing is "free" within semantic build path), and line chunker at scale answers no question regex doesn't already answer.
- **Publish "Headline numbers" from Phase 1's single-machine, single-run data.** Rejected. `docs/internal/PERF.md`'s acceptance threshold is explicit: median-of-N + second-machine confirm before publication. Phase 1's job was investigation, not headlining. Promoting any of these numbers without the calibration steps would erode the discipline this campaign exists to demonstrate.
- **Commit to a v0.9.0 themed release shape inside this ADR.** Rejected by scope. Release-shape decision (task #11) is the *next* decision after this one; it depends on whether the three Ship-tier fixes (above) land as separate Parts (matching v0.8.x cadence) or as a single release. Forcing the decision into this ADR confuses what's empirical (the findings) with what's a release-engineering choice.
- **Ship "treesitter is alloc-pathological at scale" as a calibration warning in the README.** Considered, deferred. The story is more nuanced than a single bullet: regex is the documented default, treesitter is opt-in, `mcp.Run` users via ADR-024 pay the cost once at `ken build-index` time. The right place for this nuance is the `docs/internal/PERF.md` "Empirical findings" section, not the README. Revisit if treesitter usage patterns shift.

## Consequences

- **Three perf wins enter the in-tree development queue** (#1a `ann.Flat.Query` heap, #2 `bm25.Index.TopK` heap, #3 BM25 tokenizer alloc reduction). #1a and #2 are natural pair partners (same heap-of-size-K template) and may ship as one PR; #3 is independent and has a real NDCG-regression-risk check. Each ships with its own ADR when it lands (ADR-026, ADR-027 in some ordering), with the calibration discipline (benchstat sign-off, second machine confirm, NDCG@10 within ±0.005) applied per fix.
- **Future-ADR seed `outputs/treesitter-port-considerations.md` gains a concrete empirical anchor.** The Tier 2 upstream-PR conversation now has medium-scale numbers (1.09 TB cumulative arena alloc, 37-minute index wall time) to point at if/when the conversation is opened upstream.
- **`docs/internal/PERF.md` "Empirical findings" lands with the Phase 1 summary** in the same docs commit as this ADR. Workload-table cleanup (19 vs 63 repos) lands alongside.
- **The harness is proven by use.** `ken perf` + `scripts/perf_collect.sh` + `internal/perf/` survived a real investigation pass including a correctness-bug-discovery, a multi-hour stress run, and the chunker-fix loop. Phase 0's "harness before headlines" discipline paid for itself.
- **Large + giant workloads are deferred but reachable.** The harness exists; the filter flag makes targeted runs cheap; the only thing missing is the trigger to re-run. Documented so a future contributor knows the cost (~12+ hours for a treesitter combo at large scale) and the value (confirmatory rather than informative).
- **The calibration credibility holds.** v0.8.2 / ADR-023 established the "investigation outcome with named triggers" shape. This ADR extends it: investigation that surfaces concrete fixes (rather than ADR-023's "the answer is no"). Both shapes are legitimate; both keep the "headline numbers don't ship until they survive a re-run" discipline visible.

## Triggers to ADR-026+ (or to follow-on investigation passes)

- **Trigger A — Ship-tier fix lands:** when #1, #2, or #3 lands as a PR, the accompanying ADR documents the per-fix calibration result (benchstat output, NDCG@10 delta, before/after wall-time numbers on a specific machine spec). One ADR per fix.
- **Trigger B — Search latency reports from real users:** if an `mcp.Run` SDK author or `ken-mcp` operator reports search latency as actionable pain on a 100k+ chunk corpus, escalate the HNSW conversation (#4) from documented-future to active investigation. New ADR for the dep / implementation choice.
- **Trigger C — Post-fix re-measurement:** after #1–#3 land, re-run the small + medium workloads. If embed accumulator (#5) is still >20% of indexing CPU, escalate to active SIMD investigation. If GC pressure stays the same (it should drop with #2's alloc reduction), something is wrong and we need to re-investigate.
- **Trigger D — Large-scale measurement need:** any of (a) a regression check needs a large-scale baseline, (b) the chromium decision in the giant workload becomes a real product question (e.g., kubernetes-demo from `outputs/oss-demo-playbook.md` shows hybrid search behaving differently than medium predicted), (c) an SDK author reports binary-size or memory pain at large scale.
- **Trigger E — gotreesitter upstream conversation:** when the open selective-grammar issue resolves (whether merged or rejected), the goodwill budget clears for the arena-optimization PR per `outputs/treesitter-port-considerations.md` Tier 2.

---

## Open questions deliberately not resolved here

- ~~Exact share of `filePathPenalty` at medium scale.~~ **Resolved during draft review:** medium pprof showed `filePathPenalty` does not appear in the top 30 cumulative frames (cutoff <0.5%). ANN flat (78.56%) dominates so completely that the regex backtracking is lost in noise at production scale. `filePathPenalty` demoted from Ship tier accordingly; see hotspot table for the full reasoning.
- **Why is per-chunk tokenizer allocation GROWING with corpus size?** 35 KB/chunk → 53 KB/chunk going small → medium isn't constant overhead per-chunk. Suggests the postings-list hashmap insertion is paying real cost as the vocabulary grows (more rehashing? more collision handling?). Worth a pprof read at the medium scale before fix #2 ships, to make sure the fix targets the actual hotspot.
- **Where is the chromium-vs-synthetic decision for the "giant" workload row in `docs/internal/PERF.md`?** Still TBD. Phase 1 didn't need to resolve it. Resolve when Trigger D fires.

## Files this ADR's promotion will touch

- `docs/internal/DECISIONS.md` — append ADR-025; cross-link from ADR-010, ADR-011, ADR-016, ADR-023, ADR-024 summary table.
- `docs/internal/PERF.md` — populate "Empirical findings" section with the Phase 1 summary + cross-reference to this ADR. Fix the workload-table 19-vs-63-repos inconsistency. "Headline numbers" stays empty.
- `outputs/treesitter-port-considerations.md` — already exists as the future-ADR seed. No edits needed; this ADR references it.

CHANGELOG entry for the next release: ADR-025 lands as a docs-only commit. No code ships in this ADR; each follow-on fix gets its own commit + CHANGELOG entry.

---

## ADR-026: Paired heap refactor for `ann.Flat.Query` + `bm25.Index.TopK` (v0.8.4)

**Status:** Accepted

**Date:** 2026-05-26

**Issue:** TBD (open a townsendmerino/ken issue for the v0.8.4 refactor + cross-link here on merge).

**Extends:** [ADR-025](#adr-025-perf-campaign-phase-1-investigation-outcome--hotspot-identification-across-small--medium-workloads) (perf-investigation outcome — ADR-026 references the empirical findings produced by that investigation).

### Context

[ADR-025](#adr-025-perf-campaign-phase-1-investigation-outcome--hotspot-identification-across-small--medium-workloads)'s medium-scale (semble bench corpus, 378,524 chunks) CPU profile surfaced two adjacent hotspots that share the same structural defect:

- **`ann.Flat.Query` is 78.56% of hybrid-regex search CPU at medium**, and **30.88% of that is `sort.Slice`** sorting all 378k candidate cosine-similarity scores to take K=10. Top-K-of-N via full sort is O(N log N).
- **`bm25.Index.TopK` is 36% of bm25-regex search CPU at medium**, all in `sort.Slice` / `sort.pdqsort_func` / `sort.partition_func` over the positive-score candidate list. Same O(N log N) pattern.

Both code paths use the same anti-pattern: score every candidate into a slice, sort by score descending, take the first K. Top-K via min-heap of size K is the canonical fix — O(N log K), which at K=10 and N=378k is roughly 50× cheaper in the asymptotic bound and produces the same K items in the same order (ties handled identically when the caller iterates input in natural index order, which both call sites do).

The refactors are mechanically identical and ship together as a single PR + single ADR. The heap is shared via a new `internal/topk` leaf package so a future caller (a reranker, a `find_related` re-implementation, etc.) doesn't have to re-derive the same primitive.

### Decision

Replace sort-then-slice with min-heap-of-size-K in `ann.Flat.Query` and `bm25.Index.TopK`, sharing a new `internal/topk` package.

**Three surfaces:**

1. **`internal/topk` package** (NEW, leaf). Generic `Selector[T any]` with capacity K. `Push(item, score) bool` (strict `>` on at-capacity replace — see tie-breaking below); `Result() []ItemWithScore[T]` returns descending by score; `Len() int`. Hand-rolled `siftUp` / `siftDown` rather than `container/heap` because `heap.Interface` takes `interface{}` and would force boxing — defeating the perf goal.

2. **`ann.Flat.Query` two-path refactor.** `k<=0 || k>=Len` keeps the existing full-sort path (preserves the documented "return all, sorted" escape hatch); `0 < k < Len` takes the heap. A `sort.SliceStable` at the end of the heap path imposes the documented ascending-Index tie-break that the heap on its own doesn't guarantee (K-sized stable sort is O(K log K), cheap at K=10).

3. **`bm25.Index.TopK` two-path refactor.** `k<0` keeps the full-sort path (the original `if k >= 0 && k < len(res)` truncation gate's escape hatch); `k>=0` takes the heap. `k=0` selector returns empty, matching the original gate's k=0 behavior. Same `sort.SliceStable` tie-break for ascending Doc id.

**Tie-breaking design.** The heap's `Push` uses strict `>` (not `>=`) on at-capacity replace, meaning a new item tying the current minimum's score is **discarded**. Both call sites iterate input in their natural index order (`for i, v := range f.vecs` / `for d, s := range scores`), so the first-seen-of-tied-group is naturally the smaller-index one — which the heap retains. The final `sort.SliceStable` re-orders any ties within the K result by ascending secondary key for deterministic output that matches the pre-refactor public contract.

### Alternatives considered

- **Quickselect / `container/heap` without a generic wrapper.** Rejected for code-template-reuse reasons. The two callers want the same primitive; sharing a small generic package keeps the heap logic in one place + lets future callers reuse it without re-deriving heap correctness. `container/heap` specifically also has the `interface{}` boxing problem ([ADR-001](#adr-001-pure-go-no-cgo)'s pure-Go-no-cgo discipline argues for letting generics handle this — Go 1.26 is the project's toolchain pin, generics are fully available).

- **Partial sort.** Rejected because Go's stdlib doesn't have one. Implementing a partial sort would be more code than the min-heap, with no compensating benefit.

- **Custom min-heap inlined in each caller.** Rejected for DRY. The heap logic is small but non-trivial (siftUp/siftDown correctness, tie-breaking semantics, the K-sized stable sort at the end). Duplicating it in `internal/ann` and `internal/bm25` would also duplicate the test surface and the future-maintenance burden.

- **Pure-Go HNSW** (replacement for ANN flat). Rejected for v0.8.4 scope; documented future work in [docs/DESIGN.md §10](../DESIGN.md#10-risk-register) and ADR-025 hotspot #6. The trigger to revisit: real user reports search latency on a 100k+ chunk corpus as actionable pain, OR a pure-Go HNSW dep emerges that meets the no-cgo bar with good maintenance signal. The heap refactor is complementary to eventual HNSW (every search algorithm needs a top-K selector); landing it now is not the same kind of work.

- **Placing `topk` at `internal/search/topk`** rather than the standalone leaf `internal/topk`. Considered. Rejected because the heap utility has no business knowing about search internals; a future reranker or `find_related` re-implementation needing top-K shouldn't have to import from the search subsystem. Standalone leaf placement keeps the dependency direction one-way (concrete subsystems depend on the primitive, not the reverse).

- **Push returns `bool` vs no return value.** Kept the bool return per the briefing. Rare but useful for callers that want to short-circuit expensive scoring on items that obviously won't make the cut (e.g., score = 0). Not applicable to the two production callers today; cheap to expose now if a future caller needs it.

### Consequences

**Measured impact** (Apple M1 Pro / Go 1.26.3 / darwin/arm64 / `CGO_ENABLED=0 -trimpath -ldflags='-s -w'`):

- **`ann.Flat.Query` micro-benchmark** (`go test -bench BenchmarkFlatQuery -benchmem -count=10`, before = perf-investigation HEAD with sort-based code, after = this commit):

  ```
                   │ sort-based       │  heap-based                     │
                   │   sec/op         │   sec/op       vs sort          │
  FlatQuery/N1k    │ 182.3µs ± 1%     │ 103.2µs ± 1%   −43.4% (p=0.000) │
  FlatQuery/N10k   │ 2.253ms ± 1%     │ 990.2µs ± 1%   −56.1% (p=0.000) │
  FlatQuery/N50k   │ 12.297ms ± 1%    │ 4.926ms ± 0%   −59.9% (p=0.000) │

                   │ B/op             │  B/op          vs sort          │
  FlatQuery/N1k    │ 16,472 ± 0%      │ 728 ± 0%       −95.6% (p=0.000) │
  FlatQuery/N10k   │ 163,928 ± 0%     │ 728 ± 0%       −99.6% (p=0.000) │
  FlatQuery/N50k   │ 802,904 ± 0%     │ 728 ± 0%       −99.9% (p=0.000) │
  ```

- **`bm25.Index.TopK` micro-benchmark** (via `BenchmarkScore` which is dominated by TopK; same harness):

  ```
                   │ sort-based       │  heap-based                     │
                   │   sec/op         │   sec/op       vs sort          │
  Score/N100       │ 3.934µs ± 2%     │ 1.099µs ± 1%   −72.1% (p=0.000) │
  Score/N1000      │ 8.782µs ± 2%     │ 1.995µs ± 1%   −77.3% (p=0.000) │

                   │ B/op             │  B/op          vs sort          │
  Score/N100       │ 2,711 ± 0%       │ 1,586 ± 0%     −41.5% (p=0.000) │
  Score/N1000      │ 15,961 ± 0%      │ 5,961 ± 0%     −62.7% (p=0.000) │
  ```

- **End-to-end medium-corpus search latency** (`scripts/perf_collect.sh medium --modes=bm25,hybrid --chunkers=regex`, 1000 queries / k=10 against a warm 378,524-chunk semble medium index):

  | combo | metric | sort-based | heap-based | Δ |
  |---|---|---|---|---|
  | bm25-regex SEARCH | p50 | 11.5 ms | **1.88 ms** | **−83.7%** |
  | bm25-regex SEARCH | p95 | 23.2 ms | 5.14 ms | −77.8% |
  | bm25-regex SEARCH | p99 | 31.4 ms | 20.58 ms | −34.5% |
  | bm25-regex SEARCH | allocs/q | 8.7 MB | 2.89 MB | −66.8% |
  | hybrid-regex SEARCH | p50 | 193 ms | **113.2 ms** | **−41.4%** |
  | hybrid-regex SEARCH | p95 | 214 ms | 124 ms | −42.1% |
  | hybrid-regex SEARCH | p99 | 391 ms | 141 ms | −64.0% |
  | hybrid-regex SEARCH | allocs/q | 14.5 MB | 2.98 MB | −79.4% |

  The hybrid p50 result hits the upper end of ADR-025's 25-40% projection; bm25 search came in well above projection because TopK was a larger fraction of bm25 search CPU than ann flat is of hybrid search CPU. **Index times unchanged** (bm25 index 55s → 59s, hybrid index 178s → 182s — within noise; the refactor only touches the search path, not indexing).

- **NDCG@10 safety check** (semble bench corpus, 63 repos / 1251 tasks, all 3 modes, `--chunker regex` both sides for clean apples-to-apples):

  | mode | sort-based NDCG@10 | heap-based NDCG@10 | Δ |
  |---|---|---|---|
  | bm25 | 0.6237 | 0.6237 | **0.0000** (exact) |
  | semantic | 0.6469 | 0.6469 | **0.0000** (exact) |
  | hybrid | 0.8418 | 0.8418 | **0.0000** (exact) |

  Exact match in all three modes confirms the top-K determinism claim. The heap refactor changes search *latency*, not search *results* — the K items emerging in the K positions are identical. NDCG-bench wall times are dominated by per-repo index build (unchanged by this refactor) and vary within noise from machine load; the relevant signal is the NDCG@10 value, not the wall time.

- **`internal/topk` is reusable.** A future caller (the rerank pipeline if/when it becomes hot at small scale per ADR-025 hotspot #7's small-scale finding; a `find_related` re-implementation; etc.) can take a dependency without re-deriving the heap correctness invariants.

- **Net allocations per call go up by 3** (4 → 7 for ann, 5 → 8 for bm25). That's the `Selector` + result slice + scratch slice overhead. The aggregate *bytes* allocated drops by orders of magnitude because the full N-sized candidate slice is gone. Net GC pressure drops dramatically — visible as the per-query alloc bytes collapse in the end-to-end table above.

- **No public API changes.** Both `ann.Flat.Query` and `bm25.Index.TopK` keep their existing signatures + documented tie-breaking semantics. Existing callers (`search.hybridSearch`, the rerank pipeline, etc.) integrate unchanged.

- **No new third-party dependencies.** `internal/topk` is standard-library-only.

- **Two-commit PR shape.** Commit 1 lands `internal/topk` + tests + micro-benchmark; commit 2 lands the two refactors + this ADR-026. Reviewers see the new utility in isolation before reading the integrations.

- **Future perf work informed.** The remaining ADR-025 hotspots (BM25 tokenizer allocs at ~45% of indexing allocs, embed `weightedMeanPoolSafe` accumulator) will ship in subsequent rolling point releases (v0.8.5+) as the analysis from this work feeds back into the prioritization. Specifically, with search latency cut as documented above, the embed inference cost becomes a proportionally larger fraction of cold-start time on the giant-corpus workload that the planning Claude's Phase 1 medium re-run will benchmark next.

---

## ADR-027: BM25 tokenizer allocation reduction — `[]rune` → `[]byte` + `sync.Pool` scratch + lowercase fast-path (v0.8.5)

**Status:** Accepted

**Date:** 2026-05-26

**Issue:** TBD (open a townsendmerino/ken issue for the v0.8.5 tokenizer-allocs work + cross-link here on merge).

**Extends:** [ADR-025](#adr-025-perf-campaign-phase-1-investigation-outcome--hotspot-identification-across-small--medium-workloads) (perf-investigation outcome that ranked BM25 tokenizer alloc reduction as Ship-tier #3), [ADR-008](#adr-008-bm25-tokenizer--verbatim-port-of-sembles-tokenspy-snake-case-compound-preservation) (verbatim-parity contract — this refactor explicitly affirms token-set parity is preserved).

### Context

[ADR-025](#adr-025-perf-campaign-phase-1-investigation-outcome--hotspot-identification-across-small--medium-workloads)'s medium-scale (semble bench corpus, 378,524 chunks) alloc_space pprof attributed **~45% of bm25-regex indexing allocations to the tokenizer**:

```
bm25.Tokenize.func1            5.7 GB  (29.6%)
bm25.camelSplit                2.6 GB  (13.5%)
bm25.Tokenize.func1-range1     0.37 GB (1.9%)
─────────────────────────────────────────────
Tokenizer subtotal            ~8.7 GB  (~45%)
```

Per-chunk tokenizer allocation grew from 35 KB (small workload, 1,560 chunks) to 53 KB (medium, 378k chunks) — not constant overhead; the cost grew with scale. GC dominated indexing CPU at scale (~50% per ADR-025), so the wall-time win from reducing tokenizer allocs is larger than the alloc-bytes number alone suggests — every byte not allocated is a byte the GC doesn't have to scan.

The hotspot's structural source: the tokenizer iterated text as `[]rune` (4 bytes per ASCII char) and called `strings.ToLower(string(run))` twice per token (once for the compound, once per camelSplit part), each conversion allocating. ASCII-only design — `isIdentStart` / `isIdentCont` reject non-ASCII bytes — means the `[]rune` decoding was paying UTF-8 overhead it didn't need.

[ADR-008](#adr-008-bm25-tokenizer--verbatim-port-of-sembles-tokenspy-snake-case-compound-preservation) makes the load-bearing constraint explicit: `Tokenize` is a verbatim port of `semble/tokens.py`, and any change that affects its token output violates the parity contract. The refactor's North Star is therefore "cut allocations without changing output bytes."

### Decision

Refactor `internal/bm25/tokenize.go` to scan input bytes directly (instead of decoding runes) and to share a `sync.Pool`-backed scratch buffer across calls for lowercase conversion. Public API unchanged: `Tokenize(text string) []string` and the documented identifier-extraction + split semantics are preserved byte-for-byte.

**Three allocation levers:**

1. **Byte-level scan replaces rune decoding.** ASCII identifier bytes (0x30-0x39, 0x41-0x5A, 0x5F, 0x61-0x7A) are all < 0x80; UTF-8 multi-byte sequences use only 0x80-0xFF bytes. The two ranges don't overlap, so a non-ASCII byte cannot false-positive as an identifier-start byte and correctly terminates any in-progress run. Byte-iteration is structurally faster than `for _, r := range text`'s UTF-8 decoder and removes the `[]rune` allocation entirely.

2. **`sync.Pool` scratch buffer for lowercase conversion.** Each `Tokenize` call gets one pooled `[]byte` (initial cap 256, grows for rare long identifiers); the buffer is reused for every `lowerString` slow-path conversion within the call and returned to the pool on defer. Eliminates per-token allocation for the lowercase output buffer.

3. **Lowercase fast-path.** `lowerString` scans for any uppercase ASCII byte first; if none, returns the input string unchanged — zero allocation, the same string header pointing at the same underlying bytes. Real-source identifiers (variable names, function names that are already lowercase or whose parts are after camelSplit) hit this path constantly. Slow path is exactly one allocation (`string(*scratch)` after the per-byte ToLower copy) — half of the prior pattern's two allocations (`string(rune-slice)` then `strings.ToLower(string)`).

**Implementation supporting details:**

- `isIdentStartByte` / `isIdentContByte` / `isUpperByte` / `isLowerByte` / `isDigitByte` replace the rune-typed predicates; trivially zero-allocation.
- `slices.Contains(rs, '_')` replaced by `strings.IndexByte(run, '_') >= 0` — single byte scan, no slice header overhead.
- `strings.SplitSeq(compound, "_")` iterator replaced by hand-rolled snake-split over the SOURCE run (not the lowercased compound) so each part takes the lowercase fast-path independently when already lowercase.
- `camelSplitBytes` is a verbatim algorithmic port of the old `camelSplit` to byte offsets — same ordered-alternation regex semantics from semble's `_CAMEL_RE`. Lowercase-only and digit-only matches emit source views via the fast-path; uppercase-containing matches go through `lowerString` with the shared scratch.

### Alternatives considered

- **Change `Tokenize` to return `[][]byte`.** Rejected for API-invasiveness and [ADR-008](#adr-008-bm25-tokenizer--verbatim-port-of-sembles-tokenspy-snake-case-compound-preservation) scope. The function-level contract is byte-equality of output strings, not return-type identity. The return-type change would ripple into `bm25.Build`'s map-insertion path, query-side tokenization, and external consumers via the `Tokenize` symbol — invasive blast radius for a marginal additional alloc reduction. Documented in the v0.8.5 briefing as deferred.

- **Optimize `bm25.Build`'s postings-map lookup with the `m[string(b)]` compiler-optimized pattern.** Considered for v0.8.5; rejected to keep scope tight. The win depends on `Tokenize` returning `[][]byte` (alternative above, also rejected) AND a more complex Build refactor. Flagged as v0.8.6+ candidate after we see whether post-v0.8.5 GC pressure still warrants chasing it. ADR-025 ranked `bm25.Build` at 37.6% of indexing allocations — the larger absolute target — but the path to reduce it is more invasive than the tokenizer rewrite.

- **Adopt a third-party tokenizer** (e.g., `bluge`'s analyzer chain, or import the Python tokenizer via a CGO bridge). Rejected for two reasons: (a) [ADR-008](#adr-008-bm25-tokenizer--verbatim-port-of-sembles-tokenspy-snake-case-compound-preservation)'s verbatim-parity contract with semble's `tokens.py` would be at the mercy of an external library's choices, and (b) [ADR-001](#adr-001-pure-go-no-cgo) forbids CGO regardless. The byte-level refactor keeps the existing verbatim port intact.

- **Pool the `Tokenize` result `[]string` slice in addition to the scratch buffer.** Considered. Started without it; allocation profiles after the refactor show the result-slice growth is not a remaining hotspot (the scratch-pool + fast-path already eliminate the per-token output-buffer allocation; the result slice itself is one growing append). Adding result-slice pooling would complicate the API contract (callers would need to release the slice, or the pool would have to copy) for diminishing return. Documented for v0.8.6+ if a future profile shows otherwise.

- **Skip the lowercase fast-path; always lowercase via scratch.** Marginally simpler code but loses the zero-allocation common case. Real-source identifiers are predominantly already lowercase (variable names, after-camelSplit parts); the fast-path is the load-bearing alloc-reduction lever, not just an optimization. Kept.

- **Pre-size the `parts []string` slice in `camelSplitBytes` and the `out []string` slice in `Tokenize`.** Considered. Both are small slices (typical identifier produces 2-4 parts; typical chunk produces tens of tokens). The growth-from-empty pattern's amortized cost is bounded, and pre-sizing would require an upfront scan to count. Skipped on cost/benefit — pre-sizing would help a marginal alloc count but adds a scan pass. Revisit if profile shows otherwise.

### Consequences

**Measured impact** (Apple M1 Pro / Go 1.26.3 / darwin/arm64 / `CGO_ENABLED=0 -trimpath -ldflags='-s -w'`):

- **`Tokenize` micro-benchmark** (`go test -bench BenchmarkTokenize -benchmem -count=10`, before = main HEAD with rune-based code, after = this commit):

  ```
              │  rune-based       │  byte-based                    │
              │   sec/op          │   sec/op       vs base         │
  Tokenize    │ 530.8µs ± 3%      │ 267.8µs ± 2%   −49.5% (p=0.000) │

              │  B/s              │  B/s           vs base          │
  Tokenize    │ 47.36 MiB/s ± 3%  │ 93.87 MiB/s ± 2%  +98.2% (p=0.000) │

              │  B/op             │  B/op          vs base          │
  Tokenize    │ 398.7 KiB ± 0%    │ 318.9 KiB ± 0%  −20.0% (p=0.000) │

              │  allocs/op        │  allocs/op     vs base          │
  Tokenize    │ 13,600 ± 0%       │ 5,613 ± 0%     −58.7% (p=0.000) │
  ```

  The `BenchmarkScore/N100` and `/N1000` benchmarks (which exercise `bm25.Index.TopK`) show no movement on this commit, as expected — those benchmarks build the corpus + index outside the timer, so they don't measure `Tokenize` in the hot loop.

  The B/op reduction (−20%) is below the briefing's −50-80% projection range. The shortfall is because the lowercase fast-path's zero-allocation case only fires when an identifier is entirely lowercase; real-source corpora have many mixed-case identifiers (camelCase, PascalCase) that take the slow path. The allocs/op reduction (−58.7%) is the more representative win because it captures every avoided allocation regardless of byte size. Briefing surfaced this as a flag-if-below; documented here as the actual measured outcome.

- **End-to-end medium-corpus re-measurement** (`scripts/perf_collect.sh medium --modes=bm25,hybrid --chunkers=regex`, 378,524 chunks):

  | combo | metric | rune-based | byte-based | Δ |
  |---|---|---|---|---|
  | bm25-regex INDEX | wall time | 59.8s | **40.1s** | **−32.9%** |
  | bm25-regex INDEX | objects allocated | 301.9M | **133.6M** | **−55.7%** |
  | bm25-regex INDEX | bytes allocated | 18.80 GB | 16.86 GB | −10.3% |
  | bm25-regex INDEX | heap inuse | 6.41 GB | 5.96 GB | −7.0% |
  | bm25-regex SEARCH | p50 | 1.78 ms | 1.76 ms | ≈ noise |
  | hybrid-regex INDEX | wall time | 192.8s | **166.8s** | **−13.5%** |
  | hybrid-regex INDEX | heap inuse | 9.13 GB | **5.71 GB** | **−37.5%** |
  | hybrid-regex INDEX | objects allocated | 820.3M | 652.0M | −20.5% |
  | hybrid-regex SEARCH | p50 | 117.9 ms | 124.1 ms | +5% (within noise) |

  The bm25 indexing wall-time reduction (−32.9%) hits the upper end of the briefing's −20-30% projection. The cause is the GC-time reduction: ADR-025 measured GC at ~50% of medium-scale indexing CPU; the tokenizer's −55.7% object-count drop translates roughly proportionally into GC pressure drop. The hybrid indexing wall-time reduction (−13.5%) is smaller because embedding is the dominant cost in hybrid mode and is unaffected by this refactor — but the bm25 portion of hybrid indexing still benefits, so the net is non-trivial. **Search latency is unchanged within noise** in both modes — sanity check that the refactor only touched the indexing tokenizer.

  Heap inuse drop on hybrid (−37.5%) is the most-surprising win: the steady-state hybrid index retains less working memory because lowercase tokens now share storage with the chunk text (the `lowerString` fast-path returns the source string view) rather than being independent copies. The chunk text is retained by the index anyway, so the net working set shrinks.

- **NDCG@10 safety check** (semble bench corpus, 63 repos / 1251 tasks, all 3 modes, `--chunker regex` both sides):

  | mode | rune-based NDCG@10 | byte-based NDCG@10 | Δ |
  |---|---|---|---|
  | bm25 | 0.6237 | 0.6237 | **0.0000** (exact) |
  | semantic | 0.6469 | 0.6469 | **0.0000** (exact) |
  | hybrid | 0.8418 | 0.8418 | **0.0000** (exact) |

  Exact match in all three modes is the strongest possible confirmation of token-set parity: same tokens in → same scores → same K results → same ranking → identical NDCG@10. The v0.8.5 briefing called this an exact-match requirement (the ±0.005 escape hatch in `docs/internal/PERF.md`'s acceptance threshold is reserved for intentional algorithmic changes; this refactor is "same tokens, less allocation" so any non-zero delta would indicate a bug). NDCG-bench wall times also dropped substantially (bm25 9.1 → 5.7s, semantic 32.4 → 23.3s, hybrid 81.9 → 50.4s — the per-repo index builds in the NDCG harness pay the same tokenizer-alloc-reduction win).

- **Token-set parity test extension.** `internal/bm25/tokenize_test.go` grew three new test functions ([commit 1/2 of this PR](https://github.com/townsendmerino/ken/commit/6042be5)):
  - `TestTokenize_AdversarialParity` (16 sub-cases): whitespace-only / pure-punctuation, camelCase + acronym + digit mixes, snake leading/trailing/multiple underscores, digit-start runs, non-ASCII input (the load-bearing UTF-8 byte-vs-rune-equivalence cases), a real Go source snippet, and a multi-identifier stress input. All expected outputs hand-traced against the pre-refactor rune-based impl and verified on main HEAD before the refactor landed.
  - `TestTokenize_StablePoolReuse`: 100 iterations of three representative inputs, requires byte-identical output on every iteration. Catches scratch-buffer-pool corruption (one call leaking state into the next).
  - `TestTokenize_LongInputNoPanic`: deterministic 4 KiB realistic input + 10 pool-stability re-runs. Catches pool corruption at scale that the StablePoolReuse stress might miss with small inputs.

  All adversarial cases + the existing 28-case `TestTokenize_IdentifierSplitting` suite pass byte-identically on the byte-based impl — that's what makes the NDCG exact-match result mechanically guaranteed.

- **Allocations per call go DOWN, not up** (different from ADR-026's +3 trade). The `sync.Pool` scratch buffer eliminates the per-token output-buffer allocation; the lowercase fast-path eliminates allocations for the common already-lowercase case; the byte-level scan eliminates the rune-slice growth. Both alloc count AND alloc bytes drop. This is the opposite tradeoff from ADR-026's heap refactor (which traded +3 allocs for −99% bytes); here both metrics improve together.

- **No public API changes.** `Tokenize(text string) []string` signature unchanged. The `[]string` return type stays per [ADR-008](#adr-008-bm25-tokenizer--verbatim-port-of-sembles-tokenspy-snake-case-compound-preservation)'s function-level byte-equality contract.

- **No new third-party dependencies.** Refactor uses only standard library (`strings`, `sync`). Drops two prior dependencies inside the file (`slices`, `strings.SplitSeq`/`strings.ToLower`/`strings.IndexByte`) in favor of the byte-direct equivalents.

- **Future perf work informed.** Post-v0.8.5, the remaining ADR-025 hotspots are: `bm25.Build`'s 37.6% indexing-alloc share (potentially addressable via `m[string(b)]` lookup pattern — deferred to v0.8.6+ pending a re-measurement that shows it's still worth the API churn), and the embed pipeline (`weightedMeanPoolSafe` accumulator at 23% of small hybrid CPU — gated on a pure-Go-no-cgo SIMD pattern). GC pressure should drop substantially with this refactor in tree, which will shift the relative shares.

---

## ADR-028: BM25 tokenizer `parts`-slice pooling via `tokBuffers` struct (v0.8.6)

**Status:** Accepted

**Date:** 2026-05-27

**Issue:** TBD (open a townsendmerino/ken issue for the v0.8.6 work + cross-link on merge).

**Extends:** [ADR-027](#adr-027-bm25-tokenizer-allocation-reduction--rune--byte--syncpool-scratch--lowercase-fast-path-v085) (the v0.8.5 byte-level scan + scratch pool that this builds on), [ADR-025](#adr-025-perf-campaign-phase-1-investigation-outcome--hotspot-identification-across-small--medium-workloads) (the GC-is-~50%-of-indexing-CPU finding that makes object count the right lever), [ADR-008](#adr-008-bm25-tokenizer--verbatim-port-of-sembles-tokenspy-snake-case-compound-preservation) (the verbatim-parity contract this refactor explicitly preserves).

### Context

After ADR-027 shipped, a post-v0.8.5 re-measurement of `ken perf index` at medium scale (semble bench corpus, 378,524 chunks) revealed that the allocation picture had split into two distinct shapes — by bytes vs by object count — and the GC-time lever was on the object-count side:

**By bytes (alloc_space, 17.2 GB total):** `bm25.Build` 41.8% ≈ tokenizer 42.1% — roughly tied.

**By object count (alloc_objects, 111M total) — the GC-pressure signal:**

```
bm25.camelSplitBytes   75.8M flat  68.1%   ← the target
bm25.lowerString       20.8M       18.7%
bm25.emitRun→Tokenize 102.3M cum   91.9%
bm25.Build              5.5M        4.9%
```

The tokenizer accounted for **92% of indexing allocation COUNT but only 42% of bytes**. `bm25.Build` was the byte hog (a few large structures: ~1.3 KB/object map buckets + posting slices); the tokenizer was the count hog (tens of millions of tiny `parts []string` slices, one per identifier). GC mark cost scales with object count + pointer density, not raw bytes, and [ADR-025](#adr-025-perf-campaign-phase-1-investigation-outcome--hotspot-identification-across-small--medium-workloads) measured GC at ~50% of indexing CPU. So the GC-time lever for v0.8.6 was specifically `camelSplitBytes`'s 75.8M `parts`-slice allocations.

Root cause: `camelSplitBytes` returned a fresh `parts []string` per identifier; the snake-split path in `emitRun` did the same with a local `var parts`. Those string headers were copied into the output via `append(out, parts...)`, so the `parts` backing array was dead-and-reusable the instant the append completed. The fix is to stop allocating it per-identifier.

### Decision

Extend [ADR-027](#adr-027-bm25-tokenizer-allocation-reduction--rune--byte--syncpool-scratch--lowercase-fast-path-v085)'s pooled `*[]byte` scratch into a small `*tokBuffers` struct holding **both** `scratch []byte` (for lowercase conversion) AND `parts []string` (for sub-token accumulation). One `sync.Pool` Get/Put per `Tokenize` call; the `parts` slice is reset to length 0 at the top of each `emitRun` and refilled via the snake- or camel-split path; at the bottom, contents are copied into the output via `append(out, bufs.parts...)`.

Two surface changes:

1. **`tokBuffers` struct** holds `{scratch []byte, parts []string}`. Pooled via `sync.Pool`; initial capacities cover typical identifier shape (scratch 256 bytes, parts 16 entries).
2. **`camelSplitBytes` → `camelSplitBytesInto(run string, bufs *tokBuffers)`.** Appends directly into `bufs.parts` instead of returning a fresh slice. The signature couples the camel splitter to the buffer struct, but the struct is per-call state owned by Tokenize so the coupling is local and reads naturally.

`lowerString` signature is unchanged (still takes `scratch *[]byte`); callers pass `&bufs.scratch`.

The correctness argument has three load-bearing pieces:

- **`append(out, bufs.parts...)` COPIES string headers** into out's backing array. After the append, `bufs.parts`'s backing array holds nothing `out` depends on.
- **The string DATA those headers point at is independent of `bufs`:** lowerString's fast-path returns a view into `text` (valid as long as text lives, no bufs dependency); lowerString's slow-path returns a fresh allocation copied out of `bufs.scratch` (also no bufs dependency after the conversion).
- So `bufs.parts` can hold a mix of both, and copying them into `out` then reusing `bufs.parts[:0]` for the next identifier is safe. Same reasoning that made ADR-027's scratch reuse safe, extended one level.

### Alternatives considered

- **Pre-size `parts := make([]string, 0, 8)` per call instead of pooling.** Simpler — just a constant-cap allocation per call — but still 1 alloc/identifier. Halves the per-identifier count rather than eliminating it. Rejected: the pooled-struct refactor gets to ~0 amortized; pre-sizing leaves half the win on the table for marginal code-simplicity gain.

- **Pool `parts` separately from `scratch` (two `sync.Pool` instances).** Rejected. One struct holding both keeps Tokenize to a single Get/Put pair per call; two pools doubles the per-call overhead without any compensating benefit. The two buffers are always acquired and released together by the same caller, so the natural shape is one pooled struct.

- **Attack `bm25.Build` (the byte hog) instead.** Rejected for v0.8.6. `bm25.Build` is 41.8% of indexing alloc BYTES but only 4.9% of indexing alloc OBJECTS — so it's a memory-footprint target, not a GC-time target. Heap-inuse already dropped to ~5.5 GB post-v0.8.5; reducing bm25.Build's byte share would help RSS but not GC pressure. Documented as v0.8.7+ candidate if RSS becomes the explicit goal; the natural fix shape (`m[string(b)]` lookup pattern) requires changing `Tokenize` to return `[][]byte` which is explicitly rejected by [ADR-008](#adr-008-bm25-tokenizer--verbatim-port-of-sembles-tokenspy-snake-case-compound-preservation) (the function-level byte-equality contract is on the `[]string` output).

- **Pass `parts *[]string` to `camelSplitBytesInto` instead of `*tokBuffers`** (looser coupling). Considered. The single-argument signature is marginally cleaner but exposes the buffer-of-buffers shape less clearly. Took the struct parameter because emitRun ALSO needs `bufs.scratch` (passed through to `lowerString` for case conversion), and the consistency of "all internal helpers take the struct" reads better than mixing parameter styles.

- **Change `Tokenize`'s public signature to expose buffer reuse to callers** (e.g., `Tokenize(text string, bufs *tokBuffers)`). Rejected — public API stays exactly as ADR-008 / ADR-027 documented. The internal-only pool achieves the same amortization without leaking the buffer plumbing to callers.

### Consequences

**Measured impact** (Apple M1 Pro / Go 1.26.3 / darwin/arm64 / `CGO_ENABLED=0 -trimpath -ldflags='-s -w'`):

- **`Tokenize` micro-benchmark** (`go test -bench BenchmarkTokenize -benchmem -count=10`, before = main HEAD with v0.8.5 byte-based code, after = this commit):

  ```
              │  v0.8.5 byte-based  │  v0.8.6 pooled-parts                │
              │      sec/op         │      sec/op           vs base       │
  Tokenize    │  292.7µs ± 1%       │  207.4µs ± 18%        −29.1% (p=0.000) │

              │      B/s            │      B/s              vs base       │
  Tokenize    │  85.90 MiB/s ± 1%   │  121.19 MiB/s ± 15%   +41.1% (p=0.000) │

              │      B/op           │      B/op             vs base       │
  Tokenize    │  318.9 KiB ± 0%     │  244.4 KiB ± 0%       −23.4% (p=0.000) │

              │      allocs/op      │      allocs/op        vs base       │
  Tokenize    │  5,613 ± 0%         │  1,463 ± 0%           −73.9% (p=0.000) │
  ```

  **The allocs/op −73.9% reduction is the headline v0.8.6 metric.** This is the GC-pressure lever the briefing identified — the per-identifier `parts` slice allocations are gone. The AFTER sec/op variance (±18%) is high relative to BEFORE (±1%) because the AFTER number is now small enough that timer noise dominates — `p=0.000` still holds (the diff is significant), but treat the central tendency as a range, not a point estimate.

- **End-to-end medium-corpus re-measurement** (`scripts/perf_collect.sh medium --modes=bm25,hybrid --chunkers=regex`, 378,524 chunks):

  | combo | metric | v0.8.5 | v0.8.6 | Δ |
  |---|---|---|---|---|
  | bm25-regex INDEX | objects allocated | 133.6M | **53.5M** | **−60.0%** |
  | bm25-regex INDEX | bytes allocated | 16.86 GB | 15.04 GB | −10.8% |
  | bm25-regex INDEX | heap inuse | 5.96 GB | 5.54 GB | −7.0% |
  | bm25-regex INDEX | wall time | 40.1s | 52.4s | +30.7% (variance, see below) |
  | bm25-regex SEARCH | p50 | 1.76 ms | 1.88 ms | +7% (within noise) |
  | hybrid-regex INDEX | objects allocated | 652.0M | 571.9M | −12.3% |
  | hybrid-regex INDEX | bytes allocated | 51.56 GB | 49.74 GB | −3.5% |
  | hybrid-regex INDEX | heap inuse | 5.71 GB | 5.64 GB | −1% (noise) |
  | hybrid-regex INDEX | wall time | 166.8s | 216.8s | +30% (variance, see below) |
  | hybrid-regex SEARCH | p50 | 124.07 ms | 132.73 ms | +7% (within noise) |

  **Object-count win is the load-bearing signal:** bm25-regex INDEX drops 133.6M → 53.5M objects (−60%) — the camelSplitBytes 75.8M and the snake-path `var parts` allocations together. Hybrid-regex INDEX's smaller drop (−12.3%) is expected: hybrid's allocations are dominated by the embed pipeline (which v0.8.6 doesn't touch); the tokenizer's share of hybrid's allocs is much smaller.

  **Wall-time AFTER numbers are dominated by machine-load variance on this measurement window, not a v0.8.6 regression.** Three independent observations support this:
    1. The benchstat AFTER variance was ±18% (vs ±1% BEFORE) — consistent with high system contention.
    2. The NDCG-bench wall times for v0.8.6 (bm25 5.7→7.8s, semantic 23.3→28.4s, hybrid 50.4→59.6s — all +20-25%) showed the same uniform slowdown — a single-cause signature.
    3. The object-count win (−60%) and the alloc_objects pprof confirmation (below) are machine-independent and unambiguous.

  A second measurement window with the machine quieter would likely show wall-time improvement on the order of the briefing's −15-30% projection. Documented honestly here: the object-count win is real and reproducible; the wall-time delta on this specific run isn't a clean signal.

- **`alloc_objects` pprof top — the headline confirmation** (`go tool pprof -top -sample_index=alloc_objects bench_out/medium/2026-05-27/profiles/bm25-regex.index.mem.pprof`):

  ```
                  flat  flat%   sum%        cum   cum%
    22,448,951   66.47% 66.47% 22,448,951  66.47%  bm25.lowerString (inline)
     5,229,215   15.48% 81.95%  5,229,215  15.48%  bm25.Build
     3,195,012    9.46% 91.41% 25,643,973  75.93%  bm25.emitRun
     ...
            10  3e-05%         9,033,193   26.75%  bm25.camelSplitBytesInto    ← was 75.8M / 68.1% pre-v0.8.6
  ```

  `bm25.camelSplitBytesInto` flat allocations dropped from 75.8M (68.1%) to **10 objects** — essentially eliminated. The function still appears in the cumulative column (it calls `lowerString` for slow-path lowercases) but no longer allocates its own data. Total live-alloc objects: ~33.7M (down from ~111M pre-v0.8.6 — the records.jsonl 53.5M figure is the larger `alloc_delta` across the entire index build including walk/chunk overhead).

- **NDCG@10 safety check** (semble bench corpus, 63 repos / 1251 tasks, all 3 modes, `--chunker regex` both sides):

  | mode | v0.8.5 NDCG@10 | v0.8.6 NDCG@10 | Δ |
  |---|---|---|---|
  | bm25 | 0.6237 | 0.6237 | **0.0000** (exact) |
  | semantic | 0.6469 | 0.6469 | **0.0000** (exact) |
  | hybrid | 0.8418 | 0.8418 | **0.0000** (exact) |

  Exact match in all three modes confirms token-set parity: same tokens in → same scores → same K results → same ranking → identical NDCG. The buffer-reuse refactor introduced no semantic drift, which is the load-bearing safety property `TestTokenize_AdversarialParity`'s 17 cases (including the new `within-call-parts-reuse` stress) are designed to catch.

- **Token-set parity test extension.** Added one new case to `TestTokenize_AdversarialParity`: `within-call-parts-reuse` ("aB cD_eF gHiJkL m_n_o PQRSTuv") — five consecutive identifiers of rapidly varying part counts (2/2/4/3/2). This is the specific bug shape v0.8.6 could introduce: cross-identifier contamination via the shared `parts` slice if its backing array isn't properly reset between `emitRun` calls. The existing `stress-stable-1` covers 4-identifier reuse; `TestTokenize_LongInputNoPanic` covers many-identifier reuse via the 4 KiB stress input; the new case adds a focused mid-scale variant.

- **No public API changes.** `Tokenize(text string) []string` signature unchanged. Public callers (`bm25.Build`, query-side tokenization, external consumers via the `Tokenize` symbol) see identical behavior.

- **No new third-party dependencies.** Refactor uses only standard library (`strings`, `sync`).

- **The accumulating second-machine-confirmation debt** ([`docs/internal/PERF.md`](PERF.md) acceptance threshold step 2): ADR-026, ADR-027, and now ADR-028 have all shipped with single-machine evidence (Apple M1 Pro / native arm64 darwin / Go 1.26.3). This is the THIRD perf release in the v0.8.x cycle without a second-machine confirmation, and the briefing explicitly flagged: "Don't let it slide silently a fourth time." Before any further perf release (v0.8.7+), we either set up a Linux x86_64 CI confirmation runner OR consciously decide single-machine evidence is sufficient for this class of change and amend the acceptance threshold accordingly. Token-set parity (which load-bears each of these releases) is machine-independent, so the safety check holds across architectures; the wall-time and alloc numbers do not, and that's where the debt accumulates.

- **Future perf work informed.** Post-v0.8.6, the alloc_objects picture has shifted: `bm25.lowerString` is now the dominant flat frame at 66.47% (22.4M objects — these are the slow-path string allocations for tokens containing uppercase bytes; the byte-level lowercase work is the bottleneck). `bm25.Build` is 15.48% (5.2M — map insertions, the natural next target if RSS becomes a goal). The embed pipeline is unchanged and untouched by ADR-027 or ADR-028. Any v0.8.7+ briefing should rank against this updated profile, not the post-v0.8.5 one this ADR motivated against.

---

## ADR-029: v0.8.x perf campaign capstone — allocation/GC ceiling reached; indexing is single-threaded; parallelism is the next frontier

**Status:** Accepted

**Date:** 2026-05-27

**Extends:** [ADR-025](#adr-025-perf-campaign-phase-1-investigation-outcome--hotspot-identification-across-small--medium-workloads) (the Phase 1 investigation that opened the campaign), [ADR-026](#adr-026-paired-heap-refactor-for-annflatquery--bm25indextopk-v084) / [ADR-027](#adr-027-bm25-tokenizer-allocation-reduction--rune--byte--syncpool-scratch--lowercase-fast-path-v085) / [ADR-028](#adr-028-bm25-tokenizer-parts-slice-pooling-via-tokbuffers-struct-v086) (the three rolling releases the campaign shipped).

**Seeds:** a future indexing-parallelism campaign (the "Phase 2" this ADR scopes but does not undertake).

### Context

The v0.8.x rolling perf campaign opened with ADR-025's Phase 1 investigation and shipped three releases:

- **v0.8.4 (ADR-026):** paired min-heap refactor of `ann.Flat.Query` + `bm25.Index.TopK`. Hybrid search p50 −41%, bm25 search p50 −84% at medium scale. Zero NDCG change.
- **v0.8.5 (ADR-027):** BM25 tokenizer `[]rune`→`[]byte` + scratch-buffer pooling + lowercase fast-path. Indexing alloc bytes −10%, object count −56%. Zero NDCG change.
- **v0.8.6 (ADR-028):** BM25 tokenizer `parts`-slice pooling. Indexing object count −60% cumulative (133.6M → 53.5M at medium). Zero NDCG change.

After v0.8.6, ADR-028's forward-looking section identified `bm25.lowerString` as the dominant remaining flat-object allocator (66% of `alloc_objects`) and proposed it as a possible v0.8.7. **Before briefing that, we re-profiled on a quiet machine with a CPU profile** — and the CPU profile contradicted the object-count profile.

#### The decisive measurement (post-v0.8.6, commit `b569d09`, M1 Pro / arm64 / Go 1.26.3, semble bench corpus, 378,524 chunks)

**Clean bm25-regex index CPU profile (`go tool pprof -top -cum`):**

```
52.3%  search.FromFSWithOptions  (the whole index build)
46.3%  runtime.systemstack
38.3%  runtime.gcBgMarkWorker     ← GC mark phase
37.8%  runtime.scanObject
34.0%  search.BuildIndex
25.9%  bm25.Build                 ← postings-map construction (real work)
22.3%  runtime.(*mspan).typePointersOfUnchecked  (flat — GC pointer-walking)
18.3%  search.walkAndChunkFS
16.9%  syscall.syscalln           (file reads)
```

GC is ~38% of indexing CPU *even after v0.8.6 cut object count 60%*. The `alloc_objects` profile said `lowerString` (66% of allocation count) was the target. But the CPU profile shows the GC time is spent in `typePointersOfUnchecked` / `scanObject` — **marking the live, pointer-dense index structures** (`bm25.Build`'s `map[string][]postingEntry` + the chunks slice), not the transient tokenizer strings. `lowerString`'s allocations are pointer-light strings that die young, before a GC cycle scans them: high allocation *count*, low GC-scan *cost*.

#### The GOGC probe — the load-bearing evidence

To decide whether the 38% GC was a wall-time bottleneck worth attacking via the postings-map representation, we ran the cheapest possible discriminating experiment — collect 4× less often and see if wall time moves:

| Run | `GOGC` | index wall | heap inuse |
| --- | --- | --- | --- |
| default | 100 | 37.4 s | 5.42 GB |
| probe | 400 | 36.5 s | 9.62 GB |

**Collecting 4× less often moved wall time ~2% (within noise) while ballooning heap 77%.** The 38% GC CPU is not on the wall-clock critical path — it runs on `gcBgMarkWorker` background threads concurrent with the mutator. The CPU sample ratio confirms why: **42.74 s samples / 37.73 s wall = 1.13× — indexing uses barely more than one core on an 8-core machine.** GC has 7 idle cores to run on; reducing its frequency just frees cores that were already idle. The wall-time critical path is the serial indexing pipeline, not GC.

### Decision

**Close the v0.8.x rolling allocation/GC perf campaign at its honest ceiling.** Three doors are now empirically shut:

1. **`lowerString` reduction** — wrong target. High allocation count, low GC-scan cost (pointer-light, dies young). Cutting it would barely move GC time, which is spent marking live structures.
2. **Postings-map GC-cost reduction** (the arena/slab or integer-term-ID refactor floated in ADR-028's alternatives) — the GOGC probe bounds its prize at ~2% wall time, because the postings-map GC mark work is concurrent and absorbed by idle cores. Not worth the core-indexing-refactor risk for ~2%.
3. **`GOGC` as a shipped default** — 2% wall time for +77% heap is a bad trade; rejected as a default. (It remains available to operators as a knob; not blessed.)

The allocation/GC line of attack is exhausted: ken's indexing wall time is no longer bound by allocations or GC. **It is bound by the serial pipeline running on ~1.13 of 8 cores.**

### The next frontier: indexing pipeline parallelism

The same profile that closed the allocation door opened a bigger one. Indexing is single-threaded; the seven idle cores are the largest unrealized wall-time win in the system — far larger than anything the allocation campaign delivered. This section scopes a future parallelism campaign in enough detail to start it deliberately.

#### The opportunity + expected win bounds

Indexing currently uses ~1.13 cores. The pipeline is `walk → chunk → tokenize → bm25.Build` (bm25 mode) or `walk → chunk → {tokenize→bm25.Build, embed}` (hybrid mode). If the parallelizable stages dominate and scale to N cores, Amdahl's law caps the win at `1 / (serial_fraction + parallel_fraction/N)`. With an 8-core box and a high parallel fraction, a realistic target is **3–5× indexing wall-time reduction for bm25 mode, potentially larger for hybrid mode** (where embed inference — fully parallel — is the dominant cost). These are ceilings; the serial walk, the merge step, and any serial bm25.Build portion set the floor.

#### Pipeline stage parallelizability analysis

| Stage | Cost (bm25 mode, from profile) | Parallelizable? | Notes |
| --- | --- | --- | --- |
| `repo.WalkFS` (directory traversal) | small (part of the 18% walkAndChunk) | **Serial by nature** | Produces the ordered file list. Cheap; leave serial. Order is the determinism anchor. |
| `chunk.ChunkFile` (per-file chunking) | bulk of the 18% walkAndChunk + file I/O | **Embarrassingly parallel** | Each file is independent. The treesitter chunker's `ParserPool` is already `sync.Pool`-backed for concurrent use (ADR-010); the regex chunker is stateless. This is where most idle cores go. |
| `bm25.Tokenize` (per-chunk) | part of BuildIndex | **Parallel** | Stateless per chunk (post-v0.8.5/6 it uses pooled buffers via `sync.Pool` — already concurrency-safe by construction). |
| `bm25.Build` (postings map) | 25.9% | **Hard — shared mutable state** | The `map[string][]postingEntry` is shared. Options below. |
| `embed.StaticModel.Encode` (hybrid/semantic) | dominant in hybrid mode | **Embarrassingly parallel** *(verify)* | Per-chunk, stateless inference. MUST verify `StaticModel.Encode` is goroutine-safe (the float64-accumulation invariant + read-only model tensors suggest yes, but it's unconfirmed — see Validation). |
| `ann.Flat` build | small | Trivial | Just stores the vector matrix; fill by index. |

#### The hard requirement: determinism

The BM25 index must be **byte-deterministic** across runs for two reasons: (a) NDCG reproducibility (`docs/BENCH.md`'s parity contract), and (b) the pre-built-index format (ADR-024) requires byte-stable serialization for the embedded-corpus `mcp.Run` use case. So any parallel construction must produce a result *identical* to the serial build:

- **Chunk order:** the chunks slice must be in deterministic order (lexical file order, then chunk order within file). Parallel chunking workers must tag results with their file index and a merge step must reassemble in that order before the index is built.
- **Postings order:** `df`, posting lists, and `docLen` must be identical to the serial build. Since BM25's `Build` iterates the ordered chunks slice, if the chunks slice is deterministically ordered, a serial `Build` over it is automatically deterministic — which argues for parallelizing *chunking + embed* (the embarrassingly-parallel, cost-dominant stages) while keeping `Build` serial over the ordered result, at least initially.

#### Architecture sketch (suggested phasing)

A staged approach that captures most of the win while containing the determinism risk:

**Phase A — parallelize chunking + embed, keep `bm25.Build` serial.**
- Walk produces the ordered file list (serial, cheap).
- Fan files out to a `GOMAXPROCS`-sized worker pool over a bounded channel (backpressure to cap in-flight memory). Each worker returns `(fileIndex, []chunk)`.
- A collector reassembles chunks in `fileIndex` order → the deterministic chunks slice.
- (Hybrid/semantic) embed the ordered chunks in parallel, filling the vector matrix by index.
- `bm25.Build` runs serially over the ordered chunks slice — deterministic by construction, and it's only 26% of bm25-mode CPU, so leaving it serial still unlocks the chunking+embed parallelism (the bulk).
- **Expected:** most of the parallel win, minimal determinism risk (Build unchanged; only chunking+embed parallelized, both with order-preserving merge).

**Phase B (optional, later) — parallelize `bm25.Build`.**
- Only if Phase A profiling shows serial `Build` is now the bottleneck.
- Sharded construction: each shard builds a partial postings map over a contiguous chunk range, then a deterministic merge combines them (term lists concatenated in chunk order). Higher complexity + higher determinism risk; defer until proven necessary.

#### Validation requirements (the parallelism campaign's calibration gauntlet)

This is a bigger correctness surface than the tokenizer work. The campaign must establish, before shipping:

1. **Byte-identical index parity:** build the same corpus serially and in parallel; assert the chunks slice, the BM25 postings (`df`, posting lists, `docLen`), and the embed matrix are identical. This is the load-bearing test — stronger than NDCG, machine-independent. A new `internal/search` test that builds both ways and `reflect.DeepEqual`s the resulting `*Index` internals.
2. **NDCG@10 exact-match** on the semble bench corpus, all three modes — the downstream confirmation (must be exact, given index parity).
3. **`embed.StaticModel.Encode` goroutine-safety audit:** confirm (via code read + a `-race` test) that concurrent `Encode` calls are safe. The CLAUDE.md notes `Flat.Query` is goroutine-safe by immutability; `StaticModel.Encode` must be similarly verified (read-only tensors, no shared mutable accumulator). If it's NOT safe, that's a prerequisite fix.
4. **`-race` clean** across the new concurrent paths — mandatory, not optional, for concurrency work.
5. **Determinism stress:** build the same corpus N times in parallel; assert byte-identical serialized index every time (catches order-dependent races the single-build parity test might miss).
6. **benchstat + end-to-end wall-time** on a quiet machine (and finally — see below — a *second machine*), confirming the parallel speedup is real and not a measurement artifact.

#### Why this is a campaign, not a point release

The tokenizer work was contained (one file, byte-equality contract, existing parity suite). Pipeline parallelism touches `internal/search`'s core build path, introduces concurrency (a new class of bug — races, nondeterminism), and carries the determinism requirement that's load-bearing for both NDCG and the ADR-024 pre-built-index format. It deserves the full Phase-0/1 investigation shape the allocation campaign used: a plan with written-down predictions, a measurement harness extension (the `ken perf index` harness already exists and would measure it directly), and per-phase calibration. Enter it deliberately.

### Second-machine confirmation debt — resolved by this closure

ADR-026/027/028 each shipped single-machine (M1 Pro / arm64). ADR-028 flagged the accumulating debt and the "don't let it slide a fourth time" line. **This ADR resolves it by closing the campaign:** no v0.8.7 single-machine release accrues further debt, because there is no v0.8.7. The decision is recorded: the allocation/GC campaign ends here. If the parallelism campaign is undertaken, **second-machine confirmation (Linux x86_64) is part of its validation gauntlet from the start** — item 6 above — precisely because parallel speedup is the kind of result that can be a single-machine artifact (core count, scheduler, memory bandwidth all vary). The debt doesn't carry forward; it's retired by stopping, and pre-committed for the next campaign.

### Alternatives considered

- **Ship v0.8.7 = `lowerString` reduction.** Rejected. The CPU profile proves it targets allocation count, not GC-scan cost; the GOGC probe proves GC isn't the wall-time bottleneck anyway. It would be motion without progress — exactly the trap the re-profile (and ADR-025's "measure before optimize" discipline) exists to prevent. This is the second time a CPU re-profile reversed an object-count-profile conclusion (the first: `filePathPenalty` falsifying at medium scale in ADR-025).
- **Ship the postings-map arena/slab refactor.** Rejected for v0.8.x. The GOGC probe bounds its prize at ~2% wall time (the GC cost it would reduce is concurrent/absorbed). High risk (core indexing + scoring path), bounded reward. If indexing parallelism (Phase B) ever makes `bm25.Build` the serial bottleneck, revisit then — but as part of the parallelism campaign, not standalone.
- **Default `GOGC` higher.** Rejected as a default (2% wall for +77% heap). Documented as an available operator knob for memory-rich/latency-sensitive deployments; not blessed.
- **Keep the rolling campaign going on diminishing targets.** Rejected by the calibration discipline. The campaign's value was three clean, measured wins; continuing past the point where measurement shows the remaining work won't move wall time would trade calibration credibility for activity. Knowing when to stop is the discipline working, not failing.

### Consequences

- **The v0.8.x rolling perf campaign is closed.** Three shipped releases (ADR-026/027/028), a documented ceiling, and a scoped successor. `docs/internal/PERF.md`'s "Empirical findings" gets a closing subsection cross-referencing this ADR; the "Headline numbers" section stays empty (single-machine investigation evidence never met the publication bar — and now won't, because the campaign closed before a multi-machine publication pass).
- **The parallelism campaign is seeded, not started.** This ADR is the seed; starting it is a separate decision with its own investigation plan. The trigger is explicit: the user's stated intent to pursue it "soon." When started, it begins with a Phase-0-style plan + predictions, mirroring the allocation campaign.
- **`internal/perf` harness is reusable as-is.** `ken perf index` already measures cold-index wall time, which is exactly the parallelism campaign's headline metric. No harness work needed to start measuring.
- **Second-machine debt retired.** No further single-machine releases; the next campaign pre-commits to second-machine confirmation.
- **The `bm25.Build` / postings-map representation question is parked, not closed.** It re-opens only if parallelism Phase B makes serial `Build` the bottleneck — at which point the arena/slab work has both a clear motivation and a measured prize.

### Open questions for the parallelism campaign (deliberately not resolved here)

- **Is `embed.StaticModel.Encode` goroutine-safe today?** Unverified. The first task of the parallelism campaign's investigation phase. If not, it's a prerequisite fix and changes the effort estimate.
- **What's the actual serial fraction?** The walk + merge + serial `bm25.Build` set the Amdahl floor. A quick experiment (parallelize just chunking, measure) would bound the realistic win before committing to the full architecture.
- **Worker-pool memory ceiling.** Parallel chunking holds more chunks in flight; the bounded-channel backpressure design needs a memory budget, especially for the giant-corpus scale-out case ADR-025 flagged (where serial indexing already hit 7-8 GB heap pre-campaign).
- **Does parallelism interact with the watch-mode incremental re-index path (ADR-012)?** The atomic-snapshot-swap model should be orthogonal, but the parallel builder must produce the same `*Index` shape the snapshot swap publishes. Verify during design.

---



---

## ADR-030: Indexing pipeline parallelism — Phase A (per-file workers for chunk + embed) (v0.8.7)

**Status:** Accepted

**Date:** 2026-05-27

**Issue:** TBD (cross-link when opened).

**Extends:** [ADR-029](#adr-029-v08x-perf-campaign-capstone--allocationgc-ceiling-reached-indexing-is-single-threaded-parallelism-is-the-next-frontier) (the campaign seed — Phase A is the architecture ADR-029 sketched), [ADR-024](#adr-024-pre-built-embedded-indices-for-mcprun-v083) (byte-stable serialization contract; Phase A's determinism story is what preserves it), [ADR-008](#adr-008-bm25-tokenizer--verbatim-port-of-sembles-tokenspy) (tokenizer parity carries through unchanged), [ADR-010](#adr-010-tree-sitter-via-gotreesitter-instead-of-wazerowasm) (the `ParserPool`'s `sync.Pool`-backed design is the prerequisite that makes the treesitter chunker parallel-safe by construction).

### Context

[ADR-029](#adr-029-v08x-perf-campaign-capstone--allocationgc-ceiling-reached-indexing-is-single-threaded-parallelism-is-the-next-frontier) closed the v0.8.x allocation/GC perf campaign at its honest ceiling and surfaced the next-frontier opportunity: **indexing uses ~1.13 of 8 cores at medium scale**, and the wall-time critical path is the serial pipeline, not GC. Seven idle cores are the largest unrealized wall-time win in the system.

The parallelism investigation plan (`outputs/parallelism-investigation-plan.md`) ran a Phase 1 investigation with seven written-down predictions; the findings (`outputs/parallelism-phase1-findings.md`) graded them via five cheap experiments — code-read audits + `-race` tests + an env-var-gated throwaway parallel impl + a CPU profile re-read. Headline grading:

| # | Prediction | Status |
| --- | --- | --- |
| P1 | `embed.StaticModel.Encode` goroutine-safe | **CONFIRMED** (`TestEncodeConcurrent`: 320 concurrent calls, `-race` clean, byte-identical outputs) |
| P2 | bm25 mode ≥1.5× speedup | **FALSIFIED hard** — actual 1.09× (serial fraction 91%; `bm25.Build` dominates bm25-mode wall) |
| P3 | hybrid mode ≥2× speedup | **CONFIRMED** — actual 3.05× (serial fraction 23%) |
| P4 | Byte-identical determinism preserved by file-index-ordered reassembly | **CONFIRMED** (parity test passes both modes) |
| P5 | GC share drops, CPU/wall ratio rises | **CONFIRMED stronger than predicted** (GC 38%→8%, ratio 1.13×→3.29×; corrected mental model: ratio ≈ speedup) |
| P6 | Memory-ceiling concern at giant scale | Deferred to Phase 2+ design |
| P7 | Watch-mode interaction orthogonal | **CONFIRMED** (initial build inherits via `walkAndChunkFSWithModel`) |

The Phase 1 decision was to continue the campaign and ship Phase A (parallelize chunk + embed via per-file workers; keep `bm25.Build` and migration folding serial) targeting the hybrid 3× user-default win. **bm25 mode's 1.09× is honest disclosure**: `bm25.Build` is the serial bottleneck, and Phase A leaves it serial *by design* — that's exactly what makes the determinism story trivial (serial Build over a deterministically-ordered chunks slice is byte-stable for free).

### Decision

**Phase A: per-file worker pool for chunking + embedding, file-index-ordered collection, serial `bm25.Build` + migration folding.** Default-on (no opt-in flag); the implementation lives at `internal/search.walkAndChunkFSWithModel`.

Architecture (matching [ADR-029](#adr-029-v08x-perf-campaign-capstone--allocationgc-ceiling-reached-indexing-is-single-threaded-parallelism-is-the-next-frontier)'s Phase A sketch):

1. **Walk** (`repo.WalkFS`) produces the ordered file list. Serial. Cheap.
2. **Worker pool** of `runtime.NumCPU()` workers feeds off a bounded job channel (`numWorkers * 2` capacity for backpressure). Each worker performs per-file work end-to-end:
   - `fs.ReadFile(rel)` — read bytes.
   - `chunkOneFile(...)` — chunk + SQL structural extras.
   - For each chunk, if `model != nil`: `model.Encode(chunk.Text)` — produce embedding.
   - Write the per-file result into `results[fileIndex]` (a pre-sized slice; each worker writes a distinct index, so no synchronization needed for the write itself).
3. **Collector** flattens `results[]` in file-index order into the `chunks` + `vecs` slices — deterministic by construction because `repo.WalkFS` returns files in lexical order.
4. **Migration folding** runs serially after the parallel pass over a sorted directory list — same shape as the pre-Phase-A serial impl (small fraction of total cost, and serial preserves determinism trivially).
5. **Downstream `bm25.Build` + `serializeIndex`** (callers of `walkAndChunkFSWithModel`) run serially over the ordered chunks slice — byte-stable serialization by construction.

The error path: workers continue draining the job channel after their first error (the `errCh` is buffered `numWorkers`; later errors are dropped). The build returns the first surfaced error after the `wg.Wait()` join. One root-cause error per build is all callers need.

#### Concurrency safety prerequisites (verified during Phase 1)

- **`embed.StaticModel.Encode`** is goroutine-safe by construction: all model fields are initialized at `Load()` and never mutated; per-call accumulators (`rows`, `w`, `sum`, `wsum`, `out`) are locals; `Tokenizer`'s `vocab` + `addedTokens` maps are read-only after `Load` (Go's memory model permits concurrent read-only map access). Empirically confirmed by `TestEncodeConcurrent` (`-race` clean, 320 concurrent calls produce byte-identical outputs).
- **`chunk.ChunkFile`** + **`sql.ParseFile`** are pure functions of their inputs. The registered chunkers are concurrency-safe: regex + line are stateless; treesitter's `ParserPool` is `sync.Pool`-backed by [ADR-010](#adr-010-tree-sitter-via-gotreesitter-instead-of-wazerowasm)'s design (concurrent-safe by construction).
- **`bm25.tokenizerPool`** (v0.8.6 / [ADR-028](#adr-028-bm25-tokenizer-parts-slice-pooling-via-tokbuffers-struct-v086)) is `sync.Pool`-backed — concurrency-safe.

### Alternatives considered

- **Phase B: parallelize `bm25.Build` (sharded postings + deterministic merge).** Deferred. Phase 1 found that bm25 mode's serial fraction is ~91%; Phase B would target that. But the determinism risk is higher (sharded merge has to produce byte-identical output to a serial build), bm25 mode is opt-in (`--mode=bm25`), and the hybrid 3× already covers the user-default case. **Concrete trigger to re-open Phase B**: a real user reports bm25-mode indexing latency as actionable pain, OR post-Phase-A re-measurement shows `bm25.Build` is the bottleneck on some workload pattern not represented in semble medium. Per ADR-029's "defer-with-trigger" pattern.

- **Stage-based parallel pipeline** (separate worker stages for chunker → tokenizer → embed, with channels between stages). Rejected. Per-file workers are simpler (one channel; no inter-stage backpressure to tune), match how the serial loop interleaves stages today (so the diff to the existing code is local), and Phase 1's measured 3.05× already hits the Amdahl ceiling implied by the 23% serial fraction. There's no headroom a stage-based design would unlock.

- **Make parallel opt-in via a flag.** Considered (would have been the conservative path). Rejected because: (a) the byte-identical parity is established (`TestBuildDeterminism_CrossRun` smoke + the N=20 medium stress in this ADR's calibration evidence), (b) bm25 mode isn't worse off (1.09× speedup is small but positive), and (c) opt-in features become orphaned features. Default-on means every `mcp.Run` deployment, every `ken-mcp` watch run, and every `ken build-index` invocation gets the hybrid 3× win without changing their command lines.

- **Bigger `NumCPU * K` channel buffer for parallel chunking** (e.g., K=4 or unbounded). Considered. Kept `numWorkers * 2` as the conservative default: bounded enough to prevent unbounded in-flight memory at giant-scale corpora (the P6 deferred concern), large enough that workers don't starve on the producer at medium scale. If giant-scale runs surface memory-ceiling issues, this is the first knob to revisit.

- **Per-mode parallelism strategies** (e.g., serial for bm25, parallel for hybrid). Rejected for simplicity: one code path, one set of tests, one calibration matrix. The bm25 1.09× is small but always positive; there's no case where the parallel path is worse than serial.

- **Skip `embed.StaticModel.Encode` goroutine-safety audit and just assume it's safe.** Rejected. Phase 1.1 was a 30-minute investment that produced a permanent regression test (`TestEncodeConcurrent`) and removed a class of uncertainty from the design. The right discipline for any concurrency campaign.

### Consequences

**Measured impact** (Apple M1 Pro / Go 1.26.3 / darwin/arm64 / `CGO_ENABLED=0` / semble bench corpus / 378,524 chunks):

- **End-to-end medium-corpus indexing wall time** (3-trial medians; full data in `bench_out/parallelism-phase2/`):

  | combo | metric | post-v0.8.6 (serial) | post-v0.8.7 (parallel) | Δ |
  |---|---|---|---|---|
  | bm25-regex INDEX | wall (median of 3) | 28.57 s | 24.78 s | −13% (**1.15× speedup**) |
  | hybrid-regex INDEX | wall (median of 3) | 165.32 s | 45.43 s | −73% (**3.64× speedup**) |
  | bm25-regex INDEX | objects allocated | 53 M | 53 M | ≈ flat (refactor doesn't change tokenizer) |
  | hybrid-regex INDEX | objects allocated | 572 M | 572 M | ≈ flat |
  | bm25-regex INDEX | heap inuse (peak) | 7.12 GB | 6.17 GB | −13% (parallel mutator reclaims faster) |
  | hybrid-regex INDEX | heap inuse (peak) | 6.34 GB | 6.72 GB | +6% (per-file in-flight chunks during parallel pass; well below `numWorkers*2` channel cap) |

  Search latency unchanged (Phase A only touches the indexing path).

- **CPU profile (post-Phase-A hybrid medium index, parallel)** — the headline shift:

  ```
  Duration: 53.18s, Total samples = 174.61s (328.31%)
        flat  flat%   sum%        cum   cum%
       0.04s 0.023% 0.023%    117.78s 67.45%  walkAndChunkFSWithModel.func1  (the per-file worker body)
       0.01s 0.0057% 0.029%    94.26s 53.98%  embed.(*StaticModel).Encode
       2.01s  1.15%  1.18%     67.48s 38.65%  embed.(*Tokenizer).Encode
      27.13s 15.54% 17.54%     27.13s 15.54%  syscall.rawsyscalln          (file reads parallelized)
       0.80s  0.46% 18.33%     25.55s 14.63%  runtime.mallocgc
      17.79s 10.19% 28.52%        21s 12.03%  embed.weightedMeanPoolSafe
      19.20s 11.00% 45.43%     19.20s 11.00%  runtime.memclrNoHeapPointers
       0.01s 0.0057% 45.63%    14.47s  8.29%  runtime.gcBgMarkWorker        (was 38.3% pre-parallel)
       2.56s  1.47% 47.10%     13.81s  7.91%  runtime.scanObject            (was 37.8%)
       7.41s  4.24% 65.00%      7.41s  4.24%  runtime.(*mspan).typePointersOfUnchecked  (was 22.3%)
  ```

  Key signals: `runtime.gcBgMarkWorker` cum share dropped from 38.3% (ADR-029 serial baseline) to 8.29%; `runtime.scanObject` 37.8% → 7.91%; `runtime.(*mspan).typePointersOfUnchecked` 22.3% → 4.24%. The parallel mutator runs concurrent with GC, so GC's relative CPU share shrinks ~4×. CPU samples / wall = 174.61s / 53.18s = **3.28×** — matches the measured speedup (per the corrected Amdahl mental model from `outputs/parallelism-phase1-findings.md`; the CPU/wall ratio in steady state equals the speedup, not some independent "cores used" multiplier).

- **N=20 determinism stress at semble medium scale** (LOAD-BEARING per the planning Claude's audit of the Phase 1 findings):

  Built the semble medium corpus 20 times via the post-Phase-A parallel impl + once via a pre-Phase-A serial reference (the env-var-gated throwaway binary, gate set off → serial path). All 21 serialized index files compared by SHA-256:

  | mode | SHA-256 (reference + all 20 parallel builds) | unique SHA count |
  |---|---|---|
  | bm25 | `b4f256f2a4a09c4dd5c1b8a2f6c5bf34a31d7eaf0ec9ea9a3bc848cfbcaceac1` | **1** (expected 1) |
  | hybrid | `43db9941e60c592f56505954da82aa9ff8012b71096d48f434c9a4e3e6753b8c` | **1** (expected 1) |

  All N=21 builds per mode produced byte-identical files (`shasum -a 256` across `bm25-{reference,parallel-1..20}.bin` + same for hybrid: every line ends with the same hash). This is the strongest possible determinism check — same input → same output across 21 trials at the corpus size where contention is real (~378k chunks, 8 workers racing through hundreds of files). Raw data: `bench_out/parallelism-phase2/stress/{bm25,hybrid}-shas.txt`.

- **NDCG@10 safety check** (semble bench corpus, 63 repos / 1,251 tasks, regex chunker both sides):

  | mode | post-v0.8.6 (serial) NDCG@10 | post-v0.8.7 (parallel) NDCG@10 | Δ |
  |---|---|---|---|
  | bm25 | 0.6237 | 0.6237 | **+0.0000 (exact match)** |
  | semantic | 0.6469 | 0.6469 | **+0.0000 (exact match)** |
  | hybrid | 0.8418 | 0.8418 | **+0.0000 (exact match)** |

  Exact match required (the parity invariant: same chunks in → same scores → identical NDCG). Mechanically guaranteed given the determinism stress passes, but the explicit safety check stays in the gauntlet.

- **In-tree regression nets** added: `TestBuildDeterminism_CrossRun` (3 sub-cases: smoke-bm25 + smoke-hybrid on `testdata/repo/`, contention-bm25 on the repo root — exercises ~2.5 MB of serialized index across hundreds of files); `TestEncodeConcurrent` (`-race`-mandatory: 320 concurrent `Encode` calls, byte-identical outputs).

- **bm25 mode gets a small parallel win, not a large one** (1.09× speedup at medium scale per Phase 1.2). This is honest disclosure: `bm25.Build` is the dominant serial bottleneck for bm25-only indexing, and Phase A leaves it serial by design. Phase B (sharded parallel `bm25.Build`) is a possible future release with a concrete re-open trigger documented above.

- **One Phase 1 finding left unresolved (deliberately):** the 91% serial fraction in bm25 mode wasn't disentangled into "bm25.Build pure time" vs "allocator contention at parallel-chunking concurrency." Irrelevant for Phase A (Build stays serial either way), but **a future Phase B design should start by resolving this** — the Phase B architecture depends on whether the 91% is pure Build-time (sharded merge will help proportionally) or partly contention (sharded merge would help less). Flagging here so the Phase B briefing doesn't re-assume.

- **Watch-mode (`ADR-012`) inherits parallel-by-default for free.** The initial bulk build in `internal/search/watch.go:156` invokes `walkAndChunk` → `walkAndChunkFSWithModel` — the same canonical entry point Phase A patches. The incremental per-file rebuild path (lines 441/492/622) is unchanged + already small-batch (debounce absorbs the latency); no Phase 2 changes needed there.

- **Pre-built indices (`ADR-024`) byte-stability holds.** `serializeIndex`'s output is byte-identical between the serial reference and the N=20 parallel builds. SDK authors using `ken build-index` for `mcp.Run` get the parallel speedup at build time + the same on-disk file shape. No format-version bump.

- **Public API unchanged.** `walkAndChunkFSWithModel`'s signature is the same; callers (`FromFSWithModel`, `BuildAndSerializeIndex`, `NewWatchedIndex`) see no change. The env-var gate (`KEN_PARALLEL_INDEX`) used during Phase 1 was throwaway and is removed in this commit.

- **No new third-party dependencies.** `sync`, `runtime` (already standard library; not previously imported by `internal/search/index.go`).

- **Accumulating second-machine debt: deferred to user-action follow-up.** [ADR-029](#adr-029-v08x-perf-campaign-capstone--allocationgc-ceiling-reached-indexing-is-single-threaded-parallelism-is-the-next-frontier) pre-committed second-machine confirmation for the parallelism campaign. The calibration in this ADR was executed on Apple M1 Pro / darwin/arm64 only. Token-set parity is machine-independent (the determinism contract — same chunks slice → byte-identical serialized index → exact NDCG — holds across architectures by construction). The wall-time speedup figure may shift by ±20% on Linux x86_64 depending on cache hierarchy + memory bandwidth + scheduler choices; that's within the noise envelope ADR-028's calibration variance already documented. Second-machine confirmation is a user-action on a Linux x86_64 box before the next release tag; same harness, same corpus, same model dir.

- **Memory ceiling for giant-scale workloads (P6) remains an open question.** The bounded `numWorkers * 2` channel buffer caps in-flight memory at any moment, but the per-file work item (chunks + embeddings) is large for big files. At semble medium scale, hybrid peak heap inuse moved from 6.34 GB (serial) to 6.72 GB (parallel) — a +6% increase, well below the channel cap implied ceiling. Giant-scale (chromium-class, ~500k files) is documented future work per [ADR-025](#adr-025-perf-campaign-phase-1-investigation-outcome--hotspot-identification-across-small--medium-workloads); re-measurement on the same workload will tell us whether parallel-chunking-induced peak RSS becomes a deployment concern there.

### Post-Phase-A picture & what re-opens Phase B

After Phase A, the indexing wall-time profile shifts:

- **hybrid mode**: the embed-dominated path is now ~3× faster. The bm25.Build portion (still serial) is a larger fraction of remaining wall time but absolute time is unchanged. Anyone running ken-mcp with `mcp.Run` against an embedded corpus gets the win automatically on next rebuild.
- **bm25 mode**: barely moves (1.09×). Serial `bm25.Build` is now overwhelmingly the bottleneck.
- **semantic mode**: not measured in Phase 1, but architecturally similar to hybrid (embed dominates) — expect a similar large speedup. Phase A's calibration measured semantic mode for the NDCG check; the wall-time win is implied but not separately published.

**Phase B re-opens** if and only if:
- A user reports bm25-mode indexing latency as actionable pain on a real workload, OR
- A post-Phase-A re-measurement shows `bm25.Build` is the bottleneck on a workload pattern semble medium doesn't represent.

Without one of those triggers, the parallelism campaign closes here. Phase A is the publication; Phase B is documented future work.


---

## ADR-031: MySQL introspection sample-loop parallelism; Postgres deferred-with-trigger (v0.8.8)

**Status:** Accepted

**Date:** 2026-05-27

**Issue:** TBD (cross-link when opened).

**Extends:** [ADR-019](#adr-019-mysql-engine--schema-filtering-for-multi-schema-dev-databases) (the MySQL introspection engine — Phase A2 parallelizes its hottest loop), [ADR-021](#adr-021-mariadb-first-class-engine-support-v081-part-b) (MariaDB shares the same introspection path), [ADR-017](#adr-017-database-schema-indexing--two-tier-static-sql--live-postgres-with-documented-pii-stance) (Postgres introspection — the deferred Phase A2 target), [ADR-030](#adr-030-indexing-pipeline-parallelism--phase-a-per-file-workers-for-chunk--embed-v087) (the indexing-pipeline analogue; same predict-measure-decide discipline).

### Context

A second-opinion review of `internal/db/mysql.go` flagged `mysqlAppendSamples` as a candidate for `errgroup` parallelism: `*sql.DB` is goroutine-safe by design, each table's sample fetch is independent, and the per-table writes target distinct `&snap.tables[i]` slots so no synchronization is needed.

Initial skepticism (worth recording, because it was wrong): the sample loop runs once per reindex, the SQL is `LIMIT 5` over indexed PKs, and intuition said it would be a small fraction of total introspection wall time. The v0.8.x perf-campaign discipline (instrument-before-optimizing, see [ADR-029](#adr-029-v08x-perf-campaign-capstone--allocationgc-ceiling-reached-indexing-is-single-threaded-parallelism-is-the-next-frontier)) said: measure before deciding.

A new `//go:build dbperf`-gated harness (`internal/db/mysql_perf_test.go`, mirrored by `postgres_perf_test.go`) was added with a 50-tables × 100-rows synthetic fixture per engine. Sequential baselines on Apple M1 Pro / Go 1.26.3 / Docker localhost MySQL 8 + Postgres 16:

| metric | MySQL | Postgres |
|---|---|---|
| sample-loop wall (3-trial median) | 43.7 ms | 22.7 ms |
| full introspection wall | 76.6 ms | 90.9 ms |
| **sample-loop fraction of total** | **57.0%** | **25.0%** |
| Amdahl ceiling (8 workers) | **2.0×** | 1.28× |

**MySQL falsified the initial skepticism hard** — the sample loop is the single biggest contributor to introspection wall time on localhost, and the per-table latency-bound shape (median 813 µs, dominated by query round-trip + Go-side decode) is exactly what `errgroup` fans out cleanly.

**Postgres was borderline** on localhost (25%, ceiling 1.28×). Phase A2 needed to account for the engine asymmetry rather than assume symmetric architecture would yield symmetric wins.

### Decision

**Parallelize `mysqlAppendSamples` via `errgroup.SetLimit(sampleWorkers())`. Defer Postgres parallelism with an explicit re-open trigger.**

#### MySQL: parallelize

`mysqlAppendSamples` (`internal/db/mysql.go`) wraps each table's sample fetch in `g.Go(func() error { ...; return nil })` over an `errgroup.WithContext`. `sampleWorkers()` returns `min(8, runtime.NumCPU())` — bounded so the parallel pass never exceeds shared dev/staging MySQL `max_connections` (commonly 50–150). The `*sql.DB` opened in `indexSchemaMySQL` is capped with `SetMaxOpenConns(sampleWorkers())` so the pool size matches the worker count and there's no race between "burst of 8 goroutines" and "MySQL connection limit."

Workers return `nil` unconditionally — per-table failures keep the existing `warn+continue` semantics. Returning `err` would cause `errgroup.WithContext` to cancel siblings on the first failure, which is the wrong shape for a best-effort sample pass.

Output ordering is preserved because per-table results are written to `&snap.tables[i]` (a pre-existing distinct slot per table); the `snap.tables` slice itself is built once before the parallel pass.

#### Postgres: deferred-with-trigger

`sampleRowsImpl` (`internal/db/sample.go`) stays sequential in v0.8.8. The architectural obstacle is real: `*pgx.Conn` is NOT goroutine-safe (the pgx contract is one-goroutine-per-conn), so the Postgres path can't fan out across the existing connection. The investigated alternative — a scoped `pgxpool.Pool` opened just for the sample pass — was implemented and measured:

| metric | Postgres sequential (baseline) | Postgres scoped-pgxpool (parallel) |
|---|---|---|
| sampleRowsImpl wall (3-trial median) | 22.7 ms | 21.7 ms |
| full introspection wall | 90.9 ms | 94.7 ms |

**Localhost measurement: net wash.** Pool setup overhead (open 8 connections, warmup, teardown) approximately equals the parallelism gain on the 50-table × 100-row fixture. The architectural shape works (pool teardown is clean, no race conditions, integration tests pass `-race`), but the *measured benefit on localhost is zero*.

The load-bearing case for Postgres parallelism is **remote DBs** where per-query latency is RTT-bound (5–50 ms per `SELECT`). Synthetic estimate: at 30 ms RTT × 50 tables = 1.5 s sequential vs ~200 ms parallel = 7–8× speedup, and the sample-loop fraction shoots from 25% to ~70% of total introspection. **But we don't have a remote-DB measurement to publish.**

Per the v0.8.x discipline ("measured wins only"), Postgres parallelism stays out of v0.8.8. The harness lives in `internal/db/postgres_perf_test.go` as the regression baseline; the scoped-pgxpool implementation was reverted (kept in git history at the v0.8.8-db-parallelism branch's pre-revert state if a future reader wants to consult it).

**Phase A2-Postgres re-opens** when:
- A real user reports Postgres introspection latency as actionable pain on a remote DB (managed RDS / Aurora / etc.), OR
- A remote-DB run of `postgres_perf_test.go` (with `KEN_DB_TEST_DSN` pointed at a real-network endpoint) shows the sample-loop fraction climbs past ~50% and parallel impl wins ≥1.5× on full introspection wall time.

### Alternatives considered

- **Parallelize Postgres anyway with localhost-caveat disclosure.** Rejected. The v0.8.x discipline is "measured wins only" — shipping unmeasured architecture is exactly the pattern ADR-029 closed out. Localhost is where most CI / dev / first-touch users will exercise the introspection; a measured wash there is not a release-worthy upgrade.
- **Migrate the whole Postgres introspection to `pgxpool.Pool` wholesale.** Rejected as scope-creep for v0.8.8. The original ADR-017 chose `*pgx.Conn` deliberately (introspection is one-shot per reindex; pooling adds complexity for no benefit). Reversing that decision needs its own ADR with its own measurement — concurrent metadata queries, connection lifetime under listen.go's separate LISTEN connection, etc. — not a side effect of a sample-loop parallelism release.
- **Synthetic latency injection in the harness (sleep N ms per query) to "prove" the remote case.** Rejected. Synthetic latency isn't network latency — TCP backoff, MySQL/Postgres-side query pipelining, kernel scheduling under real load all behave differently from `time.Sleep`. A synthetic-but-meaningful measurement is still a single-machine experiment; it would land Postgres parallelism on artifice, not evidence.
- **Bigger fan-out for MySQL (16 or 32 workers).** Rejected. `min(8, NumCPU())` matches the indexing-pipeline ADR-030 pattern; the 8-cap protects shared MySQL servers; the measured 1.85× speedup is already 92% of the 2.0× Amdahl ceiling — diminishing returns past 8.
- **`errgroup` without `SetLimit`.** Rejected. Without the limit, a 200-table schema would spawn 200 goroutines and 200 connection requests simultaneously, blowing past MySQL's `max_connections` on shared servers. The `SetLimit + SetMaxOpenConns` pair is two belts (errgroup-side + pool-side) for one suspender.
- **Drop the pre-walk approxRowCount attach step from the parallel section.** Kept inside the parallel guard for clarity; technically the attach pass touches `t.approxRowCount` before the parallel pass begins, so there's no race with workers (which only write `t.sampleColumns` + `t.sampleRows`).

### Consequences

**Measured impact** (Apple M1 Pro / Go 1.26.3 / darwin/arm64 / Docker localhost MySQL 8 / 50 tables × 100 rows / 3-trial medians):

| metric | post-v0.8.7 (sequential) | post-v0.8.8 (parallel) | Δ |
|---|---|---|---|
| **mysqlAppendSamples wall** | 43.7 ms | **19.2 ms** | **−56% (2.28× speedup)** |
| **MySQL full introspection wall** | 76.6 ms | **41.4 ms** | **−46% (1.85× speedup)** |
| sample-loop fraction of total | 57.0% | 46.3% | shrunk proportionally |

1.85× on full MySQL introspection is **92% of the 2.0× Amdahl ceiling** — minimal scheduling/coordination overhead, the parallel impl cashes nearly all of the theoretical max.

**Postgres unchanged** in v0.8.8: sampleRowsImpl 22.7 ms / full introspection 90.9 ms / sample-loop fraction 25.0%. Re-opens on the trigger above.

- **Output ordering preserved.** `TestMySQLIntegration_RowSamplingDeterministic` (the load-bearing parity test for sample-row determinism) passes `-race` clean post-parallel. Per-table writes target distinct `&snap.tables[i]` slots; the `snap.tables` slice itself is built once before the parallel pass and never mutated by workers.
- **`-race` clean across the full integration suite.** `go test -tags=dbintegration -race -run TestMySQLIntegration ./internal/db/` passes all 5 subtests.
- **Pool cap protects shared servers.** `*sql.DB.SetMaxOpenConns(sampleWorkers())` bounds the pool to ≤8 connections even on a 16-core box. The errgroup limit + the pool cap together guarantee no shared-MySQL connection exhaustion regardless of how many goroutines an upstream caller spawns.
- **No new third-party dependencies.** `runtime` (stdlib), `golang.org/x/sync/errgroup` (already in go.mod).
- **Public API unchanged.** `mysqlAppendSamples` is package-private; `IndexSchema` signature is unchanged; ken-mcp and `mcp/db` see no surface change.
- **Harnesses live in tree as regression baselines.** Both `mysql_perf_test.go` and `postgres_perf_test.go` are gated by `//go:build dbperf` so they don't run in normal `go test ./...`. The Postgres harness exists deliberately even though Postgres isn't parallelized — it's the proof of work for the "deferred-with-trigger" decision and the harness that re-opens Phase A2-Postgres when remote-DB users surface latency pain.
- **Honest disclosure on engine asymmetry.** v0.8.8 is the first DB-introspection release that has measurably different speedup across engines. MySQL gets a clean 1.85× win; Postgres + SQLite are unchanged. Users running ken-mcp against the Tier-2 DB path will see the gain only when DSN points at MySQL/MariaDB.
- **The "fast-path optimization" precursor was rejected.** Before this work, a second-opinion review proposed adding `if !strings.Contains(s, "(")` short-circuit to `normalizeMySQLIntType`. Correctness-preserving but the savings were below measurement noise (~0.2% of introspection wall — 150 calls × ~1 µs each = ~150 µs saved out of 77 ms). Rejecting it was the same discipline as accepting the Postgres deferral: don't ship what isn't measurably better.

### What re-opens Phase B (parallel introspection metadata)

After Phase A2, MySQL introspection wall is ~41 ms at medium scale. The remaining 22 ms is dominated by serial information_schema queries (constraints, indexes, FK references, views, routines). Each of these is a single big metadata query that has nowhere to fan out — they're already optimal in shape. Phase B would target running them concurrently against the same `*sql.DB`, saving perhaps another 10–15 ms.

Phase B re-opens if:
- A real user reports MySQL introspection on a giant-schema deployment (thousands of tables, complex constraints) shows the metadata queries as the new bottleneck.
- The harness's full-introspection median climbs past 100 ms on a representative workload — meaning per-table parallelism has shipped its win and the next layer is worth investigating.

Without one of those triggers, the parallelism work closes at v0.8.8.


---

## ADR-032: Promote the chunk package to public (chunk/ + chunkers); Chunker interface is the 1.0 boundary

**Status:** Accepted

**Date:** 2026-05-28

**Issue:** Closes [#36](https://github.com/townsendmerino/ken/issues/36).

**Extends:** [ADR-005](#adr-005-chunker-interface-with-three-pluggable-options-ship-c-first) (the Chunker interface this promotes to public), [ADR-010](#adr-010-tree-sitter-via-gotreesitter-instead-of-wazerowasm) (the gotreesitter-backed treesitter chunker, whose pre-1.0 dep this ADR keeps behind a best-effort tier), [ADR-016](#adr-016-embedded-corpus-mcp-build-pattern-via-mcprun-library-function) + [ADR-024](#adr-024-pre-built-embedded-indices-for-mcprun-v083) (the `mcp.Run` embedded-corpus pattern this unblocks for external authors).

### Context

The OSS-demo binaries (`demos/v0.1.0`) surfaced a real gap by dogfooding: `mcp.Run`'s `Options.ChunkerName` documents `"treesitter"` as a choice "requires importing internal/chunk/treesitter for side-effect registration" — but that package lived at `internal/chunk/treesitter`, and **Go forbids importing `internal/` packages across module boundaries**. So any *external* `mcp.Run` author wanting treesitter (or any non-default chunker) was structurally blocked. The postgres demo only worked because it lives inside the ken module (`ken/demos/postgres`); an external SDK author following the documented pattern would hit the wall. Filed as #36.

The fix the issue recommended: move the chunker to a non-`internal` path. A subtler middle path was also considered (make only the registration mechanism public, keep the chunkers internal — authors bring their own chunker). We chose the fuller move.

### Decision

**Promote the entire `internal/chunk` subtree to a public top-level `chunk/` package** (matching the existing public `mcp/` package; ken doesn't use `pkg/`):

- `internal/chunk` → `chunk` (the `Chunker` interface, `Register`/`Get`/`Names`, `ChunkFile`, the `Chunk` struct, `Language`, `DefaultChunkSize`)
- `internal/chunk/{regex,treesitter,markdown}` → `chunk/{regex,treesitter,markdown}`

The `chunk` base package is a clean leaf (no other `internal/` dependency), so the move is self-contained — no other internal package had to come along. All ~48 importing files were updated; package names are unchanged (only import paths moved).

**Two stability tiers, documented in the code:**

1. **Hard, 1.0-committed:** the `chunk.Chunker` interface (`Chunk()` + `SupportedLanguages()` + `Name()`), `Register`/`Get`/`Names`, `ChunkFile`, and the `Chunk` struct's `File`/`StartLine`/`EndLine`/`Text` fields. Small and dependency-free on purpose — this is the swap-out boundary ADR-010 designed for. External authors implement or register against this.
2. **Best-effort:** the concrete chunkers behind the interface — especially `chunk/treesitter`, backed by the pre-1.0, single-maintainer `gotreesitter` dep (bus-factor 1) and the 1-second per-parse timeout. Their exact chunk boundaries may shift across versions (chunk counts wobble ~0.1% under load — [#35](https://github.com/townsendmerino/ken/issues/35)). The promise is "valid, contiguous, byte-faithful chunks," not byte-for-byte boundary stability.

This split is the whole point of choosing the fuller move while bounding the commitment: external authors get ken's actual treesitter chunker (the demo-proven value), and ken's hard 1.0 promise stays on the tiny dep-free interface, not on gotreesitter's behavior.

### Alternatives considered

- **Registration-mechanism-only (the middle path): make `chunk` public but keep the chunkers internal.** Rejected as near-theater for #36's actual case. To get treesitter, an external author would have to reimplement ken's `chunker.go` + `cast.go` + the gotreesitter dep + the fallback logic just to register it — nobody does that. It would help only authors who already have a fully custom chunker (rare), without delivering the value #36 is about (getting *ken's* treesitter). Note both this path and the chosen one require promoting `chunk` itself, because `treesitter` imports `chunk`; the only delta is whether the concrete chunkers come too.
- **`pkg/chunk/...`.** Rejected for consistency: ken already exposes its one public package at the top level (`mcp/`), and uses `internal/` for everything private. A top-level `chunk/` matches; `pkg/` would be a lone exception.
- **Keep it internal; promise treesitter stability too (full hard-commit A).** Rejected. Promising byte-for-byte treesitter boundaries across 1.0 would lock ken to gotreesitter's exact output forever — exactly the coupling ADR-010 firewalled. The interface-only hard tier keeps the swap-out path open.

### Consequences

- **External `mcp.Run` authors can now use treesitter (and any chunker).** Blank-import `github.com/townsendmerino/ken/chunk/treesitter` (or implement `chunk.Chunker` + `chunk.Register`) before calling `mcp.Run`. The documented embedded-corpus pattern (ADR-016/ADR-024) is finally true for out-of-module authors. Closes #36.
- **The demos no longer *need* to be in-tree.** `ken/demos/{kubernetes,postgres}` stay in-tree for a single paired launch + shared build tooling, not out of necessity. They could move to separate repos later (the campaign's original Path-A preference) with no code change beyond the module boundary.
- **One leaky field in the public surface:** `Chunk.Tombstoned` is an internal incremental-indexing detail (the watch path marks chunks tombstoned in-place before compaction) that now rides in the public struct. Documented as "external Chunker implementations leave it false." Living with it beats splitting `Chunk` into public/internal variants.
- **No behavior change, no API removal.** Pure package relocation + doc tiers. `go build ./...` / `go vet` / `gofmt` clean; full `go test ./...` (incl. `-race` on the chunk + search packages) green after the move. Package names unchanged; only import paths moved.
- **Historical references in earlier ADRs/docs that say `internal/chunk` describe the pre-ADR-032 layout** and are left as point-in-time records; CLAUDE.md (the live guide) was updated to `chunk`.


---

## ADR-033: Adopt gotreesitter grammar_subset; slim release binaries (v0.20.0-rc2)

**Status:** Accepted

**Date:** 2026-05-28

**Resolves:** [ADR-023](#adr-023-gotreesitter-grammar_subset-machinery--binary-size-reduction-outcome-v082-investigation-outcome) (the v0.8.2 investigation that named the exact upstream change needed). **Extends:** [ADR-010](#adr-010-tree-sitter-via-gotreesitter-instead-of-wazerowasm), [ADR-016](#adr-016-embedded-corpus-mcp-build-pattern-via-mcprun-library-function), [ADR-032](#adr-032-promote-the-chunk-package-to-public-chunk--chunkers-chunker-interface-is-the-10-boundary).

### Context

ADR-023 (v0.8.2) audited gotreesitter v0.18.0 and reached a precise conclusion: the `grammar_subset` build-tag machinery worked at the *registration* layer but **not the embed layer** — the embedded blobs were one monolithic `//go:embed grammar_blobs/*.bin` glob, and Go's linker can't dead-code-eliminate `embed.FS` payloads, so no tag combination shrank the binary. ADR-023 named the upstream change that would unblock ken and left it as a deferred-with-trigger.

That change landed as upstream PR #92 (issue #88), which gates the all-grammars wildcard off under `grammar_subset` and adds per-language `embed.FS` blobs selected by `grammar_subset_<lang>` tags. The upstream author left #88 open to get ken to confirm the API fits the `mcp.Run` use case.

A throwaway measurement spike (2026-05-28) confirmed everything before committing:

- **API fits, zero source changes** — `go build ./...` green on the new version.
- **The feature is in a tag**, not just `main`: `go get @<#92 commit>` resolves to **`v0.20.0-rc2`** (it carries `blob_source_subset_embedded.go` + 206 per-language embed files).
- **Measured size deltas** (M1 Pro, `CGO_ENABLED=0`): `ken-mcp` 52.3 → 38.3 MB (−14.0 MB), `ken` 36.0 → 22.0 MB (−14.0 MB); the C-only `ken-demo-postgres` 265.9 → 243.8 MB binary (tarball 163 → 147 MB).
- **No silent fallback** — on a mixed corpus the slim build (17 langs) produced byte-identical treesitter chunks to the fat build (`total=5, fallback=0` both); a C-only control correctly line-fell-back on the other languages (`unsupported_lang=5`), proving the tags gate cleanly and never crash.

### Decision

**Bump to `v0.20.0-rc2` and make ken's own release binaries slim** (embed only the 17 grammars `chunk/treesitter`'s `kenToTreeSitter` dispatches), while the library `go build` / `mcp.Run` stays all-grammars.

- **`.goreleaser.yml` is the single source of truth** for the subset: the `ken` and `ken-mcp` builds carry the 18-token `tags:` list (`grammar_subset` master gate + 17 `grammar_subset_<lang>`). `scripts/subset-tags.sh` derives the slim local/CI build from that file; no second copy.
- **Drift guard** (`chunk/treesitter`'s `TestSubsetTagsMatchKenToTreeSitter`, runs in the default build): asserts the goreleaser tag set equals `kenToTreeSitter`'s values in both directions (missing tag → silent fallback; extra tag → wasted bytes), and `TestKenToTreeSitterGrammarsResolve` asserts every mapped grammar still exists in the pinned dep (catches an upstream rename/drop). A CI compile-smoke builds the slim binaries on every PR.
- **csharp / shell stay omitted** (DESIGN.md §10 grammar-quality reasons) — they're simply not embedded under subset, which is the desired behavior.
- **`go.mod` pins `v0.20.0-rc2`** — an rc, accepted deliberately (see below); also a parser-correctness improvement over v0.19.1.

### Alternatives considered

- **Wait for stable v0.20.0 before adopting.** Rejected for now: the rc carries the feature + the API-fit confirmation #88 asked for, the spike verified it fully, and the win (slimmer binaries + smaller demo downloads) is wanted now. **Trade-off accepted:** pinning an rc bends ADR-010's "pin at major.minor" rule. Mitigation: bump to stable `v0.20.0` when it cuts (tracked); the rc is functionally identical for ken's use.
- **Pin a raw `main` pseudo-version.** Unnecessary — `v0.20.0-rc2` is a real tag containing #92, strictly cleaner than a pseudo-version.
- **Add slim as parallel artifacts, keep fat as the release default.** Rejected: ken only ever dispatches the 17 mapped languages, so shipping all 206 in the *release binaries* is pure waste. Slim-default for ken's releases + fat for the library is the right split.
- **Keep the orphaned `internal/parser/grammars_subset.go` scaffolding.** Deleted (it was untracked local cruft from the v0.8.2 spike, imported nowhere); `chunk/treesitter`'s `SupportedLanguages()` ← `kenToTreeSitter` is the single source of truth.

### Consequences

- **ken's release binaries shrink ~14 MB each** with no behavior change for the 17 dispatched languages; the demo binaries can be rebuilt slim (C-only postgres saves ~16 MB of download).
- **Library consumers are unaffected** — build tags are set by whoever compiles, not by importers; an `mcp.Run` author who `go build`s without the tags gets all 206 grammars.
- **The #88 collaboration thread closes** with ken's confirmation + the measured numbers.
- **DESIGN.md §2/§10's "the embed bundle is monolithic at the source layer" statements are now historical** (annotated to point here); the embed layer is selectable as of this ADR.
- **rc-pin debt:** revisit when stable v0.20.0 ships.


---

## ADR-034: Extract reusable algorithm packages into a separate `aikit` module

**Status:** Accepted

**Date:** 2026-05-30

**Extends:** [ADR-032](#adr-032-promote-the-chunk-package-to-public-chunk--chunkers-chunker-interface-is-the-10-boundary) (which already promoted `chunk` to a public 1.0-stable surface).

### Context

Every reusable algorithm package in ken lives under `internal/`, which Go's compiler forbids any other module from importing. The packages that could legitimately be shared with other projects — generic top-K selector, flat brute-force ANN, BM25 lexical index, Model2Vec inference, CodeRankEmbed transformer encoder, language-aware code chunkers — were trapped behind the `internal/` boundary.

ADR-032 promoted `chunk` to a public path (`github.com/townsendmerino/ken/chunk`) but that's still inside the ken module. A second project wanting to reuse `embed` or `bm25` still couldn't.

### Decision

**Extract `topk`, `ann`, `bm25`, `embed`, `coderank`, and `chunk` (+ subpackages) into a new module `github.com/townsendmerino/aikit`. ken consumes it as a dependency.**

**End state:**

- `aikit` lives at [`github.com/townsendmerino/aikit`](https://github.com/townsendmerino/aikit), independent module.
- Eight aikit packages: `topk` · `ann` · `bm25` · `embed` · `encoder` · `chunk` (+ `regex` / `markdown` / `treesitter`).
- `coderank` renamed to `encoder` on the move — the package is a transformer encoder, not a PageRank-style ranker; the old name misled. Done at the only moment it's free (every import line was being rewritten anyway).
- ken's go.mod adds `require github.com/townsendmerino/aikit v0.1.0`. All former `internal/{topk,ann,bm25,embed,coderank}` and `chunk/*` imports point at `aikit/...`. App glue (`internal/search`, `internal/db`, `internal/sql`, `internal/repo`, `internal/usage`, `mcp/*`, `cmd/*`) is unchanged in behavior.
- `chunk/treesitter`'s drift-guard test (`TestSubsetTagsMatchKenToTreeSitter`) moved to `ken/internal/buildchecks/subset_test.go` because it's a ken release-config guard, not a chunk package concern. `kenToTreeSitter` was uppercased to `KenToTreeSitter` so the test can reach it from outside its package.
- Public-API break for `chunk` (ADR-032's promotion was only 2 days old, never tagged in a release, zero external consumers found via GitHub code search): hard-break, no shim. Path moved from `github.com/townsendmerino/ken/chunk` to `github.com/townsendmerino/aikit/chunk`.

### Alternatives considered

- **In-repo `kit/` curation.** Cheaper but doesn't actually solve the problem — packages still can't be imported by another module.
- **Multi-module monorepo** (second `go.mod` under `ken/aikit/`). Avoids the second repo but drags ken's history + deps into every consumer of `aikit`, and makes the "reusable by another project" narrative awkward.
- **Status quo + manual cp/paste for the next project.** Highest cost long-term; rejected.
- **Skip the `coderank → encoder` rename.** Adversarial review noted the rename costs ~50 string sites (panic prefixes, scripts, testdata path conventions, gitignore, ADR text). Did the rename anyway: the moment is free, the name is more accurate, and future readers of `aikit/encoder` aren't asked to know that the package is named for an unrelated algorithm.

### Migration mechanics (executed 2026-05-30 in one session)

Leaf-first per-stage moves under `go.work`, each stage left both modules green at every commit:

1. **Stage A:** init `aikit/` (LICENSE + NOTICE + THIRD_PARTY_LICENSES + README + .gitignore + go.mod), `go.work` at the parent.
2. **Stage B1:** atomic move of `topk` + `ann` + `bm25` + ken-side consumer rewrites (`internal/search/{hybrid,index}.go` + `bench/tokens/grep_baseline.go` + tests).
3. **Stage B2:** atomic move of `embed` + `testdata/golden.json` + `scripts/{pin_inference,parity_dump}.py` + ken consumer rewrites (`internal/coderank` still in ken, `internal/search/{watch,index,index_serialize}`, `mcp/run`, `cmd/ken/build_index`, `cmd/ken-mcp`, all relevant tests).
4. **Stage B3:** atomic move + RENAME `coderank → encoder`. Updated package decl, panic prefixes, error strings, scripts (`pin_coderank.py → pin_encoder.py`, `coderank_model.py → encoder_model.py`, `m0_ceiling.py` also moved), testdata convention (`testdata/coderank-model → testdata/encoder-model`), `testdata/coderank_golden.json → testdata/encoder_golden.json`, ken's `.gitignore` line, and the 5 ken-side selector edits (`coderank.Foo → encoder.Foo`).
5. **Stage B4:** atomic move of `chunk` + all 3 subpackages + drift-guard test relocation + `kenToTreeSitter → KenToTreeSitter` export + 53-file consumer rewrite (every `internal/{search,db,sql}`, `mcp/*`, `cmd/*`, and `demos/*` import path).
6. **Stage D:** tag `aikit v0.1.0`, push, replace ken's go.work require with `v0.1.0`, verify `GOWORK=off go build ./... && go test ./...` green against the tagged version.

### Verification

- `gofmt -l` clean in both modules.
- `go vet ./...` clean in both modules.
- `go test ./...` green in both modules. `aikit/encoder` golden cosine = **1.000000** preserved on all 18 fixtures (bit-identical to PyTorch+MPS reference). `TestForwardBatch_matchesSingle` exact. `TestMatmulBT_blockedMatchesNaive` exact on all 8 shapes.
- ADR-033's drift guard (now `ken/internal/buildchecks/subset_test.go`) still passes — `KenToTreeSitter` ↔ `.goreleaser.yml` parity.
- `mcp/binary_contract_test.go` v0.6.0 binary-size contract still satisfied (aikit imports don't trigger any forbidden DB-driver patterns).

### Consequences

- **External consumers of `chunk` would break** — but ADR-032 was never released (latest ken tag v0.8.8 still has `internal/chunk`); GitHub code search returns zero downstream consumers. The break is purely against unreleased main.
- **ken's release path now depends on aikit's:** ken cannot ship a release with `require aikit v0.0.0`; aikit must have a tagged release available before ken cuts its next tag. Bounded by Stage D — ~minutes per release-pair, not a calendar gap.
- **`go.work` stays in `.gitignore`'d state**; CI continues to build with the module-tagged require. `GOWORK=off` is the canonical release-path build.
- **Stability tiers carry over** to `aikit` per its README — hard-1.0-committed surface matches ADR-032 + the documented `aikit/embed`/`aikit/encoder` boundaries; concrete chunkers + Q8 paths + mmap variant stay best-effort.
- **License chain intact:** LICENSE + NOTICE + THIRD_PARTY_LICENSES.md copied to aikit (Model2Vec + semble + gotreesitter + x/text attributions travel with `embed`/`bm25`/`encoder`/`chunk`).
- **Pre-execution evaluation:** five-angle parallel review (build correctness, public-API impact, testdata portability, adversarial gaps, sequencing) ran before any code moved. Every reviewer returned `proceed-with-fixes`; concerns were folded into the executed plan (test files added to worklist, atomic per-Stage-B commits, drift-guard relocation, scripts also moved with their packages). Two findings stayed unfixed in this migration: dead markdown links in historical CHANGELOG entries (accepted as docs cost; the prose narrative still reads correctly), and pre-existing-broken bench fixture paths in chunk subpackages (already silently skipped today; fix folded into the move via path-depth correction).


## ADR-035: Ship Arm B structural enrichment in the production indexer (Stage 8 close)

### Context

Stage 8 Track 2 (definition / references / outline / symbols MCP tools backed by the gotreesitter-based structural index) shipped to production through commits `c783be8` → `7417578` → `bc5c04f` → `f9578f0` → `8006d48`. The structural index plus 10-language extractors are live behind the Track 2 surface.

Track 1's Arm B candidate — prepending a deterministic per-chunk label `# func: NAME | calls: A, B | raises: X\n` to every chunk before BM25 tokenization and embedding — was validated by two gates:

- **M0d csn-python-nl-stripped:** +0.0100 NDCG@10, +0.0160 R@50 (p=0.04), hybrid+rerank, N=500 against the *Python-materialized* corpus `csn-python-nl-stripped-heur/`.
- **Stage 8 Gate 1 CoSQA realism:** +0.0342 hybrid / +0.0696 semantic / +0.0412 bm25 on CoSQA dev (313 queries, casual web-search register) against the *Python-materialized* corpus `cosqa-python-heur/`.

Both validated numbers came from Python `bench_csn_nl_stripped_heur.py` / `bench_cosqa_heur.py` materializers writing prefix-prepended files to disk; the production indexer never applied the label. The Stage 8 close required wiring the Go `structural.Enrich()` path into `search.FromPath` and verifying — by **in-process re-bench** — that the production code path reproduces the validated lifts. Anything less would ship a number derived from infrastructure that isn't actually on the user's hot path.

### Decision

**Wire Arm B into `walkAndChunkFSWithModel`'s per-file worker, between `chunkOneFile` and `model.Encode`. Default-on. Disable knob `KEN_ENRICH=off` or `FSOptions.DisableEnrichment=true`.**

**End state:**

- `internal/structural/extract_file.go` exposes `ExtractFile(rel string, data []byte) *FileStruct` — per-file gotreesitter parse + extractor invocation. Same per-file work as Pass 1 of `structural.Build`, exposed for callers that need one file's data without the full-corpus walk + cross-file reverse maps.
- `internal/structural/enrich.go` adds `EnrichFromFileStruct(fs *FileStruct, opts EnrichOptions) string`, an index-free per-FileStruct enricher. `(Index).Enrich` and `EnrichFromFileStruct` both delegate to a shared `enrichCore` so the production indexer and any future bench materializer produce **byte-for-byte the same prefix from the same code**.
- The indexer worker, after producing `cs []chunk.Chunk` from `chunkOneFile`, calls `structural.ExtractFile(rel, data)`. If that returns a non-nil FileStruct (i.e. the file's extension has a registered extractor), the worker computes the label and prepends it to every chunk's `Text` before embedding. Files with no extractor pass through unchanged.
- `FSOptions.DisableEnrichment` (zero-value = enrichment on) is the Go-side opt-out; `FromFS` / `FromPath` / `FromFSWithModel` route through a new `defaultFSOptions()` that reads `KEN_ENRICH` and disables enrichment for any of `"0" / "off" / "false" / "no"` (case-insensitive). All other values (including unset and `"on"`) keep enrichment enabled.

**Default-on** is the product decision. Three reasons:

1. Net-positive on every measured corpus and mode (table below).
2. Deterministic, pure-Go, no extra model — zero new dependencies at the user.
3. The opt-out covers the corpus-pathological case where a specific extractor misbehaves; we'd rather a user explicitly disables enrichment for one corpus than make every user opt into the win.

### In-process bench numbers (the drift gate)

Both bench runs use the **raw** corpora (`csn-python-nl-stripped/` and `cosqa-python/`); enrichment is applied by the production code path inside `walkAndChunkFSWithModel`, not pre-materialized to disk. Two cells per bench: KEN_ENRICH=off (baseline) and default (enriched).

**csn-python-nl-stripped, N=500, hybrid path** (rerank cell deferred; see Caveats):

| Mode | Baseline | Enriched | Δ |
|---|---:|---:|---:|
| bm25 | 0.5987 | 0.6165 | **+0.0178** |
| semantic | 0.5967 | 0.6237 | **+0.0270** |
| hybrid | 0.6144 | 0.6352 | **+0.0208** |

Hybrid lift is ~2× M0d's validated +0.0100. The over-reproduction is consistent with Model2Vec being order-insensitive (per-token IDF-weighted average): the Go walker's call-ordering differences vs Python's `ast.walk` (see Drift section) don't degrade the embedding, and the slightly different call sets the Go walker captures appear to net positive.

**CoSQA dev, N=313, hybrid path:**

| Mode | Baseline | Enriched (in-proc) | Δ | Gate-1 validated Δ | Drift |
|---|---:|---:|---:|---:|---:|
| bm25 | 0.4724 | 0.5154 | +0.0430 | +0.0412 | +0.0018 |
| semantic | 0.6520 | 0.7216 | +0.0696 | +0.0696 | **0.0000** |
| hybrid | 0.5708 | 0.6029 | +0.0321 | +0.0342 | −0.0021 |

CoSQA in-process numbers reproduce the validated Gate-1 numbers within 0.002 across all three modes; semantic hits exactly. Gate cleared.

### Label drift between Go and Python materializers

`scripts/armb_drift_diff.go` compares the Go `EnrichFromFileStruct` label against the first line of the Python-materialized file for every file in `csn-python-nl-stripped/corpus/`:

- **Byte-exact match: 8,142 / 14,725 = 55.3%**
- **Mismatch: 6,583** — but 5,250 (80% of mismatches) are call-ORDER differences with the **same call set**. Python's `ast.walk` and gotreesitter cursor traversal both visit depth-first but in different child-order, so when ≤8 calls exist both walkers list the same names in different sequence.
- Of the genuinely-different mismatches: 1,247 differ in the actual call set (truncation hits a different 8 of N when more than 8 calls exist), 208 in func name, 26 in raises set.
- Mean label byte length: Go 74.0, Python 73.9 (essentially identical).

**Why the drift is harmless on this retrieval stack:**

- BM25 is a token bag → reordering 8 names produces an identical BM25 contribution.
- Model2Vec is a static averaging encoder → reordering doesn't affect the per-chunk embedding.
- CodeRankEmbed (the neural reranker, order-sensitive) only re-scores the top-50 from the hybrid shortlist; since hybrid surfaces the right docs into the shortlist (the +0.0208 lift), the reranker has the right candidates to work with regardless of internal order.

If we ever swap the embedding model for an order-sensitive one (e.g. a real transformer), the drift could matter. Documented as a follow-up trigger.

### Caveats

- **Rerank cell on csn-stripped was not measured.** TestCoIR_CSNPython with KEN_RERANK=1 on N=500 hit the 1h test timeout mid-rerank-forward-pass; 500 queries × top-50 forward passes on the f32 CodeRankEmbed model exceed the budget on local hardware. The hybrid cell (which produces the shortlist the reranker re-scores) shows the drift gate passing cleanly, so the rerank cell is expected to follow but is not directly measured. A larger-timeout sweep or a Q8 run would close that loop.
- **The 4 unparseable-after-docstring-strip files in `cosqa_to_bench.py`** carry no Go label (ExtractFile silently returns nil; chunk passes through unmodified). Same behavior the Python materializer produced. Match-rate: 0 of those files contribute to drift.

### Alternatives considered

- **Keep enrichment off by default; require explicit `KEN_ENRICH=on`.** Rejected. Every measured corpus + mode is net-positive. Making users opt into a free, deterministic, no-new-dep win means most users won't get it. The opt-out covers corpus-specific exceptions.
- **Pre-materialize the enriched corpus to disk (mirror the Python bench shape).** Rejected. Adds a disk write step on every index build, doubles storage for a corpus, and creates two paths (raw + materialized) where one path with in-process enrichment suffices.
- **Make `Enrich()` use a callback for cross-file Callers lookup so the production indexer can call the index-free path AND a future "rich enrichment with callers" path uses the same code.** Implemented. `(Index).Enrich` passes its callersOf closure into `enrichCore`; `EnrichFromFileStruct` passes nil. The path that ships is the index-free one — M0e proved callers/imports/signature/siblings all hurt or are neutral; they remain available behind opts but are not on the ship path.
- **Delete the now-redundant Python materializers (`bench_csn_nl_stripped_heur.py` and `bench_cosqa_heur.py`).** Deferred. They produced the original validated numbers; keeping them as a known-good reference for future drift checks is cheap. Noted in road-to-1.0 nits.

### Consequences

- **Chunker byte-fidelity invariant is intentionally broken at the indexer layer** for enriched chunks. The chunker itself still satisfies the invariant on raw source; the indexer's deliberate post-chunker `Text = label + Text` is what an indexer is for. Search-result display now includes the label as the first line of the chunk text — same as M0d's materialized corpus did, so the user-facing presentation matches the validated baseline.
- **Indexing wall-time cost: ~25ms per file on this corpus** (regex + treesitter mix; absolute number scales with file count). Cheap enough to default-on without an indexing-perf regression note in CLAUDE.md.
- **Single source of truth for Arm B label**: any future bench / materializer that wants the same prefix MUST call `structural.EnrichFromFileStruct` or `(Index).Enrich`. Other paths are forbidden by convention; the diff script catches drift if anyone tries.
- **Closes Stage 8.** Together with the already-shipped Track 2 tools and 10-language extractors, the Stage 8 plan is now production. road-to-1.0.md tracker retrieval section can be marked closed.


## ADR-036: Close the startup + query-latency perf campaign

### Context

[`docs/internal/perf-campaign-startup-query.md`](perf-campaign-startup-query.md) kicked off a focused perf campaign targeting two surfaces the v0.8.x indexing campaign (project-perf-phase0, ADRs 026-029) didn't touch: ken-mcp cold-start time and per-query latency. Profile-driven; no Mn optimization until M0 data ranked it.

### Decision

**Close the campaign.** Both confirmed-hypothesis milestones shipped; both refuted-hypothesis milestones killed without code change. The campaign's closure criteria (every confirmed-H milestone shipped + residual hypotheses documented) are met.

**Shipped milestones:**

- **M2 — Lazy rerank model load** (commit fa5dc5e, [outputs/perf-startup-m2-results.md](../../outputs/perf-startup-m2-results.md)). New `internal/search/LazyReranker` defers `encoder.Load` + `LoadCacheFromFile` until the first `Rerank` call; sync.Once for single-shot under concurrent callers; thread-safe by construction. cmd/ken-mcp builds a closure that performs the 3-step block lazily. **−491 ms on ken-mcp startup when `KEN_MCP_RERANK=on`** — rerank-on startup is now indistinguishable from unset (~30 ms median, perf_startup_m2.sh).
- **M4 — Parallel `structural.Build`** (commit 34a52ae, [outputs/perf-startup-m4-results.md](../../outputs/perf-startup-m4-results.md)). Pass 1 of `internal/structural/Build` refactored to `runtime.NumCPU()` workers using the same per-file work pattern v0.8.7's chunk+embed parallelism shipped under ADR-030. Determinism preserved by writing per-file results into idx-aligned slice + merging in lexical order before Pass 2 builds lookup maps. **3.5× on jekyll (−1,127 ms), 4.5× on ken itself (−360 ms).**

**Killed milestones:**

- **M1 — Default Q8 rerank when present.** Closed without code change ([outputs/perf-startup-m1-results.md](../../outputs/perf-startup-m1-results.md)). Two reasons: (1) M2 already removed the rerank-load cost from the cold-start critical path, so M1's premise dissolved; (2) on Apple Silicon (the campaign's host platform), f32+NEON dominates int8 in both speed AND accuracy — the existing in-tree commentary in cmd/ken/main.go was right. Q8 remains available via `KEN_MCP_RERANK_QUANT=int8` for memory-constrained amd64/Linux deployments.
- **M3 — Warm-up `Encode("")` after index build.** Killed in M0 by H2 refutation. First-query semantic embed pays a ~25% cold penalty in relative terms, but the absolute difference is < 0.3 ms — well below the worthwhile-optimization bar.
- **M5 — Query-path micro-optimizations.** Killed in M0 by H4 confirmation. Warm-search p50 is already sub-millisecond on all three test corpora; no work needed.

### Cumulative cold-start reduction

vs M0 baseline, after M2 + M4:

| Corpus | M0 baseline | After M2 + M4 | Total reduction |
|---|---:|---:|---:|
| tiny (6 files) | 627 ms | 134 ms | **−493 ms (79%)** |
| medium (ken, 250 files) | 1,405 ms | 555 ms | **−850 ms (60%)** |
| large (jekyll, 766 files) | 2,927 ms | 1,309 ms | **−1,618 ms (55%)** |

Warm-search p50 unchanged (already sub-ms; H4 confirmed in M0).

### Alternatives considered

- **Keep M1 open as a 1.0 nice-to-have.** Rejected — without amd64/Linux benchmark data, "default to Q8 because it's smaller" is a vibes-based optimization that would regress arm64. The platform-aware version requires a fetch-and-build path (`ken download-model --quant int8`) that isn't on the campaign scope. Re-open trigger documented in the M1 memo for future sprints.
- **Add per-query rerank-side optimization milestones** beyond M5. Refused — M5 itself was killed by H4; warm-search is already sub-ms. The remaining rerank-side cost is the 491 ms first-load wall, which M2 covers, and the per-rerank-call cost, which M9/M10/M11 already campaigned under project-rerank.
- **Cross-platform x86_64 reproduction.** Documented as nice-to-have in the campaign plan; not gated. arm64 numbers stand as the authoritative campaign baseline.

### Consequences

- **Cold-start budget halved on real corpora**, with the largest reduction (1.6 s) on the multi-language large-corpus case agents care about most.
- **Single source of truth for the rerank load** is now the LazyReranker closure in cmd/ken-mcp. Any future change to model load semantics goes there, not in five scattered call sites.
- **Determinism contract preserved.** Parallel structural.Build produces the same Index.files iteration order as the single-threaded build — every existing test pinning the structural surface (16 across the 10 extractors, plus the dogfood-driven Stage 8 Gate 2 precision check) continues to pass.
- **Campaign re-opens** only on a real user latency report that hits a hot path none of M0's measurements touched (matches the out-of-band trigger in the campaign plan).

This is the second "perf-campaign close" ADR in ken's history; ADR-029 closed project-perf-phase0. Both follow the same pattern: profile → ship the confirmed wins → kill the refuted ones with evidence → close.
