# Cold-start campaign — task doc

Status: draft for review
Scope: `ken-mcp` cold start → first servable query
Related: ADR-036 (startup campaign: lazy rerank, parallel structural), ADR-038 (`.kenignore`), memory campaign (v1.2.0), `docs/PERF-expectations.md`, `internal/search/index_serialize.go`

## Problem

Cold start is the worst number in the external bench (Trofimov, 2026-07-18): 256.6 s
to first servable query on a ~7.9k-file PHP/Yii monorepo (i5-6300U, 4 cores). v1.2.x
ignore parity + minified-skip shrinks the corpus, but two structural facts remain:

1. **Every `ken-mcp` launch rebuilds the index from scratch.** The serialize/load
   format exists (`BuildAndSerializeIndex` / `LoadSerializedIndex`) but the server
   boots through `NewWatchedIndexWithContext` → `FromFS` every time. Cold start is
   currently a policy, not physics. Every restart of the IDE re-pays the full build.

2. **The build cost is not where intuition says it is.** Kernel measurement
   (2026-07-24, M1 Pro, 826k chunks): bm25-only index 471.9 s vs hybrid 501.9 s —
   **embedding is ~6% of index time**. The cost lives in walk / chunk / tokenize /
   `structural.Build`. This also explains the bench anomaly: Go ken was ~3.6× slower
   cold than Python semble on ~2× the files — ~1.8× slower *per file*. Something in
   the non-embedding path is pathological on PHP corpora.

Two distinct user experiences to fix, in priority order:

- **Everyday cold** (IDE restart, reboot; repo mostly unchanged) — should be seconds.
- **True cold** (first index ever, or invalidated snapshot) — should be minutes → tens
  of seconds, and should serve *something* early.

## Milestones

### M0 — Profile the build path (prerequisite; do first) — ✅ DONE 2026-07-25

Full writeup + reproduce: [`cold-start-M0-findings.md`](cold-start-M0-findings.md).
Profiled `ken perf index` on a yii2 PHP proxy (2,783 files / 12,349 chunks, M1 Pro).

Questions the profile answered:

- [x] Does `structural.Build` tree-sitter-parse files the chunker already parsed?
      **Within `perf index`: no double-parse** (regex chunker doesn't parse;
      enrichment parses once; `structural.Build` isn't on that path). **But ken-mcp
      parses every file TWICE at startup** — enrichment (search index) + eager
      `structural.Build` (`main.go:411`, symbol index). The bench eats both.
- [x] What fraction is tree-sitter vs chunking vs BM25 vs GC? **Enrichment
      tree-sitter parse ~48–54% (the dominant cost, > embedding), embedding ~31%,
      floor ~21%, GC ~15%.** Of the parse, **~21% of total CPU is gotreesitter
      GLR retry / full-DFA error-recovery** on PHP — the per-file pathology.
- [ ] Walk IO- vs CPU-bound at 4 cores — not isolated (profiled on 10-core M1;
      floor is only ~21% so walk is not the lever). Defer to a bench-host run if M1
      lands.
- [x] Lazy-structural quick win: **GO, and bigger than assumed** — deferring
      ken-mcp's eager `structural.Build` removes a whole redundant parse pass. Its
      "build wall is in the noise" comment is empirically false on PHP.

**Lazy-structural quick win — ✅ SHIPPED.** ken-mcp's eager `structural.Build`
(`main.go:411`) is now deferred: the Builder wires `RepoBundle.StructuralBuilder`
and the symbol index builds on first structural-tool call (`sync.Once`-guarded,
concurrency-safe; `status` peeks via `StructuralIfBuilt()` without triggering it).
Measured deferred cost on the yii2 proxy: **`structural.Build` ~1.29 s** removed
from every cold start (the full hybrid build was ~2.3 s) — unpaid entirely by
sessions that never call definition/references/callers/outline/symbols.

Go/no-go summary: **lazy-structural quick win → GO** (own PR, ahead of M1 — done);
**M1 → GO** (skips both parses on everyday cold); **M2 → GO but extend to cache the
enrichment label line by content hash**, not just embeddings (the parse is the more
expensive thing to memoize); **M3 → GO** (BM25 floor is 476 ms → sub-second
first-servable plausible). New candidate: **upstream the PHP GLR-retry cost** to
gotreesitter (cf. #110). See findings doc for the numbers behind each.

Estimate: 1–2 days. **Actual: done.**

### M1 — Snapshot persistence + reconcile-on-boot (the big lever)

Wire the existing serialize format into the `ken-mcp` lifecycle.

Design sketch:

- **Write:** after initial build and after watch flushes (debounced), persist the
  index to `<repo>/.ken/index.bin` (fallback: XDG cache keyed by repo path hash when
  the repo isn't writable). Atomic rename; partial writes must never be loadable.
- **Cache key / invalidation:** format version ⊕ model hash ⊕ chunker name/version ⊕
  mode ⊕ ignore-rules hash (.gitignore + .kenignore/.sembleignore content) ⊕
  relevant env knobs (`KEN_MAX_FILE_BYTES`, `KEN_MAX_AVG_LINE_BYTES`). Any mismatch
  → full rebuild, never a partial trust.
- **Boot:** load snapshot → walk for drift (mtime+size per file; optionally
  `git status --porcelain` fast path when clean) → re-chunk/re-embed only changed
  files → patch index in place. This is the same per-file update path the fsnotify
  watch already exercises; reuse it, don't fork it.
- **Serve** as soon as reconcile completes. Everyday cold ≈ load + drift scan.

Tasks:

- [ ] ADR: snapshot lifecycle, cache key, corruption/downgrade behavior, and the
      explicit decision that `.ken/` is a cache (safe to delete, in `.gitignore`).
- [ ] Loader hardening: `LoadSerializedIndex` already has typed corrupt errors —
      boot must treat *any* load failure as cache-miss + rebuild, log once, never
      crash. (Snapshot is untrusted input; fuzz the loader.)
- [ ] Reconcile path: extract watch's single-file update into a batch API.
- [ ] `KEN_MCP_SNAPSHOT=off` escape hatch; `ken index --write-snapshot` for CI
      prewarming.
- [ ] Bench: everyday-cold (snapshot present, ≤1% files changed) added to
      `PERF-expectations.md` as its own row — it's the number users feel.

Acceptance: everyday cold on the large (~750 file) corpus ≤ 2 s to first servable
query; kernel-scale snapshot load measured and published; true cold unchanged.

Estimate: 4–6 days including ADR + fuzz.

### M2 — Content-hash embedding cache

Model2Vec embeddings are deterministic; SQLite is already a dependency. Cache
`chunk content hash → vector` (keyed also by model hash + dim). Then even a
*full* rebuild — including one forced by a snapshot invalidation that didn't touch
the model — only embeds never-seen chunks.

- [ ] Schema + eviction policy (LRU by last-used, size-capped; `.ken/embed.db`).
- [ ] Measure: on the bench-host class this matters more than on M-series
      (embedding is 6% on M1; likely a larger share on 4 old cores — M0 confirms).
- [ ] Interaction with M1 documented: snapshot hit skips this entirely; this is the
      second line of defense.

Estimate: 2–3 days. Priority may drop if M0 shows embedding is <15% on the i5 class
too — don't build it on M-series numbers alone.

### M3 — Serve-before-warm (staged readiness)

Don't gate the first servable query on the semantic arm.

- Phase 1: walk + chunk + BM25 → **serve lexical-only**, with an explicit
  `"semantic": "warming"` field in tool responses so agents (and benchers) know.
- Phase 2: embed in background, swap the hybrid index in atomically (existing
  snapshot-publish pattern).

Honesty requirement: this changes what "cold Q1" measures. Docs and any bench
communication must report **both** first-servable and fully-warm times — a
bench-savvy reviewer will notice, and should be told, not surprised. Consider
`KEN_MCP_STAGED=off` for strict apples-to-apples runs.

- [ ] ADR: readiness states, response contract, interaction with M1 (staged mode
      only matters on true cold; snapshot hit skips it).
- [ ] Verify hybrid → lexical-only ranking degradation is acceptable on the three
      fixed bench queries.

Estimate: 3–4 days. Sequenced after M1 because M1 makes true cold rare.

### M4 — mmap-able snapshot layout (stretch)

Serialize vectors as one contiguous f32 blob (+ offsets), so snapshot load is
mmap + fixup instead of parse-and-allocate.

- Near-zero load time at kernel scale; pages are evictable → also an RSS lever
  (memory campaign synergy: vectors stop counting against GC heap entirely).
- Cost: format v2, alignment/endianness discipline, unsafe-slice care (precedent:
  aikit's safetensors mmap).

Decide after M1 ships: if snapshot load at 826k chunks is already <2 s, skip.

## Sequencing

M0 → (lazy-structural quick win if M0 confirms) → M1 → release + external rebench
window → M3 → M2/M4 as M0/M1 data dictates.

## Anti-goals (this campaign)

- No HNSW / ANN index work — warm search is fine; the O(N) scan is a separate,
  measured, not-yet-hurting concern (`PERF-expectations.md`).
- No daemonization / background indexer service — the MCP process lifecycle stays
  Cursor's; persistence must work within it.
- No cold-start claims in README until measured on x86 4-core class hardware, per
  the marketing-claims lesson (memory campaign M5).

## Risks

- **Stale-index correctness** (M1): mtime granularity, clock skew, case-insensitive
  filesystems. Mitigation: size+mtime both, conservative rebuild on doubt, and the
  watch keeps reconciling after boot anyway — boot drift only needs to be *close*.
- **Snapshot as attack surface** (M1): loader parses untrusted bytes. Fuzz gate
  before release (existing fuzz infra precedent).
- **Bench-perception risk** (M3): staged readiness read as gaming. Mitigation is
  the dual-number reporting requirement above.
- **Two caches, one truth** (M1+M2): invalidation keys must be shared constants,
  not parallel implementations.
