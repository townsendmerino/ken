# ken — Developers guide

For SDK authors building on `mcp.Run`, operators tuning the rerank
pipeline, and anyone shipping ken-embedded-corpus binaries to
their users.

If you're a casual agent user wanting to install ken-mcp, see
[USERS.md](USERS.md) instead.

## Contents

- [mcp.Run library — embedded corpora](#mcprun-library)
- [Prebuilt indices (ADR-024)](#prebuilt-indices)
- [Public API surface](#public-api-surface)
- [JSON output mode](#json-output-mode)
- [Custom chunkers](#custom-chunkers)
- [Tuning rerank](#tuning-rerank)
- [Performance expectations](#performance-expectations)
- [Internals + contributing](#internals--contributing)

## mcp.Run library

Ship your SDK's docs as a **single static MCP binary**. Write ~20
lines of Go, `//go:embed` your corpus and the Model2Vec model,
`go build`, push to GitHub releases. Users `brew install`, add
one line to their agent config, and their coding agent has
high-quality local retrieval over your docs. No backend, no
vector DB, no "is the cache stale" question — the binary IS the
corpus, version-pinned by build artifact.

### Canonical pattern

```go
package main

import (
    "context"
    "embed"
    "io/fs"
    "log"
    "os"

    "github.com/townsendmerino/ken/mcp"

    _ "github.com/townsendmerino/aikit/chunk/markdown" // register markdown
    _ "github.com/townsendmerino/aikit/chunk/regex"    // register regex
)

//go:embed all:docs
var docsCorpus embed.FS

//go:embed all:model
var modelDir embed.FS

func main() {
    sub, err := fs.Sub(docsCorpus, "docs")
    if err != nil {
        log.Fatal(err)
    }
    msub, err := fs.Sub(modelDir, "model")
    if err != nil {
        log.Fatal(err)
    }
    // The corpus FS is the second positional argument; Options carries
    // the rest. ModelFS wins over ModelDir (ModelDir is ignored when set).
    if err := mcp.Run(context.Background(), sub, mcp.Options{
        ModelFS:     msub,
        Mode:        "hybrid",
        ChunkerName: "markdown",
        LogWriter:   os.Stderr,
    }); err != nil {
        log.Fatal(err)
    }
}
```

That's the full server. `mcp.Run` blocks on stdio, handling
JSON-RPC, the SDK transport, and graceful shutdown. The `mcp.Run`
path registers the **`search`, `find_related`, and `status`** tools
(plus `reindex_db` when `Options.DB` is set) — the embedded-corpus
surface. The full nine-tool surface including the structural tools
(`definition` / `references` / `callers` / `outline` / `symbols` /
`recently_changed`) is the `cmd/ken-mcp` / `mcp.NewServer` path, which
indexes a live checkout. The embedded model in `model/` is `~60 MB`;
binary lands at roughly that plus the corpus.

### When to use vs not

**Use mcp.Run** when:

- You're shipping a single-purpose docs server (an SDK's docs,
  an internal handbook, a small corpus you control). Version-
  pinned distribution + zero ops.
- You want the user install to be one binary + one config line.

**Don't use mcp.Run** when:

- The corpus is the user's local repo (use `ken-mcp` directly;
  it indexes on-demand with file-watching).
- The corpus is large (>50 MB compressed). Binary size scales
  with the corpus + model; >100 MB binaries are awkward.
- You need DB introspection. The DB path needs the live
  `ken-mcp` binary's `KEN_DB_DSN` wiring, not embedded.

## Prebuilt indices

For embedded-corpus servers, ADR-024 lets you ship a **prebuilt
search index alongside the corpus** to skip the cold-build cost
on first query.

```bash
# Build the index once at release time
ken build-index /path/to/corpus -o /path/to/corpus/.ken/index.bin \
    --mode hybrid --chunker markdown --model /path/to/model
```

Then `//go:embed all:corpus/.ken` alongside the corpus. `mcp.Run`
auto-detects `.ken/index.bin` and loads it instead of indexing.

**Format**: `KEN1` magic + per-chunk records + CRC32 trailer. See
[reference-prebuilt-indices](../scripts/dogfood_structural.go)
for the operational reference.

**Caveat**: prebuilt indices carry the model reference (model
fingerprint + dimensionality). If your build-time model differs
from the runtime model, the index will refuse to load with a
scope-mismatch error. Use the same `--model` path at build and
ship.

## Public API surface

ken's exported packages and their 1.0 stability commitment. **Audit
closed 2026-06-03** (see [road-to-1.0 §4](internal/road-to-1.0.md#4-strategic--positioning-items)):
every public symbol crossing a package boundary has a tier here, plus
a `Stability:` doc-comment on the function/type itself. Three
internal-type leaks through the mcp package surface
(`Config.Mode` → `search.Mode`, `Config.TelemetryLog` →
`search.Telemetry`, `FormatResults([]search.Result)`) were resolved
by adding [`mcp/api_aliases.go`](../mcp/api_aliases.go) — 1.0-stable
`mcp.Mode` / `mcp.Telemetry` / `mcp.Result` type aliases so SDK
authors never need to import `internal/search`.

### ken's own packages

**`github.com/townsendmerino/ken/mcp`** — Hard, 1.0-committed:

- `Run(ctx, fsys, Options) error` — the embedded-corpus server entry.
- `Options{Corpus, ModelFS, ModelDir, Mode, ChunkerName,
  CacheSize, LogLevel, LogWriter, DB}` — the configuration
  struct.
- `NewServer(Config) *sdk.Server` — the on-demand server entry
  (cmd/ken-mcp uses this).
- `Config{Cache, DefaultRepo, Mode, Chunker, Instructions,
  ServerName, ServerVersion, DB, TelemetryLog,
  TelemetryInResponse, UsageRecorder}` — server configuration.
- `Mode`, `ModeBM25`, `ModeSemantic`, `ModeHybrid`,
  `ModeHybridRerank` — search-pipeline mode + enum values
  (1.0-stable type aliases in [`api_aliases.go`](../mcp/api_aliases.go)
  for `internal/search`'s `Mode`).
- `Telemetry` — per-query timing struct surfaced by
  `Config.TelemetryLog` (alias for `search.Telemetry`).
- `Result` — single search hit returned by ken's hybrid pipeline
  (alias for `search.Result`); consumed by `FormatResults`.
- `Cache`, `NewCache(max, builder)`, `RepoBundle{Index,
  Structural}`, `Builder` — repo cache used by tools.
- `Cache.{Get, GetBundle, Len, Capacity, Close}` — cache
  read/write surface.
- `DBIntegration` interface, `ReindexResult` — DB tier-2 integration.
- `UsageRecorder` interface — savings-store hook.
- All `*Args` types (`SearchArgs`, `DefinitionArgs`, etc.).
- All `*Response` JSON types
  ([mcp/json_responses.go](../mcp/json_responses.go)).
- `FormatResults(header, []Result) string` — semble-compatible
  markdown shape. (Signature accepts the `mcp.Result` alias as of
  the v0.9.x API audit; runtime behaviour unchanged.)
- `Logger`, `LogLevel`, `NewLogger`, `ParseLogLevel`,
  `LogLevelNames` — logging seam used by `Options.LogWriter`
  consumers.
- `DefaultTopK`, `DefaultCacheSize`,
  `DefaultRecentlyChangedCommits`, `MaxRecentlyChangedCommits` —
  default constants; values may tune across minors but the
  symbols stay.
- `ErrPrivateCloneTarget` — typed error for SSRF-blocked clone
  targets; callers may check via `errors.Is`.

**`github.com/townsendmerino/ken/mcp`** — Best-effort (signatures
stable, semantics may evolve between minors):

- `CloneShallow(ctx, url)` — go-git shallow clone with the
  SSRF guard. Useful for custom `Builder` implementations; the
  temp-dir scheme + allow-list may shift.
- `NormalizeKey(source)` — cache key normalization (URL vs
  local path). Same caveat — useful for custom Builders, may
  evolve if the source-key scheme grows.
- `ValidateEnum(name, raw, allowed, fallback, lg)` — env-var
  validation helper. Internal to ken-mcp's own config parsing;
  external consumers should not depend on it.

**`github.com/townsendmerino/ken/mcp/db`** — Hard, 1.0-committed:

- `Setup(ctx, Config) (DBIntegration, func(), error)` — wire a
  DB introspection layer for the `reindex_db` tool.
- `Config{DSN, Engine, Schemas, ExcludeSchemas, Listen, …}` —
  DB integration options.

### ken's `internal/*`

All `internal/` packages are **not public**. They're stable
across `ken-mcp` releases but you should not import them from
external code — they may break without ADR.

### aikit packages

ken consumes aikit (
[github.com/townsendmerino/aikit](https://github.com/townsendmerino/aikit))
as a separate module — `require github.com/townsendmerino/aikit
v1.4.0` + `aikit/chunk/treesitter v1.0.0` in [go.mod](../go.mod).
**aikit is at 1.0**, so its hard-tier algorithm packages (`topk`,
`ann`, `bm25`, `embed`, `encoder`, `fuse`, `chunk` + subpackages) are
1.0-committed — ken 1.0's own stability promise composes cleanly on top
of them. ken's CHANGELOG records every aikit bump; the v0.4.1 → v1.0.0
pin (2026-06-06) was validated byte-identical (full suite + build-parity
+ the `grammar_subset` drift guard green), so it changed no behavior.

The full aikit stability table is in
[aikit/README.md](https://github.com/townsendmerino/aikit#stability).
The summary:

- **Hard, 1.0-committed**: `topk` · `ann.New/Flat/Hit` · `bm25` ·
  `embed` · `encoder.Load/Encoder` · `chunk.Chunker/Chunk/...` ·
  `fuse.RRFWeighted` (consolidated onto from `internal/search`'s
  hand-rolled RRF in v0.9.x).
- **Best-effort**: concrete chunker structs (use
  `chunk.Get("regex")` instead), `chunk/treesitter` (depends on
  pre-1.0 gotreesitter), `encoder.LoadQ8`, HNSW internals,
  `decoder` + `tokenizer` (v0.2.0 generative-half packages — explicitly
  best-effort under aikit's own README stability section).

ken pins aikit via `require`; bumping the minor follows ken's
own release rhythm.

## JSON output mode

All nine MCP tools accept `output: "json"` for a typed
JSON response instead of markdown. The response shapes are
defined in [mcp/json_responses.go](../mcp/json_responses.go) and
are part of the 1.0-stable surface.

### Response types

- `SearchResponse{Query, Mode, Results[], Filter?}` — `search`.
- `FindRelatedResponse{Anchor, Results[]}` — `find_related`.
- `DefinitionResponse{Symbol, Definitions[]}` — `definition`.
- `ReferencesResponse{Symbol, References[], Totals}` — `references`.
- `CallersResponse{Symbol, Files[]}` — `callers`.
- `OutlineResponse{Path, Entries[], ByFile?}` — `outline`.
- `SymbolsResponse{PathPrefix, Symbols[]}` — `symbols`.
- `RecentlyChangedResponse{PathPrefix?, Considered, Commits[]}` —
  `recently_changed` (each commit: hash, short_hash, subject,
  author_name/email, when (RFC3339), changed_files).
- The `status` tool also accepts `output: "json"` and returns
  the full `status.Status` struct.

### Behavior

- Default (`output` empty or `"markdown"`): markdown response,
  unchanged from semble's wire format.
- `output: "json"`: indented JSON, deterministic field order
  (Go struct tags).
- `output: "yaml"` (or any other value): friendly error rather
  than silent fallback. Agents that mis-spell `"jsom"` see the
  typo, not the wrong format.

### Adding fields

Adding a new field to a response struct is non-breaking; renaming
or removing one is a breaking change requiring an ADR.

## Custom chunkers

A **chunker** turns a file's bytes into a slice of `chunk.Chunk`
records. ken ships three: `regex` (default), `treesitter` (opt-in
per ADR-010/011), `line` (universal fallback).

### The Chunker interface (ADR-032, 1.0-stable)

```go
// aikit/chunk/chunker.go
type Chunker interface {
    Chunk(path string, data []byte) ([]Chunk, error)
    Name() string
    Languages() []Language
}

type Chunk struct {
    File      string
    StartLine int    // 1-indexed inclusive
    EndLine   int    // 1-indexed inclusive
    Text      string // exact source slice for [StartLine, EndLine]
}
```

The **byte-fidelity invariant** is part of the interface
contract: concatenating every chunk's `Text` reproduces the
source. ken's Arm B enrichment (ADR-035) intentionally violates
this at the indexer layer — the chunker itself still satisfies it
on raw source.

### Registering a new chunker

Chunkers self-register via blank-import. The pattern (from
`aikit/chunk/regex/init.go`):

```go
func init() {
    chunk.Register(&Chunker{ /* … */ })
}
```

To use a custom chunker:

```go
import (
    "github.com/townsendmerino/aikit/chunk"
    _ "your/module/chunker/foo" // registers "foo"
)

// In mcp.Run:
mcp.Options{
    ChunkerName: "foo",
    // ...
}
```

The Languages() method advertises which extensions the chunker
prefers. Ken's mode auto-selects per-file when ChunkerName is
unset, but explicit ChunkerName overrides per-corpus.

## Tuning rerank

The neural reranker is opt-in (`KEN_MCP_RERANK=on`). When enabled,
it re-scores the top-N hybrid candidates using CodeRankEmbed,
blending the neural cosine with the hybrid score. Lift on the
public bench: +0.165 NDCG@10 on CoIR-CSN-Python.

### When to enable

- **Default off**: most agent queries don't need it; hybrid is
  already strong.
- **Turn it on when**: queries are ambiguous (multiple plausible
  matches, agent picking wrong), or the corpus has high name
  collision (many `Login` definitions across files). The +30 ms
  per-query cost is usually worth it.

### Knobs

| Variable | Default | What it does |
|---|---|---|
| `KEN_MCP_RERANK_MODEL_DIR` | `~/.ken/rerank-model` | CodeRankEmbed snapshot. `ken download-model --rerank` fetches it. |
| `KEN_MCP_RERANK_TOP_N` | `50` | How many hybrid candidates to rerank. Higher = better quality but slower. 50 is the M0-validated sweet spot. |
| `KEN_MCP_RERANK_BETA` | `0.25` | Blend weight. `0` = pure hybrid (no rerank effect); `1` = pure neural (regresses on semble's bench). `0.25` is the M0-validated default. |
| `KEN_MCP_RERANK_QUANT` | `int8` | As of aikit v1.5.0 `int8` reaches `f32` latency parity (matmulBTQ8 dequant-then-SIMD fix) at ~140 MB resident vs ~547 MB and ~21× less runtime scratch, cosine 0.997 vs f32 — so it's the default. Pass `f32` for the full-precision path. |
| `KEN_MCP_RERANK_ADAPTIVE` | empty | `THRESHOLD:MINN` (e.g. `0.30:10`). When stage-1 is confident, rerank only the top MINN. 2-5× win on the typical workload. |
| `KEN_MCP_RERANK_CACHE_SIZE` | `32768` | LRU bound for the per-process rerank cache (doc-embedding entries). |
| `KEN_MCP_RERANK_CACHE` | `~/.ken/rerank-cache-<quant>.bin` | Persistent cache path. Empty string disables persistence. |

### M9 persistent cache

The reranker maintains an LRU of `(query, candidate) → cosine`
results. Persisted across restarts at
`~/.ken/rerank-cache-{f32,int8}.bin`. 40× warm-cache speedup
empirically (M9 memo). The cache is scope-keyed by
`<model_name>:<quant>:<embed_dim>` — switching models or quants
gets a clean cold cache; the disk file stays untouched until the
next compatible run saves over it.

### Lazy load (M2)

As of [ADR-036](internal/DECISIONS.md#adr-036), the rerank model loads on
the **first hybrid+rerank query**, not at startup. ken-mcp
startup with `KEN_MCP_RERANK=on` is now indistinguishable from
unset (~30 ms median). The 491 ms encoder.Load wall moves to the
first query that actually engages the reranker. If you boot
ken-mcp with rerank on but issue only hybrid queries, the cost
is never paid.

## Performance expectations

Measured on M-series Mac, darwin/arm64, Go 1.26.3, GOMAXPROCS=8.
Numbers from [perf-startup-m0-baselines.md](
internal/results/perf-startup-m0-baselines.md) +
[perf-startup-m4-results.md](internal/results/perf-startup-m4-results.md)
(post-M2 + M4).

### Cold-start budget (first servable query)

| Corpus | Files | Chunks | Cold-start total | Dominant cost |
|---|---:|---:|---:|---|
| tiny (testdata/repo) | 6 | 6 | ~134 ms | embed.Load (~53 ms) + chunk/embed |
| medium (ken itself) | 250 | 1,667 | ~555 ms | search.FromFS (chunk + embed) |
| large (jekyll, 1k files) | 766 | 1,865 | ~1,309 ms | search.FromFS + structural.Build (parallelized) |

### Query latency (warm, sub-millisecond on all sizes)

| Corpus | p50 | p95 | p99 |
|---|---:|---:|---:|
| tiny | 0.27 ms | 0.36 ms | 0.60 ms |
| medium | 0.78 ms | 3.36 ms | 14.17 ms |
| large | 0.92 ms | 1.07 ms | 1.15 ms |

The medium p99 of 14 ms is an outlier (GC pause or scheduler);
typical responses are well under a millisecond. Per-query telemetry
is exposed via `KEN_MCP_RERANK_TELEMETRY=on` for end-of-response
diagnostics.

### Indexing throughput

- Per-file chunk + embed cost is parallelized across NumCPU
  workers (ADR-030).
- structural.Build is parallelized as of M4 (ADR-036): 3.5× on
  jekyll, 4.5× on ken itself.
- Sequential 14.7k-file CSN benchmark indexes in ~12 s on this
  hardware. Scales linearly with file count and chunk count.

### Memory

- Embedding model: ~60 MB resident.
- Rerank model (f32): ~547 MB resident. Q8 variant: ~140 MB.
- Per-corpus index: ~8 KB per chunk (BM25 posting + embed vec).
- Repo cache: bounded by `KEN_MCP_CACHE_SIZE` (default 16 repos);
  each repo's working set is its index + structural + rerank
  cache LRU.

### Token savings

Reported by the `status` tool. ken returns ~40× fewer tokens
than `grep + Read` for the same recall (see
[BENCH.md](BENCH.md#token-budget-recall--agent-side-efficiency)
for methodology).

## Internals + contributing

- **[DESIGN.md](DESIGN.md)** — architecture spec: pipeline,
  fusion algorithm, embed implementation, license chain.
- **[DECISIONS.md](internal/DECISIONS.md)** — every ADR. Read these
  before proposing structural changes — most paths have already
  been explored.
- **[BENCH.md](BENCH.md)** — NDCG@10 / token-budget bench
  methodology. Every quality claim ships with a reproducible
  command.
- **[PERF.md](internal/PERF.md)** — performance discipline. Same
  reproducibility rule for perf claims.
- **[road-to-1.0.md](internal/road-to-1.0.md)** — current state of every
  1.0 ship-list item, plus closed/killed/deferred reasons.
- **[Issues](https://github.com/townsendmerino/ken/issues)** —
  bug tracker. The maintainer (and Claude Code) read these.

### Pull requests

The repo's CI is `golangci-lint v2.11.4` + `go vet` +
`gofmt -l cmd internal mcp bench` (must print nothing) + the full
test suite. Local pre-flight: `go test ./... && go vet ./... &&
golangci-lint run ./...` — all three should be clean before pushing.

### Adding a chunker, extractor, or MCP tool

- **Chunker**: implement aikit's `chunk.Chunker` interface,
  register in `init()`. Per ADR-032 the interface is the 1.0
  surface; the concrete struct is best-effort.
- **Structural extractor** (new language): full step-by-step
  walkthrough in [add-a-language.md](internal/add-a-language.md) — AST
  probing via `debug_ast_test.go`, writing the extractor,
  registering in `kenLangToTSLang` + `langExtractor` maps,
  fixture tests, dogfood validation against a real repo,
  precision-sample check. The existing extractors covering ken's
  thirteen languages are the canonical templates.
- **MCP tool**: define `*Args` + `*Response` in
  `mcp/types.go` + `mcp/json_responses.go`. Add handler in
  `mcp/*.go`. Register via `sdk.AddTool` in `mcp/server.go`
  AND `mcp/run.go` if it should land in embedded-corpus
  servers too. Add to tool-count tests.

### License

MIT. See [LICENSE](../LICENSE) and
[THIRD_PARTY_LICENSES.md](../THIRD_PARTY_LICENSES.md) for upstream
attributions.
