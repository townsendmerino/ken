# Cold-start campaign — task doc

Status: in progress (M0 done; M1 infra landed, wiring pending)
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
   bump**, so the parser itself is a prime suspect for the per-file slowness — and
   M0(a) below measures exactly how much the dep bump alone recovers. (Note: M0
   profiling at HEAD/0.47.0 *still* shows ~21 % of CPU in gotreesitter GLR
   retry/full-DFA recovery on PHP, so the bump likely helped but did not
   eliminate the retry cost — quantifying the before/after is the open question.)

Two distinct user experiences to fix, in priority order:

- **Everyday cold** (IDE restart, reboot; repo mostly unchanged) — should be seconds.
- **True cold** (first index ever, or invalidated snapshot) — should be minutes → tens
  of seconds, and should serve *something* early.

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

**Lazy-structural quick win — ✅ SHIPPED** (commit `ce1581f`). ken-mcp's eager
`structural.Build` (`main.go:411`) is deferred: the Builder wires
`RepoBundle.StructuralBuilder` and the symbol index builds on first structural-tool
call (`sync.Once`-guarded, concurrency-safe; `status` peeks via `StructuralIfBuilt()`
without triggering it). Removes ~1.29 s from every cold start for the majority of
sessions that never call definition/references/callers/outline/symbols.

Go/no-go summary: **M1 → GO** (skips both parses on everyday cold); **M2 → GO but
extend to cache the enrichment label line by content hash**, not just embeddings
(the parse is the more expensive thing to memoize); **M3 → GO** (BM25 floor is
476 ms → sub-second first-servable plausible). New candidate: **upstream any
residual PHP GLR-retry cost** to gotreesitter (cf. #110), pending the M0(a) before/
after.

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

### M1 — Snapshot persistence + reconcile-on-boot (the big lever) — infra landed, wiring pending

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
- [ ] Loader hardening: `LoadSerializedIndex` already has typed corrupt errors and
      the manifest reader returns `ErrManifestCorrupt` — boot must treat *any* load
      failure as cache-miss + rebuild, log once, never crash. **Fuzz the loader**
      (snapshot is untrusted input).
- [ ] ken-mcp wiring: load-or-build in `loadOrBuildWatched`, write on build + flush,
      distinct `.ken/snapshot.bin` artifact so the ADR-024 operator prebuilt
      (`.ken/index.bin`, frozen) is untouched.
- [ ] Reconcile path (Increment 2): extract watch's single-file update into a batch API.
- [ ] `KEN_MCP_SNAPSHOT=off` escape hatch; `ken index --write-snapshot` for CI
      prewarming.
- [ ] Bench: everyday-cold (snapshot present, ≤ 1 % files changed) added to
      `PERF-expectations.md` as its own row — it's the number users feel.

Acceptance: everyday cold on the large (~750 file) corpus ≤ 2 s to first servable
query; kernel-scale snapshot load measured and published; true cold unchanged.

Estimate: 4–6 days including ADR + fuzz.

### M2 — Content-hash embedding cache

Model2Vec embeddings are deterministic; SQLite is already a dependency. Cache
`chunk content hash → vector` (keyed also by model hash + dim) in `.ken/embed.db`.
Then even a *full* rebuild — including one forced by a snapshot invalidation that
didn't touch the model — only embeds never-seen chunks.

- [ ] Schema + eviction policy (LRU by last-used, size-capped; `.ken/embed.db`).
- [ ] Measure: on the bench-host class this matters more than on M-series
      (embedding is ~6 % on M1 kernel but ~31 % on M1 PHP; likely a larger share on
      4 old cores). **Gate priority on M0(a)/M0(c)**: only build if the embedding
      share on 4-core x86 is material — don't build it on M-series numbers alone.
- [ ] Interaction with M1 documented: snapshot hit skips this entirely; this is the
      second line of defense. Invalidation key shared with M1 as a constant.

Estimate: 2–3 days.

### M3 — Serve-before-warm (staged readiness)

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

### M4 — mmap-able snapshot layout (stretch; decide after M1)

Serialize vectors as one contiguous f32 blob (+ offsets), so snapshot load is
mmap + fixup instead of parse-and-allocate.

- Near-zero load time at kernel scale; pages are evictable → also an RSS lever
  (memory-campaign synergy: vectors stop counting against GC heap entirely).
- Cost: format v2, alignment/endianness discipline, unsafe-slice care (precedent:
  aikit's safetensors mmap).

Decide after M1 ships: if snapshot load at 826k chunks is already < 2 s, skip.

## Sequencing

M0 → lazy-structural quick win (done) → **M0(a) dep-bump before/after** → M1 →
release + external rebench window → M3 → M2/M4 per data.

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
