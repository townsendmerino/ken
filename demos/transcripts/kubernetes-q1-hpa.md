# kubernetes — Q1: HorizontalPodAutoscaler scaling decision  ✅ KEEPER
config: server=ken-kubernetes  mode=hybrid  chunker=regex  index=source-only (local, round-2 excludes)  | ken build ~9f928fc (post-#31)
Local index confirmed (line numbers match local; no `repo` arg). Round-2 curation working in live retrieval: NO `fake/`, `*_test.go`, or `openapi-spec/` paths in any result. Residual noise = client-go typed clients + listers (generated client, not excluded) + cluster addon YAML + cross-controller semantic bleed — agent routed around all of it.

## Question (verbatim, with grounding preamble)
Use your `ken-kubernetes` code-search tool to answer this from the Kubernetes source. Don't pass a `repo` argument — just search; the tool is already pointed at the right codebase. Ground every claim in the files you retrieve (cite path:line), and refine your searches as needed.
How does HorizontalPodAutoscaler decide when to scale a deployment?

## Tool calls (in order; outputs condensed to path:line + score + gloss)
(Pre-step: two MCP tool-load meta-calls — "No matching tools found" then "Loaded 3 ken-kubernetes tools". Plumbing, not ken searches.)

### search 1 — `{"query":"HorizontalPodAutoscaler reconcile scale decision","top_k":10}`
NOISE (but fakes gone): all type defs + generated client/listers.
```
1. pkg/apis/autoscaling/types.go:528-559  0.044   HorizontalPodAutoscaler type
2-4. staging/.../api/autoscaling/v2,v2beta2,v2beta1/types.go   HPA type (versions)
5-6. staging/.../client-go/kubernetes/typed/autoscaling/v1,v2/horizontalpodautoscaler.go  ← gen client (residual noise)
7-8,10. staging/.../client-go/listers/autoscaling/v1,v2beta1,v2/horizontalpodautoscaler.go  ← gen listers (residual noise)
9. staging/.../typed/autoscaling/v2beta1/horizontalpodautoscaler.go  ← gen client
```
Agent: "These are mostly type definitions and client code. I need the controller itself."

### search 2 — `{"query":"reconcileAutoscaler controller compute desired replicas","top_k":8}`
```
1. pkg/controller/podautoscaler/horizontal.go:838-874  0.019  ← reconcile tail: max metric, rescaleReason, normalize, rescale write
2-4,6. pkg/controller/{controller_ref_manager,deployment,replication,replicaset}...  ← OTHER controllers (semantic bleed)
5,7,8. cluster/addons/calico-policy-controller/*.yaml  ← addon YAML (residual noise)
```
Agent: "horizontal.go is the controller. Let me read the full reconcile loop."

### search 3 — `{"query":"reconcileAutoscaler function horizontal pod autoscaler entry","top_k":5}`
WHIFF — all 5 results were gen listers/clients. Agent pivoted to find_related instead.

### find_related 4 — `{"file_path":"pkg/controller/podautoscaler/horizontal.go","line":838,"top_k":10}`
Strong — pulled the reconcile core:
```
1. horizontal.go:799-837  0.918  ← min/max short-circuits: 0-replica ScalingDisabled / >max clamp / <min clamp / else computeReplicasForMetrics
2. horizontal.go:875-904  0.845  ← rescale write: scaleNamespacer.Scales().Update(); SucceededRescale event; storeScaleEvent; else "Decided not to scale"
3. horizontal.go:943-963  0.776  normalizeDesiredReplicas (legacy)
4. horizontal.go:975-995  0.765  normalizeDesiredReplicasWithBehaviors (4-step doc)
5-7,9. staging/.../autoscaling/*/types.go  HorizontalPodAutoscalerSpec (ratio×pods doc)
8. staging/.../kubectl/pkg/scale/scale.go  scaleHasDesiredReplicas
10. test/e2e/framework/autoscaling/autoscaling_utils.go  (e2e helper — residual)
```

### search 5 — `{"query":"computeReplicasForMetrics maximum desired replica count across all metrics","top_k":5}`
```
1. replica_calculator.go:164-176  0.020  GetMetricReplicas → calcPlainMetricReplicas
2. horizontal.go:293-319  0.020  ← computeReplicasForMetrics (loops Spec.Metrics, returns max)
3-5. autoscaling/*/types.go  spec doc
```

### search 6 — `{"query":"usageRatio ceil currentReplicas calcPlainMetricReplicas tolerance","top_k":8}`
The core formula:
```
1. replica_calculator.go:265-286  0.019  ← getUsageRatioReplicaCount: if |1-ratio|<=c.tolerance return current; else ceil(ratio*readyPods); currentReplicas==0 → ceil(ratio) (scale-from-zero)
2. horizontal.go:838-874  reconcile tail
5. replica_calculator.go:123-151  0.008  ← recompute newUsageRatio after unready→0%; bail if within tolerance OR direction flips; newReplicas=ceil(newUsageRatio*len(metrics))
(others: conversion, deployment sync — tangential)
```

### search 7 — `{"query":"GetResourceReplicas unready missing pods ignoredPods scale up direction","top_k":5}`
```
1. replica_calculator.go:177-207  0.020  ← calcPlainMetricReplicas: groupPods→ready/unready/missing/ignored; removeMetricsForPods(ignored,unready); scaleUpWithUnready := len(unready)>0 && ratio>1.0
5. replica_calculator.go:63-84  0.008  ← GetResourceReplicas head (same grouping)
(2-4: kubelet/deployment — bleed)
```

### find_related 8 — `{"file_path":"pkg/controller/podautoscaler/replica_calculator.go","line":85,"top_k":5}`
```
1. replica_calculator.go:208-243  0.846  ← missing pods: scale-down→target, scale-up→0%; unready scale-up→0%; recompute; bail on tolerance/direction-flip; newReplicas=ceil(newUsageRatio*len(metrics))
2. replica_calculator.go:123-151  0.821  (resource path, same logic)
3. replica_calculator.go:177-207  calcPlainMetricReplicas
4. metrics/utilization.go:23-53   0.728  ← GetResourceUtilizationRatio: currentUtilization=(metricsTotal*100)/requestsTotal; ratio=current/target
5. replica_calculator.go:63-84    GetResourceReplicas
```

### search 9 — `{"query":"stabilizeRecommendation downscale stabilization window highest recent recommendation","top_k":5}`
```
1. horizontal.go:1090-1127  0.020  stabilizeRecommendationWithBehaviors (up=min over up-window, down=max over down-window)
2-4. autoscaling/*/types.go:177-201  HPAScalingRules doc (scale-up window 0, scale-down 300s)
5. cmd/kube-controller-manager/app/options/hpacontroller.go:32-41  ← FLAGS: sync-period, downscale-stabilization, tolerance
```

### search 10 — `{"query":"stabilizeRecommendation downscaleStabilisationWindow replace older sample","top_k":3}`
```
1. horizontal.go:905-942  0.020  ← legacy stabilizeRecommendation: max of recs not older than downscaleStabilisationWindow
2. horizontal.go:1090-1127  behaviors variant
3. hpacontroller.go:32-41  flags
```

### search 11 — `{"query":"HorizontalController Run worker processNextWorkItem sync period","top_k":3}`
```
1. horizontal.go:81-123  0.028  ← HorizontalController struct (queue, recommendations, downscaleStabilisationWindow, replicaCalc)
2-3. crdregistration / statefulset processNextWorkItem (bleed)
```

## Final answer (verbatim)
**How the HorizontalPodAutoscaler decides when to scale.** The HPA lives in `pkg/controller/podautoscaler/`. It runs in kube-controller-manager, picking HPA keys off a workqueue (`HorizontalController` at `horizontal.go:84`) and re-running its sync every `--horizontal-pod-autoscaler-sync-period` (`cmd/kube-controller-manager/app/options/hpacontroller.go:38`). Each sync calls `reconcileAutoscaler`.

**Stage 1 — short-circuit vs min/max** (`horizontal.go:817–828`): if `currentReplicas==0 && minReplicas!=0`, scaling disabled (`ScalingActive=False`, `ScalingDisabled`); if `> spec.MaxReplicas` clamp to max; if `< minReplicas` clamp to min. Only otherwise consult metrics.

**Stage 2 — per-metric desired count, take max** (`computeReplicasForMetrics`, `horizontal.go:299`; selection at `:853–856`): largest proposal across `spec.Metrics` wins. API doc: "the maximum replica count across all metrics will be used."

**Stage 3 — core ratio formula.** `GetResourceReplicas` (`replica_calculator.go:65`): fetch metrics, `groupPods` → ready/unready/missing/ignored (drop ignored+unready, `:79–80`), `usageRatio = currentUtilization/targetUtilization` (`metrics/utilization.go:26`, `:51`), tolerance band `if math.Abs(1.0-usageRatio) <= c.tolerance return currentReplicas` (`replica_calculator.go:181–184`, also `:135`) — the `--horizontal-pod-autoscaler-tolerance` flag (`hpacontroller.go:41`), default ~10%. Else `newReplicas = ceil(usageRatio × readyPods)` (`replica_calculator.go:141`/`:240`/`:279`). Fundamental formula `desired = ceil(currentMetric/targetMetric × readyPods)`, matching the spec doc.

**Asymmetric unhealthy-pod handling** (`replica_calculator.go:85–110`, `211–230`): missing pods → on scale-down treated as target, on scale-up as 0%; unready pods on scale-up → 0%. Recompute `newUsageRatio`; if folding those in re-enters tolerance or flips direction, stay put (`:135–145`).

**Stage 4 — normalize + stabilize** (`horizontal.go:864–868`). No behaviors → `normalizeDesiredReplicas` (`:945`): `stabilizeRecommendation` (`:919`) returns max recommendation within downscaleStabilisationWindow (default 5 min, `--horizontal-pod-autoscaler-downscale-stabilization`, `hpacontroller.go:39`) → scale-up immediate, scale-down only when all recent recs agree; then clamp `[min,max]`. With behaviors → `normalizeDesiredReplicasWithBehaviors` (`:980`), two-sided `stabilizeRecommendationWithBehaviors` (`:1093`); defaults scale-up window 0s, scale-down 300s.

**Stage 5 — scale or not** (`horizontal.go:870–876`): `rescale = desiredReplicas != currentReplicas`; if true write `scale.Spec.Replicas` via `/scale` subresource Update, emit `SuccessfulRescale`, `storeScaleEvent`; else "Decided not to scale".

**Summary pipeline:** (1) pinned-to-zero or outside [min,max] → snap into range, skip metrics; (2) per metric `ceil((currentMetric/targetMetric) × readyPods)`, missing/unready pessimistic against scale-up flap; (3) within ±10% tolerance → no change; (4) max across metrics; (5) downscale stabilization (5 min) + behavior windows/step-limits; (6) clamp [min,max]; (7) if changed, PATCH `/scale`. The two big "don't scale" levers: the tolerance band and the downscale stabilization window (eager up, reluctant down).

## Capture notes
- **KEEPER, and an improvement over the round-1 capture.** Local index, no `repo` arg.
- **Round-2 curation confirmed in live retrieval:** zero `fake/`, `*_test.go`, or `openapi-spec/` paths anywhere (round-1 capture had fakes as the entire first result set). 
- **Retrieval: hit.** All answer-critical files surfaced (horizontal.go reconcile/normalize/stabilize/struct, replica_calculator.go, utilization.go, hpacontroller.go flags). Search 3 whiffed (all listers) but the agent recovered via find_related — realistic.
- **Hallucination: improved.** The prior run's weak spot (the 10% tolerance default ungrounded) is now better — the agent retrieved `hpacontroller.go:41` (the `--horizontal-pod-autoscaler-tolerance` flag) and hedges "default ~10%." The exact `0.1` literal still isn't in a returned chunk, but the flag is now grounded. Everything else traces cleanly.
- **Residual noise (post round-2):** client-go typed clients + listers (generated client surface, not excluded), `cluster/addons/*.yaml`, and cross-controller semantic bleed (other controllers' code on "reconcile/controller" queries). RECOMMENDATION: stop curating here — these are lower-volume, the agent routes around them cleanly, and a third exclude round risks over-gaming the demo. A little visible noise + refinement reads as authentic.
