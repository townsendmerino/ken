# postgres (regex) — Q3: autovacuum trigger  ✅ KEEPER (A/B regex arm — sharpest A/B test)
config: server=ken-postgres-regex  mode=hybrid  chunker=regex (= line-fallback on C)  index=source-only  | ken build ~9f928fc (post-#31)
A/B PAIR of postgres-treesitter-q3-autovacuum.md. **The sharpest boundary-quality test:** `relation_needs_vacanalyze` is one tight function in `autovacuum.c`, but regex line-fallback FRAGMENTS it across two overlapping 50-line windows — `:2971-3020` (anti-wraparound force-vacuum) and `:3016-3065` (threshold formula), overlapping at lines 3016-3020. The agent had to retrieve BOTH windows (one per search) to assemble the full decision. Treesitter (once #12 is fixed) should return the whole function as a single coherent chunk in one search.

## Question (verbatim, with grounding preamble)
Use your `ken-postgres-regex` code-search tool to answer this from the PostgreSQL source. Don't pass a `repo` argument — just search; the tool is already pointed at the right codebase. Ground every claim in the files you retrieve (cite path:line), and refine your searches as needed.
What triggers an autovacuum?

## Tool calls (in order; outputs condensed). 2 ken searches — most efficient capture so far.

### search 1 — `{"query":"autovacuum trigger threshold decision to vacuum table","top_k":8}`
Hit #1 = the threshold formula chunk, exactly the answer:
```
1. src/backend/postmaster/autovacuum.c:3016-3065  0.022  ← THRESHOLD FORMULA (line-window, opens at "/* If we found stats..." comment):
   reltuples = classForm->reltuples;
   vactuples = tabentry->dead_tuples;
   instuples = tabentry->ins_since_vacuum;
   anltuples = tabentry->mod_since_analyze;
   vacthresh    = (float4) vac_base_thresh     + vac_scale_factor     * reltuples;
   vacinsthresh = (float4) vac_ins_base_thresh + vac_ins_scale_factor * reltuples;
   anlthresh    = (float4) anl_base_thresh     + anl_scale_factor     * reltuples;
   *dovacuum  = force_vacuum || (vactuples > vacthresh) || (vac_ins_base_thresh >= 0 && instuples > vacinsthresh);
   *doanalyze = (anltuples > anlthresh);
2. src/backend/utils/misc/guc_tables.c:3331-3380  0.019  ← GUC defaults: autovacuum_vacuum_threshold 50, _insert_threshold 1000 (-1 disables), _analyze_threshold 50, _freeze_max_age 200000000
3. src/bin/psql/tab-complete.c   tab-completion list of autovacuum_* reloptions (bleed)
4. src/backend/utils/misc/postgresql.conf.sample:631-680  0.016  ← AUTOVACUUM section: autovacuum=on requires track_counts; naptime 1min; scale factors 0.2/0.2/0.1; freeze_max_age 200M; multixact 400M
5. src/backend/access/common/reloptions.c:226-275  0.016  per-table autovacuum_* reloptions (override globals)
6. src/backend/commands/vacuum.c   VACUUM command shared code
7. src/backend/utils/activity/pgstat_relation.c:226-275  0.014  pgstat counters that feed the thresholds (dead_tuples, ins_since_vacuum, last_autovacuum_time)
8. src/backend/access/heap/vacuumlazy.c  lazy-vacuum constants (REL_TRUNCATE_*, BYPASS_THRESHOLD_PAGES) — tangential
```

### search 2 — `{"query":"force_vacuum anti-wraparound freeze_max_age relfrozenxid autovacuum","top_k":5}`
Hit #1 = the anti-wraparound branch, exactly the complementary chunk:
```
1. src/backend/postmaster/autovacuum.c:2971-3020  0.023  ← ANTI-WRAPAROUND BRANCH (line-window opens mid-statement on anl_scale_factor=...):
   freeze_max_age = (relopts && relopts->freeze_max_age >= 0) ? Min(relopts->freeze_max_age, autovacuum_freeze_max_age) : autovacuum_freeze_max_age;
   multixact_freeze_max_age = ... ;
   av_enabled = (relopts ? relopts->enabled : true);
   /* Force vacuum if table is at risk of wraparound */
   xidForceLimit = recentXid - freeze_max_age;
   relfrozenxid = classForm->relfrozenxid;
   force_vacuum = TransactionIdIsNormal(relfrozenxid) && TransactionIdPrecedes(relfrozenxid, xidForceLimit);
   if (!force_vacuum) { /* same check for MultiXacts: relminmxid vs recentMulti - multixact_freeze_max_age */ }
   *wraparound = force_vacuum;
   if (!av_enabled && !force_vacuum) { *doanalyze = false; *dovacuum = false; return; }
2. src/backend/commands/vacuum.c:1171-1220  0.018  aggressive-vacuum cutoffs (freeze_table_age, multixact)
3. src/backend/access/common/reloptions.c:271-320  0.017  autovacuum_freeze_max_age / _multixact_freeze_max_age / _freeze_table_age reloptions
4. postgresql.conf.sample (repeat)
5. guc_tables.c (repeat)
```

## Final answer (verbatim)
**What triggers an autovacuum.** The decision is per-table in `relation_needs_vacanalyze` (`src/backend/postmaster/autovacuum.c`). Two independent paths: a forced wraparound trigger and a threshold-based trigger.

1. **Anti-wraparound / freeze age (forced).** Before any threshold math, the code checks the table's XID and MultiXact age. Computes `xidForceLimit = recentXid - freeze_max_age` and sets `force_vacuum` true if `relfrozenxid` precedes that limit (`autovacuum.c:2989-2995`). If not, the same check for MultiXacts: `relminmxid` vs `recentMulti - multixact_freeze_max_age` (`:2996-3004`). Forced vacuum fires regardless of dead-tuple counts, and even if autovacuum was disabled for the table via reloptions — `if (!av_enabled && !force_vacuum)` is the only thing that lets a disabled table off the hook (`:3007-3013`). Defaults: `autovacuum_freeze_max_age = 200,000,000`, `autovacuum_multixact_freeze_max_age = 400,000,000` (`guc_tables.c:3367-3372`, `postgresql.conf.sample:676-678`).

2. **Threshold-based decision.** When stats exist and autovacuum is active, three thresholds computed as `base + scale_factor × reltuples` (`autovacuum.c:3035-3037`):
   - **Vacuum:** `vacthresh = vac_base_thresh + vac_scale_factor * reltuples` vs `tabentry->dead_tuples`. Defaults: `autovacuum_vacuum_threshold=50`, scale `0.2` (`guc_tables.c:3339-3346`, `conf.sample:665-671`).
   - **Insert vacuum:** `vacinsthresh = vac_ins_base_thresh + vac_ins_scale_factor * reltuples` vs `ins_since_vacuum`. Default threshold `1000`, scale `0.2`; setting to `-1` disables (`guc_tables.c:3347-3354`).
   - **Analyze:** `anlthresh = anl_base_thresh + anl_scale_factor * reltuples` vs `mod_since_analyze`. Defaults: threshold `50`, scale `0.1`.

   Final combine: `*dovacuum = force_vacuum || (vactuples > vacthresh) || (vac_ins_base_thresh >= 0 && instuples > vacinsthresh)` and `*doanalyze = (anltuples > anlthresh)` (`autovacuum.c:3055-3058`).

**In short:** autovacuum is triggered on a table when either its frozen XID/MultiXact age exceeds the freeze-max-age limit (forced anti-wraparound), or its accumulated dead tuples, inserts-since-vacuum, or modifications-since-analyze exceed the computed base+scale thresholds. Per-table reloptions override the corresponding global GUCs (`autovacuum.c:2971-2985`, `reloptions.c:226-...`). All of this is evaluated only when autovacuum runs and `track_counts` is on, since the thresholds rely on the cumulative stats counters.

## Capture notes
- **KEEPER, and the cleanest capture of the set so far.** 2 ken searches, no find_related — most efficient demo run we've seen. The autovacuum decision surface is tight (one function, well-named) and hybrid hit it dead-center on the first query.
- **Retrieval: hit on the precise answer.** The exact threshold formula at `autovacuum.c:3016-3065` is search-1 #1 (0.022); the anti-wraparound force-vacuum branch at `:2971-3020` is search-2 #1 (0.023). All supporting context (GUC defaults, conf.sample documentation, reloptions, pgstat counters) surfaced in the same two searches.
- **Hallucination: clean.** Every specific traces to a returned chunk: the threshold formula verbatim, force_vacuum logic, defaults (50/1000/50/200M/400M, 0.2/0.2/0.1 scale factors from `conf.sample`), the `(!av_enabled && !force_vacuum)` reloption-override semantics, `track_counts` dependency (from `conf.sample`'s `autovacuum = on # ... requires track_counts to also be on`).
- **CHUNK BOUNDARIES — the headline A/B finding.** `relation_needs_vacanalyze` got split across two overlapping line-windows: the anti-wraparound branch in `:2971-3020`, the threshold formula in `:3016-3065`, overlapping at lines 3016-3020. The agent had to assemble the full function from two separate retrievals (one per search). This is exactly the case where treesitter's whole-function AST chunk should win: one chunk = whole function = potentially one search. Treesitter prediction: search 1 surfaces `relation_needs_vacanalyze` as a single chunk; the agent gets the entire decision logic in one retrieval rather than two. **This is the cleanest visible boundary-quality story we'll have for the blog.**
- **Tool use:** 2 searches, well-targeted. No flailing, no find_related needed — the answer surface was tight enough.
- **Residual noise:** very low. `psql/tab-complete.c` (autovacuum_* reloption names — false-friend bleed) and `vacuumlazy.c` (lazy-vacuum constants — different function, related domain). Both low rank, no impact on the answer.
