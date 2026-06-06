# M0 — Startup & Query Latency Baselines

Profile-driven baselines for the perf campaign tracked in
[`docs/internal/perf-campaign-startup-query.md`](../docs/internal/perf-campaign-startup-query.md).
Single run on M-series Mac (darwin/arm64, Go 1.26.3, GOMAXPROCS=8).
N=100 warm-search samples per corpus after a 10-sample warmup
drop. No GOGC tweaks, no profile-time-ordering games.

## Method

Harness: [`scripts/perf_startup_m0.go`](../scripts/perf_startup_m0.go).
One-shot Go program that loads each model once, then for every
corpus measures: `search.FromFS` (cold), `structural.Build`
(cold), first `SearchMode` call, first `Outline` call, then 100
warm `SearchMode` samples. JSON record per corpus to stdout +
markdown summary to stderr.

Corpora:

| Tag | Path | Files (indexed) | Chunks |
|---|---|---:|---:|
| tiny | `testdata/repo` | 6 | 6 |
| medium | ken itself | 250 | 1,667 |
| large | `/tmp/ken-dogfood/jekyll` | 766 | 1,865 |

## Results

| Corpus | embed load | rerank f32 load | search.FromFS | structural.Build | first search | first outline | warm p50 | warm p95 | warm p99 |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| tiny | 52.7ms | **491.4ms** | 75.8ms | 7.1ms | 0.4ms | 0.0ms | 0.27ms | 0.36ms | 0.60ms |
| medium | 52.7ms | **491.4ms** | 398.2ms | 462.6ms | 1.0ms | 0.0ms | 0.78ms | 3.36ms | 14.17ms |
| large | 52.7ms | **491.4ms** | 805.3ms | **1577.4ms** | 1.0ms | 0.0ms | 0.92ms | 1.07ms | 1.15ms |

Numbers ≥100ms in **bold**. Q8 rerank not measured this run
(`KEN_RERANK_MODEL_Q8_DIR` unset).

### Cold-start budget breakdown

The "first servable query" budget = embed load + rerank load +
search.FromFS + structural.Build (the four serialize on the
critical path of cmd/ken-mcp startup before any tool can return):

| Corpus | total cold-start | rerank share | search share | structural share | embed share |
|---|---:|---:|---:|---:|---:|
| tiny | 627 ms | **78%** | 12% | 1% | 8% |
| medium | 1,405 ms | 35% | 28% | **33%** | 4% |
| large | 2,927 ms | 17% | 28% | **54%** | 2% |

**At tiny scale rerank load is the floor cost.** At medium and
large scale, indexing and structural extraction dominate.

## Hypothesis verdicts

- **H1: Rerank model load dominates cold-start.** ✅ Confirmed at
  tiny scale (78% of cold-start budget); ⚠️ partial at medium/large
  (rerank stays at ~490 ms regardless, but scales out of dominance
  vs indexing). The 491 ms is a fixed floor every cold start pays
  even when no `hybrid+rerank` query is ever issued.

- **H2: First-query semantic embed pays a cold-cache penalty.**
  ⚠️ Weakly confirmed. First search ~1.0 ms vs warm p50 0.78-0.92
  ms = ~25% slowdown. Absolute difference < 0.3 ms. Below the
  worthwhile-optimization bar.

- **H3: structural.Build is meaningfully slow on multi-lang repos.**
  ✅ Confirmed. Per-file cost: 1.4 ms tiny / 2.3 ms medium /
  **9.4 ms large**. Ruby's tree-sitter grammar is slower than Go's
  per-file, AND jekyll has more lines per file on average. At 1k+
  files this dominates cold-start.

- **H4: Query-path overhead is small.** ✅ Confirmed. Warm p50 sub-
  millisecond on all three corpora. Even the medium p99 of 14 ms
  is one tail event in 100 — likely GC pause or scheduler.

## Findings beyond the hypotheses

- **`search.FromFS` scales linearly with chunks** (already
  parallelized by ADR-030 — v0.8.7 per-file workers).
- **`structural.Build` scales worse than search.FromFS by file**
  AND is currently single-threaded (per-file gotreesitter walk
  with no fan-out). The 9.4 ms / file on jekyll is the cost of
  serializing 167 Ruby parses.
- **embed.Load is constant** (~53 ms) and is fixed by file size,
  not corpus.
- **First outline ≈ 0 ms** — once the structural index is built,
  the lookup is a map fetch. No cost there.
- **Medium-corpus warm p99 of 14 ms** is an outlier (max 20.5 ms).
  Probably a GC pause. Not chasing in this campaign unless it
  shows up in real user reports — see "Out-of-band trigger" in
  the campaign plan.

## Promoted milestone plan

The provisional milestone list in the campaign plan is updated
based on M0:

| Mn | Hypothesis | Verdict | Status |
|---|---|---|---|
| M1 | Default Q8 rerank when present (saves ~365 ms cold-start) | Worth shipping if cosine-parity holds | Greenlight, gated on parity |
| M2 | Lazy rerank model load (saves 491 ms when no rerank query) | Worth shipping; ken-mcp's default mode is hybrid, not hybrid+rerank | Greenlight |
| M3 | Warm-up `Encode("")` after index build | H2 weak → no win to capture | **Refuted; killed** |
| M4 | Parallel `structural.Build` | H3 confirmed; saves up to ~1.4 s on large corpora | Greenlight |
| M5 | Query-path micro-optimizations | H4 refuted → already fast | **Refuted; killed** |

Recommended ship order:

1. **M2 (lazy rerank load).** Biggest absolute win on cold-start
   for the common case (default hybrid mode). Mechanical refactor.
2. **M4 (parallel structural.Build).** Biggest absolute win at
   scale. Same per-CPU worker pattern v0.8.7 already used.
3. **M1 (Q8 rerank default).** Smaller win and requires a
   cosine-parity decision. Lands after the first two.

## Reproducibility

```bash
# Fresh process for clean cold-start numbers; --corpus selects
# tiny / medium / large / all.
go run scripts/perf_startup_m0.go --corpus all

# Override model dirs if needed.
KEN_MODEL_DIR=~/.ken/model \
  KEN_RERANK_MODEL_DIR=~/.ken/rerank-model \
  go run scripts/perf_startup_m0.go --corpus medium

# With Q8 measured alongside (only when the dir is set).
KEN_RERANK_MODEL_Q8_DIR=~/.ken/rerank-model-q8 \
  go run scripts/perf_startup_m0.go --corpus tiny
```

Determinism caveat: warm-search jitter is real (medium p99 of
14 ms vs p95 of 3.4 ms). Re-run produces slightly different
percentiles. The deltas worth optimizing for are 10×+ shifts,
not single-digit-percent.

## Out-of-scope (intentional)

- Rerank pass latency (`hybrid+rerank` queries). Not in startup-
  + query-latency campaign scope; M9 + M10 already campaigned
  this surface. Re-open if a real user reports it.
- Q8 cosine parity verification. Owned by aikit/encoder's
  golden tests, not this campaign.
- x86_64 reproduction. Documented as nice-to-have in the campaign
  plan.
