# What ken should feel like

User-facing performance expectations. Concrete numbers per corpus
size + what counts as a regression. Anchored to a specific hardware
profile so "is ken slow?" has a checkable answer.

For the measurement *methodology* — the harness, the workloads, the
benchstat discipline that says when a number is publishable — see
[PERF.md](internal/PERF.md). This doc is the layer above: "if you run ken on
a typical laptop, here's roughly what you should see, and here's
what would be surprising."

## Hardware anchor

Numbers below are measured on **Apple M-series (M1 Pro, 8-core,
arm64 darwin, Go 1.26.3, native build)** unless otherwise noted.

- **x86_64 Linux on equivalent core count:** mostly within ±20%.
  No second-machine confirmation has been published yet (the
  rolling debt from ADR-026/027/028 was retired by stopping rather
  than spent, per ADR-029).
- **Rotational disk:** all bets off. ken's indexing reads every
  file once; if your storage can't keep up with sequential reads
  of your corpus, no in-process optimization recovers that.

## What you should feel

A "is this normal?" cheat sheet. All numbers are M-series M1 Pro
medians.

| Corpus | Files | ken-mcp cold start → first servable query | Warm `search` p50 | Cold rerank (N=50) | Warm rerank cache hit |
|---|---:|---:|---:|---:|---:|
| tiny (test fixture) | <50 | **~130 ms** | 0.27 ms | n/a (no rerank on tiny) | n/a |
| medium (ken itself) | ~250 | **~550 ms** | 0.78 ms | ~7 s | 110 ms |
| large (jekyll-class) | ~750 | **~1.3 s** | 0.92 ms | ~7 s | 110 ms |
| huge (semble bench) | ~80k repos × ~378k chunks | ~45 s (hybrid index) | TBD | not measured at this scale | not measured |

> **aikit v1.4.0 speedup (measured 2026-06-11, M-series).** The bump moved
> the semantic-arm cosine scan (`aikit/ann.Flat.Query`) from a scalar-f64
> loop to a SIMD-f32 dot kernel: **11.7× faster** in isolation (micro-bench
> 2061 µs → 176 µs over 8 000×256). Because the scan is **O(N) in chunks**,
> the win is invisible at the sub-ms small-corpus scales in the table above
> but compounds with corpus size — on laravel-framework (~13 k chunks)
> end-to-end hybrid `search` p50 dropped **4.58 ms → 1.56 ms (−66 %, ≈3×)**,
> allocations unchanged, **recall@10 re-verified identical**. Index time is
> unchanged (the bump didn't touch the index path).
>
> **aikit v1.5.0 — int8 reranker is now the default.** The q8 reranker path
> had an allocation + matmul bug (4.4 GiB scratch + `matmulBTQ8` re-widening
> int8→f32 inside the GEMM) that made it ~5× slower than f32. aikit v1.5.0
> fixes both (pooled scratch + dequant-once-then-SIMD), so on ken's rerank
> path int8 now reaches **f32 latency parity** (50-doc cold: 7.35 s vs 7.75 s,
> arm64) at **~21× less runtime memory** (18 MiB vs 379 MiB) and ¼ the weight
> storage (~140 MB resident vs ~547 MB), cosine 0.997 vs f32 unchanged. So
> `KEN_MCP_RERANK_QUANT` / `--rerank-quant` now default to `int8`; pass `f32`
> for the full-precision path. (This reverses the v1.4.0-era note that int8
> was slower on Apple Silicon — that was the pre-fix aikit q8 path.)

**Cold start is the budget cmd/ken-mcp pays before any tool can
return.** It splits into four pieces — embed model load (~53 ms,
fixed), rerank model load (~491 ms; **lazy as of v0.9.0 / ADR-036
M2**, paid on the first hybrid+rerank query rather than at
startup), `search.FromFS` (linear in chunks), and
`structural.Build` (linear in files, parallel as of v0.9.0 /
ADR-036 M4). Per ADR-036's cumulative numbers: tiny dropped 79 %,
medium 60 %, large 55 % from pre-campaign baselines.

**Warm search is sub-millisecond by design.** If you see >5 ms p50
on a medium corpus, something is wrong — that's the regression
threshold below.

**Rerank cache hit is 65× faster than rerank cold.** This is
ken-side measured in
[`internal/search/neural_rerank_bench_test.go`](../internal/search/neural_rerank_bench_test.go);
the on-disk cache (ADR-024 / KNRC format) means once the same docs
get scored once, future rerank passes over the same docs are 110 ms
instead of 7 s.

## Indexing throughput

What `ken index <corpus>` should feel like, by mode.

| Corpus | bm25 only | hybrid (M2V semantic) | hybrid+treesitter chunker |
|---|---:|---:|---:|
| medium (~378k chunks) | ~25 s | **~45 s** (post-ADR-030 parallel) | ~37 min — see caveat |
| large (Linux kernel, ~80k files) | not measured; extrapolated ~minutes | not measured; extrapolated ~10–30 min for full hybrid | not recommended at this scale (see caveat) |

**Caveat on the treesitter chunker at scale.** Per ADR-029's
investigation, the gotreesitter arena cost is super-linear — 6.4×
growth in per-chunk allocation from small to medium corpora. On
huge corpora it's tractable but expensive. The regex chunker
remains the default; treesitter is opt-in
(`--chunker=treesitter` / `KEN_MCP_CHUNKER=treesitter`).

**Caveat on the Linux-kernel extrapolation.** ken hasn't actually
been measured against the Linux kernel yet — it's the "Large"
workload TBD in [PERF.md](internal/PERF.md). The "~10–30 min" estimate is
linear extrapolation from medium (378k chunks → 45 s hybrid) to
the kernel's expected ~5 M chunks. Real numbers will land when the
PERF.md "Large" pass runs; treat the extrapolation as a check
against intuition, not a published headline.

**Per-file rule of thumb** for cold `structural.Build` (Go on
M-series):

- ~1.4 ms/file on tiny / small Go corpora
- ~2.3 ms/file on medium Go corpora
- ~9.4 ms/file on jekyll-class Ruby corpora (Ruby's gotreesitter
  grammar is heavier than Go's, plus jekyll has more lines per
  file)

If your `structural.Build` is significantly outside that range
for the language mix you're indexing, [ADR-036 M4 (parallel
structural.Build)](internal/DECISIONS.md#adr-036) may not be active —
check that ken-mcp is reporting `runtime.NumCPU()` workers, not 1.

## Query latency

Warm-query targets (M-series, post-warmup, n=100):

- **p50:** sub-millisecond on every measured corpus (tiny / medium /
  large). The query path is genuinely fast and was never the
  campaign target.
- **p95:** ≤ 3.5 ms on medium; ≤ 1.1 ms on large.
- **p99:** typically a one-tail-event GC pause (~14 ms on medium
  was the worst seen).

Hybrid+rerank latency (the slow path, when the agent calls
`ModeHybridRerank` with N candidates):

- **N=50 cold (first query, no on-disk cache yet):** ~7 s.
- **N=50 warm-cache (cache hit on the same docs):** ~110 ms — 65×
  faster.

If you see warm `search` p50 ≥ 5 ms on a medium corpus, that's a
regression. ken's `search` is designed to be in the sub-ms band on
any laptop-scale corpus; the only knobs that move it materially
are rerank-on (which is its own latency budget per above) and
flat-vs-HNSW ANN (HNSW not shipped; see
[DESIGN.md §10's HNSW row](DESIGN.md#10-risk-register)).

## Memory & binary size

**Resident memory at steady state** (after `Build` settles, before
queries):

| Corpus | structural.Index | Full ken-mcp process (approx) |
|---|---:|---:|
| jekyll (Ruby, 167 files) | ~29 MiB HeapAlloc | dominated by gotreesitter parser arenas above this |
| express (JS, 141 files) | ~25 MiB | same caveat |
| ripgrep (Rust, 101 files) | **~309 MiB** | gotreesitter arenas dominate — Rust grammar is heavy |

The structural-call-graph Phase 0 substrate (per-call-site
`CallRef` records, the 2026-06-03 ship) adds ~500 KiB on each
of those — well inside the plan's ≤2× memory envelope. See
[structural-call-graph-plan.md](internal/structural-call-graph-plan.md).

**Binary size (slim release builds, ADR-033):**

- `ken-mcp`: **~38 MB** (was ~52 MB pre-slim).
- `ken`: **~22 MB** (was ~36 MB pre-slim).

Slim builds embed only the 19 tree-sitter grammars ken actually
dispatches (per `aikit/chunk/treesitter.KenToTreeSitter`). Fat
builds (no `grammar_subset` build tags) embed all ~206 grammars
gotreesitter ships — ~+15 MB. Slim is the release default;
external `mcp.Run` consumers who don't pass the tags get the fat
build.

## Watch-mode incremental rebuild

ken's `ken index --watch` (default) + ken-mcp's always-on watcher
use fsnotify with a **2-second debounce window** (ADR-012). What
you should feel:

- Save a file → ~2 s later the new index is published via an atomic
  pointer swap → next query reads the new state.
- Multiple saves within the 2 s window collapse into one re-publish.
- Reader queries during the re-publish see the OLD snapshot, never
  a partial one — atomic-snapshot invariant.

If you're seeing visible per-edit latency above the 2 s debounce
on a small corpus, the debouncer isn't the bottleneck — likely
either the rebuild itself is slow (large corpus, hybrid mode, no
prebuilt index) or fsnotify isn't seeing your edits at all.

## What counts as a regression

A red flag for any of these means run `scripts/perf_startup_m0.go`
on a known corpus and compare to the M0 baseline in
[`outputs/perf-startup-m0-baselines.md`](../outputs/perf-startup-m0-baselines.md):

1. **`ken-mcp` cold start with `KEN_MCP_RERANK=on` is materially
   above 30 ms median.** ADR-036 M2 made rerank load lazy; if
   you see ~500 ms of cold-start cost with no first query yet,
   the LazyReranker wiring broke.
2. **`structural.Build` runs with one worker.** ADR-036 M4 made
   it parallel; check ken-mcp's startup log line. If `Build` is
   single-threaded, the speedup from M4 is gone and large
   corpora pay the pre-campaign cold-start time.
3. **Warm `search` p50 above 5 ms on a medium corpus.** The query
   path should be sub-ms; this means either ANN flat is being
   asked to do more work than it should (rerank-on while you
   meant rerank-off?) or the structural index isn't loading.
4. **Cold rerank pass over N=50 docs is materially above 10 s.**
   The neural rerank bench in
   [`internal/search/neural_rerank_bench_test.go`](../internal/search/neural_rerank_bench_test.go)
   establishes the ~7 s baseline; check with that bench before
   debugging upstream.
5. **Warm rerank cache hit is materially above 200 ms.** The
   on-disk KNRC cache should bring N=50 to ~110 ms; if cache hits
   are slow, suspect the on-disk file is corrupt or the encoder
   model changed (cache scope-keys on the quantization tier).
6. **NDCG@10 moved by more than ±0.005 on the semble bench
   corpus.** Per [PERF.md](internal/PERF.md) and
   [BENCH.md](BENCH.md): perf changes that regress retrieval
   quality don't ship without an explicit calibration-discipline
   ADR documenting the trade. Same threshold here for "user
   reports ken feels worse" — re-run the bench.

If none of the red flags fire but ken still feels slow on your
specific corpus, the next step is to run `scripts/perf_collect.sh
medium` per [PERF.md](internal/PERF.md) and benchstat against a stored
baseline. The PERF.md harness is the right tool for honest
investigation; this doc is the rule-of-thumb layer above it.

## How to check ken on your corpus

Two quick paths:

**Quick health check (single binary, ~30 s):**

```bash
go run scripts/perf_startup_m0.go /path/to/your/corpus
```

That prints the same record format
[`outputs/perf-startup-m0-baselines.md`](../outputs/perf-startup-m0-baselines.md)
was built from. Compare embed-load + rerank-load + search.FromFS +
structural.Build to the table at the top of this doc.

**Full benchstat pass (see [PERF.md](internal/PERF.md) for the methodology):**

```bash
scripts/perf_collect.sh small      # ~30 s
scripts/perf_collect.sh medium     # ~5 min
scripts/perf_collect.sh large      # ~20-60 min
```

Per PERF.md's discipline, numbers from these are publishable only
after median-of-N + second-machine confirmation; for "is ken slow
on my workload right now" the quick health check above is the
right tool.

## What's NOT in this doc

- **A published Linux kernel measurement.** The "Large" workload
  in PERF.md is pinned at v6.10 but has not yet been measured; the
  extrapolation in this doc is rule-of-thumb only.
- **A published x86_64 second-machine number.** Per ADR-029, the
  rolling debt from ADR-026/027/028 was retired by stopping; a
  fresh cross-architecture pass would need to land first before
  publishable headline numbers exist.
- **Memory at "huge" scale.** No published number for what
  `structural.Index` resident-memory looks like on a 100k-file
  monorepo. The Phase 0 substrate budget held on the three corpora
  tested (~500 KiB each); whether the ~2 % growth ratio extends to
  monorepo scale is the next thing to measure if/when Phase 1+4
  ship.

---

References:

- [PERF.md](internal/PERF.md) — measurement methodology, harness, workloads
- [BENCH.md](BENCH.md) — NDCG / quality methodology (companion to PERF.md)
- [DECISIONS.md ADR-026 → ADR-031](internal/DECISIONS.md) — the v0.8.x perf campaign trail
- [DECISIONS.md ADR-036](internal/DECISIONS.md#adr-036) — startup + query latency campaign close (M2 + M4)
- [`outputs/perf-startup-m0-baselines.md`](../outputs/perf-startup-m0-baselines.md) — M0 cold-start baselines
- [`internal/search/neural_rerank_bench_test.go`](../internal/search/neural_rerank_bench_test.go) — neural rerank bench (cold vs warm-cache)
- [`scripts/perf_startup_m0.go`](../scripts/perf_startup_m0.go) — quick health-check harness
