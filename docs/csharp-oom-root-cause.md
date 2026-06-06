# gotreesitter C# OOM root cause (v0.20.0-rc3)

> **RESOLVED 2026-06-06 in gotreesitter v0.20.2.** Upstream #98/#106
> ("bound C# namespace recovery sub-parses") bounded the unbounded
> recursion this memo diagnosed. Re-verified: the minimal reproducer
> below parses in ~5ms (was 3GB/OOM), and Dapper's 156 `.cs` files parse
> in ~3s at 89% clean root with no OOM. C# is now un-parked and shipped —
> see [`DESIGN.md` §10](DESIGN.md#10-risk-register) and
> [`road-to-1.0.md`](road-to-1.0.md). Retained as the diagnostic record
> of the original defect.

Written 2026-06-03 for filing upstream at
[github.com/odvcencio/gotreesitter](https://github.com/odvcencio/gotreesitter).
This is the diagnostic memo behind the ken-side C# park documented in
[`DESIGN.md` §10](DESIGN.md#10-risk-register).

## TL;DR

The C# post-parse namespace-recovery pass in `parser_result_csharp.go`
**recurses unboundedly into itself** on certain shapes of code. The
per-call cap `csharpMaxNamespaceRecoveries = 32` only bounds breadth
within one call frame — every snippet sub-parse it triggers re-enters
the same recovery pass with a freshly-zeroed counter, so recursion
depth is unbounded and each frame allocates a brand-new
`*Parser` (the `sync.Pool` is empty when called recursively because
all pooled parsers are checked out by ancestor frames).

Result on a 65-byte input: 9M+ allocations / 3 GB Go heap in ~3 seconds
before OOM. The 1.3 GB Dapper corpus has 12+ files that trigger this
shape; an 8-worker indexer pass on it climbed to 82 GB resident
before SIGKILL.

## Minimal reproducer

```csharp
namespace N { class C { void M(string n) { F(n, E.A | E.B); } } }
```

That's it. 65 bytes. Single parse:

- `pool.Parse([]byte("namespace N { class C { void M(string n) { F(n, E.A | E.B); } } }"))` never returns.
- Heap grows ~700 MB/sec until OOM.
- `GOMAXPROCS=1` does not help — this is a single-goroutine recursion,
  not contention.
- Increasing `GOGC` does not help — there's nothing to GC, every frame
  retains live state.

## What triggers it

The bug requires **all** of these in combination — drop any one and
the parse completes in ~5 ms:

1. **A block-scoped namespace declaration** `namespace N { ... }`.
   File-scoped (`namespace N;`) does NOT trigger it.
2. A method whose body contains a **call expression** whose
   3. **first argument is an identifier** (parameter or local), AND
   4. some subsequent argument is a **bitwise-or of member-access
      expressions** (`E.A | E.B`, `System.X.A | System.X.B`, etc).

Smaller positive cases that **pass cleanly** (~5 ms each):

| Variant | Body | Verdict |
| --- | --- | --- |
| Same body, no enclosing namespace | `F(n, E.A \| E.B)` | PASS |
| Same body, **file-scoped** namespace `namespace N;` | `F(n, E.A \| E.B)` | PASS |
| First arg is a string literal | `F("s", E.A \| E.B, ...)` | PASS |
| `or` operands are simple identifiers | `F(n, a \| b, ...)` | PASS |
| No bitwise-or | `F(n, E.A, ...)` | PASS |
| Trivial body | `=> null` | PASS |

The triggering combination corresponds neatly to the parser's
"is this top-level item really a namespace declaration that we
mis-recognized as an ERROR?" recovery heuristic.

## Stack trace (the recursion)

Captured via `runtime/pprof.Lookup("goroutine").WriteTo(f, 2)` while
the parser was at ~2 GB heap on the minimal reproducer. The relevant
nested frames, top of recursion at the bottom:

```
parseWithSnippetParser            parser_api.go:120
    Parser.Parse                  parser_api.go:445
        retryFullParseWithDFA     parser_retry.go:692
            retryFullParse        parser_retry.go:634
                parseInternal     parser.go:2372
                    buildResultFromNodes              parser_result.go:767
                        resultRootBuild.buildSyntheticRootTree
                            finishTree → finalizeResultRoot
                                normalizeResultCompatibility          parser_result_compat.go:21
                                    runLanguageResultCompatibility    parser_result_compat.go:44
                                        normalizeCSharpCompatibility  parser_result_csharp.go:36
                                            normalizeCSharpRecoveredNamespaces  parser_result_csharp.go:412
                                                csharpRecoverNamespaceFromChildren  parser_result_csharp.go:473
                                                    csharpRecoverNamespaceNodeFromRange  parser_result_csharp.go:496
                                                        ↓ parseWithSnippetParser ↓
                                                          (recurses on source[start:end] —
                                                           on the minimal reproducer, the
                                                           same 65-byte range each time)
```

In the goroutine dump the **same 65-byte source range
`{ptr=0x..., len=0x41, cap=0x41}`** appears at every recursion level.
The recovery pass keeps deciding the same range still needs namespace
recovery.

## Why the existing cap doesn't catch it

`parser_result_csharp.go:18`:

```go
const csharpMaxNamespaceRecoveries = 32
```

is consulted at line 411:

```go
for i := 0; i < len(root.children); {
    if recoveryCount < csharpMaxNamespaceRecoveries {
        if recovered, next, ok := csharpRecoverNamespaceFromChildren(...); ok {
            recoveryCount++
            ...
        }
    }
    ...
}
```

This counter is **local to one call frame**. When
`csharpRecoverNamespaceFromChildren` succeeds at line 412, it calls
`csharpRecoverNamespaceNodeFromRange` which calls
`parseWithSnippetParser` on a sub-range; that sub-parse goes through
its own `finalizeResultRoot → … → normalizeCSharpRecoveredNamespaces`
which creates a **fresh** `recoveryCount = 0` and can perform another
32 recoveries — each of which can recurse another 32, and so on.

## Why memory grows so fast

`parseWithSnippetParser → acquireSnippetParser` (parser_api.go:120)
uses `snippetParserPool` (a `sync.Pool`). Under the recursive workload
all pooled parsers are checked out by ancestor frames (the outer
`defer releaseSnippetParser(parser)` hasn't fired yet because the
outer call is still inside the recursion). Each recursive frame
therefore falls through to `snippetParserPool.New → NewParser →
buildSmallLookup` — every frame allocates a fresh parser including
its parse-table lookups.

Allocation profile (`go tool pprof -top -cum -sample_index=alloc_objects`) at 2 GB:

```
   2124560 (94.96%) NewParser
   2073187 (92.66%) finalizeResultRoot → runLanguageResultCompatibility → normalizeCSharpCompatibility
   2048990 (91.58%) csharpRecoverNamespaceFromChildren → csharpRecoverNamespaceNodeFromRange → parseWithSnippetParser
                    → retryFullParseWithDFA → retryFullParse → parseInternal → buildResultFromNodes
```

~2.1M `NewParser` calls in <3 s. Each `NewParser` calls `buildSmallLookup`
(parser_tables.go:69) which allocates a sym-id lookup table for the
grammar. That's the dominant slice allocation; everything else is
the per-Parser overhead.

## Suggested fix directions (pick whichever fits the design)

### A. Depth counter passed through `parseWithSnippetParser`

Add `depth uint8` to `parseWithSnippetParser` (default 0; cap at some
small N like 4) and propagate it through `Parser.Parse` →
`finalizeResultRoot` → `normalizeCSharpCompatibility` →
`normalizeCSharpRecoveredNamespaces`. When `depth >= N`, the
compatibility pass skips namespace recovery.

This is the most surgical fix — it preserves recovery for legitimate
nested-namespace top-level recoveries (which typically need depth 1)
while killing the runaway. The boundedness test in
`parser_csharp_boundedness_test.go` already plumbs `SetTimeoutMicros`
through, so a parallel `setRecoveryDepth` plumb-through is consistent
with the existing style.

### B. Range-progress guard in `csharpRecoverNamespaceNodeFromRange`

Track the source ranges already being recovered higher up the stack
(parser-thread-local stack of `[start, end)` intervals). Refuse to
sub-parse a range that is `==` or `⊇` any range currently active. On
the minimal reproducer the sub-parse range is the same 65 bytes each
time, so any non-trivial-shrink requirement would terminate it.

### C. Snippet parser pool wired with a depth-cap

Make `acquireSnippetParser` reject acquisition past some depth and
return `nil`, which already has a path through
`parseWithSnippetParser` (`if parser == nil { return nil, ErrNoLanguage }`).
Less surgical than A; visible in the result as a `ErrNoLanguage`
return from the inner parse.

I'd favor **A or B**. C side-effects callers that didn't ask for it.

## Test that would have caught this

Append to `parser_csharp_boundedness_test.go`:

```go
func TestCSharpNamespaceRecoveryStaysBounded_DottedBitorArg(t *testing.T) {
    src := []byte(`namespace N { class C { void M(string n) { F(n, E.A | E.B); } } }`)
    parser := gotreesitter.NewParser(grammars.CSharpLanguage())
    parser.SetTimeoutMicros(500_000)
    tree, err := parser.Parse(src)
    if err != nil {
        t.Fatalf("Parse: %v", err)
    }
    defer tree.Release()
    // Without bounded recovery this allocates GBs and either OOMs
    // or trips SetTimeoutMicros. With bounded recovery it returns
    // a tree (possibly with errors) in <50 ms.
}
```

(The existing boundedness tests only cover designer-style synthetic
sources and CJK identifiers; they do not generate dotted-bitor args
inside method bodies.)

## Cross-checks performed

- Same shape with **file-scoped** namespace (`namespace N;`) parses in
  4 ms. Confirms the recovery pass behaves differently per-namespace-form,
  consistent with grep-only "find block-scoped namespaces in ERROR"
  logic in the recovery path.
- `GOMAXPROCS=1`: identical OOM trajectory. Not a parallelism artifact.
- Re-parsing the **same** tiny C# fixture 200× in a tight loop: heap
  stays flat at 12 MB post-GC. Per-parse machinery does not leak.
  Confirms the issue is per-input, not per-parser-lifetime.
- Allocation profile dominated by `NewParser → buildSmallLookup` (>94 %),
  exactly what runaway recursion through `acquireSnippetParser` would
  produce.

## Reproducer artifacts (in-tree under `ken/scripts/`)

These scripts exist in the ken repo so the upstream issue can link
back to runnable diagnostics. All use only public gotreesitter APIs.

- `scripts/csharp_oom_diag.go` — three modes:
  - `--mode=leak` parses the same 97-byte fixture 200× to rule out
    per-parse leaks (shows flat 12 MB post-GC heap).
  - `--mode=per-file` walks a corpus smallest-first, logs heap+sys+RSS
    deltas; surfaces *which* corpus file triggers the OOM.
  - `--mode=single --file=...` parses one file with the same
    instrumentation.
- `scripts/csharp_bisect.go` — fork-and-budget bisection harness. For
  each `*.cs` file in a directory, forks a child to parse it, applies
  a 15 s wall + 1500 MB RSS budget, and reports PASS / TIMEOUT /
  RSS_KILL.
- `scripts/csharp_pprof.go` — runs the parse in a goroutine on
  `GOMAXPROCS=2`, has a watcher goroutine that writes
  `pprof.Lookup("allocs")`, `pprof.Lookup("heap")`, and
  `pprof.Lookup("goroutine")` to disk when heap crosses 1.5 GB. The
  HTTP `/debug/pprof/` endpoint can't be used here — the parse
  goroutine starves the scheduler under `GOMAXPROCS=1` and never
  serves an HTTP scrape.

Run:

```
go build -o /tmp/diag    ken/scripts/csharp_oom_diag.go
go build -o /tmp/bisect  ken/scripts/csharp_bisect.go
go build -o /tmp/pprof   ken/scripts/csharp_pprof.go

/tmp/diag   --mode=leak
/tmp/diag   --mode=per-file --corpus=/path/to/DapperLib/Dapper
/tmp/bisect --dir=/path/to/min-cases
/tmp/pprof
go tool pprof -top -cum -nodecount=20 /tmp/csharp-allocs.pb
```
