# ken

**Fast hybrid code search for agents.** Pure Go, single static binary, drop-in MCP-compatible with [MinishLab/semble](https://github.com/MinishLab/semble) — same tool schemas, same output format, install steps swapped to a Go binary.

[![CI](https://github.com/townsendmerino/ken/actions/workflows/ci.yml/badge.svg)](https://github.com/townsendmerino/ken/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go Reference](https://pkg.go.dev/badge/github.com/townsendmerino/ken.svg)](https://pkg.go.dev/github.com/townsendmerino/ken)
![Go 1.26+](https://img.shields.io/badge/go-1.26%2B-blue)

ken is a Go port of [semble](https://github.com/MinishLab/semble): BM25 lexical + Model2Vec semantic embeddings + RRF fusion + a code-aware reranker, with the retrieval algorithm ported **verbatim** from semble's `search.py` + `ranking/*.py`.

## Why ken

- **~97% recall@10 in the default (hybrid) mode** — **0.967 NL / 0.995 symbol** on semble's 1,251-query benchmark, vs grep's ~99.9% — while costing an agent **~46× fewer tokens** than `grep + Read` (4,120 vs 189,773 median tokens on NL queries — measured in the same default hybrid mode). For "find the chunk that answers this," that's a 1–2 order-of-magnitude token win at near-parity recall. (Reproduce: [`docs/BENCH.md`](docs/BENCH.md#default-mode-hybrid-recall--the-number-that-matters).)
- **Single static binary.** Pure Go, no cgo, no Python interpreter on cold start, no GIL on indexing. Cross-compiles to Linux / macOS / Windows (amd64/arm64) for free.
- **Drop-in for semble.** Same `search` / `find_related` MCP tool schemas and the same markdown-string wire format — swap the `command:` path and existing agents work unchanged.
- **Local, CPU-only.** Embedding inference, BM25, and fusion all run on the CPU. No API keys, no GPU, no vector DB, air-gapped friendly.

> **One knob controls recall.** The **82–91%** figure in the token-budget tables is the **BM25-only fallback** ken runs in when no embedding model is installed. **ken-mcp fetches the model automatically on first run** (~60 MB, pure-Go, no Python — serves bm25 until it lands, then upgrades to the **~97%** hybrid path; `KEN_MCP_AUTO_FETCH=0` to disable). For the CLI, run `ken download-model` once. Exhaustive enumeration (refactors, pre-rename audits) still belongs to grep; ken is for "find the chunk that answers this."

## Where to start

- **[ARCHITECTURE.md](ARCHITECTURE.md)** — current-state map: module layout, runtime/concurrency model, data flow, invariants. Start here for the code.
- **[docs/USERS.md](docs/USERS.md)** — agent users. Install ken-mcp, point your agent at it, use the nine tools. 5-minute on-ramp.
- **[docs/DEVELOPERS.md](docs/DEVELOPERS.md)** — SDK authors and tuners. The `mcp.Run` embedded-corpus library, prebuilt indices, `fs.FS` indexing, custom chunkers, tuning rerank, performance expectations.
- **[docs/DESIGN.md](docs/DESIGN.md)** + **[docs/internal/DECISIONS.md](docs/internal/DECISIONS.md)** — algorithm spec + every architectural decision (ADRs).
- **[docs/BENCH.md](docs/BENCH.md)** — benchmark reproduction (NDCG, token-budget recall, the hybrid-vs-BM25 decomposition).

## Quickstart

Install via a package manager:

```bash
# macOS / Linux (Homebrew) — installs both `ken` and `ken-mcp`:
brew install --cask townsendmerino/tap/ken
```
```powershell
# Windows (Scoop):
scoop bucket add townsendmerino https://github.com/townsendmerino/scoop-bucket
scoop install ken
```

Or with Go:

```bash
# Install both binaries (Go 1.26+).
go install github.com/townsendmerino/ken/cmd/ken@latest
go install github.com/townsendmerino/ken/cmd/ken-mcp@latest

# Download the default Model2Vec model (~60 MB, one-time). Pure Go, no Python.
# (ken-mcp auto-fetches this on first run; the CLI needs it explicitly.)
# This is the single biggest retrieval-quality lever — it puts you on the ~97% path.
ken download-model

# Search any local repo from the CLI.
ken search /path/to/myrepo "save model to disk" --model ~/.ken/model
```

Or skip the model and use lexical-only mode (BM25-only costs ~14 pp recall@10 vs the hybrid default — see [`docs/BENCH.md`](docs/BENCH.md#default-mode-hybrid-recall--the-number-that-matters)):

```bash
ken search /path/to/myrepo "validateToken" --mode bm25
```

Pre-built binaries for **macOS, Linux, and Windows** (amd64/arm64) are attached to each [release](https://github.com/townsendmerino/ken/releases) — `.tar.gz` for macOS/Linux, `.zip` for Windows.

As of v0.3, `ken index <path>` defaults to **watch mode** — it stays alive and re-indexes on change (2 s debounce); `--no-watch` restores build-once-and-exit. `ken-mcp` always watches, so an agent editing the repo mid-session sees its own changes without a restart. ken also respects **nested `.gitignore`** files (per-directory, matching git).

## Install as an MCP server

`ken-mcp` speaks JSON-RPC over stdio and serves the same two core tools (`search`, `find_related`) semble does, with the same arg shapes and markdown output.

```bash
# Claude Code
claude mcp add ken -s user -- /absolute/path/to/ken-mcp
```

```jsonc
// ~/.cursor/mcp.json  (or .cursor/mcp.json) — also .vscode/mcp.json with "servers"
{ "mcpServers": { "ken": { "command": "/absolute/path/to/ken-mcp" } } }
```

```toml
# ~/.codex/config.toml
[mcp_servers.ken]
command = "/absolute/path/to/ken-mcp"
```

```json
// ~/.opencode/config.json
{ "mcp": { "ken": { "type": "local", "command": ["/absolute/path/to/ken-mcp"] } } }
```

### Core environment variables

| Variable | Default | Purpose |
|---|---|---|
| `KEN_MCP_DEFAULT_REPO` | (unset) | Pre-indexed source; lets tools omit the `repo` arg. |
| `KEN_MCP_MODE` | `hybrid` | `bm25` / `semantic` / `hybrid`. Serves `bm25` while the model is missing — fetched on first run by default (see `KEN_MCP_AUTO_FETCH`). |
| `KEN_MCP_MODEL_DIR` | `~/.ken/model` | Path to a Model2Vec snapshot containing `model.safetensors`. Falls back to `~/.ken/model` (where `ken download-model` writes) when unset. |
| `KEN_MCP_AUTO_FETCH` | `1` | On first run with a model-needing mode and no model present, fetch `potion-code-16M` (~60 MB) in the background, serving `bm25` until it lands then upgrading to hybrid. `0` disables (serve bm25, warn). |
| `KEN_MCP_CHUNKER` | `regex` | `regex` / `treesitter` / `line` / `markdown`. See [Choosing a chunker](#choosing-a-chunker). |
| `KEN_MCP_CACHE_SIZE` | `16` | LRU bound on the repo→Index cache. |
| `KEN_MCP_LOG_LEVEL` | `warn` | `debug` / `info` / `warn` / `error`. All logs go to stderr; **stdout is the JSON-RPC channel** ([details](docs/DESIGN.md#hard-rule--stdoutstderr-contract)). |
| `KEN_ALLOW_PRIVATE_CLONE_TARGETS` | `0` | Off by default: for `http(s)` `repo` URLs, ken rejects loopback / link-local / RFC1918 addresses (SSRF guard). Set `1` to allow internal git hosts. |

The full env reference — including the `KEN_DB_*` database variables — is in [docs/USERS.md](docs/USERS.md) and [docs/db-indexing.md](docs/db-indexing.md). For agents that should route between ken and grep deliberately (rather than ken's default "prefer ken" instruction), see the routing snippet in [docs/USERS.md](docs/USERS.md).

## Tools

Both core tools return a formatted markdown string identical to semble's `_format_results` output. (ken-mcp also exposes seven structural tools — `definition`, `references`, `callers`, `outline`, `symbols`, `recently_changed`, `status` — plus `reindex_db` when a database is configured; see [docs/USERS.md](docs/USERS.md).)

### `search`

| Arg | Type | Required | Default | Description |
|---|---|---|---|---|
| `query` | string | ✓ | — | Natural language or code query. |
| `repo` | string |   | — | `https://` / `http://` URL or local directory. Required if no `KEN_MCP_DEFAULT_REPO`. |
| `mode` | `hybrid`\|`semantic`\|`bm25` |   | `hybrid` | Search mode. |
| `top_k` | int |   | `5` | Number of results. |

### `find_related`

| Arg | Type | Required | Default | Description |
|---|---|---|---|---|
| `file_path` | string | ✓ | — | Path as it appears in a `search` result. |
| `line` | int (1-indexed) | ✓ | — | A line inside the chunk to seed the similarity search. |
| `repo` | string |   | — | Same as for `search`. |
| `top_k` | int |   | `5` | Number of similar chunks. |

## What ken indexes

ken's hybrid retrieval is calibrated for **source code** (Python / Go / TypeScript / Java / Rust have language-aware chunking; others fall back to the line chunker) and **documentation** (markdown chunked on heading boundaries, code blocks/tables kept atomic, frontmatter handled). Mixed code-and-docs corpora route per file by extension.

It also indexes **database schemas alongside code** — static `.sql` files (with migration-history folding) and live introspection of **Postgres / SQLite / MySQL / MariaDB** — so an agent answering "how do users get authenticated" gets the Go function, the SQL it runs, the `users` table definition, and the FK relationships in one ranked list. Full reference (Tier-1/Tier-2, row sampling, LISTEN/NOTIFY, the `reindex_db` tool, PII stance, all `KEN_DB_*` vars): **[docs/db-indexing.md](docs/db-indexing.md)**.

For plain prose with no code or structured docs, BM25 mode (`--mode=bm25`) carries the load; the semantic model is code-trained and unvalidated on literary text.

## How it works

```
gitignore-respecting walk
    → regex chunker (Python / Go / TS / Java / Rust) with line-chunker fallback
    → BM25 (Lucene variant, k1=1.5, b=0.75)  +  Model2Vec semantic (cosine over a dense matrix)
    → α-weighted RRF fusion (α auto-detected: 0.3 for symbol queries, 0.5 for NL)
    → file-coherence boost + query-type boosts (definition / embedded-symbol / stem-match)
    → path penalties (test files, compat / legacy, `.d.ts`) + file-saturation decay
    → top-k
```

The retrieval algorithm is a verbatim port of semble's `search.py` + `ranking/*.py`; see [docs/DESIGN.md §7](docs/DESIGN.md#7-hybrid-retrieval--rerank) for every constant and pipeline-order subtlety, and [§4](docs/DESIGN.md#4-model2vec-inference-format) for the Model2Vec inference contract (three-tensor `safetensors`, the `mapping[]` indirection, the float64 precision that's load-bearing for cosine parity).

## Comparison to semble

| Property | semble | ken |
|---|---|---|
| Language / distribution | Python · `uvx` / `pip` | Go · single static binary |
| Cold start | ~500 ms (interpreter + numpy + model) | ~10–20 ms `ken search` over a tiny index |
| Retrieval algorithm | reference implementation | verbatim port (constants + pipeline order from `search.py` + `ranking/*.py`) |
| NDCG@10 on semble's benchmark | 0.854 | **0.842 hybrid** (gap 0.012, full 63 repos × 1,251 queries) |
| Recall@10 on agent queries | (not measured) | **~0.97 hybrid** (0.967 NL / 0.995 symbol); BM25-only fallback ~0.84 |
| Tokens to recall@10 | (not measured) | **~46× fewer than grep+Read** on NL queries (4,120 vs 189,773 median, hybrid) |
| MCP server | yes | yes — drop-in (same schemas + wire format) |
| Binary size | n/a | release (slim) `ken` ~22 MB · `ken-mcp` ~38 MB |
| Requires `huggingface-cli` | yes | **no** — `ken download-model` fetches direct from HF |

Full methodology, the per-ablation breakdown (semantic-raw matches semble within 0.003, validating the embedding + tokenizer + ANN port), the CoIR-CSN-Python external anchor, and every footnote are in **[docs/BENCH.md](docs/BENCH.md)**.

## Compared to other agent code-search tools

The crowded part of this category splits on one axis: **what you have to run.** ken's bet is that the embedding model belongs *inside* the binary — pure-Go Model2Vec inference, no cgo — so there's nothing else to stand up: no embedding daemon, no vector database, no API key, air-gapped. The two closest points of comparison:

- **[grepai](https://github.com/yoanbernabeu/grepai)** — the closest architectural analog: a single Go binary with a file watcher and an MCP server, 100% local. It offloads embeddings to a separate **Ollama** server (you install + run Ollama and pull a model).
- **[claude-context](https://github.com/zilliztech/claude-context)** (Zilliz) — the most visible: hybrid BM25 + dense search, but backed by a **vector database** (self-hosted Milvus via Docker, or managed Zilliz Cloud) and an **embedding provider** (OpenAI / VoyageAI / Gemini API, or local Ollama).

| | **ken** | grepai | claude-context |
|---|---|---|---|
| Runtime | single static Go binary (no cgo) | single Go binary | Node/TS (npm) |
| Embeddings | **in-process, pure Go** (Model2Vec) | external **Ollama** daemon | external provider (OpenAI / Voyage / Gemini, or Ollama) |
| **External services needed** | **none** — auto-fetches a ~60 MB model, then runs offline | Ollama (daemon + model) | **vector DB** (Milvus/Docker or Zilliz Cloud) **+** an embedding API/daemon |
| Retrieval | BM25 + dense + RRF + code-aware rerank | dense + call graphs | hybrid (BM25 + dense) |
| Recall / NDCG | **0.967 recall@10 · 0.842 NDCG@10**, with a reproduction harness | not published | not published |
| Token savings | **~46× vs grep+Read**, measured + reproducible | not published | vendor-claimed −39% vs a baseline |
| Speed | index ~1.6 s / 13 k chunks; hybrid search p50 ~1.5 ms (measured) | vendor: "10 k files in seconds, ms queries" | depends on the vector DB + network |
| Languages (structural) | 13 (tree-sitter) | 10 | chunk-level, language-agnostic |
| License | MIT | MIT | MIT |

Two honest caveats. **First**, ken's numbers ship with reproduction commands ([docs/BENCH.md](docs/BENCH.md)); the cells marked "not published" mean we found no standard-benchmark figure to cite and have not independently benchmarked the others' speed — architecture, dependencies, and license are the verifiable axes (as of June 2026). **Second**, the tools optimize for different things — grepai adds call-graph tracing; claude-context leans on a managed vector DB for scale-out. ken's specific claim is **near-grep recall at ~1–2 orders of magnitude fewer tokens, from one binary with no external services, every number reproducible.**

## Choosing a chunker

The default `regex` chunker handles most cases well. The opt-in `treesitter` chunker (`--chunker=treesitter` / `KEN_MCP_CHUNKER=treesitter`, pure-Go [`gotreesitter`](https://github.com/odvcencio/gotreesitter)) measurably wins for **Kotlin, Zig, TypeScript, Java, PHP** and loses on **Python, C, Rust, Lua, Scala** — net Δ −0.004 NDCG overall (within noise), so it stays opt-in. The full per-language recommendation table is in [docs/BENCH.md](docs/BENCH.md#per-language-chunker-recommendation-full-table); the default-stays-regex rationale is [ADR-011](docs/internal/DECISIONS.md#adr-011-default-chunker-stays-regex-in-v020-treesitter-is-opt-in).

## For SDK authors: ship docs as a single binary

The `mcp.Run` library lets you bake a `//go:embed` corpus + the Model2Vec model into **one static MCP server binary** — no backend, no vector DB, no per-query network egress, version-pinned by build artifact. ~20 lines of `main.go`, `go build`, push to a GitHub release; users `brew install` and add one line to their agent config. The walker and indexer take any `fs.FS` (embed.FS, fstest.MapFS, tarball-backed), which also gives agent sandboxing by construction.

Full guide — the canonical pattern, prebuilt indices for fast cold start, the binary-size contract, and the opt-in `mcp/db` package — is in **[docs/DEVELOPERS.md](docs/DEVELOPERS.md)**.

**Live demos** (downloadable `mcp.Run` binaries over real codebases, with audit transcripts): [`demos/v0.1.0` release](https://github.com/townsendmerino/ken/releases/tag/demos/v0.1.0) — Kubernetes v1.31.0 (59,795 chunks) and PostgreSQL 17.0 (64,506 chunks). Writeup: [*I shipped two downloadable code search binaries. The audit caught two bugs.*](https://townsendmerino.github.io/ken/demos-audit/).

## Roadmap

The risk register with explicit triggers is in [docs/DESIGN.md §10](docs/DESIGN.md#10-risk-register); the living 1.0-readiness tracker is [docs/internal/road-to-1.0.md](docs/internal/road-to-1.0.md). Retrieval is treated as closed for 1.0 (the relevance curve is flat); remaining work is polish + onboarding (getting fresh installs onto the hybrid path) + distribution.

## How this was built

ken is a port. The retrieval algorithm is verbatim from [MinishLab/semble](https://github.com/MinishLab/semble) (Python); the Go implementation was written by Claude under fixed constraints: pure Go / no cgo, algorithm constants ported verbatim and never tuned, **original source wins whenever Claude's reconstruction diverges from semble's live code**. That last rule caught five material errors during the rerank-pipeline port — each a confident-sounding hallucination that was wrong when checked against the Python source. The discipline of always checking, the verbatim-port rule, and the 11k-input tokenizer parity harness (which surfaced three bugs an 18-case spot-check missed) are human-supplied. Every architectural decision is recorded in [docs/internal/DECISIONS.md](docs/internal/DECISIONS.md).

## Acknowledgments

ken stands on MinishLab's shoulders — the retrieval algorithm, the model, the whole embedding-table approach are theirs.

- **[semble](https://github.com/MinishLab/semble)** — the original Python implementation. © Thomas van Dongen, MIT.
- **[model2vec](https://github.com/MinishLab/model2vec)** — the static-embedding library whose three-tensor format ken implements. © Thomas van Dongen, MIT.
- **[potion-code-16M](https://huggingface.co/minishlab/potion-code-16M)** — model weights, distilled from `nomic-ai/CodeRankEmbed` (MIT), itself from `Snowflake/snowflake-arctic-embed-m-long` (Apache-2.0). © Minish Lab. Redistributed per [`NOTICE`](NOTICE).

## License

ken is [MIT-licensed](LICENSE). It bundles attribution for the redistributed model weights in [`NOTICE`](NOTICE) and a generated dependency-license list in [`THIRD_PARTY_LICENSES.md`](THIRD_PARTY_LICENSES.md); every link in the provenance chain is permissive (MIT, Apache-2.0, MPL-2.0). See [docs/DESIGN.md §6](docs/DESIGN.md#6-license--attribution-chain).

For contributors: [`CLAUDE.md`](CLAUDE.md) has the build/test/formatting conventions and the project's invariants (precision contract, stdout/stderr contract).
