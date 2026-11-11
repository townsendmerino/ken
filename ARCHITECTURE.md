# Architecture

**Current-state map of the system.** This doc answers "what is ken *now* and where does each concern live." For *why* it's built this way, see [docs/DESIGN.md](docs/DESIGN.md) (algorithm spec, precision contracts, written stage-by-stage during the port) and [docs/internal/DECISIONS.md](docs/internal/DECISIONS.md) (ADRs). When this doc and DESIGN.md §1 disagree about layout, this doc wins — DESIGN.md predates the aikit extraction (ADR-034).

Last verified against the code: 2026-11-10 (main after v1.13.0). The package map below is drawn from `go list`; [`internal/buildchecks/importrules_test.go`](internal/buildchecks/importrules_test.go) holds the same graph as a table and fails when the code and the table differ, so update both together.

## Bird's eye

ken is a hybrid code-search engine (BM25 lexical + Model2Vec semantic + RRF fusion + code-aware rerank, ported verbatim from [semble](https://github.com/MinishLab/semble)) packaged four ways: a CLI (`ken`), a multi-repo MCP server (`ken-mcp`), an in-process library ([`engine`](engine/), which `ken-mcp` and [tack](https://github.com/townsendmerino/tack) build on), and an embedded-corpus server (`mcp.Run`) for shipping a corpus + model + index as one static binary. Everything is pure Go, no cgo.

## Two-module map

The generic retrieval primitives live in a separate module, **[aikit](https://github.com/townsendmerino/aikit)** (extracted in ADR-034). ken composes them and owns everything code-search-specific. Dependency direction is strictly ken → aikit; aikit knows nothing about ken, MCP, or git.

```mermaid
graph LR
    subgraph aikit ["aikit (generic retrieval primitives)"]
        chunk["chunk — Chunk type + Chunker iface<br/>regex · line · markdown · treesitter"]
        retr["bm25 · embed · fuse · ann · topk · linalg"]
        encoder["encoder — neural reranker model"]
    end
    gts["gotreesitter (parser + grammars)"]
    subgraph programs ["programs"]
        ken["cmd/ken"]
        kenmcp["cmd/ken-mcp"]
        embedded["cmd/ken-mcp-docs · demos/*"]
    end
    subgraph public ["public library packages"]
        engine["engine — in-process library face<br/>Open · Workspace · Hit"]
        mcppkg["mcp — MCP server lib<br/>Run · NewServer · Cache · clone"]
        mcpdb["mcp/db — Tier-2 DB opt-in"]
    end
    subgraph internal ["internal/"]
        search["search<br/>Index · WatchedIndex · rerank pipeline"]
        structural["structural<br/>tree-sitter extraction · Arm B enrichment"]
        repo["repo — gitignore walk"]
        sqlpkg["sql — .sql folding"]
        dbpkg["db — live DB introspection"]
        status["status — doctor/status report"]
        envcfg["envcfg — every KEN_* env read + parsing"]
        mcplog["mcplog — stderr logger"]
        small["leaves: binfmt · bytesize · usage ·<br/>modelfetch · embedcache · perf"]
    end
    ken --> engine & status & envcfg
    ken -. "bench / perf only<br/>(cmd/ken/internal/measure)" .-> search
    kenmcp --> engine & mcppkg & mcpdb & search & structural & envcfg
    embedded --> mcppkg
    engine --> search & structural & repo
    mcppkg --> search & structural & status & mcplog & envcfg
    mcpdb --> mcppkg & dbpkg
    envcfg --> mcplog & repo
    search --> structural & repo & sqlpkg
    structural --> repo
    search --> chunk & retr & encoder
    structural --> gts
    sqlpkg & dbpkg --> chunk
```

Arrows are imports and point down the layers: programs → public library packages → `internal/` → aikit. Nothing under `internal/` imports `engine` or `mcp`, and `mcp` does not import `engine` (ADR-048: `cmd/ken-mcp` adapts a `Workspace` to `mcp.StructuralSource` instead). The diagram leaves out the leaf packages' incoming edges (`search → binfmt`, `envcfg → bytesize`, `status → usage`, and the programs' own uses of `usage`, `modelfetch`, `embedcache`, `perf`, `bytesize`), the direct aikit imports of the programs and public packages (chunker registration, model loading, the `chunk.Chunk` type), and `bench/` and `tools/`; the table in the import-rules test has every edge.

What moved to aikit and what stayed: tokenizer, BM25, embedding inference, fusion, ANN, and the chunkers are **generic** → aikit. The rerank heuristics ([`rerank.go`](internal/search/rerank.go), [`penalties.go`](internal/search/penalties.go), [`adaptive.go`](internal/search/adaptive.go)) are **semble-specific** and stay in `internal/search` under the verbatim-port precision contract.

## Four deployment shapes

| Shape | Entry point | Corpus | Watching | Cache |
|---|---|---|---|---|
| CLI | [`cmd/ken`](cmd/ken/main.go) | one local path | `index --watch` (default) | none |
| Multi-repo MCP server | [`cmd/ken-mcp`](cmd/ken-mcp/main.go) → [`mcp.NewServer`](mcp/server.go) | local paths + `https://` clones per request | always | LRU + singleflight ([`mcp/cache.go`](mcp/cache.go)) |
| In-process library | [`engine.Open`](engine/engine.go) → `Workspace` (tack; `cmd/ken-mcp` builds each local repo through it) | one local path | `Options.Watch` | none — optional snapshot (`Options.Snapshot`, ADR-039) |
| Embedded corpus | [`mcp.Run`](mcp/run.go) (SDK authors) | one fixed `fs.FS` (typically `//go:embed`) | never | none — optional prebuilt index (`.ken/index.bin`, ADR-024) |

`mcp.Run` deliberately carries none of the multi-repo machinery; `cmd/ken-mcp` deliberately doesn't embed. The [`mcp`](mcp/) package must stay DB-free — `mcp/db` is the opt-in bridge, enforced by [`mcp/binary_contract_test.go`](mcp/binary_contract_test.go).

## Runtime view: ken-mcp

The concurrency model is **immutable snapshots behind atomic pointers** — no RWMutex on any query path (ADR-012). An `*Index` is never mutated after construction; writers build a replacement off to the side and `atomic.Pointer.Store` it; readers do exactly one `Load()` at query entry and use that snapshot for the whole call.

```mermaid
flowchart TD
    agent["MCP client (agent)"] <-->|"JSON-RPC over stdio<br/>(stdout is sacred)"| sdk["go-sdk server loop"]
    sdk --> handlers["tool handlers<br/>search · find_related · 7 structural · status · reindex_db*"]
    handlers --> cache["mcp.Cache (repo → WatchedIndex)<br/>LRU eviction · singleflight build dedup"]
    cache -->|local path| wi["WatchedIndex"]
    cache -->|https URL| clone["shallow clone via go-git<br/>SSRF guard (clone.go)"] --> wi
    subgraph wi_inner ["WatchedIndex (one per cached repo)"]
        fsn["fsnotify events"] --> deb["debouncer goroutine<br/>2 s debounce → rebuild changed files<br/>→ compact → Store(newIx)"]
        deb -->|atomic.Pointer.Store| snap["*search.Index snapshot<br/>(immutable)"]
    end
    handlers -->|"one Load() per query"| snap
    fetch["auto-fetch goroutine (ADR-037)<br/>no model? download potion-code-16M,<br/>purge cache → next query rebuilds hybrid"] -.-> cache
    refresher["mcp/db Refresher (Tier-2, optional)<br/>introspect → LISTEN/NOTIFY / poll<br/>→ WithExtraChunks → swap"] -.-> snap
    wi_inner ~~~ fetch
```

\* `reindex_db` registers only when a DB is wired, so `tools/list` stays honest.

Long-lived goroutines and who owns them: the SDK stdio loop (process lifetime); one fsnotify + one debouncer goroutine per `WatchedIndex` (owned by the Cache, shut down on eviction/close — goleak-tested); the one-shot model auto-fetch ([`cmd/ken-mcp/main.go`](cmd/ken-mcp/main.go)); the DB refresher ([`mcp/db`](mcp/db/)). The same snapshot-swap pattern recurs at the `mcp.Run` layer for DB extras: `baseIx.WithExtraChunks(extras)` rebuilds against the *original* snapshot (extras replace, never accumulate — [`mcp/run.go`](mcp/run.go)).

The startup model-resolution ladder (env validation in [`internal/envcfg/`](internal/envcfg/)): explicit dir → `~/.ken/model` → auto-fetch in background, serving bm25 meanwhile. Search reads `ix.Mode()` per query, so the bm25→hybrid upgrade is invisible to clients.

**Cold start (ADR-039, the v1.3 campaign — [`engine/snapshot.go`](engine/snapshot.go)).** ken-mcp no longer rebuilds from scratch every launch. It persists the built index to `<repo>/.ken/snapshot.{bin,manifest}` and, on restart, loads + drift-scans (config-key ⊕ per-file mtime/size) instead of rebuilding when the repo is unchanged, reconciling only changed files otherwise (`KEN_MCP_SNAPSHOT`, default on; distinct from the ADR-024 operator prebuilt `.ken/index.bin`, which still wins). Three opt-in serve-before-warm modes move first-query latency off a true-cold build: `KEN_MCP_STAGED` (serve BM25 instantly, embed in the background, upgrade to hybrid — responses carry `"semantic":"warming"`), `KEN_MCP_LAZY_ENRICH` (defer the Arm B tree-sitter parse), and `KEN_MCP_EMBED_CACHE` (persistent `sha256(chunk)→vector` cache so a rebuild re-embeds only new text). A per-file parse budget guards pathological files ([`cmd/ken-mcp/enrich_budget.go`](cmd/ken-mcp/enrich_budget.go)); the long-lived server also applies GC hygiene — `GOGC`/`GOMEMLIMIT` tuning + `FreeOSMemory` after builds/flushes ([`cmd/ken-mcp/gc.go`](cmd/ken-mcp/gc.go)), all binary-layer only so `internal/search` and aikit stay allocation-policy-free.

## Data flow

**Index build** ([`internal/search/index.go`](internal/search/index.go)):

```
internal/repo walk (gitignore, binary/oversize skip, fs.FS canonical)
  → aikit/chunk (regex default; per-file routing by extension; markdown for docs)
  → internal/structural Arm B enrichment (ADR-035): prepend "# func: … | calls: … | raises: …"
  → aikit/bm25 BM25 postings  +  aikit/embed Model2Vec vectors (hybrid/semantic)
  → optional extras: internal/sql (.sql folding) and internal/db (live schema) chunks
```

**Query** ([`internal/search/hybrid.go`](internal/search/hybrid.go) → [`rerank.go`](internal/search/rerank.go) → [`penalties.go`](internal/search/penalties.go)):

```
query → adaptive classifier (symbol vs NL → α = 0.3 / 0.5)
  → BM25 top-N  +  flat cosine top-N      (candidateCount = topK × 5)
  → α-weighted RRF fuse (aikit/fuse)
  → boosts: file-coherence · definition · embedded-symbol · stem
  → penalties: 3 tiers (test/compat/.d.ts paths) + file-saturation decay
  → top-k  → optional neural rerank (aikit/encoder, lazy-loaded, KEN_MCP_RERANK)
```

Every constant and the pipeline order are pinned to semble's Python — see DESIGN.md §7 and the divergence notes at the top of each file.

## Invariants (the things you must not break)

1. **stdout/stderr contract** — stdout is the JSON-RPC channel; any stray write corrupts the stream. All logging goes to stderr. Enforced by `TestBinary_StdoutIsCleanJSONRPC` ([`cmd/ken-mcp/main_test.go`](cmd/ken-mcp/main_test.go)). Audit every new dependency for default stdout writers.
2. **Snapshot immutability** — an `*Index` is never mutated after construction. New state = new Index + atomic swap. If you're reaching for a mutex on a query path, you're fighting the design.
3. **Verbatim-port precision contract** — rerank/fusion constants come from semble's source and are never tuned. Changes are validated by parity (golden fixtures, the 11,447-input tokenizer harness under `-tags=parity`), not by "looks better."
4. **Float64 accumulation + `mapping[]` indirection** in embedding inference (DESIGN.md §4) — float32 accumulation silently breaks cosine parity on long inputs; zero-norm inputs yield zero vectors, never NaN.
5. **Byte fidelity** — concatenating `Chunk.Text` for a file reproduces the source bytes, across all chunkers.
6. **`mcp` stays DB-free** — no pgx/sqlite/mysql in `mcp`'s import graph ([`binary_contract_test.go`](mcp/binary_contract_test.go)); DB support enters only via `mcp/db`.
7. **Import rules** — every package's allowed ken imports are a table in [`internal/buildchecks/importrules_test.go`](internal/buildchecks/importrules_test.go), checked against `go list`. A new edge needs a table change in the same PR; an `internal/` package importing `engine` or `mcp` points the wrong way.
8. **Environment reads stay at the edge** (ADR-053) — `engine` and `internal/*` (apart from `internal/envcfg`) never call `os.Getenv`; they take options. The edges are the programs, `internal/envcfg`, the `mcp` server package ([`mcp/env.go`](mcp/env.go) lists what it reads) and the bench harnesses. Enforced by `TestEnvReadsStayAtTheEdge` in [`internal/buildchecks/envread_test.go`](internal/buildchecks/envread_test.go).
9. **Pure Go, no cgo** — the single-static-binary posture is the product. Native deps go behind interfaces with pure-Go implementations or don't go in.

## Stability tiers

**1.0-stable:** `mcp.Run` + `mcp.Options`, the `engine` package apart from its escape hatches (the full list is in [DEVELOPERS.md](docs/DEVELOPERS.md#public-api-surface)), the MCP tool wire format (semble-compatible), `aikit/chunk`'s `Chunker` interface, the documented env vars (as read by the `ken` and `ken-mcp` binaries and by servers built on `mcp`; `engine` takes `Options`, not env). **Best-effort:** concrete chunkers (treesitter especially — gotreesitter-backed), `mcp.NewServer`/`Cache` internals, and engine's escape hatches: the `engine.Index`, `engine.WatchedIndex` and `engine.StructuralIndex` aliases and the `Workspace.WatchedIndex`, `Workspace.Structural` and `Workspace.StructuralIfBuilt` accessors that return them. An alias carries every exported method of its target, so these expose `internal/search`'s and `internal/structural`'s methods, which change without notice. **No promises:** everything under `internal/`, including the methods those aliases expose.

## Where to look

| Concern | Location |
|---|---|
| Library face (in-process) | [`engine/`](engine/) (`engine.go`, `search.go`, `structural.go`, `snapshot.go`, `refresh.go`; ADR-046/047/048) |
| Retrieval pipeline + rerank | [`internal/search/`](internal/search/) (`hybrid.go`, `rerank.go`, `penalties.go`, `adaptive.go`) |
| Index build, serialization, extras | [`internal/search/index.go`](internal/search/index.go), [`index_serialize.go`](internal/search/index_serialize.go) |
| Watch mode | [`internal/search/watch.go`](internal/search/watch.go) (ADR-012) |
| Cold start: snapshots, serve-before-warm, parse budget, GC | [`engine/snapshot.go`](engine/snapshot.go) (ADR-039); [`cmd/ken-mcp/`](cmd/ken-mcp/) (`enrich_budget.go`, `gc.go`); env: `KEN_MCP_SNAPSHOT` / `KEN_MCP_STAGED` / `KEN_MCP_LAZY_ENRICH` / `KEN_MCP_EMBED_CACHE` |
| Walker / gitignore | [`internal/repo/`](internal/repo/) |
| Structural extraction + enrichment + structural MCP tools | [`internal/structural/`](internal/structural/), [`mcp/structural_tools.go`](mcp/structural_tools.go) |
| DB indexing (static SQL / live introspection; Postgres · MySQL · MariaDB · SQLite) | [`internal/sql/`](internal/sql/), [`internal/db/`](internal/db/), [`mcp/db/`](mcp/db/), [docs/db-indexing.md](docs/db-indexing.md) |
| MCP server, cache, clone, SSRF guard | [`mcp/`](mcp/) (`server.go`, `cache.go`, `clone.go`, `run.go`) |
| Binary wiring, env validation, auto-fetch | [`cmd/ken-mcp/`](cmd/ken-mcp/) (`main.go`) + [`internal/envcfg/`](internal/envcfg/) |
| Logger (`mcp.Logger` is an alias of it) | [`internal/mcplog/`](internal/mcplog/) |
| Layering guard | [`internal/buildchecks/importrules_test.go`](internal/buildchecks/importrules_test.go) |
| Benchmarks | [`bench/ndcg/`](bench/ndcg/), [`bench/tokens/`](bench/tokens/), [docs/BENCH.md](docs/BENCH.md) |
| Chunkers, BM25, embeddings, fusion, ANN, encoder | external: [aikit](https://github.com/townsendmerino/aikit) |
