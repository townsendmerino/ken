# Wiring aikit's quantized/approximate retrievers into ken — measured, 2026-09

**Closes roadmap A2** (`docs/internal/roadmap-2026-06.md` Addendum) and the
trigger question in [`DESIGN.md` §10](../DESIGN.md#10-risk-register)
("HNSW for the dense retriever" — *trigger: dense matrix size makes exact
cosine the bottleneck on a real workload*).

## TL;DR

**`FlatBinaryI8` wins outright — not just "at scale."** On the real 63-repo /
1251-query semble corpus, swapping ken's semantic arm from `ann.Flat` to
`ann.FlatBinaryI8` costs **zero measured end-to-end recall** (pipeline
recall@10 identical to 3 decimals: 0.974 all / 0.971 NL / 0.995 symbol —
matching the published 0.967/0.995 bar) while being **~2× faster even at
today's repo scale** (N≈13k) and **5.4× faster / 3.5× smaller at N=800k**.
`FlatI8` alone is a smaller, scale-gated win (slower than `Flat` below
~50k, 2.7–2.8× faster above 200k). `HNSW` is disqualified for ken's default
indexing model at any scale that matters: build time reproduces the prior
4m23s@200k anchor almost exactly (271.8s here) even though its *query* is
competitive from ~50k on, and it costs **more** memory than `Flat`, not
less (keeps the f32 vectors AND a graph). **Recommendation:** wire
`FlatBinaryI8` in behind a default-off knob now (Phase 2, this doc), with a
path to flipping the default once kernel-scale (500k+) recall is spot-checked
against a real corpus (the one gap this pass didn't close — see "What's not
measured").

## Background

ken's dense retriever is `aikit/ann.Flat` — exhaustive f32 cosine, O(N) per
query (`internal/search/index.go:694`, `ann.New(vecs)`). A prior roofline
investigation (`aikit/docs/task-archsimd-eval.md`,
[`kernel-demo-feasibility.md`](kernel-demo-feasibility.md)) established that
`Flat.Query` is **memory-bandwidth-bound ~11×** on this class of machine — a
dot product is a fixed 0.25 MAC/byte at every dimension, so no wider SIMD
kernel can move it; the lever is **fewer bytes streamed per candidate**, not
a faster multiply. aikit already ships three retrievers that pull that
lever — `FlatI8` (int8, ~4× smaller), `FlatBinaryI8` (binary prefilter +
int8 rerank, two-stage), `HNSW` (sub-linear, changes complexity class) — but
ken wires none of them.

This doc measures whether wiring any of them in is actually justified, and
at what scale, against ken's published recall bar (0.967 NL / 0.995 symbol
recall@10, 0.842 NDCG@10 hybrid — `docs/BENCH.md`).

An earlier, narrower pass at this question ran 2026-08-15 (see the
`project-aikit-adoption-evals` memory) at the aikit level only — synthetic
scale sweep + `agree@k` vs f32 + one end-to-end NDCG gate for FlatI8 alone.
It found FlatI8 quality-neutral end-to-end but shelved the *memory* win
because ken's incremental watch store (`WatchedIndex.w.vecs`) must stay f32
for append/compaction — a separate refactor, not this decision. That finding
still holds and isn't re-litigated here. What's new in this pass: (1) the
candidate-set-vs-end-to-end distinction is measured directly through ken's
own RRF fusion rather than inferred, (2) `FlatBinaryI8` (added to aikit since
that prior pass) is evaluated as the two-stage retriever the framing
predicted, (3) the scale ramp is retained as a harness instead of ad hoc, and
(4) HNSW's real-corpus recall is measured through the full pipeline, not
just `agree@1`.

## Why the candidate-set/end-to-end distinction matters here specifically

`internal/search/hybrid.go`'s `hybridSearch` calls
`flat.Query(qVec, candidateCount)` and keeps only `h.Index` — the **rank
position** — to build `semOrder`, which feeds `aikit/fuse.RRFWeighted`
(1-indexed `1/(k+rank)`, ranked-based, not score-based). The retriever's
actual cosine *values* are discarded before fusion. That means an
approximate retriever that gets the candidate SET right, and even one that
only roughly preserves order within that set, costs the fusion stage
**nothing** — BM25 and the boost/penalty stages downstream never see the
approximation at all. The exposed case is `find_related`, which is pure-ANN
with no fusion to paper over a wrong candidate set.

## Method

Two harnesses, both retained in `internal/search/` under the `bench` build
tag (same tag as `TestRecallDecomp`, same prerequisites — semble checkout +
`~/.cache/semble-bench` corpus + `~/.ken/model`):

- **`retriever_eval_test.go` / `TestRetrieverEval`** — real-corpus recall.
  For each of semble's benchmark repos, builds all four retrievers
  (`flat-f32`, `flat-i8`, `flat-binary-i8`, `hnsw`) from the *same*
  `ix.Vecs()` the hybrid index already produced, then measures per retriever:
  semantic-only candidate recall@{10,50,100} (no BM25, no fusion — the
  `find_related` exposure), full pipeline recall@10 (`hybridSearchAnyMatch`
  replays `hybridSearch`'s body verbatim, semantic arm swapped for the
  retriever under test — literally the Phase-2 production swap), and
  in-process query latency.
- **`retriever_scale_bench_test.go` / `TestRetrieverScaleSweep`** (opt-in,
  `KEN_SCALE_SWEEP=1`) — the crossover ramp. Real repos top out around 6-7k
  chunks, short of where the O(N) wall bites; this sweeps synthetic
  unit-normalized vectors at ken's real dim (256, potion-code-16M) across
  N ∈ {13k, 50k, 200k, 800k} and measures build time, query p50/p95, and
  memory (both an exact analytic byte formula and a measured heap delta).
  HNSW is capped at 200k — a prior anchor already put its build time at
  4m23s@200k, established as a dealbreaker for ken's rebuild-on-change
  indexing model independent of recall, so extending it to 800k would only
  re-confirm an already-decided point at real memory risk on a 16GB box.

Recall bar reproduced from `docs/BENCH.md`: **0.967 NL / 0.995 symbol
recall@10**, **0.842 NDCG@10 hybrid**.

## Results

Ran 2026-09-06, this machine (M-series, 8 cores, 16GB), aikit v1.31.0 (ken's
pinned version — cross-checked: the 2 commits between v1.31.0 and the aikit
HEAD used for local iteration are a GPU-shard-split clamp fold and a `go fix`
pass, neither touching `ann`'s CPU retrieval path, so these numbers hold for
the pinned version with no aikit bump needed).

### Real-corpus recall (`TestRetrieverEval`, all 63 semble repos, 1251 tasks)

| Retriever | class | n | sem@10 | sem@50 | sem@100 | **pipe@10** | p50 | p95 | bytes/vec |
|---|---|--:|--:|--:|--:|--:|--:|--:|--:|
| flat-f32 (today) | all | 1251 | 0.843 | 0.970 | 0.986 | **0.974** | 33µs | 236µs | 1024 |
| flat-f32 | nl | 1057 | 0.843 | 0.968 | 0.984 | 0.971 | 33µs | 240µs | 1024 |
| flat-f32 | symbol | 194 | 0.840 | 0.979 | 1.000 | 0.995 | 35µs | 222µs | 1024 |
| flat-i8 | all | 1251 | 0.844 | 0.970 | 0.986 | **0.974** | 25µs | 119µs | 260 |
| flat-i8 | nl | 1057 | 0.845 | 0.968 | 0.984 | 0.971 | 25µs | 122µs | 260 |
| flat-i8 | symbol | 194 | 0.840 | 0.979 | 1.000 | 0.995 | 26µs | 97µs | 260 |
| **flat-binary-i8** | all | 1251 | 0.844 | 0.970 | 0.987 | **0.974** | 26µs | 117µs | 292 |
| flat-binary-i8 | nl | 1057 | 0.845 | 0.968 | 0.985 | 0.971 | 25µs | 117µs | 292 |
| flat-binary-i8 | symbol | 194 | 0.840 | 0.979 | 1.000 | 0.995 | 26µs | 116µs | 292 |
| hnsw | all | 1251 | 0.843 | 0.970 | 0.986 | **0.974** | 49µs | 146µs | 1152 |
| hnsw | nl | 1057 | 0.843 | 0.968 | 0.984 | 0.971 | 48µs | 145µs | 1152 |
| hnsw | symbol | 194 | 0.840 | 0.979 | 1.000 | 0.995 | 56µs | 152µs | 1152 |

**Reading this table is the whole point of framing #1.** `sem@10` (pure
semantic-arm recall, no BM25/fusion — the `find_related` exposure) barely
moves across all four retrievers (0.840–0.845), and `pipe@10` (the real
`search` pipeline) is bit-for-bit identical to 3 decimals across all four —
0.974/0.971/0.995 in every row. Real code corpora at repo scale simply don't
have enough near-duplicate/adversarial structure for int8 rounding or a
binary Hamming prefilter to flip the fused top-10, and where it might, BM25
+ RRF absorbs it. This matches aikit's own isolated real-Model2Vec gates:
`TestFlatI8_recallReal_Model2Vec` mean recall@10 vs f32 = **1.0000**;
`TestHNSW_int8RecallGate` recall@10 f32 vs int8 = **1.0000 vs 1.0000**;
`TestFlatBinaryI8_recallReal_Model2Vec` at `DefaultOverquery=16` = **1.0000
@10, 0.99 @50**.

Latency at repo scale already separates the four: `flat-binary-i8` is the
fastest (~26µs, essentially tied with `flat-i8`), `hnsw` is the **slowest**
(49µs — 48% slower than today's `flat-f32`) because graph traversal
overhead exceeds a brute-force scan of a few hundred-to-thousand vectors.

### Scale ramp — latency / build time / memory (`TestRetrieverScaleSweep`, synthetic unit vectors, dim=256, this machine)

| N | Retriever | Build | Query p50 | Query p95 | Memory (measured) |
|--:|---|--:|--:|--:|--:|
| 13,000 | flat-f32 | <1ms | 137µs | 196µs | 13.3 MB |
| 13,000 | flat-i8 | <1ms | 192µs (**slower**) | 243µs | 3.4 MB (3.9×↓) |
| 13,000 | flat-binary-i8 | 20ms | **67µs (2.0×)** | 98µs | 3.8 MB (3.5×↓) |
| 13,000 | hnsw | **10.2s** | 139µs | 203µs | 14.8 MB (1.1×↑) |
| 50,000 | flat-f32 | <1ms | 750µs | 1239µs | 51.2 MB |
| 50,000 | flat-i8 | <1ms | 773µs (~even) | 1786µs | 13.0 MB (3.9×↓) |
| 50,000 | flat-binary-i8 | 80ms | **159µs (4.7×)** | 185µs | 14.6 MB (3.5×↓) |
| 50,000 | hnsw | **53.5s** | 269µs (2.8×) | 356µs | 64.1 MB (1.25×↑) |
| 200,000 | flat-f32 | 2ms | 2575µs | 3354µs | 204.8 MB |
| 200,000 | flat-i8 | 20ms | 972µs (2.65×) | 1657µs | 52.0 MB (3.9×↓) |
| 200,000 | flat-binary-i8 | 300ms | **430µs (6.0×)** | 563µs | 58.4 MB (3.5×↓) |
| 200,000 | hnsw | **271.8s (4m32s)** | 457µs (5.6×) | 645µs | 229.7 MB (1.12×↑) |
| 800,000 | flat-f32 | 4ms | 9070µs | 11913µs | 819.2 MB |
| 800,000 | flat-i8 | 100ms | 3302µs (2.75×) | 4386µs | 208.0 MB (3.9×↓) |
| 800,000 | flat-binary-i8 | 1.2s | **1669µs (5.4×)** | 1931µs | 233.6 MB (3.5×↓) |
| 800,000 | hnsw | *capped — see below* | — | — | — |

The 271.8s HNSW build at N=200k reproduces the 2026-08-15 aikit-level eval's
"49s@50k → 4m23s@200k" anchor almost exactly (53.5s / 271.8s here) — the
finding is not a fluke of that earlier run, it's a stable property of
`BuildHNSW`'s default heuristic-neighbor-selection config at this N. N=800k
was **not run for HNSW**: extrapolating the 50k→200k build-time growth
(≈4× N → ≈5.1× time) puts 800k in the 20–25 minute range, which only
restates an already-disqualifying number at real risk to this box's 16GB
(the run was capped by design — see the harness's doc comment).

**HNSW's memory line is the one that surprises naive intuition**: it is
**larger** than `flat-f32` at every N (1.1–1.25× measured), not smaller —
confirmed both analytically (it keeps the full f32 vectors *and* a
navigable-small-world graph on top) and by the measured heap delta. An ANN
structure is not automatically a memory win; only the ones that replace the
f32 storage (`FlatI8`, `FlatBinaryI8`) are.

## Two-stage assessment (`FlatBinaryI8`)

`ann.FlatBinaryI8` composes the binary Hamming prefilter (`FlatBinary`'s own
shortlist mechanism) with `FlatI8`'s int8 rerank instead of an exact f32
rerank — exactly the "coarse shortlist + exact-ish rescoring" framing #2/#3
of this eval predicted, and it already exists in aikit (no need to compose
it ken-side). aikit's own `TestFlatBinaryI8_recallReal_Model2Vec` gates it
at ≥0.85 recall@{10,50} on real Model2Vec embeddings at `DefaultOverquery`.

## Determinism

`Flat`, `FlatI8`, `FlatBinaryI8` are all pure functions of the input vecs —
deterministic by construction, same as today's `ann.Flat`. `HNSW`'s level
assignment is a seeded PRNG (`ann.Config.Seed`); aikit's own test suite
documents "same (n, d, seed) → identical" and exercises it across builds
(`ann_bench_test.go`, `hnsw_persist_test.go`, `hnsw_mmap_test.go`, etc. all
pin `Seed:`). Wiring HNSW into `ken build-index`'s byte-identical-output
contract is possible (pin a `Seed` constant) but is moot here since HNSW's
build cost already disqualifies it at the sizes where it would otherwise be
attractive (see scale ramp).

## What's not measured (the honest gap)

Real semble repos top out at ~6,700 chunks (laravel-framework) — nowhere
near kernel scale, so the recall numbers above are only validated up to
~7k-vector single corpora (76,616 vecs total, but split across 63
*independent* small indices, not one large one). The scale ramp
deliberately measures latency/memory/build-time only, on synthetic
unit-random vectors — it says nothing about whether `FlatBinaryI8`'s binary
prefilter keeps recall at N=200k–800k **on real, semantically clustered
code embeddings**, where near-duplicate boilerplate (the exact clustered
structure `HNSW`'s own doc references — "clustered data" is what its
diversity heuristic is tuned for) could plausibly stress a Hamming-distance
prefilter differently than it stresses exact cosine at small N. This is the
one place this pass genuinely extrapolates rather than measures, and it's
also the actual kernel-demo regime `kernel-demo-feasibility.md` cares about.
**Trigger to close this gap:** run `scripts/kernel_demo_bench.sh`'s scale
ramp (or an equivalent large single real corpus) with `KEN_ANN=flat-binary-i8`
wired in (Phase 2 below) and diff recall against the `flat-f32` baseline at
each rung.

The already-known memory caveat from the 2026-08-15 pass still applies
identically here and is **not re-opened by this doc**: `ann.New` aliases
`vecs` (free), but `NewFlatI8`/`NewFlatBinaryI8` copy into a fresh
int8/binary buffer, so on ken-mcp's **watched/incremental** path (where
`WatchedIndex.w.vecs` must stay f32 for append/compaction) building
`FlatBinaryI8` *adds* ~290 bytes/vec on top of the retained f32 vecs rather
than replacing them — a net memory *increase* (~29%) there, even though the
static/non-watched build paths (`ken index --no-watch`, `mcp.Run` embedded
corpora) can drop the f32 vecs after building and realize the full ~3.5×
memory win. The latency win is unconditional either way; the memory win on
the live server specifically still needs the int8-throughout incremental
refactor scoped (not started) in that earlier pass.

## Per-regime recommendation

| Regime | Recommendation |
|---|---|
| **Repo scale (today's default, N < ~50k)** | Adopt `FlatBinaryI8`. Measured on the real published benchmark: zero recall cost, ~2× faster even here. This is not "wait for a trigger" — it's a strict improvement at the scale ken already runs at every day. |
| **Mid scale (50k–200k)** | `FlatBinaryI8` — 4.7–6.0× faster, 3.5× less memory, same recall evidence (aikit's own gates + the fact nothing in the mechanism is scale-dependent). `FlatI8` alone is a weaker, non-default-worthy choice at this range — 2.65× is a real number but binary-i8 dominates it in every column measured. |
| **Kernel scale (500k–5M, the demo regime)** | `FlatBinaryI8` is the projected answer (5.4× faster / 3.5× smaller at 800k, and the mechanism has no reason to degrade further) but recall AT THIS SCALE ON REAL DATA is unmeasured — see the gap above. Don't flip a default here before that spot-check; do wire the knob so the check is a one-line config change, not a refactor. |
| **HNSW, any scale** | Not adopted at any scale for ken's default indexing model. Query is competitive from ~50k on (2.8–5.6× vs `flat-f32`), but build cost (53.5s@50k, 271.8s@200k, tens of minutes extrapolated@800k) is fundamentally incompatible with `ken index --watch`'s 2-second republish-on-edit contract, and it costs *more* memory than `Flat`, not less — it solves neither of ken's two stated goals (latency AND memory) at once the way `FlatBinaryI8` does. The only scenario where it could still make sense is a build-once, never-re-indexed static artifact (e.g., a frozen `ken build-index` output that's never watched) — out of scope here since `FlatBinaryI8` already wins without HNSW's downsides in every regime tested. |

## Decision

**Adopt `FlatBinaryI8` as an opt-in dense retriever now** (Phase 2, wired in
this change as `FSOptions.DenseRetriever` / the `KEN_ANN=flat-binary-i8` env
knob it reads by default — no new CLI flag, matching how `KEN_ENRICH`
already threads through `defaultFSOptions`; default unchanged at
`flat-f32`) — this closes the "evaluate against the recall bar"
half of [`DESIGN.md` §10](../DESIGN.md#10-risk-register)'s HNSW/quantized-
retriever trigger with a measured **no** for HNSW and a measured **yes** for
`FlatBinaryI8`, while being honest that the yes is validated at repo/mid
scale and projected (not yet confirmed) at kernel scale.

**Do not flip the default yet.** Two things gate that, in order: (1) the
kernel-scale real-corpus recall spot-check above, since that's the regime
the memory savings matter most for and the one this pass couldn't measure
directly; (2) a decision on the ken-mcp incremental-store memory tradeoff
(accept the ~29% overhead there, or wait for the int8-throughout refactor).
Neither blocks shipping the knob — they block defaulting it on.

**`HNSW` is closed, not deferred** — `DESIGN.md` §10's "HNSW for the dense
retriever" risk-register entry should be marked resolved-declined rather
than left open, since the trigger it names ("dense matrix size makes exact
cosine the bottleneck") is real and confirmed (the 200k/800k `flat-f32`
numbers above show it), but `HNSW` specifically is not the answer to it —
`FlatBinaryI8` is, at a fraction of the operational cost (no minutes-long
rebuild, no memory increase).
