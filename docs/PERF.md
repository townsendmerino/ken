# Measuring ken's performance numbers

ken's quality story is settled — NDCG@10 lands within the documented ±0.005 of semble (see [`docs/BENCH.md`](BENCH.md)). The perf story isn't yet: we have no published numbers, no measurement harness in-tree, and no documented methodology. This file is the calibration anchor for that work. It lands **before any number is collected**, so the methodology can't be retrofitted to flatter a conclusion.

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
| Medium | semble bench corpus | ~19 repos aggregate | `repos.json` revisions (upstream-pinned) | Same corpus as `docs/BENCH.md` NDCG runs; perf and quality numbers line up. |
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

*This section accumulates per-investigation findings the same way `docs/BENCH.md` accumulates per-version findings. The first investigation pass will land its outcome here, cross-referenced to ADR-025.*

## Files

Landed:

- `internal/perf/` — measurement helpers: `LatencyStats` (p50/p95/p99/min/max/mean over `[]time.Duration`), `AllocSnapshot` (runtime.MemStats delta), `HeapSnapshot` (Go-runtime view of HeapInuse/HeapSys/Sys; OS-level peak RSS still needs `/usr/bin/time -v`).
- `cmd/ken/perf.go` — the `ken perf` subcommand dispatch + `perf index` / `perf search` / `perf watch` implementations. Each emits one JSON record per invocation; `index` + `search` also accept `--cpuprofile FILE` / `--memprofile FILE`. Sibling to `ken bench` (NDCG/quality harness); the two share no state.
  - `perf index` — measures cold-index wall time + alloc delta + heap.
  - `perf search` — `--queries FILE` (same `'#' comments + blank lines skipped` format as `ken bench` stdin) + `--n N` (sample size; cycles through the file's queries until N samples collected); emits LatencyStats + alloc-per-query.
  - `perf watch` — `--edits N` (default 10); copies the corpus to a temp dir, opens a WatchedIndex with `SetOnFlush` as the rebuild-completed hook, mutates a target file N times, measures end-to-end edit → flush latency. The v0.3 debouncer (default 2s, ADR-012) dominates and that's the right thing to measure — users see this latency.

- `internal/bm25/bm25_bench_test.go` — `BenchmarkTokenize` over a real Go source file (ken's own `internal/search/index.go`; skips if not found); `BenchmarkScore/N100` and `BenchmarkScore/N1000` over a synthetic line-grouped corpus built from the same source. Every benchmark calls `b.ReportAllocs()` and `b.SetBytes()` where meaningful. Targets prediction #2 (BM25 tokenizer allocates heavily) and #3 baseline (BM25 score at the small/medium sizes the rest of the search pipeline feeds it).
- `internal/embed/embed_bench_test.go` — `BenchmarkInferOne` (single ~50-line chunk) and `BenchmarkInferBatch` (1000 distinct chunks, single model load, so amortised per-call cost reads off the batch throughput). Both `b.Skip()` if `testdata/model/` is absent (mirrors the existing parity / golden tests). `b.SetBytes()` reports MB/s of embedding throughput. Targets prediction #1 (embed inference dominates indexing CPU time).
- `internal/ann/ann_bench_test.go` — `BenchmarkFlatQuery/N1k`, `/N10k`, `/N50k` (table-driven via `b.Run`). Synthetic L2-normalized vectors at `dim=128` (Model2Vec's potion-code-16M output dim) with a seeded PRNG so runs are deterministic across hosts. `b.SetBytes()` reports flat-scan memory bandwidth (N × dim × 4 B per query). Max N is 50k rather than the briefing's 100k — judgment call to keep a single iteration under a couple seconds on a laptop; Phase 1 can bump back up on better hardware. Targets prediction #3 (flat search becomes intractable at chromium scale).
- `internal/chunk/regex/chunker_bench_test.go` + `internal/chunk/treesitter/cast_bench_test.go` — `BenchmarkChunker_Go` / `_TypeScript` / `_Python` (regex side) and `BenchmarkCAST_Go` / `_TypeScript` / `_Python` (treesitter side). Identical fixture per language so a Phase-1 benchstat run can diff regex-vs-treesitter throughput directly: Go = ken's own `internal/search/index.go`; TS + Py = `testdata/repo/widget.ts` / `auth.py` (smaller but real — no larger TS/Py fixtures are checked in outside the COIR bench corpus). The treesitter benchmarks include a warm-up call before `ResetTimer` to amortise first-call parser-pool init (ADR-010).
- `internal/search/rerank_bench_test.go` — `BenchmarkRerank/Symbol` and `/NaturalLang` over a synthetic 100-chunk × 20-file corpus with realistic Go-method shapes. Two query types because `applyQueryBoost` branches on `isSymbolQuery(query)` — the symbol path runs `boostSymbolDefinitions`, the natural-language path runs `boostStemMatches` + `boostEmbeddedSymbols`. Falsifies (or doesn't) briefing prediction #7 (rerank is NOT a hotspot at k=20).
- `internal/repo/walk_bench_test.go` — `BenchmarkWalkFS/N100`, `/N1k`, `/N10k` over a synthetic tree spread across N/100 subdirectories. Max N is 10k rather than the briefing's 100k — judgment call to keep setup time bounded (10k files takes ~1 s on a fast SSD; 100k would be ~10×). Targets briefing prediction #4 (file walk is NOT a bottleneck relative to per-file parse + embed).
- `scripts/perf_collect.sh` — bash workload-collection wrapper. Takes one positional arg (`small` / `medium` / `large` / `giant` / `all`) plus optional `--modes=LIST` and `--chunkers=LIST` filters (comma-separated subsets of `{bm25,semantic,hybrid}` / `{regex,treesitter,line}`; default is the full 3×3 matrix). Drives `ken perf index` + `ken perf search` across the selected mode × chunker subset, wraps each invocation in `gtime -v` (macOS, via `brew install gnu-time`) or `/usr/bin/time -v` (Linux) for truthful OS-level peak RSS, captures pprof CPU + heap profiles, writes everything under `bench_out/<workload>/<date>/`. Each run emits a `meta.json` header (uname / go version / ken commit / build flags / start+end timestamps / `modes_run` + `chunkers_run` so downstream analysis can distinguish a filtered run from a full matrix), one JSON record per `(mode × chunker × {index,search})` appended to `records.jsonl`, and pprof profiles under `profiles/`. Workload paths default to the conventional locations and are overridable via `WORKLOAD_MEDIUM` / `WORKLOAD_LARGE`; `giant` is currently a stub pending the Phase-1 corpus-choice decision. Default query set for `perf search` is a small built-in list; override with `PERF_QUERIES=FILE`. Filter examples: `--modes=bm25` (skip semantic+hybrid; re-running after a chunker fix without paying embed cost), `--chunkers=regex,line` (skip the slow treesitter pass), `--modes=bm25 --chunkers=regex` (smoke pass — 1×1 = 2 invocations per workload, makes `large` + `giant` tractable).
- `bench_out/` — per-workload, per-date output directory. Not committed (added to `.gitignore` alongside `outputs/`); reproducible from the methodology above, so we don't carry raw outputs in version control.

The full perf harness is now in tree. Phase 1 runs measurements against it.
