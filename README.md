# ken

**Fast hybrid code search for agents.** Pure Go, single static binary, drop-in MCP-compatible with [MinishLab/semble](https://github.com/MinishLab/semble) — same tool schemas, same output format, same install steps swapped to a Go binary.

*Built collaboratively: most of the Go implementation written by Claude, with constraints, architectural decisions, and review discipline from [@townsendmerino](https://github.com/townsendmerino). The verbatim-port rule and the corpus-scale parity harness — the things that make this a faithful port instead of an approximate one — came from the human side. See [How this was built](#how-this-was-built).*

[![CI](https://github.com/townsendmerino/ken/actions/workflows/ci.yml/badge.svg)](https://github.com/townsendmerino/ken/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go Reference](https://pkg.go.dev/badge/github.com/townsendmerino/ken.svg)](https://pkg.go.dev/github.com/townsendmerino/ken)
![Go 1.26+](https://img.shields.io/badge/go-1.26%2B-blue)

ken is a Go port of semble. The retrieval algorithm is ported verbatim from semble's `search.py` + `ranking/*.py`; ken adds two things on top: **runtime properties** (single-binary distribution, no Python interpreter import on cold start, no GIL on the indexing pipeline) and **measured agent-input efficiency** (~44× fewer tokens than grep+Read at recall@10 on semble's diverse-query benchmark; at corpus scale — CoIR-CSN-Python's 280K files — corpus-wide grep is functionally impossible and ken's 1,296-token result is the only workable path). The honest tradeoff: ken's recall caps at 82–91% vs grep's ~99%, so exhaustive enumeration (refactors, pre-rename audits) still belongs to grep — but for "find the chunk that answers this," ken wins by 1–2 orders of magnitude on tokens. Full table in [`docs/BENCH.md`](docs/BENCH.md#token-budget-recall--agent-side-efficiency). If you already use semble in your agent, you can swap to ken-mcp without re-prompting; the wire format is the same string semble emits.

## Embedded-corpus build pattern (v0.6.0)

The library form of ken-mcp lets SDK authors ship docs as a **single static MCP server binary**. Write ~20 lines of `main.go`, `//go:embed` your `docs/` and the Model2Vec model, `go build` — push a binary to a GitHub release. Users `brew install`, add one line to their agent config, and their coding agent has high-quality local retrieval over your SDK's docs. No backend, no vector DB to operate, no network egress per query, no "is the cache stale" question — the binary IS the corpus, version-pinned by build artifact.

```go
package main

import (
    "context"
    "embed"
    "io/fs"
    "log"
    "os"

    "github.com/townsendmerino/ken/mcp"

    _ "github.com/townsendmerino/ken/chunk/markdown"
)

//go:embed docs/*.md
var docsFS embed.FS

//go:embed model/tokenizer.json model/config.json model/model.safetensors
var modelFS embed.FS

func main() {
    docsSub, _  := fs.Sub(docsFS, "docs")
    modelSub, _ := fs.Sub(modelFS, "model")
    if err := mcp.Run(context.Background(), docsSub, mcp.Options{
        Mode:        "hybrid",
        ChunkerName: "markdown",
        ModelFS:     modelSub,
        LogWriter:   os.Stderr,
    }); err != nil {
        log.Fatal(err)
    }
}
```

[`cmd/ken-mcp-docs/`](cmd/ken-mcp-docs/) is the canonical worked example — it bakes ken's own [`docs/*.md`](docs/) and the Model2Vec model into a 74 MB static binary built via [`scripts/build-docs-mcp.sh`](scripts/build-docs-mcp.sh). Design and rationale: [ADR-016](docs/DECISIONS.md#adr-016-embedded-corpus-mcp-build-pattern-via-mcprun-library-function).

### Pre-building the index for faster cold start (v0.8.3)

Every `mcp.Run` startup walks the embedded corpus, chunks every indexable file, and (for semantic / hybrid mode) calls `model.Encode` on every chunk to produce the dense embedding matrix. For a small docs corpus the cost is modest; for a larger embedded corpus (~10K+ chunks) it can add seconds-to-minutes of CPU on each process launch.

v0.8.3 ships **pre-built embedded indices** — serialize the index once at build time, ship the bytes inside your `//go:embed` corpus, skip the walk + chunk + embed pass at runtime. Cold start drops from "build the index" to "read pre-serialized bytes + verify header."

Two additions to your build script:

```bash
# Before `go build`, pre-build the index from your corpus.
ken build-index ./corpus \
    -o ./corpus/.ken/index.bin \
    --mode hybrid \
    --chunker markdown \
    --model ~/.ken/model

# Then `go build` as usual — //go:embed corpus picks up
# .ken/index.bin alongside the rest of your files.
go build ./cmd/your-docs-mcp
```

Your `main.go` is unchanged from the v0.6.0 baseline — `mcp.Run` auto-discovers `corpus/.ken/index.bin` from the supplied `fs.FS` and loads it. SDK authors who use a non-conventional layout (index outside the corpus FS, in a sibling `embed.FS`, etc.) can set `mcp.Options.PrebuiltIndex []byte` explicitly.

**Lazy fallback on any load failure** — corrupt bytes, format-version mismatch, mode / chunker mismatch — produces a stderr warning + falls back to the v0.6.0 build-from-corpus path. The pre-built path is purely an optimization, never a requirement; a stale or corrupt pre-built file gets you a slower-but-still-working binary, not a crash. Re-run `ken build-index` to refresh.

Design and rationale: [ADR-024](docs/DECISIONS.md#adr-024-pre-built-embedded-indices-for-mcprun-v083).

### Live demos (v0.1.0)

Two downloadable `mcp.Run` binaries that use this pattern against real codebases, with full audit transcripts:

- **`ken-demo-kubernetes`** — Kubernetes v1.31.0 source, `regex` chunker (Go AST-tracking). 59,795 chunks, 216 MB binary, ≈ 3.9 s to ready, ~60 ms first query.
- **`ken-demo-postgres`** — PostgreSQL 17.0 source, `treesitter` chunker (real C AST, 0% silent fallback verified). 64,506 chunks, 265 MB binary, ≈ 3.5 s to ready, ~30 ms first query.

The 4-second startup is "loads a pre-built index," not "instant" — the writeup linked below has the honest measurement breakdown and the audit that caught two bugs in ken itself before publication.

- Download: [`demos/v0.1.0` release](https://github.com/townsendmerino/ken/releases/tag/demos/v0.1.0) — `darwin/arm64`, `darwin/amd64`, `linux/amd64`, `linux/arm64`.
- Audit transcripts: [`demos/transcripts/`](demos/transcripts) (nine captured agent conversations) + [`demos/transcript-audit-rubric.md`](demos/transcript-audit-rubric.md).
- Writeup: [*I shipped two downloadable code search binaries. The audit caught two bugs.*](https://townsendmerino.github.io/ken/demos-audit/)

### Why this is interesting

- **Zero-infrastructure distribution.** No backend, no vector DB, no per-query cloud calls. The binary IS the corpus.
- **Version-pinned by build artifact.** The corpus and the model and the search algorithm all ship together. There is no "stale index" question — to update, rebuild and re-release.
- **Agent sandboxing by construction (for `mcp.Run` embedded-corpus binaries).** The embedded-corpus build has no path-resolution code, so there is no path-traversal escape path. The corpus is structurally sealed; an agent cannot pivot from "search the docs" to "read the host's secrets." Note this property is specific to `mcp.Run` — `cmd/ken-mcp` against a live filesystem resolves the agent-supplied `repo` argument to a real path on disk and is a different threat model (see [SSRF guard env var below](#mcp-server) for the related defense).
- **Air-gapped friendly.** All queries answered locally, no network egress. For enterprise / restricted-egress environments this is the difference between "we can use this" and "we can't."

For multi-repo code search with live file-watching, use [`cmd/ken-mcp`](cmd/ken-mcp/) directly (below) — the two modes coexist by design.

## Who's this for?

ken has several distinct use cases. Pick the entry point that matches yours; each bullet names the relevant binary or mode and links to the in-depth section where its workflow is documented.

- **AI coding agent users** (Claude Code, Cursor, Codex, opencode, VS Code) — install `ken-mcp` as an MCP server in your agent client. Same `search` / `find_related` tool schemas and wire format as semble, so existing semble configurations work with the `command:` path swapped to `ken-mcp`. See [MCP server](#mcp-server) and [Comparison to semble](#comparison-to-semble).

- **SDK / docs authors shipping a single static binary** — use `mcp.Run` to bake your `//go:embed` corpus + the Model2Vec model into one executable that serves MCP search. No backend, no per-platform asset bundles, version-pinned by build artifact. See [Embedded-corpus build pattern](#embedded-corpus-build-pattern-v060) and [Pre-building the index for faster cold start](#pre-building-the-index-for-faster-cold-start-v083).

- **Backend developers with code + database schemas** — point `ken-mcp` at your repo alongside a local Postgres / SQLite / MySQL / MariaDB dev DB. Code chunks, `.sql` file chunks, and live schema chunks compete in one ranked retrieval. See [Indexing database schemas](#indexing-database-schemas-v070-expanded-in-v071) and [Tier 2 — Live Postgres introspection](#tier-2--live-postgres-introspection-ken_db_dsn).

- **CLI-first code search** — `ken index <path>` + `ken search <path> <query>` for fast local exploration of an unfamiliar repository, with `--json` output for piping into other tools. Pure Go, single static binary cross-compiled to any `GOOS` / `GOARCH`. See [Quickstart](#quickstart).

- **Air-gapped or restricted-egress environments** — embedding inference, BM25 scoring, and fusion all run locally on the CPU. No network calls for search; no external vector DB; no API keys. See [Why this is interesting](#why-this-is-interesting).

## What ken indexes well

ken's hybrid BM25 + Model2Vec retrieval is calibrated for two content types:

- **Source code** — Python, Go, TypeScript, Java, Rust have language-aware chunking via the regex chunker (default) or the optional tree-sitter chunker. Other languages fall back to the line chunker.
- **Documentation** — markdown files (`.md`, `.mdx`, `.markdown`) chunk on heading boundaries, keep code blocks / tables / lists atomic, and handle YAML/TOML frontmatter. Mixed corpora (code + docs in one repo) work out of the box — each file routes to the right chunker by extension.

For plain-text corpora with no code or structured documentation (novels, journals, raw transcripts), the BM25 side works fine in `--mode=bm25`, but the semantic model is code-trained — semantic ranking quality on pure literary prose is unvalidated. If that's your use case, expect BM25 mode to do most of the heavy lifting.

## Indexing database schemas (v0.7.0, expanded in v0.7.1)

Agents working on a real codebase need schema context **alongside** the code. ken v0.7.0 indexes both. An agent answering "how do users get authenticated" gets the Go function doing auth, the SQL it executes, the `users` table definition, AND the FK relationships from `sessions.user_id` — all in one ranked result list. Design rationale in [ADR-017](docs/DECISIONS.md#adr-017-database-schema-indexing--two-tier-static-sql--live-postgres-with-documented-pii-stance).

### Tier 1 — Static `.sql` parsing (automatic)

When ken's walker sees `.sql` files in the corpus, it parses each `CREATE TABLE` / `INDEX` / `VIEW` / `ALTER TABLE` and emits one structural chunk per object alongside the raw line-chunked file. No env var, no opt-in. Migration files in `migrations/` are now first-class retrieval units:

```
-- file: migrations/001_users.sql
TABLE users
  id          BIGSERIAL PRIMARY KEY
  email       VARCHAR(255) NOT NULL UNIQUE
  role        VARCHAR(32)  NOT NULL DEFAULT 'guest'
  created_at  TIMESTAMP    NOT NULL DEFAULT NOW()

  INDEX users_email_idx ON (email)
```

As of **v0.7.1** (ADR-018), when ken's walker detects a directory containing numbered `.sql` migration files — Goose / dbmate / Rails-4 (`\d+_*.sql`), Flyway (`V\d+__*.sql`), or Rails-5 / Alembic (`\d{14}_*.sql`) — it **folds CREATE TABLE + later ALTER TABLE statements into a single "current state" chunk per table**. An agent asking "what columns does `users` have?" gets one denormalized chunk reflecting the final schema, not N+1 chunks (CREATE + every ALTER) it has to mentally replay.

```
-- file: migrations/0001_init.sql
-- folded from migrations
TABLE users
  id          BIGSERIAL PRIMARY KEY
  email       VARCHAR(255) NOT NULL UNIQUE
  status      VARCHAR(16) NOT NULL DEFAULT 'active'   -- added by 0002_add_status.sql
```

Folding covers `ADD COLUMN`, `DROP COLUMN`, `ALTER COLUMN ... TYPE`, `ADD CONSTRAINT`, `DROP CONSTRAINT`, and — as of **v0.8.1 Part C** ([ADR-022](docs/DECISIONS.md#adr-022-rename-column--rename-constraint-folding-via-eager-application-v081-part-c), closes [#14](https://github.com/townsendmerino/ken/issues/14)) — **`RENAME COLUMN` and `RENAME CONSTRAINT`**. RENAME is applied eagerly during replay: a `RENAME COLUMN old TO new` mutates the in-flight folded table so subsequent ALTERs see the post-rename state, and this-table column references inside constraint definitions (PK / UNIQUE / FK source-side / CHECK) get rewritten via a word-boundary regex scoped to the first parenthesized group. Cross-table FK target-side column references (the `REFERENCES other(remote)` portion) are NOT propagated — that's cross-table dependency requiring migration-DAG analysis and remains out of scope. Operators using MySQL's `CHANGE old new TYPE` syntax (rename + retype in one statement) see the BOTH-chunks fallback below.

**RENAME folding is a Tier-1 chunk-content fidelity improvement, NOT a search-ranking improvement.** ken's hybrid retrieval recall@10 numbers ([`docs/BENCH.md`](docs/BENCH.md)) measure a different system — they're about whether the right chunk surfaces in the top-10 results, not about whether the chunk contains post-rename column names. v0.8.1 Part C closes the latter gap without affecting the former.

When an ALTER can't be folded cleanly (unknown column, type conflict, missing CREATE TABLE, RENAME of a column that doesn't exist, anonymous constraint with no name to match), ken emits **both** the original per-file ALTER chunk **and** the folded chunk for what could be resolved — the agent never sees less information than v0.7.0.

Set `KEN_SQL_NO_AUTO_MIGRATIONS=1` to restore the v0.7.0 per-file behavior. Useful for operators who maintain a canonical `schema/current.sql` and don't want migration history surfaced separately.

### Tier 2 — Live Postgres introspection (`KEN_DB_DSN`)

When `KEN_DB_DSN` is set, ken connects to the live database, introspects via `information_schema` / `pg_catalog`, and emits one chunk per table / view / index / function. Every chunk carries a freshness header naming the engine + host (never credentials):

```
-- indexed at 2026-08-15T14:23Z from postgres@dev-pg.local
TABLE users
  id          bigint        PK
  email       varchar(255)  NOT NULL UNIQUE
  role        varchar(32)   NOT NULL DEFAULT 'guest'
  created_at  timestamp     NOT NULL DEFAULT now()

  INDEX users_email_idx ON (email)
  FK referenced by: sessions(user_id), audit_log(actor_id)
```

`KEN_DB_DSN` must be the URL form (`postgres://user:pass@host:port/db?sslmode=...`). Tier 2 requires `KEN_MCP_DEFAULT_REPO` to be set to a local path — DB chunks attach to that repo's index. Multi-repo searches (no default) get FS-only.

### PII stance: documentation + sane defaults

> **This is intended for development databases. Do not point this at production data — sample rows are sent to your LLM provider as part of search results.** ken does NOT ship column-exclusion DSLs, redaction modes, or row-synthesis controls. If you can't trust the database with the LLM, don't connect ken to it.

The defaults are conservative:
- **Schema-only is the default.** `KEN_DB_SAMPLE_ROWS=0` (unset) means no row data is read.
- **The opt-in is unambiguous.** `KEN_DB_SAMPLE_ROWS=N` reads as "rows you're choosing to expose."
- **Every chunk carries provenance.** The freshness header surfaces `postgres@dev-pg.local` in agent output, so reviewers see where the data came from.

### Row sampling (opt-in)

`KEN_DB_SAMPLE_ROWS=3` appends 3 deterministically-ordered rows per table to that table's chunk:

```
  Sample rows (3 of ~12,847):
    (1,   alice@example.com, admin,  2024-01-15)
    (47,  bob@example.com,   member, 2024-03-22)
    (203, claire@example.com,guest,  2025-11-08)
```

Rows are ordered by the first PK column (fallback `ORDER BY 1` for tables without PK) so successive reindexes produce identical content. Long cells truncate at 80 chars with `…`.

### Three reindex layers

1. **Build-once-at-startup (default).** When `ken-mcp` starts, it introspects once and never refreshes. Restart to pick up schema changes. No background goroutines, no polling cost.
2. **Periodic.** `KEN_DB_REINDEX_INTERVAL=5m` enables a background ticker that re-introspects on the configured cadence (Go duration string: `5m`, `1h`, etc.). Failures log a warn and skip that tick; agents tolerate stale schema better than no schema.
3. **Manual via SIGHUP.** Standard Unix convention. Useful with migrate-up workflows:

   ```makefile
   migrate-up:
       psql -f migrations/$(NEXT).sql
       kill -HUP $$(pgrep ken-mcp)
   ```

   The Refresher's mutex serializes concurrent triggers, so SIGHUP is safe to spam. No-op on Windows.

### Failure modes (all non-fatal)

- DSN unset → silent no-op (FS-only).
- DSN invalid → stderr warn, FS-only.
- `KEN_MCP_DEFAULT_REPO` unset or http(s) URL → stderr warn, Tier 2 stays off.
- Initial connect / introspection fails → stderr warn, Tier 2 stays off but FS-only server keeps running.
- Periodic tick fails → stderr warn, skip tick, retry next interval.
- SIGHUP refresh fails → stderr warn, previous chunks remain in the snapshot.

Tier 2 going dark never crashes `ken-mcp`. Restart picks up a recovered DSN.

### Engine scope

As of **v0.7.2**, Tier 2 supports **Postgres + SQLite + MySQL**. Engine routing inside `internal/db.IndexSchema` dispatches on the DSN scheme:

| Scheme | Driver | Typical use |
|---|---|---|
| `postgres://` / `postgresql://` | `github.com/jackc/pgx/v5` (pure Go) | server-backed dev DBs |
| `sqlite://` / `sqlite3://` | `modernc.org/sqlite` (pure Go, transpiled from C, no cgo) | Rails / Django / Phoenix / Laravel / FastAPI / embedded apps |
| `mysql://` or `user:pass@tcp(host:port)/db` | `github.com/go-sql-driver/mysql` (pure Go) | MySQL 5.7+ / MySQL 8.x / **MariaDB 10.x+** (first-class as of v0.8.1) — Rails, Django, Laravel, .NET, LAMP |

SQLite DSN examples:
- `sqlite:///var/data/dev.db` — absolute path (note the triple slash: scheme + empty host + absolute path).
- `sqlite://./dev.db` — relative path, resolved against `KEN_MCP_DEFAULT_REPO`. Convenient when the SQLite file lives inside the repo (overwhelmingly common).

MySQL DSN examples:
- `mysql://alice:s3cret@db.local:3306/mydb?parseTime=true` — URL form (canonical, matches the Postgres pattern).
- `alice:s3cret@tcp(db.local:3306)/mydb?parseTime=true` — native go-sql-driver form, accepted directly because that's what most .env files in the wild already contain.
- `alice@unix(/var/run/mysqld/mysqld.sock)/mydb` — Unix-socket form.

`parseTime=true` is forced on internally if absent — without it, DATE/DATETIME/TIMESTAMP columns deserialize as `[]byte` and don't render cleanly in row samples.

**MariaDB is first-class as of v0.8.1** ([ADR-021](docs/DECISIONS.md#adr-021-mariadb-first-class-engine-support-v081-part-b)) — same `KEN_DB_DSN` env var, same MySQL DSN forms above, same `go-sql-driver/mysql` driver. CI's `test-db-integration` job now runs the integration suite against both `mysql:8` and `mariadb:11-jammy` service containers; the v0.8.1 normalization layer strips MariaDB's legacy `bigint(20)` / `int(11)` integer display widths so chunks stay byte-identical across engines. End users see no operator-visible difference — point ken at MariaDB the same way you point it at MySQL.

`KEN_DB_MARIADB_TEST_DSN` is a **CI / development-only** env var that the integration test suite uses to run the same tests against a live MariaDB container in parallel with `KEN_DB_MYSQL_TEST_DSN`. End users do not need to set it; both engines share `KEN_DB_DSN` for production use.

The freshness header omits credentials and shows the engine label only (`postgres@dev-pg.local`, `mysql@db.local`, `sqlite@dev.db`); ports are surfaced only when non-default. SQLite uses the file basename so chunks don't leak local filesystem layout. The same row-sampling / periodic-refresh / SIGHUP machinery works for all three engines without configuration changes.

### Filtering indexed schemas

Production-cloned dev DBs accumulate noise — audit / cron / queue / per-tenant schemas the agent shouldn't suggest using. As of **v0.7.2** two env vars filter the schema set Tier 2 indexes:

- `KEN_DB_SCHEMAS` — comma-separated allow-list. Only these schemas are indexed (intersected with the engine's default exclusions, which always apply). Example: `KEN_DB_SCHEMAS=public,billing`.
- `KEN_DB_EXCLUDE_SCHEMAS` — comma-separated deny-list. Extends (does not replace) the default exclusions. Example: `KEN_DB_EXCLUDE_SCHEMAS=audit,cron,legacy`.

Resolution rules:
- Neither set → default behavior (everything except engine system schemas). v0.7.0 / v0.7.1 byte-identical.
- Only `KEN_DB_SCHEMAS` → index exactly those schemas (system schemas still filtered).
- Only `KEN_DB_EXCLUDE_SCHEMAS` → index everything not in the deny-list and not in system schemas.
- Both set → stderr warn, allow-list wins, deny-list ignored.

Default exclusions are **never user-overridable**: `pg_catalog` and `information_schema` for Postgres; `information_schema`, `mysql`, `performance_schema`, `sys` for MySQL. Operators who genuinely need to index those should not point ken at the DB.

SQLite is a single-schema engine and ignores both env vars (debug-level log when they're set with a SQLite DSN).

Wildcards (e.g. `KEN_DB_SCHEMAS=tenant_*`) are explicitly out of scope for v0.7.2 — multi-tenant operators can fall back to the explicit form `KEN_DB_SCHEMAS=tenant_001,tenant_002,...` until field signal calls for wildcard syntax. See [ADR-019](docs/DECISIONS.md#adr-019-mysql-engine--schema-filtering-for-multi-schema-dev-databases) for the rejected-alternatives audit trail.

### LISTEN/NOTIFY push notifications (v0.8.0, Postgres only)

The v0.7.x reindex layers (startup + `KEN_DB_REINDEX_INTERVAL` + SIGHUP) are pull-based — operators waiting for the next interval tick see stale schemas in the meantime. As of **v0.8.0**, Postgres deployments can opt into push-based change detection: schema changes propagate to ken's index within ~100ms instead of waiting for the next tick.

**One-time setup.** ken does NOT modify your database without explicit consent. Run the embedded SQL script once:

```bash
ken-mcp print-listen-script | psql $KEN_DB_DSN
```

This installs a single schema-level event trigger (`ken_schema_changed_trigger`) that fires on tracked DDL (`CREATE / ALTER / DROP TABLE`, `INDEX`, `VIEW`, `MATERIALIZED VIEW`, `FUNCTION`, `TRIGGER`, `TYPE`) and emits a `pg_notify('ken_schema_changed', ...)`. The script is idempotent (`DROP IF EXISTS` + `CREATE`); re-running is safe.

**Activate.** Set `KEN_DB_LISTEN=1` (or `true` / `yes`) on the ken-mcp process. The listener uses a dedicated pgx connection separate from the introspection pool, so a long `WaitForNotification` call doesn't tie up the connection introspection needs.

**Failure modes** (all non-fatal; interval polling continues regardless):
- Event trigger not installed → clear warn naming the fix command (`ken-mcp print-listen-script | psql $KEN_DB_DSN`); listener idles until the next reconnect re-checks.
- Connection drops (network partition, server restart) → exponential-backoff reconnect (100ms → 30s cap), reset on each successful reconnect.
- Non-Postgres DSN → debug log + no-op (MySQL and SQLite have no equivalent push mechanism).

**Recommendation: use alongside `KEN_DB_REINDEX_INTERVAL`, not instead.** NOTIFY connections can drop silently (network partition, brief reconnect window); interval polling acts as defense-in-depth backstop that catches missed notifications without operator intervention. Both can run simultaneously; the `Refresher`'s internal mutex serializes concurrent refreshes so a NOTIFY arriving mid-tick collapses cleanly.

See [ADR-020](docs/DECISIONS.md#adr-020-listennotify-push-based-schema-change-detection-v080-part-1) for the alternatives considered (auto-install rejected; per-table opt-in rejected; replace-interval rejected; no-debouncing rejected; faked-MySQL rejected).

### Agent-triggered reindex (`reindex_db` tool, v0.8.0 Part 2)

Agents can refresh ken's view of the database schema on demand by calling the `reindex_db` MCP tool. Useful after the agent has run a migration, or before asking a schema-dependent question:

> **User:** "I just ran migration 042 that adds the `email_verified` column to `users`. Does the schema reflect that?"
> **Agent:** *(calls `reindex_db`)* → *(calls `search "users table"`)* → returns the post-migration schema.

**Always available** when `KEN_DB_DSN` is set; no env var to enable. When no DB is configured, the tool is not registered at all (the agent's `tools/list` shows only `search` and `find_related`).

**Engine-agnostic.** Works for Postgres, MySQL, and SQLite — the tool is a thin wrapper around the same `Refresher` that drives `KEN_DB_REINDEX_INTERVAL`, SIGHUP, and (Postgres) LISTEN/NOTIFY.

**Fail-fast on contention.** If a reindex is already in flight (LISTEN burst, interval tick, SIGHUP, or prior `reindex_db` call), the tool returns `Reindex already in progress; nothing to do.` immediately rather than queuing. The agent can retry, back off, or proceed with stale data based on its workflow — no silent queueing, no unbounded memory growth, no time-based cooldown env vars.

**Pairs with LISTEN/NOTIFY and `KEN_DB_REINDEX_INTERVAL`.** Push notifications cover Postgres deployments with the trigger installed; interval polling covers MySQL / SQLite and the LISTEN-not-set-up Postgres case; `reindex_db` covers the case where the agent itself caused the schema change and knows it needs to refresh.

See [ADR-020 Part 2](docs/DECISIONS.md#part-2-agent-callable-reindex-via-reindex_db-mcp-tool-v080-part-2) for the alternatives considered (cooldown, queue, async-return, env-var-disable, auto-call-from-search all rejected with mechanism-level failure modes).

### Embedded DB support for SDK authors (v0.8.0 Part 3, opt-in)

SDK authors using [`mcp.Run`](#using-ken-in-your-own-mcp-server-mcprun) (the v0.6.0 embedded-corpus entrypoint) can wire Tier 2 DB support — schema introspection, optional LISTEN/NOTIFY, optional interval reindex, and the `reindex_db` MCP tool — via the new opt-in `mcp/db` package:

```go
package main

import (
    "context"
    "log"
    "os"
    "time"

    "github.com/townsendmerino/ken/mcp"
    mcpdb "github.com/townsendmerino/ken/mcp/db"
)

func main() {
    ctx := context.Background()

    // Opt-in: only SDK authors who want DB support import mcp/db.
    refresher, err := mcpdb.Setup(ctx, mcpdb.Config{
        DSN:             os.Getenv("MY_DB_DSN"),
        SampleRows:      0,
        ReindexInterval: 5 * time.Minute,
        EnableListen:    true, // requires one-time `mcpdb.ListenNotifyScript | psql $DSN` setup
    })
    if err != nil {
        log.Fatal(err)
    }

    // refresher is nil when MY_DB_DSN is unset → opts.DB stays nil →
    // reindex_db tool NOT registered (the v0.6.0 docs-only behavior).
    // When non-nil, mcp.Run calls refresher.Start internally and
    // defers the returned cleanup.
    if err := mcp.Run(ctx, myEmbeddedDocsCorpus, mcp.Options{
        Mode:        "hybrid",
        ChunkerName: "markdown",
        DB:          refresher, // *mcpdb.Refresher satisfies mcp.DBIntegration
    }); err != nil {
        log.Fatal(err)
    }
}
```

**v0.6.0 binary-size contract preserved.** SDK authors who DON'T import `mcp/db` get a binary identical in dep-tree shape to v0.7.2's `mcp.Run` use case — no pgx, no SQLite, no MySQL driver, no `internal/db` in the link graph. The opt-in package boundary is enforced at CI time by `TestBinary_MCPPackageStaysDBFree`, which shells out to `go list -deps github.com/townsendmerino/ken/mcp` and fails if any DB driver path appears.

**SDK authors who want `print-listen-script`** in their own CLI can grab the embedded SQL script from `mcpdb.ListenNotifyScript` (a re-export of `internal/db.ListenNotifyScript`) without depending on the `internal/` package:

```go
if len(os.Args) > 1 && os.Args[1] == "print-listen-script" {
    _, _ = io.WriteString(os.Stdout, mcpdb.ListenNotifyScript)
    return
}
```

**Chunk integration is end-to-end.** Calling `reindex_db` from an agent against an `mcp.Run + mcp/db.Setup` binary runs the introspection AND makes the new DB chunks searchable in the agent's next `search` / `find_related` call. The pipeline: `mcp.Run` wraps the embedded `*search.Index` in `atomic.Pointer[search.Index]`; `mcp/db.Refresher.Start` (called by `mcp.Run` on startup) wires the swap callback to `*search.Index.WithExtraChunks` + atomic-pointer store; each refresh rebuilds against the original corpus + the latest DB chunks. `cmd/ken-mcp` continues to use `*WatchedIndex.SetExtraChunks` for its fsnotify-rooted path; the SDK-author + CLI surfaces converge on the same `Refresher` + `reindex_db` semantics. See [ADR-020 Part 3](docs/DECISIONS.md#part-3-opt-in-mcpdb-package-preserving-v060-binary-size-contract-v080-part-3) for the full design + the rejected alternatives.

## Quickstart

```bash
# Install both binaries (Go 1.26+).
go install github.com/townsendmerino/ken/cmd/ken@latest
go install github.com/townsendmerino/ken/cmd/ken-mcp@latest

# Download the default Model2Vec model (~64 MB, one-time).
# Pure Go, no Python tooling required.
ken download-model

# Search any local repo from the CLI.
ken search /path/to/myrepo "save model to disk" --model ~/.ken/model
```

Or skip the model download and use lexical-only mode:

```bash
ken search /path/to/myrepo "validateToken" --mode bm25
```

Library use (sketch):

```go
import "github.com/townsendmerino/ken/internal/search"

ix, _ := search.FromPath("/path/to/myrepo", search.ModeHybrid, "regex", "/path/to/model")
for _, r := range ix.Search("save model to disk", 10) {
    fmt.Printf("%.3f  %s:%d-%d\n", r.Score, r.Chunk.File, r.Chunk.StartLine, r.Chunk.EndLine)
}
```

Pre-built binaries for macOS and Linux are attached to each [release](https://github.com/townsendmerino/ken/releases).

As of v0.3, `ken index <path>` defaults to **watch mode** — it keeps the process alive and re-indexes files on change (2 s debounce); pass `--no-watch` for the v0.2 build-once-and-exit behavior. `ken-mcp` watches always — an agent editing the repo mid-session sees its own changes without a restart.

As of v0.5.0, ken respects **nested `.gitignore` files** (per-directory), matching git's behavior: a `.gitignore` inside a subdirectory applies to paths under it, with outer scopes evaluated first and inner scopes last (last match wins). Monorepos with per-package `node_modules/` exclusions in subdirectory `.gitignore` files are correctly pruned without a root-level entry.

The default `regex` chunker handles most cases well. If you index a lot of Kotlin / Zig / TypeScript / Java / PHP, the opt-in `treesitter` chunker (`--chunker=treesitter` / `KEN_MCP_CHUNKER=treesitter`) measurably wins for those languages — see ["Choosing a chunker"](#choosing-a-chunker) for the per-language recommendation.

## Features

- **Pure Go, no cgo.** Single static binary; `GOOS`/`GOARCH` cross-compiles for free; no `libtokenizers.a` to vendor per platform.
- **Drop-in MCP-compatible with semble.** Same `search` / `find_related` tool schemas, same markdown-string output format, install snippets adapted from semble's README.
- **Algorithm verbatim from semble.** BM25 + Model2Vec semantic + α-weighted RRF fusion + code-aware rerank (definition / embedded-symbol / file-coherence / stem-match boosts) + path penalties + file-saturation decay. See [docs/DESIGN.md §7](docs/DESIGN.md#7-hybrid-retrieval--rerank).
- **Measured agent-input efficiency.** ~44× fewer tokens than grep+Read at recall@10 on semble NL queries (4,269 vs 189,591 tok); ~16× on symbol queries; at 280K-file corpus scale, grep+Read is functionally impossible and ken is the only workable path. Full breakdown + caveats in [`docs/BENCH.md`](docs/BENCH.md#token-budget-recall--agent-side-efficiency).
- **Tokenizer parity proven against `transformers.AutoTokenizer`** on an 11k-input adversarial+repo corpus (`scripts/parity_dump.py` + `internal/embed/parity_test.go`).
- **Fast cold start.** No Python interpreter import (`ken search` from a tiny index returns in ~10–20 ms on a Mac).
- **Concurrent indexing scaled to cores.** No GIL.
- **CPU-only.** No API keys, no GPU, no external services.

## MCP server

`ken-mcp` speaks JSON-RPC over stdio. Configure your agent to invoke it; it serves the same two tools (`search`, `find_related`) semble does, with the same arg shapes and the same markdown-string output.

### Install in your agent

```bash
# Claude Code
claude mcp add ken -s user -- /absolute/path/to/ken-mcp
```

`~/.cursor/mcp.json` (or `.cursor/mcp.json`):
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

### Environment

| Variable | Default | Purpose |
|---|---|---|
| `KEN_MCP_DEFAULT_REPO` | (unset) | Pre-indexed source; lets tools omit the `repo` arg. |
| `KEN_MCP_MODE` | `hybrid` | `bm25` / `semantic` / `hybrid`. Auto-downgrades to `bm25` with a stderr warning if the model dir is unreachable. |
| `KEN_MCP_MODEL_DIR` | (unset) | Path to a Model2Vec snapshot containing `model.safetensors`. Empty ⇒ `bm25`-only. |
| `KEN_MCP_CHUNKER` | `regex` | `regex` / `treesitter` / `line` / `markdown`. See ["Choosing a chunker"](#choosing-a-chunker). |
| `KEN_DB_DSN` | (unset) | Database DSN. Postgres (`postgres://...` / `postgresql://...`), SQLite (`sqlite:///abs/path.db`, `sqlite://./rel/path.db`, `sqlite3://...`), or MySQL (`mysql://user:pass@host:3306/db`, native `user:pass@tcp(host:3306)/db`, or `user:pass@unix(/sock)/db`) — engine routing dispatches on the scheme (or `@tcp(`/`@unix(` for the native MySQL form). Enables [Tier 2 DB indexing](#tier-2--live-postgres-introspection-ken_db_dsn). Requires `KEN_MCP_DEFAULT_REPO` to be a local path. |
| `KEN_DB_SAMPLE_ROWS` | `0` | Rows per table to sample. **Default 0 means schema-only.** See the [PII stance](#pii-stance-documentation--sane-defaults) before enabling. |
| `KEN_DB_REINDEX_INTERVAL` | (off) | Go duration (`5m`, `1h`). Background refresh cadence. Off by default — restart or `SIGHUP` to refresh. |
| `KEN_DB_LISTEN` | `0` | `1` / `true` / `yes` activates Postgres LISTEN/NOTIFY push notifications (v0.8.0). Requires the one-time setup script: `ken-mcp print-listen-script \| psql $KEN_DB_DSN`. Non-Postgres DSNs log debug + no-op. See [LISTEN/NOTIFY push notifications](#listennotify-push-notifications-v080-postgres-only). |
| `KEN_DB_SCHEMAS` | (unset) | Comma-separated allow-list of schema names (Postgres) / database names (MySQL). Example: `public,billing`. Default exclusions (`pg_catalog`, `information_schema`, `mysql`, `performance_schema`, `sys`) always still apply. SQLite ignores. See [Filtering indexed schemas](#filtering-indexed-schemas). |
| `KEN_DB_EXCLUDE_SCHEMAS` | (unset) | Comma-separated deny-list. Extends (does not replace) the default exclusions. Example: `audit,cron,legacy`. When set alongside `KEN_DB_SCHEMAS`, the allow-list wins (stderr warn). SQLite ignores. |
| `KEN_SQL_NO_AUTO_MIGRATIONS` | (off) | `1` / `true` / `yes` disables v0.7.1 Tier-1 migration-history folding (restores v0.7.0 per-file behavior). Useful when you maintain a canonical `schema/current.sql` and don't want migration history surfaced as folded chunks. |
| `KEN_MCP_CACHE_SIZE` | `16` | LRU bound on the repo→Index cache. |
| `KEN_MCP_LOG_LEVEL` | `warn` | `debug` / `info` / `warn` / `error`. All logs go to stderr; **stdout is the JSON-RPC channel** ([details](docs/DESIGN.md#hard-rule--stdoutstderr-contract)). |
| `KEN_ALLOW_PRIVATE_CLONE_TARGETS` | `0` | Defaults off. When an agent passes an http(s) URL as `repo`, ken pre-resolves the host and rejects loopback / link-local / RFC1918 / RFC4193 / unspecified addresses (SSRF guard — blocks an agent from coaxing ken-mcp into probing cloud-metadata or other internal endpoints). Set to `1` / `true` / `yes` if you legitimately need to clone from an internal git host. |

### Tuning ken's routing for your repo

By default, `ken-mcp`'s server-side instructions tell agents to prefer ken's `search` and `find_related` tools over grep, Glob, or Read for code-related questions — semble's verbatim behavior, faithful to the drop-in claim. For many repos that default is right; for some it's too aggressive (small codebases where grep is plenty fast; refactors that need exhaustive enumeration that top-N retrieval can silently miss).

If you'd rather have agents route between ken and grep deliberately, add something like the following to your repo's `CLAUDE.md`:

> **Search routing — ken vs grep.** The `ken` MCP server is user-scoped (`claude mcp add ken -s user …`); not every session has it. Check the tool list before assuming.
>
> - **ken** — first-pass "show me the surface of X", semantic / conceptual queries ("where do we handle X?"), unfamiliar areas. Returns a ranked top-N grouped across layers (handler → store → resolver → migrations → generated → docs). ~1–2 s warm round-trip.
> - **grep / rg** — exhaustive enumeration, pre-rename audits, every literal occurrence, known-identifier lookups, one-off literal checks. ~0.06 s and deterministic. **Use grep before any rename or refactor that must be complete** — ken is top-N and can miss matches past its result window.
> - Don't reach for ken on a one-off literal lookup where you already know the symbol — the latency tax isn't worth it.

ken's defaults stay unchanged; this is per-repo tuning, not a configuration flag.

## Tools

Both tools return a formatted markdown string identical to semble's `_format_results` output.

### `search`

| Arg | Type | Required | Default | Description |
|---|---|---|---|---|
| `query` | string | ✓ | — | Natural language or code query. |
| `repo` | string |   | — | `https://` / `http://` URL or local directory. Required if no `KEN_MCP_DEFAULT_REPO`. |
| `mode` | `hybrid`\|`semantic`\|`bm25` |   | `hybrid` | Search mode. |
| `top_k` | int |   | `5` | Number of results. |

### `find_related`

| Arg | Type | Required | Default | Description |
|---|---|---|---|---|
| `file_path` | string | ✓ | — | Path as it appears in a `search` result. |
| `line` | int (1-indexed) | ✓ | — | A line inside the chunk to seed the similarity search. |
| `repo` | string |   | — | Same as for `search`. |
| `top_k` | int |   | `5` | Number of similar chunks. |

Example response (verbatim from a real session against this repo's polyglot fixture):

```
Search results for: "validate_user" (mode=bm25)

## 1. auth.py:1-22  [score=5.518]
​```
"""Authentication helpers."""

import hashlib

@dataclass
class User:
    name: str
    token: str

    def is_valid(self):
        return bool(self.token)

# validate_user checks a token against a user record.
def validate_user(user, token):
    return user.token == token
​```
```

## How it works

```
gitignore-respecting walk
    → regex chunker (Python / Go / TS / Java / Rust) with line-chunker fallback
    → BM25 (Lucene variant, k1=1.5, b=0.75)  +  Model2Vec semantic (cosine over a dense matrix)
    → α-weighted RRF fusion (α auto-detected: 0.3 for symbol queries, 0.5 for NL)
    → file-coherence boost + query-type boosts (definition / embedded-symbol / stem-match)
    → path penalties (test files, compat / legacy, `.d.ts`) + file-saturation decay
    → top-k
```

The retrieval algorithm is a verbatim port of semble's `search.py` + `ranking/*.py`; see [docs/DESIGN.md §7](docs/DESIGN.md#7-hybrid-retrieval--rerank) for every constant, every pipeline-order subtlety, and where the original scoping reconstruction diverged from semble's live source. The Model2Vec inference path (three-tensor `safetensors` layout, the `mapping[]` indirection, the float64 precision contract that's load-bearing for ≥1−1e-5 cosine parity) is in [§4](docs/DESIGN.md#4-model2vec-inference-format).

## Using ken as a library over `fs.FS`

As of v0.5.0 the walker and indexer take any `fs.FS`, so ken can index an `embed.FS`, an `fstest.MapFS`, a tarball-backed FS, or any other `fs.FS` implementation — useful for agent sandboxing (no escape from the corpus) and offline analysis (no unpack-to-disk step). The `--watch` codepath stays real-FS-only.

```go
import (
    "embed"

    "github.com/townsendmerino/ken/internal/search"
)

//go:embed corpus/**
var corpus embed.FS

func main() {
    ix, _ := search.FromFS(corpus, search.ModeBM25, "regex", "")
    for _, r := range ix.Search("validate token", 5) {
        // r.Chunk.File, r.Chunk.StartLine, r.Score, ...
    }
}
```

For test fixtures, `testing/fstest.MapFS` works the same way: `search.FromFS(fstest.MapFS{"a.go": {Data: []byte("...")}}, …)`. The legacy `search.FromPath(root, …)` is now a thin deprecated wrapper around `search.FromFS(os.DirFS(root), …)`. See [ADR-014](docs/DECISIONS.md#adr-014-fsfs-as-canonical-walkerindexer-surface) for the design rationale.

## Choosing a chunker

ken ships with **two chunkers** behind the same `--chunker=` flag (CLI) / `KEN_MCP_CHUNKER=` env var (MCP):

- **`regex`** *(default)* — hand-rolled per-language regex rules for Python / Go / TypeScript / Java / Rust with a line-window fallback for everything else.
- **`treesitter`** *(opt-in)* — pure-Go tree-sitter via [`gotreesitter`](https://github.com/odvcencio/gotreesitter), running the cAST split-then-merge algorithm from [arXiv 2506.15655](https://arxiv.org/html/2506.15655). Its 206 embedded grammars are ~19 MB on-disk (gotreesitter's `embed.FS` payload); importing the chunker adds ~26 MB to the linked binary (parser runtime + embed payload + symbol bookkeeping; measured darwin/arm64). Importing is per-binary at compile time — `cmd/ken` and `cmd/ken-mcp` blank-import it; `cmd/ken-mcp-docs` deliberately doesn't. Once imported, chunker choice is a runtime flag (`--chunker=treesitter` / `KEN_MCP_CHUNKER=treesitter`). (v0.8.2 found per-grammar build-tag gating couldn't shrink the binary because the embed layer was a monolithic upstream glob; gotreesitter v0.20.0-rc2 fixed that, so as of [ADR-033](docs/DECISIONS.md#adr-033-adopt-gotreesitter-grammarsubset-slim-release-binaries-v0200-rc2) ken's *release* binaries build slim — embedding only the 17 dispatched grammars (~14 MB lighter) — while the library `go build` stays all-grammars. History in [ADR-023](docs/DECISIONS.md#adr-023-gotreesitter-grammar_subset-machinery--binary-size-reduction-outcome-v082-investigation-outcome) and its [calibration amendment](docs/DECISIONS.md#calibration-amendment-post-v083-audit).)

**TL;DR:** stay on `regex` unless you index one of the languages where treesitter measurably wins.

The NDCG@10 difference is small (overall hybrid: treesitter 0.838 vs regex 0.842 — Δ −0.004, within bench noise), but it's not uniform per-language. From the v0.2.0 measurement on semble's 63-repo benchmark:

| Language | regex | treesitter | Recommendation |
|---|---:|---:|---|
| Kotlin | 0.806 | **0.817** | **`treesitter`** *(+0.011)* |
| Zig | 0.867 | **0.880** | **`treesitter`** *(+0.013)* |
| TypeScript | 0.676 | **0.685** | **`treesitter`** *(+0.009)* |
| Java | 0.829 | **0.835** | **`treesitter`** *(+0.006)* |
| PHP | 0.860 | **0.865** | **`treesitter`** *(+0.005)* |
| Python | **0.870** | 0.861 | `regex` *(−0.009)* |
| C | **0.748** | 0.731 | `regex` *(−0.017)* |
| C++ | **0.896** | 0.884 | `regex` *(−0.012)* |
| Rust | **0.806** | 0.793 | `regex` *(−0.013)* |
| Lua | **0.838** | 0.816 | `regex` *(−0.022)* |
| Scala | **0.905** | 0.883 | `regex` *(−0.022)* |
| Go | **0.849** | 0.846 | either *(tied within ±0.005)* |
| JavaScript | 0.917 | 0.912 | either |
| Ruby | 0.903 | 0.903 | either |
| Swift | 0.846 | 0.841 | either |
| Elixir | 0.911 | 0.907 | either |
| Haskell | 0.738 | 0.739 | either |
| C# | 0.859 | 0.859 | either *(treesitter auto-falls-back to line)* |
| Bash | 0.821 | 0.821 | either *(treesitter auto-falls-back to line)* |

Notes on the auto-fallback rows:
- **C#** — the gotreesitter v0.18.0 C# grammar OOMs on real-world C# files (1.7+ GB RSS during indexing). The treesitter chunker detects unsupported languages and routes them through the line chunker, so C# behaves identically under both selections.
- **Bash** — the bash grammar is pathologically slow on real bash-it content (~39% of files timeout). Same auto-fallback behavior.

The full per-language NDCG breakdown plus the empirical findings that informed this is in [`docs/BENCH.md`](docs/BENCH.md). The rationale for default-stays-regex is in [`docs/DECISIONS.md` ADR-011](docs/DECISIONS.md#adr-011-default-chunker-stays-regex-in-v020-treesitter-is-opt-in).

## Comparison to semble

| Property | semble | ken |
|---|---|---|
| Language | Python | Go |
| Distribution | `uvx` / `pip install` | single static binary |
| Cold start | (Python interpreter + `import numpy` + model load: ~500 ms per [semble README](https://github.com/MinishLab/semble#benchmarks)) | ~10–20 ms `ken search` over a tiny index (measured, M2 Mac) |
| Index this repo (1,710 chunks, hybrid w/ model) | (not measured locally) | **0.36 s** (measured‡) |
| Index `/tmp/semble` checkout (1,151 chunks, hybrid w/ model) | (not measured locally) | **1.27 s** (measured‡) |
| Index this repo (1,710 chunks, BM25 only) | (not measured locally) | **0.06 s** (measured‡) |
| Retrieval algorithm | reference implementation | verbatim port (constants and pipeline order ported from `search.py` + `ranking/*.py`) |
| NDCG@10 on semble's benchmark | 0.854 ([semble README](https://github.com/MinishLab/semble#benchmarks)) | **0.842 hybrid** (gap 0.012, full corpus 63 repos × 1251 queries)† |
| NDCG@10 on CoIR-CSN-Python (external) | (not measured; semble doesn't run this bench) | **0.8743 bm25 / 0.7839 hybrid** ([see why](#benchmarks--external-reference-coir-csn-python))†† |
| Median tokens to recall@10 on agent queries | (not measured; semble doesn't run this bench) | **4,269 tok @ 82% recall** on semble NL queries — vs grep+Read's 189,591 tok @ 99.9% (44× cheaper at 17 pp lower recall)††† |
| MCP server | yes | yes — drop-in compatible (same tool schemas, same wire format) |
| Binary size | n/a (Python env) | release (slim) `ken` ~22 MB · `ken-mcp` ~38 MB; default `go build` (all 206 grammars) `ken` ~36 MB · `ken-mcp` ~54 MB. Slim embeds only the 17 dispatched grammars via `grammar_subset` build tags ([ADR-033](docs/DECISIONS.md#adr-033-adopt-gotreesitter-grammarsubset-slim-release-binaries-v0200-rc2)); measured darwin/arm64 — see [Choosing a chunker](#choosing-a-chunker) |
| Requires `huggingface-cli` for model | yes | **no** — `ken download-model` fetches direct from HF (or skip and use `--mode bm25`) |

† **Measured at v0.1.0 / v0.2.0 against semble's published benchmark** (63 repos, 1251 queries, semble's own `benchmarks.metrics.ndcg_at_k` + `target_rank`). Reproduce: see [`docs/BENCH.md`](docs/BENCH.md). Ablation breakdown vs semble's published raw retrieval numbers:
>
> | Mode | semble (raw) | ken regex (default) | ken treesitter (opt-in) |
> |---|---:|---:|---:|
> | Semantic only (potion-code-16M) | 0.650 | **0.647** | — |
> | BM25 only | 0.675 | 0.624 | 0.621 |
> | **Hybrid (full ranker)** | **0.854** | **0.842** | **0.838** |
>
> The semantic-raw match within 0.003 isolates and validates the embedding + tokenizer + ANN port. The BM25 tokenizer was also re-aligned to a verbatim port of semble's `tokens.py` (snake-case compound preservation, ASCII-only identifier extraction, compound-first emission order). The v0.2.0 tree-sitter chunker (`--chunker=treesitter` via [`gotreesitter`](https://github.com/odvcencio/gotreesitter)) trades NDCG per-language without net movement — clear wins on Kotlin / Zig / TypeScript / Java / PHP, losses on Python / Rust / C / Lua / Scala — so the **default chunker stays regex** and treesitter is opt-in. See ["Choosing a chunker"](#choosing-a-chunker) for the per-language recommendation and [`docs/DECISIONS.md` ADR-011](docs/DECISIONS.md#adr-011-default-chunker-stays-regex-in-v020-treesitter-is-opt-in) for the full rationale.

†† CoIR-CSN-Python numbers reported separately because they tell a different story than semble's bench: on CSN, BM25 beats hybrid by ~0.09 due to a substring-leak artifact in how CoIR reframes the CodeSearchNet dataset (queries are Python function sources; documents are docstrings extracted from those same functions, so the answer is a literal substring of the query). See the ["Benchmarks — external reference"](#benchmarks--external-reference-coir-csn-python) section and [`docs/BENCH.md`](docs/BENCH.md#external-benchmark--coir-csn-python) for the corrected explanation. semble's bench is the verbatim-port confirmation; CoIR-CSN is the externally-reproducible anchor against published code-IR baselines but is read as a dataset-construction case study, not as evidence about ken's hybrid retrieval on natural NL-to-code queries.

††† Measured at v0.3.0 against semble's 63-repo benchmark (914 NL queries from semble's 1,251-query corpus, ranked by ken's regex chunker, K=10). The honest framing: ken trades ~17 percentage points of recall for ~44× fewer agent-input tokens. Exhaustive enumeration (refactors, pre-rename audits) still belongs to grep — ken is for "find the chunk that answers this." Full per-query-class table (symbol + NL) and the methodology + caveats are in [`docs/BENCH.md`](docs/BENCH.md#token-budget-recall--agent-side-efficiency).

‡ **Indexing times re-measured 2026-05-29** at commit `fe53e91` (post-perf-campaign: v0.8.5–v0.8.7 tokenizer-allocation reduction + indexing-pipeline parallelism, [ADR-027](docs/DECISIONS.md#adr-027-bm25-tokenizer-allocation-reduction--rune--byte--syncpool-scratch--lowercase-fast-path-v085)/[ADR-030](docs/DECISIONS.md#adr-030-indexing-pipeline-parallelism--phase-a-per-file-workers-for-chunk--embed-v087)), via `ken perf index <path> --mode …` (darwin/arm64, Go 1.26.3, 8 cores). "This repo" is the ken repo root — it grew from 542 to 1,710 chunks (3.2×) since the prior measurement, yet hybrid indexing *dropped* from 0.45 s to 0.36 s and BM25 held at 0.06 s for 3.2× the work.

semble timings cited above are from semble's own [README "Benchmarks" section](https://github.com/MinishLab/semble#benchmarks); ken's are measured on the ken repo root and on a sibling shallow clone of `/tmp/semble`. Cold-start was timed by `/usr/bin/time -p ken search testdata/repo "validate" -k 1 --mode bm25` over three trials (M2 MacBook Air, Go 1.26.3, darwin/amd64 build under Rosetta).

## Benchmarks — external reference (CoIR-CSN-Python)

A single externally-reproducible NDCG@10 number on [CoIR](https://github.com/CoIR-team/coir)'s `CodeSearchNet-python` task, independent of semble's own benchmark — gives readers a comparable anchor against published code-IR baselines.

Result (v0.2.0, 1000-query subsample, regex chunker):

| Mode                       | NDCG@10 |
|----------------------------|--------:|
| bm25                       |  0.8743 |
| semantic                   |  0.7405 |
| **hybrid (default)**       | **0.7839** |

Reproduce:

```bash
python scripts/bench_coir.py                                # ~45 s download + 280k corpus files
KEN_COIR_QUERY_LIMIT=1000 go test -tags=bench ./bench/ndcg/ -run TestCoIR -v   # ~13 min
```

A nuance worth surfacing up front: **on CSN-Python, BM25 beats hybrid by 0.09** — opposite of what semble's bench shows. CSN-Python's queries (as CoIR re-hosts the dataset) are full Python function sources, and the relevant document for each query is the docstring extracted from that same function. Because the docstring lives inside the function source as a literal substring (the function's own `"""..."""` block), any lexical retriever with identifier-aware tokenization wins — BM25 has the answer string as input. ken's α=0.5 RRF fusion then drags the hybrid number down by averaging in the weaker semantic ranking. Not a ken bug; it's a structural artifact of how CoIR reframed CodeSearchNet for retrieval, and doesn't generalize to natural NL-to-code distributions. Detailed empirical findings and the comparison to potion-code-16M's published aggregate are in [`docs/BENCH.md`](docs/BENCH.md#external-benchmark--coir-csn-python).

## Roadmap

The full risk register with explicit triggers is in [docs/DESIGN.md §10](docs/DESIGN.md#10-risk-register). Highlights:

- **NDCG vs semble — measured at v0.1.0 / v0.2.0**: hybrid 0.842 (regex) and 0.838 (treesitter) vs semble's 0.854. The ~0.012 gap is **not primarily chunker-driven** — v0.2.0's tree-sitter chunker trades per-language wins and losses without closing the gap (see [docs/BENCH.md](docs/BENCH.md) "v0.2.0 empirical findings"). The algorithm port itself is validated by the semantic-raw match within 0.003.
- **Tree-sitter chunker (Option A)** — landed in v0.2.0 via [`gotreesitter`](https://github.com/odvcencio/gotreesitter) as opt-in (`--chunker=treesitter`). Default stays `regex`. Per-language guidance in ["Choosing a chunker"](#choosing-a-chunker).
- **Chroma chunker (Option B)** — broader language coverage via a token-stream lexer. Trigger: a polyglot repo where neither chunker covers a needed language. Not currently triggered.
- **Class-body-aware Python chunking** — currently top-level only; large Django models / SQLAlchemy bases line-split through methods. Trigger: Python NDCG visibly below the other languages (not currently triggered).
- **~~Incremental indexing~~ — landed in v0.3.** `ken-mcp` watches the repo file tree and republishes a snapshot 2s after any edit, so an agent querying its own working tree sees its own edits without a restart. `ken index --watch` (default) keeps the CLI alive in a similar role; `ken index --no-watch` restores the v0.2 build-and-exit behavior. Tombstones for deletes, no compaction — memory grows monotonically with cumulative edit volume, which is fine for typical agent-session lifetimes; compaction is a v0.3.x trigger if multi-day sessions hit pressure. Atomic-snapshot reads keep query latency unchanged from v0.2. Implementation: [`internal/search/watch.go`](internal/search/watch.go), design rationale in [`docs/DECISIONS.md` ADR-012](docs/DECISIONS.md#adr-012-incremental-indexing-via-fsnotify--atomic-snapshot-swap).
- **Token-budget recall — agent-side efficiency vs grep+Read.** Measured at v0.3.0; ken surfaces the qrel target chunk in ~44× fewer tokens than the tokenized-grep baseline at K=10 on semble's NL queries (82% recall vs 99%), and in ~10,000× fewer tokens on the 280K-file CoIR-CSN-Python corpus (91% vs 100% recall). Grep wins on recall completeness; ken wins decisively on agent-input cost. See [`docs/BENCH.md` "Token-budget recall"](docs/BENCH.md#token-budget-recall--agent-side-efficiency).

## How this was built

ken is a port. The retrieval algorithm is verbatim from [MinishLab/semble](https://github.com/MinishLab/semble) (Python). The Go implementation was written by Claude under a fixed set of constraints: pure Go / no cgo, algorithm constants ported verbatim never tuned, original source wins whenever Claude's reconstruction of an algorithm detail diverges from semble's live code.

That last rule caught five material errors during the rerank-pipeline port (see [docs/DESIGN.md §7](docs/DESIGN.md#7-hybrid-retrieval--rerank)) — each one a confident-sounding hallucination of an algorithm detail that turned out to be wrong when checked against the Python source. The discipline of always checking is human-supplied.

Benchmark numbers in the [Comparison table](#comparison-to-semble) are measured against semble's own harness using its native NDCG@10 metric, not synthesized — reproducible via [`docs/BENCH.md`](docs/BENCH.md). The 11k-input tokenizer parity test ([`scripts/parity_dump.py`](scripts/parity_dump.py) + [`internal/embed/parity_test.go`](internal/embed/parity_test.go)) was a human call — "the 18-case spot-check isn't enough" — and surfaced three real bugs the spot-check missed.

The ADR-style record of every architectural decision (alternatives considered, consequences) lives in [`docs/DECISIONS.md`](docs/DECISIONS.md).

## Acknowledgments

ken stands on MinishLab's shoulders. The retrieval algorithm, the model, the entire approach to embedding-table-driven code search — all theirs.

- **[semble](https://github.com/MinishLab/semble)** — the original Python implementation. ken's retrieval pipeline is a verbatim port; constants and pipeline order come straight from `search.py` and `ranking/*.py`. © Thomas van Dongen, MIT.
- **[model2vec](https://github.com/MinishLab/model2vec)** — the static-embedding library whose three-tensor format ken implements. © Thomas van Dongen, MIT.
- **[potion-code-16M](https://huggingface.co/minishlab/potion-code-16M)** — model weights, distilled from `nomic-ai/CodeRankEmbed` (MIT) which is itself initialized from `Snowflake/snowflake-arctic-embed-m-long` (Apache-2.0). © Minish Lab. Redistributed per [`NOTICE`](NOTICE).

## License

ken is [MIT-licensed](LICENSE). It bundles attribution for the redistributed model weights and their upstream lineage in [`NOTICE`](NOTICE), and a generated list of Go-module dependency licenses in [`THIRD_PARTY_LICENSES.md`](THIRD_PARTY_LICENSES.md). Every link in the provenance chain is permissive (MIT, Apache-2.0, and MPL-2.0 — the MPL-2.0 entry (`go-sql-driver/mysql` v1.10.0, added in v0.7.2) is file-level copyleft only and is safe to redistribute when used as an unmodified library); see [docs/DESIGN.md §6](docs/DESIGN.md#6-license--attribution-chain).

For contributors: see [`CLAUDE.md`](CLAUDE.md) for build / test / formatting conventions and the project's invariants (precision contract, stdout/stderr contract).
