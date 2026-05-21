# Changelog

All notable changes to ken are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
ken is pre-1.0 (`v0.x.y`); breaking changes can land in minor versions
until v1.0.0. Each release tag has a corresponding GitHub release page
with pre-built binaries.

## [Unreleased]

(no changes yet)

## [0.4.0] — 2026-05-21

The release that closes the gap between ken's pure-Go claim and its
actual end-user experience. A user can now `go install` ken, run
`ken download-model`, and reach hybrid retrieval without ever
touching a Python interpreter — the "single static binary"
positioning is finally 100% true end-to-end, not 80%-with-Python-
bootstrap. Agent-side routing also gets honest: the MCP tool
instructions now name the recall/token tradeoff explicitly so AI
agents know to fall back to grep for exhaustive enumeration. Plus
incremental-indexing memory hygiene (tombstone compaction) and the
disciplined close of ADR-013 after the validation precondition
caught a misread of the CSN-Python benchmark.

### Added

- **`ken download-model`** — pure-Go fetch of the three Model2Vec files
  (`model.safetensors`, `tokenizer.json`, `config.json`) directly from
  HuggingFace's CDN. Closes the gap between ken's "single static binary"
  claim and the previous Quickstart's `huggingface-cli` dependency.
  Defaults to `minishlab/potion-code-16M` into `~/.ken/model`; pass
  `--model ORG/NAME` / `--to DIR` to override; `--force` re-downloads
  files already on disk. Public models only — gated/private models
  still need `huggingface-cli`. Atomic-rename writes so a cancelled
  download leaves no partial files.

### Changed

- **Model directory resolution.** The CLI now searches a priority list
  for the Model2Vec snapshot — `--model <DIR>` → `$KEN_MODEL_DIR` →
  `~/.ken/model` (canonical end-user location) → `./testdata/model`
  (repo-developer fallback) — instead of defaulting to a single
  repo-relative `testdata/model` path. A user who `go install`ed ken
  and ran `ken download-model` now gets working hybrid/semantic search
  out of the box; repo developers can either follow the same public
  convention or pass `--model testdata/model` for local-only iteration.
  The "model not found" error continues to point at whichever path
  was tried and includes the exact `ken download-model --to <path>`
  command to resolve it.

- **ken-mcp tool instructions surface the recall/token tradeoff.** The
  `Instructions` string every MCP agent reads on startup gained a
  sentence directing exhaustive-enumeration tasks (every callsite,
  pre-rename audits) at grep instead of ken — ken caps at ~82–91%
  recall at K=10 and isn't built for that. Mirrors the "honest
  tradeoff" framing already in `README.md`, grounded in the
  token-budget bench's measured ceilings. Closes the gap where agents
  were told "prefer ken over grep" without naming the exception.

- **Tombstone compaction.** Watched indexes now drop tombstoned chunks
  during every debounced snapshot rebuild instead of accumulating
  them. Memory plateaus at live-chunk working-set size; multi-day
  agent sessions on actively-edited corpora no longer grow unbounded.
  No user-visible behavior change; query results, `ResolveChunk`, and
  `FindRelated` are unchanged. The `OnFlush` message format gains an
  optional `(compacted N tombstones)` suffix that's emitted only when
  N > 0, so pure-write flushes keep the v0.3.0 format. Closes the
  v0.3.x compaction trigger named in
  [`docs/DECISIONS.md` ADR-012](docs/DECISIONS.md#adr-012-incremental-indexing-via-fsnotify--atomic-snapshot-swap).

### Documentation

- ADR-013 closed as **Deprecated** (Proposed → Deprecated). Prompt 22's
  precondition inspection of the CoIR-CSN-Python dataset revealed the
  motivating empirical anchor was misread: CSN queries are full Python
  function sources, not English docstrings; documents are docstrings
  extracted from those same functions, so BM25's win on this benchmark
  is a substring-leak artifact of dataset reframing, not a query-class
  signal an α-routing lever could exploit. `docs/BENCH.md`,
  `docs/DESIGN.md`, `docs/DECISIONS.md`, and `README.md` corrected
  correspondingly. The CSN-Python NDCG and token-budget numbers
  themselves are unchanged; only the causal explanation shifts. See
  [`docs/DECISIONS.md` ADR-013](docs/DECISIONS.md#adr-013-corpus-adaptive-α--adding-a-third-query-class-branch).

## [0.3.0] — 2026-05-21

Two stories: incremental indexing, and the first measurement of ken's
agent-input-cost claim. `ken-mcp` now watches the indexed repo and
re-publishes a snapshot 2s after any edit, so an agent editing files
mid-session sees its own edits without a restart; `ken index` defaults
to the same watch mode. Separately, a new token-budget benchmark
quantifies how many agent-input tokens ken needs to recall the target
chunk vs the grep+Read baseline — the headline ratios are in the
README; methodology and per-query-class numbers are in
[`docs/BENCH.md`](docs/BENCH.md#token-budget-recall--agent-side-efficiency).

### Added

- **Incremental indexing.** `WatchedIndex`
  ([`internal/search/watch.go`](internal/search/watch.go)) wraps the
  immutable `Index` with an fsnotify watcher, a 2s edit-burst debounce,
  and an `atomic.Pointer[Index]` snapshot swap. Reader path is one
  atomic load per query and never blocks; deletes are tombstones (no
  compaction in v0.3, monotonic-growth is bounded by edit volume).
  Design rationale in [`docs/DECISIONS.md` ADR-012](docs/DECISIONS.md#adr-012-incremental-indexing-via-fsnotify--atomic-snapshot-swap).
- `ken index --watch` / `--no-watch` flags. `--watch` is the default in
  v0.3+; `--no-watch` restores the v0.2 build-once-and-exit behavior for
  batch / CI / huge-corpus scenarios.
- `ken-mcp` always watches the indexed repo; cache entries refresh on
  file change. No env var to disable in v0.3.
- `WatchedIndex.SetOnFlush(func(msg string))` — per-flush callback so
  `ken index --watch` can emit interactive "re-indexed N files" lines
  on stderr without `internal/search` taking a dependency on a logger.
- **External-reference NDCG benchmark** on
  [CoIR-CSN-Python](https://github.com/CoIR-team/coir). 1000-query
  subsample, regex chunker: BM25 0.8743 / hybrid 0.7839 / semantic
  0.7405. Independent of semble's bench; published code-IR baselines
  give readers a comparable anchor.
  [`docs/BENCH.md`](docs/BENCH.md#external-benchmark--coir-csn-python).
- **Token-budget recall benchmark** (`bench/tokens/`, build tag
  `bench`). First measurement of agent-input token cost at fixed
  recall@10 vs a grep+Read baseline. cl100k_base tokenization via
  `pkoukk/tiktoken-go` as a universal LLM-input-cost proxy. The
  `bench` tag keeps the tiktoken-go dep out of released binaries.
  [`docs/BENCH.md`](docs/BENCH.md#token-budget-recall--agent-side-efficiency).
- `mcp.FormatResults` exported so the token-budget bench measures
  tokens against the same wire format ken-mcp emits, not a separate
  formatter.

### Changed

- README opening reframed: the two-axis value claim (runtime properties
  + measured agent-input efficiency) replaces the previous "runtime
  properties, not retrieval quality" framing now that the
  token-budget bench quantifies the agent-side win.
- [`internal/ann/flat.go`](internal/ann/flat.go) package comment
  updated to reflect the atomic-snapshot pattern (Flat stays immutable;
  new Flats are built per snapshot under v0.3 incremental indexing)
  instead of the previous "would require a lock" caveat.

### Fixed

- `THIRD_PARTY_LICENSES.md` regenerated to include `fsnotify v1.10.1`
  (BSD-3-Clause, new v0.3 dep) and `gotreesitter v0.18.0` (MIT, v0.2
  dep that was previously missing). Attribution chain now matches the
  binaries actually shipped.
- Doc-currency sweep ahead of the tag: `KEN_MCP_CHUNKER` enum in
  `docs/DESIGN.md` now correctly lists `regex/treesitter/line` (was
  `regex/line`); incremental-indexing subsection re-tensed to past
  with ADR-012 cross-reference; `internal/search/watch.go` added to
  the project layout; Sources updated (wazero relabeled "superseded by
  gotreesitter per ADR-010").
- `CLAUDE.md` parity-harness claim updated: the 11,447-input
  `transformers.AutoTokenizer` parity test is done with zero drift;
  `CLAUDE.md` previously said it was "still owed."

### Dependencies

- Added: `github.com/fsnotify/fsnotify v1.10.1` (runtime, BSD-3-Clause).
- Added: `github.com/pkoukk/tiktoken-go` (bench-only, MIT).

## [0.2.0] — 2026-05-20

Tree-sitter chunker lands (opt-in), the BM25 tokenizer is rewritten as
a verbatim port of semble's `tokens.py`, and every `KEN_MCP_*` env var
is validated at startup instead of silently falling through. The
chunker default stays `regex` — the per-language NDCG swap between
regex and tree-sitter is within bench noise overall, with wins on
Kotlin/Zig/TypeScript/Java/PHP and losses on Python/C/Rust/Lua/Scala.

### Added

- **Tree-sitter chunker** via [`gotreesitter`](https://github.com/odvcencio/gotreesitter)
  ([`internal/chunk/treesitter/`](internal/chunk/treesitter/)), opt-in
  via `--chunker=treesitter` or `KEN_MCP_CHUNKER=treesitter`. Runs cAST
  split-then-merge from [arXiv 2506.15655](https://arxiv.org/html/2506.15655).
  206 grammars embedded. [`docs/DECISIONS.md` ADR-010](docs/DECISIONS.md#adr-010-pure-go-tree-sitter-via-gotreesitter).
- "Choosing a chunker" subsection in the README — per-language
  recommendation table based on the full 63-repo bench.
- "How this was built" notice on the README — frames the human/AI
  division of labor honestly up-front so readers aren't left guessing.
- "Tuning ken's routing for your repo" README subsection — guidance
  for agents on when to prefer ken vs grep.
- 11,447-input tokenizer parity harness (`scripts/parity_dump.py` +
  `internal/embed/parity_test.go` under `-tags=parity`). Zero drift
  across normalize / pre_tokenize / wordpiece / other on first run;
  surfaced and fixed three real tokenizer bugs the 18-case golden
  fixture had missed.
- `KEN_MCP_*` env-var validation: every typoed enum / non-integer /
  bad path now logs a stderr warning and falls back to the documented
  default, replacing the silent-fallthrough mode where `Atoi("of")`
  returned 0 and disabled the cache. [`docs/DECISIONS.md` ADR-009](docs/DECISIONS.md#adr-009-env-var-validation-instead-of-silent-fallthrough).
- `regen_golden.sh` — idempotent helper that bootstraps `.venv/`,
  pip-installs the Python reference deps, regenerates the embedding
  golden fixture, prints a sanity summary.
- Dependabot config covering Go modules and GitHub Actions.

### Changed

- **BM25 tokenizer** rewritten as a verbatim port of semble's
  `tokens.py` — snake-case compound preservation, ASCII-only run
  extraction matching `_TOKEN_RE`, compound-first emission order.
  Moved hybrid NDCG +0.002 and BM25-raw +0.002 on semble's bench.
  [`docs/DECISIONS.md` ADR-008](docs/DECISIONS.md#adr-008-bm25-tokenizer-as-verbatim-port-of-sembles-tokenspy).
- C# and bash grammars in the tree-sitter chunker route through the
  line chunker (C# OOMs on real-world files at ~1.7 GB RSS; bash is
  pathologically slow on real bash-it content). Auto-fallback;
  documented in the README "Choosing a chunker" notes.

### Decided (no code change)

- **Default chunker stays `regex` in v0.2.0**; tree-sitter is opt-in.
  Net NDCG difference is within bench noise (0.838 vs 0.842, Δ −0.004);
  per-language wins on Kotlin/Zig/TypeScript/Java/PHP, losses on
  Python/C/Rust/Lua/Scala. [`docs/DECISIONS.md` ADR-011](docs/DECISIONS.md#adr-011-default-chunker-stays-regex-in-v020-treesitter-is-opt-in).

### Dependencies

- Added: `github.com/odvcencio/gotreesitter v0.18.0` (MIT).

## [0.1.0] — 2026-05-19

Initial public release. End-to-end pure-Go, no-cgo port of semble's
hybrid code-retrieval pipeline — walk → chunk → BM25 + Model2Vec
semantic → α-weighted RRF fusion → rerank → penalties — with an MCP
server speaking the same `search` / `find_related` schema and wire
format as semble's, so existing semble-trained agents work unchanged.

### Added

- Pure-Go, no-cgo port of semble's hybrid retrieval pipeline. Single
  static binary; `GOOS`/`GOARCH` cross-compiles for free.
  [`docs/DECISIONS.md` ADR-001](docs/DECISIONS.md#adr-001-pure-go-no-cgo).
- [`internal/repo/walk.go`](internal/repo/walk.go) — gitignore-respecting
  filesystem walk (common-subset matcher), with binary-file (NUL sniff)
  and oversized-file skips.
- [`internal/chunk/`](internal/chunk/) — `Chunker` interface (registry
  via `database/sql`-style blank imports to avoid an import cycle).
  Line chunker (50-line / 5-overlap) and per-language regex chunkers
  for Python / Go / TypeScript / Java / Rust (JavaScript routes through
  TypeScript). [`docs/DECISIONS.md` ADR-005](docs/DECISIONS.md#adr-005-regex-chunkers-as-stage-2-default).
- [`internal/bm25/`](internal/bm25/) — Lucene-variant BM25 (`k1=1.5`,
  `b=0.75`, non-negative IDF; ATIRE TF formula — rank-equivalent
  cosmetic divergence vs Lucene). [`docs/DECISIONS.md` ADR-006](docs/DECISIONS.md#adr-006-bm25-formula-choice-lucene-variant).
- [`internal/embed/`](internal/embed/) — Model2Vec inference: hand-rolled
  WordPiece tokenizer ([ADR-003](docs/DECISIONS.md#adr-003-hand-rolled-wordpiece-tokenizer)),
  hand-rolled safetensors mmap reader ([ADR-004](docs/DECISIONS.md#adr-004-hand-rolled-safetensors-reader)),
  three-tensor pooling (`Σ embeddings[mapping[id]]·weights[id] / Σ
  weights[id]`) with float64 accumulators (float32 silently fails ≥1−1e-5
  cosine on longer inputs).
- [`internal/ann/flat.go`](internal/ann/flat.go) — exact brute-force
  cosine retriever over the dense embedding matrix.
- [`internal/search/`](internal/search/) — orchestration: α-weighted
  RRF fusion + file-coherence / definition / embedded-symbol /
  stem-match boosts + three-tier path penalties + file-saturation
  decay. Ported verbatim from semble's `search.py` +
  `ranking/{boosting,penalties,weighting}.py` + `tokens.py`.
  [`docs/DECISIONS.md` ADR-002](docs/DECISIONS.md#adr-002-retrieval-algorithm-verbatim-from-semble).
- [`cmd/ken`](cmd/ken/) — CLI subcommands `index` / `search` / `bench`.
- [`cmd/ken-mcp`](cmd/ken-mcp/) — MCP server speaking JSON-RPC over
  stdio. Two tools (`search`, `find_related`) with arg shapes ported
  verbatim from `/tmp/semble/src/semble/mcp.py`. Same markdown-string
  return format as semble's `_format_results`, so existing
  semble-trained agents work unchanged.
  [`docs/DECISIONS.md` ADR-007](docs/DECISIONS.md#adr-007-mcp-server-as-drop-in-replacement-for-semble).
- Per-process repo→Index cache in [`mcp/`](mcp/) (LRU + singleflight
  dedup); `http(s)://` URLs shallow-clone via `go-git` to
  `$TMPDIR/ken-mcp/<sha256>/`.
- 18-case embedding golden fixture (`testdata/golden.json`, generated
  by `scripts/pin_inference.py`). Asserts ≥1−1e-5 cosine parity against
  `StaticModel.encode()` on non-degenerate inputs; zero-vector
  contract on empty / all-`[UNK]` inputs.
- First NDCG measurement against semble's bench: 0.842 hybrid (gap
  0.012 vs published 0.854), 0.624 BM25-raw, 0.647 semantic-raw
  (within 0.003 of semble's 0.650 — algorithm port validated).
  [`docs/BENCH.md`](docs/BENCH.md#empirical-findings-v010).

### Dependencies (runtime)

- `github.com/go-git/go-git/v5` (Apache-2.0; MCP shallow-clone).
- `github.com/modelcontextprotocol/go-sdk` (Apache-2.0).
- `golang.org/x/sync` (singleflight for the MCP cache).
- `golang.org/x/text` (Unicode normalization for the embed tokenizer).
