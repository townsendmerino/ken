# Roadmap — June 2026 external review

Source: full-repo review (code quality, maintainability, docs currency, security, competitive position), 2026-06-09. Companion to [road-to-1.0.md](road-to-1.0.md); this picks up where that tracker ends. Items are ordered by priority within each section. Effort: S (<1 h), M (half-day), L (multi-day).

**Status — verified post-v1.1.0 (2026-06-11, external re-review).** Independently re-checked against the tree at the v1.1.0 tag: the race fix (`defPatternMu` RWMutex + race-proven regression test), the clone byte cap + dial-time SSRF transport, the govulncheck workflow (9 CVEs caught and fixed on first run), CONTRIBUTING/SECURITY/Makefile, the README comparison table, the doc-drift guards, and the aikit v1.5 / go 1.26.4 pins are all as claimed. **Scoreboard: 25/28.** P0–P4 + P6 are now fully clear (#27 landed: a model-free bm25 concurrent query-shape stress test in the `-race` job). What remains is **entirely discretionary growth work**: #22 (head-to-head bench), #23 (lead with the two unique capabilities), #24 (ongoing watch).

**Close-out verdict:** the engineering roadmap is done — every correctness, hygiene, docs, security, and process item from the June 9 review is fixed, gated-out with data, or decided with rationale. ken at v1.1.0 has no known correctness bugs, a CVE gate, fuzzed untrusted-input surfaces, drift-guarded docs, and a concurrent query-pipeline race net. **#27 is now landed, so the engineering punch list is fully closed.** Disposition: archive this file (it's served its purpose) and graduate #22–#24 into a separate growth/marketing note — they're campaigns, not defects, and #24 never "completes."

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

### 18. Reduce personal-namespace single points of failure — **S (re-scoped)** — decision 2026-06-11: **STAY PERSONAL**
`github.com/townsendmerino/aikit` and `github.com/odvcencio/gotreesitter` are personal forks pinned by go.sum hashes. **Decision: keep ken under the `townsendmerino` personal namespace.** Rationale: the namespace is an *asset* for the project's stated consulting-credibility goal — every install snippet, `go install`, pkg.go.dev page, Homebrew tap, and benchmark writeup carries the author's name; attribution accrues to the personal profile graph, which an org transfer would dilute. Personal namespaces carry zero credibility penalty for serious tools (BurntSushi/ripgrep, junegunn/fzf, sharkdp/fd, jesseduffield/lazygit are all personal and enterprise-adopted). The supply-chain concern is real but it's an *adopter's* concern that scales with adoption ken doesn't yet have — and the risk is **bus factor, not namespace** (a one-member org is the same single point of failure with worse branding). What actually reassures adopters is already in place: tagged releases, go.sum pinning, CI, MIT, documented fork rationale.

- ✅ **(a) DONE** — gotreesitter fork rationale + upstream delta documented in [DEVELOPERS.md](../DEVELOPERS.md#personally-namespaced-dependencies): why the dep (pure-Go/no-cgo constraint), the one runtime delta ken carries (the `maxEnrichBytes` 64 KiB overflow guard — a guard, not a source fork; there's no `replace` directive), the pin/bump discipline, and the exit path.
- ✅ **(c) report FILED** — the overflow bug is filed upstream as [odvcencio/gotreesitter#110](https://github.com/odvcencio/gotreesitter/issues/110) (2026-06-11), with minimal repro. Remaining-optional: a PR making `buildResultFromNodes` iterative / depth-bounded so `Parse` returns an error instead of fatal-overflowing — but ken's contribution obligation was the report, which is done. When upstream fixes it, drop the 64 KiB guard.
- ❌ Drop **(b)** (org migration). Replace with: **stay personal; revisit only if `aikit` gains real third-party *library* adoption — and at that point prefer a vanity import path (`go.<domain>/ken`) over an org transfer.** Vanity paths cleanly decouple the module path from GitHub; org transfers are not the escape hatch anyway (Go module paths don't follow GitHub redirects cleanly for new versions). Not painted into a corner by waiting: GitHub repo transfers DO redirect existing clones + `go get`.

### 19. Resolve the module-path question before promoting the SDK story — **M** — ✅ **RESOLVED 2026-06-11 (by #18's decision)**
`mcp.Run` invites third parties to build binaries on `github.com/townsendmerino/ken`. Resolved: the module path **stays `github.com/townsendmerino/ken`** — no org migration to precede SDK adoption (see #18). The only future move that would matter is a vanity import path, which can be adopted later without breaking existing users (GitHub redirects clones + `go get`), and is only worth the ceremony if aikit's library adoption materializes. The SDK story can be promoted now under the personal path.

---

## P5 — Competitiveness & distribution

Context: technically ken's claims hold up (verbatim-port parity, quantified 0.012 NDCG gap to semble, measured token economics), but the category is crowded — semble (upstream), claude-context (Zilliz), CocoIndex, grepai, Codemogger, codebase-memory-mcp, coa-codesearch — several pitching the same "single binary, ~99% fewer tokens" line. A June 2026 web search for ken returns nothing. Distribution, not retrieval quality, is the binding constraint (consistent with road-to-1.0's own conclusion).

### 20. Get listed where agents' users look — **S/M** — ✅ **DONE 2026-06-12**
Submitted to **mcpservers.org, mcp.so, and Glama** — the three registries where the upstream (semble) actually surfaces, so this captures the parity visibility delta. Listing config is the bare stdio block `{"mcpServers":{"ken":{"command":"ken-mcp"}}}` (PATH name, no required args/env — the model auto-fetches).

**Deliberately deferred: the official MCP registry (registry.modelcontextprotocol.io).** For a Go binary it requires a new release artifact — an OCI/ghcr.io image (or per-platform MCPB bundles) maintained *solely* for the listing, since ken's actual install story is brew/scoop/`go install` and Docker is an awkward fit for local-repo indexing. Glama is already a superset of the official registry, so we get most of that propagation without it. Re-open trigger: if a future aggregator only ingests the official registry, wire a GoReleaser `dockers:` block (`FROM scratch` + the static binary + `LABEL io.modelcontextprotocol.server.name`) + a `server.json` (OCI variant) + `mcp-publisher`. Research + the exact recipe are in this session's notes.

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

### 27. Stress-test queries in the race job — **S** — ✅ **DONE**
`TestWatchedIndex_ConcurrentMixedQueries_DuringWrite` (`internal/search/watch_stress_test.go`): 12 reader goroutines × 80 iterations rotating through 7 mixed query shapes — symbol/camelCase (`ValidateToken`, `ParseConfig`), multi-word NL, and definition-shaped (`func ValidateToken`, `def parse_config`) — searching a model-free `ModeBM25` `WatchedIndex` while a writer rewrites files, so reads race the watcher's atomic snapshot swaps. Stays in CI's no-model `-race` job as-is. Confirmed the design honestly: the symbol/def **boost** branches (`isSymbolQuery`, `definitionPattern`) are hybrid-gated and don't fire in bm25 mode — so this nets the bm25-reachable pipeline (identifier-aware tokenizer across shapes → bm25 TopK → result assembly → swap), and the hybrid-only branches stay covered by #1's targeted test exactly as planned. A `hits` counter fails the test if every query goes empty, so it can't silently rot into a no-op. Race-clean + golangci-lint + gofmt all green.

### 28. godoc audit of the public surface — **M** — ✅ **DONE (376e609)**
Audited every exported symbol in `mcp/` + `mcp/db/` via a `go/doc` walk. `mcp/db` was already 100% documented; `mcp/` had exactly one gap (the `LogLevel` const block) — now documented. `TestPublicSurfaceDocumented` (in `mcp/`) keeps it complete in CI without depending on a golangci revive-rule config.
