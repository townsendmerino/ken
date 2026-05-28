# postgres (regex) — Q1: hash join vs merge join choice  ✅ KEEPER (A/B regex arm)
config: server=ken-postgres-regex  mode=hybrid  chunker=regex (= line-fallback on C)  index=source-only  | ken build ~9f928fc (post-#31)
A/B PAIR of postgres-treesitter-q1-join.md (same wording). NOTE for comparison: regex has no C rules, so chunks here are ~50-line line-windows that frequently START mid-construct (e.g. costsize.c:4051-4100 opens mid-comment "* total costs. If we are unable..."; joinpath.c:1486-1535 opens at "int num_sortkeys;"). Retrieval is still strong despite ragged boundaries.

## Question (verbatim, with grounding preamble)
Use your `ken-postgres-regex` code-search tool to answer this from the PostgreSQL source. Don't pass a `repo` argument — just search; the tool is already pointed at the right codebase. Ground every claim in the files you retrieve (cite path:line), and refine your searches as needed.
How does the query planner choose between hash join and merge join?

## Tool calls (in order; outputs condensed to path:line + score + gloss). 10 searches, no find_related.

### search 1 — `{"query":"choose between hash join and merge join cost comparison","top_k":8}`
```
1. src/backend/optimizer/path/costsize.c:4051-4100  0.020  ← initial_cost_hashjoin (window opens mid-comment)
2. src/backend/executor/nodeMergejoin.c:1-50  0.016  merge-join executor header
3. src/backend/optimizer/path/joinpath.c:1486-1535  0.012  ← try_mergejoin (find_mergeclauses_for_outer_pathkeys; FULL-join clauseless corner) — window opens mid-func
4. src/include/optimizer/cost.h:136-185  0.010  initial/final_cost_{nestloop,mergejoin,hashjoin} decls
5. src/backend/executor/nodeHashjoin.c:1-50  hybrid-hash-join header
6. src/backend/optimizer/README:811-860  EquivalenceClass/mergejoinable prose
7. nodeHash.c:631-680  ExecChooseHashTableSize
8. src/backend/parser/gram.y:12421-12470  ← MergeStmt (SQL MERGE — false-friend bleed)
```

### search 2 — `{"query":"add_paths_to_joinrel sort_inner_and_outer hash_inner_and_outer match_unsorted_outer","top_k":6}`
```
1. joinpath.c:271-320  0.020  ← add_paths_to_joinrel steps 1-4: sort_inner_and_outer / match_unsorted_outer / (match_unsorted_inner #ifdef NOT_USED, diked 2000) / hash
2. partbounds.c  (partition merge — bleed)
3. joinrels.c:901-950  add_paths_to_joinrel call sites per jointype
4. pathnodes.h:3196-3245  JoinPathExtraData / SemiAntiJoinFactors
5. joinpath.c:2071-2120  ← hash_inner_and_outer (scan restrictlist for hashclauses)
6. pathnode.c:2386-2435  calc_non_nestloop_required_outer
```

### search 3 — `{"query":"mergejoin_allowed select_mergejoin_clauses enable_mergejoin enable_hashjoin","top_k":6}`
```
1. joinpath.c:181-230  0.019  ← if (enable_mergejoin || JOIN_FULL) select_mergejoin_clauses(...); compute_semi_anti_join_factors
2. pathkeys.c:1576-1625  mergeclause/pathkey matching
3. costsize.c:3781-3830  0.013  ← final_cost_mergejoin: if(!enable_mergejoin) startup_cost+=disable_cost; skip_mark_restore
4. nodeMergejoin.h  / 5. nodeMergejoin.c:1576-1625 executor
6. contrib/postgres_fdw/sql/postgres_fdw.sql  ← contrib test fixture (survives src/test/ exclude — low rank)
```

### search 4 — `{"query":"hash_inner_and_outer enable_hashjoin disable_cost FULL OUTER override","top_k":5}`
```
1. costsize.c:4051-4100  initial_cost_hashjoin
2. joinpath.c:2071-2120  ← hash_inner_and_outer def
3. createplan.c:4861-4910  make_hashjoin (plan build)
4. execnodes.h:2161-2210  HashJoinState
5. nodeHash.c:406-455  ExecHashTableCreate
```

### search 5 — `{"query":"step 4 hashjoin paths disregard enable_hashjoin full joins joinpath","top_k":3}`
```
1. joinpath.c:316-365  0.023  ← step 4: if (enable_hashjoin || JOIN_FULL) hash_inner_and_outer; step 5 FDW; step 6 set_join_pathlist_hook
2. pathnode.c:2611-2660  ← create_hashjoin_path ("hashjoin never has pathkeys")
3. foreign.c  GetExistingLocalJoinPath
```

### search 6 — `{"query":"try_hashjoin_path try_mergejoin_path add_path_precheck workspace startup_cost","top_k":6}`
```
1. joinpath.c:991-1040  0.020  ← try_mergejoin_path: initial_cost → add_path_precheck → create_mergejoin_path/add_path
2. costsize.c:4141-4190  hashjoin batch I/O cost (numbatches>1 seq_page_cost)
3. pathnode.c:631-680  0.015  ← add_path_precheck (cost+pathkeys domination vs pathlist)
4. joinpath.c:1126-1175  ← try_hashjoin_path: initial_cost_hashjoin → precheck → create_hashjoin_path/add_path
5. foreign.c  / 6. costsize.c:3736-3785 final_cost_mergejoin head
```

### searches 7-9 — final_cost details (`final_cost_hashjoin ... bucketsize MCV skew` / `!enable_hashjoin startup_cost += disable_cost` / `final_cost_hashjoin virtualbuckets`)
```
costsize.c:4051-4100  initial_cost_hashjoin (repeat)
costsize.c:451-500 + :2701-2750  ← disable_cost pattern in cost_gather_merge / hashagg (shows the !enable_X → disable_cost idiom)
costsize.c:4366-4415  0.020  ← final_cost_hashjoin probe cost: hash_qual_cost × outer_rows × (inner_rows × innerbucketsize) × 0.5
nodeHash.c:721-770/811-860  ExecChooseHashTableSize skew MCV / batch sizing
```

### search 10 — `{"query":"select_mergejoin_clauses mergejoin_allowed outer join hashable mergejoinable restrictinfo","top_k":3}`
```
1. joinpath.c:2341-2390  0.022  ← select_mergejoin_clauses: keep mergeopfamilies!=NIL clauses; have_nonmergeable_joinclause → !mergejoin_allowed for outer joins
2. pathkeys.c:1576-1625  mergeclause matching
3. analyzejoins.c:1306-1340  mergejoinable test (rel_is_distinct_for)
```

## Final answer (verbatim)
The choice between hash join and merge join is not a decision made at a single branch — it falls out of PostgreSQL's general "generate every viable path, cost each one, keep the cheapest" approach. The work happens in `add_paths_to_joinrel` (`src/backend/optimizer/path/joinpath.c`), which generates both kinds of paths (when legal) and lets `add_path`'s cost comparison pick the winner.

**Step 1 — eligibility.** Merge join requires equality clauses whose operators belong to a btree opfamily; `select_mergejoin_clauses` keeps only clauses with `mergeopfamilies != NIL` and matching sides (`joinpath.c:2370-2386`), and sets `*mergejoin_allowed` false if an outer join has non-mergeable extra quals. Hash join requires hashable equality clauses; `hash_inner_and_outer` scans the restrictlist for them (`joinpath.c:2081-2120`). Both respect `enable_mergejoin`/`enable_hashjoin` GUCs, with a `JOIN_FULL` carve-out (`joinpath.c:205-212` and `:316-323`); when built despite a disabled GUC, the cost functions add `disable_cost` (`costsize.c:3793-3795` for merge).

**Step 2 — path generation.** `add_paths_to_joinrel` runs four steps (`joinpath.c:271-322`): (1) `sort_inner_and_outer` (merge, sort both sides); (2) `match_unsorted_outer` (nestloops + merge reusing an already-sorted outer); (3) `match_unsorted_inner` is `#ifdef NOT_USED` (diked 2000 as redundant); (4) `hash_inner_and_outer`. Each calls `try_mergejoin_path`/`try_hashjoin_path` → `initial_cost_*` → `add_path_precheck` → (if promising) `final_cost_*` + `add_path` (merge site `joinpath.c:991-1015`, hash site `:1136-1158`).

**Step 3 — cost models.** Hash (`initial_cost_hashjoin` `costsize.c:4073-4090`, `final_cost_hashjoin`): inner's full total cost goes into *startup* (table must be built before probing); per-column hash cost + insert cost; if `numbatches>1`, seq-page I/O for spilling (`costsize.c:4147-4156`); probe cost `hash_qual_cost × outer_rows × (inner_rows × innerbucketsize) × 0.5` (`costsize.c:4395-4398`) — `innerbucketsize` from MCV stats makes skew expensive. Merge (`final_cost_mergejoin` `costsize.c:3744`): adds `cost_sort` when a side needs sorting; charges inner-duplicate rescans (skipped via `skip_mark_restore` for SEMI/ANTI/unique, `costsize.c:3807-3818`); inherits outer pathkeys (saves downstream sort), whereas hash has no pathkeys (`pathnode.c:2628-2635`).

**Step 4 — comparison.** No `if (hash_cost < merge_cost)` exists. Both go to `add_path`; `add_path_precheck`'s loop (`pathnode.c:646-683`) dominates losers on startup/total cost + pathkeys + parameterization. **When each wins:** hash when inner fits `work_mem`, keys unskewed, nothing pre-sorted, no ordered output needed; merge when an input is already sorted (index), inner is huge enough that hash spills to many batches, keys are skewed, or downstream wants sorted output; merge is *required* for some FULL-join shapes lacking hashable clauses (`joinpath.c:1499-1508`). The planner doesn't reason about these rules explicitly — it costs both shapes and the cheaper survives.

## Capture notes
- **KEEPER (A/B regex arm).** Local index, no `repo` arg, mode=hybrid.
- **Retrieval: strong hit** despite regex line-fallback on C. All load-bearing files surfaced (costsize.c cost fns, joinpath.c path-gen + eligibility + try/precheck, pathnode.c add_path, select_mergejoin_clauses). The architecturally-correct "no single branch, cost-and-keep-cheapest" answer.
- **Hallucination: none.** Spot-checked the key claims — disable_cost (costsize.c:3781-3830), enable_hashjoin||FULL (joinpath.c:316-365), add_path_precheck (pathnode.c:631-680), innerbucketsize probe cost (costsize.c:4366-4415), no-pathkeys (pathnode.c:2611-2660), #ifdef NOT_USED dike (joinpath.c:271-320) — all trace to returned chunks.
- **CHUNK BOUNDARIES (the A/B variable):** regex = ~50-line line-windows, frequently opening mid-comment / mid-function (costsize.c:4051-4100 starts inside initial_cost_hashjoin's comment block; joinpath.c:1486-1535 starts at a bare `int num_sortkeys;`). Functionally fine for BM25+embedding retrieval, but the chunks are not function-aligned. ⏳ Compare against the treesitter arm (postgres-treesitter-q1) to see whether whole-function AST chunks improve rank/readability or are merely tidier.
- **Tool use:** 10 searches, no find_related — heavy, well-targeted refinement on a deep architectural question (eligibility → path-gen → try/precheck → cost models → comparison).
- **Residual noise (light, postgres-flavored):** `gram.y` MergeStmt (SQL MERGE false-friend), `contrib/postgres_fdw/sql/postgres_fdw.sql` (contrib test fixture — survives the `src/test/` exclude, as vscode-claude flagged), partbounds partition-merge. All low rank.
