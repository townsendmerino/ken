# Cold-start campaign — task doc

Status: in progress (M0 done; M1 Increments 1+2 wired — clean-load + incremental reconcile; perf follow-ups + M2 pending)
Scope: `ken-mcp` cold start → first servable query
Related: ADR-036 (startup campaign: lazy rerank, parallel structural), ADR-038 (`.kenignore`), ADR-039 (snapshot + reconcile), memory campaign (v1.2.0), `docs/PERF-expectations.md`, `internal/search/index_serialize.go`, `docs/internal/cold-start-M0-findings.md`

## Problem

Cold start is the worst number in the external bench (Trofimov, 2026-07-18): 256.6 s
to first servable query on a ~7.9k-file PHP/Yii monorepo (i5-6300U, 4 cores). v1.2.x
ignore parity + minified-skip shrinks the corpus, but three structural facts remain:

1. **Every `ken-mcp` launch rebuilds the index from scratch.** The serialize/load
   format exists (`BuildAndSerializeIndex` / `LoadSerializedIndex` in
   `internal/search/index_serialize.go`) but the server boots through
   `NewWatchedIndexWithContext` → `FromFS` every time. Cold start is currently a
   **policy, not physics** — every IDE restart re-pays the full walk+chunk+embed.

2. **Embedding is NOT the bottleneck.** Kernel measurement (2026-07-24, M1 Pro,
   826k chunks): bm25-only index 471.9 s vs hybrid 501.9 s — **embedding ≈ 6 % of
   index time**. The cost lives in walk / chunk / tokenize / `structural.Build`.
   M0 profiling (below) localized it further: on PHP the per-file tree-sitter
   parse (Arm B enrichment + the symbol index) is ~50 % of index time — the
   opposite of intuition, and the reason Go ken was ~1.8× slower *per file* than
   the Python baseline.

3. **The bench ran a parser we have since replaced.** The 256.6 s bench was on ken
   **v1.1.x, which pinned gotreesitter 0.20.5**. Per the gotreesitter 0.21.0
   changelog, the 0.20.x line **silently disabled the hand-written repeat-boundary
   conflict resolvers** for several elected languages (PHP among them), causing
   **GLR stack-forking blowups** on real-world code, and used the pre-engine
   per-language error-recovery path. We **bumped to 0.47.0 on 2026-07-24 (PR #62)**,
   which ships the C-faithful engine, the conflict-resolver fix, ~18 % higher
   recovery throughput, and non-quadratic cap-eviction. **The bench predates the
   bump.** M0(a) below measured the before/after and found the opposite of the
   hypothesis: **the bump REGRESSED the PHP parse ~2.7×** (0.20.5 was fast because
   it skipped the C-faithful work; 0.47.0 is correct but slower). gotreesitter's own
   `perf_ratio_budgets.json` accepts ~1.9× C tree-sitter for PHP as the current
   state, and separately logs a single-file cliff (`bootstrap-5.blade.php` at 159×
   aggregate vs a 1.55× median) — template-like files can be individually
   pathological. **Conclusion adopted: keep the bump (correctness) and take the
   parse OFF the cold path** (M2 lazy enrichment + M2-Task-2 per-file budget) rather
   than try to make it faster. Consequence for projections: HEAD cold-indexes typical
   PHP ~2.7× slower than the version the 256.6 s bench measured, so any pre-lazy-enrich
   cold-start projection is **optimistic** — **M1 (skip the build) and M2 (parse off
   the cold path) should both land before inviting an external rebench**, or the
   rebench measures the regression, not the fix.

Two distinct user experiences to fix, in priority order:

- **Everyday cold** (IDE restart, reboot; repo mostly unchanged) — should be seconds.
- **True cold** (first index ever, or invalidated snapshot) — should be minutes → tens
  of seconds, and should serve *something* early.

## Results (consolidated, final binary)

Time to a servable first query, measured in one pass on the shipped code
(HEAD `2a1b82d`), yii2 PHP corpus (~12 k chunks), hybrid mode, M1 Pro / 16 GB,
median of 3. **All speedups are M1 Pro; absolute times won't hold on a 4-core
i5 — the on-by-default calibration owes that measurement.**

| scenario | first-servable | vs cold |
|---|---:|---:|
| cold full build (baseline = fully-warm) | 2.39 s | 1× |
| **M1** everyday-cold — restart, repo unchanged *(default on)* | 573 ms | **4.2×** |
| **M1** reconcile — restart after a 1-file edit *(default on)* | 746 ms | **3.2×** |
| **M2** lazy enrich — true cold, first query *(opt-in)* | 1.24 s | 1.9× |
| **M4** staged — true cold, first query *(opt-in)* | 564 ms | **4.2×** |

Dual-number honesty (M3/M4): first-servable above; the **fully-warm** hybrid
index (the 2.39 s baseline) lands in the background shortly after for M2/M4.
M3 (embed cache) is a rebuild lever, not a first-query one (warm rebuild 1.5×).

## Milestones

### M0 — Profile the build path (prerequisite; do first) — ✅ DONE 2026-07-25

Full writeup + reproduce: [`cold-start-M0-findings.md`](cold-start-M0-findings.md).
Profiled `ken perf index` on a yii2 PHP proxy (2,783 files / 12,349 chunks, M1 Pro).

Questions the profile answered:

- [x] **(a) Index wall at v1.1.1 (gotreesitter 0.20.5) vs HEAD (0.47.0), same corpus,
      isolated — how much does the dep bump alone recover?** **Measured (M0(a) below):
      it did NOT recover — the bump REGRESSED the PHP parse ~2.7× (total bm25 index
      2.16×).** The enrich-off control confirms the delta is 100 % the tree-sitter
      parse. A further parser fix is now clearly worth chasing — reframed as "0.47.0
      correctness without its ~2.7× well-formed-PHP parse cost."
- [x] **(b) Does `structural.Build` re-parse files the chunker already parsed?**
      Within `perf index`: no double-parse (regex chunker doesn't parse; enrichment
      parses once; `structural.Build` isn't on that path). **But ken-mcp parses every
      file TWICE at startup** — enrichment (search index) + eager `structural.Build`
      (`main.go:411`, symbol index). The bench ate both. → lazy-structural quick win.
- [x] **(c) Share of tree-sitter vs chunking vs BM25 tokenization vs alloc/GC?**
      Enrichment tree-sitter parse ~48–54 % (dominant, > embedding), embedding ~31 %,
      floor (walk/chunk/tokenize) ~21 %, GC ~15 %. Of the parse, ~21 % of total CPU
      is gotreesitter GLR retry / full-DFA error-recovery on PHP.
- [x] **(d) Can `structural.Build` go lazy on first structural-tool call
      (ADR-036 precedent), and what does it save?** GO — measured `structural.Build`
      ~1.29 s on the yii2 proxy, removed from every cold start. **Shipped** (below).
- [ ] **(e) Enrichment cost share on 4-core x86 (the reframed open question).**
      M0(a)/M0(c) answered *why* the parse is expensive (0.47.0's C-faithful engine,
      ~2.7× on PHP) — that's settled, no more "why is the parser slow" work. What's
      still open is the *share* on the bench-host class (i5, 4 cores), which decides
      M2's background-vs-on-demand shape and whether M3 (embed cache) is worth it on
      that hardware. Profiled on 10-core M1 so far; a 4-core run should ride along
      with the M2 implementation, not block it.

**Lazy-structural quick win — ✅ SHIPPED** (commit `ce1581f`). ken-mcp's eager
`structural.Build` (`main.go:411`) is deferred: the Builder wires
`RepoBundle.StructuralBuilder` and the symbol index builds on first structural-tool
call (`sync.Once`-guarded, concurrency-safe; `status` peeks via `StructuralIfBuilt()`
without triggering it). Removes ~1.29 s from every cold start for the majority of
sessions that never call definition/references/callers/outline/symbols.

Go/no-go summary (post-M0(a), renumbered): **M1 → GO** (skips the whole build on
everyday cold); **M2 (lazy/async enrichment) → GO and PROMOTED ahead of the
embedding cache** — M0(a) reframed the parse as the dominant, now-*worse* cold
cost, so taking it off the cold path beats trying to speed it up; **M3 (embed
cache) → GO but extend to cache the enrichment label by content hash too**, not
just embeddings (the parse is the more expensive thing to memoize); **M4 (staged
readiness) → GO** (BM25 floor ~476 ms → sub-second first-servable plausible).
New candidate, now confirmed by M0(a): **take the PHP parse cost upstream to
gotreesitter as a ratchet data point** (Task 4 draft, `gotreesitter-php-datapoint.md`)
— reframed from "fix the retry" to "0.47.0 correctness without its ~2.7× cost".

Estimate: 1–2 days. **Actual: done.**

#### M0(a) — dep-bump before/after (Task 3, measured 2026-07-25)

Measured 2026-07-25 on M1 Pro / 16 GB. Corpus: yii2 + yii2-app-advanced
(1,184 `.php`, 12,349 chunks). Method: temp binaries built at each ref
(`GOWORK=off`, each ref's own go.mod pins), `ken perf index --mode bm25
--chunker regex`, median of 5. bm25 mode isolates the parser (no embedding);
`KEN_ENRICH=off` is the control that removes the tree-sitter parse entirely.

| config | v1.1.1 (gotreesitter 0.20.5) | HEAD (gotreesitter 0.47.0) | ratio |
|---|---|---|---|
| enrich **on** (tree-sitter parse exercised) | 1598 ms | 3455 ms | **2.16× slower** |
| enrich **off** (control — no parse) | 537 ms | 557 ms | ~equal (3.7 %, noise) |

**Result — the bump REGRESSED parse cost; it did not recover it.** The
enrichment-off control is identical across versions (537 ≈ 557 ms), so the
*entire* on/off delta is the gotreesitter parse: v1.1.1 spends ~1061 ms in it,
HEAD ~2898 ms — the 0.47.0 C-faithful engine is **~2.7× slower** on this PHP
corpus than 0.20.5's (conflict-resolver-disabled) engine, dragging total bm25
index 2.16× slower. (Absolute ms are load-sensitive on a busy laptop; the
**ratio** and the flat enrich-off control are the robust findings.)

This **overturns fact (3)'s hypothesis.** The 256.6 s bench ran the *faster*
parser — 0.20.5's GLR blowup is input-specific (malformed/pathological files)
and yii2 doesn't trigger it enough to lose the well-formed-code speed, so the
parser was not the bench's bottleneck in the way we suspected. And the bump we
shipped for *correctness* (PR #62) carries a real, previously-unmeasured
**cold-start parse regression** on typical PHP. Consequences:

- Re-weights the campaign toward the caching levers (M1 snapshot, M2 embed/
  label cache) and the shipped lazy-structural quick win — they now dodge a
  parse that is *slower*, not faster, than before.
- Promotes "**upstream the PHP parse cost to gotreesitter**" from a maybe to a
  real target, reframed: not "fix the 0.20.5 blowup" (done, in 0.47.0) but "get
  0.47.0's correctness without its ~2.7× well-formed-PHP parse cost."
- Flags a latent regression the external rebench would otherwise surface: HEAD
  cold-indexes typical PHP slower than the version the bench measured. M1 must
  land before that rebench.

### M1 — Snapshot persistence + reconcile-on-boot (the big lever) — Increment 1 WIRED

**Increment 1 shipped.** ken-mcp now persists the built index to
`<repo>/.ken/snapshot.{bin,manifest}` and, on restart, loads it + drift-scans
(config-key + per-file mtime/size) instead of rebuilding when the repo is
unchanged — the everyday-cold fast path. `loadOrBuildWatched` →
`buildOrLoadSnapshot`: try snapshot → live-build on miss/drift → persist;
re-persist on each watch flush. `KEN_MCP_SNAPSHOT=off` disables; local-path
repos only; distinct from the ADR-024 operator prebuilt (`.ken/index.bin`,
frozen), which still wins when present. Any load failure/drift/corruption
degrades silently to a live build (snapshot is untrusted input). Tested:
clean-load fast path, drift (edit/new-file), config-mismatch, missing/corrupt
manifest all fall back correctly; no testdata pollution.

**Increment 2 WIRED — reconcile on drift.** On drift, ken-mcp now re-indexes
*only* the changed files instead of a full rebuild: `SnapshotManifest.Diff`
computes added/modified/deleted, and `WatchedIndex.ReconcileFiles` replays them
through the fsnotify `flush` path (tombstone deleted+modified, re-chunk/enrich/
embed changed+added), keeping every unchanged file's chunks + vectors from the
snapshot; then re-persists so the next boot is a clean load. A **threshold**
falls back to full rebuild when the change set exceeds 50 % of the corpus
(tombstone is O(K·N) over chunks, so heavy drift is cheaper to rebuild).
Tested: unchanged files kept, edited re-indexed, deleted dropped, added
included; heavy-drift → threshold → full rebuild.

**Perf.** The **corpus-only loader** follow-up shipped (`LoadSerializedCorpus`
skips the throwaway `BuildIndex` the KEN1 loader would build just for its
chunks/vecs). Measured on yii2 (12 k chunks), median of 3:

- **Everyday-cold clean-load** (repo unchanged — the common path): **bm25 3.2×**
  (1.72 s → 532 ms), **hybrid 4.2×** (2.40 s → 575 ms). Was 2.3× before the
  corpus-only loader.
- **Edit-1-file reconcile** vs full rebuild: **bm25 3.1×** (2.12 s → 686 ms),
  **hybrid 3.3×** (2.50 s → 752 ms). Was 1.3×/1.7× before the **single-publish
  reconcile** shipped (`NewWatchedIndexReconciled` mutates the corpus BEFORE the
  initial build, so drift costs one BM25/ANN build, not seed-then-reconcile's
  two). The embedding skip (re-embed 1 file, not 12 k) means the win **grows
  with corpus size** — kernel-scale hybrid would skip ~826 k embeddings.

All M1 follow-ups shipped: both perf optimizations (corpus-only loader,
single-publish reconcile), the loader **fuzz** gate (`FuzzDecodeManifest` +
`FuzzLoadSerializedCorpus`, 550k–750k execs clean), the everyday-cold
`docs/PERF-expectations.md` row, and **`ken index --write-snapshot`** (CI
prewarming — build once + persist, shares `search.WriteSnapshot` with ken-mcp).
**M1 is complete.** Next: M2 (lazy/async enrichment).

Wire the existing serialize format into the `ken-mcp` lifecycle. Design recorded in
**ADR-039**. Infra already on this branch (`8f6e7da`, `ab7f3f5`): the drift/config
sidecar (`SnapshotManifest` + `SnapshotConfigKey` + `ModelFingerprint`), a
snapshot-seeded *watching* index constructor (`NewWatchedIndexFromSnapshot`), and
`WatchedIndex.SnapshotBytes()` / `EmbedModel()`. Remaining is the ken-mcp wiring.

Design:

- **Write:** after initial build and after watch flushes (debounced), persist the
  index to `<repo>/.ken/index.bin` + `<repo>/.ken/snapshot.manifest` (XDG cache
  fallback keyed by repo path hash when the repo isn't writable). Atomic rename;
  partial writes must never be loadable.
- **Cache key / invalidation** (shared constant, ADR-039): format version ⊕ model
  fingerprint ⊕ chunker ⊕ mode ⊕ enrichment on/off. Size caps
  (`KEN_MAX_FILE_BYTES` / `KEN_MAX_AVG_LINE_BYTES`) and ignore rules
  (`.gitignore`/`.kenignore`) are **not** keyed — they change the file *set*, which
  the drift scan already catches (keying them adds false-invalidation risk).
- **Boot:** load snapshot → drift scan (mtime+size per file; optionally a
  `git status --porcelain` fast path when clean) → re-chunk/re-embed only changed
  files → patch index in place, reusing the fsnotify watch's per-file
  `tombstoneFile`/`appendFile` primitives extracted into a batch API. **Increment 1
  = load-if-clean-else-full-rebuild; Increment 2 = incremental reconcile.**
- **Serve** as soon as reconcile completes. Everyday cold ≈ load + drift scan.

Tasks:

- [x] ADR (ADR-039): snapshot lifecycle, cache key, corruption/downgrade behavior,
      `.ken/` is a cache (safe to delete, `.gitignore`d).
- [x] Snapshot infra in `internal/search` (manifest, config-key, seeded constructor).
- [x] ken-mcp wiring (Increment 1): load-or-build in `loadOrBuildWatched` →
      `buildOrLoadSnapshot`, write on build + flush, distinct `.ken/snapshot.{bin,
      manifest}` artifact so the ADR-024 operator prebuilt (`.ken/index.bin`,
      frozen) is untouched. Integration-tested (clean-load/drift/mismatch/corrupt).
- [x] `KEN_MCP_SNAPSHOT=off` escape hatch (default on, local-path repos only).
- [ ] Loader hardening: boot treats *any* load failure as cache-miss + rebuild
      already (verified in tests) — still TODO: a **fuzz gate** on the manifest +
      KEN1 loaders (snapshot is untrusted input).
- [x] Reconcile path (Increment 2): on drift, re-index only changed files via
      `WatchedIndex.ReconcileFiles` (batch replay through `flush`), with a
      >50%-drift → full-rebuild threshold. (Perf follow-ups noted below.)
- [ ] `ken index --write-snapshot` for CI prewarming.
- [ ] Bench: everyday-cold (snapshot present, ≤ 1 % files changed) added to
      `PERF-expectations.md` as its own row — it's the number users feel.

Acceptance: everyday cold on the large (~750 file) corpus ≤ 2 s to first servable
query; kernel-scale snapshot load measured and published; true cold unchanged.

Estimate: 4–6 days including ADR + fuzz.

### M2 — Lazy / async enrichment (promoted ahead of the embedding cache) — SHIPPED (opt-in)

**Shipped v1, opt-in (`KEN_MCP_LAZY_ENRICH=1`, default off).** The cold build
serves a RAW BM25/dense index for a fast first query; a background pass
(`WatchedIndex.enrichCorpusInBackground`) then re-labels every file, re-embeds,
and republishes the fully-enriched index atomically (fresh slices — never
mutates the live array; `Close()` waits on / cancels it). `FSOptions.LazyEnrichment`
gates the initial inline enrich; `enrichLabelFor` is the single-source label so
inline and background can't drift; the background parse honors the M2-Task-2
`KEN_ENRICH_FILE_BUDGET_MS`. Interaction with M1: a lazy cold build defers the
raw-snapshot persist and lets the background pass's OnFlush persist the
*enriched* snapshot, so the next boot is a clean enriched load (crash
mid-enrich → no snapshot → next boot lazy-rebuilds, also fast).

Measured first-servable (yii2 ~12 k chunks, M1 Pro, median of 3): **bm25 3.2×**
(1.66 s → 522 ms — ≈ the enrich-off floor, acceptance met), **hybrid 1.9×**
(2.34 s → 1.21 s). Full enrichment completes in the background off the query
path. Default stays **off** pending a 4-core-x86 profile of background-sweep vs
on-demand and the honest dual-number (first-servable vs fully-warm) rebench.

Tests (-race): lazy build eventually matches the inline-enriched corpus byte
for byte (watch=false runs the pass synchronously); watch=true serves raw
first, then swaps in the enriched index; the fresh-slice publish is race-clean.

**Motivation (M0(a)):** the 0.47.0 parser is ~2.7× slower on PHP than the
version the external bench measured, and the enrichment tree-sitter parse is
~48–54 % of cold index time (M0(c)) — the single biggest cold-path cost, now
*worse* than before the bump. The adopted conclusion is to take the parse
**off the cold path** rather than try to make it faster. This applies the
lazy-structural / lazy-rerank precedent (ADR-036) to enrichment itself.

**Enrichment is additive, not load-bearing (verified).** `enrichChunks`
(`internal/search/index.go`) only prepends the `# func: … | calls: … |
raises: …` label to each chunk's `Text` before BM25/embed
(`cs[i].Text = label + cs[i].Text`). Nothing downstream parses the label back
out — the structural tools use `structural.Build`, not the label. A chunk
served *without* the label is well-formed source; the label is a pure
retrieval-signal prefix (ADR-035: +0.02–0.03 NDCG). So serving pre-enrichment
is correct, just slightly lower-ranked. **The one caveat is quality, not
correctness:** a *heterogeneous* index (some chunks enriched, some raw) has
BM25 tokens/embeddings that diverge between the two populations
(`index.go:337-339`), so ranking is temporarily inconsistent during the
background pass. Not a blocker — end state is fully enriched. **No place was
found where enrichment is load-bearing for correctness.**

**Design:**

- Cold index builds + serves the BM25/dense index **without** enrichment
  (first-servable pays only the ~540 ms floor, not the ~3.5 s enriched parse).
- A background pass then enriches, re-chunking/re-embedding each file's chunks
  with the label and republishing via the existing atomic snapshot-publish
  pattern (`WatchedIndex` swap) — the same mechanism the fsnotify watcher and
  M1 reconcile already use. **Profile in this task**: whole-corpus background
  enrich vs on-demand per-file (enrich a file's chunks the first time results
  from it are served). On 4-core hosts a single background sweep may thrash the
  query path; on-demand may be gentler. Pick per data.
- Pair with the M2-Task-2 per-file parse budget so one pathological file can't
  stall the background pass.

**Interaction with M1:** snapshots store the *enriched* chunk `Text` (the label
is in `Text`, which is serialized), so a snapshot hit is already fully enriched
— the lazy path only runs on **true cold / cache miss**. M1 + M2 compose: M1
skips the whole build when the repo is unchanged; M2 makes the *unavoidable*
cold build serve fast and enrich behind it.

**Acceptance:** on the PHP corpus, first-servable within ~10 % of the
enrich-off floor (~540 ms, not ~3.5 s); fully-enriched state reached in the
background without blocking queries; a `KEN_MCP_STAGED`-style dual-number
report (first-servable vs fully-enriched) per the M4 honesty rule.

Estimate: 2–3 days incl. the background-vs-on-demand profile.

### M3 — Content-hash embedding cache — SHIPPED (opt-in)

**Shipped v1, opt-in (`KEN_MCP_EMBED_CACHE=1`, default off).** Persistent
`sha256(chunk text) → vector` cache at `<repo>/.ken/embed.db` so a full rebuild
re-embeds only never-seen text. Deterministic (Model2Vec is pure), scoped to the
model (a meta row = model fingerprint + dim; a model/dim change truncates it).
The second line of defense behind an M1 snapshot load (which skips embedding
entirely) — it helps the rebuilds M1 can't: heavy drift, a mode change, or a
deleted snapshot with an intact cache.

Architecture: the SQLite impl lives in **`internal/embedcache`** (imported only
by `cmd/ken-mcp`); `internal/search` sees only a `VecCache` interface, so the
`mcp` package stays DB-driver-free — the v0.6.0 binary-size contract (ADR-020),
which its `binary_contract_test.go` enforces (and caught this during dev).

**Writes are batched** (buffer → one transaction per 512) — a per-chunk INSERT
made a cold build ~13× slower (31 s vs 2.4 s); batching brings it back on par.
Measured on yii2 (~12 k chunks, M1 Pro, hybrid): no-cache 2.63 s, **cache-cold
2.35 s** (~on par — warms the cache), **cache-warm 1.76 s = 1.5×** (all hits,
embedding skipped). `KEN_MCP_EMBED_CACHE_MAX` bounds it (default 1M entries,
batch-granularity oldest-first eviction).

Honest scope: the win is narrow given M1 (rebuilds are rare) and that embedding
is the smaller cold cost (~31 % vs ~50 % parse). Default OFF pending 4-core-x86
data — enable where full rebuilds of the same content recur. Tests: vec
serialization, put/get, model/dim scope invalidation, size-bound eviction
(-race).

### M4 — Serve-before-warm (staged readiness) — SHIPPED (opt-in)

**Shipped v1, opt-in (`KEN_MCP_STAGED=1`, default off).** On a cold hybrid
build, the initial index is published as **BM25 (lexical-only)** for an instant
first query; a background pass then enriches + embeds and republishes as hybrid.
Staged defers **both** the enrich parse and the embed (it subsumes M2 for the
staged case), so first-servable is the pure BM25 floor. Measured on yii2 (~12 k
chunks, M1 Pro, median of 3): inline hybrid 2.37 s → **staged first-servable
564 ms = 4.2×**; the full hybrid+enriched index lands in the background.

The M2 and M4 background passes are now one unified `warmCorpusInBackground`
(re-label when deferred, re-embed under the target mode, upgrade bm25→hybrid,
atomic fresh-slice republish). `WatchedIndex.Warming()` reports the interim
state; **tool responses carry `"semantic":"warming"`** (JSON) + a header note
(markdown) until the upgrade lands — the honesty contract, so a bench/agent
knows it's getting lexical-only temporarily. `KEN_MCP_STAGED` takes precedence
over `KEN_MCP_LAZY_ENRICH` (both defer background work; kept exclusive). A lazy
cold build defers the snapshot persist to the warm pass's OnFlush (persists the
full hybrid snapshot; crash mid-warm → next boot rebuilds, also fast).

Dual-number honesty: report BOTH first-servable (564 ms) and fully-warm (~2.4 s)
in any cold-start comparison. Default OFF pending the 4-core-x86 profile + rebench.
Tests (-race): staged serves bm25 then upgrades to hybrid (watch), synchronous
upgrade (no-watch), and the `semantic:warming` wire field.

*(Original design below.)*


Don't gate the first servable query on the semantic arm.

- Phase 1: walk + chunk + BM25 → **serve lexical-only**, with an explicit
  `"semantic": "warming"` field in tool responses so agents (and benchers) know.
- Phase 2: embed in background, swap the hybrid index in atomically (existing
  snapshot-publish pattern).

**HARD RULE:** this changes what "cold Q1" measures. Docs and any bench communication
must report **both** first-servable and fully-warm times — a bench-savvy reviewer
will notice, and should be told, not surprised. `KEN_MCP_STAGED=off` for strict
apples-to-apples runs.

- [ ] ADR: readiness states, response contract, interaction with M1 (staged mode
      only matters on true cold; snapshot hit skips it).
- [ ] Verify hybrid → lexical-only ranking degradation is acceptable on the three
      fixed bench queries.

Estimate: 3–4 days. Sequenced after M1 because M1 makes true cold rare.

### M5 — mmap-able snapshot layout (stretch; decide after M1)

Serialize vectors as one contiguous f32 blob (+ offsets), so snapshot load is
mmap + fixup instead of parse-and-allocate.

- Near-zero load time at kernel scale; pages are evictable → also an RSS lever
  (memory-campaign synergy: vectors stop counting against GC heap entirely).
- Cost: format v2, alignment/endianness discipline, unsafe-slice care (precedent:
  aikit's safetensors mmap).

Decide after M1 ships: if snapshot load at 826k chunks is already < 2 s, skip.

## Sequencing

M0 → lazy-structural quick win (done) → **M0(a) dep-bump before/after (done)** →
**M1 + M2 both land** → release + external rebench window → M4 → M3/M5 per data.

**M1 and M2 must both land before inviting an external rebench.** M0(a) showed
HEAD cold-indexes typical PHP ~2.7× slower (parse) than the version the 256.6 s
bench measured, so any pre-lazy-enrich cold-start projection is now optimistic:
a rebench today would measure the *regression*, not the fix. M1 (skip the
rebuild) + M2 (take enrichment off the cold path) are the two levers that turn
that around; ship both, then rebench.

## Anti-goals (this campaign)

- **No HNSW / ANN index work** — warm search is fine; the O(N) scan is a separate,
  measured, not-yet-hurting concern (`docs/PERF-expectations.md`).
- **No daemonization / background indexer service** — the MCP process lifecycle
  stays the IDE's; persistence must work within it.
- **No cold-start claims in README** until measured on 4-core x86 class hardware,
  per the marketing-claims lesson (memory campaign M5).

## Risks

- **Stale-index correctness** (M1): mtime granularity, clock skew, case-insensitive
  filesystems. Mitigation: size+mtime both, conservative rebuild on doubt, and the
  watch keeps reconciling after boot anyway — boot drift only needs to be *close*.
- **Snapshot as attack surface** (M1): loader parses untrusted bytes. Fuzz gate
  before release (existing fuzz infra precedent).
- **Bench-perception risk** (M3): staged readiness read as gaming. Mitigation is the
  dual-number reporting rule above.
- **Two caches, one truth** (M1+M2): invalidation keys must be shared constants, not
  parallel implementations.
- **Golden drift from gotreesitter error-tree shape changes** (Task 2): 0.21.0
  changed error-tree shapes for elected languages to match C tree-sitter (PHP static
  named functions are now explicitly named). A grammar bump can silently shift
  chunker/structural goldens; every bump must audit malformed-input fixtures against
  the changelog before regenerating, and CI needs a malformed-PHP fixture so the
  shift surfaces.
