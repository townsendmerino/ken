# postgres (treesitter) — Q3: autovacuum trigger  ✅ KEEPER (A/B treesitter arm — headline A/B comparison)
config: server=ken-postgres-treesitter  mode=hybrid  chunker=treesitter  index=source-only (round-2)  | ken build ~9f928fc + pre-built load fix
A/B PAIR of postgres-regex-q3-autovacuum.md. **Operational note:** first call timed out at 4 min (likely no pre-built `index.bin` in place → ken-mcp fell back to live build under contention; the second call benefited from the in-flight cache after the build completed). The predicted-vs-actual story: treesitter did NOT return the (220-line) `relation_needs_vacanalyze` as one chunk, but split it into multiple **AST-bounded** sub-chunks — including one the regex arm couldn't produce: the function's full doc comment as its own retrieval unit.

## Question (verbatim, with grounding preamble)
Use your `ken-postgres-treesitter` code-search tool to answer this from the PostgreSQL source. Don't pass a `repo` argument — just search; the tool is already pointed at the right codebase. Ground every claim in the files you retrieve (cite path:line), and refine your searches as needed.
What triggers an autovacuum?

## Tool calls (4 successful ken searches + 1 timeout, after meta tool-load)

### search 1 — `{"query":"autovacuum trigger threshold decide whether to vacuum table","top_k":8}`
**TIMED OUT** at 4 minutes ("No result received from the Claude Desktop app"). Agent retried with different query.

### search 2 — `{"query":"relation_needs_vacanalyze autovacuum threshold dead tuples","top_k":6}`
The standout chunk — **the function's full doc comment as one clean retrieval unit**:
```
1. autovacuum.c:2867-2903  0.024  ← FULL DOC COMMENT for relation_needs_vacanalyze (37 lines): opens at /* and closes at the end of the doc-comment block. Contains the authoritative natural-language description: "A table needs to be vacuumed if the number of dead tuples exceeds a threshold. This threshold is calculated as threshold = vac_base_thresh + vac_scale_factor * reltuples"; the analyze description; the forced-freeze description ("relfrozenxid is more than freeze_max_age transactions back, ... relminmxid is more than multixact_freeze_max_age multixacts back"); the autovacuum_enabled override semantics. CLEAN AST chunk: this prose was NEVER retrievable as its own unit on the regex arm (it was embedded in mid-function line-windows mixed with code)
2. pgstat_relation.c:231-276  0.018  pgstat write path: tabentry->dead_tuples, ins_since_vacuum=0, last_autovacuum_time, autovacuum_count++
3. rel.h:299-345  0.016  AutoVacOpts reloptions struct (per-table override fields: vacuum_threshold, vacuum_ins_threshold, scale_factor, freeze_max_age, etc.)
4. tableam.h:624-658  relation_vacuum table-AM hook (tangential, has the "VACUUM can be triggered by a user or by autovacuum" comment)
5. psql/tab-complete.c  storage_parameters list (autovacuum_* reloption names — false-friend bleed)
6. postgresql.conf.sample:631-680  AUTOVACUUM section: track_counts requirement, naptime 1min, vacuum_threshold 50, insert_threshold 1000 (-1 disables), analyze_threshold 50, scale_factors 0.2/0.2/0.1, freeze_max_age 200000000, multixact 400000000
```

### search 3 — `{"query":"relation_needs_vacanalyze vacthresh vacinsthresh anlthresh force_vacuum relfrozenxid freeze_max_age comparison","top_k":5}`
```
1. autovacuum.c:2904-2958  0.020  ← FUNCTION SIGNATURE + variable decls + start of parameter-source computation (55 lines). Opens cleanly at `static void relation_needs_vacanalyze(...)`. Contains all local variable declarations (force_vacuum, av_enabled, reltuples, vac_base_thresh / vac_ins_base_thresh / anl_base_thresh, vac_scale_factor / vac_ins_scale_factor / anl_scale_factor, vacthresh / vacinsthresh / anlthresh, vactuples / instuples / anltuples, freeze_max_age / multixact_freeze_max_age, xidForceLimit, relfrozenxid, multiForceLimit), then the per-relopt parameter computation starting at "vac_scale_factor = (relopts && relopts->vacuum_scale_factor >= 0)..."
2. commands/vacuum.c:1033-1083  unrelated VACUUM-relation-list code (bleed)
3. vacuum.h:182-290  VacuumParams + VacuumCutoffs structs (relfrozenxid/relminmxid/FreezeLimit/MultiXactCutoff fields)
4. rel.h:299-345  AutoVacOpts (repeat)
5. vacuumlazy.c:506-544  lazy_vacuum_heap update_relstats (tangential)
```

### search 4 — `{"query":"force_vacuum TransactionIdPrecedes xidForceLimit dovacuum vactuples greater vacthresh doanalyze instuples vacinsthresh","top_k":4}`
The **final decision** chunk:
```
1. autovacuum.c:3038-3084  0.020  ← FINAL DECISION (47 lines): the DEBUG3 elog with vac/ins/anl threshold values + THE DISJUNCTION: `*dovacuum = force_vacuum || (vactuples > vacthresh) || (vac_ins_base_thresh >= 0 && instuples > vacinsthresh);` `*doanalyze = (anltuples > anlthresh);` + no-stats fallback (`*dovacuum = force_vacuum; *doanalyze = false;`) + pg_statistic special case + function close. AST-respecting boundaries
2. heapam.c:7368-7407  heap_tuple_should_freeze (tangential — freeze decision at row level)
3. vacuumlazy.c:1041-1066  lazy_vacuum + FreeSpaceMapVacuumRange (tangential)
4. commands/vacuum.c:1248-1287  vacuum_xid_failsafe_check (related but separate failsafe path)
```

### search 5 — `{"query":"xidForceLimit recentXid freeze_max_age force_vacuum multiForceLimit MultiXactIdPrecedes vactuples dead_tuples instuples ins_since_vacuum","top_k":3}`
The **force_vacuum branch** + **threshold computation**:
```
1. autovacuum.c:2998-3038  0.019  ← (41 lines) The MultiXact wraparound branch (`force_vacuum = MultiXactIdIsValid(relminmxid) && MultiXactIdPrecedes(relminmxid, multiForceLimit);` and the analogous XID check) + `*wraparound = force_vacuum;` + EARLY-OUT: `if (!av_enabled && !force_vacuum) { *doanalyze = false; *dovacuum = false; return; }` + start of threshold computation (`if (PointerIsValid(tabentry) && AutoVacuumingActive()) { reltuples = classForm->reltuples; vactuples = tabentry->dead_tuples; instuples = tabentry->ins_since_vacuum; anltuples = tabentry->mod_since_analyze; ... vacthresh = vac_base_thresh + vac_scale_factor * reltuples; vacinsthresh = vac_ins_base_thresh + vac_ins_scale_factor * reltuples; anlthresh = anl_base_thresh + anl_scale_factor * reltuples;`)
2. commands/vacuum.c:1207-1248  vacuum_get_cutoffs multixact branch + vacuum_xid_failsafe_check head (related)
3. vacuum.h:182-290  VacuumCutoffs (repeat)
```

**`relation_needs_vacanalyze` chunk map (assembled from above):** 4 AST-bounded sub-chunks retrieved across 4 searches:
- `2867-2903` — full doc comment (37 lines)
- `2904-2958` — function signature + variable decls + parameter-source start (55 lines)
- `2998-3038` — MultiXact force_vacuum + wraparound flag + early return + threshold-formula start (41 lines)
- `3038-3084` — DEBUG3 + final dovacuum/doanalyze disjunction + no-stats fallback + close (47 lines)
- (A chunk for 2959-2997 covering the XID-side force_vacuum / `xidForceLimit` calculation likely exists but wasn't retrieved in this session.)

## Final answer (verbatim)
A table is selected for autovacuum inside `relation_needs_vacanalyze` in `src/backend/postmaster/autovacuum.c`. The final decision is the disjunction at lines 3052-3053: `*dovacuum = force_vacuum || (vactuples > vacthresh) || (vac_ins_base_thresh >= 0 && instuples > vacinsthresh)`, with analyze decided separately by `*doanalyze = (anltuples > anlthresh)` (`autovacuum.c:3054`). So three independent conditions trigger it.

1. **Dead-tuple threshold (the normal case).** A vacuum is triggered when the number of dead tuples exceeds `threshold = vac_base_thresh + vac_scale_factor * reltuples` (`autovacuum.c:2879-2881`). In code, `vactuples` comes from `tabentry->dead_tuples` and the threshold is `vacthresh = vac_base_thresh + vac_scale_factor * reltuples` (`autovacuum.c:3027, 3035`). Base and scale default to the `autovacuum_vacuum_threshold` (50) and `autovacuum_vacuum_scale_factor` (0.2) GUCs unless overridden per-table (`autovacuum.c:2895-2900`; `postgresql.conf.sample:660-665`).

2. **Insert threshold.** A vacuum is also triggered when inserts since last vacuum exceed an analogous insert threshold: `instuples` (from `tabentry->ins_since_vacuum`) `> vacinsthresh`, where `vacinsthresh = vac_ins_base_thresh + vac_ins_scale_factor * reltuples` (`autovacuum.c:3028, 3036, 3053`). Gated on `vac_ins_base_thresh >= 0`; setting `autovacuum_vacuum_insert_threshold` to -1 disables it (`postgresql.conf.sample:662`). These insert-triggered vacuums mainly exist to freeze newly inserted tuples (`pgstat_relation.c:238-242`).

3. **Forced freeze / wraparound (overrides everything, even disabled autovacuum).** `force_vacuum` is set true when the table's `relfrozenxid` is older than `freeze_max_age` transactions back, or `relminmxid` is older than `multixact_freeze_max_age` multixacts back (`autovacuum.c:2881-2884`). Concretely: `force_vacuum = MultiXactIdIsValid(relminmxid) && MultiXactIdPrecedes(relminmxid, multiForceLimit)` (and the analogous XID check), where `multiForceLimit = recentMulti - multixact_freeze_max_age` (`autovacuum.c:3000-3004`). This fires even when a table has `autovacuum_enabled = false`: the early-out at `autovacuum.c:3008-3014` only skips when `!av_enabled && !force_vacuum`. `*wraparound = force_vacuum` (`autovacuum.c:3006`) marks it as a wraparound vacuum. Defaults: `autovacuum_freeze_max_age = 200000000`, `autovacuum_multixact_freeze_max_age = 400000000` (`postgresql.conf.sample:666-669`).

Two surrounding conditions: the evaluation only runs if there are stats for the table and `AutoVacuumingActive()` is true; without stats, only `force_vacuum` causes a vacuum (`autovacuum.c:3022-3023, 3056-3062`). And the autovacuum launcher only does any of this when `autovacuum = on` with `track_counts` on (`postgresql.conf.sample:656-657`).

## Capture notes
- **KEEPER, but with operational caveat: cold-start hung 4 min on first call.** Most likely cause: pre-built `index.bin` not in place at the time, so ken-mcp fell back to live build. The retry worked, presumably because the in-flight build completed during the 4-min wait. Worth confirming you have a curated `~/tmcode/postgres/.ken/index.bin` in place; otherwise the demo binary's startup story doesn't hold.
- **Retrieval: full hit on every component of the answer.** All 17 distinct citations trace to retrieved chunks (function disjunction at 3052-3053, threshold formulas, wraparound branch, early-out, GUC defaults, AutoVacuumingActive guard).
- **Hallucination: clean.** Every claim grounded; even the "track_counts required" condition is sourced to `postgresql.conf.sample:656-657`'s verbatim "requires track_counts to also be on" comment.

## PREDICTED-vs-ACTUAL (the headline A/B finding)

**My prediction:** "the regex arm fragmented `relation_needs_vacanalyze` across two overlapping 50-line windows; treesitter should pull it as a single coherent chunk in one search."

**What actually happened:** the function is ~220 lines — long enough that treesitter's cAST splits it too. The treesitter arm needed **4 searches**, not 1, vs the regex arm's **2 searches**. By the simple "fewer searches" yardstick, **treesitter did worse on this question.**

**The more nuanced and more interesting finding (this IS the blog headline):** treesitter split the function differently than regex — into **AST-bounded sub-chunks** including ones the regex arm couldn't produce. Specifically:

- **The function's full doc comment came back as ITS OWN CHUNK** at `autovacuum.c:2867-2903`. That 37-line block is the PostgreSQL maintainers' authoritative natural-language explanation of the trigger logic: "A table needs to be vacuumed if the number of dead tuples exceeds a threshold. This threshold is calculated as `threshold = vac_base_thresh + vac_scale_factor * reltuples`. ... We also force vacuum if the table's relfrozenxid is more than freeze_max_age transactions back...". The regex arm never had this prose as its own retrieval unit — it was embedded inside line-windows that mixed it with code, and the comment block sat across two of regex's overlapping line-windows (the regex chunks `2971-3020` and `3016-3065` both straddle code+comment regions further down the function).
- The treesitter arm's answer cites **`autovacuum.c:2879-2881`** for the threshold formula — those are lines inside the doc comment. That's the kind of citation the regex arm couldn't produce.

So the real A/B story isn't "fewer chunks, fewer searches." It's: **treesitter exposes a richer set of retrieval surfaces** (doc-comment-as-chunk; signature+vars-as-chunk; final-decision-as-chunk; wraparound-branch-as-chunk) where regex carves at arbitrary line offsets. The agent here used more searches to walk that richer surface, but the answer it produced has citations the regex arm structurally couldn't have made. **More chunks isn't worse — different chunks let you cite different things.**

## CHUNK-BOUNDARY A/B (vs postgres-regex-q3-autovacuum.md)

| region of `relation_needs_vacanalyze` | regex arm | treesitter arm |
| --- | --- | --- |
| **function's full doc comment** (~37 lines) | **not retrievable** as a unit — mid-function line-windows fragment it | `autovacuum.c:2867-2903` — clean standalone chunk; the agent quotes from it directly |
| function signature + variable declarations | tangentially within `2971-3020` line-window | `autovacuum.c:2904-2958` — clean AST-bounded chunk opening at `static void relation_needs_vacanalyze(...)` |
| XID-side force_vacuum / xidForceLimit | within `2971-3020` line-window (opening mid-statement) | not retrieved this session, but presumably `~2959-2997` based on the tiling pattern |
| MultiXact force + early return + threshold formulas | overlap region `2998-3020` mixed with surrounding code | `autovacuum.c:2998-3038` — coherent block, opens at the multi-line `MultiXactId relminmxid = ...` and closes at the threshold-formula assignments |
| DEBUG3 + final `*dovacuum =` / `*doanalyze =` decision | within `3016-3065` line-window | `autovacuum.c:3038-3084` — coherent block, closes at the function close `}` |
| **Searches needed to assemble** | **2** | **4** |

**Pattern:** treesitter chunks are smaller, AST-bounded, and tile cleanly (no overlap); regex's two overlapping 50-line windows are larger per-chunk but coarser-bounded. For a long function, neither produces a single-chunk answer — but the chunk *granularity* is different in ways that affect what can be cited.
