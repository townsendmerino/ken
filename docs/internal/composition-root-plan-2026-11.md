# One composition root: design note (phase 0)

**Status:** accepted with the decisions below (Francis, 2026-11-10). Slice 0 and the cmd/ken slices are approved; the mcp and cmd/ken-mcp slices wait for a report after cmd/ken.
**Date:** 2026-11-10. **Tree:** `main` at `87eca6f` (v1.14.0 plus one docs commit).
**From:** the 2026-11-09 architecture audit's "one composition root" item.

## The goal, restated

`engine` becomes the only package outside `internal/` that imports
`internal/search` or `internal/structural`. `mcp`, `cmd/ken` and `cmd/ken-mcp`
work through `*engine.Workspace` and `engine.Options`. Search results, scores,
the MCP wire format, every documented flag and variable, and `mcp`'s public
API stay as they are.

## What the tree says, against the brief

| Brief | Tree |
|---|---|
| Identifier counts at `be9caeb`: engine 31, mcp 18, cmd/ken 39, cmd/ken-mcp 36 | A text count of `search.X` at `be9caeb` gives engine 31, mcp 14, cmd/ken 34, cmd/ken-mcp 32. I could not reproduce 18 / 39 / 36. The type checker's count, which includes methods called on values, is the real coupling and is larger (next table). |
| mcp and cmd/ken do not import engine | True, at both commits. |
| cmd/ken-mcp opens a Workspace and still reaches into `internal/search` | True: 40 identifiers, for the reranker, the rerank cache, pre-built indexes and DB chunks. |
| An index is constructed from five places with four constructors | Four packages (engine, mcp, cmd/ken, cmd/ken-mcp) and seven entry points: `FromFSWithOptions`, `NewWatchedIndexWithOptions`, `BuildAndSerializeIndex`, `FromFSWithModelOptions`, `LoadSerializedIndex` (+ `WrapStatic`), `NewWatchedIndexWithContext`, `NewWatchedIndexReconciled`. |
| cmd/ken-mcp's `main()` is 438 lines | 439 (`cmd/ken-mcp/main.go:354-792`). cmd/ken's is 38; its bulk is in the subcommand functions. |

Identifiers used today, from the type checker (types, functions, constants and
methods; struct fields not counted):

| Package | `internal/search` | `internal/structural` |
|---|--:|--:|
| engine | 50 | 39 |
| mcp | 28 | 26 |
| cmd/ken | 48 | 3 |
| cmd/ken-mcp | 40 | 7 |

`tools/k8-structural` also reaches two `structural.Index` methods through
`Workspace.Structural`.

## 1. Inventory, grouped by purpose

### mcp

| Purpose | `internal/search` | `internal/structural` |
|---|---|---|
| **Public API types** | `Mode` and its four constants, `Result`, `Telemetry` (re-exported in `api_aliases.go`); `WatchedIndex` in `RepoBundle.Index` and `Cache.Get`'s return type | `Index` in `RepoBundle.StructuralBuilder` and `StructuralSource` |
| **Query handlers** (`server.go`) | `WatchedIndex.Load`, `.Warming`; `Index.SearchMode`, `.SearchModeWithTelemetry`, `.FindRelated`, `.Mode`; `ParseMode`, `ModeNames` | `StripLabel` |
| **Structural tools** (`structural_tools.go`) | | `Index.Definition`, `.References`, `.Callers`, `.Outline`, `.Symbols`, `.File`, `.FilesUnderPath`, `.SymbolsInPath`; `NormalizePath`; `OutlineEntry`; `DefinitionKind*`, `ReferenceKind*` |
| **Status tool** | `Index.Chunks`, `.Len`, `.Mode`, `.DenseRetriever`, `.EnrichedChunks` | `Index.Files`; `ParseBudgetSkips`, `ParseBudgetReached` |
| **`mcp.Run` build** (`run.go`) | `FromFSWithModelOptions`, `FSOptions`, `LoadSerializedIndex`, `LoadOptions`, `Index.WithExtraChunks` | `SetParseBudget` |
| **Cache lifecycle** | `WatchedIndex.Close`, `.Root` | |

### cmd/ken

| Purpose | `internal/search` |
|---|---|
| **Index builds** | `FromFSWithOptions` (5 sites: search, bench, index --no-watch, perf ×2), `NewWatchedIndexWithOptions` (3: index, index --write-snapshot, perf watch), `FSOptions`, `ParseMode`, `Mode*` |
| **Queries** | `Index.Search`, `.SearchMode`, `.SearchModeWithTelemetry`, `.Len`, `.Chunks`; `Result`, `Telemetry` |
| **`ken bench` research knobs** | `Index.SearchModeAlphas`, `.SearchModeAblated`; `AlphaPair`, `AdaptiveAlphas`, `RankAblation`, `IsSymbolQuery` |
| **Reranker** | `NewNeuralReranker`, `Index.SetReranker`, `RerankerOption`, `With{RerankN,RerankBlendBeta,AdaptiveRerankN}`, `NeuralReranker.CacheStats` |
| **Rerank cache file** | `LoadCacheFromFile`, `SaveCacheToFile`, `CacheScopeKey`, `ModelFingerprint`, four `ErrCache*` |
| **Serialized output** | `BuildAndSerializeIndex`, `BuildOptions` (build-index); `WriteSnapshot`, `SnapshotConfigKey`, `SnapshotBinPath` (index --write-snapshot) |
| **`ken doctor` inspection** | `PeekSnapshotChunks`, `PeekRerankCacheCount` |
| **Watch loop** | `WatchedIndex.Close`, `.Len`, `.SetOnFlush`, `.EmbedModel` |

`internal/structural`: `SetParseBudget`, `SetParseBudgetLogf`,
`SetParseBudgetReachedLogf`.

### cmd/ken-mcp

| Purpose | `internal/search` | `internal/structural` |
|---|---|---|
| **Reranker** | `NewNeuralReranker`, `NeuralReranker`, `NewLazyReranker`, `LazyReranker` (+ `.Inner`, `.Loaded`), `Reranker`, `RerankerOption`, `With{RerankN,RerankBlendBeta,AdaptiveRerankN,CacheSize}`, `DefaultRerankerCacheSize`, `WatchedIndex.SetReranker`, `NeuralReranker.CacheStats` | |
| **Rerank cache file** | `LoadCacheFromFile`, `SaveCacheToFile`, `CacheScopeKey`, `ModelFingerprintFromDir`, four `ErrCache*` | |
| **Pre-built index** (`.ken/index.bin`, ADR-024) | `LoadSerializedIndex`, `LoadOptions`, `LoadModelDir`, `WrapStatic`, `ErrModeMismatch`, `ErrChunkerMismatch` | `BuildWithOptions`, `BuildOptions`, `Index`, `Index.Stats` (the frozen bundle's structural builder) |
| **Options plumbing** | `FSOptions`, `Mode`, `ModeBM25`, `ModeNames`, `ParseMode`, `Telemetry` | |
| **DB tier 2** | `WatchedIndex.SetExtraChunks` | |
| **Logging hooks** | `WatchedIndex.SetOnFlush`, `.Len`, `Index.Len` | `SetParseBudget`, `SetParseBudgetLogf`, `SetParseBudgetReachedLogf` |

## 2. How each group is reached through engine

"Stable" means I think it belongs in the 1.0 surface now. Everything else is
added as **best-effort**; the promise can be made later.

| Group | Through engine | Tier |
|---|---|---|
| Watched index over a directory | `Open(ctx, root, Options)`, exists | stable (exists) |
| One-shot build over a directory (`ken search`, `bench`, `index --no-watch`) | `Open` with `Watch: false`. Section 3 has the equivalence that must be proven first. | stable (exists) |
| Index over an `fs.FS` (`mcp.Run`) | new `OpenFS(ctx, fsys, Options)`: never watches, no root, `Refresh` returns an error, no structural index | best-effort |
| Pre-built index (`ken-mcp`'s `.ken/index.bin`, `mcp.Run`'s) | new `Options.Prebuilt []byte` with typed errors `ErrPrebuiltMismatch` (mode or chunker differ: fail) and `ErrPrebuiltUnusable` (corrupt, version: caller falls back to a build) | best-effort |
| Model from an `fs.FS` (`mcp.Options.ModelFS`) | new `Options.ModelFS fs.FS` | best-effort |
| Model ladder, auto-fetch, staged warm-up | ladder: `Options.ModelDir ""` already means `~/.ken/model`; the binaries' own ladder (flag, env, home, testdata) stays in the binaries as option resolution. Auto-fetch and the bm25-then-upgrade swap: section 3, it does not fit cleanly. | open |
| Queries | `Search`, `SearchMode`, `SearchWith`, `FindRelated`, exist. New `SearchOptions.Telemetry *Telemetry` (filled per call). `Telemetry` and `Result`-equivalent data already alias out of engine. | stable (methods), best-effort (the new field) |
| Raw chunk text (the label `ken search --json` prints) | new `Hit.Label string`: the enrichment line `Hit.Text` omits. `Label + Text` is the stored chunk. | best-effort |
| Warming flag, counts, dense store, enriched chunks, parse-budget counts | new fields on `Status` (`Warming`, `Files`, `EnrichedChunks`, `DenseRetriever`, `ParseBudgetSkipped/Reached`, `StructuralFiles` by extension) | best-effort |
| Structural tools | `Definition`, `References`, `Callers`, `Outline`, `Symbols` exist. `mcp` formats from the raw index today, so each tool needs checking against them; where a tool needs more (directory outline, a file's raw entry), a new Workspace method, not the raw index. | stable (exists); additions best-effort |
| Reranker and its cache | new `OpenReranker(RerankerConfig) (*RerankerHandle, error)`: model dir, quantization, cache size and path, lazy load. `Handle.Close()` saves the cache, `Handle.Stats()` replaces `CacheStats`, and the handle is what `SetReranker` takes. One handle is shared by every Workspace, as `ken-mcp` does today. | best-effort |
| `ken bench` research knobs (alpha pairs, ablations, saturation settings) | new `SearchOptions.Tuning *Tuning`, documented as experimental and outside any promise | best-effort, permanently |
| Serialized output | new `BuildIndexBytes(fsys, Options) ([]byte, error)` (build-index, byte-identical by contract); `Options.Snapshot` plus a new `Workspace.WriteSnapshot()` for `index --write-snapshot` | best-effort |
| `ken doctor` inspection | new `InspectSnapshot(root)` and `InspectRerankCache(path)` returning counts | best-effort |
| DB tier 2 | `Workspace.SetExtraChunks` exists and stays the seam. `mcp/db` calls it. engine gains no DB import. | stable (exists) |
| Parse-budget log hooks | new `SetEnrichFileBudgetLog(skip, reached func(path string, d time.Duration))` beside `SetEnrichFileBudget` | best-effort |
| Flush callback, length | `SetOnFlush` exists; length from `Status` | stable (exists) |

Nothing here needs `internal/search` types in a new public signature except
through the aliases engine already exports.

## 3. What does not fit cleanly

### 3.1 mcp's public API is written in internal types

`mcp.RepoBundle.Index` is a `*search.WatchedIndex`. `RepoBundle.StructuralBuilder`
and `StructuralSource` return `*structural.Index`. `Cache.Get` returns
`*search.WatchedIndex`. `mcp.Mode`, `Result` and `Telemetry` alias search types.
All of it is Hard 1.0.

So three of the brief's requirements cannot all hold literally: mcp's public API
unchanged, mcp not importing `internal/search`, and the escape-hatch aliases
having no caller inside ken. A signature has to name those types, and outside
`internal/search` the only names for them are `engine.WatchedIndex`,
`engine.Index` and `engine.StructuralIndex`.

Proposal: mcp's public signatures name the engine aliases (type identity is
unchanged, so no caller breaks), and that is the one remaining use of the
escape-hatch types inside ken. mcp's own code stops calling methods on them:

- `RepoBundle` gains a `Workspace *engine.Workspace` field (best-effort).
  `cmd/ken-mcp`'s builder sets it.
- A bundle from a third-party `Builder` that sets only the old fields is
  wrapped once, when the cache stores it, by a new `engine.WrapIndex(index,
  structuralBuilder)` that returns a Workspace over an index someone else
  built (best-effort, and the mirror image of the escape hatch).
- Every handler then works on a Workspace. `Cache`, its LRU and its
  singleflight builds do not change: they hold bundles and never looked inside
  the index beyond `Close` and `Root`.

Alternative: leave the old-field path on the raw types and move only the
Workspace path. mcp would keep importing `internal/search`, which misses the
goal.

### 3.2 mcp importing engine reverses ADR-048, and registers a chunker

ADR-048 rejected `mcp` importing `engine` because it "changes the dependency
graph `mcp.Run` users link". The end state requires that import, so ADR-048's
rejection has to be superseded on the record.

The concrete effect: `engine` blank-imports `aikit/chunk/markdown`, so every
binary built on `mcp` would register the markdown chunker, and since ADR-056
markdown files route to it when it is registered. `ken-mcp` and
`cmd/ken-mcp-docs` already link it, so they do not change. A third-party
`mcp.Run` or `mcp.NewServer` binary that does not link it would start chunking
its `.md` files by heading instead of by line. That is an output change for
those binaries.

Options: (a) accept it and say so in the CHANGELOG; (b) move the registration
out of engine's package init into `Open`/`OpenFS` behind an option, which
changes nothing for tack only if the option defaults on; (c) have `mcp` pass an
option that tells engine not to route markdown unless the binary registered the
chunker itself. I recommend (c): it keeps every existing binary's output.

engine has no database import and no tokenizer import, so mcp's two binary
contracts (`TestBinary_MCPPackageStaysDBFree`, `StaysTokenizerFree`) keep
holding; the import-rules table will show the new edge.

### 3.3 `mcp.Run` rebuilds differently from a Workspace

`mcp.Run` holds an immutable `*search.Index` and applies DB chunks with
`Index.WithExtraChunks`, rebuilding against the original each time. A Workspace
holds a `WatchedIndex` and applies them with `SetExtraChunks`, which goes
through the watched index's union path. Both replace rather than accumulate,
but they are different code, and the scores have to be shown equal rather than
assumed. A static `WatchedIndex` (the `WrapStatic` kind) also has not been
shown to accept `SetExtraChunks`; I have not checked.

There is no shipped `mcp.Run` binary over a code corpus, so the before/after
comparison needs a test binary (the `embedded-with-db` fixture under
`mcp/db/testdata` is the starting point). If the two paths cannot be made
equal, `OpenFS` keeps an immutable index internally and `SetExtraChunks` on
that shape calls `WithExtraChunks`.

### 3.4 A one-shot build is a different object from a watched one

`ken search` builds a static `search.Index` (`FromFSWithOptions`): it keeps
full-precision vectors and never creates a matcher or a dense store. A
Workspace always builds a `WatchedIndex`. In the #124 comparison the two
returned the same hits and scores in all ten settings, which is encouraging,
but `ken bench`, `ken perf` and `build-index` were not part of that claim.

- `build-index` must stay byte-identical (ADR-040), so `BuildIndexBytes` calls
  the same serializer and does not go through a Workspace at all.
- `ken perf` prints timings of the internal entry points. Through a Workspace
  it would time something else. Either `ken perf` gets engine-level timing
  hooks and its numbers shift once, or it moves under `tools/` as a harness.
  Its output cannot be "unchanged" in any useful sense.
- If `Open` with `Watch: false` turns out not to be output-identical for some
  command, `OpenFS(os.DirFS(root))` is the fallback for one-shot commands.

### 3.5 Where ken-mcp's server assembly lives

The brief wants auto-fetch, staged warm-up, snapshot loading and DB tier 2
"behind options", and `main()` reduced to parsing plus a few calls. Snapshot
loading and staged embedding already are `engine.Options`. The rest is not
per-Workspace:

- **Auto-fetch** downloads the model in the background and then purges the
  multi-repo cache so the next query rebuilds in hybrid. That is a server
  behavior over many Workspaces. Putting the download in engine would also give
  every engine importer a network fetch path and an `internal/modelfetch`
  import.
- **DB tier 2** needs the database drivers. Behind `engine.Options` it would
  link them into tack. Behind `mcp` it would break mcp's DB-free contract.
- **The shared reranker, GC hygiene, the HTTP transport, usage recording** are
  process-level.

So there is a layer above Workspace: "a ken-mcp server", which today is
`main()`. It cannot go under `internal/` (an internal package importing
`engine` and `mcp` points the wrong way in the import rules). Options:

1. **A private package under the program**, `cmd/ken-mcp/internal/server`,
   with `server.Config` filled from flags and environment and `server.Run`.
   `main()` becomes parse, `Config`, `Run`. No public API grows. Recommended.
2. A public `mcp/server` (or `mcp.Serve`) so third parties can run the full
   multi-repo server. It would need DB support injected through `mcp/db`. This
   is new Hard-ish surface nobody has asked for.

### 3.6 Smaller items

- `Hit` costs a SHA-256 per result and, in hybrid, extra single-arm queries to
  fill `BM25Rank`/`DenseRank`. mcp's handlers need neither. A
  `SearchOptions.NoArmRanks` (best-effort) avoids a latency regression in
  `ken-mcp`; without it results are the same but slower.
- The MCP token budget and filters operate on `search.Result` lists. They move
  to `Hit` lists; the text they measure is the same (`Hit.Text` is
  `StripLabel` of the chunk, which is what mcp does today).
- `tools/k8-structural` uses `Workspace.Structural`; it moves to Workspace
  methods in the mcp slice so the escape hatch really has no caller.
- Tests are not covered by the import-rules table and may keep importing
  `internal/search`.

## 4. Slices

The brief's order (cmd/ken, mcp, cmd/ken-mcp) holds, with the engine additions
landing in the slice that first needs them, so each addition arrives with a
caller and a comparison.

| Slice | Contents | Import-rules change |
|---|---|---|
| **0. Baseline** | Commit the before/after comparison harness (today it lives in a scratch directory) and record the semble recall benchmark with the parse budget off. No behavior change. | none |
| **1a. cmd/ken: search, index, build-index, status, doctor** | engine: `SearchOptions.Telemetry`, `Hit.Label`, `Status` additions, `OpenReranker`, `BuildIndexBytes`, `Workspace.WriteSnapshot`, `InspectSnapshot`, `InspectRerankCache`, `SetEnrichFileBudgetLog`. cmd/ken's five commands move. | cmd/ken keeps `internal/search` (bench, perf remain) |
| **1b. cmd/ken: bench and perf** | Both move, unchanged, into one private package under `cmd/ken` that keeps importing `internal/search` (decision 6/7). | cmd/ken drops `internal/search` and `internal/structural` and gains `engine`; the private measurement package is the table's one named exception |
| **2a. mcp: NewServer and Cache** | engine: `WrapIndex`, the structural additions the tools need, `SearchOptions.NoArmRanks`, the markdown option from 3.2. mcp: `RepoBundle.Workspace`, handlers and the three tool files on Workspace, public signatures on engine aliases. ADR superseding ADR-048's rejection. | mcp keeps `internal/search` only for `run.go` |
| **2b. mcp: Run** | engine: `OpenFS`, `Options.Prebuilt`, `Options.ModelFS`. `mcp.Run` moves, with the test binary from 3.3. | mcp drops `internal/search`, `internal/structural`; gains `engine` |
| **3a. cmd/ken-mcp: stop reaching past the Workspace** | Reranker through `OpenReranker`, pre-built index through `Options.Prebuilt`, the frozen bundle's structural builder through the Workspace, DB chunks through `Workspace.SetExtraChunks`. | cmd/ken-mcp drops `internal/search`, `internal/structural` |
| **3b. cmd/ken-mcp: shrink main()** | Move the assembly to the package chosen in 3.5. Pure movement. | per 3.5 |
| **4. Close** | `tools/k8-structural` off the escape hatch; a test that the escape-hatch accessors have no non-test caller in the module; final recall benchmark; ARCHITECTURE.md. | engine is the only importer of `internal/search` and `internal/structural` outside `internal/` |

Each slice carries the comparison (every environment setting, both modes,
search, find_related, the structural tools, status, index, build-index,
snapshot hashes) and stops if an output would change.

## Decisions (Francis, 2026-11-10)

1. **mcp's public types (3.1): yes.** mcp's public signatures name the engine
   alias types, as their one remaining use inside ken, with `engine.WrapIndex`
   for third-party bundles.
2. **Markdown registration (3.2): option (c), as plumbing between mcp and
   engine rather than a public `Options` field, if possible.** It is not
   possible as plumbing alone; see "Open after the decisions" below.
3. **ADR-048: superseded.** A new ADR records why `mcp` importing `engine` is
   now wanted. It lands with slice 2a.
4. **ken-mcp's assembly (3.5): private**, `cmd/ken-mcp/internal/server`.
5. **Auto-fetch and DB tier 2 stay out of `engine.Options`.** tack must not
   link a downloader or database drivers.
6. and 7. **`ken perf` and `ken bench` are measurement harnesses** and get
   neither timing hooks nor a public `Tuning` option. Their implementation
   moves into one private package under `cmd/ken` that may import
   `internal/search`, as the single named exception in the import-rules table.
   Commands, flags and output stay exactly as they are. This replaces section
   2's `SearchOptions.Tuning` row, the perf alternatives in 3.4 and slice 1b's
   contents.
8. **The comparison harness is committed under `tools/`**, in slice 0.

With decision 6/7 the end state reads: `engine` is the only package outside
`internal/` that imports `internal/search` or `internal/structural`, except
the one private measurement package under `cmd/ken`.

### Open after the decisions

**Decision 2 cannot be done as plumbing.** The markdown chunker registers
itself in its package `init`. Any binary that imports `engine` therefore has
it registered before `main` runs, and neither `mcp` nor `internal/search` can
tell "the binary linked it" from "engine linked it". A flag passed from mcp to
engine could only switch markdown routing off for every mcp binary, which
would change `ken-mcp` and `ken-mcp-docs`, or leave it on for all of them,
which is option (a). The routes that do preserve every binary's output:

- **Split engine.** The Workspace implementation moves to an internal package
  that does not import the markdown chunker; `engine` stays the public face,
  aliases the type and keeps the blank import. `mcp` uses the internal package,
  so its signatures still say `*engine.Workspace` (same type) but an mcp binary
  links the chunker only if it imports it. The import-rules table would show
  `mcp` importing that internal package rather than `engine`, and `engine`
  would no longer be the *only* importer of `internal/search`; the internal
  core would be.
- **Change aikit** so the markdown package no longer self-registers. That
  breaks every other user of the package.
- **Accept (a):** third-party mcp binaries that do not link the markdown
  chunker start chunking `.md` files by heading. `ken-mcp` and `ken-mcp-docs`
  are unaffected.

This needs a decision before slice 2a, not before the cmd/ken slices.

## Baseline (slice 0)

Recorded on `main` at `87eca6f`, 2026-11-10, before any slice. Semble set, 63
repos, 1,251 queries, shipped defaults, parse budget off.

| | overall | NL | symbol |
|---|--:|--:|--:|
| BM25-only recall@10 (`TestRecallDecomp_BM25Baseline`) | 0.840 | 0.831 | 0.892 |
| Hybrid recall@10 (`TestRecallDecomp`) | 0.974 | 0.970 | 0.995 |

Hybrid NDCG@10 (`bench/semble/run_ken.py --mode hybrid`): **0.8436**
(0.843619 unrounded).

The before/after comparison is `tools/compare-binaries/compare.py`. `main`
against itself: `ken` identical in 29 of 30 settings plus the timing-dependent
one, `ken-mcp` identical in all 30.

## Progress

- **Slice 0** (PR #130): harness and baseline.
- **Slices 1a and 1b** (one PR; they share `cmd/ken/main.go`): cmd/ken's
  commands on `engine.Workspace`; bench and perf in
  `cmd/ken/internal/measure`; shared flag and reranker helpers in
  `cmd/ken/internal/cli`. cmd/ken no longer imports `internal/search` or
  `internal/structural`. A new guard, `TestOnlyNamedPackagesImportSearch`,
  names every package outside `internal/` that still may.
  - Comparison against `87eca6f`: `ken` identical in 23 of 30 settings plus
    the timing one; the other 6 differ in one line, the snapshot key (below).
    `ken-mcp` identical in all 30. Rerank output identical cold and warm.
  - After: recall lines identical to the baseline; NDCG@10 0.8436 with 0 of
    63 repos differing.
  - **One output change, held for a decision:** `ken index --write-snapshot`
    wrote a key that said "enrichment on, no dense store" whatever the
    settings. Through the Workspace it writes the real key.
  - **Found on the way:** the bench harnesses under `bench/` also import
    `internal/search` (seven packages). The brief's end state did not mention
    them; the guard lists them as harnesses.

- **Harness** (PR #132): scratch-HOME guard, and `runbin`, an `mcp.Run`
  test server over a code corpus.
- **Slice 2a**: mcp's `NewServer` / `Cache` path on `engine.Workspace`
  (ADR-057). mcp no longer imports `internal/structural`; it imports
  `internal/search` only in `run.go`, for `mcp.Run`.
  - Comparison against main: `ken` 29 identical plus the timing case;
    `ken-mcp` 30 identical; `runbin` differs in 25 settings, only in its
    live and DB variants, because importing `engine` registers the markdown
    chunker (decision 2, accepted). Pre-built sections are identical in
    every setting, and main's `runbin` with the chunker linked in is
    identical to the new build.
  - **Found on the way:** hybrid search with DB chunks under `mcp.Run`
    is not perfectly repeatable. About once in 500 sessions a few
    consecutive queries return scores that differ in the fifth digit, on
    main compared with itself. Not traced. The harness retries a differing
    setting once and reports "flaky" if the retry agrees.

## Inferred, not verified

- That `Open` with `Watch: false` matches `FromFSWithOptions` for every
  command: shown for `ken search` in ten settings (#124), not for bench or
  perf.
- That each mcp structural tool can be produced from the existing Workspace
  methods: I compared the method lists, not the output.
- That a static `WatchedIndex` accepts `SetExtraChunks`, and that the union
  path scores the same as `Index.WithExtraChunks`.
- That `Hit`-based handlers reproduce the MCP wire format byte for byte:
  `Hit.Text` is the same stripped text, but the proof is the comparison in
  slice 2a.
- The brief's 18 / 39 / 36 counts: I could not reproduce them and do not know
  how they were counted.
