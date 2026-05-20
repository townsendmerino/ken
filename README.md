# ken

**Fast hybrid code search for agents.** Pure Go, single static binary, drop-in MCP-compatible with [MinishLab/semble](https://github.com/MinishLab/semble) — same tool schemas, same output format, same install steps swapped to a Go binary.

[![CI](https://github.com/townsendmerino/ken/actions/workflows/ci.yml/badge.svg)](https://github.com/townsendmerino/ken/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go Reference](https://pkg.go.dev/badge/github.com/townsendmerino/ken.svg)](https://pkg.go.dev/github.com/townsendmerino/ken)
![Go 1.26+](https://img.shields.io/badge/go-1.26%2B-blue)

ken is a Go port of semble. The retrieval algorithm is ported verbatim from semble's `search.py` + `ranking/*.py`; the value ken adds is runtime properties, not retrieval quality: single-binary distribution, no Python interpreter import on cold start, no GIL on the indexing pipeline. If you already use semble in your agent, you can swap to ken-mcp without re-prompting; the wire format is the same string semble emits.

## Quickstart

```bash
# Install both binaries (Go 1.26+).
go install github.com/townsendmerino/ken/cmd/ken@latest
go install github.com/townsendmerino/ken/cmd/ken-mcp@latest

# Download the default Model2Vec model (~64 MB, one-time).
huggingface-cli download minishlab/potion-code-16M \
    tokenizer.json config.json model.safetensors \
    --local-dir ~/.ken/model

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

## Features

- **Pure Go, no cgo.** Single static binary; `GOOS`/`GOARCH` cross-compiles for free; no `libtokenizers.a` to vendor per platform.
- **Drop-in MCP-compatible with semble.** Same `search` / `find_related` tool schemas, same markdown-string output format, install snippets adapted from semble's README.
- **Algorithm verbatim from semble.** BM25 + Model2Vec semantic + α-weighted RRF fusion + code-aware rerank (definition / embedded-symbol / file-coherence / stem-match boosts) + path penalties + file-saturation decay. See [docs/DESIGN.md §7](docs/DESIGN.md#7-hybrid-retrieval--rerank).
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
| `KEN_MCP_CHUNKER` | `regex` | `regex` / `line`. |
| `KEN_MCP_CACHE_SIZE` | `16` | LRU bound on the repo→Index cache. |
| `KEN_MCP_LOG_LEVEL` | `warn` | `debug` / `info` / `warn` / `error`. All logs go to stderr; **stdout is the JSON-RPC channel** ([details](docs/DESIGN.md#hard-rule--stdoutstderr-contract)). |

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
| NDCG@10 on semble's benchmark | 0.854 ([semble README](https://github.com/MinishLab/semble#benchmarks)) | pending† |
| MCP server | yes | yes — drop-in compatible (same tool schemas, same wire format) |
| Binary size | n/a (Python env) | `ken` 3.9 MB · `ken-mcp` 16 MB |
| Requires `huggingface-cli` for model | yes | yes (or skip and use `--mode bm25`) |

† **NDCG vs semble's benchmark is deferred** — the benchmark corpus isn't publicly downloadable from semble's repo. Since the algorithm is ported verbatim (with verbatim constants), any future gap would be a measurement question, not an algorithm question. See the [risk register](docs/DESIGN.md#10-risk-register).

semble timings cited above are from semble's own [README "Benchmarks" section](https://github.com/MinishLab/semble#benchmarks); ken's are measured on the included `testdata/repo` polyglot fixture and on a sibling shallow clone of `/tmp/semble`. Cold-start was timed by `/usr/bin/time -p ken search testdata/repo "validate" -k 1 --mode bm25` over three trials (M2 MacBook Air, Go 1.26.3, darwin/amd64 build under Rosetta).

## Roadmap

The full risk register with explicit triggers is in [docs/DESIGN.md §10](docs/DESIGN.md#10-risk-register). Highlights:

- **NDCG vs semble** — gated on benchmark-corpus access. If you're at MinishLab and there's a publishable corpus, file an issue.
- **Chroma chunker (Option B)** — broader language coverage via a token-stream lexer. Trigger: a polyglot repo where the regex chunker doesn't cover a needed language.
- **WASM tree-sitter chunker (Option A)** — highest parity with semble's Chonkie. Trigger: regex chunker NDCG measurably below tree-sitter-based on a benchmark we can run.
- **Class-body-aware Python chunking** — currently top-level only; large Django models / SQLAlchemy bases line-split through methods. Trigger: Python NDCG visibly below the other languages.

## Acknowledgments

ken stands on MinishLab's shoulders. The retrieval algorithm, the model, the entire approach to embedding-table-driven code search — all theirs.

- **[semble](https://github.com/MinishLab/semble)** — the original Python implementation. ken's retrieval pipeline is a verbatim port; constants and pipeline order come straight from `search.py` and `ranking/*.py`. © Thomas van Dongen, MIT.
- **[model2vec](https://github.com/MinishLab/model2vec)** — the static-embedding library whose three-tensor format ken implements. © Thomas van Dongen, MIT.
- **[potion-code-16M](https://huggingface.co/minishlab/potion-code-16M)** — model weights, distilled from `nomic-ai/CodeRankEmbed` (MIT) which is itself initialized from `Snowflake/snowflake-arctic-embed-m-long` (Apache-2.0). © Minish Lab. Redistributed per [`NOTICE`](NOTICE).

## License

ken is [MIT-licensed](LICENSE). It bundles attribution for the redistributed model weights and their upstream lineage in [`NOTICE`](NOTICE), and a generated list of Go-module dependency licenses in [`THIRD_PARTY_LICENSES.md`](THIRD_PARTY_LICENSES.md). Every link in the provenance chain is permissive (MIT ∪ Apache-2.0); see [docs/DESIGN.md §6](docs/DESIGN.md#6-license--attribution-chain).

For contributors: see [`CLAUDE.md`](CLAUDE.md) for build / test / formatting conventions and the project's invariants (precision contract, stdout/stderr contract).
