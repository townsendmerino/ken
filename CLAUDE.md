# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`ken` is a **pure-Go, no-cgo** port of [MinishLab/semble](https://github.com/MinishLab/semble), a hybrid code-search tool (lexical BM25 + Model2Vec semantic embeddings + RRF fusion + rerank heuristics). The authoritative design — algorithm spec, precision contracts, license chain, risk register — lives in **[`docs/DESIGN.md`](docs/DESIGN.md)**; read it before any non-trivial change. This is **not** a git repository.

## Repository ownership (read this first)

**Claude Code is the sole editor of this repository.** A second Claude instance ("the claude app") is used for design/planning discussion only and has been instructed by the user not to modify files here. Implications:

- There is **no lane partition**. `internal/embed/` (Stage 3) was originally scaffolded via the other instance but is now maintained here like every other package — fix it directly; the old "report, don't cross lanes" rule no longer applies.
- `docs/DESIGN.md` is the shared design doc and may be discussed conceptually with the other instance, but all file edits (code and `docs/DESIGN.md`) happen here.
- Keep the whole tree `gofmt`-clean and `go test ./...` green on every change.

## Commands

```bash
go build ./...                                   # build everything
go test ./...                                    # all tests
go test ./internal/bm25/ -run TestBM25_IDF -v    # a single test (regexp on name)
go vet ./...                                      # must be clean
gofmt -l cmd internal                             # must print nothing (whole tree gofmt-clean)
go fix ./...                                      # Go 1.26 modernizers (SplitSeq, min, range-int)

go run ./cmd/ken index  <path>  [--chunker=regex|line] [--mode=bm25|semantic|hybrid] [--model=DIR]
go run ./cmd/ken search <path> <query>...  [-k N] [--chunker=...] [--mode=...] [--model=...]
go run ./cmd/ken-mcp                               # stdio MCP server (env-configured; see MCP section)
```

Default mode is **hybrid** (Stage 4). hybrid/semantic need `--model <dir-with-model.safetensors>` (default `testdata/model`); without it the CLI errors clearly. `ken-mcp` instead **downgrades to bm25 with a stderr warning** if the model dir is missing — first-launch usability for agents.

Toolchain: `go.mod` pins `go 1.26.3` + `toolchain go1.26.3`. With `GOTOOLCHAIN=auto` (default) an older system Go auto-downloads 1.26.3. Deps are now: `golang.org/x/text` (embed normalizer), `github.com/go-git/go-git/v5` (ken-mcp clone), `github.com/modelcontextprotocol/go-sdk` (ken-mcp), `golang.org/x/sync` (singleflight). After a `go`/`toolchain` bump, run `go mod tidy`.

### Golden fixture workflow

`pin_inference.py` is the Python reference harness. It downloads `minishlab/potion-code-16M`, verifies the pooling algorithm, and writes `ken_golden.json`. Regenerate and install with:

```bash
.venv/bin/python scripts/pin_inference.py && cp ken_golden.json testdata/golden.json
```

**Gotcha:** Python's `json.dumps` emits bare `NaN` (invalid JSON; Go's `encoding/json` rejects the *entire file*). The script already sanitizes non-finite → `null` and emits `ground_truth: null` for zero-norm degenerate rows (empty string, all-`[UNK]`). Keep `allow_nan=False` on the dump so any regression fails loudly. Never hand-edit the fixture; regenerate it.

The full embedding-parity tests in `internal/embed` **skip** unless the model is present at `testdata/model/` (not committed; per-machine — see `testdata/README.md`). A green `go test` with those skipped is expected on a fresh checkout.

## Architecture

The pipeline (semble parity target): **walk → chunk → {BM25 lexical | Model2Vec semantic} → α-weighted RRF fuse → file-coherence + query boosts → path penalties + saturation**. The algorithm spec for each piece is in [`docs/DESIGN.md`](docs/DESIGN.md) §§2–7.

- **`internal/repo`** — gitignore-respecting filesystem walk; prunes `.git`, skips binary (NUL sniff) and oversized files. The matcher is a deliberate common-subset of gitignore; full pathspec parity is a later dependency swap (`docs/DESIGN.md` §1).
- **`internal/chunk`** — `Chunk` is the stable unit every stage depends on. Stage 1 ships only the 50-line/5-overlap fallback (`lines.go`). The runtime-selectable `Chunker` interface + registry and per-language regex chunkers are Stage 2; the fallback exists now to validate that seam early.
- **`internal/bm25`** — identifier-aware tokenizer (camelCase/PascalCase/ACRONYM/digit splits, plus the whole lowercased run for recall) feeding a Lucene-variant BM25 index (`k1=1.5`, `b=0.75`, non-negative IDF) — pinned to bm25s defaults so ranking can be diffed against semble's `SearchMode.BM25`.
- **`internal/embed`** — Model2Vec inference. The safetensors blob has **three** tensors (`embeddings` F32, `mapping` I64, `weights` F64). Inference is `normalize(Σ embeddings[mapping[id]]·weights[id] / Σ weights[id])`. Two non-negotiable invariants (see `docs/DESIGN.md` §4): always index through `mapping[]`, and **accumulate in float64** (float32 silently fails ≥1−1e-5 cosine on longer inputs). Empty / all-`[UNK]` → zero vector, not NaN.
- **`internal/ann/flat.go`** — flat brute-force cosine retriever over the dense matrix. HNSW behind this same `Hit/Query` shape later; flat is exact and fine at repo scale.
- **`internal/search`** + **`cmd/ken`** — orchestration. `FromPath(root, mode, chunker, modelDir)` builds the BM25 index always and additionally embeds every chunk under semantic/hybrid. `Mode = ModeBM25 | ModeSemantic | ModeHybrid`. The retrieval pipeline (`hybrid.go` + `rerank.go` + `penalties.go` + `adaptive.go`) is ported **verbatim from /tmp/semble** (`search.py`, `ranking/{boosting,penalties,weighting}.py`, `tokens.py`); see [`docs/DESIGN.md`](docs/DESIGN.md) §7 for the as-built constants (α-weighted fusion, 3 penalty tiers, file-saturation decay, etc.).
- **`mcp/` + `cmd/ken-mcp`** (Stage 5) — MCP server on top of `github.com/modelcontextprotocol/go-sdk`. Two tools (`search`, `find_related`) with arg schemas ported verbatim from `/tmp/semble/src/semble/mcp.py`; the return format is the same markdown string semble emits via `_format_results`. Per-process repo→Index cache (singleflight dedup + LRU); http(s) URLs shallow-clone via go-git to `$TMPDIR/ken-mcp/<sha256>/`. See "MCP server" section below for install + env vars.

## MCP server

`ken-mcp` is a drop-in replacement for semble's MCP server. Same tool surface (`search`, `find_related`), same wire format, so agents already trained against semble work unchanged.

### **Hard rule — stdout/stderr contract**
stdin and stdout **are** the JSON-RPC channel. ANY write to stdout outside of the SDK's protocol writer corrupts the stream and the agent disconnects with a cryptic JSON-decode error. This is the #1 way new MCP servers fail. `cmd/ken-mcp/main.go` has the full contract at the top — read it before adding ANY dependency, and audit each new dep for default writers pointed at stdout. `TestBinary_StdoutIsCleanJSONRPC` builds the real binary and drives an MCP session through `sdk.CommandTransport` to enforce this; if it fails, you've polluted stdout.

### Env vars (configure ken-mcp at startup)
- `KEN_MCP_DEFAULT_REPO` — optional pre-indexed source; tools may then be called without `repo`.
- `KEN_MCP_MODE` — `bm25`/`semantic`/`hybrid` (default `hybrid`; auto-downgrades to bm25 with a stderr warning if the model is unreachable).
- `KEN_MCP_MODEL_DIR` — Model2Vec snapshot dir (must contain `model.safetensors`). Empty ⇒ bm25-only.
- `KEN_MCP_CHUNKER` — `regex`/`line` (default `regex`).
- `KEN_MCP_CACHE_SIZE` — LRU bound (default 16).
- `KEN_MCP_LOG_LEVEL` — `debug`/`info`/`warn`/`error` (default `warn`); all logs go to stderr.

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
- **Deps land with the stage that needs them** — Stage 1 was stdlib-only; the embed normalizer pulled `golang.org/x/text`; ken-mcp pulled `go-sdk`, `go-git`, `x/sync`. HNSW and Chroma/tree-sitter chunkers are still ahead per the `docs/DESIGN.md` dependency table.
- **Validate-against-Python before advancing** — every stage's correctness is defined as parity with semble/`StaticModel.encode()` on the same corpus, not just "looks reasonable". The tokenizer's real acceptance test (100k-input parity dump vs `transformers.AutoTokenizer`) is still owed and is the main Stage-3 risk; the 18-case `golden.json` is a spot-check, not that harness.
