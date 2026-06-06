# Road to 1.0

Living document tracking what stands between ken's current state and a
v1.0 release. Updated as items ship or change. Last updated:
2026-06-06 — **v0.10.0 tagged + released**: C# (13th language), ken-mcp
model **auto-fetch** on first run (ADR-037), **Windows binaries**, and
the structural-call-graph Phase 0 substrate. Plus the recall-narrative
correction — the headline "82–91%" was the BM25-only fallback; default
hybrid measures **~0.97 recall@10** (0.967 NL / 0.995 symbol) — and the
docs reorg (working docs → `docs/internal/`, README 711 → 184 lines).
ken is feature-complete and **at its retrieval ceiling** for 1.0. Both
remaining gates are now **cleared** (2026-06-06): aikit 1.0 pinned
(`aikit v1.0.0` + `chunk/treesitter v1.0.0`, validated byte-identical),
and distribution done (Homebrew cask + Scoop manifest shipped in v0.10.1
alongside the v0.10.0 Windows binaries). **`v1.0.0` is ready to cut** —
straight to 1.0, no RC.

Status legend: 🟢 done · 🟡 open · 🔴 blocked · ⚪ deferred / killed

## (1) Retrieval — closed for 1.0

The exploration has been thorough and the relevance curve is flat.
This axis is treated as **closed for 1.0** and would only re-open on
a specific new bench gap.

**Killed cleanly with published evidence:**

| What | Verdict | Memo |
|---|---|---|
| HyDE on rerank-on path | Killed (M0a) | [outputs/m0-hyde-results.md](../../outputs/m0-hyde-results.md) |
| Index-time enrichment with `callers` / `imports` / `signature` / `siblings` | Killed (M0e Track 1 floods) | outputs/m0e-results.md |
| Query-time graph expansion (additive boost) | Killed | [outputs/stage8-qgraph-expansion-results.md](../../outputs/stage8-qgraph-expansion-results.md) |
| ColBERT MaxSim cheap-reuse on CodeRankEmbed token vectors | Parked (slim N=25, both prefix-on and prefix-off) | [outputs/stage8-maxsim-probe-parked.md](../../outputs/stage8-maxsim-probe-parked.md). Companion analysis at [colbert-late-interaction-for-ken.md](colbert-late-interaction-for-ken.md). |
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
([docs/internal/structural-call-graph-plan.md](structural-call-graph-plan.md) —
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
| — | Windows binary | 🟢 shipped 2026-06-06 | done | Re-opened 2026-06-05 (was ⚪ deferred); shipped as part of v0.10.0. `.goreleaser.yml` now targets `windows/amd64`+`arm64` (`.zip` archives); `sighup_windows.go` already no-ops the Unix-only SIGHUP path (the `aikit v0.4.1` Windows build fix landed earlier). Release-equivalent slim builds cross-compile clean both arches; `goreleaser check` green. **Still open:** scoop manifest (needs a scoop-bucket repo) + a Windows CI smoke job — C3 of [`onboarding-plan.md`](onboarding-plan.md). |
| 4 | `ken status` CLI + MCP tool | 🟢 shipped | done | 2026-06-02. New `internal/status` package builds a Status snapshot (versions, models, Arm B env, savings, optional live index + structural + cache). `ken status` CLI + `status` MCP tool registered on both NewServer and Run paths. Output modes: text (default), `--json` / `output:"json"`, markdown for MCP. Token savings surfaced as today / 7d / all-time with chars + ~tokens estimate. |
| 5 | `recently_changed(N)` MCP tool (git-aware) | 🟢 shipped | done | 2026-06-02. mcp/recently_changed_tool.go — go-git PlainOpen, walks HEAD N commits back, formats commit + changed-file list as markdown. Args: `n` (default 10, max 100), `repo`, `path` prefix filter. Local repos only in Pass 1; URL repos return a friendly "clone first" error rather than coupling to the cache's temp clone dir. |
| 6 | JSON output mode for `search` and structural tools | 🟢 shipped | done | 2026-06-02. Each of search / find_related / definition / references / callers / outline / symbols got an `Output` arg. `mcp/json_responses.go` defines a typed response struct per tool (1.0-stable surface) + a shared `dispatchOutput` helper. Markdown stays the default; `output: "json"` returns the typed struct as indented JSON. Unknown values like `"yaml"` get a friendly error rather than silent fallback. Tests: 13 sub-tests across `TestRun_JSONOutput` (Run-path: search + find_related + dispatch corners) and `TestJSONOutput_StructuralTools` (NewServer fixture upgraded to also build a structural index). |
| 7 | First-class user docs | 🟢 shipped | done | 2026-06-02. Two-tier user-facing docs: [`docs/USERS.md`](../USERS.md) (install per agent, ken-vs-grep decision, the 9 tools at-a-glance, common env vars, troubleshooting) + [`docs/DEVELOPERS.md`](../DEVELOPERS.md) (mcp.Run library, prebuilt indices, public API stability table, JSON output mode, custom chunkers, tuning rerank, performance expectations). README + GitHub Pages index both link both docs. Audience cut (Option A): agent users vs SDK authors. |
| — | Perf campaign: startup + query latency | 🟢 closed (ADR-036) | done | 2026-06-02. **M0 baselines** ([memo](../../outputs/perf-startup-m0-baselines.md)) → **M2 lazy rerank load** ([memo](../../outputs/perf-startup-m2-results.md)): −491 ms when `KEN_MCP_RERANK=on`. → **M4 parallel `structural.Build`** ([memo](../../outputs/perf-startup-m4-results.md)): 3.5× on jekyll (−1,127 ms). → **M1 killed without code change** ([memo](../../outputs/perf-startup-m1-results.md)): M2 superseded it. M3 + M5 killed by M0 data. **Cumulative cold-start reduction: 55-79% across corpora**; warm-search p50 already sub-ms. Closed via [ADR-036](DECISIONS.md#adr-036). |
| — | Language coverage (post-1.0 ship list) | 🟢 13 languages shipped | done | 2026-06-03 (v0.9.1): Kotlin + Dart on top of the v0.9.0 ten (Python · Go · TypeScript · JavaScript · Java · Rust · C · C++ · PHP · Ruby). **C# added 2026-06-06** on gotreesitter v0.20.2 — the OOM that parked it (#98/#106 bounded the C# namespace-recovery sub-parses) is fixed upstream; coordinated the same three wirepoints as Dart (aikit `chunk/treesitter` v0.4.1 `KenToTreeSitter` + `.goreleaser.yml` `grammar_subset_c_sharp` + structural extractor), drift-guard green, with an OOM-trigger regression test. **Swift stays parked**: re-tested on v0.20.2 (#99/#107 license-header fix lifted Alamofire 0%→35% clean, but ~65% still fail + ~20% take 2–6s/parse — not viable). Diagnostic memos: [docs/internal/csharp-oom-root-cause.md](csharp-oom-root-cause.md) (now resolved) + [docs/internal/swift-parse-root-cause.md](swift-parse-root-cause.md). Developer walkthrough at [docs/internal/add-a-language.md](add-a-language.md). |
| — | Structural call-graph Phase 0 (substrate) | 🟢 shipped 2026-06-03 | done | Span fields on FuncDef/ClassDef, per-call-site CallRef with enclosing-symbol attribution, CalleeNames() accessor preserving Arm B byte-identity. All 10 shipping languages plus the build-tagged C# / Swift extractors. Memory budget gate cleared on jekyll / express / ripgrep. Plan doc revised per the Plan-agent independent review (Phases 1+4 bundled behind one opt-in flag, validation-harness scope explicit, silent-wrong-answer watch-mode risk elevated). Substrate only — no MCP tool surface change; Phases 1+4 trigger-gated on MCP log evidence. See [docs/internal/structural-call-graph-plan.md](structural-call-graph-plan.md). |

### Lower-priority but real

- ⚪ **Bulk-search** — multi-query in one MCP call, saves roundtrips for agent workflows.
- ⚪ **Per-result "why this match" explanation** — hard but useful for debugging.
- ⚪ **Saved searches / query aliases** — overkill for 1.0.
- ⚪ **Auth/access control** for multi-repo MCP serving — not 1.0 unless someone hosts ken-as-a-service.

## (3) Nits before 1.0

| Nit | Status | Notes |
|---|---|---|
| Untracked / new planning docs in `docs/` | 🟢 tracked 2026-06-03 | Was 🟡 (`colbert-late-interaction-for-ken.md`, `ken-context.md`). Both now tracked. Subsequent planning + analysis docs added through v0.9.1 are tracked under their respective commits: `add-language-support-kotlin-csharp-swift-dart.md`, `structural-call-graph-plan.md`, `stage6-paths-with-aikit-decoder.md`, `csharp-oom-root-cause.md`, `swift-parse-root-cause.md`, `add-a-language.md`. |
| MCP tool description audit | 🟢 done (2026-06-02 partial + 2026-06-03 close) | Stale "Stage 8 extractor covers Python only" copy fixed; v0.9.0 added the public-API discipline audit ([0f780b4](../../README.md)). Voice/length sweep across all 7 tools held to "clean and consistent enough" per the audit's own bar. |
| Deprecated functions | 🟢 un-deprecated for 1.0 | v0.9.0 dropped the Deprecated markers on `search.FromPath` + `repo.Walk` after the 1.0 API audit confirmed both signatures stable. Doc-comments now describe them as "1.0-stable" with rationale (string path vs fs.FS choice). |
| `CHANGELOG.md` currency | 🟢 current through v0.9.1 + Phase 0 | v0.9.0 and v0.9.1 entries exist with annotated tags; the Phase 0 substrate entry lives under [Unreleased] pending the next tag. |
| CI Docker-pulls-Postgres flake | 🟢 documented + mitigation | The flake is at GitHub Actions' service-container provisioning layer — before any step we control runs. Actions has built-in pull-retry-with-backoff; when Docker Hub is slow OR has an outage (recent release push hit this), even the built-in retry exhausts. **Live mitigation: `gh run rerun <run-id> --failed`** (already-green jobs stay green). **Permanent fix: mirror service images to ghcr.io** — deferred until the flake becomes load-bearing. Documented in [`.github/workflows/ci.yml`](../../.github/workflows/ci.yml) above the `test-db-integration` job. |
| `internal/structural/Enrich()` unused opts | 🟢 documented 2026-06-06 | Marked experimental rather than deleted: `Callers/Imports/Signature/Siblings` (M0e-disproven) now carry an EXPERIMENTAL doc on `EnrichOptions` stating they're off the production ship path (which builds `EnrichOptions{}` — func/calls/raises only). NOT deleted because their one live consumer is `scripts/materialize_heur.go`, the drift/research harness nit #94 deliberately retains; deleting the opts would cascade into that harness + tests. |
| Bench-side parallel impl | 🟢 documented | After ADR-035 ship, the Python materializers are docs-noted as "bench-only fallback for drift cross-checks." Retained as a known-good reference for any future drift investigation; production goes through `structural.EnrichFromFileStruct`. |

## (4) Strategic / positioning items

- 🟡 **Hybrid-by-default onboarding** — opened 2026-06-05. The recall
  decomposition (BENCH.md "Default-mode (hybrid) recall";
  `internal/search/recall_decomp_test.go`) showed default hybrid measures
  ~0.97 recall@10 and the "0.82–0.91" is the BM25-only fallback; the only
  users silently stuck on the fallback are ken-mcp users with no model
  (silent stderr downgrade at `cmd/ken-mcp/main.go:378`). Plan +
  owner-approved scope in [`docs/internal/onboarding-plan.md`](onboarding-plan.md):
  **(A) background auto-fetch on first run [recall lever] — 🟢 SHIPPED
  2026-06-06 ([ADR-037](DECISIONS.md#adr-037-ken-mcp-auto-fetches-the-embedding-model-on-first-run-background-default-on)):
  `KEN_MCP_AUTO_FETCH` default-on, `KEN_MCP_MODEL_DIR` defaults to
  `~/.ken/model`, `Cache.Purge()` + rebuild on model arrival, DB case gets
  a restart prompt. A fresh install now lands on the ~0.97 hybrid path.**
  **(B) loud degraded-state notice — ⚪ moot:** auto-fetch makes the bm25
  window transient, so there's no persistent degraded state to warn
  about. **(C) distribution — 🟢 DONE:** Windows binaries shipped in
  v0.10.0; **Homebrew cask + Scoop manifest shipped in v0.10.1** (GoReleaser
  publishes to `townsendmerino/homebrew-tap` + `scoop-bucket`; both install
  `ken` + `ken-mcp`; PAT + cross-repo push verified live) + a `windows-latest`
  CI smoke job. Bundled model-embedded binary ⚪ optional (not needed —
  auto-fetch covers first-run). ADR-037 captured the network-egress default.
  **Net: onboarding + distribution fully shipped.** winget remains a
  post-1.0 nice-to-have.

- 🟢 **Versioning / public API discipline** — closed 2026-06-03.
  Audit walked every public symbol crossing a package boundary
  (`go doc -short ./mcp` + `./mcp/db`); each is now pinned by tier in
  [DEVELOPERS.md → Public API surface](../DEVELOPERS.md#public-api-surface).
  Three internal-type leaks through the mcp package surface
  (`Config.Mode` was `search.Mode`, `Config.TelemetryLog` carried
  `search.Telemetry`, `FormatResults` took `[]search.Result`) were
  resolved by adding [`mcp/api_aliases.go`](../../mcp/api_aliases.go) —
  1.0-stable `mcp.Mode` / `mcp.Telemetry` / `mcp.Result` type aliases
  so SDK authors never need to import `internal/search`. cmd/ken-mcp
  + the mcp package itself keep their existing `search.*` imports;
  runtime behaviour is unchanged because Go type aliases name the
  same type. Code-level `Stability:` doc-comments added to the
  load-bearing entry points (`Run`, `NewServer`, `NewCache`,
  `FormatResults`, `Logger`). Best-effort symbols (`CloneShallow`,
  `NormalizeKey`, `ValidateEnum`) keep their existing
  "best-effort" stability notes.
- 🟢 **Flagship demo with broad recognition** — shipped 2026-06-03 at
  [`demos/go-stdlib/`](../../demos/go-stdlib/). "ken indexes the Go
  standard library, ask it questions." Same single-static-binary
  pattern as the kubernetes + postgres demos (regex chunker, hybrid
  mode, 35,708 chunks on the Go 1.26.3 stdlib at `$GOROOT/src` minus
  `cmd/` and `*/testdata/*`). 191 MB binary, ~2 s startup.
  Phase 1 vetted **14 demo artifacts** — 7 semantic-bridging
  queries, 4 structural-tool lookups, 3 grep-vs-ken head-to-head
  comparisons (one shows grep returning 1,103 hits across 131 files
  vs ken returning 1 chunk). Two reproducible harnesses in tree:
  [`scripts/stdlib_demo_vet.go`](../../scripts/stdlib_demo_vet.go) for
  the semantic-bridging side, [`scripts/stdlib_phase1_close.go`](../../scripts/stdlib_phase1_close.go)
  for structural + head-to-head. Audience can reproduce the entire
  demo against their own `$GOROOT/src` in 30 s (rsync recipe in the
  demo README). Per the kickoff doc's recommendation: flagship +
  supporting positioning — kubernetes/postgres stay as proof-of-
  scale and proof-of-polyglot-reach. **What's left for Phase 3:**
  agent-session capture (transcripts) + blog post + GitHub Pages
  landing update; tracked separately on the demo plan, not gating
  the binary itself.
- 🟢 **Document the recommended Claude-Code-with-ken workflow** —
  shipped 2026-06-03 at [`docs/internal/claude-code-workflow.md`](claude-code-workflow.md).
  Four-layer framing: routine (just ken) → local `/review` →
  `/code-review ultra` before merging load-bearing diffs → ultracode
  sideways for genuinely parallel work. Honest about cost / time on
  the cloud-side skills and consistent with USERS.md's existing
  ken-vs-grep decision matrix.
- 🟢 **Aikit's 1.0 — CLEARED 2026-06-06.** aikit shipped 1.0; ken now
  pins `aikit v1.0.0` + `aikit/chunk/treesitter v1.0.0`. Validated against
  the published modules (GOWORK=off): full `go test ./...` 15/15,
  `build_parity` + the `grammar_subset` drift guard green, slim release
  build compiles — the ken-imported surface is byte-identical, so
  retrieval output is unchanged. The stability tiers now compose: ken 1.0
  rests on aikit 1.0's hard-tier packages. (History: aikit v0.1.1 → v0.2.0
  → v0.3.0 → v0.4.x extraction/C# → **v1.0.0**.)
  v0.2.0 added the generative half — pure-Go `decoder` + `tokenizer`
  + GGUF support — without disturbing the v0.1 hard tier; both new
  packages are best-effort. **v0.3.0 is decoder/quant maturation:**
  full K-quant + IQ2/3/4 ladder, Mellum2 (code-pretrained) +
  qwen2/qwen3/gemma3/Qwen-MoE end-to-end from bare GGUF, GPTQ + AWQ
  safetensors-resident int4, parallel per-layer load (Mellum2-12B
  Q4_K_M ~2 min → ~20 s), SDOT/NEON int8 kernels, and the
  `constrain` package (structured decoding that cannot emit
  malformed JSON). Zero API churn in the ken-imported surface
  (`ann`, `bm25`, `topk`, `encoder`, `chunk` + 3 sub, `fuse`,
  `embed`) — `embed/model.go` / `tokenize.go` / `pool.go`
  byte-identical; the only edit in the import surface was a 15-line
  additive `embed/safetensors.go` change for GPTQ checkpoint
  sniffing. aikit's README documents its stability tiers in the
  same hard/best-effort shape ken uses;
  [DEVELOPERS.md](../DEVELOPERS.md#aikit-packages) notes that ken 1.0
  requires aikit at a tagged 1.0 (or clearly within a 1.0-RC
  window) so the stability promises compose cleanly. The
  generative-half packages would still be best-effort under that
  requirement — a coordination point, and **the gating dependency for
  ken's own `v1.0.0`** (which must pin a tagged aikit 1.0).
- 🟢 **Performance expectations doc** — shipped 2026-06-03 at
  [`docs/PERF-expectations.md`](../PERF-expectations.md). User-facing
  "what should ken feel like" layer above PERF.md's measurement
  methodology. Cold-start budget split (M2 + M4 wins from ADR-036
  cited inline), warm-search p50 sub-ms claim, cold vs warm-cache
  rerank latency (from the v0.9.1 neural rerank bench), six
  concrete regression red flags with the exact bench/script to
  re-check each. Honest about what's NOT measured: no published
  Linux-kernel number (extrapolation only), no x86_64 second-
  machine pass, no huge-monorepo memory data.

## Honest summary

As of **v0.10.0 (2026-06-06)** ken is at its **retrieval ceiling** —
default hybrid measures ~0.97 recall@10 (0.967 NL / 0.995 symbol;
`internal/search/recall_decomp_test.go`), further gains are 0.005-level,
and **rerank/ColBERT is explicitly NOT a 1.0 item** (the opt-in neural
reranker already covers the high-value chunk-retrieval case; ColBERT is
parked — §1). It is also **feature-complete** on the ship list, and the §4
strategic items — public-API freeze, flagship demo, perf-expectations
doc, claude-code-workflow doc, and the recall-lever onboarding
(auto-fetch, ADR-037) — are all **shipped**.

**Both 1.0 gates are now cleared (2026-06-06):**

1. **Aikit 1.0 — 🟢 done.** ken pins `aikit v1.0.0` +
   `aikit/chunk/treesitter v1.0.0`; validated byte-identical against the
   published modules (full suite + build-parity + drift guard green).
2. **Distribution — 🟢 done.** Windows binaries (v0.10.0) + Homebrew cask +
   Scoop manifest (v0.10.1), cross-repo publish verified live.

The §3 nits are closed (the `Enrich()` opts marked experimental). **Nothing
substantive remains — `v1.0.0` is ready to cut (straight to 1.0, no RC, per
owner).** Just push the aikit-1.0-pin commit, roll the CHANGELOG to
`[1.0.0]` (done), and tag.

Deliberately post-1.0 (substrate ready, trigger-gated):

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
