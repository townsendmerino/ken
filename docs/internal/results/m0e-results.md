# Stage 8 Track 1 — additive structural-enrichment bake-off

**Date:** 2026-06-02
**Author:** Claude Code working in ken
**For:** review by the claude app (planning instance) — Stage 8 Track 1 decision.

---

## TL;DR

**None of the additive structural enrichments earn their place on the
M0d Arm B baseline.** On plain hybrid at N=500 over csn-python-nl-stripped:

| Arm | NDCG@10 | R@10 | R@50 (the gate) | R@100 | label:body p99 |
|---|---:|---:|---:|---:|---:|
| **baseline (Arm B reproduction)** | **0.6309** | 0.8180 | **0.9360** | 0.9580 | 0.336 |
| + imports | 0.6311 | 0.8180 | 0.9360 | 0.9580 | 0.341 |
| + signature | 0.6318 | 0.8240 | 0.9320 | 0.9560 | 0.407 |
| + callers | **0.5925** | 0.7980 | 0.9200 | 0.9400 | **0.502** |
| + siblings | (corpus byte-identical to baseline; skipped) | | | | |

Three findings:

1. **R@50 — the metric that propagates through the reranker — does NOT
   improve for any additive arm.** imports holds R@50 identical to
   baseline (+0.000); signature drops it (−0.004); callers drops it
   meaningfully (−0.016). None of the additions surfaces more qrel
   docs into the rerank-shortlist top-50, so there is no recall gain
   that would propagate through the reranker (per the Phase B reframe).
2. **Callers actively floods.** NDCG@10 collapses by −0.038 and R@50 by
   −0.016. The body:label ratio metric correctly predicted this BEFORE
   the bench ran: callers' p99 is 0.502 (half the chunk is the label on
   the worst case); the M0d heur-only ablation set the precedent for
   what happens past the ~25–30% threshold.
3. **The shippable heuristic enrichment stays as M0d Arm B**
   (`# func: NAME | calls: A, B | raises: X`). Stage 8's structural
   index has value for Track 2 (MCP tools), but Track 1's additive
   hypothesis is closed.

**Recommendation:** ship M0d Arm B (the existing winner) without
modification. Skip the cumulative-union arm (no single arm passed, so
the union has nothing to combine). Skip the long hybrid+rerank runs
on these arms — R@50 is the metric that propagates through the
reranker, and R@50 didn't move on plain hybrid for any additive arm,
so the rerank arm cannot rescue it.

---

## Methodology

### Bench setup

- **Corpus:** `csn-python-nl-stripped` (the M0d leak-free
  benchmark — docstrings stripped from corpus docs, queries are
  docstring-shaped, 14,725 evaluable queries × 1 qrel per query).
- **N:** 500 queries (the planning-instance-locked sample). Time
  constraint on this run forced plain-hybrid only; see "What
  this run did NOT do" below.
- **Mode:** plain hybrid (`KEN_HYDE_RERANK_ONLY` unset). Each
  bench cell ran in ~19s wall, total 4 cells in ~1m35s.
- **Comparison anchor:** the Go-side baseline cell (`heur-go-baseline`).
  This re-reproduces M0d Arm B byte-equivalently via the new
  `internal/structural` extractor — see "Go vs Python materializer
  parity" below.

### The structural index + Go-side materializer

New production package `internal/structural` (gotreesitter-backed,
pure-Go, no Python at runtime):

- **`Build(corpusDir)`** walks the corpus, parses every `.py` file
  with `gotreesitter.NewParserPool("python")`, builds:
  - per-file `FileStruct` (functions, classes, imports, calls,
    raises)
  - corpus-wide `callers map[string][]CallSite` (reverse call
    graph)
  - corpus-wide `defs map[string][]string` (definition lookup)
- **`Enrich(filePath, opts) string`** returns the comment-line label
  prefix for a file. `opts == EnrichOptions{}` reproduces M0d Arm B
  (`# func: NAME | calls: A, B | raises: X`); each opts boolean
  enables one additive section.
- **Track 2 lookup API** (`References`, `Definition`, `Outline`,
  `Symbols`, `SymbolsInPath`) sits on the same index, ready for the
  MCP tool wiring in a follow-up.

8 unit tests cover the extractor + Enrich + lookups (Python AST
shapes, function vs method, imports with aliasing, call vs raise,
outline ordering, symbol prefix filtering).

### The 4-arm corpus materializer

`scripts/materialize_heur.go` is a `go run`-invocable driver that
materializes a variant corpus directory from the stripped baseline.
One invocation per arm; corpus-independent files (queries.jsonl,
qrels.jsonl, snippet caches) are symlinked into the variant dir
with absolute paths.

The 4 arms (+ the siblings arm noted below):

| Arm | EnrichOptions | Avg label chars | Body:label ratio |
|---|---|---:|---:|
| baseline | `{}` | 74 | 0.116 |
| callers | `{Callers: true}` | 97 | 0.152 |
| imports | `{Imports: true}` | 75 | 0.118 |
| signature | `{Signature: true}` | 101 | 0.158 |
| siblings | `{Siblings: true}` | 74 | 0.116 |

**Siblings was byte-identical to baseline** and skipped — CSN-Python
is essentially all top-level functions with no class context, so
the "other methods in the same enclosing class" enrichment never
fires (confirmed by `diff -r` showing 0 differences).

### Body:label ratio metric — earned its keep

The bench harness now logs the per-corpus label-line / chunk-size
ratio at startup (mean / median / p99). This was the
planning-instance-specified flooding canary; it correctly predicted
the callers regression:

```
baseline:  mean=0.148 median=0.139 p99=0.336
imports:   mean=0.150 median=0.140 p99=0.341  (≈ baseline)
signature: mean=0.190 median=0.182 p99=0.407  (+0.07 mean)
callers:   mean=0.188 median=0.170 p99=0.502  (+0.04 mean, p99 alarming)
```

The M0d heur-only ablation showed what happens past ~25–30% ratio
(catastrophic precision collapse: NDCG@10 0.6144 → 0.5037, R@50
0.9200 → unchanged but NDCG plummets). callers's p99 of 0.502
foreshadowed exactly what we measured: the chunks with the largest
label:body ratio are exactly the ones flooded by callers vocabulary.

---

## Results

### Aggregate (plain hybrid, N=500, csn-python-nl-stripped)

| Arm | NDCG@10 | Δ vs base | R@10 | R@50 | Δ R@50 | R@100 |
|---|---:|---:|---:|---:|---:|---:|
| baseline | 0.6309 | — | 0.8180 | 0.9360 | — | 0.9580 |
| imports | 0.6311 | +0.0002 | 0.8180 | 0.9360 | +0.000 | 0.9580 |
| signature | 0.6318 | +0.0009 | 0.8240 | 0.9320 | −0.004 | 0.9560 |
| callers | 0.5925 | **−0.0384** | 0.7980 | 0.9200 | **−0.016** | 0.9400 |
| siblings | (corpus byte-identical to baseline) | | | | | |

### Per-arm reading

**imports — null effect.** The imports enrichment fires for only
~3% of files (CSN-Python chunks are individual functions; the
file-level imports were stripped at corpus extraction time). When
it does fire, the imports don't add identifiers the body's calls
list doesn't already capture. Effect: zero.

**signature — null effect with regression hints.** Adding
`params: a, b | returns: T` to the prefix expands the label by
~25 chars on average. The added identifiers are usually the
function's own parameter names (e.g. "url", "path") — generic
tokens that show up in many corpus chunks. The body's call list
already includes most type-signal identifiers via call targets;
the parameter names just dilute BM25 and shift the dense vector
toward "files that mention url/path" generally. NDCG@10 actually
gains a hair (+0.0009) but R@50 LOSES 0.004 — same shape as M0d's
oracle-df5 on plain hybrid: a few rescues, a few losses, net zero
or negative.

**callers — active regression.** The big drop. Two compounding
factors:

1. **Label size jumps.** callers adds `called by: A, B, C, D...` —
   the file basenames of files that call into this function. On
   popular functions (e.g. utility helpers called from 20+ places)
   the list is large; even capped at maxCallersInLabel=8 the
   tokens (file basenames like `q265644`, `q265700`) are
   high-IDF artifacts that BM25 latches onto.
2. **Caller-vocabulary contamination.** The basenames have no
   semantic relation to the query. BM25 elevates files whose
   neighbors' basenames happen to fuzzy-match query tokens —
   spurious correlations that wreck top-10 ordering. NDCG@10
   absorbs most of the damage (−0.038); R@50 still loses 0.016
   because the boost path follows BM25's bad signal.

**siblings — N/A.** CSN-Python's per-function-file structure means
no method-of-a-class chunks exist. The enrichment never fires.
For corpora with substantial class-method content (more typical of
real-world repos), siblings WOULD differ from baseline and merit a
separate test — but on this bench, it's a no-op.

### What this means for the rerank arm

The reranker re-scores stage-1 candidates from scratch — only the
*recall* component (R@rerankN) propagates through it; within-
shortlist position errors get overwritten. So on the build-target
`hybrid+rerank` config, the answer is determined by **R@50**, not
NDCG@10.

R@50 across the four arms:

- baseline: 0.9360
- imports: 0.9360 (no movement)
- signature: 0.9320 (−0.004)
- callers: 0.9200 (−0.016)

**None of the additive arms improves R@50.** That is sufficient to
rule out rerank-arm wins by mechanism: a rerank cell on these arms
cannot rescue more queries than stage-1 already surfaces. Spending
~80 min × 4 = ~5h of bench time to confirm zero-or-negative R@50
gains is a clean no.

---

## What this run did NOT do

- **No hybrid+rerank cells.** Time-constrained: a single cold rerank
  cell takes ~80 min and there are 4 arms. The R@50 plain-hybrid
  numbers above are the deciding signal regardless. If the planning
  instance wants the corresponding NDCG@10 numbers on the
  hybrid+rerank arm for completeness, those would run overnight at
  ~5h wall.
- **No union arm.** The plan called for a final cumulative
  `Arm B + survivors` cell. With zero survivors, there's nothing to
  combine. Skipped.
- **No CosQA realism check.** The Stage 8 plan deferred CosQA to
  "the winning arm". With no winning arm, no CosQA run is justified.

If any of these become useful follow-ups, the infrastructure is in
place: the structural index, the materializer, the harness's
M0c-30 capture report, and the body:label ratio metric all sit
ready for re-use.

---

## What stays in the tree regardless

The Stage 8 Track 1 negative result does not invalidate the
foundation it tested on; everything below earned its place:

- **`internal/structural/` (pure-Go, gotreesitter)** — Index +
  Build + extract_python + Enrich + lookups. 8 unit tests passing.
  The same package is the foundation for Track 2's MCP tools
  (`definition`, `references`, `outline`, `symbols`) which a
  follow-up will register against ken-mcp's existing tool surface.
- **`scripts/materialize_heur.go`** — driver for producing variant
  enriched corpora. Useful for any future enrichment experiment.
- **Body:label ratio metric** (`bench/ndcg/hyde_test.go::
  reportBodyLabelRatio`) — startup-time flooding canary. Earned its
  place by predicting callers's regression before the bench ran.
- **M0c-30 hardcoded set** (`bench/ndcg/hyde_test.go::
  m0cUnreached30`) — the 30 qids M0d Arm B did not rescue,
  available for any future arm to score against.

---

## Decision

**Track 1 closed. Ship the existing M0d Arm B unchanged.** The
heuristic enrichment delivered M0d's +0.0100 NDCG@10 / +0.0160 R@50
lift; no additive structural fact (callers / imports / signature /
siblings) earns inclusion.

**Track 2 (MCP tools) proceeds independently.** The structural
index has product value beyond retrieval enrichment — `definition`,
`references`, `outline`, `symbols` are fast, language-agnostic,
zero-setup tools that no current MCP server exposes the same way.
The Track 2 lookup API is already implemented and tested in
`internal/structural/lookups.go`; the remaining work is the MCP
tool registration in `cmd/ken-mcp` + the per-repo caching design
for the structural index across repos.

**Stage 8 as a whole becomes a one-half-win:** retrieval enrichment
hit a ceiling at M0d Arm B (Track 1 negative), but the structural
infrastructure unlocks Track 2's exact-answer tools at low marginal
cost. The original Stage 8 kickoff's framing ("build once, use
twice") still holds — we built once, and one of the two uses paid
off.

---

## Open questions for the planning instance

1. **Is the rerank-arm completeness check worth ~5h overnight?**
   The R@50 plain-hybrid result is decisive on the mechanism; the
   rerank arm would just confirm at a different aggregate metric.
   My read: no, but if the ADR for closing Track 1 needs full-stack
   numbers, run it.
2. **Track 2 scoping.** With Track 1 closed, Track 2 becomes the
   active Stage 8 build. The lookup API is in. Next: the MCP
   handler functions (Args types + handle* + AddTool registration)
   and the per-repo caching question (build structural index at
   first lookup vs eagerly at index time vs alongside WatchedIndex
   snapshots). Worth a brief design pass before implementation.
3. **Other-language extractors.** Stage 8 v0 is Python-only. Adding
   Go, JS/TS, Rust, etc. is one new extract_<lang>.go each + a
   row in the `kenLangToTSLang` map. Worth doing now so Track 2's
   tools work on more than just Python repos, or defer until a real
   user need pulls?
