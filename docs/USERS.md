# ken — Users guide

Hybrid (BM25 + Model2Vec) code search over your repo, served as
an MCP server so your coding agent can call it the same way it
calls grep — but it returns ~40× fewer tokens per result, and it
handles "what's the auth flow in this codebase?" cleanly where
grep can't.

This guide is for **agent users**: install ken-mcp, point your
agent at it, and use the tools. If you're building on top of ken
(embedding a corpus, writing a custom chunker), see
[DEVELOPERS.md](DEVELOPERS.md).

## 5-minute install

Pick the agent you use. All five paths get you the same `ken`
server with all nine tools registered.

### Claude Code

```bash
claude mcp add ken -s user -- /absolute/path/to/ken-mcp
```

That's it. Reload Claude Code; `ken-mcp` should show up in the
MCP servers list. The first time an agent asks for a search, ken
will index the repo and cache it for the session.

### Cursor

`~/.cursor/mcp.json` (global) or `.cursor/mcp.json` (per-project):

```json
{ "mcpServers": { "ken": { "command": "/absolute/path/to/ken-mcp" } } }
```

### Codex

`~/.codex/config.toml`:

```toml
[mcp_servers.ken]
command = "/absolute/path/to/ken-mcp"
```

### OpenCode

`~/.opencode/config.json`:

```json
{ "mcp": { "ken": { "type": "local", "command": ["/absolute/path/to/ken-mcp"] } } }
```

### VS Code (with MCP)

`.vscode/mcp.json`:

```json
{ "servers": { "ken": { "command": "/absolute/path/to/ken-mcp" } } }
```

### Get the binary

Options:

1. **Package manager** (installs both `ken` and `ken-mcp`):
   - macOS / Linux: `brew install --cask townsendmerino/tap/ken`
   - Windows: `scoop bucket add townsendmerino https://github.com/townsendmerino/scoop-bucket` then `scoop install ken`
2. **Pre-built release**: download from
   [github.com/townsendmerino/ken/releases](https://github.com/townsendmerino/ken/releases)
   — macOS / Linux (`.tar.gz`) and Windows (`.zip`), amd64/arm64.
3. **Go**: `go install github.com/townsendmerino/ken/cmd/ken-mcp@latest`
   (and `.../cmd/ken@latest`).
4. **Demo binaries** (Go-stdlib / kubernetes / postgres, pre-indexed): see
   [releases/demos/v0.2.0](https://github.com/townsendmerino/ken/releases/tag/demos/v0.2.0).

### Get the embedding model

Most ken features need the Model2Vec embedding model
(`potion-code-16M`, ~60 MB). **ken-mcp fetches it automatically in
the background on first run** when it's missing — serving BM25-only
until it lands (a few seconds, one-time), then upgrading to hybrid.
Set `KEN_MCP_AUTO_FETCH=0` to disable that. The CLI (`ken search`)
does not auto-fetch; run `ken download-model` once to pre-seed it
(or pass `--mode bm25`):

```bash
ken download-model
```

This puts it under `~/.ken/model/`. ken-mcp finds it from there
by default; override with `KEN_MCP_MODEL_DIR=/path/to/model`.

If you want the optional neural reranker (better quality for
ambiguous queries, ~547 MB): `ken download-model --rerank`,
then enable with `KEN_MCP_RERANK=on`.

## First query

Once installed, just ask your agent something like:

> Where do we handle user authentication in this codebase?

A well-prompted agent should call ken's `search` tool with
`query: "user authentication"`, get a list of ranked code chunks
back, and answer from those. You can also be explicit:

> Use ken to find the authentication code, then read the top result.

The response shape is markdown by default — same format
[MinishLab/semble](https://github.com/MinishLab/semble) emits, so
agents trained against semble work unchanged.

## When to use ken vs grep

Use **ken** for:

- **Conceptual queries**: "where do we handle auth?" / "find the
  retry logic for HTTP requests" / "what's the database
  connection setup?"
- **Locating definitions and references**: "where is `Login`
  defined?" / "which files call `verify_token`?" — the
  `definition`, `references`, and `callers` tools resolve these
  more cleanly than text-grep can.
- **Exploring an unfamiliar codebase**: `outline` and `symbols`
  give a structural overview without you reading every file.
- **When the agent will read the result anyway** — ken's snippets
  cost ~40× fewer tokens than `grep + Read` at near-parity recall
  (~0.97 vs ~0.999 in default hybrid mode).

Use **grep** (or `rg`, `ag`, etc.) for:

- **Exhaustive enumeration**: refactors, pre-rename audits, "find
  EVERY place that uses this string" — grep guarantees 100%
  recall on literal matches; ken optimizes for relevance and
  reaches ~97% recall at top-10 in its default hybrid mode (0.967
  NL / 0.995 symbol on semble's benchmark). Without the embedding
  model installed it falls back to BM25-only at ~82–91% — run
  `ken download-model` once to stay on the default path. grep
  remains the right tool when you need every match. See
  [`docs/BENCH.md`](BENCH.md#default-mode-hybrid-recall--the-number-that-matters)
  "Default-mode (hybrid) recall."
- **Literal-string searches**: SQL fragments, error message text,
  config keys you know verbatim.
- **Speed-critical scripted searches**: `rg foo` returns in
  ~50 ms; ken's first query against a fresh repo includes index
  build time (1-3 s for medium / large repos — see
  [DEVELOPERS.md → Performance expectations](DEVELOPERS.md#performance-expectations)).

Rule of thumb: if the question is "find chunks that answer this
question," ken wins. If it's "find all instances of X," grep
wins.

## The nine MCP tools

Quick reference. Pass `output: "json"` to any of these for a
structured response instead of markdown (covered tool-by-tool in
[DEVELOPERS.md → JSON output mode](DEVELOPERS.md#json-output-mode)).

### `search`
Hybrid code search. **The main tool.**

- Args: `query` (required), `repo`, `mode` (`bm25`/`semantic`/`hybrid`),
  `top_k`, `languages`, `path_contains`, `exclude_path_contains`,
  `output`.
- Returns: ranked snippets with file, line range, score, text.
- Example: `search(query: "rate limit middleware",
  languages: ["go"], path_contains: "internal")`.

### `find_related`
Semantic-similarity to a specific location. Use after a `search`
result to find more code that looks like it.

- Args: `file_path`, `line`, `repo`, `top_k`, `output`.
- Returns: ranked snippets similar to the chunk at the anchor
  location.

### `definition`
Find where a symbol is defined. Tree-sitter-grade, name-resolved
(NOT type-resolved — same-spelled names in different files all
return).

- Args: `symbol` (e.g. `"Login"` or `"User.Login"`), `repo`,
  `output`.
- Returns: file + kind (`function` / `class` / `method`) for each
  site.

### `references`
Find every place a name is used: call sites, imports, raises.

- Args: `symbol`, `repo`, `output`.
- Returns: per-file list of reference kinds.

### `callers`
Find files that contain a call to a given function. File-level
granularity (100% precision on the Stage 8 Gate 2 sample).

- Args: `symbol`, `repo`, `output`.
- Returns: list of file paths.

### `outline`
Structural outline of a file or directory: top-level functions,
classes, methods, with parameters.

- Args: `path` (file or directory), `repo`, `output`.
- Returns: structured outline; for directories, one section per
  indexed file.

### `symbols`
List every top-level symbol (function or class) in the repo.

- Args: `path` (optional prefix filter), `repo`, `output`.
- Returns: flat list of names.

### `status`
Report ken's current state: build identity, model availability,
enrichment state, **token savings summary**.

- Args: `repo` (optional, for live index info), `verbose`,
  `output`.
- Returns: markdown overview by default. Pass `repo` for the
  live index file/chunk counts on that repo.

### `recently_changed`
Last N commits with the files each touched. Git-aware (uses
go-git on the local working tree).

- Args: `n` (default 10, max 100), `repo` (local path), `path`
  prefix filter, `output`.
- Returns: per-commit markdown with hash, author, time, message,
  and changed file list.

## Configuration

The env vars you'll actually touch:

| Variable | Default | What it controls |
|---|---|---|
| `KEN_MCP_DEFAULT_REPO` | empty | Pre-indexed source. Tools work without `repo` arg when set. |
| `KEN_MCP_MODE` | `hybrid` | Retrieval mode: `bm25`/`semantic`/`hybrid`. Serves `bm25` while the model is missing — fetched on first run by default (`KEN_MCP_AUTO_FETCH`). |
| `KEN_MCP_MODEL_DIR` | `~/.ken/model` | Model2Vec snapshot dir. Empty ⇒ bm25-only. |
| `KEN_MCP_CHUNKER` | `regex` | Chunker: `regex`/`treesitter`/`line`/`markdown`. |
| `KEN_MCP_CACHE_SIZE` | `16` | Repo LRU bound. `0` disables caching. |
| `KEN_MCP_LOG_LEVEL` | `warn` | `debug`/`info`/`warn`/`error`. Logs go to stderr. |
| `KEN_ENRICH` | (enabled) | Arm B structural enrichment. Set to `off` to disable. |
| `KEN_MCP_RERANK` | `off` | Opt into the neural reranker (better quality, +~30 ms per query when warm). |

Most users set `KEN_MCP_DEFAULT_REPO` to their main project and
leave everything else alone. The neural reranker is worth turning
on for ambiguous queries against larger codebases; see
[DEVELOPERS.md → Tuning rerank](DEVELOPERS.md#tuning-rerank) for
the `KEN_MCP_RERANK_*` knobs.

## Troubleshooting

### "No model at ~/.ken/model — downgrading to bm25"

Run `ken download-model` to fetch it. ~60 MB; one-time. After it
lands, restart ken-mcp. This is the single biggest retrieval-quality
lever you control: the BM25-only fallback caps around 82–91%
recall@10, while the default hybrid mode (model present) reaches
~97% (0.967 NL / 0.995 symbol) — see
[`docs/BENCH.md`](BENCH.md#default-mode-hybrid-recall--the-number-that-matters).

### "ken returns no results / weird results"

Try the `status` tool first:

```
status
```

This reports: build identity, models loaded, enrichment state,
token-saving stats. If `embedding: missing` shows up, you don't
have the model.

If the model is fine, try:
- Looser query terms (concepts, not literal strings)
- `mode: "bm25"` for keyword-heavy queries
- Higher `top_k` (default 5; try 20)
- Drop filters: `languages`, `path_contains`, `exclude_path_contains`

### "ken returns stale results after I edited a file"

ken-mcp watches files via `fsnotify` and re-indexes ~2 s after
changes settle. If results lag for more than a couple of
seconds:

- `status(repo: "...", verbose: true)` reports the last build time
- Restart ken-mcp if the watcher is stuck (rare; report a bug)

### "ken-mcp won't start / agent can't reach it"

Check stderr — it'll print one line per stage. Common gotchas:

- **Binary path is relative**: MCP configs need ABSOLUTE paths.
  `~/bin/ken-mcp` doesn't expand; use `$HOME/bin/ken-mcp` (or the
  full literal path).
- **Stale model dir**: `KEN_MCP_MODEL_DIR` points somewhere that
  no longer has a `model.safetensors`. Either `ken
  download-model` or unset the variable.
- **Tools missing from `tools/list`**: you're probably looking at
  an old ken-mcp release; check `status` for the version, upgrade
  to the latest. The 9 tools listed above are all 1.0-stable.

### "Search is fast on second query but slow on first"

Expected. First query against a fresh repo includes index build
time:

- Tiny repo (≤10 files): ~150 ms
- Medium repo (~250 files): ~600 ms
- Large repo (~1000 files): ~1.3 s

After that, queries are sub-millisecond. The numbers depend on
mode + model availability; see
[DEVELOPERS.md → Performance expectations](DEVELOPERS.md#performance-expectations).

### "Where are my token savings?"

The `status` tool's "Token savings" section reports it. Daily /
7-day / all-time bucketed, with a `~tokens` estimate (chars ÷ 4).
Stored at `~/.ken/savings.jsonl` — no query text, just counts;
opt out with `KEN_NO_USAGE_STATS=1`. CLI variant:
`ken savings --verbose`.

## Where to next

- **You want to embed a corpus in your own binary** (think:
  shipping a single-file MCP server with your SDK's docs
  pre-indexed) → [DEVELOPERS.md → mcp.Run library](DEVELOPERS.md#mcprun-library).
- **You want to tune the rerank pipeline** for your workload →
  [DEVELOPERS.md → Tuning rerank](DEVELOPERS.md#tuning-rerank).
- **You want to understand the algorithm** (BM25 + semantic +
  RRF + boosts + penalties + rerank) → [DESIGN.md](DESIGN.md).
- **You want to see the benchmark methodology** → [BENCH.md](BENCH.md).
- **You found a bug** →
  [github.com/townsendmerino/ken/issues](https://github.com/townsendmerino/ken/issues).
