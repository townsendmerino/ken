# Kernel demo feasibility — measure before you commit

**Question:** is "ken takes on the Linux kernel" a good demo for the performance work (aikit v1.4 SIMD f32 dot kernel, v1.5 int8 reranker)?

**Short answer:** it's a great *headline* and a risky *perf* demo, because the kernel lands squarely on ken's one unshipped scaling component. Don't commit on extrapolation — run `scripts/kernel_demo_bench.sh` on the M1 Pro first and let the curve decide.

## Why it's risky (from ken's own docs)

The semantic arm (`aikit/ann.Flat.Query`) is brute-force cosine, **O(N) in chunks**; HNSW is on the risk register, unshipped ([DESIGN.md §10](../DESIGN.md#10-risk-register)). The SIMD win was measured at ~13k chunks (laravel) and is a **constant factor on an O(N) scan** — it moves the wall later, not away. The kernel is the "Large" row in [PERF-expectations.md](../PERF-expectations.md): **~80k files, extrapolated ~5M chunks**, ~10–30 min full hybrid index, with memory-at-huge-scale listed as unmeasured and treesitter "not recommended at this scale."

Linear extrapolations from the documented anchors (M1 Pro):

| Quantity | Anchor | Kernel (~5M chunks) extrapolated |
|---|---|---|
| Hybrid index time | 378k chunks → ~45 s | **~10 min** |
| Hybrid query p50 (O(N) scan) | 13k chunks → 1.56 ms | **~600 ms** |
| Embedding matrix RAM | unmeasured | **multiple GB** (→ embedded-binary `mcp.Run` demo balloons to multi-GB, breaking the "single small static binary" pitch) |

A ~600 ms p50 is the opposite of the "instant" story a perf demo needs. That's the risk: the full kernel hides your constant-factor win under O(N) and may showcase the wall instead.

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

"I measured ken honestly against the kernel, found the flat-ANN wall at ~N chunks, and that's the empirical case for the HNSW work on the risk register" is a stronger engineering story than a cherry-picked win — it demonstrates you know your own complexity curve. The harness output is the artifact that story is built on.

## Note on sandbox measurement

This couldn't be run in the assistant's Linux sandbox: no Go 1.26.4 (official downloads, the module proxy, and `golang.org/x` are all network-blocked there; only github.com is reachable), and the repo's prebuilt binaries are macOS. The M1 Pro is the correct machine anyway — it's the baseline `PERF-expectations.md` standardizes on, so the numbers are directly comparable and publishable.
