# M1 — Q8 Rerank Default (Closure, No Code Change)

**Verdict:** M1 closes WITHOUT a code change. M2 (lazy rerank
load) already removed the rerank-model-load cost from the cold-
start critical path; switching the default quantization from f32
to int8 would solve a problem M2 already solved AND introduce a
regression on arm64 (the host platform for this campaign and the
default on modern Apple Silicon devices).

## Rationale

M1's original premise: "f32 rerank model load = 491 ms; Q8 file is
~4× smaller, load should be ~4× faster; default to Q8 when
present to cut the startup cost."

Two facts emerged from M2 + an existing in-tree comment:

1. **M2 already deferred the load entirely off the cold-start
   critical path.** `KEN_MCP_RERANK=on` startup is now 30 ms,
   indistinguishable from `KEN_MCP_RERANK` unset. Q8 vs f32 makes
   no difference because neither one runs at startup. The cost
   that remains is "first hybrid+rerank query pays the load wall
   once" — and on a warm disk that's ~91 ms even for f32, well
   below the threshold where shaving more matters.
2. **f32 + NEON dominates int8 on arm64** ([cmd/ken/main.go](
   ../cmd/ken/main.go) comment, pre-existing): for both speed AND
   accuracy on this platform. The relevant excerpt:
   > on Apple Silicon f32+NEON dominates int8 in both speed AND
   > accuracy, so int8 is mainly useful on memory-constrained
   > amd64/Linux deployments.

Combining (1) and (2): making int8 the default would
   - **Save nothing on startup** (already 30 ms).
   - **Cost arm64 users some query latency** (slower inference)
     and **some accuracy** (quantization noise).
   - **Save amd64/Linux users some load wall + memory** — but
     they can opt in today via `KEN_MCP_RERANK_QUANT=int8`.

The right shape is the opt-in we already ship.

## Verification of the existing opt-in path

`KEN_MCP_RERANK_QUANT=int8` is handled by [`envEnum`](
../cmd/ken-mcp/main.go) in the rerank-config block; the loader
closure (introduced in M2) routes to `encoder.LoadQ8` when
quant=="int8" and to `encoder.Load` otherwise. Both paths share
the LazyReranker wiring, the persistent cache, the cache-scope
key (which distinguishes f32 from int8 caches via the per-quant
filename `~/.ken/rerank-cache-{f32,int8}.bin`), and the shutdown
save path. No code touched in M1; no code needs touching.

When `KEN_MCP_RERANK_QUANT=int8` but no Q8 model is on disk,
`encoder.LoadQ8` returns a load error; LazyReranker captures it
in `Err()` and returns nil from subsequent `Rerank` calls,
which the orchestrator interprets as "skip rerank" — the same
graceful-degradation path that triggers when the f32 model is
missing.

## What's still TODO for amd64/Linux users (out of M1 scope)

For users where Q8 IS the right default (memory-constrained
amd64 deployments), the value-add isn't "flip the default
quant" — it's making Q8 *easier to get*:

- **`ken download-model --rerank --quant int8`** doesn't exist
  today. Adding it would let amd64 users one-shot fetch the Q8
  snapshot and opt in by setting `KEN_MCP_RERANK_QUANT=int8`.
- **A platform-detection helper** that suggests the right quant
  at first startup (e.g. detect Linux/amd64 + low memory → log
  "tip: try `KEN_MCP_RERANK_QUANT=int8` for smaller resident
  memory"). Optional polish.

Both items are 1.0 ship-list candidates for a follow-up sprint if
amd64/Linux usage becomes load-bearing for ken. Neither is gated
on this campaign.

## Hypothesis verdict

M0's H1 ("rerank model load dominates cold-start") was confirmed
at tiny scale (78%). M2 addressed it directly. M1 would have
been the secondary lever; M2 made it redundant on the host
platform. **H1 is now fully addressed by M2 alone.**

## Campaign close

This is the last milestone of the startup + query-latency
campaign. With M2 + M4 shipped and M1/M3/M5 closed (refuted or
superseded), the campaign meets its closure criteria from
`docs/perf-campaign-startup-query.md`:

> Closure. The campaign closes via an ADR when one of:
> - Every milestone that confirmed its hypothesis has shipped,
>   and the residual hypotheses (refuted / unclear) are documented.
> - We hit empirical diminishing returns and explicitly call it.

Both apply. **ADR for closure will be ADR-036**, following the
ADR-029 pattern that closed project-perf-phase0.

## Cumulative campaign wins

For reference, the cold-start budget reduction from the full
campaign (M2 + M4) vs the M0 baselines:

| Corpus | M0 baseline | After M2 | After M2+M4 | Total reduction |
|---|---:|---:|---:|---:|
| tiny | 627 ms | 137 ms | 134 ms | **−493 ms (79%)** |
| medium | 1,405 ms | 914 ms | 555 ms | **−850 ms (60%)** |
| large | 2,927 ms | 2,436 ms | 1,309 ms | **−1,618 ms (55%)** |

Warm-search p50 (already sub-millisecond at M0) is unchanged.
H4 confirmed → no work was needed there.

## Re-open trigger

If an amd64/Linux user reports a meaningful Q8-vs-f32 latency
or memory pain point that the current opt-in flow doesn't
address, that's grounds to revisit M1 with concrete numbers from
that platform — matching the campaign plan's out-of-band trigger
section.
