# ken-demo-kubernetes

A self-contained code-search MCP server for the **Kubernetes** source tree, powered by [ken](https://github.com/townsendmerino/ken) (a pure-Go hybrid BM25 + Model2Vec embedding code search). Plug it into any MCP-compatible agent (Claude Desktop, Claude Code, Cursor, …) and ask questions about the Kubernetes codebase; the agent gets back ranked `path:line` + verbatim snippets.

The search index and the embedding model are **baked into the binary** — no source checkout, no model download, no network, no configuration. It starts in a few seconds and serves queries in tens of milliseconds.

## What it indexes

- **Corpus:** `github.com/kubernetes/kubernetes` source (vendored dependencies and generated code excluded — `vendor/`, `zz_generated*.go`, `*.pb.go`, `*_test.go`, `applyconfigurations/`, OpenAPI specs).
- **Chunks:** 59,795 (chunker: `regex`, mode: `hybrid`).
- The index is a point-in-time snapshot of the source it shipped with; it does not update. A new release ships a refreshed index.

## Install

1. Download the archive for your platform from the release page and extract it.
2. Move the binary onto your `$PATH` (optional):
   ```sh
   mv ken-demo-kubernetes /usr/local/bin/
   ```

## Register in your MCP client

Claude Desktop (`claude_desktop_config.json`) / any stdio-MCP client:

```json
{
  "mcpServers": {
    "ken-demo-kubernetes": {
      "command": "/usr/local/bin/ken-demo-kubernetes"
    }
  }
}
```

No environment variables and no `repo` argument — the corpus is fixed inside the binary. Restart your client; the `search` and `find_related` tools appear.

## Resource usage (Apple M1 Pro, measured)

| | |
|---|---|
| download size | ~215 MB per platform |
| startup (one-time, loads the embedded index) | ~4 s |
| query latency after startup | ~60 ms |
| resident memory while running | ~1.4 GB |

Allow ~2 GB free RAM.

## Platforms

`darwin/arm64`, `darwin/amd64`, `linux/amd64`, `linux/arm64`. Static, no cgo, no dynamic dependencies. Verify your download against `SHA256SUMS`.

## Links

- ken: https://github.com/townsendmerino/ken
- Demo write-up: *(blog post link TBD)*
