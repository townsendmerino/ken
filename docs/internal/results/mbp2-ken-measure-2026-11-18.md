# Two read-only measurements: the Go typed tier's resident memory, and ApproxTokens against real tokenizers

<!-- citations-at: 1ee3bf98d0ec593610515dcd29e3895497c42cc2 -->

Measured 2026-11-18 on **mbp2 (Linux amd64, 8 logical CPUs, Intel i5-8257U, 16 GB, kernel 7.3.3-2-t2)**, ken at `1ee3bf9` (v1.20.0, `origin/main`), Go 1.27.4. Nothing in ken changed; the harnesses were throwaway and are not committed. The timing lock was free at the start and held `--shared` for every run. Other Claude sessions were idle-to-light on the box (one resident `ken-mcp` of ~0.9 GB; free RAM was never the limit), so treat absolute seconds as ±20 %; the memory figures are the ones that matter.

Kill thresholds, set before measuring (from the brief):

- **M1**: if the type checker adds under ~300 MB on goinfer at steady state, a gopls-style shallow export-data cache (golang/go#78856) is not worth building now.
- **M2**: if the median overestimate is under 10 % *and* undercounts are under 5 % of chunks, exact BPE counting is not worth building now.

## Verdicts

| | number | against the threshold | verdict |
|---|---|---|---|
| **M1** type-checker memory | **+428 MiB** live heap on goinfer (3 configurations, 422 packages each); **+~950 MiB** process RSS. tack: +15 MiB live, +89 MiB RSS | over 300 MB | **build, with a caveat** (below): the threshold is crossed, but export data would not remove most of what is held |
| **M2** token estimate | median estimate/real **1.12 to 1.25** on the three modern tokenizers; undercounts **6.3 % to 15.7 %** of chunks | both over (10 % and 5 %) | **build, with a caveat** (below): the estimate wastes 11 to 28 % of a budget, but it errs in the safe direction and a calibration constant may capture most of it |

## M1. Resident memory of the go/types layer

### What was run

`ken-mcp` (built from `1ee3bf9`) driven over stdio by a small JSON-RPC client (`initialize`, then `tools/call`), one process per repository, `KEN_MCP_DEFAULT_REPO=<repo>`, `KEN_MCP_SNAPSHOT=0` (so nothing is written into the measured repositories), `KEN_MCP_AUTO_FETCH=0`, `GODEBUG=gctrace=1`, defaults otherwise (hybrid mode, `GOGC=50`, programs resident, one per build configuration). After each call the client read `VmRSS`, `VmHWM`, `RssAnon`, `RssFile` from `/proc/<pid>/status` and the last `gctrace` post-GC heap.

Goinfer has **no `go.work`** (only `go.work.example`), so the "seven modules" are reached through its `.ken-go.json`: three configurations (`default` = linux/amd64 here, `darwin-gpu` = darwin/arm64 with tags `gpu,goinfer_testhooks,realckpt`, `linux-cuda`), which is what produces the three resident programs. tack is a single module, one configuration.

```
python3 ~/tmcode/goinfer/scripts/timing_lock.py run --shared --label ken-measure-m1 -- \
  python3 -I m1_drive.py <repo> <tag> <steps.json>      # steps: 2 search, 1 first go_references, 5 more typed queries
```

### goinfer: process memory (MiB), one run (a second run agrees within 1 %)

| step | VmRSS | VmHWM | RssAnon | live heap (post-GC) | call |
|---|--:|--:|--:|--:|--:|
| spawned | 27 | 27 | 7 | | |
| 1. index built, **no type checker** (hybrid, enriched, embedded) | 1,034 | 1,303 | 959 | 885 | 32 s build |
| after two `search` calls | 1,034 | 1,303 | 959 | 885 | 0.01 s |
| 2. first `go_references tokenizer.Tokenizer` (loads 3 programs) | 1,716 | 1,929 | 1,640 | 1,303 | 6.6 s |
| 3a `go_references decoder.Options` | 1,782 | 1,929 | 1,706 | | 0.24 s |
| 3b `go_references chat.Template` | 1,830 | 1,929 | 1,755 | | 0.14 s |
| 3c `go_implementations engine.Engine` | 1,935 | 1,935 | 1,860 | | 0.19 s |
| 3d `go_references constrain.Grammar` | 1,981 | 1,981 | 1,906 | 1,355 | 0.23 s |
| 3e, 3f (`pull.Plan`, `ResidentForward.Forward`) | 1,985 | 1,985 | 1,909 | 1,355 | 0.13 s |

- **Index alone: 1.0 GB RSS, 885 MiB live.** The index is the bigger holder; the 1.3 GB high-water mark is the build.
- **Type checker, first query: +682 MiB RSS, +418 MiB live heap.** After six more queries across packages: **+951 MiB RSS, +470 MiB live** at the last post-GC reading. The live heap is not rising with queries: a forced-GC measurement (below) shows the held set is **428 MiB flat** across three queries. The RSS growth after the first query is garbage from the typed queries and checker not yet collected (`GOGC=50`, goal ~1.95 GB), not retained state.
- ken's own accounting (`status` tool) agrees: **406.6 MB held**, three programs of 131 to 138 MB each, 422 to 423 packages each.

### A clean live-heap number, with a heap profile

Because RSS and the `gctrace` readings include uncollected garbage, a throwaway driver (`engine.NewGoTypedHost{MaxResident: 0}`, the same host `ken-mcp` builds; `runtime.GC()` before every reading) gave:

| | live heap |
|---|--:|
| before | 1 MiB |
| after `references decoder.Options` (3 programs loaded) | **429 MiB** |
| after `tokenizer.Tokenizer`, `chat.Template` | 426 MiB, 426 MiB |
| `host.Status().Programs.Bytes` | 406 MiB (3 programs) |

`go tool pprof -sample_index=inuse_space` on that process, 416 MiB in use:

| holder | MiB | share |
|---|--:|--:|
| `gotypes.(*tPackage).tabulate` (ken's own per-package use/def tables) | 113 | 27 % |
| `go/types.(*Scope).insert` | 90 | 22 % |
| `go/types.newVar` | 69 | 16 % |
| `go/types.NewScope` | 51 | 12 % |
| `go/token.(*File).AddLine` (line tables) | 27 | 6 % |
| `go/types.NewFunc` | 11 | 3 % |
| `buildXrefs`, scanner, other | ~35 | 8 % |

Cumulatively, 369 MiB (89 %) is under `(*program).check`, and `go/types.(*Checker).funcBody` accounts for 115 MiB (28 %). The ASTs themselves are **not** among the holders (no `go/ast` node types in the profile): ken already drops syntax after checking. What stays is the **local scopes and variables created while checking function bodies** (Scope/newVar/NewScope, about 55 % of the profile, 230 MiB), plus ken's own tables. `types.Info` maps do not show up as a distinct holder.

### tack (single module, one configuration)

| step | VmRSS | RssAnon | live heap |
|---|--:|--:|--:|
| index built, no type checker | 222 | 150 | 126 |
| after two `search` calls | 222 | 150 | 126 |
| first `go_references config.Config` (1.9 s) | 289 | 216 | 136 |
| after five more typed queries (`pilot.Task`, `pilot.Record`, `server.Server` impls, `config.Run`, `sandbox.Config`, `eval.Options`) | 311 | 238 | 141 |

Type checker adds **+15 MiB live (+89 MiB RSS)** on tack. Not near any threshold.

### Does ken already evict or cache anything?

Yes, in the logical sense, and the code and the run agree. `internal/gotypes/progcache.go` keeps at most one program per configuration (`MaxPrograms`, LRU-evicted) and drops each after `KEN_MCP_GO_IDLE` (default 5 min); `status` reports "programs: N of M resident, each dropped after … idle". Merged answers are cached separately (`KEN_MCP_GO_CACHE_MB`, default 32 MB; the repeat query returned in 0.05 s as a remembered answer; 7 answers cost 329 KB). With `KEN_MCP_GO_IDLE=20s`, after 30 s idle `status` showed **0 of 3 resident, held 329 KB**: the programs were released.

**But the process RSS did not fall.** Three runs, same result: after the idle drop RSS stayed at 1,752 to 1,985 MiB (`RssAnon` 1,674 to 1,909), and 40 further `search` queries and a further 60 s idle did not move it (1,752 → 1,765 MiB). By reading `internal/gotypes/progcache.go`, the cache calls `debug.FreeOSMemory()` after a load (`heapNow`) and shortly after a replacement or a rename's throwaway check (`freeSoon`, `trim`), but **`reap()` (the idle drop) and `Invalidate` delete the program and call neither**, and `GOGC=50` leaves the next collection far away, so a dropped program's pages stay resident until something else forces a GC (ken-mcp's other `FreeOSMemory` calls are after the initial build and each watch flush, `cmd/ken-mcp/internal/server/server.go`). That is a cheaper fix than any new cache: **having `reap()` arm `freeSoon()` as the replacement path does would return roughly the 0.7 to 0.9 GB RSS for the quiet periods a long-lived server mostly lives in.** (Not tried: I changed no code. The claim is the observation above plus the reading of `gc.go`; verify before relying on it.)

### Verdict and caveat

Against the threshold set beforehand: goinfer adds 428 MiB of live heap (406 MiB by ken's accounting; about 950 MiB of RSS at steady state), over the 300 MB bar, so **by the letter this is "build"**. Three things the number does not say:

1. **Export data would not remove most of what is held.** A gopls-style shallow export-data cache (`WriteShallow`/`ReadShallow`) stores declaration-level type information for a package's *dependencies*. The 416 MiB profile is dominated by function-body scopes and variables of the in-repository packages (the references answer needs every use inside those bodies) and by ken's own use tables (`tabulate`), neither of which export data holds. How much of the 230 MiB of scope/var memory belongs to dependency packages rather than the 422 in-repo ones was **not** separated here; that split (a profile labelled by package) is the one thing to measure before building.
2. **The held set is a function of the configuration count.** The three programs are one per `.ken-go.json` configuration, about 135 MiB each. A one-configuration repository of goinfer's size would hold about a third. tack, with one, holds 15 MiB.
3. **Cheaper levers exist and are untested:** (a) release scopes/vars after `tabulate` where the use tables already carry what a query needs; (b) `FreeOSMemory` after an idle drop (above); (c) fewer resident programs (`KEN_MCP_GO_RESIDENT`). The earlier Mac record ([`gotyped-resident-2026-11-13.md`](gotyped-resident-2026-11-13.md)) measured about 285 to 340 MB per configuration at peak and about 0.22 GB held per program; this Linux run holds about 0.135 GB per program, a different platform and a newer ken, so the two are not directly comparable.

**Recommendation:** the threshold says build; I would not start shallow export data without the per-package split in (1), and would take (a) and (b) first. Francis's call.

## M2. ApproxTokens against real tokenizers

### What was run

Real chunks: ken at `1ee3bf9`, `engine.Open` (hybrid, regex chunker, defaults, no snapshot, no watch) on `~/tmcode/goinfer` and `~/tmcode/tack`, **40 varied queries each (80 total)**, `Search(q, 50)`, keeping every returned hit's `File` and `Text` (the label-stripped chunk text the budget path sees). 4,000 hits, **2,792 unique chunks**. Mixed conceptual ("where do we handle stop sequences") and identifier-shaped ("rope scaling", "decoder options") queries; the sets are in the harness and are not tuned to the result.

Tokenizers: goinfer's `tokenizer` package (pure Go), loaded from checkpoints already on disk, nothing downloaded:

| tokenizer | source | vocab |
|---|---|--:|
| Qwen2.5-Coder | `~/models/qwen2.5-coder-0.5b-instruct-q4_k_m.gguf` (embedded tokenizer) | 151,936 |
| Qwen3.5 | `~/.cache/goinfer/models/Qwen/Qwen3.5-0.8B/tokenizer.json` | 248,070 |
| Gemma 4 | `~/.cache/goinfer/models/google/gemma-4-26B-A4B-it-qat-q4_0-gguf/*.gguf` (metadata only, mmap'd) | 262,144 |
| Phi-3-mini (an extra, old 32k llama SPM) | `~/models/phi3-mini-4k-gguf/*.gguf` | 32,064 |

**Not measured:** gpt-oss / o200k (harmony). goinfer has the o200k *pre-tokenizer* (`split_o200k.go`) but **no o200k vocabulary and no gpt-oss checkpoint exists on this machine**, and the brief says not to download without asking. **Qwen3 itself is not on disk** either; Qwen2.5-Coder and Qwen3.5 bracket it (Qwen3 shares the Qwen2.5 pre-tokenizer family and a 151k vocabulary). Both gaps are noted in the verdict.

`Encode(text, false)`, no special-token handling. Estimate = `engine.ApproxTokens`, the same function as `mcp/token_budget.go`'s `approxTokens` (the two bodies are identical).

### Per chunk: estimate / real

| tokenizer | median | p10 | p90 | aggregate (Σest/Σreal) | estimate **under** real (undercount) |
|---|--:|--:|--:|--:|--:|
| Qwen2.5-Coder | **1.253** | 1.040 | 1.393 | 1.177 | **6.3 %** (176 of 2,792) |
| Qwen3.5 | **1.198** | 1.004 | 1.342 | 1.137 | **9.5 %** (265) |
| Gemma 4 | **1.121** | 0.953 | 1.282 | 1.075 | **15.7 %** (437) |
| Phi-3-mini (extra) | 1.016 | 0.851 | 1.155 | 0.976 | 43.8 % (1,222) |

So the estimate overshoots by 12 to 25 % on the three current tokenizers, and is close to unbiased on the old 32k-vocab one, where it is under about half the time. The spread is wide: p90 is 28 to 39 % high on the modern three.

### Packing simulation

Per query, the top 50 ranked hits, greedily filled as `applyTokenBudget` does (cost = text + file path + 8 scaffold, always keep the first), once with the estimate and once with real counts for text and path (scaffold kept at 8 in both). "Exact gain" is how many more chunks exact counting fits; "real/budget" is the real token total of the set the *estimate* kept, as a fraction of the budget (above 1.0 means the estimate overshot).

| tokenizer | budget | estimate packs / query | exact packs / query | exact gain | real/budget (median, p10–p90) | over budget (queries of 80) |
|---|--:|--:|--:|--:|---|--:|
| Qwen2.5-Coder | 2k | 4.29 | 5.47 | **+27.7 %** | 0.69 (0.53–0.79) | 0 |
| | 4k | 8.64 | 11.06 | +28.1 % | 0.74 (0.66–0.82) | 0 |
| | 8k | 17.54 | 21.55 | +22.9 % | 0.77 (0.71–0.86) | 1 |
| Qwen3.5 | 2k | 4.29 | 5.26 | +22.7 % | 0.70 (0.57–0.82) | 0 |
| | 4k | 8.64 | 10.59 | +22.6 % | 0.77 (0.69–0.85) | 1 |
| | 8k | 17.54 | 20.60 | +17.5 % | 0.80 (0.75–0.88) | 1 |
| Gemma 4 | 2k | 4.29 | 4.85 | +13.1 % | 0.75 (0.61–0.88) | 1 |
| | 4k | 8.64 | 9.80 | +13.5 % | 0.82 (0.74–0.90) | 1 |
| | 8k | 17.54 | 19.45 | +10.9 % | 0.86 (0.80–0.92) | 3 |
| Phi-3-mini (extra) | 2k | 4.29 | 4.38 | +2.0 % | 0.84 (0.65–0.99) | 7 |
| | 4k | 8.64 | 8.85 | +2.5 % | 0.91 (0.81–1.00) | 8 |
| | 8k | 17.54 | 17.76 | +1.3 % | 0.95 (0.87–1.01) | 9 |

In words: with a current Qwen or Gemma tokenizer the heuristic fills only **70 to 86 % of the budget it was given** and leaves **one to four chunks per query on the table** (median +1 at 2k, +2 to +4 at 4k to 8k; p90 up to +6). The overshoot risk is real but small on those tokenizers (0 to 3 of 80 queries went over budget). Phi-3's old vocabulary is the reverse: the estimate is nearly unbiased in aggregate, so it overshoots a budget in 7 to 9 queries of 80, while exact counting gains almost nothing.

### Verdict and caveat

Against the thresholds set beforehand, both conditions for killing it fail: the median overestimate is 12 to 25 % (not under 10 %) and the undercount share is 6.3 to 15.7 % on the three modern tokenizers (not under 5 %), so **by the letter this is "build"**. The caveats:

1. **The error is mostly one-sided and constant-shaped.** Aggregate estimate/real is 1.08 to 1.18 on the modern tokenizers, so most of the waste is a bias, not noise. A calibration constant (for instance dividing the estimate by about 1.15) might recover a large share of the 11 to 28 % gain with no vocabulary tables, keeping ken's slim-binary contract (`binary_contract_test.go`). **I did not test a constant**; it is the first thing to try. The p10 to p90 spread (about ±15 % around the median) is what exact counting would remove and a constant would not.
2. **Exact counting has a hidden cost the number ignores:** ken does not know which model its caller uses, so "exact" would mean choosing one tokenizer (a vocabulary table in every `ken-mcp`) or having the caller pass counts. The gain is also tokenizer-dependent (+2 % on Phi-3 to +28 % on Qwen2.5-Coder), so one exact table is exact for only one model family.
3. **gpt-oss/o200k is unmeasured** (no vocabulary on disk), and Qwen3 is bracketed rather than measured; the claim covers the three families above.
4. Two repositories, both Go, one chunker (`regex`), one scaffold constant (8, unchanged). Code in other languages tokenizes differently.

**Recommendation:** try the calibration constant first (a one-line change and this harness re-runs in under a minute); build exact BPE only if the residual spread matters to a caller. Francis's call.

## Reproducing

```bash
# M1: ken-mcp over stdio, repo <R>, /proc/<pid>/status after every call (client: ~60-line Python JSON-RPC driver)
KEN_MCP_DEFAULT_REPO=<R> KEN_MCP_SNAPSHOT=0 KEN_MCP_AUTO_FETCH=0 GODEBUG=gctrace=1 ken-mcp
#   steps: search x2 -> go_references <pkg.Type> -> 5 more go_references / go_implementations -> idle -> search x40
# M1 live heap + pprof: an engine.NewGoTypedHost{MaxResident: 0} driver calling gt.References, runtime.GC() around each reading,
#   pprof.Lookup("heap"); go tool pprof -sample_index=inuse_space -top
# M2: one module outside both repos with replace directives for ken and goinfer;
#   engine.Open + Workspace.Search(q, 50) -> chunks.json, then goinfer tokenizer.LoadGGUF / Load + Encode vs engine.ApproxTokens
python3 ~/tmcode/goinfer/scripts/timing_lock.py run --shared --label ken-measure-m2 -- m2 chunks.json
```

The harness sources are not committed (throwaway; about 60 and 190 lines). Raw per-step output for M1 (`*.json`, `*.stderr` with gctrace, `*.status.txt`) and the M2 chunk set (6 MB) were kept in the session's scratch directory, not in the repository.
