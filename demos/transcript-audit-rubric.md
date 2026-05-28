# Transcript audit rubric (task #4)

How I'll evaluate each captured demo transcript before anything goes public. Purpose (per playbook): catch hallucination, retrieval misses, and ranking weirdness. **A ken retrieval failure is a ken bug to fix before we ship a demo built on it** — not something to paper over by re-rolling the question until it looks good. The output of this pass is, per transcript, one of: **ship-as-is**, **recapture** (rephrase / let the agent refine), or **ken-bug** (blocks ship until fixed).

## The six checks, per transcript

**1. Retrieval correctness — did ken surface the actually-relevant code?**
Against the answer key below, record: did the ground-truth file(s) appear in the returned chunks, and at what rank? Classify **hit@1 / hit@5 / miss / partial** (right file, wrong region). This is scored on ken's *raw tool output*, not the agent's answer.

**2. Ranking sanity — is the signal above the noise?**
Are the top results the real logic, or do boilerplate / tests / mocks / residual generated files (e.g. client-go `*/fake/`) / config crowd the top? Note any inversion where a tangential file out-ranks the core logic. We curated to source-only, so flag any noise that *survived* curation — it may be a new exclude candidate or a ranking issue.

**3. Hallucination trace-back — the highest-stakes check.**
Every factual claim in the agent's final answer must trace to a chunk ken actually returned. Go claim-by-claim; flag any that:
- names a file/function/symbol ken did **not** return,
- states a specific constant / threshold / formula / default not present in any returned chunk,
- describes control flow or behavior ken never surfaced (i.e. the agent is filling from training knowledge, not retrieval).
A demo where the answer is correct but ken *didn't actually find* the supporting code is a **failed demo** — it proves the model's prior, not ken. That's a recapture, not a ship.

**4. Tool-use quality — this IS the demo.**
Were the `search` / `find_related` calls sensible? Did the agent refine productively (e.g. type-name query → read → control-flow re-search → logic) or flail (redundant calls, gave up, fell back to grep)? Did `find_related` earn its place by pivoting to genuinely related code? Note the strong moments (blog-worthy) and the weak ones.

**5. Answer correctness — independent of ken.**
Is the final answer actually *true* about how the code works (verified against the returned chunks + the real k8s/postgres internals)? Separate three cases explicitly: (a) ken retrieved right **and** answer right — the win; (b) answer right but ken under-retrieved (lucky prior) — see check 3; (c) answer wrong.

**6. Postgres A/B read (postgres transcripts only).**
For each postgres question, compare the `treesitter` arm vs the `regex` (line-fallback) arm on the *same* question: which surfaced the relevant C function at a better rank, and — the real treesitter thesis — which returned **cleaner chunk boundaries** (a coherent whole-function AST chunk vs a mid-function line-window fragment)? This is the evidence that treesitter earns its keep on C, and the before/after content for the blog.

## Answer key — expected ground-truth targets

Best-estimate target locations (to confirm against the transcripts; a miss here is the signal, not a disqualifier of the question).

**kubernetes**
1. *HPA scaling decision* → `pkg/controller/podautoscaler/horizontal.go` (`reconcileAutoscaler`, `computeReplicasForMetrics`), `replica_calculator.go` (`calcPlainMetricReplicas` / utilization-ratio path), `metrics/utilization.go`. (We already saw control-flow phrasing puts these at #1–3.)
2. *kubelet pod eviction* → `pkg/kubelet/eviction/eviction_manager.go` (`synchronize`, `evictPod`), `eviction/helpers.go` (threshold/signal logic), `eviction/types.go` (`Threshold`, `Config`).
3. *scheduler extender plugins* → `pkg/scheduler/extender.go` (`HTTPExtender.Filter`/`Prioritize`/`Bind`), call sites in `pkg/scheduler/schedule_one.go` (`findNodesThatFitPod` / `prioritizeNodes`), framework wiring under `pkg/scheduler/framework/`.

**postgres**
1. *hash vs merge join* → `src/backend/optimizer/path/joinpath.go` (`add_paths_to_joinrel`, `hash_inner_and_outer`, `sort_inner_and_outer`, `match_unsorted_outer`), `optimizer/path/costsize.go` (`initial_cost_hashjoin`/`final_cost_hashjoin`, `cost_mergejoin`).
2. *WAL flush + checkpoint* → `src/backend/access/transam/xlog.c` (`XLogFlush`, `XLogWrite`, `CreateCheckPoint`), `src/backend/postmaster/checkpointer.c` (`CheckpointerMain`, checkpoint triggering / `CheckPointTimeout` / `max_wal_size`).
3. *autovacuum trigger* → `src/backend/postmaster/autovacuum.c` — specifically `relation_needs_vacanalyze` and the threshold formula (`vac_base_thresh + vac_scale_factor * reltuples`); plus `AutoVacLauncherMain` / `do_autovacuum`. (Hybrid already pulled `autovacuum.c` to #1; check whether the *threshold* chunk specifically is what surfaces, since that's the precise answer.)

## Output format I'll produce

A short scorecard per transcript:

```
<codebase> Q<n> — <verdict: ship | recapture | ken-bug>
  retrieval: hit@1 | hit@5 | miss   (target: <file> at rank <r>)
  ranking:   clean | noise: <what survived>
  hallucination: none | <claim → no supporting chunk>
  tool use:  <1-line read; note any blog-worthy moment>
  answer:    correct-and-retrieved | correct-but-under-retrieved | wrong
  [postgres only] A/B: treesitter rank <r>/boundaries <coherent|fragmented> vs regex rank <r>/<...>
```

Then an overall: the publishable 3-per-codebase set, any ken bugs that block ship (with the failing query + what ken returned, handed to vscode-claude), and the consolidated postgres treesitter-vs-regex finding for the blog.

## Calibration line

Any number or claim that makes it from this audit into the blog/release notes follows `docs/PERF.md` discipline — machine-annotated, no unverified headline claims. If retrieval is genuinely mediocre on a question, we say so (the playbook's "honest learning if adoption is weak" applies to the demo's own warts too); we don't quietly drop the hard questions and ship only the flattering ones.
