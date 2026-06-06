# Hybrid-by-default onboarding — plan

**Status:** proposed 2026-06-05. Scoping decisions made by the owner (see
"Decisions" below). Not yet started.

## Why

The recall-decomposition run (BENCH.md "Default-mode (hybrid) recall";
`internal/search/recall_decomp_test.go`) established that ken's default
hybrid mode measures **~0.97 recall@10** (0.967 NL / 0.995 symbol), while
the widely-quoted **0.82–0.91 is the BM25-only fallback** — the mode ken
runs in when no embedding model is installed. The decomposition also
showed the residual hybrid miss is mostly ranking loss (~2 pp,
rerank-territory), not a retrieval ceiling.

Conclusion: **the single biggest retrieval-quality lever a user controls
is having the model installed.** And today the only users silently stuck
on the 0.84 path are **ken-mcp users with no model** — `cmd/ken-mcp/main.go`
downgrades hybrid→bm25 and logs a warning to **stderr, which agents and
their users almost never read** (`cmd/ken-mcp/main.go:378`). The CLI, by
contrast, errors loudly with a fix (`internal/search/index.go:278`), so CLI
users already know. So "hybrid-by-default onboarding" = **close the
ken-mcp silent-downgrade gap + make the model trivial to obtain.**

Nothing is missing technically — `internal/modelfetch.Fetch` is pure-Go,
fetches potion-code-16M (~60 MB) to `~/.ken/model`, atomic, no Python, and
already exposes `BaseURL`/`Client`/`Progress` injection points for
offline tests. This is a behavior + distribution change, not new infra.

## Decisions (owner, 2026-06-05)

- **Auto-fetch behavior: background fetch + hot-swap.** When ken-mcp
  starts in a model-needing mode with no model, serve BM25 immediately,
  fetch the model in the background, and hot-swap to hybrid when it lands.
- **Distribution: all four channels** — Homebrew tap, bundled
  (model-embedded) binary asset, Scoop manifest, and a Windows build
  (this re-opens the road-to-1.0 "Windows deferred-until-pressure" item by
  explicit owner choice).

## Architectural note — the hot-swap is already supported

`search.WatchedIndex` holds its live snapshot in an
`atomic.Pointer[Index]` (`internal/search/watch.go:56`); every query does
one atomic `Load`. The retrieval *mode* is a property of the `*Index`. So
swapping a freshly-built **hybrid** `*Index` into that pointer upgrades
every subsequent query atomically — the exact mechanism the watcher
already uses on file changes, triggered by "model arrived" instead of
"file changed." No lock-step with in-flight readers; no new concurrency
model. The only addition is a way to (a) rebuild a watched index in a new
mode with a model and (b) publish it into the existing pointer.

## ADR-worthy decision to capture

**ken-mcp auto-fetches the embedding model from HuggingFace on first run,
by default.** This is a consequential default (network egress to a
third-party CDN at startup, supply-chain surface, offline/air-gapped
behavior) and belongs in DECISIONS.md as its own ADR, with:
- default ON, opt-out `KEN_MCP_AUTO_FETCH=0` (and never fetch when the
  resolved mode is an explicit `bm25`, or when `KEN_MCP_MODEL_DIR` is set
  but unreachable — that's a user-pinned path, not a "no model" state);
- one attempt, no retry-storm; on failure stay BM25 with a clear stderr
  line naming the opt-out and the manual `ken download-model`;
- all progress/logs to **stderr only** (the JSON-RPC stdout contract);
- the CLI keeps its current loud-error behavior (no surprise egress from a
  foreground command the user is watching) — auto-fetch is a ken-mcp-only
  default because that's the silent-downgrade surface. (CLI may grow an
  opt-in `--auto-fetch` later; not in scope.)

## Workstreams (ordered)

### A — Background auto-fetch + hot-swap  ← the recall lever, do first

`cmd/ken-mcp` + a small `search.WatchedIndex` upgrade method.

1. New env `KEN_MCP_AUTO_FETCH` (default on) via the existing
   `envEnum`/`envInt` helpers in `cmd/ken-mcp/env.go`.
2. At the current downgrade site (`main.go:378`): if mode needs a model,
   model is absent, and auto-fetch is enabled and `KEN_MCP_MODEL_DIR`
   isn't a user-pinned-but-broken path — start serving BM25 (current
   path) **and** spawn a background goroutine: `modelfetch.Fetch` to
   `~/.ken/model` (Progress → the stderr logger).
3. On success: build a hybrid `*Index` over the same corpus and publish it
   into each served `WatchedIndex` (new `(*WatchedIndex).UpgradeToHybrid`
   or a generic re-mode publish). Re-attach the lazy reranker if
   configured. Log `upgraded bm25 → hybrid`.
4. On failure (offline / HF down / write-perm): stay BM25, one clear
   stderr line (opt-out + manual command). No retry loop.
5. Contracts/tests:
   - extend `TestBinary_StdoutIsCleanJSONRPC` with an auto-fetch variant
     driven by `modelfetch.Options.BaseURL` pointed at a local httptest
     server (no network) — assert stdout stays pure JSON-RPC through the
     fetch + swap;
   - unit test the mode transition (bm25 → hybrid) with the injected
     fetcher; assert opt-out respected; assert fetch-failure stays bm25;
   - determinism: a query mid-swap returns a consistent snapshot (atomic
     pointer guarantees this — assert no torn results).

Note: post-fetch the hybrid build re-embeds the corpus, same cost as
starting in hybrid — but it's in the background while BM25 serves, so the
agent is never blocked.

### B — Loud, actionable degraded-state notice  ← cheap; covers auto-fetch-off/failed

`cmd/ken-mcp` + `mcp/`. When still serving BM25 (auto-fetch off or
failed), surface the degraded state where the agent will actually see it —
the MCP server `instructions` / `initialize` response and/or a one-line
prefix on tool-result headers (not every result — avoid spam; the server
instructions field is the cleanest single surface). Reuse `internal/status`
data. Message: BM25-only, recall ~0.84 vs ~0.97 hybrid, run
`ken download-model` (or note it's auto-fetching if A is mid-flight).

### C — Distribution (release infra; parallel to A/B)

- **C1 Windows build.** Add `windows/amd64`+`arm64` to `.goreleaser.yml`
  (re-opening the deferred item). Smoke-check CRLF / path-separator /
  MCP-stdio behavior. Unblocks C3.
- **C2 Homebrew tap.** GoReleaser `brews:` block → a `townsendmerino/
  homebrew-ken` tap. With Workstream A shipped, ken-mcp needs no model
  caveat (it self-fetches); the formula caveat can simply mention
  `ken download-model` for CLI users who want it pre-seeded.
- **C3 Scoop manifest.** GoReleaser `scoops:` block → a scoop bucket.
  Depends on C1.
- **C4 Bundled (model-embedded) binary asset.** A model-embedded build
  (the existing `//go:embed` + `mcp.Run` pattern, cf. `cmd/ken-mcp-docs`)
  shipped as an extra release artifact (~74 MB) for offline / zero-config
  / locked-down installs where auto-fetch is unwanted. Generalize the
  docs-demo embed pattern into a reusable `ken-mcp-bundled` target.

## Sequencing

A → B (B is cheap and pairs with A for the auto-fetch-off case). C runs in
parallel: C1 unblocks C3; C2 and C4 are independent. A is the only piece
that moves the recall number; C is reach/footprint. Land A+B first.

## Out of scope

Per-language α tuning, alternative rerank models, the rerank/ColBERT
2-point polish (road-to-1.0 §1 — all 0.005-level). CLI auto-fetch (keep
the loud error). Aikit changes.
