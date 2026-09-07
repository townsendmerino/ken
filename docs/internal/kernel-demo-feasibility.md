# Kernel demo feasibility — measure before you commit

**Question:** is "ken takes on the Linux kernel" a good demo for the performance work (aikit v1.4 SIMD f32 dot kernel, v1.5 int8 reranker)?

**Short answer:** it's a great *headline* and a risky *perf* demo, because the kernel lands squarely on ken's flat O(N) semantic scan — the one place ken hasn't taken the scaling lever its own dependency already ships. Don't commit on extrapolation — run `scripts/kernel_demo_bench.sh` on the M1 Pro first and let the curve decide.

## Why it's risky (from ken's own docs)

The semantic arm (`aikit/ann.Flat.Query`) is brute-force cosine, **O(N) in chunks**, and ken wires only the f32 `ann.Flat` (`ann.New(vecs)` in `internal/search/index.go`). The approximate/quantized retrievers that fix this — `HNSW`, `FlatI8`, `FlatBinary` — are **shipped in aikit** (HNSW since v0.2.0) but **not yet wired into ken**; the swap is a constructor change gated on a trigger ([DESIGN.md §10](../DESIGN.md#10-risk-register)). The SIMD win was measured at ~13k chunks (laravel) and is a **constant factor on an O(N) scan** — it moves the wall later, not away. The kernel is the "Large" row in [PERF-expectations.md](../PERF-expectations.md): **~80k files, extrapolated ~5M chunks**, ~10–30 min full hybrid index, with memory-at-huge-scale listed as unmeasured and treesitter "not recommended at this scale."

Linear extrapolations from the documented anchors (M1 Pro):

| Quantity | Anchor | Kernel (~5M chunks) extrapolated |
|---|---|---|
| Hybrid index time | 378k chunks → ~45 s | **~10 min** |
| Hybrid query p50 (O(N) scan) | 13k chunks → 1.56 ms | **~600 ms** |
| Embedding matrix RAM | unmeasured | **multiple GB** (→ embedded-binary `mcp.Run` demo balloons to multi-GB, breaking the "single small static binary" pitch) |

A ~600 ms p50 is the opposite of the "instant" story a perf demo needs. That's the risk: the full kernel hides your constant-factor win under O(N) and may showcase the wall instead.

## The real lever: bytes per candidate (not multiply-width)

An archsimd evaluation in aikit (2026-09, `docs/task-archsimd-eval.md`) settled *why* wider SIMD is the wrong lever here: **`ann.Flat.Query` is memory-bandwidth-bound ~11× over**. A dot product reads each operand once and reuses nothing (0.25 MAC/byte, fixed at every dimension), while the box's roofline ridge is 2.74 MAC/byte; production `Flat.Query` already shards across cores and plateaus at ~90% of the DRAM read ceiling while using ~8% of the arithmetic. A faster multiply optimizes the 92% that's already idle — so both the f32 dot kernel and the int8 reranker were NO-GO for Go's `simd` (bandwidth, plus VPDPBUSD isn't in the API).

The lever the roofline points at is **fewer bytes streamed per candidate**, and every rung already exists in aikit — it just needs wiring into ken's pipeline:

- **`FlatI8`** — f32→int8 measured ~4.44× at d=768/N=200k (against a 4× byte reduction — the near-exact match *is* the bandwidth-bound confirmation). Mild recall cost; the likeliest first swap.
- **`FlatBinary`** — ~11.6×, larger recall cost; the "how fast can it possibly go" bound.
- **`HNSW`** — changes the complexity class (sub-linear), not just the constant; the real answer to "the kernel is 5M chunks."

**Update (2026-09-06, measured — see
[`docs/internal/dense-retriever-adoption-2026-09.md`](dense-retriever-adoption-2026-09.md)
for the full evaluation, roadmap A2):** all three were evaluated against the
0.967 NL / 0.995 symbol recall bar through ken's real RRF fusion, not just
inferred from theory, and then — the same day — against a real 584k-chunk
sparse Linux kernel checkout (the exact regime this doc is about), which is
what this script sparse-clones. The result has two parts, because the
kernel-scale check revised the first one:

- On the real 63-repo/1251-query semble benchmark (repo/mid scale), `FlatBinaryI8`
  (binary Hamming prefilter + int8 rerank — a two-stage retriever added to
  aikit since this doc was first written) wins outright — 2× faster than
  `Flat` at N=13k with zero measured end-to-end recall cost (pipeline
  recall@10 identical to 3 decimals across `Flat`/`FlatI8`/`FlatBinaryI8`/
  `HNSW`).
- On the real 584,019-chunk kernel checkout (`arch/x86 drivers fs kernel mm
  net sound` at v6.6) — the actual regime this document is about — that
  result **did not hold**: `FlatI8` matched exact `Flat`'s top-10 essentially
  perfectly (agree@10 1.00 on realistic domain queries, 0.98 on a
  near-duplicate stress test) at 5.5× the query speed, but `FlatBinaryI8`'s
  agreement dropped to 0.96 on both — a real, quantified gap the repo-scale
  benchmark and the earlier synthetic-vector scale ramp both missed, because
  it's specific to real code's semantic clustering at real scale meeting the
  binary prefilter. **`FlatI8`, not `FlatBinaryI8`, is the answer for a real
  kernel-scale corpus.**

`HNSW`'s query is competitive from ~50k vecs on but its build cost reproduces
the 4m23s@200k anchor almost exactly (271.8s measured) and it costs *more*
memory than `Flat`, not less (keeps the f32 vectors AND a graph) —
disqualified for `ken index --watch`'s 2-second republish contract at any
scale. Both `FlatI8` and `FlatBinaryI8` are now wired in as opt-in
(`KEN_ANN=flat-i8` / `flat-binary-i8`, default unchanged); a full-kernel
demo today should reach for `flat-i8`, not `flat-binary-i8`, given the
measured gap above. (Separately, the kernel-scale run also surfaced that Arm
B structural enrichment can hang indefinitely on a real large driver tree —
see the findings doc's callout; run with `KEN_ENRICH=off` if reproducing this
until that's investigated.)

The original next step this whole thread pointed to — **evaluate
`FlatI8`/`FlatBinary`/`HNSW` in ken's hybrid path against the 0.967 NL /
0.995 symbol recall bar** — a recall-vs-latency measurement, using this same
harness, not a compute-kernel rewrite — is now done; the paragraph above is
the answer.

## What the harness measures

`scripts/kernel_demo_bench.sh` walks a scale ramp of subsystems (default `fs/ext4 fs/xfs fs/btrfs` → `fs` → `fs mm kernel` → `fs mm kernel net`) and records, per (corpus, mode): chunk count, cold index time, warm query p50/p95, and OS-level peak RSS (via `/usr/bin/time -l`). It runs `bm25` and `hybrid` by default (add `hybrid-rerank` via `MODES=`). It uses ken's own `ken perf index|search` JSON, so the numbers are publishable under the same discipline as the rest of `PERF-expectations.md`.

**Read the result as a curve, not a point:** plot `p50_ms` against `chunks`. Flat-ish = headroom; a knee that climbs with chunk count = the O(N) flat-ANN wall. bm25 has no cosine scan, so its line is the control. Compare hybrid-vs-bm25 RSS to see the embedding-matrix cost.

## Decision rule

- **If subsystem-scale hybrid p50 stays low and RSS is sane** → the full-kernel demo is viable; re-run on the whole tree for the headline number and ship it with real numbers.
- **If p50 climbs steeply across the ramp** → don't demo full-kernel *hybrid*. Two honest alternatives:
  1. **Scope to a subsystem** ("ken searches the kernel's networking stack") — impressive, stays under the wall, and is the natural place the SIMD win is still visible.
  2. **Full-kernel in bm25 mode**, framed as indexing throughput + lexical search at kernel scale (sidesteps the O(N) scan and the multi-GB matrix; costs the ~14 pp hybrid recall).
- **Either way**, the perf work itself (SIMD f32, int8 reranker, ~21× less reranker RAM, 3× hybrid p50) demos best on a small–medium interactive corpus where sub-2 ms p50 is *visible* — the kernel's O(N) scan masks exactly the win you'd be showcasing.

## Credibility angle (consulting goal)

"I measured ken honestly against the kernel, found the flat-ANN wall at ~N chunks, then measured the fix — wiring aikit's already-shipped `FlatI8`/`FlatBinary`/`HNSW` — against the recall bar" is a stronger engineering story than a cherry-picked win: it shows you know your own complexity curve *and* your dependency's toolbox. Bonus credibility, and squarely on-brand for the pure-Go-no-cgo thesis: the archsimd NO-GO (`docs/task-archsimd-eval.md`) is itself a publishable artifact — a rigorous *no*, backed by a roofline, is rarer and more trustworthy than a cherry-picked yes. The harness output is what both stories are built on.

## Note on sandbox measurement

This couldn't be run in the assistant's Linux sandbox: no Go 1.26.4 (official downloads, the module proxy, and `golang.org/x` are all network-blocked there; only github.com is reachable), and the repo's prebuilt binaries are macOS. The M1 Pro is the correct machine anyway — it's the baseline `PERF-expectations.md` standardizes on, so the numbers are directly comparable and publishable.
