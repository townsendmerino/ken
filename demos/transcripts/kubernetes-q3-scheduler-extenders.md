# kubernetes — Q3: scheduler extender plugins  ✅ KEEPER (strongest k8s capture)
config: server=ken-kubernetes  mode=hybrid  chunker=regex  index=source-only (local, round-2 excludes)  | ken build ~9f928fc (post-#31)
Local index, no `repo` arg. The architectural "stitch multiple files into one answer" question — ken surfaced all four extender call sites across the scheduling pipeline. Lowest residual noise of the three k8s questions; no generated/fake/test/openapi.

## Question (verbatim, with grounding preamble)
Use your `ken-kubernetes` code-search tool to answer this from the Kubernetes source. Don't pass a `repo` argument — just search; the tool is already pointed at the right codebase. Ground every claim in the files you retrieve (cite path:line), and refine your searches as needed.
Where does the scheduler call out to extender plugins?

## Tool calls (in order; outputs condensed to path:line + score + gloss)
(Pre-step: one MCP tool-load meta-call — "Loaded 3 ken-kubernetes tools".)

### search 1 — `{"query":"scheduler extender filter prioritize bind","top_k":10}`
Rich, relevant first result set:
```
1. pkg/scheduler/extender.go:359-395  0.026  ← HTTPExtender.Bind / IsBinder / IsPrioritizer / IsFilter
2. pkg/scheduler/framework/extender.go:24-50  0.021  ← Extender interface (Filter/Prioritize/Bind/IsBinder/IsInterested)
3. pkg/scheduler/schedule_one.go:980-1010  0.020  ← extendersBinding: loop sched.Extenders, IsBinder&&IsInterested, extender.Bind(); TODO(#87159) make it a Plugin
4-5. pkg/scheduler/apis/config/types.go:257-278 + staging/.../kube-scheduler/config/v1/types.go  ← Extender config (URLPrefix, Filter/Preempt/Prioritize/BindVerb, Weight)
6. pkg/scheduler/scheduler.go:490-529  0.015  ← buildExtenders → NewHTTPExtender; ignorable handling
7. pkg/scheduler/apis/config/v1/defaults.go  pluginsNames (extension points)
8. pkg/scheduler/apis/config/validation/validation.go:251-281  0.013  ← validateExtenders: only one binder allowed
9. pkg/scheduler/framework/preemption/preemption.go:250-281  0.013  ← callExtenders: SupportsPreemption&&IsInterested → ProcessPreemption
10. pkg/scheduler/metrics/metrics.go  extension-point labels
```

### search 2 — `{"query":"findNodesThatFitPod findNodesThatPassExtenders extender filter","top_k":5}`
```
1. pkg/scheduler/schedule_one.go:695-726  0.038  ← findNodesThatPassExtenders: sequential loop, IsInterested skip, extender.Filter(pod, feasibleNodes), IsIgnorable skip-on-error
3. pkg/scheduler/framework/extender.go:51-79  0.013  Extender interface tail (ProcessPreemption/SupportsPreemption/IsIgnorable)
5. pkg/scheduler/extender.go:242-289  0.011  ← HTTPExtender.Filter (builds ExtenderArgs, h.send(filterVerb))
(2,4: interpodaffinity / podtopologyspread filtering — other scheduler plugins, semantic bleed)
```

### search 3 — `{"query":"extender Prioritize score aggregation scheduling cycle","top_k":5}`
```
1. pkg/scheduler/schedule_one.go:791-826  0.019  ← Prioritize fan-out: per interested extender, goroutine extender.Prioritize(pod,nodes); weight×score merged into NodePluginScores; errors ignored
2. pkg/scheduler/framework/extender.go:24-50  Extender interface
3,5. config types.go:279-300 + v1/types.go  ← HTTPTimeout (Filter timeout fails scheduling; Prioritize timeout ignored), ManagedResources (Filter/Prioritize/Bind iff pod requests managed resource)
4. pkg/scheduler/framework/interface.go:123-152  score constants (bleed)
```

### search 4 — `{"query":"findNodesThatFitPod sched.Extenders call site","top_k":3}`
```
1. pkg/scheduler/schedule_one.go:397-435  0.023  ← schedulePod: findNodesThatFitPod → prioritizeNodes(ctx, sched.Extenders, ...) call site
2. pkg/scheduler/scheduler.go:367-389  Scheduler struct (Extenders field)
3. pkg/scheduler/schedule_one.go:538-580  0.010  ← evaluateNominatedNode: 2nd findNodesThatPassExtenders call site (NominatedNodeName path); hasScoring/hasExtenderFilters
```

## Final answer (verbatim)
**Where the scheduler calls extenders.** The `Extender` interface is in `pkg/scheduler/framework/extender.go:24-79` (`Filter`, `Prioritize`, `Bind`, `ProcessPreemption`). The HTTP implementation is `pkg/scheduler/extender.go` — a client POSTing to the verb-suffixed URL (`Filter` at `extender.go:242-289`, `Bind` at `extender.go:359-379`). Each `HTTPExtender` is built by `buildExtenders` (`pkg/scheduler/scheduler.go:490-529`) from the `Extender` config (`pkg/scheduler/apis/config/types.go:257-300`) and stored as `sched.Extenders`. That slice is called from four places:

1. **Filter (post-predicate narrowing).** `schedulePod` → `findNodesThatFitPod` → `findNodesThatPassExtenders` (`pkg/scheduler/schedule_one.go:695-726`): iterates extenders sequentially, skips `!IsInterested(pod)`, calls `extender.Filter(pod, feasibleNodes)`, shrinking the set. Second call site at `schedule_one.go:550` in `evaluateNominatedNode` (used when a pod has a `NominatedNodeName` from prior preemption).

2. **Prioritize (scoring).** `schedulePod` calls `prioritizeNodes(ctx, sched.Extenders, ...)` (`schedule_one.go:435`); fan-out at `schedule_one.go:798-815`: per interested extender a goroutine calls `extender.Prioritize(pod, nodes)`, result × weight merged into per-node `NodePluginScores`. Extender prioritization errors are deliberately swallowed so other scorers still decide.

3. **Bind.** After framework Bind plugins, `sched.extendersBinding` (`schedule_one.go:980-998`) walks `sched.Extenders`, picks the one with `IsBinder() && IsInterested(pod)`, calls `extender.Bind(&v1.Binding{...})`. At most one binder — `validateExtenders` (`pkg/scheduler/apis/config/validation/validation.go:251-281`) rejects >1. (`TODO(#87159)`: should become a plugin.)

4. **Preemption.** Default preemption PostFilter → `callExtenders` (`pkg/scheduler/framework/preemption/preemption.go:250-281`): iterates extenders, checks `SupportsPreemption() && IsInterested(pod)`, calls `extender.ProcessPreemption(pod, victimsMap, nodeLister)` to prune candidates/victims.

Details: Filter and Bind respect `IsIgnorable()` (error logged + skipped — `schedule_one.go:716-720`, `:990-993`); Prioritize errors are always non-fatal. Filter extenders run sequentially shrinking the list; Prioritize extenders run concurrently via `WaitGroup`; Bind picks the first matching binder. `IsInterested` (driven by `ManagedResources`) gates every call site — pods requesting no extender-managed resource bypass that extender.

## Capture notes
- **KEEPER — best of the three k8s questions.** This is the architectural/multi-file question, and ken stitched the full picture: all four call sites plus the interface, HTTP impl, config, builder, and validation.
- **Retrieval: excellent hit.** Every load-bearing file surfaced across 4 searches; the first query alone pulled the interface, HTTP Bind, extendersBinding, config, builder, validateExtenders, and preemption callExtenders. Strong evidence for ken's "show me the surface of this area" pitch.
- **Hallucination: none.** All four call-site line ranges, the single-binder rule, the IsIgnorable handling, the Prioritize-errors-ignored behavior, and the `TODO(#87159)` all trace to returned chunks.
- **Tool use:** 4 searches, no find_related — tight, well-targeted refinement (methods → Filter narrowing → Prioritize fan-out → call sites).
- **Residual noise (lowest of the three):** only mild same-domain bleed (interpodaffinity / podtopologyspread filtering plugins on a "filter" query; framework score constants). No generated/fake/test/openapi. Reinforces the "stop curating" call.

---
## kubernetes set complete (3/3): all keepers
Q1 HPA — keeper (needed refinement past API-type boilerplate). Q2 eviction — keeper (clean first-query hit). Q3 scheduler extenders — keeper (architectural multi-file stitch, lowest noise). Cross-cutting finding for the blog: first-query retrieval quality tracks generated-code overlap of the topic — a type-name-heavy area (HPA) needs an agent refinement step; concept-named areas (eviction, scheduler extenders) hit clean immediately. All answers fully grounded, zero hallucinations after the round-2 curation. Residual noise across all three is same-domain semantic bleed, never generated/fake/test/openapi.
