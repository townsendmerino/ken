# Road to 1.0

Living document tracking what stands between ken's current state and a
v1.0 release. Updated as items ship or change. Last updated:
2026-06-02 (Arm B shipped, MaxSim parked).

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
| ColBERT MaxSim cheap-reuse on CodeRankEmbed token vectors | Parked (slim N=25, both prefix-on and prefix-off) | [outputs/stage8-maxsim-probe-parked.md](../outputs/stage8-maxsim-probe-parked.md) |

**Shipped in production:**

BM25 (identifier-aware) + Model2Vec + RRF + 3-tier penalties +
CodeRankEmbed neural rerank (M0–M11) + Stage 8 Track 2 tools + Arm B
enrichment (default-on; **shipped 2026-06-02 in ADR-035**, +0.0208
hybrid on csn-stripped, +0.0321 on CoSQA reproducing Gate-1 within
0.002 on the production code path).

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
| 1 | `callers(name)` MCP tool | 🟡 | ~1 day | Stage 8 Gate 2 recommendation. File-level granularity is honest; immediate agent value. |
| 2 | Search filters: `--language` / `--path-glob` / `--exclude-path` | 🟡 | ~1 day | Agents constantly want "find auth code in `src/api/` only" — currently they post-filter. Implement at chunk-iteration. **High-value, low-risk.** |
| 3 | Windows binary in the release matrix | 🟡 | ~half day | [`.goreleaser.yml`](../.goreleaser.yml) builds 4 platforms (darwin/linux × amd64/arm64); no Windows. Pure-Go ⇒ cross-compiles cleanly. Adoption blocker for Windows-first developers. |
| 4 | `ken status` (CLI + MCP tool) | 🟡 | ~half day | Indexed file count, last-rebuild time, watch status, model availability, structural-extractor coverage by language. Reassurance signal. |
| 5 | `recently_changed(N)` MCP tool (git-aware) | 🟡 | ~half day | Common agent question; currently they shell out. Reuses the `go-git` dep we already have. |
| 6 | JSON output mode for `search` and structural tools | 🟡 | ~half day | Markdown-only locks out programmatic consumers. Add `args.output = "json" \| "markdown"`. |
| 7 | First-class user docs | 🟡 | ~2 days | "How to think about ken vs grep" decision doc, "tuning your config" doc, MCP-tool-by-tool reference. |

### Lower-priority but real

- ⚪ **Bulk-search** — multi-query in one MCP call, saves roundtrips for agent workflows.
- ⚪ **Per-result "why this match" explanation** — hard but useful for debugging.
- ⚪ **Saved searches / query aliases** — overkill for 1.0.
- ⚪ **Auth/access control** for multi-repo MCP serving — not 1.0 unless someone hosts ken-as-a-service.

## (3) Nits before 1.0

| Nit | Status | Notes |
|---|---|---|
| 2 untracked docs in `docs/` | 🟡 | [`colbert-late-interaction-for-ken.md`](colbert-late-interaction-for-ken.md), [`ken-context.md`](ken-context.md). Either track or `.gitignore`; they've been in every `git status` for weeks. |
| MCP tool description audit | 🟡 | 7 tool description strings in [`mcp/server.go`](../mcp/server.go). Check consistency in voice / length and agreement with tested behavior. `references` returns text not structured callsites; `outline` is file-scoped; etc. |
| Deprecated functions | 🟡 | `search.FromPath` ([internal/search/index.go](../internal/search/index.go)) and `repo.Walk` ([internal/repo/walk.go](../internal/repo/walk.go)) marked Deprecated but still called internally. 1.0 is the moment to remove or commit to keeping. I lean remove. |
| `CHANGELOG.md` currency | 🟡 | Check it's current through v0.8.8 and the demos/v0.1.0 release. |
| CI Docker-pulls-Postgres flake | 🟡 | Hit on the recent release push. Worth a retry-with-backoff or a registry mirror; "ship the flake" is not the right answer. |
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
- 🟡 **Aikit's 1.0.** We extracted aikit at v0.1.0 (ADR-034); ken
  1.0 likely wants aikit at a tagged 1.0 too, with its own
  documented public API.
- 🟡 **Performance expectations doc.** ADRs 026–030 set baselines.
  "What should ken feel like" — "indexing the Linux kernel takes
  ~X minutes, queries are sub-Y ms" — sets user expectations and
  catches regressions.

## Honest summary

ken is **1.0-ready on the retrieval axis.** It's **maybe 2 sprints
from 1.0** on the feature surface + polish axes. The biggest
single-PR wins are: Windows binary + search filters + `callers`
MCP tool + `ken status`. None of those is research; all are
well-scoped engineering.

## Maintenance

This doc is updated when items ship, get killed, or change scope.
The owning Claude Code instance is expected to keep it current — if
the table above lists a 🟡 that's actually done, the next session
that touches it should mark it 🟢 with a link to the commit / memo
that closed it.
