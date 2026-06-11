# Roadmap — June 2026 external review

Source: full-repo review (code quality, maintainability, docs currency, security, competitive position), 2026-06-09. Companion to [road-to-1.0.md](road-to-1.0.md); this picks up where that tracker ends. Items are ordered by priority within each section. Effort: S (<1 h), M (half-day), L (multi-day).

**Status — updated 2026-06-11 for v1.0.1.** The 1.0.1 docs sweep closed #3, #4, #5 (plus ARCHITECTURE.md shipped, which #3/#5 now anchor to). 1.0.1 also added deserializer fuzzing (see P3 note) and the aikit v1.4 SIMD bump (~3× faster hybrid p50, 4.58 ms → 1.56 ms — feeds #21/#24). **#1, the `defPatternCache` race, is now fixed (1381c51)** with an `RWMutex` + a race-proven regression test. Plus aikit bumped to v1.5.0 with the int8 reranker now default (727c145). Scoreboard: 21/28 resolved (incl. #2, #6–#17, #21, #26, #28; #25 gated out). P2 code-health, P3 security, and the doc-drift/godoc guards (#26/#28) all clear. Remaining: P4 ownership (#18/#19, M/L — org-namespace question), P5 distribution (#20 registry listings), and the strategic items (#22–#24, #27).

---

## P0 — Correctness

### 1. Fix the `defPatternCache` data race — **S** — ✅ **DONE (1381c51)**
`internal/search/rerank.go:151` — package-level `map[string]defPattern{}` written in `definitionPattern` (rerank.go:176) and read in the same function, no mutex. The MCP SDK dispatches tool calls on separate goroutines, so concurrent `Search` calls race. Current `-race` CI didn't catch it: `TestWatchedIndex_ConcurrentReads_DuringWrite` uses the query "alpha", which doesn't drive concurrent writes to the cache.

- ✅ Fixed with `sync.RWMutex` (RLock hit path, Lock only the store; regex compile stays outside the lock).
- ✅ Regression test `TestDefPatternCache_ConcurrentDistinctSymbols_NoRace` — 64 distinct symbols × 8 goroutines hammering `chunkDefinesSymbol` (drives `definitionPattern` directly: the boost path is hybrid-only/model-gated, so a Search-based test would `t.Skip` in CI's no-model `-race` job). Verified it trips the detector with the locks removed.

### 2. Race-proof the rest of package-level state — **S** — ✅ **DONE (a1c6525)**
Audited `internal/search`, `mcp`, and `internal/structural`. Result: `defPatternCache` (fixed in #1) was the **only** unguarded runtime-written package var — everything else is immutable (`regexp`/`errors`/read-only static slices+maps) or already correct (`parserPools` is a `sync.Map`; WatchedIndex/Cache/NeuralReranker mutexes are instance-level). Established the `// concurrency:` tag convention (CONTRIBUTING.md → Concurrency) + annotated the shared package vars so a future runtime write is an obvious red flag.

---

## P1 — Documentation currency

### 3. ~~Refresh DESIGN.md §1 project layout~~ — **DONE (1.0.1)**
Resolved via the "Historical (pre-ADR-034)" banner in DESIGN.md §1 pointing at [ARCHITECTURE.md](../../ARCHITECTURE.md) as the authoritative current-state map (committed in the 1.0.1 docs sweep). The residual guard against re-drift is #26.

### 4. ~~Update the DECISIONS.md index table~~ — **DONE (1.0.1)**
Index table now carries all 37 ADRs including 034–037. The table-count CI check to prevent recurrence is still open → folded into #26.

### 5. ~~Relabel or refresh DESIGN.md "Status"~~ — **DONE (1.0.1)**
§Status now opens with the 1.0.0-shipped line, is explicitly framed as recording the original build order, and defers current state to ARCHITECTURE.md + CHANGELOG.

### 6. Document the `go.work` / aikit workspace setup — **S** — ✅ **DONE (next commit)**
Added a "Local aikit development (`go.work`)" subsection to DEVELOPERS.md (under aikit packages): when you need the workspace, `go work init` / `go work use` setup, the gitignored-per-machine note, the cross-repo change workflow (edit local → tag aikit → `go get @tag` → re-validate), and that `GOWORK=off` is the proxy-pinned production path CI runs. Also fixed the stale `require aikit v1.4.0` → `v1.5.0` in that section.

### 7. Add CONTRIBUTING.md — **S** — ✅ **DONE (981f1c3)**
Short top-level `CONTRIBUTING.md`: setup, the CI bar, the gofmt-clean + reproducible-claim disciplines, links into DEVELOPERS.md / ARCHITECTURE.md / add-a-language.md / aikit.

### 8. Add SECURITY.md — **S** — ✅ **DONE (981f1c3)**
Top-level `SECURITY.md`: private-disclosure policy (GitHub advisories), the `KEN_DB_DSN` PII stance (dev-only, schema-only default; ADR-017), and the remote-clone SSRF guard + its documented limits (DNS-rebinding TOCTOU, no size cap) + untrusted-index DoS-ceiling framing.

### 9. Track the `recently_changed` JSON gap — **S** — ✅ **DONE (420be3d) — closed, not just tracked**
Rather than track the follow-up, closed it: `recently_changed` now supports `output: "json"` (typed `RecentlyChangedResponse`, built from the same rows as the markdown render). All nine MCP tools now accept `output: "json"`. Test + DEVELOPERS.md + CHANGELOG updated.

---

## P2 — Code health & maintainability

### 10. Decompose `cmd/ken-mcp/main.go` `main()` — **M** — ✅ **DONE (a8f66e4)**
Was 457 lines; now 294. Extracted `rerankerLoader` (named type, `Load() (search.Reranker, error)` method — the cache scope/dim it records are fields the shutdown path reads), `setupReranker` (wraps the whole `KEN_MCP_RERANK*` block, mirrors `wireDBTier2`), `resolveStartupMode` (ParseMode + model-missing downgrade + auto-fetch-dest, pure logic), and `parseRerankAdaptive` / `resolveRerankCachePath`. `startup_test.go` covers the decision logic; behavior byte-for-byte preserved (full suite incl. stdout-contract green).

### 11. Untrack the stale `outputs/` files — **S** — ✅ **DONE (1c00d22)**
All 8 turned out to be load-bearing (PERF-expectations cites `perf-startup-m0-baselines` as its number source; DEVELOPERS/road-to-1.0/DECISIONS/add-a-language link them), so rather than untrack-and-dangle they were `git mv`'d into a deliberately-tracked `docs/internal/results/` (4 perf-campaign baselines + 4 Stage 8 gate memos) with all citations rewired + a README. `outputs/` stays gitignored for live scratch. (The ~15 OTHER `outputs/` files docs cite were never tracked — pre-existing local-only pattern, out of scope.)

### 12. Document the `NormalizePath` trust boundary — **S** — ✅ **DONE (6c03013)**
Added a "TRUST BOUNDARY" doc comment: `filepath.Clean` doesn't strip `../`; harmless because all callers (`Outline` / `SymbolsInPath`) use the result only as an in-memory lookup key, never to open a file; a future filesystem-access caller must add containment checks first. Comment (not rename) — the name is fair, it was only the unstated guarantee that was the gap.

### 13. Name the candidate-multiplier constant — **S** — ✅ **DONE (d09acaf)**
`topK * 5` → `topK * candidateOverfetch` (named const with a doc comment on the per-arm over-fetch rationale). Pure rename, no behavior change.

### 14. Working-tree clutter — **S** — ✅ **DONE (5a776dd)**
Added a `Makefile`: `clean` (build products), `clean-bench` (the heavy 37 GB `bench_out/` + bench results, split out so a routine clean doesn't nuke bench data), `clean-all`, plus `build`/`test`/`vet`/`fmt`/`check` + self-documenting `help`. CONTRIBUTING.md points at it.

---

## P3 — Security hardening

> 1.0.1 progress: fuzz coverage landed for the two untrusted-input binary deserializers (`FuzzDeserializeIndex` for KEN1 — auto-loaded from shallow-cloned remote repos — and `FuzzDecodeRerankCache`; 2.6M execs, zero crashers), and aikit v1.4 brought fuzz-fixed `embed` tensor parsing + `bm25`/`chunk` pipeline fuzzing. This materially de-risks the hostile-repo input surface. **#15–#17 are now all done (2026-06-11):** clone byte cap + dial-time DNS-rebinding-safe SSRF guard (#15/#16, d186d60) and a govulncheck CI workflow that caught + fixed 9 reachable CVEs (#17, 8a2f22a).

### 15. Cap clone size — **M** — ✅ **DONE (d186d60)**
go-git's clone now dials through a guarded transport; each connection is wrapped in `cappedConn`, a per-clone byte budget (`KEN_MAX_CLONE_BYTES`, default 2 GiB). A hostile unbounded pack aborts with `ErrCloneTooLarge` + the partial dir is cleaned up. Done together with #16 (same transport).

### 16. DNS-rebinding TOCTOU on the SSRF guard — **M** — ✅ **DONE (d186d60)**
The guarded transport's `DialContext` re-validates the resolved IP at connect time and dials it literally (TLS still verifies the hostname via SNI) — closing the rebinding window between the pre-flight check and git's own lookup, and covering redirects to internal hosts. Pre-flight check kept as the fast early rejection. Tests cover dial-time rejection of private literals.

### 17. Periodic `govulncheck` — **S** — ✅ **DONE (8a2f22a)**
Added `.github/workflows/govulncheck.yml` (weekly schedule + PR/push to main + manual). The first run was NOT clean — it caught **9 reachable CVEs**, all fixed in the same commit: `x/crypto` v0.50.0→v0.52.0 (7 ssh-transport CVEs via go-git) + toolchain 1.26.3→1.26.4 (2 stdlib: crypto/x509, net/textproto). `govulncheck ./...` now reports 0 affecting vulns. (Like the Windows CI smoke job, it earned its keep on the first run.)

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

### 21. Publish a comparison table vs the actual competitive set — **M** — ✅ **DONE (09a8452)**
Added a "Compared to other agent code-search tools" section to the README: ken vs grepai (Go single binary, but Ollama-dependent embeddings) vs claude-context (Zilliz — Milvus/vector-DB + embedding-provider backed) on runtime / embeddings / **external services needed** / retrieval / recall+NDCG / token savings / speed / languages / license. Framed honestly — ken's numbers are reproducible (BENCH.md), competitor perf is vendor-claimed or "not published"; the verifiable axes are architecture/deps/license. ken's zero-external-services + measured-benchmarks position carries it. Facts checked against each project's GitHub (June 2026).

### 22. Run the NDCG/token harness against one or two competitors — **L**
The bench harness already exists. Even a single reproducible head-to-head (e.g., vs grepai on the 1,251-query set) would be the only measured comparison in the category and is the kind of artifact that travels (HN/blog/release notes).

### 23. Lean into the two unique capabilities — **M**
Nobody in the surveyed set offers (a) the `mcp.Run` embedded-corpus story — docs + model + index in one static MCP binary — or (b) DB-schema-alongside-code ranked retrieval. The demos exist (k8s, PostgreSQL binaries + audit writeup); package them as the headline rather than the footnote. A "ship your project's docs as an MCP binary in 20 lines" blog post / template repo is the differentiated growth lever.

### 24. Watch the semble cold-start gap — **ongoing**
The 25–50× cold-start advantage erodes if semble ships a compiled/standalone distribution. The durable moats are the Go SDK surface, DB indexing, and Windows support — invest there, not in latency marketing. *(1.0.1 note: the aikit v1.4 SIMD bump — hybrid `search` p50 4.58 ms → 1.56 ms, recall re-verified identical at NL 0.969 / symbol 0.995 — widens the query-latency story too; fold these numbers into the #21 comparison table.)*

### 25. Per-language treesitter routing — **L, data-gated** — ❌ **GATE FAILED — don't build (evaluated 2026-06-11)**
The gate already has data: BENCH.md's v0.2.0 per-language measurement IS the harness output, and it characterizes the deltas as noise ("within bench noise", "directionally mixed … reshuffling not systematic improvement"). Statistical structure confirms it: the corpus is **3 repos / ~60 queries per language**, so the paired per-language SE ≈ `std(per-query Δ)/√60 ≈ 0.023`; the largest observed delta (|Δ|=0.022, Lua/Scala) is ~1 SE — **not significant** — and within-language repo-level deltas **flip sign** (nlohmann-json +0.039 vs aiohttp −0.018). A per-extension routing table would fit per-language sampling noise (≤0.01-scale, unstable) while adding a dispatch layer. **Decision: don't build; keep treesitter opt-in (ADR-011).** A fresh full re-run wouldn't move the small-N noise floor; it needs `pip install -e <semble>` (pulls the model2vec stack) + ~45 min and would re-confirm the documented null. Re-open only if the corpus grows to many repos/language or a paired per-query test shows stable, >2·SE deltas.

---

## P6 — Process

### 26. Doc-drift CI checks — **S** — ✅ **DONE (42bf195)**
Two guards in `internal/buildchecks` (ride the existing `go test ./...` CI job): `TestADRIndexMatchesSections` (DECISIONS.md index ↔ sections — the #4 drift) and `TestDocLinksResolve` (every relative markdown link resolves — the #3 drift; skips external/anchor, gitignored `outputs/`, the `cmd/ken-mcp-docs` embedded copy, the append-only CHANGELOG, and the Pages index). Caught + fixed 4 real broken links on the first run. (The DESIGN §1 internal/-grep was omitted: §1 is historical-banner'd on purpose.)

### 27. Stress-test queries in the race job — **S**
Extend the concurrent-search tests to cover symbol-shaped and definition-shaped queries concurrently (the gap that hid #1).

### 28. godoc audit of the public surface — **M** — ✅ **DONE (376e609)**
Audited every exported symbol in `mcp/` + `mcp/db/` via a `go/doc` walk. `mcp/db` was already 100% documented; `mcp/` had exactly one gap (the `LogLevel` const block) — now documented. `TestPublicSurfaceDocumented` (in `mcp/`) keeps it complete in CI without depending on a golangci revive-rule config.
