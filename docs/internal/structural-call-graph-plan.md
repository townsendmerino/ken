# Plan: from name-resolved symbols to a resolved call/dependency graph

## Status

- **Phase 0 — node-level data model:** **SHIPPED** (2026-06-03, this commit).
  Span fields on `FuncDef` / `ClassDef`, per-call-site `CallRef` records
  with `Callee` / `Receiver` / `Line` / `EnclosingSymbol`, `CalleeNames()`
  accessor preserving Arm B byte-identity, all 10 shipping languages
  (plus the parked C# / Swift extractors behind their build tags).
  Memory measured well within the ≤2× budget on three corpora — jekyll
  (Ruby, 167 files, 9350 CallRefs, +29 MiB HeapAlloc), express
  (JavaScript, 141 files, 10855 CallRefs, +25 MiB), ripgrep (Rust, 101
  files, 4484 CallRefs, +309 MiB — gotreesitter parser arenas dominate
  here, not our data model). The CallRef substrate itself adds ~500 KiB
  on the structural index of each corpus.
- **Phases 1, 2, 4 — bundled behind a trigger:** **Deferred until MCP
  log evidence shows demand** (see Sequencing & triggers below).
- **Phase 3 — type / receiver resolution:** **Hard-deferred** until
  Phase 2 visibility hits a measured precision ceiling on OO-heavy
  corpora.

Each accepted phase becomes ADR-037+ in [`DECISIONS.md`](DECISIONS.md)
(latest landed ADR is ADR-036). This doc is the umbrella plan; Phase 0
ships under it without its own ADR (mechanical superset, no
architectural alternatives to weigh); Phases 1+ will each get their own
ADR with the rejected-alternatives audit trail the repo convention
requires.

**Performance is a first-class acceptance criterion here, not an
afterthought.** Every phase below has an explicit *Performance impact*
subsection covering the four cost axes that matter for ken: index-time
(cold-start `structural.Build`), query-time (the MCP tool round-trip),
resident memory (the cached `*structural.Index`), and the two derived
surfaces that must not regress — the pre-built serialized index (ADR-024)
and watch-mode incremental rebuild (ADR-012). A phase that wins capability
but blows the cold-start or memory budget is not done.

## Non-goals

- **Cypher / arbitrary graph query language.** Explicitly out of scope.
  The value being chased is *resolved edges + transitive traversal exposed
  through purpose-built tools*, not a general query surface. ken's structural
  tools stay a fixed, typed set.
- **Compiler-grade type checking.** We are not building a type checker, a
  borrow checker, or a full name-resolution/scope engine per language. The
  target is *tree-sitter-grade-plus*: heuristic resolution with explicit,
  surfaced confidence — the same relevance-over-completeness posture ken
  already takes for retrieval, applied to graph edges. Honest in both
  directions: an edge ken isn't sure about is labeled, not dropped and not
  faked.
- **A second storage engine.** The graph lives in process memory inside the
  existing `*structural.Index`, serialized with the existing pre-built-index
  machinery. No embedded graph DB, no new on-disk format beyond extending the
  current one.

## Where ken is today (grounded in the source)

The structural layer is `internal/structural`, built by `structural.Build`
(`index.go`) and consumed by the `definition` / `references` / `callers` /
`outline` / `symbols` MCP tools (`mcp/structural_tools.go`). Build is invoked
eagerly per repo in `cmd/ken-mcp/main.go` and cached on the `RepoBundle`
(`mcp/cache.go: Structural *structural.Index`). It is a **second pass** over
the corpus, independent of the BM25/dense search index, and it is already
parallelized per file (worker pool over `repo.WalkFS` output, `sync.Map`
parser-pool cache, `parseTimeoutMicros` graceful-degrade).

The data model is the constraint. From `index.go` and `extract_python.go`:

- **`FileStruct` is per-file.** `Functions`, `Classes`, `Imports`, `Calls`,
  `Raises`. `Calls` is a **deduped, file-scoped** list of bare callee leaf
  names — `walkPy` deliberately recurses call/raise capture to file scope and
  does *not* change the enclosing symbol. So there is no record of *which
  function* made a call, even within one file.
- **Call sites carry no location.** `CallSite` is literally `{File string}`.
  `nodeText` already reads `StartByte()/EndByte()`, so spans are available at
  zero marginal parse cost — they are simply not recorded.
- **Receivers are discarded.** `pyCalleeName` maps `obj.bar(...)` → `"bar"`,
  throwing away `obj` — exactly the signal type resolution would need.
- **Lookups are `name → []file`.** `callers map[string][]CallSite`,
  `defs map[string][]string`, `methods map[string][]string` (bare and
  `Type.method`). Same-spelled names collapse; resolution is name-only.
- **Ten languages.** `kenLangToTSLang` + `langExtractor`: Python, Go,
  TypeScript, JavaScript, Java, Rust, C, C++, PHP, Ruby. (The package-doc
  "v0 Python only" comment is stale and should be fixed as drive-by cleanup.)

So `callers("Foo")` answers "which *files* contain a call to *some* `Foo`",
not "which *functions* call *this* `Foo`". That is the gap.

## Target end-state

A node-level directed graph held in `*structural.Index`:

- **Nodes** are symbols — functions, methods, classes — each with a stable
  `SymbolID` (file + qualified name + kind) and a line span.
- **Call edges** `caller-symbol → callee-symbol`, each with a resolution
  `Confidence` and the call-site line.
- **Dependency edges** `file → file` (and where resolvable, `symbol → symbol`)
  derived from imports.
- **Traversal** — forward (callees) and reverse (callers) adjacency, with a
  bounded transitive walk powering a new `impact` / `call_graph` tool.

Exposed through additions to the existing typed tool set, never a query
language. Edges below a confidence threshold are returned but labeled.

---

## Phase 0 — node-level data model (mechanical; foundation for everything)

**What changes.** Extend the extractor output so structure is attributed to
symbols and located, without yet resolving anything:

- Add `StartLine, EndLine int` (or `StartByte, EndByte uint32`) to `FuncDef`
  and `ClassDef`. Free at parse time — `nodeText` already touches the bytes.
- Replace the file-scoped `FileStruct.Calls []string` with per-site records:
  `CallRef{ Callee string; Receiver string; Line int; EnclosingSymbol string }`.
  `Receiver` is the retained `obj` text from `obj.bar()` (empty for bare
  calls), kept for Phase 3; everything else is needed for Phase 1.
- Thread an `enclosingSymbol` argument through `walkPy` (and each sibling
  `walk*`) alongside the existing `enclosingClass`, so a call records the
  function/method it sits inside.
- Keep a derived, deduped `CalleeNames()` accessor so Arm B enrichment
  (`enrichCore`, ADR-035) and the existing BM25 label path are byte-identical
  — enrichment must not change, or retrieval NDCG moves. This is a hard
  invariant: Phase 0 is a *superset* of today's facts, not a reshape of the
  enrichment input.

**Why first.** Every later phase needs call-site → enclosing-symbol
attribution and node identity. Nothing resolves yet; this is pure capture.

**Performance impact.**

- *Index-time (Build):* negligible CPU change. The AST is already fully
  walked once; we record more fields per visited node, no new traversal. The
  receiver-text capture is one extra `nodeText` slice per selector call.
  Projection: within noise of today's Build (anchor: M0 ≈ 1577 ms for 167
  Ruby files on jekyll; per-file walk dominated by parse, not capture).
- *Memory:* this is the real cost and must be budgeted now. Today `Calls` is
  deduped to ~tens of names/file; `CallRef` records *every* call site
  (hundreds/file in dense code) with two strings + an int. Mitigations baked
  into Phase 0: (1) **intern** callee/receiver/symbol strings through a
  per-index `*stringTable` (`[]string` + `map[string]uint32`), storing
  `uint32` IDs in `CallRef`, so repeated names cost 4 bytes not a string
  header + backing array; (2) store call sites in one flat
  `[]CallRef` per file, not a slice-per-function. Budget: target ≤ 2× the
  current structural-index resident size, measured on the K8s (59,795-chunk)
  and Postgres (64,506-chunk) demo corpora before the phase is accepted.
- *Serialization (ADR-024):* the pre-built index format gains the new fields;
  bump the format version so a stale `.ken/index.bin` triggers the documented
  lazy-rebuild fallback rather than mis-parsing. Serialized size grows roughly
  with the memory delta; the string table serializes once and dedupes on disk
  too.
- *Watch-mode (ADR-012):* unaffected at this phase — still a per-file
  re-extract on change. Edge invalidation is a Phase 1 concern.

---

## Phase 1 — call-edge resolution + adjacency (medium)

**What changes.** Add a resolution pass (pass 3 in `Build`, after the pass-2
lookup maps exist) that turns each `CallRef` into zero or more edges:

- Define `SymbolID` and a node table `map[SymbolID]*Node`.
- For each `CallRef`, resolve `Callee` against `defs`/`methods`:
  - unique in-file match → one edge, `Confidence = High`;
  - unique corpus-wide match → one edge, `Confidence = High`;
  - multiple candidates (name collision) → an edge to *each*, `Confidence`
    scaled by candidate count and tie-breakers (same-file, import-visible —
    Phase 2 sharpens this);
  - no match (external/stdlib/unresolved shape) → recorded as an unresolved
    edge with the bare name, so `references` keeps today's recall.
- Build forward (`callee adjacency`) and reverse (`caller adjacency`) lists
  keyed by `SymbolID`.
- Upgrade the tools: `callers(symbol)` becomes function-level and ranked by
  confidence (today: file-level); `references` gains optional line numbers
  (the `Reference.File`-only surface in `lookups.go` already anticipates
  this). Wire format stays markdown; confidence and line are additive.

**Why.** This is the step that makes "who calls *this* function" true rather
than "which files mention the name". It is also the minimum substrate for
Phase 4 traversal.

**Performance impact.**

- *Index-time:* one extra pass, O(total call sites). Each resolution is a
  handful of `map` lookups against the already-built `defs`/`methods` (O(1)
  each) plus a small scoring constant. The pass-2 maps are **read-only** after
  pass 2, so the resolution pass parallelizes per file the same way pass 1
  does; only the adjacency writes need coordination. Use **sharded
  accumulation** (one edge buffer per worker, merged after fan-in) to avoid a
  shared-map mutex on the hot path — same pattern pass 1 uses for `results[]`.
  Projection: adds a sub-linear fraction to Build wall-clock (lookups are
  cheap relative to tree-sitter parsing in pass 1); accept only if the K8s/PG
  demo Build stays within ~10–15% of its pre-phase number.
- *Query-time:* `callers`/`references` get *faster* per result (direct
  adjacency lookup vs today's `callers[name]` slice scan + per-file import/
  raise linear scan in `References`). No regression expected.
- *Memory:* node table + two adjacency lists. Adjacency is the dominant new
  structure: ~one edge per resolved call site, each edge = two `SymbolID`
  refs (interned `uint32`s) + a byte of confidence + a line `int32`. With
  interning this is small and bounded; budget it into the same ≤2× envelope
  and re-measure.
- *Serialization:* node table + adjacency serialize as ID arrays (the string
  table already carries the names). Format-version bump again, or gate the
  whole graph behind a format flag so one bump covers Phases 0–4.
- *Watch-mode:* **new invalidation cost.** Editing file F changes F's own
  edges *and* edges from other files that resolved into F's symbols (and edges
  out of F into others). Two viable strategies, decide in the Phase 1 ADR:
  (a) *recompute-affected* — on F's change, re-resolve F's outgoing edges and
  re-resolve incoming edges by consulting a `symbol → referencing-files`
  reverse index (bounded by F's symbols' fan-in); (b) *mark-stale-resolve-lazy*
  — invalidate edges touching F and re-resolve on next query. (a) keeps query
  latency flat at the cost of edit-time work; (b) keeps edits cheap at the
  cost of first-query-after-edit latency. Recommendation: (a), matching ken's
  "queries stay fast" stance and the atomic-snapshot-swap model already in
  `watch.go`.

---

## Phase 2 — import / dependency resolution (medium-hard)

**What changes.** Resolve `FileStruct.Imports` (today: bound leaf names only)
to concrete target files/modules, producing `file → file` dependency edges and
sharpening Phase 1's collision scoring (a callee defined in an imported module
beats one that isn't visible):

- Build a `module-path → file` index once per corpus (language-specific:
  Python package/relative imports, Go import paths, TS/JS path + `node_modules`
  resolution rules, Java packages, Rust `mod`/`use`, etc.).
- Map each import to its target file where resolvable; record a dependency
  edge. Unresolved imports (third-party, stdlib) recorded as external nodes.
- Feed import visibility into Phase 1's confidence: in-scope candidates rank
  above out-of-scope ones.

**Why.** Two payoffs: a real dependency graph (the "what does this file/module
depend on" axis), and materially better call-edge precision because most
real-world name collisions are disambiguated by what's actually imported.

**Performance impact.**

- *Index-time:* building the module index is O(files); per-import resolution
  is O(imports) map lookups. Cheaper than Phase 1's call resolution (imports ≪
  call sites). Parallelizable identically; module index built in a quick serial
  prelude or concurrent-map.
- *Query-time:* enables a new dependency surface; no regression to existing
  tools.
- *Memory:* module index (`map[string]fileID`) + dependency adjacency — both
  small (imports per file are few). Negligible against the call-edge cost.
- *Serialization / watch-mode:* dependency edges serialize like call edges.
  Watch-mode invalidation is *simpler* than Phase 1's (imports change less and
  resolve locally), but module-index entries for a renamed/moved file must be
  updated — fold into the Phase 1 invalidation path.

---

## Phase 3 — type / receiver resolution (hard; approximate, opt-in)

**What changes.** Resolve `obj.bar()` to the specific method by inferring the
type of `obj`, plus class-heritage resolution for inherited methods. This is
the single biggest effort jump and the one that converts "method named `bar`
somewhere" into "this class's `bar`".

- Retain the receiver expression (Phase 0 already kept `Receiver`).
- Per-function **local type environment**: bind variables to types from the
  cheap, high-signal sources only — explicit annotations (params, return
  types, variable annotations) and obvious constructor assignments
  (`x = Foo()`). No dataflow, no generics solving, no cross-function inference
  in v1.
- **Heritage map**: class → base classes, so a method absent on the receiver's
  class resolves to the inherited definition.
- Resolution remains *ranked + confidence-scored*; anything the cheap
  environment can't bind stays a Phase-1-style name-based edge (lower
  confidence), never dropped.

**Why.** Most of the precision difference between ken's name-resolution and a
graph-native tool's resolved edges lives here, specifically for OO-heavy
codebases. But it has sharply diminishing returns past the
annotation+constructor heuristics, and it is per-language-hard.

**Performance impact.**

- *Index-time:* the expensive phase. Adds a second per-function analysis with
  a scope/type-env map and heritage lookups — still linear in AST size but a
  larger constant and more allocation (a `map` per function scope; pool and
  reset these via `sync.Pool` to avoid GC pressure, mirroring the BM25
  tokenizer scratch-pool work in ADR-027/028). Projection: this is where Build
  cost could grow noticeably (estimate 1.3–1.8× the structural pass on
  OO-heavy corpora), which is precisely why it is **opt-in**.
- *Control:* gate behind `KEN_STRUCTURAL_TYPE_RESOLUTION` (and an
  `mcp.Options` / `FSOptions` field), default **off**. Off ⇒ Phases 0–2
  behavior, fast path preserved, identical to the rest of ken's "expensive
  thing is opt-in" pattern (treesitter chunker, row sampling, LISTEN/NOTIFY).
- *Query-time:* unchanged — resolution happens at index time; queries read
  resolved edges. Higher-confidence edges, same latency.
- *Memory:* type envs are transient (freed after Build); the persistent cost
  is only higher-confidence edges replacing low-confidence ones — net neutral
  to slightly lower (fewer ambiguous fan-out edges).
- *Serialization:* no new persistent structure beyond edges. The heritage map
  may be worth persisting for tooling; small.
- *Watch-mode:* type/heritage resolution for F must rerun on F's change and on
  changes to F's base classes — extend the Phase 1 reverse index to track
  heritage dependents. Bounded; only OO inheritance chains pay.

---

## Phase 4 — transitive traversal + `impact` tool (medium)

**What changes.** Add bounded graph traversal over the Phase 1 adjacency and
expose it as one new typed tool (no query language):

- `impact(symbol, depth)` — reverse-BFS over caller adjacency: "what
  transitively depends on this symbol", depth-grouped, confidence-aggregated
  along paths (a path is only as strong as its weakest edge).
- Optionally `call_graph(symbol, direction, depth)` — forward or reverse
  bounded walk for exploration.
- Cycle detection (visited set) and a hard depth/result cap to bound cost.

**Why.** This is the headline capability of graph-native tools — blast-radius
in one call instead of the agent chaining many `callers` lookups — and it is
*only* possible once Phases 0–1 exist. It is also where ken's in-memory model
should shine.

**Performance impact.**

- *Query-time:* O(reachable nodes within depth), pure in-memory adjacency walk
  — no DB round-trip, no recursive SQL. Anchor for comparison: graph-native
  tools cite ~0.3 ms for a recursive-CTE traversal; an in-memory bounded BFS
  over `uint32` adjacency should be in the same order or faster. Enforce a
  result/`depth` cap (default small, e.g. depth 3) so a pathological hub symbol
  can't fan out to the whole graph; document the cap like `top_k`.
- *Index-time / memory:* zero new index-time cost; traversal reuses Phase 1
  adjacency. Optional memoization of hot roots is a query-time cache with an
  LRU bound (reuse the `KEN_MCP_CACHE_SIZE` pattern) — off by default to keep
  memory flat.
- *Serialization / watch-mode:* nothing new; consumes structures already
  serialized and already invalidated by Phase 1.

---

## Cross-cutting concerns

### Languages (the multiplier)

The structural layer covers 10 languages. Phases 0–2 are *mostly mechanical*
per language — each `extract_*.go` gains call-site attribution, spans, and
receiver capture, and each gets language-specific import-resolution rules in
Phase 2. **Phase 3 is per-language-hard** — type/heritage rules don't transfer.

Sequencing recommendation: land Phases 0–1 across **all 10** languages at once
(shared mechanical change, validated by the existing per-language extractor
tests). Land Phases 2–3 **Python-first**, then Go/TS/Java by demand signal,
because those carry the type-resolution complexity. Keep the
"unsupported-language silently degrades" property throughout — a language
without Phase 3 rules yields Phase 1 name-based edges, not an error.

### Validation — the methodology shift

Everything in ken to date is a **verbatim port of semble** with a precision
contract validated by diffing against the Python reference (the golden fixture,
the 11k-input tokenizer parity harness). **A resolved call graph has no
upstream reference to port** — it is original heuristic resolution. The
validation burden therefore shifts from "matches semble" to "matches a
hand-labeled ground truth":

- Build a small, labeled call-graph ground-truth corpus per language (extend
  the Stage 8 Gate-2 sample that already underpins the "file-level callers,
  100% precision" claim).
- Report **precision/recall of resolved edges** at each confidence tier, per
  language, in [`BENCH.md`](../BENCH.md) — the analog of the NDCG tables.
- Gate each phase on not regressing the prior phase's edge precision.

This harness is real work and is budgeted as part of Phases 1 and 3, not bolted
on after.

### Config / opt-out surface

Mirror ken's existing "expensive is opt-in, fast path is default" convention:

- Phases 0–2: on by default (cheap, strict superset of today's facts).
- Phase 3 (type resolution): `KEN_STRUCTURAL_TYPE_RESOLUTION=on`, default off.
- A master `KEN_STRUCTURAL=off` to disable the whole node-graph build for
  operators who only want retrieval + Arm B enrichment (which must keep working
  from `FileStruct` alone — see the Phase 0 enrichment invariant).

## Performance budget summary

| Phase | Index-time (cold Build) | Query-time | Resident memory | Serialized size | Watch-mode |
|---|---|---|---|---|---|
| 0 — data model | ~flat (same single walk) | flat | **main cost** — bound ≤2× via string interning + flat call slices | +fields, version bump | unaffected |
| 1 — call edges | +sub-linear pass (sharded, parallel) | **faster** per result | +adjacency (interned IDs, small) | +ID arrays | **new edge invalidation** (recompute-affected) |
| 2 — imports/deps | +O(imports), cheap | flat (+ new dep surface) | +module index (small) | +dep edges | local, fold into Phase 1 path |
| 3 — type resolution | **+1.3–1.8× structural pass — opt-in, default off** | flat (higher-confidence edges) | transient envs (pooled); persistent ~neutral | ~flat | heritage-dependent rerun |
| 4 — traversal/impact | zero | O(reachable≤depth), in-memory BFS, capped | zero (optional LRU memo) | zero | none |

Hard gates before any phase merges: (1) cold-start Build on the K8s (59,795)
and Postgres (64,506) demo corpora within budget; (2) resident
`*structural.Index` within ≤2× envelope (Phases 0–1); (3) pre-built-index
load + lazy-fallback still correct after the format bump; (4) watch-mode
query latency unchanged (atomic-snapshot invariant from ADR-012 preserved);
(5) edge precision/recall reported and non-regressing.

## Sequencing & triggers

**Revised after Plan-agent independent review (2026-06-03):**

1. **Phase 0** — unblocks everything; ship across all 10 languages.
   Trigger: now. **Status: SHIPPED.**
2. **Phases 1 + 4 BUNDLED behind `KEN_STRUCTURAL_GRAPH=on`** — function-level
   call resolution + transitive impact traversal, shipped as one opt-in
   unit so users get the headline capability (`impact(symbol)` blast-radius)
   on the same flag flip that upgrades `callers` to function-level. Phase 1
   alone would give a marginally-better `callers` with no headline
   capability and would invite scope-creep pressure to immediately do
   Phase 2 or 3; bundling 1+4 keeps the "expensive is opt-in" boundary
   crisp. **Trigger:** MCP log evidence that the agent's current 2-step
   pattern (`callers` → `outline` of returned files → re-query) is in
   practice 3+ steps in a measurable fraction of `callers` invocations
   (rough rule of thumb: >30% of `callers` calls followed by `outline`
   followed by another `callers` / `search`). One week of `ken-mcp` logs
   settles the question.
3. **Phase 2** — dependency edges + collision sharpening; all languages.
   Trigger: 1+4 ship + demand for the dependency surface, or Phase 1 edge
   precision capped by collisions in the first month of real usage.
4. **Phase 3** — type resolution; Python-first, opt-in. Trigger:
   measured edge-precision ceiling on OO-heavy corpora that Phase 2
   visibility can't lift.

### Why the Phase 1 + 4 bundling matters

The Plan-agent review flagged two things this doc originally
under-budgeted:

- **The validation harness is bigger than Phases 1 and 3's "part of
  the phase" framing suggests.** A multi-language hand-labeled
  call-graph ground-truth corpus with confidence-tier precision /
  recall reporting is on the same order as Stage 8 Gate 2 itself
  (~2-3 weeks of dedicated work), AND has no upstream reference to
  diff against (unlike everything ken has done to date, which diffed
  against semble's Python). The 6-10 week Phase 1+4 estimate should
  therefore be read as **6-10 weeks of implementation + a
  separately-budgeted eval-infrastructure project**. The implementation
  total is genuinely "6-10 weeks plus an eval surface ken has never
  built before"; don't take the implementation estimate as the
  shipping estimate.
- **Silent wrong answers from missed watch-mode invalidation.** Today's
  file-level `callers` degrades gracefully — a missed cross-file
  invalidation gives the agent a stale file in a list, agent re-reads,
  no harm. With resolved function-level edges, a missed cross-file
  invalidation hands the agent **a confidently-typed edge to the
  wrong function** — strictly worse than today's status quo. The
  Phase 1 recommendation in the original plan (recompute-affected
  invalidation strategy) is correct in principle but is the kind of
  correctness work that takes longer to harden than to write, and
  belongs in the risk register at higher weight than "memory blowup."

## Risk register

- **Silent wrong answers from missed watch-mode invalidation** (re-weighted
  highest as of the Plan-agent review). A resolved function-level graph has
  non-local invalidation; a missed invalidation yields a confidently-typed
  edge to the wrong function — strictly worse than today's file-level status
  quo, where a stale entry just costs a re-read. Mitigated by the reverse
  `symbol → referencing-files` index, a watch-mode integration test that
  asserts edge consistency after every cross-file edit shape (rename, move,
  delete, add) extending `watch_test.go`, and a CI gate that re-runs the
  shape suite under fuzzing. **Do not ship Phase 1 without those tests
  shipping in the same commit.**
- **Validation-harness scope creep.** The hand-labeled ground-truth corpus
  per language with confidence-tier precision/recall reporting is **not** a
  ~2-week side-task — it is on the same order as Stage 8 Gate 2 itself
  (~2-3 weeks of dedicated work) and has no upstream reference to diff
  against. Mitigated by budgeting it as a separate explicit deliverable
  before Phase 1+4 starts, NOT folded into the implementation estimate.
- **Memory blowup from per-call-site records.** Originally listed first; now
  judged lower than the two risks above based on Phase 0's measurements
  (~500 KiB CallRef substrate on a ~28 MiB structural index across jekyll /
  express / ripgrep). The ≤2× envelope held with room to spare; if Phase 1's
  adjacency lists threaten the envelope, string interning + flat slices land
  as the documented mitigation.
- **Type resolution over-promising.** The danger is presenting heuristic edges
  as authoritative. Mitigated by mandatory confidence on every edge, the
  labeled-ground-truth precision reporting, and keeping Phase 3 opt-in.
- **Enrichment regression.** Arm B (ADR-035) reads `FileStruct`; any reshape
  that changes the enrichment label changes retrieval NDCG. **Phase 0
  preserved this byte-identically via the `CalleeNames()` accessor** —
  `TestEnrich_ArmBBaseline_FormatStability` confirms. Same invariant
  applies to Phase 1+.
- **Per-language drift in Phase 3.** Type rules are bespoke per language and
  easy to get subtly wrong. Mitigated by Python-first + per-language gates +
  graceful degrade to Phase 1 edges.

## Relationship to ken's philosophy

This plan is deliberately *not* an attempt to out-resolve a compiler. It
extends ken's existing posture — fast, pure-Go, no-LSP, relevance-over-
completeness, honest about confidence — from retrieval into structure. The
landing spot is Phases 0–2 + 4 (function-level edges, dependency edges,
transitive impact) on by default, with Phase 3 type resolution as an opt-in
sharpening. That captures the large majority of a graph-native tool's
day-to-day value (blast-radius, who-calls-this, what-depends-on-this) while
staying inside the single-static-binary, no-external-service envelope that is
the whole point of ken.
