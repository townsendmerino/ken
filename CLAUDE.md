# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`ken` is a **pure-Go, no-cgo** port of [MinishLab/semble](https://github.com/MinishLab/semble), a hybrid code-search tool (lexical BM25 + Model2Vec semantic embeddings + RRF fusion + rerank heuristics). The authoritative docs:

- **[`docs/DESIGN.md`](docs/DESIGN.md)** — algorithm spec, precision contracts, license chain, risk register. Read it before any non-trivial change.
- **[`docs/internal/DECISIONS.md`](docs/internal/DECISIONS.md)** — ADR-style record of every architectural decision (alternatives considered, consequences). Cross-linked from DESIGN.md's "Decisions" summary.
- **[`docs/BENCH.md`](docs/BENCH.md)** — NDCG benchmark reproduction + empirical findings.

The repo is on GitHub at **[townsendmerino/ken](https://github.com/townsendmerino/ken)**.

## Repository ownership (read this first)

**Claude Code is the sole editor of this repository.** A second Claude instance ("the claude app") is used for design/planning discussion only and has been instructed by the user not to modify files here. Implications:

- There is **no lane partition**. Every package in this repo is maintained here directly; the old "report, don't cross lanes" rule no longer applies. (The algorithm packages themselves now live in the sibling `aikit` repo — ADR-034; edit them there.)
- `docs/DESIGN.md` is the shared design doc and may be discussed conceptually with the other instance, but all file edits (code and `docs/DESIGN.md`) happen here.
- Keep the whole tree `gofmt`-clean and `go test ./...` green on every change.

## Commands

```bash
go build ./...                                   # build everything
go test ./...                                    # all tests
go test ./internal/search/ -run TestSerializeRoundtrip_BM25 -v   # a single test (regexp on name)
go vet ./...                                      # must be clean
gofmt -l cmd internal                             # must print nothing (whole tree gofmt-clean)
go fix ./...                                      # Go 1.26 modernizers (SplitSeq, min, range-int)

go run ./cmd/ken index  <path>  [--watch|--no-watch] [--chunker=regex|treesitter|line] [--mode=bm25|semantic|hybrid] [--model=DIR]
go run ./cmd/ken search <path> <query>...  [-k N] [--chunker=...] [--mode=...] [--model=...]
go run ./cmd/ken-mcp                               # stdio MCP server (env-configured; see MCP section)
```

Default mode is **hybrid** (Stage 4). hybrid/semantic need a model directory; the CLI resolves one in priority order: `--model <DIR>` → `$KEN_MODEL_DIR` → `~/.ken/model` (canonical end-user location) → `./testdata/model` (repo-developer fallback). Run `ken download-model` to populate `~/.ken/model` without Python tooling. If none of those resolve, the loader errors clearly with the suggested fix. `ken-mcp` resolves `KEN_MCP_MODEL_DIR` → `~/.ken/model` and, if the model is still missing, **auto-fetches it in the background** (ADR-037, `KEN_MCP_AUTO_FETCH`, default on): it serves bm25 immediately, downloads `potion-code-16M`, then purges the per-repo cache so the next query rebuilds hybrid (search reads `ix.Mode()` per query). `KEN_MCP_AUTO_FETCH=0` reverts to the prior **downgrade-to-bm25-with-stderr-warning** behavior. The DB-Tier-2 case logs a restart prompt instead of a live swap (the Refresher holds the default repo's index).

As of v0.3, **`ken index <path>` defaults to `--watch`** — the process stays alive and re-publishes the index 2 s after any file change (fsnotify + atomic snapshot swap, ADR-012). `--no-watch` is the v0.2-compatible build-once-and-exit opt-out for batch / CI / huge-corpus scenarios. `ken-mcp` **always watches**; no env var to disable it in v0.3.

Toolchain: `go.mod` pins `go 1.26.5` (the go directive doubles as the toolchain floor; no separate `toolchain` line). With `GOTOOLCHAIN=auto` (default) an older system Go auto-downloads 1.26.5. (Bumped 1.26.3 → 1.26.4 for the go1.26.4 stdlib CVE fixes `govulncheck` flagged, then 1.26.4 → 1.26.5 for GO-2026-5856, the crypto/tls Encrypted-Client-Hello privacy leak the govulncheck gate caught as reachable via the MySQL TLS + model-fetch HTTPS paths — see `.github/workflows/govulncheck.yml`.) All CI reads the version via `go-version-file: go.mod`, so this one directive is the single toolchain source of truth. Primary deps: `github.com/townsendmerino/aikit` (+ `aikit/chunk/treesitter` submodule) — the extracted algorithm packages (ADR-034), pin tagged versions and re-run the parity/recall checks on bump; `github.com/odvcencio/gotreesitter` (treesitter grammars), `github.com/go-git/go-git/v5` (ken-mcp clone), `github.com/modelcontextprotocol/go-sdk` (ken-mcp), `github.com/fsnotify/fsnotify` (watch mode), `golang.org/x/sync` (singleflight), plus the DB drivers (`modernc.org/sqlite`, `github.com/jackc/pgx/v5`, `github.com/go-sql-driver/mysql`). After a `go`/`toolchain`/aikit bump, run `go mod tidy`.

### Embedding parity & golden fixtures (now in aikit)

The embedding/tokenizer parity harness moved to aikit with the `embed` package (ADR-034): `pin_inference.py` (golden generator), `parity_dump.py` (the 11,447-input tokenizer harness), `testdata/golden.json`, and the `embed/golden_test.go` / `embed/parity_test.go` tests all live in [`github.com/townsendmerino/aikit`](https://github.com/townsendmerino/aikit) now. Embedding-numerics regressions are caught by **aikit's** suite (cosine ≥ 1 − 1e-5 vs the Python reference; `allow_nan=False` so NaN regressions fail loudly). When bumping the aikit pin, the end-to-end parity check is: run aikit's `TestGolden_EmbeddingCosine` against a real model — it confirms cosine parity is preserved through the new aikit.

ken still uses a per-machine model at `testdata/model/` (not committed — see `testdata/README.md`); ken's own semantic / hybrid tests (e.g. in `internal/search`) **skip** when it's absent, and a green `go test` with those skipped is expected on a fresh checkout. `ken download-model` populates `~/.ken/model` without Python tooling.

## Architecture

The pipeline (semble parity target): **walk → chunk → Arm B enrichment → {BM25 lexical | Model2Vec semantic} → α-weighted RRF fuse → file-coherence + query boosts → path penalties + saturation**. The algorithm spec for each piece is in [`docs/DESIGN.md`](docs/DESIGN.md) §§2–7.

**Arm B enrichment (ADR-035, Stage 8 close):** for every chunk produced from a file whose extension has a registered gotreesitter extractor (`.py`, `.go`, `.ts/.tsx`, `.js/.jsx/.mjs/.cjs`, `.java`, `.rs`, `.cpp/.cc/.cxx/.hpp/.hh/.hxx`, `.c/.h`, `.php`, `.rb`, `.kt/.kts`, `.dart`, `.cs`), the indexer prepends a deterministic per-file label line `# func: NAME | calls: A, B | raises: X\n` before BM25 tokenization and embedding. Pure-Go, no extra model. Default-on; opt out with `KEN_ENRICH=off` or `FSOptions.DisableEnrichment=true`. In-process bench shows **+0.0208 NDCG@10 hybrid on csn-python-nl-stripped (N=500)** and **+0.0321 on CoSQA dev** — reproducing the validated Gate-1 numbers within 0.002 on the production code path. Files with no registered extractor pass through unchanged. Single source of truth: `structural.EnrichFromFileStruct` (no-index per-file path) and `(Index).Enrich` (with optional cross-file callers) both delegate to `enrichCore`.

**Algorithm packages live in `aikit` (ADR-034).** The retrieval primitives — `chunk` (+ `chunk/regex`, `chunk/treesitter`, `chunk/markdown`), `bm25`, `embed`, `ann`, `encoder` (the neural reranker, formerly `coderank`), `topk`, and `fuse` — were extracted into [`github.com/townsendmerino/aikit`](https://github.com/townsendmerino/aikit), which ken pins in `go.mod` (`chunk/treesitter` is its own submodule). The bullets below describe what each does; the `internal/…` paths are pre-extraction and now resolve under `aikit/…`. ken retains the orchestration + ken-specific layers: `internal/repo`, `internal/search`, `internal/structural`, `internal/db`, `internal/sql`, `internal/modelfetch`, `mcp`, and the `cmd/…` binaries.

- **`internal/repo`** (ken) — gitignore-respecting filesystem walk; prunes `.git`, skips binary (NUL sniff), oversized (`KEN_MAX_FILE_BYTES`, default 2 MiB), and minified/generated files (`KEN_MAX_AVG_LINE_BYTES` avg-line-length heuristic, ADR-038 memory campaign M3). Also honors a **`.kenignore` / `.sembleignore`** ignore family (ADR-038) as an independent union with `.gitignore` (two-family evaluation, no cross-file re-includes). The matcher is a deliberate common-subset of gitignore; full pathspec parity is a later dependency swap (`docs/DESIGN.md` §1).
- **`aikit/chunk`** — `Chunk` is the stable unit every stage depends on. Three chunkers land behind a single `Chunker` interface (registered via `database/sql`-style blank imports to avoid an import cycle): `line` (universal 50-line/5-overlap fallback), `regex` (default; per-language rules for Python/Go/TypeScript/Java/Rust + line fallback), and `treesitter` (opt-in as of v0.2.0; pure-Go tree-sitter via `gotreesitter` running cAST split-then-merge — `aikit/chunk/treesitter`, ADR-010/011 in [`docs/internal/DECISIONS.md`](docs/internal/DECISIONS.md)). The byte-fidelity invariant (concat of `Chunk.Text` reproduces source) holds across all three. **Public package as of ADR-032** — the `Chunker` interface is the 1.0-stable surface; the concrete chunkers (especially treesitter, gotreesitter-backed) are best-effort.
- **`aikit/bm25`** — identifier-aware tokenizer (camelCase/PascalCase/ACRONYM/digit splits, plus the whole lowercased run for recall) feeding a Lucene-variant BM25 index (`k1=1.5`, `b=0.75`, non-negative IDF) — pinned to bm25s defaults so ranking can be diffed against semble's `SearchMode.BM25`.
- **`aikit/embed`** — Model2Vec inference. The safetensors blob has **three** tensors (`embeddings` F32, `mapping` I64, `weights` F64). Inference is `normalize(Σ embeddings[mapping[id]]·weights[id] / Σ weights[id])`. Two non-negotiable invariants (see `docs/DESIGN.md` §4): always index through `mapping[]`, and **accumulate in float64** (float32 silently fails ≥1−1e-5 cosine on longer inputs). Empty / all-`[UNK]` → zero vector, not NaN. (As of aikit 1.4, `embed.Load` also reads the standard single-tensor Model2Vec layout, e.g. `potion-retrieval-32M`.)
- **`aikit/ann`** (`flat.go`) — flat brute-force cosine retriever over the dense matrix. Scoring uses the SIMD dot kernel (`linalg.Dot`, **float32-precision** as of aikit 1.4 — near-exact, reorders only sub-1e-6 ties vs a float64 scan; recall@10 re-verified unchanged). HNSW behind this same `Hit/Query` shape later; flat is fine at repo scale.
- **`internal/search`** (ken) + **`cmd/ken`** — orchestration. `FromPath(root, mode, chunker, modelDir)` builds the BM25 index always and additionally embeds every chunk under semantic/hybrid. `Mode = ModeBM25 | ModeSemantic | ModeHybrid`. The retrieval pipeline (`hybrid.go` + `rerank.go` + `penalties.go` + `adaptive.go`) is ported **verbatim from /tmp/semble** (`search.py`, `ranking/{boosting,penalties,weighting}.py`, `tokens.py`); see [`docs/DESIGN.md`](docs/DESIGN.md) §7 for the as-built constants (α-weighted fusion, 3 penalty tiers, file-saturation decay, etc.).
- **`mcp/` + `cmd/ken-mcp`** (ken) — MCP server on top of `github.com/modelcontextprotocol/go-sdk`. The two semble-parity tools (`search`, `find_related`) keep arg schemas + markdown wire format ported verbatim from `/tmp/semble/src/semble/mcp.py` (drop-in for semble); ken adds eight more — the structural tools (`definition`, `references`, `callers`, `outline`, `symbols`), `status`, `recently_changed`, and `reindex_db` (10 total). Per-process repo→Index cache (singleflight dedup + LRU); http(s) URLs shallow-clone via go-git to `$TMPDIR/ken-mcp/<sha256>/`. See "MCP server" section below for install + env vars.

## MCP server

`ken-mcp` is a drop-in replacement for semble's MCP server. Same tool surface (`search`, `find_related`), same wire format, so agents already trained against semble work unchanged.

### **Hard rule — stdout/stderr contract**
stdin and stdout **are** the JSON-RPC channel. ANY write to stdout outside of the SDK's protocol writer corrupts the stream and the agent disconnects with a cryptic JSON-decode error. This is the #1 way new MCP servers fail. `cmd/ken-mcp/main.go` has the full contract at the top — read it before adding ANY dependency, and audit each new dep for default writers pointed at stdout. `TestBinary_StdoutIsCleanJSONRPC` builds the real binary and drives an MCP session through `sdk.CommandTransport` to enforce this; if it fails, you've polluted stdout.

### Env vars (configure ken-mcp at startup)
- `KEN_MCP_DEFAULT_REPO` — optional pre-indexed source; tools may then be called without `repo`.
- `KEN_MCP_MODE` — `bm25`/`semantic`/`hybrid` (default `hybrid`; serves bm25 while the model is missing — see `KEN_MCP_AUTO_FETCH`).
- `KEN_MCP_MODEL_DIR` — Model2Vec snapshot dir (must contain `model.safetensors`). Defaults to `~/.ken/model` when unset.
- `KEN_MCP_AUTO_FETCH` — `1`/`0` (default `1`). On first run with no model + a model-needing mode, fetch `potion-code-16M` in the background and upgrade bm25→hybrid (ADR-037). `0` = downgrade-and-warn only.
- `KEN_MCP_CHUNKER` — `regex`/`treesitter`/`line` (default `regex`).
- `KEN_MCP_CACHE_SIZE` — LRU bound (default 16); `0` means caching is disabled (re-index on every request).
- `KEN_MCP_LOG_LEVEL` — `debug`/`info`/`warn`/`error` (default `warn`); all logs go to stderr.
- `KEN_MEMLIMIT` — optional soft memory limit for the long-lived server (`1GiB`/`512MiB`/byte count → `debug.SetMemoryLimit`; overrides `GOMEMLIMIT`). M2 GC hygiene: ken-mcp also applies `GOGC=50` by default (unless `GOGC` is set) and calls `debug.FreeOSMemory()` after the initial index build and each watch flush — all binary-layer only (`cmd/ken-mcp/gc.go`), never `internal/search`/aikit.

All env vars are validated at startup. Invalid values (typoed enums like `KEN_MCP_MODE=hybryd`, non-integer `KEN_MCP_CACHE_SIZE=of`, `KEN_MCP_MODEL_DIR` pointing at a non-existent path) log a stderr warning and fall back to the documented default — the silent-typo failure mode (where `Atoi("of")` returned 0 and disabled the cache) is gone. Enum match is case-sensitive: `Hybrid` warns and falls back to `hybrid`. Helpers live in `cmd/ken-mcp/env.go` (`envInt` / `envEnum` / `envPath` / `envPathOrURL`).

### Install snippets (mirror semble's, swap `uvx … semble` for the `ken-mcp` binary)

```bash
# Claude Code
claude mcp add ken -s user -- /absolute/path/to/ken-mcp
```

`~/.cursor/mcp.json` / `.cursor/mcp.json`:
```json
{ "mcpServers": { "ken": { "command": "/absolute/path/to/ken-mcp" } } }
```

`~/.codex/config.toml`:
```toml
[mcp_servers.ken]
command = "/absolute/path/to/ken-mcp"
```

`~/.opencode/config.json`:
```json
{ "mcp": { "ken": { "type": "local", "command": ["/absolute/path/to/ken-mcp"] } } }
```

`.vscode/mcp.json`:
```json
{ "servers": { "ken": { "command": "/absolute/path/to/ken-mcp" } } }
```

### Constraints that shape the code

- **Pure Go, no cgo, no per-platform vendored artifacts** — this is the whole point of the port (single static cross-compiled binary). Tree-sitter, native tokenizers, etc. are off the table; pure-Go alternatives go behind interfaces.
- **Deps land with the stage that needs them** — Stage 1 was stdlib-only; the embed normalizer pulled `golang.org/x/text`; ken-mcp pulled `go-sdk`, `go-git`, `x/sync`; v0.2.0's tree-sitter chunker pulled `github.com/odvcencio/gotreesitter`; v0.3's incremental indexing pulled `github.com/fsnotify/fsnotify`. HNSW and the Chroma chunker remain documented future paths in [`docs/DESIGN.md` §10](docs/DESIGN.md#10-risk-register).
- **Validate-against-Python before advancing** — every stage's correctness is defined as parity with semble/`StaticModel.encode()` on the same corpus, not just "looks reasonable". The tokenizer + embedding parity now lives in **aikit** (ADR-034): an 11,447-input tokenizer harness against `transformers.AutoTokenizer` (aikit's `scripts/parity_dump.py` + `embed/parity_test.go` under `-tags=parity`, zero drift across every category) plus the 18-case `golden.json` embedding spot-check (aikit's `embed/golden_test.go`). aikit gates those on its own releases; ken inherits the guarantee by pinning a tagged aikit. ken's own validate-against-Python surface is the retrieval-quality benchmark (`docs/BENCH.md`, the semble NDCG/recall reproduction + `internal/search/recall_decomp_test.go`).
