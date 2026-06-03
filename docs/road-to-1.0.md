# Road to 1.0

Living document tracking what stands between ken's current state and a
v1.0 release. Updated as items ship or change. Last updated:
2026-06-03 — **v0.9.0 and v0.9.1 tagged + released** with the
seven-item ship-list complete (closed 2026-06-02). v0.9.1 also
captures the language-coverage adds (Kotlin + Dart), parks
(C# + Swift), and the structural-call-graph Phase 0 substrate.
ken is feature-complete for 1.0; what remains is polish + the
strategic items in §4.

Status legend: 🟢 done · 🟡 open · 🔴 blocked · ⚪ deferred / killed

## (1) Retrieval — closed for 1.0

The exploration has been thorough and the relevance curve is flat.
This axis is treated as **closed for 1.0** and would only re-open on
a specific new bench gap.

**Killed cleanly with published evidence:**

| What | Verdict | Memo |
|---|---|---|
| HyDE on rerank-on path | Killed (M0a) | [outputs/m0-hyde-results.md](../outputs/m0-hyde-results.md) |
| Index-time enrichment with `callers` / `imports` / `signature` / `siblings` | Killed (M0e Track 1 floods) | outputs/m0e-results.md |
| Query-time graph expansion (additive boost) | Killed | [outputs/stage8-qgraph-expansion-results.md](../outputs/stage8-qgraph-expansion-results.md) |
| ColBERT MaxSim cheap-reuse on CodeRankEmbed token vectors | Parked (slim N=25, both prefix-on and prefix-off) | [outputs/stage8-maxsim-probe-parked.md](../outputs/stage8-maxsim-probe-parked.md). Companion analysis at [colbert-late-interaction-for-ken.md](colbert-late-interaction-for-ken.md). |
| Generative LLM lever (Stage 6) | **Newly unblocked** by aikit v0.2.0 shipping a pure-Go decoder + tokenizer + GGUF. Was previously gated on Option C feasibility. Three concrete paths analysed in [stage6-paths-with-aikit-decoder.md](stage6-paths-with-aikit-decoder.md) — Path A (HyDE expansion) is the cheap probe; uses existing Stage 7a bench harness. No commitment yet; recommendation is to run the cheap probe first. |

**Shipped in production:**

BM25 (identifier-aware) + Model2Vec + RRF + 3-tier penalties +
CodeRankEmbed neural rerank (M0–M11) + Stage 8 Track 2 tools + Arm B
enrichment (default-on; **shipped 2026-06-02 in ADR-035**, +0.0208
hybrid on csn-stripped, +0.0321 on CoSQA reproducing Gate-1 within
0.002 on the production code path). As of v0.9.1 (2026-06-03) the
RRF math lives in `aikit/fuse.RRFWeighted` rather than ken-local
code — numerically identical, just consolidated onto the toolkit.

**Structural call-graph Phase 0 substrate landed 2026-06-03**
([docs/structural-call-graph-plan.md](structural-call-graph-plan.md) —
span fields on every symbol, per-call-site `CallRef` records,
`CalleeNames()` accessor preserving Arm B byte-identity). This is
substrate-only — no MCP tool surface change yet — but the data
model is now ready for Phase 1+4 when the trigger fires (MCP log
evidence that the agent's current 2-step `callers → outline →
re-query` pattern is in practice 3+ steps in ≥30% of `callers`
invocations).

**Remaining ideas in retrieval-space are 0.005-level wins, not 0.05:**

- ⚪ Per-language α tuning — Python/Java/Rust likely want a different
  hybrid mix. Future probe if a bench gap emerges.
- ⚪ Alternative rerank model (UniXcoder, jina-code-reranker). Each is
  a multi-week port; M0's +0.165 is already large.
- ⚪ Query rewriting via a tiny in-process classifier
  ("is this NL or code-shaped?"). Small future probe.

## (2) Features missing — ranked by user-impact-for-effort

### Ship-list for 1.0

| Rank | Feature | Status | Effort | Why |
|---|---|---|---:|---|
| 1 | `callers(name)` MCP tool | 🟢 shipped | done | 2026-06-02. handleCallers + AddTool registration in mcp/server.go + types.CallersArgs. File-level granularity; honest framing in tool description + response header. |
| 2 | Search filters: `languages` / `path_contains` / `exclude_path_contains` | 🟢 shipped | done | 2026-06-02. SearchArgs extended; runSearchWithTelemetry over-fetches 10× when filters are set, post-filters, reports candidate-vs-filter ratio in the header. Substring match (not glob); covers the canonical "find auth code in src/api/" + "exclude test files" cases. |
| — | ~~Windows binary~~ | ⚪ deferred-until-pressure | — | Owner preference (2026-06-02): defer until extreme user pressure. Pure-Go cross-compile is technically trivial but the support surface (CRLF, path separators, Windows-specific MCP quirks, npm/PowerShell install paths) is not free. Re-open ONLY if a real user reports being blocked. |
| 4 | `ken status` CLI + MCP tool | 🟢 shipped | done | 2026-06-02. New `internal/status` package builds a Status snapshot (versions, models, Arm B env, savings, optional live index + structural + cache). `ken status` CLI + `status` MCP tool registered on both NewServer and Run paths. Output modes: text (default), `--json` / `output:"json"`, markdown for MCP. Token savings surfaced as today / 7d / all-time with chars + ~tokens estimate. |
| 5 | `recently_changed(N)` MCP tool (git-aware) | 🟢 shipped | done | 2026-06-02. mcp/recently_changed_tool.go — go-git PlainOpen, walks HEAD N commits back, formats commit + changed-file list as markdown. Args: `n` (default 10, max 100), `repo`, `path` prefix filter. Local repos only in Pass 1; URL repos return a friendly "clone first" error rather than coupling to the cache's temp clone dir. |
| 6 | JSON output mode for `search` and structural tools | 🟢 shipped | done | 2026-06-02. Each of search / find_related / definition / references / callers / outline / symbols got an `Output` arg. `mcp/json_responses.go` defines a typed response struct per tool (1.0-stable surface) + a shared `dispatchOutput` helper. Markdown stays the default; `output: "json"` returns the typed struct as indented JSON. Unknown values like `"yaml"` get a friendly error rather than silent fallback. Tests: 13 sub-tests across `TestRun_JSONOutput` (Run-path: search + find_related + dispatch corners) and `TestJSONOutput_StructuralTools` (NewServer fixture upgraded to also build a structural index). |
| 7 | First-class user docs | 🟢 shipped | done | 2026-06-02. Two-tier user-facing docs: [`docs/USERS.md`](USERS.md) (install per agent, ken-vs-grep decision, the 9 tools at-a-glance, common env vars, troubleshooting) + [`docs/DEVELOPERS.md`](DEVELOPERS.md) (mcp.Run library, prebuilt indices, public API stability table, JSON output mode, custom chunkers, tuning rerank, performance expectations). README + GitHub Pages index both link both docs. Audience cut (Option A): agent users vs SDK authors. |
| — | Perf campaign: startup + query latency | 🟢 closed (ADR-036) | done | 2026-06-02. **M0 baselines** ([memo](../outputs/perf-startup-m0-baselines.md)) → **M2 lazy rerank load** ([memo](../outputs/perf-startup-m2-results.md)): −491 ms when `KEN_MCP_RERANK=on`. → **M4 parallel `structural.Build`** ([memo](../outputs/perf-startup-m4-results.md)): 3.5× on jekyll (−1,127 ms). → **M1 killed without code change** ([memo](../outputs/perf-startup-m1-results.md)): M2 superseded it. M3 + M5 killed by M0 data. **Cumulative cold-start reduction: 55-79% across corpora**; warm-search p50 already sub-ms. Closed via [ADR-036](DECISIONS.md#adr-036). |
| — | Language coverage (post-1.0 ship list) | 🟢 12 languages shipped | done | 2026-06-03 (v0.9.1). Added Kotlin (.kt/.kts) + Dart (.dart) on top of the v0.9.0 ten (Python · Go · TypeScript · JavaScript · Java · Rust · C · C++ · PHP · Ruby). Dart coordinated three wirepoints (aikit chunker + .goreleaser.yml slim subset + structural index) with the drift-guard test staying green. C# + Swift parked behind `csharp` / `swift` build tags with upstream-ready diagnostic memos at [docs/csharp-oom-root-cause.md](csharp-oom-root-cause.md) + [docs/swift-parse-root-cause.md](swift-parse-root-cause.md). Developer walkthrough at [docs/add-a-language.md](add-a-language.md). |
| — | Structural call-graph Phase 0 (substrate) | 🟢 shipped 2026-06-03 | done | Span fields on FuncDef/ClassDef, per-call-site CallRef with enclosing-symbol attribution, CalleeNames() accessor preserving Arm B byte-identity. All 10 shipping languages plus the build-tagged C# / Swift extractors. Memory budget gate cleared on jekyll / express / ripgrep. Plan doc revised per the Plan-agent independent review (Phases 1+4 bundled behind one opt-in flag, validation-harness scope explicit, silent-wrong-answer watch-mode risk elevated). Substrate only — no MCP tool surface change; Phases 1+4 trigger-gated on MCP log evidence. See [docs/structural-call-graph-plan.md](structural-call-graph-plan.md). |

### Lower-priority but real

- ⚪ **Bulk-search** — multi-query in one MCP call, saves roundtrips for agent workflows.
- ⚪ **Per-result "why this match" explanation** — hard but useful for debugging.
- ⚪ **Saved searches / query aliases** — overkill for 1.0.
- ⚪ **Auth/access control** for multi-repo MCP serving — not 1.0 unless someone hosts ken-as-a-service.

## (3) Nits before 1.0

| Nit | Status | Notes |
|---|---|---|
| Untracked / new planning docs in `docs/` | 🟢 tracked 2026-06-03 | Was 🟡 (`colbert-late-interaction-for-ken.md`, `ken-context.md`). Both now tracked. Subsequent planning + analysis docs added through v0.9.1 are tracked under their respective commits: `add-language-support-kotlin-csharp-swift-dart.md`, `structural-call-graph-plan.md`, `stage6-paths-with-aikit-decoder.md`, `csharp-oom-root-cause.md`, `swift-parse-root-cause.md`, `add-a-language.md`. |
| MCP tool description audit | 🟢 done (2026-06-02 partial + 2026-06-03 close) | Stale "Stage 8 extractor covers Python only" copy fixed; v0.9.0 added the public-API discipline audit ([0f780b4](../README.md)). Voice/length sweep across all 7 tools held to "clean and consistent enough" per the audit's own bar. |
| Deprecated functions | 🟢 un-deprecated for 1.0 | v0.9.0 dropped the Deprecated markers on `search.FromPath` + `repo.Walk` after the 1.0 API audit confirmed both signatures stable. Doc-comments now describe them as "1.0-stable" with rationale (string path vs fs.FS choice). |
| `CHANGELOG.md` currency | 🟢 current through v0.9.1 + Phase 0 | v0.9.0 and v0.9.1 entries exist with annotated tags; the Phase 0 substrate entry lives under [Unreleased] pending the next tag. |
| CI Docker-pulls-Postgres flake | 🟢 documented + mitigation | The flake is at GitHub Actions' service-container provisioning layer — before any step we control runs. Actions has built-in pull-retry-with-backoff; when Docker Hub is slow OR has an outage (recent release push hit this), even the built-in retry exhausts. **Live mitigation: `gh run rerun <run-id> --failed`** (already-green jobs stay green). **Permanent fix: mirror service images to ghcr.io** — deferred until the flake becomes load-bearing. Documented in [`.github/workflows/ci.yml`](../.github/workflows/ci.yml) above the `test-db-integration` job. |
| `internal/structural/Enrich()` unused opts | 🟡 | Arm B baseline is shipping but `Callers/Imports/Signature/Siblings` options exist and aren't on the ship path. M0e proved they don't help. Either delete or document as "experimental / not in production." |
| Bench-side parallel impl | 🟢 documented | After ADR-035 ship, the Python materializers are docs-noted as "bench-only fallback for drift cross-checks." Retained as a known-good reference for any future drift investigation; production goes through `structural.EnrichFromFileStruct`. |

## (4) Strategic / positioning items

- 🟡 **Versioning / public API discipline.** v1.0 = a stability
  promise for `mcp.Run`, the `chunk.Chunker` interface (ADR-032), and
  the public `search` / `structural` surfaces. Worth a 1-day audit
  identifying every public symbol crossing a package boundary and
  either pinning it as 1.0-stable or marking it `// Internal:`.
- 🟡 **Flagship demo with broad recognition** in addition to or
  replacing the current kubernetes + postgres demos. E.g. "ken
  indexes the Go standard library, ask it questions" — instantly
  understandable to anyone who's written Go.
- 🟡 **Document the recommended Claude-Code-with-ken workflow**.
  `ultracode` / `ultrareview` are Claude Code skills, not ken
  features, but they're the easiest dogfood path.
- 🟢 **Aikit's 1.0** — coordination ongoing. ken pins `aikit v0.2.0`
  in go.mod (bumped from v0.1.1 on 2026-06-03 in commit `b3ea116`).
  v0.2.0 added the generative half — pure-Go `decoder` + `tokenizer`
  + GGUF support — without disturbing the v0.1 hard tier; both new
  packages are best-effort. aikit's README documents its stability
  tiers in the same hard/best-effort shape ken uses;
  [DEVELOPERS.md](DEVELOPERS.md#aikit-packages) notes that ken 1.0
  requires aikit at a tagged 1.0 (or clearly within a 1.0-RC
  window) so the stability promises compose cleanly. v0.2.0's
  generative-half packages would currently be best-effort under
  that requirement — a coordination point, not a blocker for ken's
  1.0 release.
- 🟡 **Performance expectations doc.** ADRs 026–030 set baselines.
  "What should ken feel like" — "indexing the Linux kernel takes
  ~X minutes, queries are sub-Y ms" — sets user expectations and
  catches regressions.

## Honest summary

ken is **1.0-ready on the retrieval axis** and **feature-complete
on the ship list** as of v0.9.1 (2026-06-03). What remains for the
v1.0 cut is the strategic-items list in §4 — flagship demo,
performance expectations doc, the final public-API freeze — plus
any 🟡 nits that surface as load-bearing. None of it is research;
all of it is well-scoped polish, and several items are
"decide-and-document" rather than "build."

Two newer items that became possible only via the aikit v0.2.0 bump
and the structural-call-graph Phase 0 substrate are deliberately
post-1.0:

- **Stage 6 (generative-LLM lever).** Now feasible — see
  [stage6-paths-with-aikit-decoder.md](stage6-paths-with-aikit-decoder.md).
  Recommended next move is the Path A (HyDE) cheap probe on the
  existing Stage 7a bench harness. Doesn't gate 1.0.
- **Structural call-graph Phases 1+4** (function-level resolved
  `callers` + transitive `impact` tool). Substrate ready; ship
  trigger is MCP-log evidence. Doesn't gate 1.0.

## Maintenance

This doc is updated when items ship, get killed, or change scope.
The owning Claude Code instance is expected to keep it current — if
the table above lists a 🟡 that's actually done, the next session
that touches it should mark it 🟢 with a link to the commit / memo
that closed it.
