# ken — design

ken is a pure-Go, no-cgo port of [MinishLab/semble](https://github.com/MinishLab/semble): a hybrid code-search tool (WordPiece tokenization → Model2Vec embedding-table lookup → mean-pool → L2 normalize, plus BM25, α-weighted RRF, and a pile of regex-based reranking heuristics). The pipeline is shallow — no transformer at runtime. This document is the design rationale and the algorithm spec: *why* it's built this way and *what* the precise contracts are, for re-implementers and maintainers. For *what ken is and how to use it*, see [`../README.md`](../README.md); for *how to work in this codebase*, see [`../CLAUDE.md`](../CLAUDE.md).

The name: "ken" is Scottish/Old English for *to perceive, to know* ("beyond my ken") — exactly what the tool does. Same word appears in Japanese as 見 (`ken`, *to see/view*). Three letters joins the elite CLI tribe (`rg`, `ag`, `jq`, `awk`, `fzf`). The upstream Python project remains `semble`; this document distinguishes the two.

## Status

- **Build order: complete** (Stages 1–5; see [Decisions](#decisions) below).
- **Embedding parity: 18-case golden suite** passes at cosine ≥ 1 − 1e-5 against `StaticModel.encode()`.
- **Tokenizer parity: 11,447-input corpus-scale harness** (`scripts/parity_dump.py` + `internal/embed/parity_test.go` under build tag `parity`) reports 0 drift across `normalize` / `pre_tokenize` / `wordpiece` / `other`. Surfaced and fixed three real bugs the spot-check had missed (see §3).
- **Hybrid retrieval: ported verbatim** from semble's live source (`search.py`, `ranking/{boosting,penalties,weighting}.py`, `tokens.py`); see §7.
- **MCP server: drop-in replacement** for semble's MCP server; same tool surface and wire format (§8).
- **NDCG vs semble: measured at v0.1.0, re-measured with tree-sitter at v0.2.0** — v0.1.0 hybrid 0.842 (regex chunker) vs semble 0.854 (Δ −0.012, 63 repos × 1251 queries). v0.2.0 added the tree-sitter chunker via `gotreesitter`: hybrid 0.838 (Δ −0.004 vs regex baseline — within noise). Per-language signal is mixed: clear wins on Kotlin/Zig/TypeScript/Java/PHP, losses on Python/Rust/C/Lua/Scala. **Decision: default chunker stays `regex` in v0.2.0; treesitter is opt-in** (see [`DECISIONS.md` ADR-011](DECISIONS.md#adr-011-default-chunker-stays-regex-in-v020-treesitter-is-opt-in) and the per-language recommendation table in [README.md "Choosing a chunker"](../README.md#choosing-a-chunker)). Semantic raw matches within 0.003 (validates algorithm port); BM25 tokenizer brought to verbatim semble parity. The residual gap to semble (≈0.012) is now empirically established as **not primarily a chunker problem** — tree-sitter chunking traded wins for losses without net movement.
- **Incremental indexing: v0.3** — `WatchedIndex` (`internal/search/watch.go`) wraps `*Index` in an `atomic.Pointer` and re-publishes a fresh snapshot every 2s after a debounced batch of fsnotify events. Default-on for `ken index` and ken-mcp; `--no-watch` opts out. No reader-side lock, snapshot-consistent reads, tombstone-on-delete with compaction deferred to v0.3.x. See [`DECISIONS.md` ADR-012](DECISIONS.md#adr-012-incremental-indexing-via-fsnotify--atomic-snapshot-swap).

## Decisions

The summary below pins the as-built choices. For the full ADR-style record of each decision (alternatives considered, consequences, status), see [`docs/DECISIONS.md`](DECISIONS.md).

- **cgo: not allowed.** Pure Go. Single static binary, free `GOOS`/`GOARCH` cross-compile, no `libtokenizers.a` to vendor per platform. ([ADR-001](DECISIONS.md#adr-001-pure-go-no-cgo))
- **Chunking: all three options behind a `Chunker` interface**, selectable at runtime. v1 ships Option C (hand-rolled per-language regex); A (tree-sitter via gotreesitter) lands in v0.2.0; B (Chroma) remains a documented future path (§2, §10). ([ADR-005](DECISIONS.md#adr-005-chunker-interface-with-three-pluggable-options-ship-c-first), [ADR-010](DECISIONS.md#adr-010-tree-sitter-via-gotreesitter-instead-of-wazerowasm))
- **Tokenizer: hand-rolled WordPiece** in `internal/embed/tokenize.go`, ~400 LOC, no external tokenizer dependency. Validated against `transformers.AutoTokenizer` on 11k+ inputs (§3). ([ADR-003](DECISIONS.md#adr-003-hand-rolled-wordpiece-tokenizer))
- **Tokenizer model type: WordPiece**, confirmed against the live `tokenizer.json` for `minishlab/potion-code-16M`.
- **Vocab size / embedding dim:** 61,826 × 256, confirmed from the safetensors header.
- **`tokenizer_config.json` settings:** file doesn't exist on HF. Equivalent settings come from `tokenizer.json`'s BertNormalizer (`lowercase:true`, `strip_accents:null` → strips, `handle_chinese_chars:true`). No `[CLS]`/`[SEP]`/`[MASK]` — only `[PAD]`=0 and `[UNK]`=1.
- **v1 language coverage (regex chunker):** Python, Go, TypeScript, Java, Rust. JavaScript routes through the TypeScript ruleset.
- **Project name:** `ken`. **Module path:** `github.com/townsendmerino/ken`.

Empirical findings pinned by `pin_inference.py` against `StaticModel.encode()`:

- **Q1 — weights applied at runtime, not pre-baked.** Plain `mean(embeddings[mapping[id]])` produces wrong vectors (cosine 0.70–0.99); `Σ rows·weights / Σ weights` matches to 1.0000000 on all 16 non-degenerate cases.
- **Q2 — PAD masking is moot at v1.** ken encodes per-chunk, no batched padding. Revisit if SIMD batching is added.
- **Q3 — no whitespace scaling beyond tokenization.** `"hello world"`, `"hello  world"`, `"hello   world"` all produce identical token IDs and cosine 1.0. Replicate tokenization exactly; nothing else.

Verified normalization behaviors that ken replicates (golden in `testdata/golden.json`):

- Accents stripped: `café résumé` → `cafe, resume`
- German ß preserved (not converted to ss): `Müller weiß` → `muller, wei, ##ß`
- CJK split per-character: `中文` → `中, 文`
- Lowercasing + WordPiece `##` continuation: `PascalCase` → `pascal, ##case`
- `max_input_chars_per_word = 100`: words longer than 100 chars emit `[UNK]` immediately

The Model2Vec Rust port at [github.com/MinishLab/model2vec-rs](https://github.com/MinishLab/model2vec-rs) is the secondary reference for any future algorithmic question.

## 1. Project layout

```
ken/
├── cmd/
│   ├── ken/                 # CLI: index / search
│   └── ken-mcp/             # MCP server entry point
├── internal/
│   ├── chunk/               # code chunking (Chunker interface + impls)
│   │   ├── chunk.go         # Chunk type
│   │   ├── registry.go      # Chunker interface + Register/Get + ChunkFile routing (the swappable seam)
│   │   ├── line_chunker.go  # LineChunker wrapped to the interface ("line")
│   │   ├── regex/           # Option C — hand-rolled per-language (v1 default)
│   │   │   ├── chunker.go   # generic line-walking engine + LanguageRules; self-registers "regex"
│   │   │   ├── python.go
│   │   │   ├── golang.go
│   │   │   ├── typescript.go # also handles JavaScript (shared ruleset)
│   │   │   ├── java.go
│   │   │   └── rust.go
│   │   ├── lines.go         # fallback line chunker (50-line, 5-overlap)
│   │   └── languages.go     # extension → language map (mirrors sources.FILE_TYPES)
│   ├── embed/               # Model2Vec inference
│   │   ├── model.go         # StaticModel: orchestrates tokenize → gather → pool → norm
│   │   ├── tokenize.go      # hand-rolled WordPiece (BertNormalizer + BertPreTokenizer + WordPiece)
│   │   ├── safetensors.go   # parse safetensors header, mmap the three M2V tensors
│   │   └── pool.go          # mean-pool + L2 normalize (with mapping[] indirection)
│   ├── bm25/                # bm25s-equivalent sparse scorer
│   │   ├── index.go         # build from token streams (CSR matrix)
│   │   ├── query.go         # get_scores
│   │   └── tokenize.go      # identifier-aware tokenizer (camelCase / snake_case splitter)
│   ├── ann/                 # vicinity equivalent
│   │   └── flat.go          # cosine over dense matrix; HNSW behind an interface
│   ├── search/              # the orchestration layer
│   │   ├── index.go         # Index: FromPath, Search, FindRelated; Mode enum
│   │   ├── hybrid.go        # candidate pool (top_k*5) + RRF fusion (k=60)
│   │   ├── rerank.go        # definition / embedded-symbol / stem / file-coherence boosts
│   │   ├── penalties.go     # path penalties + file-saturation decay (rerank_topk)
│   │   ├── adaptive.go      # symbol-like vs NL query classifier + resolveAlpha
│   │   └── watch.go         # WatchedIndex: fsnotify + atomic.Pointer[Index] snapshot swap (v0.3, ADR-012)
│   └── repo/                # source acquisition
│       └── walk.go          # gitignore-respecting walk (pathspec equivalent); `fs.FS`-canonical as of v0.5.0 (ADR-014); nested `.gitignore` honored via per-dir scope stack (ADR-015)
├── mcp/                     # MCP server: tool registration, repo cache, shallow clone
│   ├── server.go
│   ├── cache.go
│   ├── clone.go
│   └── types.go
├── scripts/                 # Python reference + parity dumper
│   ├── pin_inference.py     # 18-case embedding golden generator
│   ├── parity_dump.py       # 100k-input tokenizer parity dumper
│   └── adversarial.txt      # hand-crafted edge cases for parity
├── docs/
│   └── DESIGN.md            # this file
├── testdata/                # parity fixtures (large per-machine artifacts gitignored)
├── LICENSE                  # MIT (ken's own code)
├── NOTICE                   # attribution for potion-code-16M (MIT) + arctic-embed (Apache-2.0)
├── README.md
├── CLAUDE.md                # contributor guide
└── go.mod                   # module github.com/townsendmerino/ken
```

### Dependency picks

| Need | Choice | Why |
|---|---|---|
| Code chunking | **Hand-rolled per-language regex (Option C) behind a `Chunker` interface, v1 default.** Chroma (B) and WASM tree-sitter (A) ship as alternatives in later versions, selectable via `--chunker=...`. As of v0.6.0, a `markdown` chunker is also registered for documentation corpora. | All four live in `internal/chunk/` (`line`, `regex`, `treesitter`, `markdown`); see §2. |
| Safetensors | **Hand-rolled reader in `internal/embed/safetensors.go`.** | The format is an 8-byte length + JSON header + raw bytes; ken only ever reads 3 static tensors and wants an `mmap`'d zero-copy view (see §4). A dependency can't give that without fighting its abstraction. ~80 LOC, no external dep. |
| Tokenizer | **Hand-rolled WordPiece in `internal/embed/tokenize.go`.** | No external tokenizer dep. ~400 LOC. Parity-tested against `transformers.AutoTokenizer`. See §3. |
| MCP | `github.com/modelcontextprotocol/go-sdk` | Official, typed handlers, pure Go. |
| Git | `github.com/go-git/go-git/v5` | Pure Go, no shell-out, works in containers. |
| HNSW | `github.com/coder/hnsw` (deferred — flat `internal/ann/flat.go` for v1) | Active, pure Go, generics-based. The swap would be local — `internal/ann/` exposes only `Flat.Query(q, k) []Hit` today; the eventual interface is "to be introduced when the swap happens" rather than already-present scaffolding. Flat is exact and fine at repo scale. |
| Gitignore | In-tree handwritten engine + per-directory scope stack (v0.5.0, ADR-015). `github.com/sabhiram/go-gitignore` remains a documented future swap for full pathspec edge-case parity inside the engine. | Mirrors `pathspec` behavior closely for the common subset (anchored, `**`, dir-only, negation) plus nested `.gitignore` semantics. Pure Go. |
| File watching | `github.com/fsnotify/fsnotify` (v0.3) | Pure Go, no cgo, the OS backends are abstracted (inotify on Linux, FSEvents on macOS, ReadDirectoryChangesW on Windows). Used by Kubernetes / VS Code / Hugo. v0.3's incremental indexing (`internal/search/watch.go`, ADR-012) holds one watcher per `WatchedIndex`. |

### Filesystem surface — `fs.FS` canonical (v0.5.0)

As of v0.5.0, the walker and indexer are FS-agnostic: `repo.WalkFS(fs.FS, Options)` and `search.FromFS(fs.FS, Mode, …)` are the canonical entry points; `repo.Walk` and `search.FromPath` are retained as one-line deprecated wrappers around `os.DirFS(root)`. This unlocks `embed.FS`-backed indexes (agent sandboxing), `fstest.MapFS`-backed indexes (testing), and any other `fs.FS` implementation (tarballs, git tree objects, in-memory snapshots) without ken having to know about them. The watch path (`internal/search/watch.go`, `repo.Matcher`) stays real-FS-only by construction — fsnotify is real-FS-only, not architectural — so `fs.FS`-backed indexes are build-once. See [ADR-014](DECISIONS.md#adr-014-fsfs-as-canonical-walkerindexer-surface) for the decision rationale.

### Nested `.gitignore` — per-directory scope stack (v0.5.0)

Also as of v0.5.0, the walker honors `.gitignore` files in every directory, not just the root. `WalkFS` maintains a per-directory scope stack: at each directory visit it loads `<dir>/.gitignore` (if present) into a `scopedGitignore{dir, gi}`, and rules from outer scopes evaluate first, inner scopes last, with last-match-wins across the union. Inner scopes can both add new ignores and re-include via `!pattern`. The handwritten rule engine (`compileRule`) is unchanged — the new work is scope orchestration, not pattern rewriting. `Matcher` (used by the watch path) gains the same nested awareness via a one-shot tree walk at construction; `.gitignore` changes after construction require a full re-index. The motivating field issue ([#5](https://github.com/townsendmerino/ken/issues/5)) was monorepo `node_modules/` exclusions buried in per-package `.gitignore` files; see [ADR-015](DECISIONS.md#adr-015-nested-gitignore-support-via-scope-stack-on-existing-rule-engine) for the alternatives considered (sabhiram swap, go-git matcher).

### Database schema indexing — two-tier (v0.7.0)

v0.7.0 extends ken's corpus from "files" to "files + database schemas." Two new packages land together (ADR-017):

- **`internal/sql`** — static `.sql` file parser. Tokenizer-and-dispatcher pattern: a statement splitter aware of line/block comments, single/double quotes, Postgres dollar-quoting, and paren depth; per-statement handlers for CREATE TABLE / INDEX / VIEW and ALTER TABLE. Emits one denormalized "for retrieval" chunk per database object. Wired into `internal/search`'s chunk dispatch (`chunkOneFile`) so both `cmd/ken-mcp`'s watch path and `mcp.Run`'s build-once path emit structural SQL chunks alongside the regular file chunks. **Note**: this is the one chunker where the rendered chunk text is NOT a byte-slice of source — it's a structured rendering optimized for BM25 + Model2Vec retrieval. Source provenance is preserved via the `-- file: <path>` header on every chunk.

- **`internal/db`** — live database introspection. v0.8.1 supports Postgres (`github.com/jackc/pgx/v5`), SQLite (`modernc.org/sqlite`), MySQL (`github.com/go-sql-driver/mysql`), and MariaDB (wire-compatible via the same `go-sql-driver/mysql`; v0.8.1 Part B promoted it to first-class via ADR-021 + a CI matrix `mariadb:11-jammy` slot + `normalizeMySQLIntType` to converge MariaDB's legacy `(N)` integer display widths onto MySQL 8.x's bare form so chunks stay byte-identical across engines) — all pure Go, no cgo. Driver defaults are audited for stdout cleanliness: pgx's Tracer is nil, modernc/sqlite has no protocol logger, and the MySQL driver's package-level logger writes to stderr. `IndexSchema(ctx, opts)` runs the per-category queries (tables + columns, constraints, indexes, FK references in both directions, views, function/routine signatures) and emits chunks with a freshness header. `Refresher` orchestrates the reindex layers (startup once, periodic via `Options.ReindexInterval`, manual via `Refresh`); all share one mutex-serialized `IndexSchema` call and reuse ADR-012's atomic snapshot-swap via `WatchedIndex.SetExtraChunks`. **v0.8.0 (ADR-020)** adds `internal/db.Listener` — a Postgres-only LISTEN/NOTIFY push path that converges on the same `Refresher.Refresh` entry point as the interval ticker and SIGHUP handler. The listener uses a dedicated `pgx.Conn` (not the introspection pool's), debounces notification bursts (50ms window) into one refresh, and reconnects with exponential backoff on connection drops. Operators activate it with `KEN_DB_LISTEN=1` after running the embedded one-time setup script (`ken-mcp print-listen-script | psql $DSN`). LISTEN/NOTIFY **supplements** the interval ticker rather than replacing it — both can run concurrently with `Refresher`'s mutex serializing; interval polling acts as defense-in-depth backstop in case the NOTIFY connection drops silently. MySQL and SQLite have no equivalent push mechanism and continue with interval polling. **v0.8.0 Part 2** adds `Refresher.TryRefresh` (mutex `TryLock`; returns `ErrReindexInProgress` on contention) and the `reindex_db` MCP tool that calls it — the fifth trigger source after startup / interval / SIGHUP / LISTEN. The four blocking sources keep using `Refresh` (their semantics want to serialize, not skip); the agent-callable tool uses `TryRefresh` (fail-fast so an agent in a tight loop sees an explicit `already in progress` signal instead of unbounded queueing). All five converge on the same `doRefresh` body, preserving the v0.7.0 serialization invariant. **v0.8.0 Part 3** extracts the pure-Tier-2-mechanics lifecycle into `internal/db.SetupTier2` (initial IndexSchema + Refresher + interval ticker + LISTEN/NOTIFY listener) and adds the opt-in `mcp/db` package. `mcpdb.Setup` returns a `*mcpdb.Refresher` satisfying the new `mcp.DBIntegration` interface (bundling tool invocation + chunk-integration `Start`); SDK authors pass it as `mcp.Options.DB`. The package-split design preserves the v0.6.0 binary-size contract: `mcp` package stays free of DB driver imports (enforced by `TestBinary_MCPPackageStaysDBFree`), and SDK authors who don't need DB support pay no driver-tree cost. **Chunk integration is end-to-end**: `mcp.Run` holds the embedded `*search.Index` in `atomic.Pointer[search.Index]` and the `Refresher.Start` callback rebuilds via `*search.Index.WithExtraChunks` (a new immutable-rebuild primitive that retains the model + vecs slice for re-embedding extras), so DB chunks captured by `reindex_db` / LISTEN / interval refreshes become searchable in the next agent query.

The composition seam between FS chunks and DB chunks is `WatchedIndex.SetExtraChunks([]chunk.Chunk)`. The published Index is always `chunks ∪ extraChunks` — FS chunks come from the fsnotify watch path; extras come from the orchestrator (`cmd/ken-mcp/wireDBTier2`). Both update the snapshot via the same atomic.Pointer.Store path.

Engine scope is **Postgres + SQLite + MySQL + MariaDB as of v0.8.1**. The `internal/db.IndexSchema` entry point dispatches on the DSN scheme: `postgres://` / `postgresql://` → `indexSchemaPostgres` (the v0.7.0 path); `sqlite://` / `sqlite3://` → `indexSchemaSQLite` (sibling file `sqlite.go`, ADR-018, pure-Go `modernc.org/sqlite` driver, no cgo); `mysql://` or the native go-sql-driver form (detected by `@tcp(` / `@unix(` substring) → `indexSchemaMySQL` (sibling file `mysql.go`, ADR-019, pure-Go `github.com/go-sql-driver/mysql` driver). **MariaDB shares the MySQL code path + driver** (wire-compatible); v0.8.1 Part B (ADR-021) promotes MariaDB from "compatible but not CI-tested" to first-class via `mariadb:11-jammy` in the CI matrix + the existing integration suite parameterized over both engines, with `normalizeMySQLIntType` stripping the legacy `(N)` integer display widths MariaDB still emits so cross-engine chunks stay byte-identical. The `Options` struct + `Refresher` + `WatchedIndex.SetExtraChunks` machinery is engine-agnostic and reused unchanged. v0.7.2 also adds **schema filtering** for Postgres + MySQL (and via the shared code path, MariaDB) via `KEN_DB_SCHEMAS` (allow-list) and `KEN_DB_EXCLUDE_SCHEMAS` (deny-list); SQLite is single-schema and ignores them with a debug log. The canonical filter source is `internal/db.filterSchema`, applied per-row in each engine's introspection path so default exclusions (`pg_catalog`, `information_schema`, `mysql`, `performance_schema`, `sys`) are always honored regardless of user lists; both env vars set → allow-list wins with a stderr warn. See ADR-017 for the v0.7.0 alternatives considered, [ADR-018](DECISIONS.md#adr-018-sqlite-engine--migration-history-folding-via-lightweight-alter-replay) for the v0.7.1 SQLite + migration-folding additions, [ADR-019](DECISIONS.md#adr-019-mysql-engine--schema-filtering-for-multi-schema-dev-databases) for the v0.7.2 MySQL + filtering additions, and [ADR-021](DECISIONS.md#adr-021-mariadb-first-class-engine-support-v081-part-b) for v0.8.1 Part B's MariaDB promotion + the audit findings (Tier 1 integer-width normalization shipped; Tier 2 DEFAULT-expression + Tier 3 view-body parenthesization differences documented as deliberately-non-normalized).

v0.7.1 also adds **Tier-1 migration-history folding** (ADR-018) — when `internal/search`'s walker detects a directory of numbered `.sql` migration files (Goose / Rails / Flyway / Alembic patterns), `internal/sql.FoldMigrations` replays CREATE TABLE + later ALTER TABLE statements into one "current state" chunk per table. The lightweight approach (parse ALTER, mutate in-memory column list, emit folded chunk) covers ADD COLUMN / DROP COLUMN / ALTER COLUMN TYPE / ADD CONSTRAINT / DROP CONSTRAINT. **v0.8.1 Part C ([ADR-022](DECISIONS.md#adr-022-rename-column--rename-constraint-folding-via-eager-application-v081-part-c))** extends the replay to handle **RENAME COLUMN + RENAME CONSTRAINT** via eager application: `applyColumnRename` mutates the in-flight `foldedTable.columns[i].name` directly so subsequent ALTERs see the post-rename state, and `renameInFirstParens` rewrites this-table column references inside constraint strings via a word-boundary regex scoped to the first parenthesized group (so FK source-side column lists get rewritten while FK target-side `REFERENCES other(remote)` references are left verbatim — cross-table FK target-side propagation remains out of scope per ADR-022's per-table scope). RENAME folding is a **Tier-1 SQL chunk-content fidelity improvement, NOT a recall / search-ranking improvement** — `docs/BENCH.md`'s hybrid-retrieval recall@10 numbers measure a different system. Unfoldable statements (missing source column from operator typo, anonymous constraint with no name to match, MySQL `CHANGE` rename-and-retype, type conflicts, missing CREATE TABLE) emit BOTH the original per-file chunk AND the folded chunk so the agent never sees less information than v0.7.0. Opt-out via `KEN_SQL_NO_AUTO_MIGRATIONS=1`.

The PII stance is **documentation + sane defaults**, not engineered controls. Schema-only is the default; row sampling is opt-in via `KEN_DB_SAMPLE_ROWS`; every chunk carries a freshness header naming the engine + host; the README is explicit about not pointing this at production data. ADR-017 documents why we rejected column-exclusion DSLs and similar safeguards (complexity tax for a single concern, false sense of security, inconsistent with ken's "small surface" ethos).

### Embedded-corpus MCP — `mcp.Run` library function (v0.6.0)

v0.6.0 introduces a second way to run ken's MCP server: as a **library function** that serves a single fixed `fs.FS` corpus, intended for SDK authors who want to ship their docs as a single static binary via `//go:embed`. The new package is `github.com/townsendmerino/ken/mcp` (the same package that already housed the `Cache`-backed multi-repo server logic); the new entry point is `mcp.Run(ctx, fsys, opts) error`.

Two modes now coexist by design:

- **Code search (`cmd/ken-mcp`).** Multi-repo, per-call path/URL resolution, fsnotify-driven live re-indexing (ADR-012), LRU cache + singleflight dedup. The agent's `repo` argument is honored. Unchanged from v0.5.0.
- **Docs serving (`mcp.Run`).** Single fixed corpus rooted at `fsys`, model loaded from `Options.ModelFS` (typically `//go:embed model/*`) or `Options.ModelDir`, no watch (the corpus is static-by-construction at `Run` time), no cache. The agent's `repo` argument is accepted (wire-format compatibility with semble) but logged-and-ignored.

Tool wire format and arg schemas are identical between modes, so agents trained against one work against the other. The leveled logger and validation helpers (`mcp.Logger`, `mcp.ValidateEnum`) moved out of `cmd/ken-mcp` into the `mcp` package so both paths share one logger type; `cmd/ken-mcp/env.go`'s helpers now wrap `mcp.ValidateEnum` with an `os.Getenv` lookup. The canonical worked example is [`cmd/ken-mcp-docs/`](../cmd/ken-mcp-docs/) — a ~20-line `main.go` that bakes ken's own docs + the Model2Vec model into a 74 MB single static binary, built via [`scripts/build-docs-mcp.sh`](../scripts/build-docs-mcp.sh). See [ADR-016](DECISIONS.md#adr-016-embedded-corpus-mcp-build-pattern-via-mcprun-library-function) for the rationale, alternatives considered, and the "why not per-language treesitter sub-packages" finding.

Supporting changes that landed alongside `mcp.Run`:
- `embed.LoadFromFS(fs.FS, dir) (*StaticModel, error)` — canonical model-loading entry; `embed.Load(modelDir)` is now a deprecated wrapper.
- `internal/chunk/markdown` — heading-aware chunker for documentation corpora (heading-bounded sections, atomic fenced-code / tables / lists, frontmatter handling, byte-fidelity preserved). Registered as `"markdown"` in the chunker registry.
- Chunker side-effect registration moved out of `internal/search` into the binaries that want each optional chunker. `internal/search` keeps the `regex` (universal default) blank-import; `cmd/ken` and `cmd/ken-mcp` add `treesitter` + `markdown`; `cmd/ken-mcp-docs` adds only `markdown`. This is what keeps **~26 MB of binary inflation** out of the docs binary — the cost of importing `internal/chunk/treesitter` against `gotreesitter v0.18.0`: the `gotreesitter/grammars` embed.FS payload is ~19 MB on-disk for 206 `.bin` blobs, and the linked binary grows by ~26 MB once you add parser runtime + symbol overhead (darwin/arm64; measured by building `cmd/ken-mcp` with and without the blank import). Per-binary chunker selection; not per-grammar — the embed bundle is monolithic at the source layer, per [ADR-023](DECISIONS.md#adr-023-gotreesitter-grammar_subset-machinery--binary-size-reduction-outcome-v082-investigation-outcome).

### Pre-built embedded indices — `mcp.Run` cold-start optimization (v0.8.3)

v0.8.3 closes the cold-start loop v0.6.0 left open. The expensive part of building an `*Index` is the per-chunk `model.Encode` call (linear in corpus size; dominates startup for any semantic / hybrid embedded corpus). v0.8.3 adds **serialize/deserialize primitives** in `internal/search` so SDK authors can pre-build their index once at `go generate` / build time and ship the bytes inside their `//go:embed` corpus, skipping the per-launch walk + chunk + embed pass.

Three surfaces land together (ADR-024):

- **`search.BuildAndSerializeIndex(fsys, opts) ([]byte, error)`** — library function. Wraps the existing `walkAndChunkFSWithModel` + a new `serializeIndex` internal helper. SDK authors who script the build call this directly; the CLI subcommand is a thin operator-facing wrapper.
- **`search.LoadSerializedIndex(data, opts) (*Index, error)`** — library function. Validates the on-disk header + CRC32 trailer, reconstructs the chunks slice + embedding matrix, calls the existing `BuildIndex` to re-tokenize for BM25 + wire up `ann.Flat`. Returns typed errors (`ErrCorrupt` / `ErrFormatVersion` / `ErrModeMismatch` / `ErrChunkerMismatch` / `ErrModelRequired`) so callers can decide whether to fall back or hard-fail.
- **`ken build-index <corpus> -o <output_path>`** — CLI subcommand. Model resolution priority order matches `ken index` / `ken search` (`--model` flag → `KEN_MODEL_DIR` → `~/.ken/model` → `./testdata/model`). Writes atomically (`<output>.tmp` + `os.Rename`); auto-creates the parent directory.

Two `mcp.Run` integrations:

- **Convention-over-configuration auto-discovery.** `mcp.Run` reads `corpus/.ken/index.bin` from the supplied `fs.FS` (if present). SDK authors who follow the convention add zero `main.go` lines from the v0.6.0 baseline; only their build script gets the extra `ken build-index` call.
- **`Options.PrebuiltIndex []byte` explicit override.** For SDK authors using a non-conventional layout (index in a sibling `embed.FS`, index outside the corpus root, etc.). When set, the explicit bytes win and auto-discovery is skipped.

**Lazy fallback on any load failure** keeps the optimization opt-in to the upside, never opt-in to the failure mode: corrupt bytes, format-version mismatch, mode mismatch, chunker mismatch — all log a stderr warning naming the reason + suggest `ken build-index` to refresh, then fall back to `search.FromFSWithModel` (the v0.6.0 build-from-corpus behavior). The pre-built path is purely an optimization, never a requirement; a stale or corrupt pre-built file gets a slower-but-still-working binary, not a crash.

**Walker `.ken/` prune.** `internal/repo`'s walker (both `WalkFS` and `Matcher.ShouldIndex`) skips `.ken/` directories analogously to the existing `.git/` prune. Without this, the pre-built index file would be chunked as part of the corpus on the lazy-fallback path. No env var; convention.

**Binary format spec.** Custom binary with `"KEN1"` magic + uint32 format-version gate + informational ken-version string + len-prefixed chunks/vecs sections + CRC32 IEEE corruption trailer. The format reference lives at the top of `internal/search/index_serialize.go`. Internal-only — not a public API; ken's own serialization for its own use. JSON / gob / protobuf were considered + rejected (ADR-024's alternatives).

**BM25 internals are NOT serialized.** `BuildIndex` re-tokenizes every chunk on load and rebuilds postings + df + docLen from the chunks slice — deterministic by construction (chunks come from `repo.WalkFS` in lexical order). The cost is negligible compared to the embedding matrix the optimization actually saves.

**Model-reference handling.** Semantic / hybrid `ix.Search` re-encodes the user's query string at query time (the pre-built matrix only covers the corpus). `LoadOptions.Model` is therefore mandatory for non-BM25 modes (returns `ErrModelRequired` if missing); the resulting `*Index` carries the supplied model, so `WithExtraChunks` works on loaded indices the same way it works on freshly-built ones. Combination "pre-built index + `mcp/db.Setup` chunk integration" works out of the box.

This is a **cold-start time** optimization, not a retrieval-quality change — `docs/BENCH.md`'s hybrid-retrieval recall@10 numbers measure a different system and are unaffected. Same calibration discipline as v0.8.1 / v0.8.2: name the actual gap closed, do not over-claim across system boundaries.

## 2. Code chunking without cgo

Tree-sitter is how semble (via Chonkie) gets sub-sentence chunks that respect function/class boundaries instead of cutting through them. Lose that and retrieval quality drops, because a chunk that splits a function in half is worth less to a code search. So this is the choice that materially affects NDCG.

Strategy: **all three pure-Go options ship as runtime-selectable chunkers** behind a `Chunker` interface, picked via `ken --chunker=regex|chroma|treesitter` (or the config file equivalent). v1 ships Option C only; B and A are documented future paths. Users self-select on the parity-vs-coverage-vs-binary-size axis.

The interface:

```go
type Chunker interface {
    Chunk(source []byte, language string, chunkSize int) ([]Chunk, error)
    SupportedLanguages() []string
    Name() string // "line" | "regex" | (future) "chroma" | "treesitter"
}
```

**The interface was narrowed from an earlier sketch.** The `ctx context.Context` first parameter was dropped: regex chunking is synchronous and fast, nothing in Option C uses cancellation, and a context can be threaded later without breaking B/A. A filename parameter was likewise *not* added; a chunker only needs the language, and the orchestration layer stamps `Chunk.File` after `ChunkFile` routing. The registry lives in `internal/chunk/registry.go` (a `Register`/`Get` map of chunker *instances*, not factories — chunkers are stateless). `line` self-registers in `chunk`'s own `init()`; `regex` self-registers from `internal/chunk/regex` in its `init()`, blank-imported by `internal/search` — `chunk` must not import its sub-chunkers (import cycle), so registration is decoupled the `database/sql` way. `chunk.ChunkFile` does extension→language routing and falls back to the `line` chunker for any language the chosen chunker does not support.

### Option C — Hand-rolled, per-language regex chunkers (v1 default)

**Parity:** Variable, and tunable. For the top 5 languages (Python, Go, TS, Java, Rust) hand-rolled rules match function/class boundaries quite precisely because the surface syntax is regular enough. For 19 languages it becomes a maintenance pit, but v1 only needs the top 5.
**Cost:** Pure Go, zero extra deps, fastest at runtime (regex matching on lines is faster than lexing or parsing), no binary bloat. Engineering cost is linear in language count.
**Build path:** Generic chunker that walks lines, scoring each as a candidate chunk boundary using per-language patterns (def/func/class/impl markers, brace depth for C-likes, indent dedent for Python). When the accumulated chunk hits `chunk_size`, snap to the nearest candidate boundary. If a single definition is itself larger than `chunk_size`, fall back to line-boundary splitting within it (byte-fidelity preserved).
**Per-language rules (as-built — see `internal/chunk/regex/<lang>.go`):**
- **Python:** `^(async\s+)?def\s+\w+`, `^class\s+\w+`. The leading `\s*` in the original sketch was removed because the indent strategy already enforces column 0; keeping `\s*` wrongly made indented methods boundaries (it would have split classes through their methods). Decorators `^@[\w.]+` and a preceding `^#` comment block *attach* to the def below. v1 is top-level-only — methods inside a class are not boundaries (a class is kept whole, or line-split if it alone exceeds `chunk_size`). See §10 for the deferred class-body-aware mode.
- **Go:** `^func\b`, `^type\b`, `^var\b`, `^const\b` at brace depth 0. `var f = func(){…}` / `var H = http.HandlerFunc(func(){…})` must snap on the `var` line (the closure's braces raise depth, but the `var` line itself is depth 0). Doc `//` / `/* … */` attach.
- **TypeScript/JavaScript:** classes, `function`, `interface`, `type X =`, `enum`, `namespace`, and arrow-fn / function-expr consts: `^(export\s+)?(const|let|var)\s+\w+\s*(:[^=]+)?=\s*(async\s+)?(\([^()]*\)|\w+)\s*(:[^=]+)?=>` (the original sketch's `const \w+ = (async)?\(` matched neither `=>` nor the type-annotated `const f: T = () =>`). Class members are depth-1 boundaries (a big class splits between methods); control-flow lines (`if (…) {`) are excluded via a `skip` list. `@decorator` attaches.
- **Java:** type decls, plus member methods/constructors at depth 1: `^<mods>(<generics>)?<returnType> name(...) [throws …] {`. A `skip` list removes `if/for/while/switch/catch/try/synchronized/return/new …`. Javadoc `/** */` and `@Annotation` (incl. multi-line) attach.
- **Rust:** `fn`/`struct`/`enum`/`trait`/`union`/`mod`/`type`/`const`/`static`/`macro_rules!` with full `pub(...)`/`async`/`unsafe`/`extern` qualifiers; `impl` is a depth-0 boundary and `impl` methods are depth-1 boundaries. Attributes `^#!?\[` and `///`/`//!` docs attach. **Scanner caveat:** `'` is *not* treated as a string delimiter for Rust — `'a` is a lifetime, not a char; mis-scanning it would corrupt brace depth far more often than the rare char-literal-containing-a-brace it would fix. Depth errors only ever yield a sub-optimal boundary, never data loss (chunks are always a contiguous byte partition).

**Invariant tested per language:** byte-fidelity — concatenating the produced chunks in order reproduces the source exactly.

### Option B — Chroma lexers (deferred)

**Parity:** Lower than C for languages you've tuned by hand, but immediately covers ~200 languages. Chroma is a lexer not a parser — it gives a token stream with classes (`Keyword`, `NameFunction`, `Punctuation`) but no tree. Function/class starts are detected heuristically from token sequences. Works well for most languages and badly for whitespace-sensitive ones (Python, Haskell) and overloaded keywords.
**Cost:** One dep ([`github.com/alecthomas/chroma/v2`](https://github.com/alecthomas/chroma)), pure Go, fast, no native deps.
**Build path:** Iterate `Iterator.Tokens()`, walk forward to find chunk boundaries at `Keyword` tokens like `def`/`func`/`class`/`fn`. Same registry registration as the regex chunker.
**Verdict:** The "broad coverage, modest quality" option. For a polyglot repo where regex chunker doesn't cover a needed language. See §10 for the trigger.

### Option A — Tree-sitter via `gotreesitter` (v0.2.0)

**Parity:** Highest. Same parser, same grammars, same chunk boundaries as Chonkie.
**Cost:** ~+20 MB binary with grammars embedded (12 MB → 32 MB for the 19 benchmark languages). Per-parse latency at cgo parity (~2 ms full parse; faster on incremental). No native deps, no extra runtime dirs.
**Build path:** [`github.com/odvcencio/gotreesitter`](https://github.com/odvcencio/gotreesitter) — a **pure-Go reimplementation** of the tree-sitter runtime (parser, lexer, GLR, queries, cursor). Ships ~206 grammars as embedded compressed blobs in a single shared `embed.FS` (per the v0.18.0 layout audited in [ADR-023](DECISIONS.md#adr-023-gotreesitter-grammar_subset-machinery--binary-size-reduction-outcome-v082-investigation-outcome)); the `grammar_blobs_external` build tag externalizes them via `GOTREESITTER_GRAMMAR_BLOB_DIR` and is available upstream but NOT used by ken's releases — it breaks ken's single-static-binary contract by requiring operators to ship grammar blobs as sidecar assets. ken's `.goreleaser.yml` builds the default (embedded) form for both `ken` and `ken-mcp` binaries.
**Chunking algorithm:** cAST split-then-merge ([arXiv 2506.15655](https://arxiv.org/html/2506.15655)). Implemented in `internal/chunk/treesitter/cast.go`. Pass 1 walks the AST top-down: if a node's token count exceeds `chunkSize`, recurse into its children. Pass 2 greedily merges adjacent under-sized siblings to maximize density. This is the same algorithm Chonkie uses (Chonkie's `CodeChunker` is a 50-line shim that delegates to `tree-sitter-language-pack`'s Rust `process()`, which is itself a cAST implementation).
**Verdict:** v0.2.0's tentpole. Closes the v0.1.0 chunker-driven NDCG gap (Python tracks semble well at +0.003 but go/rust/zig diverge −0.03 to −0.05 — those are exactly the languages where regex boundary detection diverges most from an AST). Coexists with the regex chunker via the existing `--chunker=` flag; users can choose at runtime.

**Pivot from WASM (recorded for future readers):** DESIGN.md v0.1.0 specified `tetratelabs/wazero` + tree-sitter's WASM core + per-language `.wasm` grammars as the path. That route required us to write the binding ourselves — the only wazero-based wrapper was `malivvan/tree-sitter` (3 stars, dormant). In May 2026 `odvcencio/gotreesitter` appeared (HN: `news.ycombinator.com/item?id=47155597`): pure-Go re-implementation of the tree-sitter runtime, embedded grammars, cgo-parity benchmarks. The "pure-Go, no cgo" constraint that motivated WASM is satisfied just as well by a direct Go port — and we skip ~1–2 weeks of wazero plumbing. The WASM rationale is preserved here so the design history is legible, but the implementation is gotreesitter.

### Why C first

Regex chunkers reach a working end-to-end pipeline fastest, with zero extra dependencies. Stages 3–5 can be validated against Python semble while the chunking story is still refined. The risk of starting with B (Chroma) is spending time on token-stream heuristics that don't help the rest of the pipeline mature. The risk of starting with A (WASM tree-sitter) is spending a week on grammar loading before the rest of the pipeline exists to validate against.

Adding B and A becomes one more implementation against the same interface and registering it. The interface having multiple consumers from day one stress-tests it; v1 with only C still benefits from the seam because the line-chunker fallback uses the same shape.

## 3. Tokenizer port

This is the question that decides whether the rewrite is a weekend or a quarter.

### What `potion-code-16M` actually uses

Inspected against the live HF artifacts:

| Field | Value |
|---|---|
| `tokenizer.json` `model.type` | `WordPiece` ✓ |
| Vocab size | **61,826** |
| `model.continuing_subword_prefix` | `##` |
| `model.unk_token` | `[UNK]` |
| `model.max_input_chars_per_word` | `100` |
| `normalizer.type` | **`BertNormalizer`** (single fused node — *not* a Sequence of NFD/Lowercase/StripAccents) |
| Normalizer config | `clean_text:true`, `handle_chinese_chars:true`, `strip_accents:null`, `lowercase:true` |
| `pre_tokenizer.type` | `BertPreTokenizer` (no further config) |
| `added_tokens` count | **2 only** — `[PAD]`=0, `[UNK]`=1 |
| `post_processor` | `null` (no `[CLS]`/`[SEP]` wrapping) |
| `tokenizer_config.json` | **404 — does not exist.** All settings live in `tokenizer.json` |

This is not a typical BERT setup. There's no `[CLS]`/`[SEP]`/`[MASK]`, no post-processing template, no separate `tokenizer_config.json`. The implementation footprint is smaller than a full BERT tokenizer port.

WordPiece itself is the easy case: normalize → pre-tokenize on whitespace/punctuation → for each word, greedy-longest-match against the vocab using `##` continuation prefixes for non-initial pieces. About 200 lines of Go. The normalizer and pre-tokenizer are the parts that need exact parity.

### The plan: hand-roll WordPiece

ken hand-rolls WordPiece tokenization rather than depending on `sugarme/tokenizer`. ken doesn't need decoding, special-token tricks, or fast-tokenizer offsets — just `text → []int32` of token IDs. Hand-rolling trades ~400 lines of code for zero external tokenizer dependency, no port-drift risk, full control over edge cases, and a parity harness that serves as exhaustive tests rather than spot-check assertions against an upstream library that itself can drift.

The algorithm has three layers that match the HF `tokenizers` pipeline exactly:

**Added-tokens carve-out** (runs *before* normalization — matches HF's `AddedVocabulary` semantics for `normalized=false, single_word=false`):
- Scan the raw text. At each position, match added-token literals longest-first; on a hit, emit the added-token id atomically and advance past it. The non-matched runs go through the normalize → pre-tokenize → wordpiece pipeline below. Skip this loop fast-path when `len(addedKeys) == 0`.

**BertNormalizer** (single fused pass, replicating HF's Rust impl):

1. `clean_text` — drop NUL / U+FFFD / `is_control` chars; replace `is_whitespace` chars with a regular space. **Order matters:** `is_control` is checked before `is_whitespace`. Cc characters like VT (`\v`) and FF (`\f`) are also in the Unicode `White_Space` property; HF drops them as control rather than turning them into spaces. (`\t` / `\n` / `\r` are exempted from `is_control` so they fall through to whitespace replacement.)
2. `handle_chinese_chars` — wrap each CJK char in spaces (so they tokenize individually).
3. Strip accents (combining marks) — `strip_accents:null` + `lowercase:true` triggers accent stripping in HF's BertNormalizer (the null default tracks `lowercase`).
4. Lowercase (Unicode-aware via `strings.ToLower`; German ß is preserved, not casefolded to "ss").

`is_whitespace` is the Unicode `White_Space` property (matches Rust `char::is_whitespace`, the HF source) — `Zs` alone misses U+2028 (Zl) and U+2029 (Zp). Combining-marks stripping happens NFD-decompose + drop Mn category; ß has no NFD decomposition so it survives.

**BertPreTokenizer**:

1. Split on whitespace.
2. Within each whitespace-split token, further split on punctuation, keeping punctuation as its own token.

**WordPiece** — runs per pre-tokenized word:

1. If the word is in the `added_tokens` map (defensive — the carve-out above handles the common case), emit its ID directly.
2. Otherwise, greedy longest-match against the vocab from the left.
3. Subsequent matches within the same word use the `##` continuation prefix.
4. If any prefix doesn't match, emit `[UNK]` (id=1) for the whole word.
5. Words longer than `max_input_chars_per_word` (100) emit `[UNK]` directly.

No post-processor — output token IDs as-is, no `[CLS]`/`[SEP]` wrapping.

Implementation lives in `internal/embed/tokenize.go` with vocab loaded from `tokenizer.json` (which is JSON — `encoding/json` parses it directly, no extra deps). Three maps/slices: `addedTokens map[string]int32` (size 2) for the pre-WordPiece lookup; `addedKeys []string` sorted longest-first for the carve-out scan; `vocab map[string]int32` (size 61826) for WordPiece matching. All immutable after load and goroutine-safe.

### Risk register (tokenizer)

**Risk A — BertNormalizer parity.** The fused HF BertNormalizer has subtle internal ordering (clean_text → handle_chinese_chars → strip_accents → lowercase) and the `is_control`-before-`is_whitespace` rule inside clean_text. The `strip_accents:null` + `lowercase:true` combination triggers accent stripping (`strip_accents` defaults to `lowercase` when null) — easy to get wrong in a hand-roll. Mitigation: the corpus-scale parity harness (below).

**Risk B — `added_tokens` priority.** The lookup must happen *before* WordPiece — and, as the parity harness revealed, *before* normalization too: HF's `AddedVocabulary` carves added-token literals from the raw text and pipelines only the non-matched runs. Trivial in principle, easy to get wrong as a per-word check only.

**Risk C — Whitespace handling around CJK.** `handle_chinese_chars:true` wraps each CJK char in spaces, which interacts with BertPreTokenizer's whitespace split. Verified on Chinese, Japanese kana, Korean hangul inputs.

**Risk D — Long words.** `max_input_chars_per_word:100` means words >100 chars produce `[UNK]` immediately, no greedy match attempted. Code search hits this (minified JS, long hex strings, base64 blobs). Cutoff is implemented and tested.

### Corpus-scale parity harness

`scripts/parity_dump.py` walks the ken repo (and a sibling `/tmp/semble` checkout when present), slices each file into ~200-char pieces, runs `transformers.AutoTokenizer` over each input, and writes `{text, normalized, pre_tokens, ids}` per line to `testdata/parity.jsonl` (gitignored). `internal/embed/parity_test.go` (build tag `parity`) streams those records, runs ken's tokenizer through the same intermediates, and classifies any drift into `normalize` / `pre_tokenize` / `wordpiece` / `other`.

Run with:

```
.venv/bin/python scripts/parity_dump.py
go test -tags=parity ./internal/embed/ -run TestParity -v
```

**11,447 inputs, 0 drift across every category, ~1.1s.** The first pass surfaced three real tokenizer bugs the 18-case `pin_inference.py` fixture had missed; all are now fixed and the test treats any future non-zero category as a regression, not a tuning knob.

The 18-case `testdata/golden.json` (emitted by `pin_inference.py`) is a Stage-3 *embedding* spot-check — it pins the pooling algorithm and a handful of normalization behaviors. The 100k-input tokenizer parity harness above is a separate, broader test; the two cover different things.

## 4. Model2Vec inference format

Worth a dedicated section because the artifact on HF is **not** a typical BERT model file — it's a Model2Vec-specific format and the inference path is not "lookup row in embeddings matrix and average." Getting this wrong silently produces plausible-looking-but-wrong vectors.

### What's actually in `model.safetensors`

Three tensors, not one:

| Tensor       | dtype | shape          | purpose                                                        |
|--------------|-------|----------------|----------------------------------------------------------------|
| `embeddings` | F32   | `[61826, 256]` | the embedding rows (vocab_size × hidden_dim)                   |
| `mapping`    | I64   | `[61826]`      | token-id → embedding-row index (cluster lookup, see below)     |
| `weights`    | F64   | `[61826]`      | per-vocab-token weight (Zipf/SIF coefficient)                  |

And `config.json` contains, full file:

```json
{
  "normalize": true,
  "embedding_dtype": "float32",
  "vocabulary_quantization": 61826
}
```

### What each tensor does

**`embeddings`** is the embedding table. Standard, except it's not necessarily indexed directly by token ID — see `mapping`.

**`mapping`** is a clustering map from PR #271 ("Vocquant"). The Model2Vec project added k-means deduplication of the vocab: many tokens with similar embeddings can share the same embedding row. So inference is:

```
embedding = embeddings[mapping[token_id]]
```

For `potion-code-16M`, `vocabulary_quantization == vocab_size == 61826`, meaning **no actual deduplication happened** — `mapping` is the identity `[0, 1, 2, …, 61825]`. **But the Go code must still index through it** — future Model2Vec models with quantized vocabs (where `vocabulary_quantization < vocab_size`) need this indirection. Doing `embeddings[token_id]` directly will break on any quantized model.

**`weights`** is a per-vocab-token weight (Zipf/SIF coefficient from PR #174). **Applied at runtime, not pre-baked.** Plain `mean(embeddings[mapping[id]])` produces cosine 0.70–0.99 vs ground truth — wrong. `Σ rows·weights / Σ weights` produces cosine 1.0000000 across all 16 non-degenerate test cases. (`weighted_mean` and `weighted_sum_over_count` differ only by a scalar that L2-normalization cancels.)

### Inference algorithm

```
text
  → tokenize(text)                            # WordPiece, §3
  → ids: []int32
  → if len(ids) == 0: return zero-vector      # empty input edge case
  → rows    = [embeddings[mapping[id]] for id in ids]  // [N, 256]  F32
  → w       = [weights[id] for id in ids]              // [N]       F64
  → v       = Σ (rows[i] · w[i])              # weighted sum, [256]  F64 accumulator
  → v       = v / Σ w                         # weighted mean         F64
  → norm    = √(Σ v[j]²)                      # L2 norm               F64 accumulator
  → if norm < eps: return zero-vector         # all-UNK / degenerate input
  → output  = v / norm                        # L2 normalize          F64 → F32 cast last
  → return output                                                  // [256]  F32
```

**Precision contract — required for ≥ 1 − 1e-5 cosine parity.** Every accumulator on the runtime path (the weighted-sum vector `v`, the weight sum `Σw`, the sum-of-squares for L2 norm) is **float64**. Embedding rows stay float32 in memory; values are widened to float64 only at the multiply-accumulate step, mirroring numpy's `embeddings[...].astype(np.float64)` in `pin_inference.py`. The final cast to float32 happens once, just before returning.

Doing the accumulation in float32 (e.g., keeping `v` as `[]float32` and summing directly) silently drifts cosine below the parity bar on inputs of more than a few dozen tokens, with no error or warning. This is the single most likely silent failure mode for a re-implementer.

Two edge cases the Python reference exhibits and the Go port must handle:

- **All-UNK input** (e.g., a single word >100 chars): tokenizes to a single `[UNK]` whose embedding × weight produces a zero vector; L2 normalize then yields NaN. Python returns the NaN; ken explicitly returns a zero vector and does not pollute downstream similarity computations.
- **Empty string**: tokenizer returns an empty ID list. Return a zero vector; don't divide by zero.

**These two cases are not assertable from `testdata/golden.json`.** The empty-string case has no ground truth in the fixture (`pin_inference.py` `continue`s on a zero-length token list); the all-UNK case is in the fixture but its candidate cosines are `NaN` (zero-norm vector) and its stored `ground_truth` is degenerate. The Go golden test asserts ken's *documented* behavior (return a zeroed `[256]` vector) directly via dedicated subtests, **not** a cosine/byte match against the fixture entries.

`mapping` indirection: confirmed identity for `potion-code-16M` (`mapping[i] == i` for all i), but **the Go code must still index through `mapping`** so future quantized models work. Cheap to do, breaks silently if skipped.

### Loading the safetensors file

Hand-rolled, no dependency (decided in §1's dependency table — a library would obstruct the `mmap` zero-copy view). The format is well-defined: 8-byte little-endian uint64 N, then N bytes of JSON header (tensor name → dtype + shape + offset), then the raw tensor bytes. For ken:

```go
// internal/embed/safetensors.go
type Tensor struct {
    DType  string    // "F32", "F64", "I64"
    Shape  []int
    Data   []byte    // mmap'd view into the file
}

func Load(path string) (map[string]Tensor, error) { ... }
```

`mmap` the file once; each tensor becomes a `[]byte` slice into the mapped region. `embeddings` gets unsafe-cast to `[]float32`, `mapping` to `[]int64`, `weights` to `[]float64`. No allocation, no copy. Shared across all embedder goroutines because read-only.

On-disk dtypes (F32 embeddings, I64 mapping, F64 weights) stay as their native dtypes in memory — *don't* widen the entire embedding matrix to float64 at load time (4× memory blowup for no benefit). The float64 widening happens per-token, per-component, at the inner accumulator (see the precision contract above).

### Cross-check against `model2vec-rs`

The Model2Vec project has an official Rust port at [github.com/MinishLab/model2vec-rs](https://github.com/MinishLab/model2vec-rs). It's the closest reference implementation in a non-Python language and is by the same author as the Python package. When ken's embeddings disagree with the Python reference, diff against model2vec-rs first to see if Rust handles the case correctly — that isolates "Model2Vec algorithm question" from "Python implementation detail."

## 5. Concurrency & performance

The pure-Go decision pairs naturally with a real performance advantage over Python, but the wins aren't where they look at first glance. The query path is already sub-2 ms — parallelizing it gets you microseconds. The indexing path is where Go can pull meaningfully ahead, and the gap exists for a specific reason: Python's hot kernels (Rust HF tokenizers, C tree-sitter, numpy) all release the GIL, but the Python-level orchestration around them does not. So the per-file dispatch loop, the per-chunk tokenize-then-embed sequencing, the BM25 corpus construction — all serialized in CPython. Go gets actual parallelism here for free.

### Where the wins are (indexing)

A bounded-channel pipeline is the natural shape:

```
walker (1 goroutine)
   ↓ paths
reader pool (~4–8 goroutines, I/O bound)
   ↓ (path, bytes)
chunker pool (~GOMAXPROCS goroutines, CPU bound)
   ↓ chunks
embedder pool (~GOMAXPROCS goroutines, CPU bound)
   ↓ (chunk, embedding, tokens)
collector (1 goroutine — builds the dense matrix + BM25 postings)
```

Bounded channels between stages give backpressure, so a slow embedder doesn't blow memory by piling up chunks. The collector runs single-threaded on purpose: the BM25 inverted index and the dense embedding matrix are append-mostly structures whose serial-build cost is dwarfed by everything upstream.

The concrete wins per stage:
- **Reader pool overlaps disk latency with chunking.** On a cold-cache repo this is the difference between 30 ms and 5 ms of effective I/O time.
- **Chunker pool scales linearly with cores up to the parser's intrinsic cost.** Chroma is pure-Go and goroutine-safe; WASM tree-sitter via `wazero` is per-instance-not-thread-safe, so each chunker goroutine needs its own grammar instance (~1 MB each, fine).
- **Embedder pool also scales linearly.** Embedding lookup + mean-pool is `~O(tokens × dim)` per chunk, no shared mutable state if you `mmap` the weights read-only.

Realistic target on a 4-core machine: **80–150 ms cold-index**, vs Python's 250 ms. Not 10x — the Python kernels are already fast — but a clean 2–3x without exotic tricks.

### Where parallelism doesn't help (querying)

Queries run in ~1.5 ms in Python; goroutine scheduling overhead is a non-trivial fraction of that. Concrete things not to parallelize:
- The semantic + BM25 retrievers in parallel. Saves maybe 200 μs at the cost of code that's harder to reason about. Run them sequentially.
- The rerank pass over ~50 candidates. Goroutine-per-candidate is anti-pattern at that scale.
- Anything inside `find_related`. Same reasoning.

Optimize query path with SIMD on the dot products (`gonum/blas/blas32` or hand-rolled AVX2), not concurrency.

### Non-concurrency wins that come bundled with Go

Quietly bigger than the parallelism gains, in some cases:
- **No interpreter startup.** Python's `import numpy` alone is 100–200 ms; full semble startup with `model2vec` import is more. Go cold-start is sub-10 ms. For an MCP server that re-indexes per session, this is the largest absolute win.
- **`mmap`'d embedding table.** Python's `StaticModel.from_pretrained` loads the matrix into Python-managed memory. Go can mmap the safetensors blob directly via `golang.org/x/exp/mmap` or `syscall.Mmap`; OS pages it in lazily and shares it across goroutines without copying. Lower RSS, faster startup, no warmup.
- **No per-chunk Python objects.** Each Python `Chunk` dataclass has refcount, dict, and type overhead. Go stack-allocates the struct in the pipeline and GC pressure stays low — measurable on large repos.
- **SIMD on dot products.** Free latency reduction on the query path that Python can't easily do without dropping to numpy operations the GIL releases for.

### Incremental indexing

A capability the Python version doesn't expose, **landed in v0.3**: an `fsnotify` watcher in a background goroutine debounces filesystem events 2s, then re-chunks/re-embeds only the changed files and publishes a new immutable snapshot via `atomic.Pointer[Index]`. Readers do exactly one atomic load at query entry; writers never block readers and readers never pay a per-query copy. Deletes are tombstoned in-place; compaction is deferred to v0.3.x.

For agents that hold a long-lived MCP session against a repo the user is actively editing, this turns "re-index on every save" into "re-index two files," which is a different category of latency. ken-mcp watches always; `ken index` defaults to `--watch` with `--no-watch` as the v0.2-compatible escape.

Design rationale and the five locked sub-decisions are in [`DECISIONS.md` ADR-012](DECISIONS.md#adr-012-incremental-indexing-via-fsnotify--atomic-snapshot-swap); the implementation lives in `internal/search/watch.go`.

### Sequencing — correctness before parallelism

The indexing pipeline is built single-goroutine first and validated deterministically. Worker pools come later, with a golden-output test that asserts identical results regardless of `GOMAXPROCS`. The single most common way concurrent Go projects go wrong is shipping flaky output ordering as "fast."

A practical seam: an interface

```go
type Indexer interface {
    Index(ctx context.Context, src Source) (*Index, error)
}
```

with two implementations — `serialIndexer` and `parallelIndexer` — and a CLI flag that picks between them. Tests run both, assert byte-equal output.

### Expected end-to-end numbers (rough)

| Stage           | Python | Go (serial) | Go (parallel, 4 cores) |
|-----------------|--------|-------------|------------------------|
| Cold startup    | ~500 ms | ~10 ms     | ~10 ms                 |
| Index repo      | 250 ms  | ~200 ms    | 80–150 ms              |
| Query p50       | 1.5 ms  | ~1.5 ms    | ~1.0 ms (SIMD)         |
| Memory (idle)   | ~300 MB | ~80 MB     | ~80 MB                 |

The startup number is where the user-felt difference lives — most other gains are within an order of magnitude of Python.

## 6. License + attribution chain

Verified license chain:

| Component | License | Owner |
|---|---|---|
| `semble` (Python upstream) | MIT | © 2026 Thomas van Dongen |
| `model2vec` library | MIT | © 2024 Thomas van Dongen |
| `potion-code-16M` weights | MIT | Minish Lab (HF model-card frontmatter) |
| ↳ distilled from `nomic-ai/CodeRankEmbed` | MIT | Nomic AI |
| ↳↳ based on `snowflake-arctic-embed-m-long` | Apache-2.0 | Snowflake |

Every link in the model-weight provenance chain above is permissive and MIT-compatible (MIT ∪ Apache-2.0). ken is MIT-licensed and redistributes the weights freely. The Go-module dependency surface (listed in [`THIRD_PARTY_LICENSES.md`](../THIRD_PARTY_LICENSES.md)) adds a third permissive license family — **MPL-2.0** — via `github.com/go-sql-driver/mysql` v1.10.0 (added in v0.7.2 for Tier 2 MySQL support; see [ADR-019](DECISIONS.md#adr-019-mysql-engine--schema-filtering-for-multi-schema-dev-databases)). MPL-2.0 is file-level copyleft, not viral: it requires modifications to MPL-licensed source files to remain under MPL-2.0, but does not extend to code that merely imports or links to the library. ken consumes `go-sql-driver/mysql` as an unmodified upstream module, so redistribution within ken's binaries is unencumbered.

**Attribution required.** MIT requires attribution preservation; Apache-2.0 has NOTICE-propagation expectations; MPL-2.0 requires the upstream license text to ship with any redistributed source. The repo ships:

- `LICENSE` — ken's MIT license
- `NOTICE` — attribution for redistributed weights:
  - `potion-code-16M` (MIT, © Minish Lab)
  - Upstream `snowflake-arctic-embed-m-long` (Apache-2.0, © Snowflake)
- `THIRD_PARTY_LICENSES.md` — generated listing of every Go-module dep + its license (regenerate via `scripts/gen_third_party_licenses.py`). The MPL-2.0 entry (`go-sql-driver/mysql`) is satisfied here: the upstream module's `LICENSE` file ships with the module via `go mod`, and the generated table preserves the SPDX identifier.

If model weights ship embedded in the binary (via `go:embed`), the NOTICE must too — bake it in alongside or expose via `ken license` / `ken --notice`. The HF repo for `potion-code-16M` has no standalone LICENSE file; the MIT grant is in the model-card metadata only, so the attribution is written by hand rather than copied.

Training-dataset licenses (CornStack `nomic-ai/cornstack-*`) don't encumber the resulting weights and are not a redistribution concern.

## 7. Hybrid retrieval & rerank

Ported verbatim from semble's live source (`search.py`, `ranking/{boosting,penalties,weighting}.py`, `tokens.py`), which diverged materially from the scoping reconstruction.

### Pipeline order

```
RRF fuse  →  boost_multi_chunk_files  →  apply_query_boost  →  rerank_topk(penalise = α<1)
```

Semantic score = cosine similarity (`1 − cosine_distance`); BM25 drops `score ≤ 0` candidates.

### α-weighted fusion

Fusion is **α-weighted**, not an equal-weight sum:

```
combined = α · rrf_sem  +  (1 − α) · rrf_bm25
```

α is the *semantic* weight from adaptive detection: `0.3` for symbol queries (lean BM25), `0.5` for NL. Adaptive weighting re-weights the RRF *inputs*, not a post-fusion boost gate.

### RRF

`1 / (k + rank)`, `k = 60`, rank is **1-indexed** over score-descending order (with the retriever's order preserved on ties).

### Adaptive classifier

A single regex full-matches the stripped query. Symbol iff:
- namespace-qualified (`::`, `\`, `->`, `.`), OR
- leading `_`, OR
- contains an uppercase or underscore, OR
- starts uppercase.

Anything else (plain lowercase word, multi-word phrase) is NL.

### Boosts

Boosts are **additive**, scaled by the candidate set's max score:

```
score += maxScore · multiplier · tier
```

Constants:

| Constant | Value | Applies to |
|---|---|---|
| `_DEFINITION_BOOST_MULTIPLIER` | 3.0 | Chunk defines a queried symbol (symbol queries only) |
| `_EMBEDDED_SYMBOL_BOOST_SCALE` | 0.5 | Chunk defines a CamelCase symbol embedded in an NL query (× the definition boost) |
| `_STEM_BOOST_MULTIPLIER` | 1.0 | NL query keywords match a file/parent-dir stem (× `match_ratio`, gated `ratio ≥ 0.10`) |
| `_FILE_COHERENCE_BOOST_FRAC` | 0.2 | Promote a file's best chunk by `maxScore · 0.2 · fileSum / maxFileSum` |
| stem-tier multiplier | ×1.5 | A chunk whose file stem matches the symbol gets this tier on definition boosts |

Symbol queries get the definition boost only; NL queries get **stem-match** AND **embedded-symbol** boosts.

### Non-candidate injection

The symbol-definition and embedded-symbol scans can **inject non-candidate chunks** (from the full corpus, not just the RRF pool) when their file stem matches the symbol and they contain a definition. The result set is not closed under the RRF candidate pool.

### Penalties (three tiers, multiplicative)

| Constant | Value | Applies to |
|---|---|---|
| `_STRONG_PENALTY` | 0.3 | Test files (per-language), test/spec/`__tests__`/testing dirs, compat/legacy/`_compat`, examples/`_examples`/`docs_src` |
| `_MODERATE_PENALTY` | 0.5 | `__init__.py`, `package-info.java` (re-export barrels) |
| `_MILD_PENALTY` | 0.7 | `.d.ts` declaration stubs |

Plus **file-saturation decay**: during the greedy top-k selection, the 2nd+ chunk kept from the same file is multiplied by `0.5 ** excess`. Penalties are applied only when `α < 1.0` (skipped for pure semantic).

### RE2 adaptations

RE2 has no look-around: `(?<=\s)` becomes `(?:^|\s)` under `(?m)`; semble's `_CAMEL_RE` lookahead is hand-scanned in `camelTokens`. Definition detection uses a fixed keyword list (incl. SQL DDL, matched case-insensitively) rather than the prompt's single regex sketch.

## 8. MCP server

`cmd/ken-mcp` is a drop-in replacement for semble's MCP server. Two tools (`search`, `find_related`) with arg shapes ported verbatim from `/tmp/semble/src/semble/mcp.py`. Same wire format as semble (a formatted markdown string via `_format_results`), so agents already trained against semble work unchanged.

### Hard rule — stdout/stderr contract

stdin and stdout **are** the JSON-RPC channel. ANY write to stdout outside of the SDK's protocol writer corrupts the stream and the agent disconnects with a cryptic JSON-decode error. This is the #1 way new MCP servers fail. `cmd/ken-mcp/main.go` enforces the contract with a comment at the top, a stdlib-`log` redirect to `os.Stderr` in `init()`, no `fmt.Print*` calls anywhere in the binary, and a `go-git` clone with `Progress = nil`. `TestBinary_StdoutIsCleanJSONRPC` builds the real binary and drives a real MCP session through `sdk.CommandTransport` to guard the contract — treat its failure as the stdout-pollution canary.

### Tool surface

| Tool | Args | Returns |
|---|---|---|
| `search` | `query` (string, required), `repo` (string, optional), `mode` (`hybrid` \| `semantic` \| `bm25`, default `hybrid`), `top_k` (int, default **5**) | Markdown string: header + numbered `## N. file:line-line  [score=X.XXX]` fenced blocks |
| `find_related` | `file_path` (string, required), `line` (int, 1-indexed, required), `repo` (string, optional), `top_k` (int, default **5**) | Same markdown shape |

Defaults match semble exactly (`top_k=5`, not 10; both tools have an optional `repo`). Validation errors are returned as TEXT, not MCP-protocol errors, so an agent passing a typo doesn't get disconnected.

### Cache + clone

Per-process repo→Index cache (`mcp/cache.go`):

- Keyed via `NormalizeKey`: http(s) URLs → lowercased host + trailing `/` and `.git` stripped; local paths → absolute. **ssh/git/scp-form rejected at the MCP boundary** with a text error (mirrors semble's MCP-only-http(s) policy).
- LRU bound (`KEN_MCP_CACHE_SIZE`, default 16). Concurrent first-access requests for the same key dedupe via `golang.org/x/sync/singleflight`.
- Eviction invokes `cleanup()` to `rm -rf` temp clone dirs.

http(s) URLs shallow-clone via `go-git` to `$TMPDIR/ken-mcp/<sha256-prefix>/`. No authentication is configured — private repos are out of scope for v1.

### Env vars

- `KEN_MCP_DEFAULT_REPO` — optional pre-indexed source; tools may then be called without `repo`.
- `KEN_MCP_MODE` — `bm25`/`semantic`/`hybrid` (default `hybrid`). Auto-downgrades to `bm25` with a stderr warning if the model dir is unreachable.
- `KEN_MCP_MODEL_DIR` — Model2Vec snapshot dir (must contain `model.safetensors`). Empty ⇒ `bm25`-only.
- `KEN_MCP_CHUNKER` — `regex`/`treesitter`/`line` (default `regex`).
- `KEN_MCP_CACHE_SIZE` — LRU bound (default 16).
- `KEN_MCP_LOG_LEVEL` — `debug`/`info`/`warn`/`error` (default `warn`); all logs go to stderr.

### Install snippets

```bash
# Claude Code
claude mcp add ken -s user -- /absolute/path/to/ken-mcp
```

`~/.cursor/mcp.json` / `.cursor/mcp.json`:
```json
{ "mcpServers": { "ken": { "command": "/absolute/path/to/ken-mcp" } } }
```

`~/.codex/config.toml`:
```toml
[mcp_servers.ken]
command = "/absolute/path/to/ken-mcp"
```

`~/.opencode/config.json`:
```json
{ "mcp": { "ken": { "type": "local", "command": ["/absolute/path/to/ken-mcp"] } } }
```

`.vscode/mcp.json`:
```json
{ "servers": { "ken": { "command": "/absolute/path/to/ken-mcp" } } }
```

## 9. What this doesn't tell you

- **Whether to track upstream.** semble is actively iterating (rerank constants changed between PRs #1, #16, #25). A Go port that wants to stay in sync needs a process for porting rerank changes, or it diverges.

## 10. Risk register

Consolidated deferred items. Each entry: the item, then the trigger that would unblock or motivate it. Items without triggers calcify into permanent TODOs.

- **Python class-body-aware chunking.** v1 is top-level-only; large Django models, SQLAlchemy declarative bases, and ML wrapper classes will line-split through their methods rather than split at method boundaries. Tracked as `TODO(stage4-risk)` in `internal/chunk/regex/python.go`. **Trigger:** Python NDCG measurably below other languages on a real corpus. **Status (v0.1.0):** not triggered — Python NDCG@10 hybrid is 0.869 (semble 0.867, +0.002); Python is in fact the best-tracking supported language.
- **Chroma chunker (Option B).** ~200-language coverage via heuristic Keyword detection. **Trigger:** users with polyglot repos where the regex chunker doesn't cover a needed language.
- **Tree-sitter chunker (Option A).** **Status (v0.2.0): landed via [`gotreesitter`](https://github.com/odvcencio/gotreesitter), shipping as opt-in (`--chunker=treesitter` / `KEN_MCP_CHUNKER=treesitter`); default stays `regex`.** Measured hybrid NDCG: 0.838 (Δ −0.004 vs the regex baseline 0.842 — within noise). Per-language wins: kotlin +0.011, zig +0.013, typescript +0.009, java +0.006, php +0.005. Losses: python −0.009, c −0.017, rust −0.013, lua −0.022. Most languages within ±0.005. The chunker pivot (originally WASM/wazero) is recorded in ADR-010; the default-stays-regex decision is ADR-011. Both grammars have known issues skipped via `kenToTreeSitter`:
  - **C# grammar** OOMs on real-world C# (1.7+ GB RSS during dapper indexing → SIGKILL on all 3 csharp bench repos in the first run). Falls back to the line chunker. **Trigger to revisit:** a `gotreesitter` release that bounds C# parser memory, OR adding a per-parse memory cap at the chunker layer.
  - **Bash grammar** is pathologically slow on real bash-it content (~39% of files time out at 1 s per parse). Falls back to the line chunker. **Trigger to revisit:** a `gotreesitter` release with a faster bash grammar, OR replacing the bash entry with `gotreesitter`'s "shellscript" grammar variant if it matures.
- **NDCG vs semble (target ≈ 0.854).** **Resolved at v0.1.0:** measured at 0.842 hybrid (gap 0.012) on the full published benchmark (63 repos, 1251 queries, semble's own `benchmarks.metrics`). Per-ablation: semantic raw matches semble within 0.003 (validates the embedding + tokenizer + ANN port); BM25 raw at 0.624 vs 0.675 is chunker-driven (the tokenizer is now a verbatim port and contributed only +0.002 of the closing). Per-category hybrid: architecture matches within 0.005, semantic and symbol within 0.017. Reproduce via `docs/BENCH.md`. The closing-the-gap path is the WASM tree-sitter chunker item above; the algorithm port itself is no longer the open question.
- **External benchmark — CoIR-CSN-Python (v0.2.0).** ken evaluated against [CoIR](https://github.com/CoIR-team/coir)'s `CodeSearchNet-python` task: hybrid 0.7839, bm25 0.8743, semantic 0.7405 (1000-query subsample, regex chunker). Surprise worth recording: **on CSN-Python, BM25 outperforms hybrid by 0.09 — opposite of semble's bench.** Not a ken bug. The mechanic is a CoIR dataset artifact: queries are full Python function sources; documents are docstrings extracted from those same functions; the docstring lives inside the query as a literal substring, so any lexical retriever with identifier-aware tokenization wins because BM25 has the answer string as input. ken's α=0.5 RRF fusion then drags hybrid down by averaging in the weaker semantic ranking. This is structural to how CoIR reframed CodeSearchNet for retrieval; it does *not* generalize to natural NL-to-code distributions, where hybrid wins. **ADR-013 investigated this as a possible α-routing trigger and closed as Deprecated** (Proposed → Deprecated, 2026-05-21) once Prompt 22's precondition step inspected the data and surfaced the substring-leak mechanic. See [`DECISIONS.md` ADR-013](DECISIONS.md#adr-013-corpus-adaptive-α--adding-a-third-query-class-branch). Full data in `docs/BENCH.md`.
- **HNSW for the dense retriever.** `internal/ann/flat.go` is exact and fine at repo scale. The eventual swap is local — `internal/ann/` exposes only `Flat.Query(q, k) []Hit`; introducing a `Retriever`-style interface happens when the swap lands, not preemptively. **Trigger:** dense matrix size makes exact cosine the bottleneck on a real workload.
- **Full pathspec gitignore.** `internal/repo/walk.go` ships a deliberate common-subset matcher; `github.com/sabhiram/go-gitignore` is the planned drop-in. **Trigger:** a real repo with nested or exotic gitignore patterns shows incorrect inclusion/exclusion.
- **Persistent on-disk index cache.** Per-process MCP cache only today; semble has none either. **Trigger:** a usage pattern where rebuild cost across process restarts dominates. Distinct from incremental indexing below — this item just skips a rebuild when the corpus is unchanged across processes; it does not handle file-level deltas.
- **Incremental indexing — landed in v0.3 via fsnotify + atomic snapshot swap.** `internal/search/watch.go` wraps an `*Index` in a `WatchedIndex` whose `atomic.Pointer[Index]` is published anew every 2 seconds of edit activity. Readers do one atomic load per Search/FindRelated/ResolveChunk call; writers build a new snapshot off to the side. No reader-side lock; in-flight queries see consistent snapshots. ken-mcp watches always; `ken index --watch` is the default (`--no-watch` is the v0.2-compatible opt-out). Tombstone-deletes-no-compaction: deleted files leave their chunks in the slice with `Tombstoned=true`, and query paths skip them. **Open v0.3.x trigger:** compaction. Memory grows monotonically with cumulative edit volume; multi-day agent sessions on a heavily-edited corpus could hit pressure. **Bonus opportunity (deferred):** `gotreesitter` supports incremental reparsing (~666 ns vs ~2 ms), but the treesitter chunker still discards the parse tree per file. Keeping the tree around would let us reparse only changed byte ranges — orthogonal to v0.3's incremental indexing, would land as v0.3.x optimization. See [`DECISIONS.md` ADR-012](DECISIONS.md#adr-012-incremental-indexing-via-fsnotify--atomic-snapshot-swap) for the full design.
- **Private-repo auth in ken-mcp.** Out of scope for v1 (semble matches). **Trigger:** a user explicitly asks for it; pick an auth model (PAT env? `gh auth` shell-out?) and the corresponding test surface.

## Sources

- [MinishLab/semble](https://github.com/MinishLab/semble)
- [MinishLab/model2vec](https://github.com/MinishLab/model2vec)
- [MinishLab/model2vec-rs](https://github.com/MinishLab/model2vec-rs) — secondary non-Python reference for the inference algorithm
- [chonkie-inc/chonkie](https://github.com/chonkie-inc/chonkie)
- [nlpodyssey/safetensors](https://github.com/nlpodyssey/safetensors) — format reference only; ken hand-rolls its reader (§4)
- [modelcontextprotocol/go-sdk](https://github.com/modelcontextprotocol/go-sdk)
- [odvcencio/gotreesitter](https://github.com/odvcencio/gotreesitter) — pure-Go tree-sitter runtime (ADR-010); v0.2.0's treesitter chunker
- [fsnotify/fsnotify](https://github.com/fsnotify/fsnotify) — v0.3's incremental indexing watcher (ADR-012)
- [tetratelabs/wazero](https://github.com/tetratelabs/wazero) — original Option A WASM runtime (superseded by gotreesitter per ADR-010)
- [alecthomas/chroma](https://github.com/alecthomas/chroma) — Option B lexer (documented future path, never triggered — see §10)
- [sugarme/tokenizer](https://github.com/sugarme/tokenizer) — considered then declined (§3, ADR-003)
- [CoIR-team/coir](https://github.com/CoIR-team/coir) — external NDCG benchmark (CoIR-CSN-Python; `docs/BENCH.md`)
- [Snowflake/snowflake-arctic-embed-m-long (BERT/WordPiece lineage)](https://huggingface.co/Snowflake/snowflake-arctic-embed-m-long)
- [WordPiece tokenization (HF course)](https://huggingface.co/learn/llm-course/en/chapter6/6)
