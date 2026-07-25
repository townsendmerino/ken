# Cold-start M0 — build-path profiling findings

Status: measured 2026-07-25. Feeds [`cold-start-campaign.md`](cold-start-campaign.md) M0 → sequencing.

## Setup

- **Corpus (public PHP proxy):** `yiisoft/yii2` + `yiisoft/yii2-app-advanced`, shallow —
  2,783 files / 1,184 `.php` → **12,349 chunks** (regex chunker). Stands in for the
  Trofimov bench's PHP/Yii monorepo; goal is the *per-file* PHP cost, not raw count.
- **Box:** M1 Pro 10-core / 16 GB, `go1.26.5`, aikit v1.11.0. Profiling binary
  `CGO_ENABLED=0 GOWORK=off go build -trimpath` (no `-s -w`, symbols kept).
- **Method:** `ken perf index <corpus> --mode … --chunker regex [--cpuprofile]`,
  median of 3 for timings; `KEN_ENRICH=on/off` toggles Arm B enrichment.
- **Caveat:** M1 10-core, not the bench's i5 4-core. Relative shares port; absolute
  ms do not. Embedding's share in particular is expected to be *larger* on 4 old cores.

## Headline: the non-embedding path is tree-sitter enrichment, and it dominates

Median index_ms, regex chunker, 12,349 chunks:

| mode | enrich | index_ms | isolates |
|---|---|---:|---|
| bm25 | off | 476 | **floor** — walk + regex chunk + BM25 tokenize/build |
| bm25 | on | 1705 | floor + enrichment |
| hybrid | off | 1188 | floor + embedding |
| hybrid | on *(default)* | 2285 | everything |

Decomposition of the default (2285 ms):

- **floor** (walk/chunk/tokenize): 476 ms — **~21%**
- **enrichment** (per-file tree-sitter parse for the Arm B label line): +1097 ms
  (marginal, hybrid) / +1229 ms (bm25) — **~48–54%** ← *the dominant cost*
- **embedding** (Model2Vec Encode): +712 ms — **~31%**

Enrichment is a **bigger** cold-start cost than embedding on PHP. This is the
opposite of the kernel (2026-07-24: embedding was ~6% of index time on C/H) and
explains the bench anomaly — Go ken ~1.8× slower *per file* than Python semble
cold is the per-file tree-sitter enrichment parse, which semble does not pay.

## CPU profile confirms it (enrichment ON, 11.89 s of samples across workers)

| routine | cum % | what |
|---|---:|---|
| `structural.ExtractFile` → `extractGuarded` → `gotreesitter.Parse` | **35.6%** | Arm B enrichment parse |
| ↳ `gotreesitter.retryFullParse*` / `…WithDFA` | **20.9%** | **GLR error-recovery / full-DFA reparse** |
| `aikit/embed.(*StaticModel).Encode` | 24.8% | embedding |
| `runtime.gcBgMarkWorker` | ~15.6% | GC (alloc churn) |

The 20.9% in `retryFullParse` is the smoking gun: the gotreesitter **PHP grammar is
failing its incremental fast path and doing expensive full-DFA reparses** on a large
fraction of files. That is where the per-file pathology lives — not in ken's own code.

## Two structural facts (verified in code, not just profiled)

1. **No double-parse *within* `perf index`.** With `--chunker regex`, each file is
   tree-sitter-parsed at most **once**, only by enrichment (`structural/extract_file.go:73`
   via `enrichChunks`, `search/index.go:344`). The regex chunker uses `regexp`, not
   tree-sitter; `structural.Build` (symbol index) is **not** on the `search.FromFS`
   path. BM25 tokenize is a single serial pass in `BuildIndex`, not per-worker.

2. **But ken-mcp parses every file TWICE at startup.** `cmd/ken-mcp/main.go:411`
   builds `structural.Build(dir)` **eagerly** — a second full per-file tree-sitter
   parse (same `extractGuarded`/`pool.Parse` site) immediately after the search-index
   build already paid for enrichment. The bench measures ken-mcp, so it eats both.
   The comment at `main.go:401` ("the build wall is in the noise vs the embedding
   pass") was calibrated on a non-PHP corpus and is **empirically false here** —
   the parse is ~50% of build, and it happens twice.

## Go / no-go

- **M0 lazy-structural quick win → GO, and bigger than the campaign doc assumed.**
  Defer `ken-mcp`'s eager `structural.Build` (`main.go:411`) to first
  structural-tool call (ADR-036 lazy-rerank precedent). This removes an entire
  redundant per-file tree-sitter parse pass from cold start — not a micro-opt.
  Ship as its own PR ahead of M1. (Handlers already treat `Structural()==nil`
  gracefully, so the degrade path exists.)
- **M1 snapshot persistence → GO (unchanged priority).** On everyday cold it skips
  the whole rebuild — *both* parses and the embed pass. Biggest lever, still #1.
- **M2 embedding cache → GO, but reframe.** Embedding is ~31% here (vs 6% on kernel),
  so it's worth more on the bench-host class — M2's premise holds. **However**, a
  plain embed cache leaves the ~50% enrichment-parse cost on the table on any forced
  rebuild. Extend M2 (or add a sibling) to cache the **enrichment label line** by
  file-content hash too — the parse is the more expensive thing to memoize.
- **M3 staged readiness → GO.** Serving BM25-first is doubly attractive here because
  BM25-without-enrichment is the 476 ms floor — first-servable could be sub-second
  even on true cold. (Ranking caveat: enrichment lifts NDCG per ADR-035; report
  both first-servable and fully-warm.)
- **New candidate: upstream the PHP GLR-retry cost.** The 20.9% `retryFullParse`
  share suggests a gotreesitter PHP-grammar issue (cf. the earlier overflow issue
  #110). Worth a reduced-repro + upstream file; a fast-path PHP parse would cut
  cold start materially regardless of M1–M3.

## Reproduce

```bash
git clone --depth 1 https://github.com/yiisoft/yii2 /tmp/php-corpus/yii2
git clone --depth 1 https://github.com/yiisoft/yii2-app-advanced /tmp/php-corpus/yii2-app-advanced
CGO_ENABLED=0 GOWORK=off go build -trimpath -o /tmp/ken-prof ./cmd/ken
KEN_ENRICH=on  /tmp/ken-prof perf index /tmp/php-corpus --mode hybrid --chunker regex \
    --model ~/.ken/model --cpuprofile /tmp/on.cpu  >/dev/null
KEN_ENRICH=off /tmp/ken-prof perf index /tmp/php-corpus --mode hybrid --chunker regex \
    --model ~/.ken/model >/dev/null      # compare index_ms
go tool pprof -top -cum /tmp/on.cpu
```
