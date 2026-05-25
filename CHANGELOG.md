# Changelog

All notable changes to ken are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
ken is pre-1.0 (`v0.x.y`); breaking changes can land in minor versions
until v1.0.0. Each release tag has a corresponding GitHub release page
with pre-built binaries.

## [Unreleased]

(no changes yet)

## [0.7.0] — 2026-05-25

The database-schema indexing release. Two tiers, both shipping together:

1. **Tier 1 — Static SQL parsing.** ken now parses `.sql` files in the
   corpus (`CREATE TABLE` / `INDEX` / `VIEW`, `ALTER TABLE`) and emits
   one denormalized "for retrieval" chunk per database object. Activates
   automatically when `.sql` files are present. No opt-in, no new env
   var; the structural chunks are additive to the regular file
   chunking, so existing BM25 hits on raw SQL still work.
2. **Tier 2 — Live Postgres introspection.** When `KEN_DB_DSN` is set,
   ken introspects via `information_schema` / `pg_catalog` and emits
   one chunk per table / view / index / function. Every chunk carries
   a freshness header (`-- indexed at <UTC> from postgres@<host>`); no
   credentials in chunk text. Postgres only for v0.7.0; MySQL +
   SQLite are planned. Closes [#8](https://github.com/townsendmerino/ken/issues/8).

Design rationale, alternatives considered (column-exclusion DSL,
per-call introspection, LISTEN/NOTIFY, agent-triggerable reindex tool),
and the PII stance are in
[ADR-017](docs/DECISIONS.md#adr-017-database-schema-indexing--two-tier-static-sql--live-postgres-with-documented-pii-stance).

### Added

- **`internal/sql` package** — pure-Go parser for the DDL subset above.
  Statement splitter is aware of line/block/nested comments,
  single/double quotes, Postgres dollar-quoting (`$$ ... $$` and
  `$tag$ ... $tag$`), and paren depth. Wired into `internal/search`'s
  chunk dispatch so the build-once path (`FromFS`) and the watch path
  (`WatchedIndex.appendFile`) both emit structural SQL chunks for
  `.sql` files. 14 unit-test scenarios pin every shape including
  malformed statements, dollar-quoting, multi-comma column lists, and
  empty/DML-only files. Exports `IsSQLFile(path) bool` and
  `ParseFile(path, content, logger) ([]chunk.Chunk, error)`.

- **`internal/db` package** — Postgres introspection via
  `github.com/jackc/pgx/v5`. `IndexSchema(ctx, opts) ([]chunk.Chunk, error)`
  is the build-once entry; `Refresher` (with `Run(ctx)` periodic + `Refresh(ctx)`
  manual) orchestrates the three reindex layers. Internal serialization
  via mutex makes concurrent triggers safe to spam. Chunk emission
  shares the denormalized "for retrieval" shape with Tier 1 (header
  line + `TABLE name` + columns + indexes + `FK referenced by:` reverse
  navigation + optional sample rows). Three integration tests gated
  by build tag `dbintegration` and `KEN_DB_TEST_DSN` env var.

- **`WatchedIndex.SetExtraChunks(chunks)`** in `internal/search` — the
  composition seam Tier 2 uses to inject DB chunks into the published
  Index without disturbing the FS chunks the fsnotify watch path
  manages. The published snapshot is always FS-chunks ∪ extras; both
  sources update it via the same ADR-012 atomic-swap path. Calling
  with `nil` clears the extras ("DB unreachable, keep serving FS").

- **`KEN_DB_DSN`** env var — Postgres connection string. Empty (default)
  keeps Tier 2 off. Format must be parseable URL (`postgres://` or
  `postgresql://`). Invalid scheme / missing host / unparseable form
  logs a stderr warning and disables Tier 2 rather than crashing.

- **`KEN_DB_SAMPLE_ROWS`** env var — rows-per-table to sample (default
  0 = schema only). When > 0, ken pulls N rows per table deterministically
  (`ORDER BY` first PK column; fallback `ORDER BY 1`) and appends them
  to the table's chunk. Long cells truncated at 80 chars with `…`.

  > Intended for development databases. Do not point this at production
  > data — sample rows are sent to the agent as part of search results
  > and thus to your LLM provider. See the README and ADR-017 for the
  > PII stance.

- **`KEN_DB_REINDEX_INTERVAL`** env var — Go duration string (e.g.
  `5m`, `1h`) for periodic DB refresh. Empty/zero (default) means no
  periodic polling — refresh only at startup or via `SIGHUP`. Tick-time
  failures log a warn and don't kill the goroutine.

- **`SIGHUP` handler** in `cmd/ken-mcp` (unix-only; no-op on Windows).
  Each `SIGHUP` triggers `Refresher.Refresh`, which rebuilds the DB
  chunks and atomically swaps them in. Standard `migrate-up` ergonomics:
  ```
  migrate-up:
      psql -f migrations/$$NEXT.sql
      kill -HUP $$(pgrep ken-mcp)
  ```

- **New env helpers** in `cmd/ken-mcp/env.go`: `envDuration` (parses Go
  duration strings) and `envDSN` (validates `postgres://` URL form
  without logging the raw DSN). Both follow the existing ADR-009
  warn-and-fallback pattern; 17 new env-helper subtests pin the
  behavior.

- **CI Postgres service container** — a new `test-db-integration` job
  on `ubuntu-latest` spins up `postgres:16-alpine` and runs the
  dbintegration tests plus `TestBinary_StdoutIsCleanJSONRPC_WithDB`
  against it. macos-latest runs the default `go test ./...` which
  skips the dbintegration tests cleanly via the build tag.

### Changed

- **`internal/search/index.go`** and **`internal/search/watch.go`** now
  route every `.sql` file through `sql.ParseFile` in addition to the
  configured chunker via the new `chunkOneFile` helper. The structural
  chunks join the index alongside the line-chunked file bytes; both
  forms are queryable.

- **`internal/search/WatchedIndex`** gains an `extraChunks` slot
  alongside `chunks` for orchestrator-injected non-FS chunks. The
  published Index is built from `chunks ∪ extraChunks` on every flush;
  `compactCorpus` only touches `chunks` so DB extras survive
  fsnotify-driven snapshot republishes.

- **`cmd/ken-mcp` startup** gains the conditional `wireDBTier2` block.
  Behavior with no `KEN_DB_DSN` set is byte-identical to v0.6.0; the
  existing `TestBinary_StdoutIsCleanJSONRPC` test confirms this and a
  new `TestBinary_StdoutIsCleanJSONRPC_WithDB` test confirms the full
  Tier-2 code path (DSN parse → connect → IndexSchema → SetExtraChunks
  → Refresher → SIGHUP handler) doesn't leak anything to stdout when
  enabled.

### Dependencies

- **`github.com/jackc/pgx/v5` v5.9.2** — pure-Go Postgres driver. Default
  `Tracer` is nil; no protocol logging to stdout.

### Notes

- **`mcp.Run` (v0.6.0 embedded-corpus library API) is unaffected.** DB
  support there is planned for v0.8.0+ via `mcp.Options.DBSource` or
  similar; no `mcp.Options` changes in v0.7.0. Tier 1's static SQL
  parsing DOES benefit `mcp.Run` because it lives at the
  `internal/search` layer — filesystem-based, not DB-based.
- **PII stance is documentation + sane defaults.** Schema-only is the
  default. The opt-in sampling env var is unambiguous. Freshness
  metadata in every chunk surfaces provenance. No engineered redaction
  controls — operators who need those should not point ken at the DB.
- **DB chunks attach to the default repo only.** `KEN_DB_DSN` requires
  `KEN_MCP_DEFAULT_REPO` (and that the default repo is a local path,
  not an http(s) URL). When unset, Tier 2 logs a warn and stays off.
- **Migration-history folding is out of scope** for v0.7.0. CREATE
  TABLE + later ALTER TABLE statements across files emit separate
  chunks; agents see the union of historical state. Documented in
  ADR-017 alternatives as a future refinement.

## [0.6.0] — 2026-05-24

The embedded-corpus release. The library form of ken-mcp lands: SDK
authors can `//go:embed` their docs + the Model2Vec model and ship a
single static MCP server binary with high-quality local retrieval, no
backend, no per-query network egress, and structural agent sandboxing
(no path resolution → no path-traversal escape). Closes
[#7](https://github.com/townsendmerino/ken/issues/7); design rationale
in [ADR-016](docs/DECISIONS.md#adr-016-embedded-corpus-mcp-build-pattern-via-mcprun-library-function).

The stock `cmd/ken-mcp` binary is **unchanged**: same env vars, same
tool surface, same watch mode, same multi-repo path/URL resolution.
v0.6.0 is purely additive — the new pattern coexists with the existing one
rather than replacing it.

### Added

- **`github.com/townsendmerino/ken/mcp.Run(ctx, fsys, opts) error`** —
  public library API for the embedded-corpus build pattern. Serves
  `search` and `find_related` over a single fixed `fs.FS` corpus,
  blocking until ctx is canceled or the client closes the transport.
  Tool wire format and arg schemas are identical to `cmd/ken-mcp`'s,
  so agents trained against semble's or ken's MCP server work
  unchanged. `Options.ModelFS` lets the Model2Vec snapshot live in an
  `fs.FS` (typically `//go:embed model/*`) rather than on disk;
  `Options.ModelDir` is the path-based alternative. Typoed enum
  values (`Mode`, `ChunkerName`, `LogLevel`) log a stderr warning and
  fall back to documented defaults — same ADR-009 contract `cmd/ken-mcp`
  uses for env vars.
- **`cmd/ken-mcp-docs`** — worked example: a 20-line `main.go` baking
  ken's own `docs/*.md` and the Model2Vec model into a single static
  74 MB binary. Builds via `scripts/build-docs-mcp.sh` (which stages
  the model + docs into the package's directory then runs `go build
  -tags=embed_corpus`). Gated by the `embed_corpus` build tag so a
  fresh clone — where the embed dirs don't yet exist — still builds
  cleanly via `go build ./...`.
- **`internal/chunk/markdown` chunker** — heading-aware boundaries
  (ATX + setext), atomic fenced-code / tables / lists, frontmatter
  handling (YAML `---` and TOML `+++`), byte-fidelity preserved.
  Registers as `"markdown"` in the chunker registry. Auto-falls back
  to the line chunker for non-markdown files in mixed-content corpora.
  No new third-party deps — handwritten pure-Go scanner.
- **`embed.LoadFromFS(fs.FS, dir) (*StaticModel, error)`** — canonical
  entry point for loading a Model2Vec snapshot from any `fs.FS`. Same
  for the helpers it composes: `LoadTokenizerFromFS` and
  `OpenSafetensorsFromFS`. The disk-path counterparts (`Load`,
  `LoadTokenizer`, `OpenSafetensors`) become thin wrappers — `Load`
  is now formally deprecated, the others remain useful for callers
  reading individual files.
- **`internal/search.FromFSWithModel(fsys, mode, chunkerName, model)`** —
  index-build entry point that takes a pre-loaded model (rather than
  resolving a `modelDir` internally). Used by `mcp.Run` so the model
  can come from `Options.ModelFS` instead of a path.
- **`mcp.Logger`, `mcp.LogLevel`, `mcp.ParseLogLevel`, `mcp.LogLevelNames`,
  `mcp.ValidateEnum`** — the leveled logger and validation helper
  moved out of `cmd/ken-mcp` into the `mcp` package so both the
  Cache-backed server and `mcp.Run` share one logger type. Stdout is
  never written from any of these — the JSON-RPC contract is enforced.

### Changed

- **Side-effect chunker imports moved to binaries.** Previously
  `internal/search/index.go` blank-imported both `regex` and
  `treesitter`, which meant any program transitively importing
  `search` pulled in `gotreesitter/grammars` and its 19 MB
  `//go:embed grammar_blobs/*.bin` payload — the Go linker cannot
  dead-code-eliminate `embed.FS` contents. `internal/search` now only
  blank-imports `regex` (the universal default); `cmd/ken` and
  `cmd/ken-mcp` add `treesitter` and `markdown` explicitly. The stock
  binaries' chunker availability is unchanged. `cmd/ken-mcp-docs`
  imports only `markdown`, which is how the 19 MB grammar bundle
  stays out of it.
- **`cmd/ken-mcp` uses `mcp.Logger`** instead of its old internal
  `leveledLogger`. Visible log format is unchanged. `env.go`'s
  `envEnum` is now a thin wrapper around `mcp.ValidateEnum` so the
  validation message format stays consistent between env-var and
  Options-field validation.

### Deprecated

- **`embed.Load(modelDir)`** — use `LoadFromFS(os.DirFS(modelDir), ".")`
  instead. The wrapper still works; will be removed in a future minor
  release (pre-1.0 semver permits this).

### Binary sizes (built 2026-05-24, darwin/arm64)

| Binary | Size | Notes |
|---|---|---|
| `bin/ken` | 36 MB | CLI; includes all chunkers |
| `bin/ken-mcp` | 42 MB | Stock MCP server; includes all chunkers + 19 MB grammar bundle |
| `bin/ken-mcp-docs` | 74 MB | Demo embedded-corpus server; no grammar bundle but +62 MB embedded model + 144 KB embedded docs |

## [0.5.0] — 2026-05-25

The library-API + monorepo-correctness release. Two changes bundled
into one tag:

1. **`fs.FS` is the canonical walker/indexer surface** — ken can now
   index any `fs.FS` (`embed.FS`, `fstest.MapFS`, tarball-backed, git
   tree object, in-memory snapshot) — not just a directory on disk.
   Unlocks agent sandboxing (`ken-mcp` over a chroot-y `fs.FS` view,
   no syscall-level escape) and offline analysis (index a tarball
   without unpacking). Prompted by an r/golang commenter on the v0.4
   release post; tracked in [#6](https://github.com/townsendmerino/ken/issues/6).

2. **Nested `.gitignore` support** — the walker now reads `.gitignore`
   files in every directory, not just the root, matching git's
   behavior. Field-driven: a gobe monorepo user reported `node_modules/`
   polluting their results because their exclusion was in per-package
   `.gitignore` files. Tracked in [#5](https://github.com/townsendmerino/ken/issues/5).

(Date is a placeholder — set on tag day.)

### Added

- **`repo.WalkFS(fs.FS, Options)` and `search.FromFS(fs.FS, Mode, …)`** —
  the canonical filesystem surface as of this release. Either one
  accepts any `fs.FS` implementation. Parity tests (`Walk` vs `WalkFS`,
  `FromPath` vs `FromFS`) pin that the `os.DirFS`-wrapped behavior
  matches the historical real-FS implementation byte-for-byte. Zero
  new deps; `fs` and `testing/fstest` are stdlib. Closes [#6](https://github.com/townsendmerino/ken/issues/6).

- **Nested `.gitignore` support.** The walker now respects `.gitignore`
  files in every directory, not just the root, matching git's behavior:
  outer scopes evaluate first, inner scopes last, last-match-wins
  across the union; inner scopes can both add new ignores and
  re-include via `!pattern`. Implemented as a per-directory scope
  stack on top of the existing handwritten rule engine — no new
  dependency, no swap to `github.com/sabhiram/go-gitignore` (kept as
  a documented future option for edge-case pathspec parity inside the
  rule engine itself; see [ADR-015](docs/DECISIONS.md#adr-015-nested-gitignore-support-via-scope-stack-on-existing-rule-engine)).
  `Matcher` (the watch-path seam) gains the same nested awareness via
  a one-shot tree walk at construction. Closes [#5](https://github.com/townsendmerino/ken/issues/5).

### Deprecated

- **`repo.Walk(opts)` and `search.FromPath(root, …)`** are now thin
  one-line wrappers around their `FS` siblings (`WalkFS(os.DirFS(opts.Root), opts)` /
  `FromFS(os.DirFS(root), …)`). They remain functional and tested but
  are marked `// Deprecated:` in source; removal is scheduled for a
  future minor release. Pre-1.0 semver permits this.

### Fixed

- **`Matcher.ShouldIndex` now performs an ancestor walk** so directory-prune
  rules (like `build/`) correctly exclude files inside them. Surfaced while
  wiring nested `.gitignore` through the watch path: `WalkFS` had always
  gotten this right via `fs.SkipDir`, but `Matcher` (used by `--watch`)
  answered paths directly with no walk, so a `build/` rule was silently
  failing to exclude `build/x.txt` when queried. Anyone running
  `ken index --watch` against a repo with dir-only ignore rules may have
  been bitten by this without noticing — the watcher would re-add files
  inside ignored directories on every event.

### Note

- **`--watch` (fsnotify) remains real-FS-only by construction**, so
  `fs.FS`-backed indexes are build-once. fsnotify itself doesn't
  abstract over `fs.FS` (the kernel APIs it wraps are real-FS-only),
  and neither sandboxing nor offline analysis needs incremental
  reindex. Same goes for `repo.Matcher` (used only by the watch path).
- **`ken-mcp` env-var config stays path-based for v0.5.0.** An
  MCP-side `fs.FS` integration (sandboxed-FS-only mode, exposed via
  new config) is a future change tracked separately.
- **`.gitignore` freshness during watch.** `Matcher` collects every
  `.gitignore` once at construction. Files added, modified, or removed
  after the watcher starts are NOT picked up — a full re-index
  (restart `ken index`) is required. Tracked for a future release.

See [ADR-014](docs/DECISIONS.md#adr-014-fsfs-as-canonical-walkerindexer-surface)
and [ADR-015](docs/DECISIONS.md#adr-015-nested-gitignore-support-via-scope-stack-on-existing-rule-engine)
for the alternatives considered and the full consequences lists.

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
