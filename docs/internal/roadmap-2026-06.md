# Roadmap — June 2026 external review

Source: full-repo review (code quality, maintainability, docs currency, security, competitive position), 2026-06-09. Companion to [road-to-1.0.md](road-to-1.0.md); this picks up where that tracker ends. Items are ordered by priority within each section. Effort: S (<1 h), M (half-day), L (multi-day).

**Status — updated 2026-06-11 for v1.0.1.** The 1.0.1 docs sweep closed #3, #4, #5 (plus ARCHITECTURE.md shipped, which #3/#5 now anchor to). 1.0.1 also added deserializer fuzzing (see P3 note) and the aikit v1.4 SIMD bump (~3× faster hybrid p50, 4.58 ms → 1.56 ms — feeds #21/#24). **Top open item remains #1, the `defPatternCache` race** — verified still present at rerank.go:151–176 as of 1.0.1. Scoreboard: 3/28 done, 25 open.

---

## P0 — Correctness

### 1. Fix the `defPatternCache` data race — **S** — **OPEN (re-verified in 1.0.1)**
`internal/search/rerank.go:151` — package-level `map[string]defPattern{}` written in `definitionPattern` (rerank.go:176) and read in the same function, no mutex. The MCP SDK dispatches tool calls on separate goroutines, so concurrent `Search` calls race. Current `-race` CI doesn't catch it: `TestWatchedIndex_ConcurrentReads_DuringWrite` uses the query "alpha", which doesn't drive concurrent writes to the cache.

- Fix: `sync.RWMutex` around the map, or `sync.Map`.
- Add a regression test: concurrent searches with distinct definition-shaped queries (`func Foo`, `class Bar`, …) under `-race`.

### 2. Race-proof the rest of package-level state — **S**
Audit `internal/search` and `mcp` for other package-level mutable state reachable from the concurrent search path. The defPatternCache pattern (lazy memoization at package scope) is easy to repeat; add a lint note or a `// concurrency:` comment convention so the next cache gets a mutex from day one.

---

## P1 — Documentation currency

### 3. ~~Refresh DESIGN.md §1 project layout~~ — **DONE (1.0.1)**
Resolved via the "Historical (pre-ADR-034)" banner in DESIGN.md §1 pointing at [ARCHITECTURE.md](../../ARCHITECTURE.md) as the authoritative current-state map (committed in the 1.0.1 docs sweep). The residual guard against re-drift is #26.

### 4. ~~Update the DECISIONS.md index table~~ — **DONE (1.0.1)**
Index table now carries all 37 ADRs including 034–037. The table-count CI check to prevent recurrence is still open → folded into #26.

### 5. ~~Relabel or refresh DESIGN.md "Status"~~ — **DONE (1.0.1)**
§Status now opens with the 1.0.0-shipped line, is explicitly framed as recording the original build order, and defers current state to ARCHITECTURE.md + CHANGELOG.

### 6. Document the `go.work` / aikit workspace setup — **S**
`go.work` references a sibling `../aikit` checkout and is gitignored with no developer docs. Add a DEVELOPERS.md subsection: when you need the workspace, how to set it up, and that `GOWORK=off` (or no go.work) is the proxy-pinned production path.

### 7. Add CONTRIBUTING.md — **S**
DEVELOPERS.md §"Internals + contributing" has the substance; GitHub conventions (and tooling) look for the top-level file. A short CONTRIBUTING.md that links into DEVELOPERS.md is enough.

### 8. Add SECURITY.md — **S**
ken can be pointed at live databases (`KEN_DB_DSN`) and clones remote URLs. Publish a disclosure policy, restate the PII stance from db-indexing.md, and summarize the SSRF guard + its documented limits.

### 9. Track the `recently_changed` JSON gap — **S**
Documented as "JSON support is a follow-up" but tracked nowhere. Add it here / road-to-1.0 successor so it doesn't get lost.

---

## P2 — Code health & maintainability

### 10. Decompose `cmd/ken-mcp/main.go` `main()` — **M**
457 lines wiring every startup concern sequentially. Worst offender: the lazy reranker loader closure (~main.go:402–443), 40 lines capturing 7 enclosing variables. Extract a named type with a `Load() (search.Reranker, error)` method; pull mode-resolution and auto-fetch wiring into testable helpers alongside the existing `wireDBTier2`.

### 11. Untrack the stale `outputs/` files — **S**
8 markdown files under gitignored `outputs/` (`perf-startup-m*.md`, `stage8-*.md`) were committed before the ignore rule. `git rm --cached` them; if the perf baselines are load-bearing references (DESIGN/BENCH cite them), move those into `docs/internal/perf/` instead and keep them tracked deliberately.

### 12. Document the `NormalizePath` trust boundary — **S**
`internal/structural/lookups.go:429` — `filepath.Clean` doesn't block `../` traversal. Harmless today (the path is only an in-memory map key, never opened), but the name implies sanitization it doesn't provide. Add a comment stating the boundary ("never used for filesystem access; if that changes, add containment checks") or rename, so a future change doesn't introduce a traversal bug.

### 13. Name the candidate-multiplier constant — **S**
`hybrid.go:43` `candidateCount := topK * 5` — the one un-named constant in the retrieval path. Trivial, but the rest of the pipeline sets the standard.

### 14. Working-tree clutter — **S**
37 GB `bench_out/` plus 38/56 MB binaries at repo root on the dev machine. All correctly gitignored — purely local hygiene, but worth a `make clean` target so the cost of cleanup is zero.

---

## P3 — Security hardening

> 1.0.1 progress: fuzz coverage landed for the two untrusted-input binary deserializers (`FuzzDeserializeIndex` for KEN1 — auto-loaded from shallow-cloned remote repos — and `FuzzDecodeRerankCache`; 2.6M execs, zero crashers), and aikit v1.4 brought fuzz-fixed `embed` tensor parsing + `bm25`/`chunk` pipeline fuzzing. This materially de-risks the hostile-repo input surface; #15–#17 below remain open.

### 15. Cap clone size — **M**
Tracked as L3 in clone.go: no max-bytes cap, so a hostile git server can serve an unbounded pack (current mitigation: ctx timeout). Add a byte-count limit on the clone stream or a post-clone size check + cleanup.

### 16. DNS-rebinding TOCTOU on the SSRF guard — **M**
Acknowledged in clone.go: the guard resolves DNS, then go-git resolves again. Closing it fully means dialing through a pinned-IP transport. Low likelihood / contained blast radius; do it when touching clone.go for #15.

### 17. Periodic `govulncheck` — **S**
Add a `govulncheck ./...` CI job (or scheduled workflow). go-git in particular has a CVE history; the SSRF guard covers one attack shape, not all of them.

---

## P4 — Supply chain & ownership

### 18. Reduce personal-namespace single points of failure — **M/L**
`github.com/townsendmerino/aikit` and `github.com/odvcencio/gotreesitter` are personal forks pinned only by go.sum hashes. Options, cheapest first: (a) document the fork rationale + upstream delta for gotreesitter (the overflow-fix memo exists — surface it in DEVELOPERS.md); (b) move aikit and ken under a GitHub org namespace before adoption grows, since a module-path migration gets more painful with every downstream user; (c) offer the gotreesitter fix upstream so the fork can eventually retire.

### 19. Resolve the module-path question before promoting the SDK story — **M**
`mcp.Run` invites third parties to build binaries on `github.com/townsendmerino/ken`. If an org migration (#18b) is ever going to happen, it must precede SDK adoption.

---

## P5 — Competitiveness & distribution

Context: technically ken's claims hold up (verbatim-port parity, quantified 0.012 NDCG gap to semble, measured token economics), but the category is crowded — semble (upstream), claude-context (Zilliz), CocoIndex, grepai, Codemogger, codebase-memory-mcp, coa-codesearch — several pitching the same "single binary, ~99% fewer tokens" line. A June 2026 web search for ken returns nothing. Distribution, not retrieval quality, is the binding constraint (consistent with road-to-1.0's own conclusion).

### 20. Get listed where agents' users look — **S/M**
Submit to mcp.so, mcpservers.org, lobehub, awesome-mcp-servers lists, and the registries that surface semble today. This is hours of work for the single largest visibility delta available.

### 21. Publish a comparison table vs the actual competitive set — **M**
README compares only against semble. Add grepai (closest analog: Go, single binary, watcher) and claude-context (most visible) on: index time, query latency, recall/NDCG where measurable, token cost, deps required (Docker/API keys/vector DB vs none), license. ken's no-external-services + measured-benchmarks position is strong; show it.

### 22. Run the NDCG/token harness against one or two competitors — **L**
The bench harness already exists. Even a single reproducible head-to-head (e.g., vs grepai on the 1,251-query set) would be the only measured comparison in the category and is the kind of artifact that travels (HN/blog/release notes).

### 23. Lean into the two unique capabilities — **M**
Nobody in the surveyed set offers (a) the `mcp.Run` embedded-corpus story — docs + model + index in one static MCP binary — or (b) DB-schema-alongside-code ranked retrieval. The demos exist (k8s, PostgreSQL binaries + audit writeup); package them as the headline rather than the footnote. A "ship your project's docs as an MCP binary in 20 lines" blog post / template repo is the differentiated growth lever.

### 24. Watch the semble cold-start gap — **ongoing**
The 25–50× cold-start advantage erodes if semble ships a compiled/standalone distribution. The durable moats are the Go SDK surface, DB indexing, and Windows support — invest there, not in latency marketing. *(1.0.1 note: the aikit v1.4 SIMD bump — hybrid `search` p50 4.58 ms → 1.56 ms, recall re-verified identical at NL 0.969 / symbol 0.995 — widens the query-latency story too; fold these numbers into the #21 comparison table.)*

### 25. Per-language treesitter routing — **L, data-gated**
BENCH shows treesitter wins for Kotlin/Zig/TS/Java/PHP and loses for Python/C/Rust/Lua/Scala (net −0.004, hence opt-in per ADR-011). A per-extension chunker routing table could capture the wins without the losses. Only worth it if the per-language deltas are outside noise; gate on a re-run of the harness.

---

## P6 — Process

### 26. Doc-drift CI checks — **S**
The two staleness findings (#3, #4) share a root cause: hand-maintained indexes/diagrams with no check. Cheap guards: ADR table-count check, a link checker over docs/, and a grep that DESIGN.md's layout block mentions no `internal/` package that doesn't exist.

### 27. Stress-test queries in the race job — **S**
Extend the concurrent-search tests to cover symbol-shaped and definition-shaped queries concurrently (the gap that hid #1).

### 28. godoc audit of the public surface — **M**
DEVELOPERS.md describes the 1.0-stable API in prose; verify every exported symbol in `mcp/` and `mcp/db/` carries a doc comment (`golangci-lint` `revive` exported-comment rule, or `gopls check`), since pkg.go.dev is part of the SDK pitch.
