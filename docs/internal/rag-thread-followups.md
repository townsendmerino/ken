# r/Rag thread follow-ups: α sweep, chunker traceability, temporal eval, result provenance

**Status:** scoped, not started · **Origin:** community feedback on the Aug 2026 r/Rag post ([thread](https://www.reddit.com/r/Rag/comments/1vwpm6t/)) · **Promise made:** "will investigate and report back" — the deliverable of every item below is a paragraph in a thread reply, backed by a reproducible number in `docs/BENCH.md`.

Two commenters, four work items. One asked for an α sweep done properly (held-out split, not tuned on the reporting set). The other pushed back on "AST chunking doesn't help": aggregate NDCG may hide value in *traceability* (does a retrieved span map cleanly to one symbol?) and *change sensitivity* (do results survive refactors?), asked for boundary-failure slicing by query intent and a temporal eval, and pointed out that results without commit/chunker/config provenance are hard to reproduce once the codebase moves.

Ordering: item 4 (provenance) first — it's small and items 2–3 want it in place so their outputs are self-describing. Then 1 (cheap, self-contained), then 2, then 3 (the only new harness).

---

## 1. α sweep on a held-out split

**Claim to test:** the inherited fusion weights (α_symbol=0.3, α_NL=0.5) are near-optimal, and RRF is flat around the middle anyway.

**What exists already.** α is adaptive per query class (`internal/search/adaptive.go:resolveAlpha`, verbatim from semble's `ranking/weighting.py`), and the `alphaOverride` parameter already plumbs through `Hybrid()` — so this is a harness change only, no search-path code. ADR-013 (corpus-adaptive α, a third query-class branch) is Deprecated and stays that way; this item is about validating the two existing constants, not adding routing.

**Design.**
- Split the 1,251 semble-bench queries **by repo, not by query** — queries from one repo share an index, and a per-query split would leak corpus statistics across the boundary. Deterministic split (hash of repo name), roughly 60/40 tune/holdout, stratified so both halves keep a similar symbol/NL mix.
- Sweep α ∈ {0.0, 0.1, …, 1.0} independently for the symbol and NL classes on the tune half (the classes use different constants, so it's two 1-D sweeps, not one 2-D grid — the classifier `isSymbolQuery` picks the constant per query).
- Take the per-class argmax from the tune half, evaluate **only that pair** once on the holdout half, and report holdout NDCG@10 and recall@10 next to the α=(0.3, 0.5) baseline on the same holdout.
- Success criterion is a *negative* result being publishable: if the tuned pair beats the baseline by less than bench noise (~±0.005 NDCG), the report-back is "swept it, flat, kept semble's constants" — which is the expected outcome and worth stating with the curve.

**The parity constraint (decided in the thread, restated here so BENCH.md's rule survives):** α=(0.3, 0.5) remains the verbatim-parity reference that the semble-benchmark comparison is run at, per BENCH.md's "don't tune ken's constants" rule. Any tuned α is reported as a separate, clearly-labeled experiment. The default shipped to users does not change unless the holdout delta is large enough to justify amending the "verbatim port" framing itself — which would need its own ADR.

**Deliverable:** a short "α sensitivity" subsection in BENCH.md with the two sweep curves (tune half), the single holdout evaluation, and the split recipe; harness under `bench/` behind the existing `bench` build tag.

**Estimate:** small. Full sweep is 11 α values × 2 classes over ~60% of the corpus; at ~30–90 s per full-corpus mode run today, expect the whole grid in well under an hour of machine time (indices build once per repo and are reused across α values, since α only affects fusion, not indexing).

---

## 2. Boundary-failure slicing by query intent

**Claim to test:** "treesitter Δ −0.004 overall, within noise" may be hiding a real win on a specific failure class that aggregate NDCG averages away.

**Design.**
- Re-run regex vs treesitter on the semble bench, but instead of comparing aggregate NDCG, extract the **per-query outcome pairs** (rank of qrel target under each chunker) from the existing result JSON (`bench/semble/results/`).
- Classify the queries where the two chunkers disagree (target found by one, missed or ranked ≥5 lower by the other) along the axes the bench already carries: query category (architecture / semantic / symbol via `by_category`), language, and — the commenter's specific axes — whether the failure is a *boundary* failure: target chunk split mid-definition, or a chunk mixing two adjacent definitions. Boundary-failure detection needs a small classifier over the chunk spans vs the file's definition spans; the enrichment pass already extracts definition locations, so this is a join, not new parsing.
- Also answer the commenter's direct question — cross-file symbol references — by slicing symbol-class disagreements on whether the qrel target file differs from the file the query's identifier is defined in.

**Deliverable:** a table in BENCH.md ("where the chunkers disagree"): disagreement count by category × language, share of disagreements that are boundary failures, and a verdict on whether ADR-011 (regex stays default) needs revisiting for any slice. If treesitter wins the boundary-failure slice decisively, that's the right amendment to the Reddit claim even if the aggregate stays flat.

**Estimate:** medium-small. No new benchmark runs needed if existing result JSONs for both chunkers are current; the work is the disagreement classifier and the definition-span join.

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

---

## Report-back checklist (the thread reply, when items land)

1. α sweep curve + holdout result, and whether "flat around the middle" held.
2. Chunker-disagreement table, the boundary-failure share, and an answer to the cross-file-symbol question.
3. Rename-survival numbers — what recall the semantic arm retains when the lexical anchor disappears.
4. "Every bench JSON now carries commit/chunker/config provenance" — one sentence, links to the harness.
