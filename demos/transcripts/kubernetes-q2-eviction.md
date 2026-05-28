# kubernetes — Q2: kubelet pod eviction trigger  ✅ KEEPER
config: server=ken-kubernetes  mode=hybrid  chunker=regex  index=source-only (local, round-2 excludes)  | ken build ~9f928fc (post-#31)
Local index, no `repo` arg. Notably cleaner first-query retrieval than Q1 — "eviction" lacks the generated-boilerplate overlap that "HorizontalPodAutoscaler" had, so the controller code surfaces at #1 immediately. Residual noise is light cross-domain bleed (kubeadm, emptydir volume, devicemanager, a hack/ shell script, API-server eviction). No fake/test/openapi.

## Question (verbatim, with grounding preamble)
Use your `ken-kubernetes` code-search tool to answer this from the Kubernetes source. Don't pass a `repo` argument — just search; the tool is already pointed at the right codebase. Ground every claim in the files you retrieve (cite path:line), and refine your searches as needed.
What triggers a pod eviction by kubelet?

## Tool calls (in order; outputs condensed to path:line + score + gloss)
(Pre-step: two MCP tool-load meta-calls — "No matching tools found" then "Loaded 3 ken-kubernetes tools". Plumbing.)

### search 1 — `{"query":"kubelet eviction manager synchronize thresholds","top_k":8}`
Clean first hit — controller core at #1:
```
1. pkg/kubelet/eviction/eviction_manager.go:329-364  0.026  ← synchronize: thresholdsMetGracePeriod, update state, localStorageEviction, sort byEvictionPriority
2. pkg/kubelet/eviction/helpers.go:996-1025  0.022  thresholdsUpdatedStats / thresholdsFirstObservedAt
3. pkg/kubelet/kubelet.go:1545-1571  0.016  initializeRuntimeDependentModules → evictionManager.Start(...)
4. cmd/kubelet/app/options/options.go:488-492  0.013  ← eviction-soft/-grace-period/-pressure-transition/-max-pod-grace/-minimum-reclaim flags
5. pkg/kubelet/apis/config/types.go:301-320  0.013  ← EvictionHard/Soft/SoftGracePeriod/PressureTransitionPeriod/MaxPodGracePeriod/MinimumReclaim
6. pkg/kubelet/eviction/types.go:45-73  0.012  ← Config{Thresholds...}; Manager interface (IsUnderMemoryPressure etc.)
7. cmd/kubeadm/app/phases/kubelet/kubelet.go:48-66  (kubeadm TryStopKubelet — bleed)
8. pkg/kubelet/cm/helpers.go:31-54  hardEvictionReservation
```

### search 2 — `{"query":"eviction signal memory.available nodefs imagefs pid","top_k":6}`
```
1. pkg/kubelet/eviction/helpers.go:867-898  0.022  makeSignalObservations (nodeFs/imageFs/containerFs available+inodes)
2. pkg/kubelet/eviction/api/types.go:28-46  0.017  ← Signal constants: memory.available, nodefs.available/inodesFree, imagefs.available/inodesFree, containerfs...
3. staging/.../kubelet/config/v1beta1/types.go:496-525  0.014  ← EvictionHard defaults doc (memory 100Mi, nodefs 10%/5%, imagefs 15%), PressureTransitionPeriod Default 5m
4. hack/local-up-cluster.sh:1171-1220  (shell parse_eviction — bleed)
5. pkg/kubelet/eviction/defaults_linux.go:1-29  0.010  ← DefaultEvictionHard map (the 100Mi/10%/5%/15%/5% defaults)
6. pkg/kubelet/eviction/eviction_manager.go:99-113  managerImpl fields (thresholdNotifiers)
```

### search 3 — `{"query":"SignalAllocatableMemoryAvailable SignalPIDAvailable signal constants","top_k":5}`
```
1. pkg/kubelet/eviction/helpers.go:78-98  0.019  ← signalToNodeCondition map (memory→NodeMemoryPressure, disk→NodeDiskPressure, pid→NodePIDPressure) + signalToResource
2. pkg/kubelet/eviction/api/types.go:47-66  0.017  ← containerfs/allocatableMemory/pid signal constants
3. pkg/kubelet/eviction/helpers_windows.go:29-48  windows memory observation
4. pkg/kubelet/eviction/api/types.go:67-85  0.008  ← OpForSignal: all signals map to OpLessThan
5. pkg/kubelet/eviction/helpers.go:835-840  0.008  byEvictionPriority.Less (memory ranked first)
```

### search 4 — `{"query":"reclaimNodeLevelResources evict pod synchronize after sort","top_k":5}`
```
1. pkg/kubelet/eviction/eviction_manager.go:365-397  0.023  ← getReclaimableThreshold; EvictionThresholdMet event; reclaimNodeLevelResources gate ("must evict pod(s)"); rank pods
2. pkg/kubelet/eviction/helpers.go:1229-1255  evictionMessage
3. pkg/kubelet/pod_workers.go:212-235  ShouldPodContentBeRemoved (eviction cleanup — tangential)
4. pkg/registry/core/pod/storage/eviction.go:125-164  ← API-server Eviction (DIFFERENT mechanism — bleed, not kubelet)
5. pkg/kubelet/cm/devicemanager/pod_devices.go  (bleed)
```

### search 5 — `{"query":"reclaimNodeLevelResources image garbage collection","top_k":4}`
```
1. pkg/kubelet/images/image_gc_manager.go:83-102  0.022  ImageGCPolicy (High/Low threshold, MinAge/MaxAge)
2. pkg/kubelet/eviction/eviction_manager.go:455-484  0.019  ← reclaimNodeLevelResources: run nodeReclaimFuncs, re-observe, return true if thresholds satisfied
3. pkg/kubelet/eviction/helpers.go:1215-1228  0.017  ← signalToReclaimFunc: nodefs/imagefs → DeleteAllUnusedContainers + DeleteUnusedImages
4. pkg/kubelet/kubelet.go:1476-1492  image GC loop
```

### search 6 — `{"query":"memcg threshold notifier synchronize trigger memory pressure","top_k":5}`
```
1. pkg/kubelet/eviction/memory_threshold_notifier_others.go:96-123  0.027  ← linux memcg notifier Start() → handler on cgroup event
2. pkg/kubelet/eviction/memory_threshold_notifier_windows.go:50-89  0.026  windows polling notifier
3. pkg/kubelet/eviction/threshold_notifier_linux.go:182-200  linuxCgroupNotifier Stop
4. pkg/kubelet/eviction/eviction_manager.go:183-214  0.019  ← Start(): thresholdHandler→synchronize; memcg notifiers for memory signals; periodic synchronize loop
5. pkg/kubelet/eviction/types.go:141-170  CgroupNotifier/ThresholdNotifier interfaces
```

### search 7 — `{"query":"localStorageEviction emptyDir ephemeral storage limit exceeded","top_k":3}`
```
1. pkg/volume/emptydir/empty_dir.go:211-236  0.042  emptyDir struct (sizeLimit)
2. pkg/kubelet/eviction/eviction_manager.go:53-64  0.019  ← signalEphemeralContainerFsLimit / signalEphemeralPodFsLimit / signalEmptyDirFsLimit; immediateEvictionGracePeriodSeconds=1
3. staging/.../kubelet/config/v1beta1/types.go:823-845  0.017  ← LocalStorageCapacityIsolation doc (emptyDir sizeLimit triggers eviction)
```

## Final answer (verbatim)
**What triggers a pod eviction by kubelet.** Kubelet's eviction manager runs a control loop that watches node-level resource signals against configured thresholds. When a threshold is crossed (and any grace period elapsed), and pressure can't be relieved by reclaiming node-level resources first, it picks pods to evict. There's also a parallel path for per-pod local-storage limits.

1. **The control loop** (`pkg/kubelet/eviction/eviction_manager.go:184-214`): `Start` runs `synchronize` on a timer (monitoringInterval), and — on Linux with `KernelMemcgNotification`, always on Windows — also wires memory cgroup notifiers that call `synchronize` out-of-band when a memory threshold is crossed (`memory_threshold_notifier_others.go:104-110` linux; `memory_threshold_notifier_windows.go:50-81` windows polling).

2. **The signals** (`pkg/kubelet/eviction/api/types.go:28-57`): `memory.available`, `nodefs.available`/`nodefs.inodesFree`, `imagefs.available`/`imagefs.inodesFree`, `containerfs.available`/`inodesFree`, `allocatableMemory.available`, `pid.available`. Each maps to a node condition (`helpers.go:78-89`: memory→NodeMemoryPressure, disk/inode→NodeDiskPressure, pid→NodePIDPressure). Only operator is `LessThan` (`api/types.go:75-85`) — fires when an "available" signal drops below threshold.

3. **The thresholds** (`pkg/kubelet/apis/config/types.go:305-319`): `EvictionHard`, `EvictionSoft` + `EvictionSoftGracePeriod`, `EvictionPressureTransitionPeriod`, `EvictionMaxPodGracePeriod`, `EvictionMinimumReclaim`. Hard fires immediately; soft fires only after the signal stays below threshold for its grace period (`--eviction-soft` help, `options.go:488-489`; grace logic `eviction_manager.go:333` `thresholdsMetGracePeriod`). Linux defaults if unconfigured (`defaults_linux.go:22-29`): `memory.available<100Mi`, `nodefs.available<10%`, `nodefs.inodesFree<5%`, `imagefs.available<15%`, `imagefs.inodesFree<5%`.

4. **Decision logic inside `synchronize`** (`eviction_manager.go:329-397`): observe signals → determine thresholds met whose grace elapsed → **localStorage capacity isolation check first** (evict pods exceeding container/pod ephemeral or emptyDir `sizeLimit` when `LocalStorageCapacityIsolation` on; internal signals at `:53-60`) → if no node threshold met, return → sort thresholds by priority (memory first, `helpers.go:835-839`) → **reclaim node-level resources before evicting**: for disk pressure, `containerGC.DeleteAllUnusedContainers` / `imageGC.DeleteUnusedImages` (`helpers.go:1215-1222`), and if that satisfies thresholds, no pod evicted (`:455-484`) → only if reclaim fails, rank active pods and evict. The commit point: `if m.reclaimNodeLevelResources(...) { return } ... "must evict pod(s) to reclaim"` (`:374-380`), with the `EvictionThresholdMet` event at `:372`.

**Summary of triggers:** (1) a **hard** threshold crossed on any monitored signal and node-level reclaim can't recover it; (2) a **soft** threshold crossed continuously for its grace period (same reclaim-first rule); (3) a pod exceeding a **local ephemeral-storage limit** (container/pod ephemeral or emptyDir sizeLimit) under `LocalStorageCapacityIsolation`, checked before node-signal evaluation. Triggering happens on the monitoring tick or, for memory, out-of-band on a kernel memcg notification (Linux) / commit-pressure poll (Windows).

## Capture notes
- **KEEPER.** Local index; round-2 curation holding (no fake/test/openapi paths).
- **Retrieval: strong hit, cleaner than Q1.** `eviction_manager.go:synchronize` was #1 on the very first query — "eviction" has far less generated-code overlap than "HorizontalPodAutoscaler," so no boilerplate crowding. All answer-critical files surfaced (eviction_manager.go, helpers.go, api/types.go, defaults_linux.go, the notifiers, config types).
- **Hallucination: none.** Every specific traces to a returned chunk — the 100Mi/10%/5%/15% defaults (defaults_linux.go:22-29), hard-vs-soft grace semantics (options.go + synchronize), the signal list + LessThan operator (api/types.go), the 5m PressureTransitionPeriod default (v1beta1/types.go). Clean.
- **Tool use:** 7 searches, no find_related — steady drill from the loop → signals → thresholds → reclaim → notifier → localStorage. No flailing.
- **Residual noise (light):** kubeadm `TryStopKubelet`, `hack/local-up-cluster.sh`, `pkg/volume/emptydir`, devicemanager, and notably `pkg/registry/core/pod/storage/eviction.go` (the *API-server* Eviction/PDB mechanism — a real conceptual neighbor but a different system). All low-rank; agent didn't confuse them. Corroborates "stop curating" — remaining noise is cross-domain semantic bleed, not generated code.
