# postgres (treesitter) — Q1: hash join vs merge join choice  ✅ KEEPER (A/B treesitter arm)
config: server=ken-postgres-treesitter  mode=hybrid  chunker=treesitter  index=source-only (round-2)  | ken build ~9f928fc (post-#31) + pre-built load fix
A/B PAIR of postgres-regex-q1-join.md. **First-call latency:** ~47ms (was 44s pre-fix #12) — pre-built load working as designed. Chunk-boundary picture more nuanced than predicted: long functions still get split, but at AST boundaries (doc-comment openings, end-of-block closings) rather than arbitrary line offsets. Notable retrieval gain over regex arm: this run surfaced `compare_path_costs_fuzzily` directly.

## Question (verbatim, with grounding preamble)
Use your `ken-postgres-treesitter` code-search tool to answer this from the PostgreSQL source. Don't pass a `repo` argument — just search; the tool is already pointed at the right codebase. Ground every claim in the files you retrieve (cite path:line), and refine your searches as needed.
How does the query planner choose between hash join and merge join?

## Tool calls (in order; outputs condensed). 6 searches (vs regex arm's 10).

### search 1 — `{"query":"choose join method hash join merge join path cost comparison","top_k":8}`
```
1. costsize.c:3789-3827  0.024  ← final_cost_mergejoin sub-chunk (38 lines): disable_cost + cost_qual_eval + skip_mark_restore + approx_tuple_count [opens at /* comment, AST-respecting sub-block]
2. joinpath.c:1879-1927  0.015  match_unsorted_outer sub-chunk (48 lines): inner try_nestloop_path tail + generate_mergejoin_paths call + partial-path commentary
3. pathnode.c:2602-2644  0.014  ← create_hashjoin_path: CLEAN function header (doc-comment + signature + body start, 42 lines)
4. cost.h:137-171  initial/final_cost_{nestloop,mergejoin,hashjoin} decls
5. costsize.c:4046-4078  0.010  ← initial_cost_hashjoin: CLEAN function header (doc-comment + signature, 32 lines)
6. pathkeys.c:1612-1644  ← select_outer_pathkeys_for_merge: CLEAN function with full doc-comment
7. pathnode.h:141-179  create_mergejoin_path / create_nestloop_path decls
8. nodeMergejoin.c:1579-1627  RIGHT/FULL join handling in executor (tangential)
```

### search 2 — `{"query":"add_path compare path costs keep cheaper discard dominated","top_k":5}`
```
1. pathnode.c:302-347  0.021  ← set_cheapest body: "If we find two paths of identical costs, try to keep the better-sorted one"; compare_path_costs(startup); compare_path_costs(total)
2. costsize.c:2301-2348  Append cost (bleed)
3. pathnode.h:1-40  set_cheapest/add_path/add_path_precheck/add_partial_path decls
4. pathkeys.c:604-648  get_cheapest_path_for_pathkeys
5. joinpath.c:786-828  try_nestloop_path tail: initial_cost_nestloop → add_path_precheck → add_path
```

### search 3 — `{"query":"add_paths_to_joinrel hash_inner_and_outer match_unsorted_outer sort_inner_and_outer","top_k":6}`
```
1. joinpath.c:278-316  0.020  ← add_paths_to_joinrel steps 1-3: sort_inner_and_outer / match_unsorted_outer / (#ifdef NOT_USED dike comment from 2000)
2. partbounds.c (partition merge — bleed)
3. joinrels.c:897-935  add_paths_to_joinrel jointype switch (INNER/LEFT/...)
4. joinpath.c:1675-1722  match_unsorted_outer body / header
5. nodeMergejoin.c:15-92  executor algorithm prose (tangential)
6. createplan.c:4504-4552  create_mergejoin_plan sort node insertion
```

### search 4 — `{"query":"hash_inner_and_outer generate hashjoin paths try_hashjoin_path","top_k":5}`
```
1. joinpath.c:1141-1190  0.022  ← try_hashjoin_path tail: add_path_precheck → create_hashjoin_path/add_path; try_partial_hashjoin_path header
2. costsize.c:4046-4078  initial_cost_hashjoin doc-header
3. src/backend/access/hash/meson.build  ← BUILD FILE BLEED (residual noise; meson.build not excluded)
4. pathnode.c:2602-2644  create_hashjoin_path
5. src/backend/access/hash/Makefile  ← BUILD FILE BLEED (Makefile not excluded)
```

### search 5 — `{"query":"consider hash join paths enable_hashjoin both relations hashable step 5","top_k":4}`
```
1. nodeHash.c:1-49  hash-table executor file header (tangential)
2. joinpath.c:316-346  0.014  ← step 4 hash + steps 5-6 (FDW push-down + set_join_pathlist_hook): "disregard enable_hashjoin for full joins"
3. dynahash.c (tangential — generic hash table impl, semantic bleed)
4. hash/meson.build (bleed)
```

### search 6 — `{"query":"add_path compare_path_costs_fuzzily costs sort order dominate keep both paths","top_k":4}`
Standout — pulled the precise fuzz-comparison rule that the regex arm hadn't surfaced:
```
1. pathnode.c:134-162  0.019  ← compare_path_costs_fuzzily doc + body: "fuzzy comparisons so add_path() can avoid keeping both of a pair of paths that really have insignificantly different cost"; "if one path is fuzzily better on startup cost and fuzzily worse on total cost, we just say that their costs are 'different', since neither dominates" — CLEAN whole-doc-comment chunk
2. indxpath.c (bleed — bitmap-and cost heuristics)
3. costsize.c:2267-2301  Append sort handling (bleed)
4. allpaths.c:1686-1714  generate_orderedappend_paths header (bleed)
```

## Final answer (verbatim)
**Short version:** the planner doesn't choose by rule — it generates candidate paths for every viable join method, estimates a cost for each, and keeps whichever survives cost comparison. No "if/else" picks hash vs merge; both are costed and the cheaper one wins (subject to tie-breaking and sort-order nuances).

1. **Both methods are generated as candidate paths.** `add_paths_to_joinrel` (`joinpath.c`) enumerates strategies in numbered steps. Merge from steps 1-2: `sort_inner_and_outer` (both sides sorted, `joinpath.c:278-284`) and `match_unsorted_outer` (nestloops + mergejoins on already-ordered outer, `:286-296`). Hash from step 4: `hash_inner_and_outer` (`:316-323`). Merge steps gated on `mergejoin_allowed`; hash step `if (enable_hashjoin || jointype == JOIN_FULL)`.

2. **Each candidate is costed in two phases.** Cheap lower-bound `initial_cost_mergejoin`/`initial_cost_hashjoin` runs first, then `add_path_precheck` decides whether it could possibly survive; only then full cost + path submitted to `add_path`. Pattern documented at `joinpath.c:801-808` ("two-phase methodology"). Prototypes co-located in `cost.h:150-176`. Models differ: `initial_cost_hashjoin` deliberately skips examining join quals beyond counting hash clauses, assuming `ExecChooseHashTableSize` is cheap (`costsize.c:4056-4060`); mergejoin model accounts for mark/restore overhead and skips it for SEMI/ANTI/inner-unique joins when all join clauses are merge clauses (`costsize.c:3807-3817`).

3. **The actual "choice" happens in `add_path` via cost comparison.** `compare_path_costs_fuzzily` (`pathnode.c:134-154`): fuzzy comparison so two nearly-equal paths aren't both retained. One path dominates if fuzzily better on one cost dimension and fuzzily no worse on the other; if each wins on a *different* dimension, costs are "different" and both are kept (`pathnode.c:147-154`). Later `set_cheapest` picks the final winner. This is why the decision is non-categorical: a hash path with low total and a merge path with useful sort order can both survive.

4. **Sort order is a real tiebreaker.** `add_path`'s cheapest-path bookkeeping explicitly prefers the better-sorted path at tied costs (`pathnode.c:321-336`: "If we find two paths of identical costs, try to keep the better-sorted one"). Merge join's ordering can satisfy `query_pathkeys` or feed an upstream merge — `select_outer_pathkeys_for_merge` is built around this (`pathkeys.c:1612-1636`). Hash join produces unordered output, so never gets this credit.

5. **The `enable_*` GUCs don't hard-disable — they penalize.** `enable_mergejoin = off` adds `disable_cost` to the path's startup cost (`costsize.c:3795-3796`). Comment: doing so "would amount to optimizing for the case where the join method is disabled, which doesn't seem like the way to bet" (`:3789-3793`). The method is still costed and can still win if every alternative is also penalized — which is also why the FULL-join carve-outs at steps 1-2/4 bypass the GUC: there may be no other legal option.

**Summary.** Merge-join path (steps 1-2, when mergejoinable) and hash-join path (step 4, when hashable) each get costed via the `initial_cost_*` → `add_path_precheck` → `final_cost_*` pipeline; `add_path` / `compare_path_costs_fuzzily` keeps whichever isn't dominated. Hash wins on raw total cost for large unsorted equi-joins; merge wins when an input is already sorted or its ordered output saves a downstream sort. `enable_hashjoin`/`enable_mergejoin` only bias via `disable_cost`, not force.

## Capture notes
- **KEEPER (A/B treesitter arm).** Pre-built index loaded instantly (~47ms vs the prior 44s) — fix #12 working as advertised. 6 searches vs the regex arm's 10 — slightly more efficient retrieval.
- **Retrieval: hit, with one sharpening over regex arm.** All load-bearing files surfaced (joinpath.c 4-step, costsize.c cost models, pathnode.c add_path machinery, pathkeys.c sort-order merge selection). Most notably, **search 6 cleanly retrieved `compare_path_costs_fuzzily` (`pathnode.c:134-162`)** — the regex arm's answer covered the "no single branch, cheapest wins" mechanism correctly but did not have this specific function's text to ground the fuzzy-comparison nuance. Treesitter arm's answer is therefore more precise on the "if each wins on a different dimension, both are kept" rule.
- **Hallucination: clean, one minor inference.** All five answer points trace to returned chunks: 4-step path generation (joinpath.c:278-316, :316-346), two-phase costing (joinpath.c:801-808), fuzzy comparison + sort-order tiebreaker (pathnode.c:134-162 + :321-336), disable_cost mechanism + "doesn't seem like the way to bet" quote (costsize.c:3789-3796 verbatim). One claim is inferred rather than directly retrieved: "Hash join produces unordered output, so never gets [sort-order] credit." The regex arm had `pathnode.c:2611-2660`'s explicit "hashjoin never has pathkeys" comment; this run didn't retrieve that chunk. Domain-correct, hedged-where-it-needed-to-be, but flag it as the one place the treesitter arm under-retrieved relative to regex.

## CHUNK-BOUNDARY A/B (vs postgres-regex-q1-join.md)

| chunk | regex arm | treesitter arm |
| --- | --- | --- |
| `initial_cost_hashjoin` area | 50-line window opening mid-comment ("* total costs. If we are unable...") at `costsize.c:4051-4100` | Clean function-with-doc-comment chunk `costsize.c:4046-4078` (32 lines, opens at `/* initial_cost_hashjoin`) |
| `create_hashjoin_path` | 50-line window in middle of function at `pathnode.c:2611-2660` | Clean function-header chunk `pathnode.c:2602-2644` (42 lines, opens at the doc-comment) |
| `compare_path_costs_fuzzily` | not directly retrieved | Clean whole-doc + body chunk `pathnode.c:134-162` (29 lines) — direct hit |
| `match_unsorted_outer` (long fn) | 50-line line-window | Multiple AST-respecting sub-chunks (e.g. `joinpath.c:1879-1927`, 48 lines, closes at end-of-block) — split but still cleanly bounded |
| 4-step `add_paths_to_joinrel` | line-window | Clean step-1/2/3-dike comment chunk `joinpath.c:278-316` (38 lines) opens at `/* 1. Consider mergejoin paths...` |

**Headline:** treesitter chunks are *consistently* AST-respecting (open at doc-comments or block starts; close at end-of-block), even when a long function is split across sub-chunks. Function-sized chunks come back as clean function-with-doc-comment units; that's where the most visible boundary improvement shows up in the demo. **Not "every function in one chunk"** — long functions still get split — but no chunks open mid-comment or mid-statement here, where every chunk in the regex arm did.

- **Tool-call count:** treesitter 6 searches vs regex 10 — the cleaner function-header openings appear to have let the agent assemble the picture with fewer refinements. Suggestive, not conclusive (n=1 per question).
- **Residual noise:** new pattern — `meson.build` and `Makefile` files showed up at low ranks twice (build-system noise). Consistent with our "stop curating" call; agent ignored them.
