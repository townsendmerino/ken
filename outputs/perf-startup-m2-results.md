# M2 — Lazy Rerank Model Load

**Verdict:** Shipped. **491 ms cut from ken-mcp startup** when
`KEN_MCP_RERANK=on` but the user hasn't yet issued a hybrid+rerank
query. ken-mcp startup with rerank enabled is now indistinguishable
from rerank disabled (~30 ms median). The rerank model load + cache
hydration moves to the first hybrid+rerank query — for users whose
first call is a plain hybrid search (the common case in ken-mcp's
default config), the 491 ms cost is never paid at all.

## What changed

- `internal/search/lazy_reranker.go` — new `LazyReranker` type that
  wraps a `func() (Reranker, error)` closure and defers it until the
  first `Rerank`/`RerankWithTelemetry` call. `sync.Once` for thread-
  safe single-load. `Loaded()` and `Inner()` accessors so the
  shutdown cache-save path can operate on the underlying
  `*NeuralReranker` without re-running the loader.
- `cmd/ken-mcp/main.go` — when `KEN_MCP_RERANK=on`:
  - Config resolution (rerank model dir, top_n, beta, adaptive,
    cache path) stays eager — cheap env reads.
  - The expensive 3-step block (`encoder.Load` ~491 ms +
    `NewNeuralReranker` + `LoadCacheFromFile`) is captured in a
    closure passed to `NewLazyReranker`.
  - `SetReranker(lazyReranker, options...)` registers the lazy
    instance on every WatchedIndex; the orchestrator (`apply
    RerankerWithTelemetry`) dispatches through it on
    `ModeHybridRerank` queries.
  - Shutdown cache save is now guarded on
    `lazyReranker.Loaded() && lazyReranker.Inner().(*NeuralReranker)`
    — when no rerank query ever lands, the disk file stays untouched.
- Startup log line `rerank: lazy-load configured (...)` confirms
  the LazyReranker was created without calling `encoder.Load`.
  Subsequent `rerank: loaded ... on first query (...)` log line
  fires the first time a hybrid+rerank query lands.

## Measurement

`scripts/perf_startup_m2.sh` — bash harness that times ken-mcp
process-start → first `starting ...` log line. 5 iterations per
cell, perl Time::HiRes for sub-ms wall.

| Cell | Median | Min | Max |
|---|---:|---:|---:|
| `KEN_MCP_RERANK` unset (baseline) | 31.14 ms | 28.50 ms | 709.65 ms |
| `KEN_MCP_RERANK=on` (M2 lazy) | **30.15 ms** | 29.85 ms | 31.88 ms |

The baseline 709.65 ms max is a one-time iteration outlier — likely
disk-cache warmup on the test binary; median across 5 runs filters
it cleanly. The rerank-on cell is tight (29.85-31.88 ms).

**Pre-M2 the rerank-on cell would have paid ~491 ms (M0 baseline)
before printing the `starting` line.** Post-M2 it's 30 ms.

Delta: **491 ms removed from the cold-start critical path** when
the user does not immediately rerank.

## What this does NOT change

- **First rerank-requiring query is +491 ms.** The cost moves; it
  doesn't disappear. For agents that issue a hybrid+rerank query as
  their first call, M2 is a wash — the 491 ms lands on the query
  wall rather than the startup wall. Acceptable trade since the
  agent gets a tool response with results that came from a useful
  reranker.
- **Steady-state rerank latency is unchanged.** Once loaded, calls
  flow through the same `NeuralReranker.RerankWithTelemetry`
  path, with the same M9 persistent cache, M10 streaming, M11
  attention alloc improvements. No behavior change on the warm
  path.
- **Persistent cache semantics.** Today: load at startup, save at
  shutdown. Post-M2: load at first-rerank (inside the loader
  closure), save at shutdown IFF loader ran successfully. The
  cache file is untouched when ken-mcp boots `KEN_MCP_RERANK=on`
  but no rerank query lands — desirable since there's nothing new
  in memory to write back.

## Tests

`internal/search/lazy_reranker_test.go` — 5 tests pinning:

1. **Defers load until first call** — loader call count is 0 after
   construction, 1 after first Rerank, 1 after subsequent calls
   (single-shot).
2. **Concurrent first-callers** — 50 goroutines firing the first
   call concurrently see the loader run exactly once;
   `sync.Once` cooperates without redundant work.
3. **Loader error passes through as nil** — `Rerank` returns nil
   (orchestrator's pass-through signal) on loader error. `Err()`
   surfaces the captured error. Subsequent calls do not retry.
4. **`Inner()` accessor** — returns nil before load; returns the
   loaded reranker after. Used by the cache-save path.
5. **`RerankWithTelemetry` fallback** — non-telemetry inner reranker
   gets the plain `Rerank` fallback (matching
   `applyRerankerWithTelemetry`'s pattern).

All pass; full suite (`go test ./...`) green.

## Hypothesis verdict update

From M0, H1 (rerank load dominates cold-start) was **confirmed at
tiny scale** (78% of cold-start budget on testdata/repo). M2
addresses this directly. Post-M2, H1's tiny-scale share drops to ~0%
when no rerank query lands. The remaining startup cost is process
init + `embed.Load` (~52 ms) + `search.FromFS` (size-dependent) +
`structural.Build` (the M4 target).

## Next

M4 (parallel `structural.Build`) is the next milestone. M0 measured
1577 ms on jekyll (167 Ruby files single-threaded); parallelization
on the per-CPU worker pattern v0.8.7 used should land near
~200-300 ms on this hardware. Expected wall savings on large
corpora: ~1.3 s.

## Reproduction

```bash
# Measurement (5 iterations per cell, ~30s total)
scripts/perf_startup_m2.sh 5

# Smoke: confirm lazy-load log line, then exit
KEN_MCP_RERANK=on KEN_MCP_RERANK_MODEL_DIR=~/.ken/rerank-model \
  KEN_MCP_LOG_LEVEL=info \
  go run ./cmd/ken-mcp 2>&1 < /dev/null &
PID=$!; sleep 1; kill $PID
# Expect to see: "rerank: lazy-load configured (...)"
```
