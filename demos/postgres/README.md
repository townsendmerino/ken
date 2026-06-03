# ken-demo-postgres

A self-contained code-search MCP server for the **PostgreSQL** source tree, powered by [ken](https://github.com/townsendmerino/ken) (a pure-Go hybrid BM25 + Model2Vec embedding code search). Plug it into any MCP-compatible agent (Claude Desktop, Claude Code, Cursor, …) and ask questions about the PostgreSQL codebase; the agent gets back ranked `path:line` + verbatim snippets.

The search index and the embedding model are **baked into the binary** — no source checkout, no model download, no network, no configuration. It starts in a few seconds and serves queries in tens of milliseconds.

## What it indexes

- **Corpus:** `github.com/postgres/postgres` at **tag `REL_18_0`** (documentation and regression-test fixtures excluded — `doc/`, `src/test/`, `*.out`).
- **Chunks:** 69,601 (chunker: `treesitter`, mode: `hybrid`).
- `treesitter` gives real C AST boundaries — ken's `regex` chunker has no C ruleset, so it would degrade `.c`/`.h` files to line windows.
- The index is a point-in-time snapshot of the source it shipped with; it does not update. A new release ships a refreshed index.

## Install

1. Download the archive for your platform from the release page and extract it.
2. Move the binary onto your `$PATH` (optional):
   ```sh
   mv ken-demo-postgres /usr/local/bin/
   ```

## Register in your MCP client

Claude Desktop (`claude_desktop_config.json`) / any stdio-MCP client:

```json
{
  "mcpServers": {
    "ken-demo-postgres": {
      "command": "/usr/local/bin/ken-demo-postgres"
    }
  }
}
```

No environment variables and no `repo` argument — the corpus is fixed inside the binary. Restart your client; the `search` and `find_related` tools appear.

## Resource usage (Apple M1 Pro, measured)

| | |
|---|---|
| download size | ~240 MB per platform |
| startup (one-time, loads the embedded index) | ~3 s |
| query latency after startup | ~35 ms |
| resident memory while running | ~1.5 GB |

Allow ~2 GB free RAM.

## Platforms

`darwin/arm64`, `darwin/amd64`, `linux/amd64`, `linux/arm64`. Static, no cgo, no dynamic dependencies. Verify your download against `SHA256SUMS`.

## Links

- ken: https://github.com/townsendmerino/ken
- Demo write-up: *(blog post link TBD)*
