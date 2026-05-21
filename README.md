# ken

**Fast hybrid code search for agents.** Pure Go, single static binary, drop-in MCP-compatible with [MinishLab/semble](https://github.com/MinishLab/semble) — same tool schemas, same output format, same install steps swapped to a Go binary.

*Built collaboratively: most of the Go implementation written by Claude, with constraints, architectural decisions, and review discipline from [@townsendmerino](https://github.com/townsendmerino). The verbatim-port rule and the corpus-scale parity harness — the things that make this a faithful port instead of an approximate one — came from the human side. See [How this was built](#how-this-was-built).*

[![CI](https://github.com/townsendmerino/ken/actions/workflows/ci.yml/badge.svg)](https://github.com/townsendmerino/ken/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go Reference](https://pkg.go.dev/badge/github.com/townsendmerino/ken.svg)](https://pkg.go.dev/github.com/townsendmerino/ken)
![Go 1.26+](https://img.shields.io/badge/go-1.26%2B-blue)

ken is a Go port of semble. The retrieval algorithm is ported verbatim from semble's `search.py` + `ranking/*.py`; ken adds two things on top: **runtime properties** (single-binary distribution, no Python interpreter import on cold start, no GIL on the indexing pipeline) and **measured agent-input efficiency** (~44× fewer tokens than grep+Read at recall@10 on semble's diverse-query benchmark; at corpus scale — CoIR-CSN-Python's 280K files — corpus-wide grep is functionally impossible and ken's 1,296-token result is the only workable path). The honest tradeoff: ken's recall caps at 82–91% vs grep's ~99%, so exhaustive enumeration (refactors, pre-rename audits) still belongs to grep — but for "find the chunk that answers this," ken wins by 1–2 orders of magnitude on tokens. Full table in [`docs/BENCH.md`](docs/BENCH.md#token-budget-recall--agent-side-efficiency). If you already use semble in your agent, you can swap to ken-mcp without re-prompting; the wire format is the same string semble emits.

## Quickstart

```bash
# Install both binaries (Go 1.26+).
go install github.com/townsendmerino/ken/cmd/ken@latest
go install github.com/townsendmerino/ken/cmd/ken-mcp@latest

# Download the default Model2Vec model (~64 MB, one-time).
# Pure Go, no Python tooling required.
ken download-model

# Search any local repo from the CLI.
ken search /path/to/myrepo "save model to disk" --model ~/.ken/model
```

Or skip the model download and use lexical-only mode:

```bash
ken search /path/to/myrepo "validateToken" --mode bm25
```

Library use (sketch):

```go
import "github.com/townsendmerino/ken/internal/search"

ix, _ := search.FromPath("/path/to/myrepo", search.ModeHybrid, "regex", "/path/to/model")
for _, r := range ix.Search("save model to disk", 10) {
    fmt.Printf("%.3f  %s:%d-%d\n", r.Score, r.Chunk.File, r.Chunk.StartLine, r.Chunk.EndLine)
}
```

Pre-built binaries for macOS and Linux are attached to each [release](https://github.com/townsendmerino/ken/releases).

As of v0.3, `ken index <path>` defaults to **watch mode** — it keeps the process alive and re-indexes files on change (2 s debounce); pass `--no-watch` for the v0.2 build-once-and-exit behavior. `ken-mcp` watches always — an agent editing the repo mid-session sees its own changes without a restart.

The default `regex` chunker handles most cases well. If you index a lot of Kotlin / Zig / TypeScript / Java / PHP, the opt-in `treesitter` chunker (`--chunker=treesitter` / `KEN_MCP_CHUNKER=treesitter`) measurably wins for those languages — see ["Choosing a chunker"](#choosing-a-chunker) for the per-language recommendation.

## Features

- **Pure Go, no cgo.** Single static binary; `GOOS`/`GOARCH` cross-compiles for free; no `libtokenizers.a` to vendor per platform.
- **Drop-in MCP-compatible with semble.** Same `search` / `find_related` tool schemas, same markdown-string output format, install snippets adapted from semble's README.
- **Algorithm verbatim from semble.** BM25 + Model2Vec semantic + α-weighted RRF fusion + code-aware rerank (definition / embedded-symbol / file-coherence / stem-match boosts) + path penalties + file-saturation decay. See [docs/DESIGN.md §7](docs/DESIGN.md#7-hybrid-retrieval--rerank).
- **Measured agent-input efficiency.** ~44× fewer tokens than grep+Read at recall@10 on semble NL queries (4,269 vs 189,591 tok); ~16× on symbol queries; at 280K-file corpus scale, grep+Read is functionally impossible and ken is the only workable path. Full breakdown + caveats in [`docs/BENCH.md`](docs/BENCH.md#token-budget-recall--agent-side-efficiency).
- **Tokenizer parity proven against `transformers.AutoTokenizer`** on an 11k-input adversarial+repo corpus (`scripts/parity_dump.py` + `internal/embed/parity_test.go`).
- **Fast cold start.** No Python interpreter import (`ken search` from a tiny index returns in ~10–20 ms on a Mac).
- **Concurrent indexing scaled to cores.** No GIL.
- **CPU-only.** No API keys, no GPU, no external services.

## MCP server

`ken-mcp` speaks JSON-RPC over stdio. Configure your agent to invoke it; it serves the same two tools (`search`, `find_related`) semble does, with the same arg shapes and the same markdown-string output.

### Install in your agent

```bash
# Claude Code
claude mcp add ken -s user -- /absolute/path/to/ken-mcp
```

`~/.cursor/mcp.json` (or `.cursor/mcp.json`):
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

### Environment

| Variable | Default | Purpose |
|---|---|---|
| `KEN_MCP_DEFAULT_REPO` | (unset) | Pre-indexed source; lets tools omit the `repo` arg. |
| `KEN_MCP_MODE` | `hybrid` | `bm25` / `semantic` / `hybrid`. Auto-downgrades to `bm25` with a stderr warning if the model dir is unreachable. |
| `KEN_MCP_MODEL_DIR` | (unset) | Path to a Model2Vec snapshot containing `model.safetensors`. Empty ⇒ `bm25`-only. |
| `KEN_MCP_CHUNKER` | `regex` | `regex` / `treesitter` / `line`. See ["Choosing a chunker"](#choosing-a-chunker). |
| `KEN_MCP_CACHE_SIZE` | `16` | LRU bound on the repo→Index cache. |
| `KEN_MCP_LOG_LEVEL` | `warn` | `debug` / `info` / `warn` / `error`. All logs go to stderr; **stdout is the JSON-RPC channel** ([details](docs/DESIGN.md#hard-rule--stdoutstderr-contract)). |

### Tuning ken's routing for your repo

By default, `ken-mcp`'s server-side instructions tell agents to prefer ken's `search` and `find_related` tools over grep, Glob, or Read for code-related questions — semble's verbatim behavior, faithful to the drop-in claim. For many repos that default is right; for some it's too aggressive (small codebases where grep is plenty fast; refactors that need exhaustive enumeration that top-N retrieval can silently miss).

If you'd rather have agents route between ken and grep deliberately, add something like the following to your repo's `CLAUDE.md`:

> **Search routing — ken vs grep.** The `ken` MCP server is user-scoped (`claude mcp add ken -s user …`); not every session has it. Check the tool list before assuming.
>
> - **ken** — first-pass "show me the surface of X", semantic / conceptual queries ("where do we handle X?"), unfamiliar areas. Returns a ranked top-N grouped across layers (handler → store → resolver → migrations → generated → docs). ~1–2 s warm round-trip.
> - **grep / rg** — exhaustive enumeration, pre-rename audits, every literal occurrence, known-identifier lookups, one-off literal checks. ~0.06 s and deterministic. **Use grep before any rename or refactor that must be complete** — ken is top-N and can miss matches past its result window.
> - Don't reach for ken on a one-off literal lookup where you already know the symbol — the latency tax isn't worth it.

ken's defaults stay unchanged; this is per-repo tuning, not a configuration flag.

## Tools

Both tools return a formatted markdown string identical to semble's `_format_results` output.

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

Example response (verbatim from a real session against this repo's polyglot fixture):

```
Search results for: "validate_user" (mode=bm25)

## 1. auth.py:1-22  [score=5.518]
​```
"""Authentication helpers."""

import hashlib

@dataclass
class User:
    name: str
    token: str

    def is_valid(self):
        return bool(self.token)

# validate_user checks a token against a user record.
def validate_user(user, token):
    return user.token == token
​```
```

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

The retrieval algorithm is a verbatim port of semble's `search.py` + `ranking/*.py`; see [docs/DESIGN.md §7](docs/DESIGN.md#7-hybrid-retrieval--rerank) for every constant, every pipeline-order subtlety, and where the original scoping reconstruction diverged from semble's live source. The Model2Vec inference path (three-tensor `safetensors` layout, the `mapping[]` indirection, the float64 precision contract that's load-bearing for ≥1−1e-5 cosine parity) is in [§4](docs/DESIGN.md#4-model2vec-inference-format).

## Choosing a chunker

ken ships with **two chunkers** behind the same `--chunker=` flag (CLI) / `KEN_MCP_CHUNKER=` env var (MCP):

- **`regex`** *(default)* — hand-rolled per-language regex rules for Python / Go / TypeScript / Java / Rust with a line-window fallback for everything else. Smallest binary (3.9 MB ken / 16 MB ken-mcp).
- **`treesitter`** *(opt-in)* — pure-Go tree-sitter via [`gotreesitter`](https://github.com/odvcencio/gotreesitter), running the cAST split-then-merge algorithm from [arXiv 2506.15655](https://arxiv.org/html/2506.15655). 206 grammars embedded (~+26 MB binary).

**TL;DR:** stay on `regex` unless you index one of the languages where treesitter measurably wins.

The NDCG@10 difference is small (overall hybrid: treesitter 0.838 vs regex 0.842 — Δ −0.004, within bench noise), but it's not uniform per-language. From the v0.2.0 measurement on semble's 63-repo benchmark:

| Language | regex | treesitter | Recommendation |
|---|---:|---:|---|
| Kotlin | 0.806 | **0.817** | **`treesitter`** *(+0.011)* |
| Zig | 0.867 | **0.880** | **`treesitter`** *(+0.013)* |
| TypeScript | 0.676 | **0.685** | **`treesitter`** *(+0.009)* |
| Java | 0.829 | **0.835** | **`treesitter`** *(+0.006)* |
| PHP | 0.860 | **0.865** | **`treesitter`** *(+0.005)* |
| Python | **0.870** | 0.861 | `regex` *(−0.009)* |
| C | **0.748** | 0.731 | `regex` *(−0.017)* |
| C++ | **0.896** | 0.884 | `regex` *(−0.012)* |
| Rust | **0.806** | 0.793 | `regex` *(−0.013)* |
| Lua | **0.838** | 0.816 | `regex` *(−0.022)* |
| Scala | **0.905** | 0.883 | `regex` *(−0.022)* |
| Go | **0.849** | 0.846 | either *(tied within ±0.005)* |
| JavaScript | 0.917 | 0.912 | either |
| Ruby | 0.903 | 0.903 | either |
| Swift | 0.846 | 0.841 | either |
| Elixir | 0.911 | 0.907 | either |
| Haskell | 0.738 | 0.739 | either |
| C# | 0.859 | 0.859 | either *(treesitter auto-falls-back to line)* |
| Bash | 0.821 | 0.821 | either *(treesitter auto-falls-back to line)* |

Notes on the auto-fallback rows:
- **C#** — the gotreesitter v0.18.0 C# grammar OOMs on real-world C# files (1.7+ GB RSS during indexing). The treesitter chunker detects unsupported languages and routes them through the line chunker, so C# behaves identically under both selections.
- **Bash** — the bash grammar is pathologically slow on real bash-it content (~39% of files timeout). Same auto-fallback behavior.

The full per-language NDCG breakdown plus the empirical findings that informed this is in [`docs/BENCH.md`](docs/BENCH.md). The rationale for default-stays-regex is in [`docs/DECISIONS.md` ADR-011](docs/DECISIONS.md#adr-011-default-chunker-stays-regex-in-v020-treesitter-is-opt-in).

## Comparison to semble

| Property | semble | ken |
|---|---|---|
| Language | Python | Go |
| Distribution | `uvx` / `pip install` | single static binary |
| Cold start | (Python interpreter + `import numpy` + model load: ~500 ms per [semble README](https://github.com/MinishLab/semble#benchmarks)) | ~10–20 ms `ken search` over a tiny index (measured, M2 Mac) |
| Index this repo (542 chunks, hybrid w/ model) | (not measured locally) | **0.45 s** (measured) |
| Index `/tmp/semble` checkout (hybrid w/ model) | (not measured locally) | **1.80 s** (measured) |
| Index this repo (BM25 only) | (not measured locally) | **0.06 s** (measured) |
| Retrieval algorithm | reference implementation | verbatim port (constants and pipeline order ported from `search.py` + `ranking/*.py`) |
| NDCG@10 on semble's benchmark | 0.854 ([semble README](https://github.com/MinishLab/semble#benchmarks)) | **0.842 hybrid** (gap 0.012, full corpus 63 repos × 1251 queries)† |
| NDCG@10 on CoIR-CSN-Python (external) | (not measured; semble doesn't run this bench) | **0.8743 bm25 / 0.7839 hybrid** ([see why](#benchmarks--external-reference-coir-csn-python))†† |
| Median tokens to recall@10 on agent queries | (not measured; semble doesn't run this bench) | **4,269 tok @ 82% recall** on semble NL queries — vs grep+Read's 189,591 tok @ 99.9% (44× cheaper at 17 pp lower recall)††† |
| MCP server | yes | yes — drop-in compatible (same tool schemas, same wire format) |
| Binary size | n/a (Python env) | `ken` 3.9 MB · `ken-mcp` 16 MB |
| Requires `huggingface-cli` for model | yes | **no** — `ken download-model` fetches direct from HF (or skip and use `--mode bm25`) |

† **Measured at v0.1.0 / v0.2.0 against semble's published benchmark** (63 repos, 1251 queries, semble's own `benchmarks.metrics.ndcg_at_k` + `target_rank`). Reproduce: see [`docs/BENCH.md`](docs/BENCH.md). Ablation breakdown vs semble's published raw retrieval numbers:
>
> | Mode | semble (raw) | ken regex (default) | ken treesitter (opt-in) |
> |---|---:|---:|---:|
> | Semantic only (potion-code-16M) | 0.650 | **0.647** | — |
> | BM25 only | 0.675 | 0.624 | 0.621 |
> | **Hybrid (full ranker)** | **0.854** | **0.842** | **0.838** |
>
> The semantic-raw match within 0.003 isolates and validates the embedding + tokenizer + ANN port. The BM25 tokenizer was also re-aligned to a verbatim port of semble's `tokens.py` (snake-case compound preservation, ASCII-only identifier extraction, compound-first emission order). The v0.2.0 tree-sitter chunker (`--chunker=treesitter` via [`gotreesitter`](https://github.com/odvcencio/gotreesitter)) trades NDCG per-language without net movement — clear wins on Kotlin / Zig / TypeScript / Java / PHP, losses on Python / Rust / C / Lua / Scala — so the **default chunker stays regex** and treesitter is opt-in. See ["Choosing a chunker"](#choosing-a-chunker) for the per-language recommendation and [`docs/DECISIONS.md` ADR-011](docs/DECISIONS.md#adr-011-default-chunker-stays-regex-in-v020-treesitter-is-opt-in) for the full rationale.

†† CoIR-CSN-Python numbers reported separately because they tell a different story than semble's bench: on CSN, BM25 beats hybrid by ~0.09 due to a substring-leak artifact in how CoIR reframes the CodeSearchNet dataset (queries are Python function sources; documents are docstrings extracted from those same functions, so the answer is a literal substring of the query). See the ["Benchmarks — external reference"](#benchmarks--external-reference-coir-csn-python) section and [`docs/BENCH.md`](docs/BENCH.md#external-benchmark--coir-csn-python) for the corrected explanation. semble's bench is the verbatim-port confirmation; CoIR-CSN is the externally-reproducible anchor against published code-IR baselines but is read as a dataset-construction case study, not as evidence about ken's hybrid retrieval on natural NL-to-code queries.

††† Measured at v0.3.0 against semble's 63-repo benchmark (914 NL queries from semble's 1,251-query corpus, ranked by ken's regex chunker, K=10). The honest framing: ken trades ~17 percentage points of recall for ~44× fewer agent-input tokens. Exhaustive enumeration (refactors, pre-rename audits) still belongs to grep — ken is for "find the chunk that answers this." Full per-query-class table (symbol + NL) and the methodology + caveats are in [`docs/BENCH.md`](docs/BENCH.md#token-budget-recall--agent-side-efficiency).

semble timings cited above are from semble's own [README "Benchmarks" section](https://github.com/MinishLab/semble#benchmarks); ken's are measured on the included `testdata/repo` polyglot fixture and on a sibling shallow clone of `/tmp/semble`. Cold-start was timed by `/usr/bin/time -p ken search testdata/repo "validate" -k 1 --mode bm25` over three trials (M2 MacBook Air, Go 1.26.3, darwin/amd64 build under Rosetta).

## Benchmarks — external reference (CoIR-CSN-Python)

A single externally-reproducible NDCG@10 number on [CoIR](https://github.com/CoIR-team/coir)'s `CodeSearchNet-python` task, independent of semble's own benchmark — gives readers a comparable anchor against published code-IR baselines.

Result (v0.2.0, 1000-query subsample, regex chunker):

| Mode                       | NDCG@10 |
|----------------------------|--------:|
| bm25                       |  0.8743 |
| semantic                   |  0.7405 |
| **hybrid (default)**       | **0.7839** |

Reproduce:

```bash
python scripts/bench_coir.py                                # ~45 s download + 280k corpus files
KEN_COIR_QUERY_LIMIT=1000 go test -tags=bench ./bench/ndcg/ -run TestCoIR -v   # ~13 min
```

A nuance worth surfacing up front: **on CSN-Python, BM25 beats hybrid by 0.09** — opposite of what semble's bench shows. CSN-Python's queries (as CoIR re-hosts the dataset) are full Python function sources, and the relevant document for each query is the docstring extracted from that same function. Because the docstring lives inside the function source as a literal substring (the function's own `"""..."""` block), any lexical retriever with identifier-aware tokenization wins — BM25 has the answer string as input. ken's α=0.5 RRF fusion then drags the hybrid number down by averaging in the weaker semantic ranking. Not a ken bug; it's a structural artifact of how CoIR reframed CodeSearchNet for retrieval, and doesn't generalize to natural NL-to-code distributions. Detailed empirical findings and the comparison to potion-code-16M's published aggregate are in [`docs/BENCH.md`](docs/BENCH.md#external-benchmark--coir-csn-python).

## Roadmap

The full risk register with explicit triggers is in [docs/DESIGN.md §10](docs/DESIGN.md#10-risk-register). Highlights:

- **NDCG vs semble — measured at v0.1.0 / v0.2.0**: hybrid 0.842 (regex) and 0.838 (treesitter) vs semble's 0.854. The ~0.012 gap is **not primarily chunker-driven** — v0.2.0's tree-sitter chunker trades per-language wins and losses without closing the gap (see [docs/BENCH.md](docs/BENCH.md) "v0.2.0 empirical findings"). The algorithm port itself is validated by the semantic-raw match within 0.003.
- **Tree-sitter chunker (Option A)** — landed in v0.2.0 via [`gotreesitter`](https://github.com/odvcencio/gotreesitter) as opt-in (`--chunker=treesitter`). Default stays `regex`. Per-language guidance in ["Choosing a chunker"](#choosing-a-chunker).
- **Chroma chunker (Option B)** — broader language coverage via a token-stream lexer. Trigger: a polyglot repo where neither chunker covers a needed language. Not currently triggered.
- **Class-body-aware Python chunking** — currently top-level only; large Django models / SQLAlchemy bases line-split through methods. Trigger: Python NDCG visibly below the other languages (not currently triggered).
- **~~Incremental indexing~~ — landed in v0.3.** `ken-mcp` watches the repo file tree and republishes a snapshot 2s after any edit, so an agent querying its own working tree sees its own edits without a restart. `ken index --watch` (default) keeps the CLI alive in a similar role; `ken index --no-watch` restores the v0.2 build-and-exit behavior. Tombstones for deletes, no compaction — memory grows monotonically with cumulative edit volume, which is fine for typical agent-session lifetimes; compaction is a v0.3.x trigger if multi-day sessions hit pressure. Atomic-snapshot reads keep query latency unchanged from v0.2. Implementation: [`internal/search/watch.go`](internal/search/watch.go), design rationale in [`docs/DECISIONS.md` ADR-012](docs/DECISIONS.md#adr-012-incremental-indexing-via-fsnotify--atomic-snapshot-swap).
- **Token-budget recall — agent-side efficiency vs grep+Read.** Measured at v0.3.0; ken surfaces the qrel target chunk in ~44× fewer tokens than the tokenized-grep baseline at K=10 on semble's NL queries (82% recall vs 99%), and in ~10,000× fewer tokens on the 280K-file CoIR-CSN-Python corpus (91% vs 100% recall). Grep wins on recall completeness; ken wins decisively on agent-input cost. See [`docs/BENCH.md` "Token-budget recall"](docs/BENCH.md#token-budget-recall--agent-side-efficiency).

## How this was built

ken is a port. The retrieval algorithm is verbatim from [MinishLab/semble](https://github.com/MinishLab/semble) (Python). The Go implementation was written by Claude under a fixed set of constraints: pure Go / no cgo, algorithm constants ported verbatim never tuned, original source wins whenever Claude's reconstruction of an algorithm detail diverges from semble's live code.

That last rule caught five material errors during the rerank-pipeline port (see [docs/DESIGN.md §7](docs/DESIGN.md#7-hybrid-retrieval--rerank)) — each one a confident-sounding hallucination of an algorithm detail that turned out to be wrong when checked against the Python source. The discipline of always checking is human-supplied.

Benchmark numbers in the [Comparison table](#comparison-to-semble) are measured against semble's own harness using its native NDCG@10 metric, not synthesized — reproducible via [`docs/BENCH.md`](docs/BENCH.md). The 11k-input tokenizer parity test ([`scripts/parity_dump.py`](scripts/parity_dump.py) + [`internal/embed/parity_test.go`](internal/embed/parity_test.go)) was a human call — "the 18-case spot-check isn't enough" — and surfaced three real bugs the spot-check missed.

The ADR-style record of every architectural decision (alternatives considered, consequences) lives in [`docs/DECISIONS.md`](docs/DECISIONS.md).

## Acknowledgments

ken stands on MinishLab's shoulders. The retrieval algorithm, the model, the entire approach to embedding-table-driven code search — all theirs.

- **[semble](https://github.com/MinishLab/semble)** — the original Python implementation. ken's retrieval pipeline is a verbatim port; constants and pipeline order come straight from `search.py` and `ranking/*.py`. © Thomas van Dongen, MIT.
- **[model2vec](https://github.com/MinishLab/model2vec)** — the static-embedding library whose three-tensor format ken implements. © Thomas van Dongen, MIT.
- **[potion-code-16M](https://huggingface.co/minishlab/potion-code-16M)** — model weights, distilled from `nomic-ai/CodeRankEmbed` (MIT) which is itself initialized from `Snowflake/snowflake-arctic-embed-m-long` (Apache-2.0). © Minish Lab. Redistributed per [`NOTICE`](NOTICE).

## License

ken is [MIT-licensed](LICENSE). It bundles attribution for the redistributed model weights and their upstream lineage in [`NOTICE`](NOTICE), and a generated list of Go-module dependency licenses in [`THIRD_PARTY_LICENSES.md`](THIRD_PARTY_LICENSES.md). Every link in the provenance chain is permissive (MIT ∪ Apache-2.0); see [docs/DESIGN.md §6](docs/DESIGN.md#6-license--attribution-chain).

For contributors: see [`CLAUDE.md`](CLAUDE.md) for build / test / formatting conventions and the project's invariants (precision contract, stdout/stderr contract).
