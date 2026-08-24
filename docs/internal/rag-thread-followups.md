# r/Rag thread follow-ups: α sweep, chunker traceability, temporal eval, result provenance

**Status:** item 4 landed (2026-08-24); items 1–3 scoped, not started · **Origin:** community feedback on the Aug 2026 r/Rag post ([thread](https://www.reddit.com/r/Rag/comments/1vwpm6t/)) · **Promise made:** "will investigate and report back" — the deliverable of every item below is a paragraph in a thread reply, backed by a reproducible number in `docs/BENCH.md`.

Two commenters, four work items. One asked for an α sweep done properly (held-out split, not tuned on the reporting set). The other pushed back on "AST chunking doesn't help": aggregate NDCG may hide value in *traceability* (does a retrieved span map cleanly to one symbol?) and *change sensitivity* (do results survive refactors?), asked for boundary-failure slicing by query intent and a temporal eval, and pointed out that results without commit/chunker/config provenance are hard to reproduce once the codebase moves.

Ordering: item 4 (provenance) first — it's small and items 2–3 want it in place so their outputs are self-describing. Then 1 (cheap, self-contained), then 2, then 3 (the only new harness).

---

## 1. α sweep on a held-out split

**Claim to test:** the inherited fusion weights (α_symbol=0.3, α_NL=0.5) are near-optimal, and RRF is flat around the middle anyway.

**What exists already.** α is adaptive per query class (`internal/search/adaptive.go:resolveAlpha`, verbatim from semble's `ranking/weighting.py`), and the `alphaOverride` parameter already plumbs through `Hybrid()` — so this is a harness change only, no search-path code. **[Correction, 2026-08-24: that last clause was wrong.** `alphaOverride` existed only on the unexported `hybridSearch`, and all three call sites in `index.go` hardcoded `-1`; nothing exported could pin α. Item 1 needed a small search-path addition — see "As built" below.**]** ADR-013 (corpus-adaptive α, a third query-class branch) is Deprecated and stays that way; this item is about validating the two existing constants, not adding routing.

**Design.**
- Split the 1,251 semble-bench queries **by repo, not by query** — queries from one repo share an index, and a per-query split would leak corpus statistics across the boundary. Deterministic split (hash of repo name), roughly 60/40 tune/holdout, stratified so both halves keep a similar symbol/NL mix.
- Sweep α ∈ {0.0, 0.1, …, 1.0} independently for the symbol and NL classes on the tune half (the classes use different constants, so it's two 1-D sweeps, not one 2-D grid — the classifier `isSymbolQuery` picks the constant per query).
- Take the per-class argmax from the tune half, evaluate **only that pair** once on the holdout half, and report holdout NDCG@10 and recall@10 next to the α=(0.3, 0.5) baseline on the same holdout.
- Success criterion is a *negative* result being publishable: if the tuned pair beats the baseline by less than bench noise (~±0.005 NDCG), the report-back is "swept it, flat, kept semble's constants" — which is the expected outcome and worth stating with the curve.

**The parity constraint (decided in the thread, restated here so BENCH.md's rule survives):** α=(0.3, 0.5) remains the verbatim-parity reference that the semble-benchmark comparison is run at, per BENCH.md's "don't tune ken's constants" rule. Any tuned α is reported as a separate, clearly-labeled experiment. The default shipped to users does not change unless the holdout delta is large enough to justify amending the "verbatim port" framing itself — which would need its own ADR.

**Deliverable:** a short "α sensitivity" subsection in BENCH.md with the two sweep curves (tune half), the single holdout evaluation, and the split recipe; harness under `bench/` behind the existing `bench` build tag.

**Estimate:** small. Full sweep is 11 α values × 2 classes over ~60% of the corpus; at ~30–90 s per full-corpus mode run today, expect the whole grid in well under an hour of machine time (indices build once per repo and are reused across α values, since α only affects fusion, not indexing).

### As built (2026-08-24) — swept; α_symbol confirmed, α_NL marginal, constants kept

Numbers, curves and the full method are in [`docs/BENCH.md` → "α sensitivity"](../BENCH.md#α-sensitivity--is-03-05-actually-the-right-pair). Headline: **"flat around the middle" essentially held.** α_symbol is flat outright — the tune argmax lands on 0.3/0.3/0.4 across three splits with the top three grid points within 0.0005 NDCG, so which one wins is decided in the fourth decimal. α_NL = 0.5 sits at the lower edge of a broad plateau; the held-out gain from 0.6 runs +0.004 to +0.010 and clears materiality on **one of three splits**. Shipped pair stands.

The one split that did clear both bars (+0.0095, t=2.66) survived a chunker-artifact check but not a resampling check — an independent seeded split came back flat (+0.0042, t=1.33). Running that third split is what kept this from being written up as a finding.

Two corrections to the plan above, both worth carrying into items 2–3:

- **"Harness change only, no search-path code" was wrong.** `alphaOverride` existed only on the unexported `hybridSearch`; all three `index.go` call sites hardcoded `-1`. Nothing exported could pin α. Item 1 added `search.AlphaPair` (per-class, negative sentinel so a pinned α=0.0 stays distinguishable from unpinned), `search.SearchModeAlphas`, and `search.IsSymbolQuery`. Production paths all still pass `AdaptiveAlphas`.
- **The sweep had to be driven from Python, not Go.** semble scores *chunk-level ranks against file-level targets* (`target_rank` + `ndcg_at_k`), a different convention from the CoIR harness's `aggregateByDoc`. Re-deriving it in Go is what `bench/semble/README.md` exists to forbid, so the driver lives in `run_ken.py` and `ken bench` gained `--alpha-symbol` / `--alpha-nl` / `--alpha-pairs`.

Methodology lessons the plan didn't anticipate:

- **The estimate assumed index reuse the harness didn't do.** One `ken bench` per α rebuilt 41 identical indexes eleven times; query time is ~0.4 ms, so essentially the whole 2-hour runtime was rebuilding an index α doesn't affect. `--alpha-pairs` scores every pair against one build — 2 passes instead of 13, ~12 min total, and every curve point sees the byte-identical index.
- **A saturated curve breaks a naive argmax.** The symbol curve is flat to 4dp on some splits; tie-breaking toward low α then reports α=0.0 as "tuned" on zero evidence (and it lost on holdout). Ties now go to the shipped constant.
- **There is no run-to-run noise to average.** ken's retrieval is deterministic; the uncertainty is sampling. Since both arms score the same queries the correct statistic is the **paired** per-query difference — reported with SE, t, and how many queries α moved at all, because a mean over mostly-zeros is a different claim from a broad shift.
- **`--tune-fraction` does not draw a new split.** Changing only the fraction re-cuts one md5 ordering, so the halves nest — the 40/60 tune set is a strict subset of the 60/40 one. That is not a replication. `--split-seed` salts the ordering for a genuine resampling check.
- **Ask "did the curve move?" before "is the tuned value better?"** The descriptive holdout curve (scored, never fed to an argmax) is what distinguishes "0.6 is right" from "the whole plateau shifted and 0.6 happens to be closer". It costs nothing once the grid rides on one build.

**Unexpected finding worth its own thread:** the α optimum appears **corpus-dependent** — the harder holdout half wants more semantic weight — which is the idea [ADR-013](DECISIONS.md) deprecated. Re-opening it needs a mechanism that predicts α from the corpus *without* peeking at labels; this experiment provides no such thing and did not attempt one.

---

## 2. Boundary-failure slicing by query intent

**Claim to test:** "treesitter Δ −0.004 overall, within noise" may be hiding a real win on a specific failure class that aggregate NDCG averages away.

**Design.**
- Re-run regex vs treesitter on the semble bench, but instead of comparing aggregate NDCG, extract the **per-query outcome pairs** (rank of qrel target under each chunker) from the existing result JSON (`bench/semble/results/`). **[Correction, 2026-08-24: the result JSON stores only per-repo aggregates (`ndcg10`, `by_category`) — there are no per-query ranks in it, so this extraction was impossible as written. `run_ken.py --dump-per-query` now records ranks + hit lists, and the two chunker runs write to distinct files.]**
- Classify the queries where the two chunkers disagree (target found by one, missed or ranked ≥5 lower by the other) along the axes the bench already carries: query category (architecture / semantic / symbol via `by_category`), language, and — the commenter's specific axes — whether the failure is a *boundary* failure: target chunk split mid-definition, or a chunk mixing two adjacent definitions. Boundary-failure detection needs a small classifier over the chunk spans vs the file's definition spans; the enrichment pass already extracts definition locations, so this is a join, not new parsing.
- Also answer the commenter's direct question — cross-file symbol references — by slicing symbol-class disagreements on whether the qrel target file differs from the file the query's identifier is defined in.

**Deliverable:** a table in BENCH.md ("where the chunkers disagree"): disagreement count by category × language, share of disagreements that are boundary failures, and a verdict on whether ADR-011 (regex stays default) needs revisiting for any slice. If treesitter wins the boundary-failure slice decisively, that's the right amendment to the Reddit claim even if the aggregate stays flat.

**Estimate:** medium-small. No new benchmark runs needed if existing result JSONs for both chunkers are current; the work is the disagreement classifier and the definition-span join. **[Correction: new runs ARE needed — see above. Both runs are ~10 min each now that indexing is the only real cost.]**

### As built (2026-08-24) — objection half-right: 3.2× traceability win, zero ranking win

The definition-span join landed first, as `bench/chunkdiff` (build tag `bench`), because it answers the commenter's *traceability* question directly and needs no query data at all:

- **SPLIT** — a definition no single chunk fully contains. Retrieval can surface it, but no one result shows the whole thing; the agent gets half a function. Measured over **leaf** definitions (functions + methods).
- **MIXED** — a chunk containing the start of ≥2 definitions, so the span doesn't map to one symbol. Measured over **top-level** definitions only (top-level functions + classes), deliberately: a chunk holding a whole class with five methods *does* map to one symbol, and counting its five method starts as mixing would penalize exactly the outcome the metric should reward. That asymmetry has its own unit test.

Spans come from `structural.ExtractFile` — the same extractor Arm B enrichment already runs — so it is a join, as planned, not new parsing. Two details that would have quietly corrupted the numbers:

- **Overlap-aware containment.** The line chunker emits overlapping windows precisely so a definition straddling one boundary still appears whole in a neighbour. Containment therefore asks "does *any* chunk cover this", not "does the chunk containing its start line"; the naive version punishes a chunker for the feature that fixes the defect being measured.
- **All-chunkers-or-none per file.** A file analyzed under regex but skipped under treesitter would make the two columns describe different corpora. Files with no registered extractor, or no spanned definitions, are skipped entirely — scoring them as perfectly traceable would dilute every rate with files that never had a symbol in them.

**Results** (full write-up: [`docs/BENCH.md` → "Where the chunkers disagree"](../BENCH.md#where-the-chunkers-disagree--traceability-vs-ranking)):

| | regex | treesitter |
|---|---:|---:|
| NDCG@10 (1,251 queries) | 0.8434 | 0.8403 |
| definition split rate (276,745 defs) | 0.082 | **0.026** |
| chunk mixed rate | 0.575 | 0.580 |
| queries where they disagree | — | 17 of 1,249 (98.6% agree) |
| symbol-query disagreements | — | **0 of 194** |

**The commenter was right that aggregate NDCG hides something, and wrong about what it hides.** treesitter is 3.2× better at keeping definitions whole, consistently across all 13 languages. That buys nothing in ranking: 98.6% of queries agree, no slice favours treesitter (regex wins the 17 disagreements 12–5, not significant at that n), and their specific cross-file-symbol hypothesis produces **zero** disagreements across all 194 symbol queries — the lexical arm's exact match dominates, which independently matches item 1's flat α_symbol curve.

Both are true because a split definition costs the **agent** a follow-up read, not the **ranker** a position. NDCG asks whether the right file surfaced; it did either way. The cost lands in the token economy, so the token-budget bench is where it would show up — ADR-011 stands on ranking grounds, and acting on the traceability argument would need that number to move.

**Unplanned finding — a user-facing hang.** Running the traceability sweep uncovered that `ken index` appears to hang on C# files: gotreesitter v0.51.0 takes **250 s** on 11 KB of valid C# (`BuiltinResolver.cs`) and still returns `root=ERROR`, with six such files in one repo. `.cs` is now parked out of `kenLangToTSLang` (deterministic, matching the bash precedent; a wall-clock budget would trade the hang for the reproducibility bug ADR-040 closed). ken-mcp was never exposed — it sets a 500 ms budget. See DESIGN.md §10. Upstream report pending.

---

## 3. Temporal eval — retrieval stability under code change

**Claim to test:** a static relevance benchmark can't see whether retrieval quality survives the codebase moving — renames, refactors, moved files — which is the regime a coding agent actually operates in (the agent edits the repo mid-session; watch mode re-indexes behind it).

**Design — synthetic first, git-history second.**
- **Phase A (synthetic mutations):** take a small fixed repo set from the semble bench, apply mechanical mutations with known ground-truth mapping — rename a public symbol repo-wide, move a function between files, split a large file — re-index, and re-run the queries whose qrel targets were touched. Score two things separately: (1) **recall under drift** — does the query still find the moved/renamed target (suffix-aware path matching needs a mutation-aware qrel remap); (2) **staleness** — with watch mode's 2 s debounce, does a query issued immediately after the mutation return the pre- or post-mutation chunk (this exercises the snapshot/drift-scan path too, `KEN_MCP_SNAPSHOT`).
- **Phase B (real history, only if A finds something):** replay N consecutive real commits from one bench repo, re-running a fixed query set at each commit, measuring rank churn for queries whose answers didn't change. High churn on untouched answers = ranking instability worth investigating; stable = a good report-back number.
- Symbol renames are the interesting case per the commenter: post-rename, the lexical arm loses its exact-match anchor entirely and the semantic arm carries the query — this is a direct measurement of what the +0.13 semantic recall lift is *for*.

**Deliverable:** a new "temporal stability" section in BENCH.md with the mutation harness (`bench/temporal/`, build-tag `bench`), Phase-A numbers for rename/move/split, and the staleness measurement. Phase B is a stretch goal, cut if Phase A comes back clean.

**Estimate:** the largest item — the only genuinely new harness. Phase A is bounded (three mutation types, small repo set); do not gold-plate the mutation engine, `gofmt`-valid text-level rewrites are enough.

---

## 4. Result provenance — log commit, chunker, and config with every result

**Claim to accept as-is:** the commenter is right — a bench result JSON that doesn't record the repo commit, chunker + version, mode, α, model, and ken version can't be reproduced once anything moves. This is also true for *agent-facing* results, where a "why did ken return this yesterday" question is currently unanswerable.

**Design.**
- **Bench side (do now):** add a `provenance` block to every result JSON the harnesses write (`bench/semble/`, `bench/ndcg/`, `bench/tokens/`, and the new `bench/temporal/`): ken version + VCS hash (from `runtime/debug.ReadBuildInfo`, already embedded), repo pinned revision (the semble manifest has it; `git rev-parse HEAD` for others), chunker name + gotreesitter version, mode, α pair, model dir hash, and the relevant `KEN_*` env. One shared helper in `bench/internal/` so the four harnesses can't drift.
- **Server side (decide, then maybe do):** ken-mcp tool responses could carry an optional one-line provenance field (index build id: commit + chunker + mode + model hash). Costs tokens on every agent call, which cuts against the token-economy story — so default off, expose behind `KEN_MCP_PROVENANCE=1`, and route the decision through a short ADR since it touches the wire format that the semble drop-in compatibility promise covers.

**Deliverable:** provenance block in all bench JSONs + the shared helper; an ADR for the server-side question. This item unblocks the "reproduce once the codebase moves" half of items 2–3 and should land first.

**Estimate:** small for the bench side; the server-side ADR is a page.

### As built (2026-08-24) — bench side done, server side still open

`bench/internal/provenance` (build tag `bench`) is the shared block builder; the schema is documented with an example in [`docs/BENCH.md` → "Result provenance"](../BENCH.md#result-provenance). Wired into every harness that writes a result file:

| Harness | File | Change |
|---|---|---|
| `bench/semble/run_ken.py` | `bench/semble/results/ken-<mode>.json` | `provenance` key added to the existing summary object |
| `bench/tokens/{semble,coir}_test.go` | `bench/tokens/results/*-tokens.json` | document reshaped from a bare array to `{provenance, records}` |
| `bench/ndcg/coir_test.go` | `bench/ndcg/results/coir-<chunker>.json` | **new** — the harness previously emitted its table to stderr only |
| `bench/ndcg/coir_export_test.go` | `testdata/bench/…/shortlist.provenance.json` | sidecar (the shortlist is JSONL; a header line would break its readers) |

Decisions worth carrying into items 1–3:

- **α comes from the code.** `search.DefaultAlphas()` was exported for this; the harness cannot record an α the fusion didn't apply. `alpha_override` is `null` for adaptive and a number when pinned — item 1's sweep sets it, and `null ≠ 0.0` is load-bearing there since α=0.0 is a real sweep point.
- **Models are identified by digest, not path.** `~/.ken/model` is per-machine; the path alone doesn't identify weights.
- **`corpora` is plural, with `repo` separate from `revision`.** The semble bench spans 63 independently-pinned repos, and a generated corpus inside the ken checkout would otherwise report ken's HEAD as if it were an upstream pin.
- **The Python harness is pinned by a test, not by discipline.** `bench/internal/provenance/schema_test.go` reflects the Go struct's json tags into dotted paths and compares them to `_PROVENANCE_SCHEMA` in `run_ken.py`; either side gaining a field fails the test by name. `run_ken.py` takes build identity from `ken status --json` of the binary under `--ken`, not from the working tree, because it benchmarks whatever binary it was pointed at. (`status.Versions` gained a `Version` field — it was reporting a commit but not a version.)
- **`make vet-bench` is now a CI step.** `go vet ./...` skips every `//go:build bench` package, so until now the bench tree could stop compiling unnoticed. The target vets `./bench/...` under the tag and runs the provenance + schema tests; the corpus-backed harnesses skip themselves, so CI downloads nothing.

**Server side — decided, not built:** [ADR-042](DECISIONS.md#adr-042-result-provenance-for-ken-mcp--a-per-response-index-build-id-in-json-full-detail-in-status-markdown-untouched). The decision diverged from the sketch above. Measuring it, the token cost of a provenance line is 0.6% of a K=10 search response and ~6% of a K=1 one — not the blocker the sketch assumed. The real objection is that commit / chunker / mode / model / α are **constant for the process lifetime** and the `status` tool already returns all of them, so repeating them per response is noise. The one thing `status` can't answer retroactively is *which index generation* served a given response — watch mode republishes ~2 s after any edit, and an agent editing mid-session moves the index under its own queries.

So: a 12-hex `index.build_id` on the **JSON** responses only (always present, no env var — an agent can't build on an operator-gated field), the same id in `status` for correlation, and the markdown path left byte-identical for semble parity. `search.SnapshotConfigKey` (ADR-039) already encodes mode/chunker/model/enrich flags, so most of the derivation exists. Unscheduled — items 1–3 come first.

---

## Report-back checklist (the thread reply, when items land)

1. ~~α sweep curve + holdout result, and whether "flat around the middle" held.~~ **Ready to report:** yes, essentially. α_symbol is flat (argmax 0.3/0.3/0.4 across three splits, top points within 0.0005). α_NL's curve is a broad plateau whose lower edge is about where 0.5 sits — a held-out gain from 0.6 of +0.004 to +0.010, material on one split of three and flat on an independent resample. Constants kept. Curves in [`docs/BENCH.md`](../BENCH.md#α-sensitivity--is-03-05-actually-the-right-pair).
2. ~~Chunker-disagreement table, the boundary-failure share, and an answer to the cross-file-symbol question.~~ **Ready to report:** 98.6% of queries agree; the 17 that don't go 12–5 to regex (not significant); **zero** symbol-query disagreements answers the cross-file-symbol question directly. The boundary share was replaced by a stronger, query-independent measurement — treesitter cuts the definition-split rate 3.2× (0.082 → 0.026) across 276,745 definitions in 13 languages, which is the traceability win NDCG can't see.
3. Rename-survival numbers — what recall the semantic arm retains when the lexical anchor disappears.
4. ~~"Every bench JSON now carries commit/chunker/config provenance" — one sentence, links to the harness.~~ **Ready to report:** every bench result file now carries commit, chunker, mode, α pair, model digest, corpus revisions and `KEN_*` env — schema + example in [`docs/BENCH.md` → "Result provenance"](../BENCH.md#result-provenance), builder in `bench/internal/provenance`.
