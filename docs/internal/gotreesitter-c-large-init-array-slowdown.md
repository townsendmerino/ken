# Upstream issue — gotreesitter `c` grammar slow on large designated-initializer arrays

**Status: OPEN upstream.** Filed 2026-09-07 as
[odvcencio/gotreesitter#1100](https://github.com/odvcencio/gotreesitter/issues/1100)
against **[odvcencio/gotreesitter](https://github.com/odvcencio/gotreesitter)**;
surfaced by the dense-retriever kernel-scale spot-check
([`dense-retriever-adoption-2026-09.md`](dense-retriever-adoption-2026-09.md))
— that work needed a real large corpus and picked a sparse Linux kernel
checkout; this finding is unrelated to dense retrievers, just discovered
along the way. ken's mitigation for it is tracked here, separately.

## What's wrong

On `v0.51.0` the `c` grammar (used for `.c`/`.h` Arm B structural
enrichment) parses some real driver-style C files far slower than their
size predicts — not a crash, every case completes with
`ParseStopAccepted`, just slowly. A representative file
(`drivers/clk/qcom/gcc-mdm9607.c`, torvalds/linux@v6.6, 43 KB) took
**~4.0–4.9s** across repeated runs, where healthy files of similar size
typically parse in tens of milliseconds.

This is the same failure *class* as
[gotreesitter#972](https://github.com/odvcencio/gotreesitter/issues/972)
(`c_sharp`'s collection-initializer slowdown, fixed for ken by parking
`.cs` — see [`DESIGN.md` §10](../DESIGN.md#10-risk-register)) — a
homogeneous-shaped initializer list costing far more than its size
suggests — on a different grammar. Not filed as a duplicate since the
trigger construct and grammar differ and the root cause is unconfirmed.

**Breadth (why this isn't a one-off edge case):** a survey of a real 37,775-
file `.c`/`.h` corpus (`arch/x86 drivers fs kernel mm net sound` @ v6.6,
584,019 downstream chunks) found **504 files (1.33%) exceeding a 3-second
parse budget**, spanning many unrelated subsystems that share only the
"large static const struct/array table" shape common in driver code (clock
drivers, GPU register tables, device-ID/routing tables, radio/PHY tables).
None confirmed to hang indefinitely, but the aggregate cost is real — the
survey run spent ~7 minutes past an unenriched build entirely inside this
slowdown.

**Root cause not confirmed.** Bisecting the real file by line-count prefix
shows a sharp ramp through the pathological array, but every truncated
prefix is syntactically incomplete C — the exact confound
[gotreesitter#972](https://github.com/odvcencio/gotreesitter/issues/972)'s
own writeup flagged for C# ("truncated prefixes are invalid ... what you
measure is error-recovery cost, not this bug"). A synthetic isolated
attempt at the same construct shape (`[IDX_n] = &items[n].field,`, up to
200 entries) did **not** reproduce any slowdown, so the trigger isn't
simply "a large designated-initializer array" in isolation. Full detail
(including the "what I tried and why it's inconclusive" section) is in the
filed issue.

## What we did downstream — protect all enrichment languages, not just this one

The one hard lesson from both this and #972: ken had **zero default bound**
on structural-enrichment parse cost outside `ken-mcp` (which sets 500ms —
`cmd/ken-mcp/enrich_budget.go`). `ken index` / `ken search` / `ken bench` /
`ken perf` had none, so *any* of the 15 enrichment languages could hit a
latent grammar performance bug — known (this, #972) or undiscovered — and
degrade a build by many minutes with the operator having no idea why (the
C# case was literally reported as "an apparent hang," v1.5.1's fix title).

**Shipped:** `cmd/ken/enrich_budget.go` — `setupEnrichBudget()`, called from
`main()` for `index`/`search`/`bench`/`perf`, defaults
`KEN_ENRICH_FILE_BUDGET_MS` to **2000ms** when the operator hasn't set it
(4× ken-mcp's 500ms — these commands tolerate more per-file latency than an
interactive server, but shouldn't be unbounded), and routes budget-exceeded
skips to stderr so a pathological file reads as "one file's enrichment was
skipped," not a hang. This protects **every** registered enrichment
language uniformly — the budget mechanism (`internal/structural`'s
`extractGuarded` / `envParseBudgetMicros`) is grammar-agnostic.

**Deliberately NOT applied to `ken build-index`.** That command's entire
contract is byte-identical, machine-load-independent output ([ADR-040](DECISIONS.md#adr-040-two-tier-treesitter-parse-timeout--disabled-for-reproducible-cli-builds-bounded-for-the-live-server)),
and a wall-clock budget is exactly the load-dependence that ADR closed for
the chunker. `build-index` stays unbounded — the residual risk there is the
same one already documented for Swift/C# in `DESIGN.md` §10, now with fresh
evidence it isn't rare. See [ADR-044](DECISIONS.md#adr-044-cli-default-enrichment-parse-budget-build-index-excepted)
for the full decision + why this is an acceptable determinism trade for
`index`/`search`/`bench`/`perf` (chunk-*text* varies with a skip, not chunk
*count*/structure — a narrower non-determinism than ADR-040 guards against)
while `build-index` keeps the stronger guarantee.

**What this does NOT fix:** the underlying gotreesitter slowness itself
(upstream's problem, tracked in #1100), and `ken build-index` / library
callers that construct `FSOptions{}` directly (e.g. `BuildAndSerializeIndex`,
used by `mcp.Run`'s embedded-corpus build) remain unbounded by design.

**Feature request filed alongside the bug:** gotreesitter's `Parser`
already computes deterministic `IterationLimit`/`NodeLimit`/`StackDepthLimit`
internally (auto-scaled by source length), just not exposed as a
`ParserPool` option. If exposed, ken could apply a *deterministic* cap
(same stop reason for the same input on any machine) instead of a
wall-clock one — closing the residual gap for `build-index` too, without
trading away its byte-identical contract. Re-open this doc if that lands
upstream.
