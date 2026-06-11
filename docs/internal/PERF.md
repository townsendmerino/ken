# Measuring ken's performance numbers

ken's quality story is settled — NDCG@10 lands within the documented ±0.005 of semble (see [`docs/BENCH.md`](../BENCH.md)). The perf story isn't yet: we have no published numbers, no measurement harness in-tree, and no documented methodology. This file is the calibration anchor for that work. It lands **before any number is collected**, so the methodology can't be retrofitted to flatter a conclusion.

The discipline mirrors `docs/BENCH.md`'s shape: every claim ships with a reproducible command, a pinned workload, a machine spec, and a `benchstat`-style median-of-N run. "No metric drift" extends from quality to perf.

## What it measures

Four orthogonal dimensions:

- **Indexing throughput.** Cold-index wall time, watch-mode incremental re-publish latency, allocation totals.
- **Search latency.** Per-query wall time p50 / p95 / p99 over a large random query sample.
- **Memory footprint.** Peak RSS during indexing, steady-state RSS after the index settles.
- **Scale-out.** Does ken complete on a giant corpus (linux kernel, chromium-class)? If not, at what corpus size does it fail and how?

Each dimension is measured across the three retrieval modes (`bm25` / `semantic` / `hybrid`) and the three chunkers (`regex` / `treesitter` / `line`) where the cross-product matters — `--mode bm25 --chunker line` is a different shape than `--mode hybrid --chunker treesitter` and we don't pretend a single number summarizes both.

## Methodology

**Harness.** `ken perf` is a sibling subcommand to `ken bench`. The split is deliberate: `bench` is the NDCG/quality harness (drives `scripts/bench_coir.py`); `perf` is the speed/memory harness (emits one JSON record per workload-run plus optional pprof profiles). Neither harness shares state with the other.

```
ken perf index  <path>  [--mode=...] [--chunker=...] [--cpuprofile=...] [--memprofile=...]
ken perf search <path>  [--queries=FILE] [--n=1000] [--mode=...] [--chunker=...] [--cpuprofile=...]
ken perf watch  <path>  [--edits=N] [--mode=...] [--chunker=...]
```

Each invocation emits a single JSON record on stdout with timing + RSS + alloc fields. `bench_out/<workload>/<date>/` collects records + profiles per run. Long-form analysis happens off-line with `pprof`, `benchstat`, and the JSON records.

**Repeatability.** Every published number is the median of N runs (N=10 for quick metrics; N=3 for expensive end-to-end runs over giant corpora). `benchstat` confirms the variance is below the published claim's significant figure. Numbers that don't survive a re-run on a clean machine don't ship.

**Machine spec.** Every number ships annotated with: machine (e.g., `M3 Pro 12-core / 36 GB`), Go toolchain (`go1.26.3`), build flags (`CGO_ENABLED=0`, `-trimpath -ldflags='-s -w'`), and the exact `ken perf …` invocation. A second machine spec re-runs the same numbers as a sanity check before publication.

**Pinning.** The workload corpus is SHA-pinned (see Workloads below). Future re-runs against the same SHA produce comparable numbers; numbers against a different SHA are documented as a separate measurement.

## Prerequisites

- **`ken` built with race detector OFF and inlining ON.** Race-instrumented and debug builds skew CPU profiles. Use the same build flags goreleaser uses for releases.
- **Go 1.26.3** matching `go.mod`'s `toolchain` directive.
- **`benchstat`** for comparing multiple `go test -bench` runs:
  ```bash
  go install golang.org/x/perf/cmd/benchstat@latest
  ```
- **`pprof`** is bundled with Go; the web UI needs `graphviz` (`brew install graphviz` / `apt install graphviz`).
- **For the medium workload (semble bench corpus):** same setup as `docs/BENCH.md` — semble checkout under `/tmp/semble`, corpus synced via `python benchmarks/sync_repos.py`, model at `~/.ken/model`.
- **For large/giant workloads:** see Workloads below — each documents its own clone/download step.

## Workloads

Four scales, each pinned to a specific revision. Adding a new workload means adding a row here with its SHA pin, expected file count, expected indexing time order-of-magnitude, and a one-line reason it's in the corpus.

| Scale | Corpus | Files (approx) | Pin | Why |
| --- | --- | --- | --- | --- |
| Small | ken itself | ~150 Go | HEAD on `perf-investigation` | Fast iteration; runs on a laptop in seconds; baseline. |
| Medium | semble bench corpus | ~80k files / 63 repos aggregate (~378k chunks indexed via regex) | `repos.json` revisions (upstream-pinned) | Same corpus as `docs/BENCH.md` NDCG runs; perf and quality numbers line up. |
| Large | Linux kernel | ~80k files / ~30M LoC | `v6.10` tag | Real-world large monorepo; multiple languages; common code-search reference. |
| Giant | TBD — chromium or synthetic | ~500k files | TBD | One-time landmark measurement for the "does the architecture hold at this scale?" question; not a routine re-run. |

The giant-scale workload is deliberately marked TBD. Two options under consideration: real chromium clone (truthful but high bandwidth + slow to refresh), or a synthetic corpus generator that produces N files with realistic language mix and known token distribution (cheap and reproducible, loses real-world distribution character). The choice gets documented here before the first giant-scale number lands.

## Run a measurement pass

```bash
# Small workload, all modes, all chunkers — fast smoke run (~30 s):
scripts/perf_collect.sh small

# Medium workload (~5 min):
scripts/perf_collect.sh medium

# Large workload (~20–60 min depending on hardware):
scripts/perf_collect.sh large
```

Each invocation writes one record per (workload × mode × chunker) combination to `bench_out/<workload>/<date>/records.jsonl`, plus CPU/heap/alloc pprof profiles to `bench_out/<workload>/<date>/profiles/`. The script is idempotent — re-running overwrites the day's records.

## Interpreting numbers

A published number is meaningful only if a re-run on a clean machine with the same workload + machine spec lands within the published variance. `benchstat` makes this explicit:

```bash
benchstat bench_out/medium/2026-05-20/raw.txt bench_out/medium/2026-05-26/raw.txt
```

`p < 0.05` means the two runs are statistically different. We don't ship "X% faster" claims that don't pass benchstat at `p < 0.05` against a baseline measured the same week on the same machine.

The acceptance threshold for shipping a perf change is:

1. The change is statistically significant per benchstat (`p < 0.05`).
2. The change holds on a second machine spec (different CPU family / different OS) — guards against single-machine artifacts.
3. NDCG@10 on the semble bench corpus is unchanged within `docs/BENCH.md`'s ±0.005 window. Perf changes that regress retrieval quality don't ship without an explicit calibration-discipline ADR documenting the trade.

## Headline numbers

*This section is empty until the first measurement pass completes. Numbers land here with their machine spec, command, and reproduction recipe inline — no separate appendix that can drift from the headlines.*

## Empirical findings

### Phase 1 (2026-05-26) — investigation pass against small + medium workloads

See [ADR-025](DECISIONS.md#adr-025-perf-campaign-phase-1-investigation-outcome--hotspot-identification-across-small--medium-workloads) for the full hotspot ranking, decision categories, and triggers for follow-on work.

**Caveats** — all numbers below are single-run on a single machine spec (Apple M1 Pro, native arm64 darwin / Go 1.26.3) and **are NOT publishable headlines** under the acceptance threshold in this file. They are investigation evidence; median-of-N + second-machine confirmation are required before any number lands in the "Headline numbers" section above.

**Hotspot summary (from medium-scale CPU + alloc profiles):**

- **ANN flat scan dominates hybrid search at scale.** `ann.Flat.Query` = 78.56% of hybrid-regex search CPU at medium (378,524 chunks). Inside that, the candidate-sort step (`sort.Slice` over all candidate scores to take K=10) is 30.88%. Min-heap-of-size-K refactor landed in v0.8.4 ([ADR-026](DECISIONS.md#adr-026-paired-heap-refactor-for-annflatquery--bm25indextopk-v084)).
- **BM25 search top-K used sort instead of heap.** `bm25.Index.TopK` was 36% of bm25-regex search CPU at medium; all in `sort.Slice` / `pdqsort_func`. Sister refactor to the ANN fix; also landed in v0.8.4.
- **BM25 tokenizer allocates ~45% of all indexing allocations.** `Tokenize.func1` + `camelSplit` + `Tokenize-range1` = 8.7 GB of 19.3 GB cumulative bm25-regex indexing allocations at medium. Per-chunk alloc grows from 35 KB (small) to 53 KB (medium) — not constant overhead; the cost grows with scale. Targeted by v0.8.5 (BM25 tokenizer alloc reduction); carries real NDCG-regression risk per the acceptance threshold.
- **GC dominates CPU at medium scale.** ~50% of indexing CPU and ~36% of search CPU at medium goes to `runtime.gcBgMarkWorker` + `tryDeferToSpanScan` + `scanObject`. Direct consequence of the allocation pattern; reducing allocations (v0.8.5) reduces GC time proportionally.
- **Tree-sitter chunker scales super-linearly.** Per-chunk arena allocation grows 6.4× small→medium (450 KB → 2.9 MB). 1.09 TB cumulative allocation to index 379k chunks via bm25-treesitter at medium. 37-minute wall time. This is an upstream gotreesitter concern, not in-tree fixable; documented in `outputs/treesitter-port-considerations.md`.
- **`filePathPenalty` regex backtracking was a small-scale-only hotspot.** 24% of hybrid search CPU at small (1,560 chunks); <0.5% at medium. Not worth shipping a fix unless a small-corpus user reports actionable pain.

**Predictions vs evidence** (Phase 1 ran with seven written-down predictions in `outputs/perf-investigation-plan.md`):

- Confirmed: embed dominates indexing CPU (#1), BM25 tokenizer alloc-heavy (#2), ANN flat dominates search at scale (#3), file walk not hot (#4), parser pool not hot (#6).
- Falsified: rerank not hot at k=20 (#7) — failed at small scale due to `filePathPenalty`; re-confirmed at medium where ANN-flat overwhelms rerank work.
- New findings: GC dominates CPU at scale; tree-sitter super-linear per-chunk; `bm25.Index.TopK` + `ann.Flat.Query` both used sort-then-slice instead of heap-of-K (both fixed in v0.8.4).

**Reproduction:** `scripts/perf_collect.sh small` for the small workload (~30s), `scripts/perf_collect.sh medium --modes=bm25,hybrid --chunkers=regex,treesitter` for the medium triage (~3 hours wall on M1 Pro native arm64; full cross-product is impractical — see [ADR-025](DECISIONS.md#adr-025-perf-campaign-phase-1-investigation-outcome--hotspot-identification-across-small--medium-workloads) for the methodology adjustment).

### v0.8.x perf campaign closure (2026-05-27) — allocation/GC ceiling reached

After three rolling releases — v0.8.4 paired heap refactor ([ADR-026](DECISIONS.md#adr-026-paired-heap-refactor-for-annflatquery--bm25indextopk-v084)), v0.8.5 BM25 tokenizer byte-lever ([ADR-027](DECISIONS.md#adr-027-bm25-tokenizer-allocation-reduction--rune--byte--syncpool-scratch--lowercase-fast-path-v085)), v0.8.6 BM25 tokenizer count-lever ([ADR-028](DECISIONS.md#adr-028-bm25-tokenizer-parts-slice-pooling-via-tokbuffers-struct-v086)) — a post-v0.8.6 re-profile on a quiet machine surfaced the campaign's honest ceiling. A CPU profile contradicted the object-count profile: although `bm25.lowerString` was 66% of `alloc_objects` post-v0.8.6, the actual GC time was being spent in `typePointersOfUnchecked` / `scanObject` marking live, pointer-dense structures (the `bm25.Build` postings map + chunks slice) — not the transient, pointer-light tokenizer strings. **A `GOGC` probe** (default `100` vs `400`) moved indexing wall time only ~2% (within noise) while inflating heap +77% — proving the 38% GC CPU is not on the wall-time critical path. CPU samples (42.74s) vs wall time (37.73s) gave 1.13× — **indexing uses barely more than one of eight cores**, so GC has 7 idle cores to run on. The wall-time critical path is the serial indexing pipeline, not GC.

See [ADR-029](DECISIONS.md#adr-029-v08x-perf-campaign-capstone--allocationgc-ceiling-reached-indexing-is-single-threaded-parallelism-is-the-next-frontier) for the full closing analysis: the three doors empirically shut (`lowerString` reduction targets the wrong cost, postings-map arena/slab bounded at ~2% wall, `GOGC` default at a bad tradeoff), the scoped successor (indexing-pipeline parallelism, the next campaign), and the resolution of the second-machine-confirmation debt accumulated across ADR-026/027/028 — retired by stopping rather than spent on a fourth single-machine release. **"Headline numbers" deliberately stays empty:** no number in this file met the `docs/internal/PERF.md` acceptance threshold of median-of-N + second-machine confirmation before the campaign closed; investigation-pass evidence lives in the ADRs above, not in Headlines.

### v0.8.7 parallelism release (2026-05-27) — Phase A: per-file worker pool for chunk + embed

The follow-on campaign ADR-029 scoped landed as v0.8.7. The pipeline now parallelizes file-read + chunk + embed via a `runtime.NumCPU()` worker pool over a bounded job channel (`numWorkers * 2`), with file-index-ordered reassembly preserving byte-stable serialization for free. `bm25.Build` + migration folding stay serial — that's exactly what makes the determinism story trivial (serial Build over the deterministically-ordered chunks slice is byte-stable by construction). End-to-end medium-corpus indexing wall time (M1 Pro / arm64 / Go 1.26.3, semble bench corpus, 378,524 chunks, 3-trial medians):

- **hybrid-regex INDEX**: 165.32 s → 45.43 s = **3.64× speedup** (the headline win on the user-default mode; embed dominates so parallelizing it across 8 cores cashes most of the Amdahl ceiling).
- **bm25-regex INDEX**: 28.57 s → 24.78 s = **1.15× speedup** (small but always positive; `bm25.Build` is the dominant serial bottleneck for bm25-only indexing per Phase 1's hard-falsified P2, and Phase A leaves it serial by design — Phase B sharded-postings is documented future work with a concrete re-open trigger in ADR-030).
- **N=20 determinism stress** at semble medium scale: one unique SHA-256 across 21 builds (1 serial reference + 20 parallel) per mode. **NDCG@10**: exact match all three modes (bm25 0.6237, semantic 0.6469, hybrid 0.8418). The byte-identical-output contract from ADR-024 holds; pre-built indices via `mcp.Run` get the parallel build-time speedup with no on-disk format change.
- **GC share** (hybrid CPU profile): `gcBgMarkWorker` 38.3% → 8.29%; `scanObject` 37.8% → 7.91%; `typePointersOfUnchecked` 22.3% → 4.24%. The parallel mutator runs concurrent with GC across 8 cores, so GC's relative share shrinks ~4×. CPU samples / wall = 3.28× — matches the measured speedup (per the corrected Amdahl mental model: in steady state CPU/wall ratio equals speedup; ADR-029's pre-parallel 1.13× was the visible symptom of single-threaded indexing).

See [ADR-030](DECISIONS.md#adr-030-indexing-pipeline-parallelism--phase-a-per-file-workers-for-chunk--embed-v087) for the architecture, alternatives considered (Phase B sharded `bm25.Build` deferred-with-trigger; stage-based pipeline rejected; opt-in flag rejected), and the deferred concerns (giant-scale memory ceiling, second-machine confirmation as a user-action follow-up). The next perf campaign is not auto-queued — Phase A closes the parallelism investigation at its scoped target; Phase B re-opens only on a concrete user-pain trigger.

### v0.8.8 DB-introspection parallelism (2026-05-27) — MySQL sample loop, Postgres deferred

Same predict-measure-decide loop, smaller scope. A second-opinion review flagged `mysqlAppendSamples` as an `errgroup` candidate. Initial skepticism (sample LIMIT 5 over indexed PKs should be cheap) was falsified by measurement: a new `//go:build dbperf`-gated harness (50 tables × 100 rows synthetic fixture) showed MySQL sample-loop wall is **57% of total introspection** with an Amdahl ceiling of 2.0×. Postgres' sample-loop was only 25% of its total (Amdahl ceiling 1.28×), and a measured implementation via scoped `pgxpool` came out essentially flat on localhost (22.7 ms → 21.7 ms) because pool-setup overhead ate the parallelism gain. Per v0.8.x discipline, Postgres parallelism was reverted; MySQL shipped.

| metric | post-v0.8.7 (sequential) | post-v0.8.8 (MySQL parallel) | Δ |
|---|---|---|---|
| mysqlAppendSamples wall | 43.7 ms | **19.2 ms** | **2.28×** |
| MySQL full introspection wall | 76.6 ms | **41.4 ms** | **1.85× (92% of Amdahl ceiling)** |
| Postgres sampleRowsImpl wall | 22.7 ms | unchanged | — (deferred-with-trigger) |

`TestMySQLIntegration_RowSamplingDeterministic` (the load-bearing sample-row determinism test) passes `-race` clean post-parallel. Output ordering is preserved by per-table writes targeting distinct `&snap.tables[i]` slots. The errgroup is bounded at `min(8, NumCPU())` and the introspection `*sql.DB` is matched via `SetMaxOpenConns(sampleWorkers())` so the pool can never exceed the worker count — two belts protecting shared dev/staging MySQL `max_connections`.

See [ADR-031](DECISIONS.md#adr-031-mysql-introspection-sample-loop-parallelism-postgres-deferred-with-trigger-v088) for the full architecture, the Postgres deferral with its concrete re-open trigger (remote-DB user latency pain), and the rejected Gemini `normalizeMySQLIntType` fast-path (below-noise win — 0.2% — rejected by the same discipline that accepts the Postgres deferral).

## Files

Landed:

- `internal/perf/` — measurement helpers: `LatencyStats` (p50/p95/p99/min/max/mean over `[]time.Duration`), `AllocSnapshot` (runtime.MemStats delta), `HeapSnapshot` (Go-runtime view of HeapInuse/HeapSys/Sys; OS-level peak RSS still needs `/usr/bin/time -v`).
- `cmd/ken/perf.go` — the `ken perf` subcommand dispatch + `perf index` / `perf search` / `perf watch` implementations. Each emits one JSON record per invocation; `index` + `search` also accept `--cpuprofile FILE` / `--memprofile FILE`. Sibling to `ken bench` (NDCG/quality harness); the two share no state.
  - `perf index` — measures cold-index wall time + alloc delta + heap.
  - `perf search` — `--queries FILE` (same `'#' comments + blank lines skipped` format as `ken bench` stdin) + `--n N` (sample size; cycles through the file's queries until N samples collected); emits LatencyStats + alloc-per-query.
  - `perf watch` — `--edits N` (default 10); copies the corpus to a temp dir, opens a WatchedIndex with `SetOnFlush` as the rebuild-completed hook, mutates a target file N times, measures end-to-end edit → flush latency. The v0.3 debouncer (default 2s, ADR-012) dominates and that's the right thing to measure — users see this latency.

> **Note (post-ADR-034):** the `bm25` / `embed` / `ann` / `chunk` benches below were extracted to the `aikit` module with their packages — they now live under `aikit/{bm25,embed,ann,chunk}/...`, not ken's `internal/`. The `internal/search`, `internal/repo`, and `cmd/ken` benches stayed in ken. Paths below are as-landed (pre-extraction).

- `internal/bm25/bm25_bench_test.go` — `BenchmarkTokenize` over a real Go source file (ken's own `internal/search/index.go`; skips if not found); `BenchmarkScore/N100` and `BenchmarkScore/N1000` over a synthetic line-grouped corpus built from the same source. Every benchmark calls `b.ReportAllocs()` and `b.SetBytes()` where meaningful. Targets prediction #2 (BM25 tokenizer allocates heavily) and #3 baseline (BM25 score at the small/medium sizes the rest of the search pipeline feeds it).
- `internal/embed/embed_bench_test.go` — `BenchmarkInferOne` (single ~50-line chunk) and `BenchmarkInferBatch` (1000 distinct chunks, single model load, so amortised per-call cost reads off the batch throughput). Both `b.Skip()` if `testdata/model/` is absent (mirrors the existing parity / golden tests). `b.SetBytes()` reports MB/s of embedding throughput. Targets prediction #1 (embed inference dominates indexing CPU time).
- `internal/ann/ann_bench_test.go` — `BenchmarkFlatQuery/N1k`, `/N10k`, `/N50k` (table-driven via `b.Run`). Synthetic L2-normalized vectors at `dim=128` (Model2Vec's potion-code-16M output dim) with a seeded PRNG so runs are deterministic across hosts. `b.SetBytes()` reports flat-scan memory bandwidth (N × dim × 4 B per query). Max N is 50k rather than the briefing's 100k — judgment call to keep a single iteration under a couple seconds on a laptop; Phase 1 can bump back up on better hardware. Targets prediction #3 (flat search becomes intractable at chromium scale).
- `internal/chunk/regex/chunker_bench_test.go` + `internal/chunk/treesitter/cast_bench_test.go` — `BenchmarkChunker_Go` / `_TypeScript` / `_Python` (regex side) and `BenchmarkCAST_Go` / `_TypeScript` / `_Python` (treesitter side). Identical fixture per language so a Phase-1 benchstat run can diff regex-vs-treesitter throughput directly: Go = ken's own `internal/search/index.go`; TS + Py = `testdata/repo/widget.ts` / `auth.py` (smaller but real — no larger TS/Py fixtures are checked in outside the COIR bench corpus). The treesitter benchmarks include a warm-up call before `ResetTimer` to amortise first-call parser-pool init (ADR-010).
- `internal/search/rerank_bench_test.go` — `BenchmarkRerank/Symbol` and `/NaturalLang` over a synthetic 100-chunk × 20-file corpus with realistic Go-method shapes. Two query types because `applyQueryBoost` branches on `isSymbolQuery(query)` — the symbol path runs `boostSymbolDefinitions`, the natural-language path runs `boostStemMatches` + `boostEmbeddedSymbols`. Falsifies (or doesn't) briefing prediction #7 (rerank is NOT a hotspot at k=20).
- `internal/repo/walk_bench_test.go` — `BenchmarkWalkFS/N100`, `/N1k`, `/N10k` over a synthetic tree spread across N/100 subdirectories. Max N is 10k rather than the briefing's 100k — judgment call to keep setup time bounded (10k files takes ~1 s on a fast SSD; 100k would be ~10×). Targets briefing prediction #4 (file walk is NOT a bottleneck relative to per-file parse + embed).
- `scripts/perf_collect.sh` — bash workload-collection wrapper. Takes one positional arg (`small` / `medium` / `large` / `giant` / `all`) plus optional `--modes=LIST` and `--chunkers=LIST` filters (comma-separated subsets of `{bm25,semantic,hybrid}` / `{regex,treesitter,line}`; default is the full 3×3 matrix). Drives `ken perf index` + `ken perf search` across the selected mode × chunker subset, wraps each invocation in `gtime -v` (macOS, via `brew install gnu-time`) or `/usr/bin/time -v` (Linux) for truthful OS-level peak RSS, captures pprof CPU + heap profiles, writes everything under `bench_out/<workload>/<date>/`. Each run emits a `meta.json` header (uname / go version / ken commit / build flags / start+end timestamps / `modes_run` + `chunkers_run` so downstream analysis can distinguish a filtered run from a full matrix), one JSON record per `(mode × chunker × {index,search})` appended to `records.jsonl`, and pprof profiles under `profiles/`. Workload paths default to the conventional locations and are overridable via `WORKLOAD_MEDIUM` / `WORKLOAD_LARGE`; `giant` is currently a stub pending the Phase-1 corpus-choice decision. Default query set for `perf search` is a small built-in list; override with `PERF_QUERIES=FILE`. Filter examples: `--modes=bm25` (skip semantic+hybrid; re-running after a chunker fix without paying embed cost), `--chunkers=regex,line` (skip the slow treesitter pass), `--modes=bm25 --chunkers=regex` (smoke pass — 1×1 = 2 invocations per workload, makes `large` + `giant` tractable).
- `bench_out/` — per-workload, per-date output directory. Not committed (added to `.gitignore` alongside `outputs/`); reproducible from the methodology above, so we don't carry raw outputs in version control.

The full perf harness is now in tree. Phase 1 runs measurements against it.
