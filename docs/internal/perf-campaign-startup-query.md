# Performance Campaign — Startup + Query Latency

Plan doc for a profile-driven perf campaign focused on **startup
time** and **per-query latency**. Distinct from the v0.8.4-7 campaign
(project-perf-phase0, ADRs 026-029) which targeted indexing wall-time
and parallelism. Targets the two remaining hot user-experience surfaces.

Living document. Mn sub-sections will be added as data lands.
Methodology stays fixed; each milestone publishes numbers in a memo
under `outputs/perf-startup-mN-results.md`.

## Scope

**In scope:**

- **Startup**: ken-mcp cold-start to first servable query.
- **Query latency**: a typical-shape query against a typical repo,
  both cold (first call after process start) and warm (subsequent
  calls).

**Out of scope** (already campaigned):

- Indexing wall-time (ADR-026/027/028 hit −84% bm25, −41% hybrid
  search p50).
- Rerank cache hit rate (M9 ships 40× warm-cache speedup,
  outputs/m9-results.md).
- Indexing parallelism (ADR-030 hits 3.64× hybrid INDEX).
- Rerank attention allocation (M11 removes −75% alloc bytes /
  −93% objects, outputs/m11-results.md).

## Prior art (what we already know)

- Rerank model is 521 MB f32 → load time is non-trivial. The Q8
  variant is ~4× smaller; aikit/encoder ships both with byte-exact
  golden parity on the f32 fixtures.
- "Per-query rerank ~30s cold on full Python function bodies" —
  Stage 7a HyDE M0 memo. M9 + M10 cut rerankN=50 cold from
  6.6s → 3.71s.
- ADR-024 prebuilt indices solve cold-start for **embedded**
  corpora (cmd/ken-mcp-docs); on-demand repo indexing still pays
  the full cost on first query.

## What we do NOT know

- ken-mcp cold start breakdown: process init / library init / model
  load / first index build.
- First-query latency breakdown across BM25 / semantic / hybrid /
  hybrid+rerank, for a real corpus rather than a benchmark queue.
- Warm-steady-state p50/p95/p99 — we track per-query telemetry in
  `internal/search/telemetry.go` but there's no rolling histogram.
- Structural index build cost for the 10-language gotreesitter
  walk on a multi-thousand-file repo.
- Whether the q8 rerank path is a clean win on cold-start latency
  (the cosine-parity tradeoff is the gate).

## Hypothesis ranking

Pre-committed so the data either confirms each H_i or surprises us:

1. **H1 — Rerank model load dominates cold-start.** 521 MB f32
   safetensors → expect ~1-2s just to mmap + parse + L2-normalize
   the weights.
2. **H2 — First-query semantic embed pays a cold-cache penalty.**
   Even after Load, the first `model.Encode` call has worse cache
   locality than subsequent ones.
3. **H3 — `structural.Build` is meaningfully slow on multi-lang
   repos.** We've never measured it; the 10-language pass through
   gotreesitter might be expensive at jekyll scale.
4. **H4 — Query-path overhead is small** (BM25 + RRF + boosts +
   penalties). Prior: already in the milliseconds.

## Milestones

### M0 — Baselines

Profile-driven, no code changes, no optimization.

**Corpora:**

| Tag | Path | Files | Why |
|---|---|---:|---|
| tiny | `testdata/repo` | ~3 | Isolates fixed overhead |
| medium | ken itself | ~200 | Real multi-language; what we use daily |
| large | `/tmp/ken-dogfood/jekyll` | ~1k | Surfaces scale-dependent costs |

**Measurements:**

For each corpus:

1. **Cold-start floor.** Process start → SDK `tools/list` response.
   Times the bare lib init + `NewServer` + transport setup.
2. **First-query latency.** First `search("foo")` after process
   start (cold cache, no Bundle built yet). Breaks down into:
   index build wall, structural build wall, embed query wall,
   search wall.
3. **Warm-steady-state latency.** 100 subsequent `search("foo")`
   calls, p50/p95/p99 of the per-call telemetry's TotalWall.
4. **First structural-tool call.** First `outline(some_file)` after
   process start. Same breakdown as #2 but routes through the
   structural side.

**Per breakdown:**

- ken-mcp process start → first SDK reply
- Library init: `embed.LoadFromFS` for the embedding model,
  `encoder.Load` for the rerank model (if `KEN_RERANK_MODEL_DIR`
  is set)
- First-index build: `search.FromFS` (chunk + embed + BM25 build)
- Structural Build: `structural.Build` (per-file gotreesitter
  parse + extract)
- Per-query: `ix.SearchMode` (search.Telemetry breakdown)

**Tooling:**

- `cmd/ken/perf.go` has the existing perf harness — covers
  indexing, doesn't currently cover startup. M0 extends it with a
  new `ken perf startup` sub-subcommand.
- `internal/search/telemetry.go` Telemetry struct is already
  populated by `SearchModeWithTelemetry`.
- `runtime/pprof` for CPU profiles where breakdowns are unclear.

**Output:** `docs/internal/results/perf-startup-m0-baselines.md` with the numbers,
the methodology, and a hypothesis-by-hypothesis verdict (each H_i
labeled confirmed / refuted / unclear). No code changes; this
milestone is read-only.

### M1+ — Driven by M0 findings

Provisional milestone outline. Will be promoted to real Mn entries
when the data justifies them; cancelled when it doesn't.

**Provisional M1 — Default Q8 rerank when present.** If H1
confirms (rerank load ≥1s), and Q8 is on disk, default to Q8 for
the rerank path. Cosine-parity decision documented before the
default flips.

**Provisional M2 — Lazy rerank model load.** If H1 confirms but
Q8 doesn't ship by default, load the rerank model on first
hybrid+rerank request rather than at startup. Trade first-rerank-
request latency for cold-start latency.

**Provisional M3 — Warm-up `Encode("")` after index build.** If
H2 confirms (first-Encode slower than steady-state), append a
dummy `model.Encode("")` at the end of index build to pre-warm
the transformer block caches.

**Provisional M4 — Parallel `structural.Build`.** If H3 confirms,
parallelize the per-file gotreesitter pass via the same per-CPU
worker pattern that v0.8.7 used for chunk+embed.

**Provisional M5 — Query-path micro-optimizations.** Only fires
if M0 finds the query path is materially over the prior. Examples:
RRF allocs per query, per-call telemetry struct overhead. Pure
speculation until measured.

**Closure.** The campaign closes via an ADR when one of:

- Every milestone that confirmed its hypothesis has shipped, and
  the residual hypotheses (refuted / unclear) are documented.
- We hit empirical diminishing returns and explicitly call it
  (matching v0.8.x's ADR-029 closure pattern).

## Bench rules

Same discipline as v0.8.x:

- Every Mn ships with `benchstat`-style median-of-N numbers (N ≥ 6
  unless single-run is justified) — not vibes, not single-run
  best-case.
- Wall-clock measurements use the per-query Telemetry struct, not
  whatever ad-hoc time.Now() the experiment introduces.
- Calibration runs happen with `GOGC=off`, no fsnotify, no caches —
  the cleanest reproducible baseline.
- Any micro-benchmarks (e.g. matmul) ship as `bench/` go tests
  rather than ad-hoc scripts.

## Calibration machine

This campaign runs on the same M-series Mac that hosted ADRs 026-030.
Reproductions on x86_64 are "nice to have" and would land in a memo
addendum, not as a campaign gate.

## Out-of-band trigger

If a real user reports a latency complaint that surfaces a hot path
none of M0's measurements touched, that's a re-open trigger for the
campaign even after it closes. The current 1.0 ship-list assumes
this campaign closes cleanly; an unexpected hot path is grounds for
adding a milestone.
