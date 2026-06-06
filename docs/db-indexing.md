# Indexing database schemas (v0.7.0, expanded in v0.7.1)

> Moved out of the README in the docs reorg; this is the full reference. Quick summary lives in the [README](../README.md#what-ken-indexes).

Agents working on a real codebase need schema context **alongside** the code. ken v0.7.0 indexes both. An agent answering "how do users get authenticated" gets the Go function doing auth, the SQL it executes, the `users` table definition, AND the FK relationships from `sessions.user_id` — all in one ranked result list. Design rationale in [ADR-017](internal/DECISIONS.md#adr-017-database-schema-indexing--two-tier-static-sql--live-postgres-with-documented-pii-stance).

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

Folding covers `ADD COLUMN`, `DROP COLUMN`, `ALTER COLUMN ... TYPE`, `ADD CONSTRAINT`, `DROP CONSTRAINT`, and — as of **v0.8.1 Part C** ([ADR-022](internal/DECISIONS.md#adr-022-rename-column--rename-constraint-folding-via-eager-application-v081-part-c), closes [#14](https://github.com/townsendmerino/ken/issues/14)) — **`RENAME COLUMN` and `RENAME CONSTRAINT`**. RENAME is applied eagerly during replay: a `RENAME COLUMN old TO new` mutates the in-flight folded table so subsequent ALTERs see the post-rename state, and this-table column references inside constraint definitions (PK / UNIQUE / FK source-side / CHECK) get rewritten via a word-boundary regex scoped to the first parenthesized group. Cross-table FK target-side column references (the `REFERENCES other(remote)` portion) are NOT propagated — that's cross-table dependency requiring migration-DAG analysis and remains out of scope. Operators using MySQL's `CHANGE old new TYPE` syntax (rename + retype in one statement) see the BOTH-chunks fallback below.

**RENAME folding is a Tier-1 chunk-content fidelity improvement, NOT a search-ranking improvement.** ken's hybrid retrieval recall@10 numbers ([`BENCH.md`](BENCH.md)) measure a different system — they're about whether the right chunk surfaces in the top-10 results, not about whether the chunk contains post-rename column names. v0.8.1 Part C closes the latter gap without affecting the former.

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

**MariaDB is first-class as of v0.8.1** ([ADR-021](internal/DECISIONS.md#adr-021-mariadb-first-class-engine-support-v081-part-b)) — same `KEN_DB_DSN` env var, same MySQL DSN forms above, same `go-sql-driver/mysql` driver. CI's `test-db-integration` job now runs the integration suite against both `mysql:8` and `mariadb:11-jammy` service containers; the v0.8.1 normalization layer strips MariaDB's legacy `bigint(20)` / `int(11)` integer display widths so chunks stay byte-identical across engines. End users see no operator-visible difference — point ken at MariaDB the same way you point it at MySQL.

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

Wildcards (e.g. `KEN_DB_SCHEMAS=tenant_*`) are explicitly out of scope for v0.7.2 — multi-tenant operators can fall back to the explicit form `KEN_DB_SCHEMAS=tenant_001,tenant_002,...` until field signal calls for wildcard syntax. See [ADR-019](internal/DECISIONS.md#adr-019-mysql-engine--schema-filtering-for-multi-schema-dev-databases) for the rejected-alternatives audit trail.

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

See [ADR-020](internal/DECISIONS.md#adr-020-listennotify-push-based-schema-change-detection-v080-part-1) for the alternatives considered (auto-install rejected; per-table opt-in rejected; replace-interval rejected; no-debouncing rejected; faked-MySQL rejected).

### Agent-triggered reindex (`reindex_db` tool, v0.8.0 Part 2)

Agents can refresh ken's view of the database schema on demand by calling the `reindex_db` MCP tool. Useful after the agent has run a migration, or before asking a schema-dependent question:

> **User:** "I just ran migration 042 that adds the `email_verified` column to `users`. Does the schema reflect that?"
> **Agent:** *(calls `reindex_db`)* → *(calls `search "users table"`)* → returns the post-migration schema.

**Always available** when `KEN_DB_DSN` is set; no env var to enable. When no DB is configured, the tool is not registered at all (the agent's `tools/list` shows only `search` and `find_related`).

**Engine-agnostic.** Works for Postgres, MySQL, and SQLite — the tool is a thin wrapper around the same `Refresher` that drives `KEN_DB_REINDEX_INTERVAL`, SIGHUP, and (Postgres) LISTEN/NOTIFY.

**Fail-fast on contention.** If a reindex is already in flight (LISTEN burst, interval tick, SIGHUP, or prior `reindex_db` call), the tool returns `Reindex already in progress; nothing to do.` immediately rather than queuing. The agent can retry, back off, or proceed with stale data based on its workflow — no silent queueing, no unbounded memory growth, no time-based cooldown env vars.

**Pairs with LISTEN/NOTIFY and `KEN_DB_REINDEX_INTERVAL`.** Push notifications cover Postgres deployments with the trigger installed; interval polling covers MySQL / SQLite and the LISTEN-not-set-up Postgres case; `reindex_db` covers the case where the agent itself caused the schema change and knows it needs to refresh.

See [ADR-020 Part 2](internal/DECISIONS.md#part-2-agent-callable-reindex-via-reindex_db-mcp-tool-v080-part-2) for the alternatives considered (cooldown, queue, async-return, env-var-disable, auto-call-from-search all rejected with mechanism-level failure modes).

### Embedded DB support for SDK authors (v0.8.0 Part 3, opt-in)

SDK authors using [`mcp.Run`](DEVELOPERS.md#mcprun-library) (the v0.6.0 embedded-corpus entrypoint) can wire Tier 2 DB support — schema introspection, optional LISTEN/NOTIFY, optional interval reindex, and the `reindex_db` MCP tool — via the new opt-in `mcp/db` package:

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

**Chunk integration is end-to-end.** Calling `reindex_db` from an agent against an `mcp.Run + mcp/db.Setup` binary runs the introspection AND makes the new DB chunks searchable in the agent's next `search` / `find_related` call. The pipeline: `mcp.Run` wraps the embedded `*search.Index` in `atomic.Pointer[search.Index]`; `mcp/db.Refresher.Start` (called by `mcp.Run` on startup) wires the swap callback to `*search.Index.WithExtraChunks` + atomic-pointer store; each refresh rebuilds against the original corpus + the latest DB chunks. `cmd/ken-mcp` continues to use `*WatchedIndex.SetExtraChunks` for its fsnotify-rooted path; the SDK-author + CLI surfaces converge on the same `Refresher` + `reindex_db` semantics. See [ADR-020 Part 3](internal/DECISIONS.md#part-3-opt-in-mcpdb-package-preserving-v060-binary-size-contract-v080-part-3) for the full design + the rejected alternatives.
