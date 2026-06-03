# Vetted demo queries — Go stdlib (Go 1.26.3)

Phase 1 evidence for the flagship "ken indexes the Go standard library"
demo. Every query below was **actually run against the curated corpus**
(`$GOROOT/src` minus `cmd/` minus `*/testdata/*` on Go 1.26.3) and the
documented answer is ken's actual top hit, not aspirational copy.

Reproduce with [`scripts/stdlib_demo_vet.go`](../../scripts/stdlib_demo_vet.go)
for category (A), and [`scripts/stdlib_phase1_close.go`](../../scripts/stdlib_phase1_close.go)
for categories (B) and (C). The corpus assembly is in
[`README.md` → Reproduce against your own `$GOROOT/src`](README.md#reproduce-against-your-own-gorootsrc).

**Verification posture.** Every Go developer knows the stdlib internals;
verify each answer by opening the cited file. If ken's top hit is wrong
on your machine — modulo line-number drift from a different Go version
— that's a real bug, not a demo flub. See ken's
[audit rubric](../transcript-audit-rubric.md) for the framing.

## (A) Semantic-bridging — "grep would never have found that"

These are the differentiator. Natural-language queries that find the
right code *without sharing its words* — the case for hybrid retrieval.

| # | Query | Canonical answer | Why it's a demo query |
|---|---|---|---|
| 1 | "where is goroutine scheduling decided" | `runtime/proc.go::schedule` / `findRunnable` neighborhood | The query says nothing about `proc` or `findRunnable`; ken bridges "goroutine scheduling" → the scheduler's main loop. |
| 2 | "how does context cancellation reach an in-flight HTTP request" | `net/http/transport.go::prepareTransportCancel` (literal doc: "sets up state to convert Transport.CancelRequest into context cancelation") | Crosses two stdlib packages (`context` ↔ `net/http`) and lands on the exact function whose comment IS the answer. |
| 3 | "the code that grows a map when it's full" | `internal/runtime/maps/map.go::h1` neighborhood (the new Swiss-table) + `runtime/map.go::hashGrow` (rank 2 — the historical hashGrow narrative) | Modern Go's Swiss-table replaced `runtime/map.go` in 1.24; ken correctly surfaces the new home and keeps the historical version as a fallback context. |
| 4 | "where do timers actually fire" | `runtime/time.go::runtimer` neighborhood | "Fire" appears nowhere in the canonical answer; ken bridges firing semantics to the runtimer logic. |
| 5 | "where do goroutines block on a full channel" | `runtime/chan.go` send/recv with the block-flag comment | The chan.go preview explicitly says "If block is not nil, then the protocol will not sleep but return" — exactly what the query asked about. |
| 6 | "parsing struct tags for JSON field names" | `encoding/json/v2_encode.go` + `encoding/json/encode.go` (the struct-tag handling neighborhood) | Both v1 and v2 paths surface; agent sees that JSON struct-tag parsing lives across both. |
| 7 | "how a mutex blocks a goroutine" | `sync/mutex.go` (top hit; demo capture should point at the chunk containing `internal/sync/mutex.go::lockSlow` for the body of the slow-path) | sync/mutex.go is the public surface; the actual blocking logic lives in `lockSlow` under `internal/sync/`. Demo capture should walk both. |

**Honesty notes:**
- Query #4 (timers) was vetted against the Go 1.26.3 runtime/time.go layout. Different Go versions may chunk this differently; line numbers drift but the file is right.
- Query #7 (mutex): ken's top hit is `sync/mutex.go:1-48` — that's the package header, not the lockSlow body. The capture should walk to rank 2 (`internal/sync/mutex.go::lockSlow`) for the actual blocking implementation; both files contain the canonical answer.

## (B) Structural tools — exact-answer lookups (no ranking)

The new Track 2 capability. These resolve by name (tree-sitter-grade), not by retrieval. Every result either exists or doesn't.

| # | Query | Result | Notes |
|---|---|---|---|
| B1 | `definition("WithCancel")` | 1 hit, kind=func, file=`context/context.go` | Single-site resolution. |
| B2 | `references("Marshal")` | **57 reference files** across `crypto/x509`, `crypto/elliptic`, `encoding/asn1`, `encoding/json`, etc. (first 10 captured) | Demonstrates "who calls X" at file-level granularity. |
| B3 | `outline("net/http/server.go")` | **186 outline entries** (capture shows top ~20) | Demonstrates the full file surface — every func / type / method in source order. |
| B4 | `outline("encoding/json/encode.go")` | **51 outline entries**, starting with the public encoder surface (`Marshal`, `MarshalIndent`, then `newEncodeState`, `valueEncoder`, ...) | Cleaner "the surface" demo than `symbols("encoding/json")` would be — file-scoped, no test-helper leakage. |

**Honesty notes:**
- `references()` works for names that appear as **call sites, imports, or raise/panic targets**. Sentinel values like `io.EOF` aren't call sites and don't surface here — by design (per `internal/structural/lookups.go`'s comment about scope-analysis cost). For "where is `io.EOF` used," fall back to `search`.
- `symbols("encoding/json")` returns 768 entries on this corpus because ken indexes test files too. For "the public surface of package X", use `outline()` on the package's main file instead — cleaner, more accurate, less noise.

## (C) Head-to-head vs grep — the persuasive artifact

The case for ken in one screen. For each query: the literal grep command a Go dev would type if `ken search` didn't exist, and what grep returns. ken's answer is the corresponding query from (A).

### C1. "where is goroutine scheduling decided"

```sh
grep -rn 'schedule' "$GOROOT/src/runtime/" | wc -l
# 230 matches across 58 files
```

First 3 grep hits (representative of the noise):

```
runtime/proc.go:24:// Goroutine scheduler
runtime/proc.go:25:// The scheduler's job is to distribute ready-to-run goroutines...
runtime/proc.go:40:// (1) scheduler state is intentionally distributed (in particular...
```

**ken's answer:** the `proc.go` chunk containing `findRunnable` — the scheduler's actual main loop, not just a comment header.

### C2. "how does context cancellation reach an in-flight HTTP request"

```sh
grep -rn 'cancel' "$GOROOT/src/net/http/" | wc -l
# 345 matches across 22 files
```

First 3 grep hits:

```
net/http/transport.go:154:	// to cancel dials as soon as they are no longer needed...
net/http/transport.go:174:	// to cancel dials as soon as they are no longer needed...
net/http/transport.go:527:	ctx    context.Context // canceled when we are done with the request
```

**ken's answer:** `net/http/transport.go::prepareTransportCancel` — the exact function whose comment IS the answer ("sets up state to convert Transport.CancelRequest into context cancelation").

### C3. "where do goroutines block on a full channel"

```sh
grep -rn 'block' "$GOROOT/src/runtime/" | wc -l
# 1,103 matches across 131 files
```

First 3 grep hits:

```
runtime/proc.go:32://     blocked or in a syscall w/o an associated P.
runtime/proc.go:970:	// bomb from something like millions of goroutines block...
runtime/proc.go:1338:	// - Time spent blocked on a sync.Mutex or sync.RWMutex...
```

**ken's answer:** `runtime/chan.go` send/recv-with-block-flag — the actual channel send/receive code with the block-or-return logic. **A grep on "block" returns more results than there are lines in many source files**; ken returns one chunk that answers the question.

## What's NOT in this demo (and why)

Three candidates were considered in Phase 1 and cut for honesty:

- **"how does append decide to reallocate"** → ken's top hit on this corpus was `math/big/internal/asmgen/mul.go` (after `cmd/` exclusion), not `runtime/slice.go::growslice`. Hybrid retrieval doesn't bridge "append reallocate" → growslice on the stdlib corpus regardless of phrasing. Honest miss; cut.
- **"where backpressure or flow control happens on a channel"** → query is genuinely ambiguous ("channel" = Go channel vs HTTP/2 stream). The rephrased version *"where do goroutines block on a full channel"* (Phase 1(A) query #5) lands cleanly; the original was cut to avoid the ambiguity.
- **"how does HTTP request cancellation propagate"** → original phrasing landed on `net/http/httputil/reverseproxy.go` (plausible but not canonical); the rephrasing in Phase 1(A) query #2 lands on `prepareTransportCancel` instead. Original cut.

Shipping these as demo queries would have violated the kickoff doc's quality bar ("every shown query reproduces; no soft queries"). The cuts are the calibration discipline ken's audit rubric asks for.

## Re-vetting

Both harnesses are checked in:

```sh
# (A) — semantic-bridging vetting against any corpus path:
go run scripts/stdlib_demo_vet.go /tmp/go-stdlib-curated

# (B) + (C) — structural-tool vetting + grep head-to-head:
go run scripts/stdlib_phase1_close.go /tmp/go-stdlib-curated
```

Each takes ~2 minutes on an M-series Mac. The corpus is the rsync recipe
in [`README.md`](README.md#reproduce-against-your-own-gorootsrc).
