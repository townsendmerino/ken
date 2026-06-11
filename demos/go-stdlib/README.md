# ken-demo-go-stdlib

A self-contained code-search MCP server for the **Go standard library**, powered by [ken](https://github.com/townsendmerino/ken) (a pure-Go hybrid BM25 + Model2Vec embedding code search). Plug it into any MCP-compatible agent (Claude Desktop, Claude Code, Cursor, …) and ask questions about the Go stdlib codebase; the agent gets back ranked `path:line` + verbatim snippets.

The search index and the embedding model are **baked into the binary** — no source checkout, no model download, no network, no configuration. It starts in a few seconds and serves queries in tens of milliseconds.

This is **ken's flagship demo.** The Go stdlib is the one corpus every Go developer already has on disk and knows cold. You can verify every answer instantly ("yes, that's exactly right"), and you can run the *exact* demo against your own `$GOROOT/src` in 30 seconds — see "Reproduce against your own GOROOT" below.

## What it indexes

- **Corpus:** Go **1.26.3** stdlib at `$GOROOT/src` with `cmd/` and `*/testdata/*` excluded — what a Go developer thinks of as "the stdlib" (`runtime`, `net`, `encoding`, `sync`, `io`, `context`, `crypto`, `os`, `syscall`, ...).
- **Files:** ~4,200 `.go` files indexed.
- **Chunks:** 35,708 (chunker: `regex`, mode: `hybrid`).
- The index is a point-in-time snapshot pinned to Go 1.26.3; it does not update. A new release ships a refreshed index against a newer Go version.

## Install

1. Download the archive for your platform from the release page and extract it.
2. Move the binary onto your `$PATH` (optional):
   ```sh
   mv ken-demo-go-stdlib /usr/local/bin/
   ```

## Register in your MCP client

Claude Code (`~/.claude.json` or via `claude mcp add`):

```sh
claude mcp add ken-demo-go-stdlib -s user -- /usr/local/bin/ken-demo-go-stdlib
```

Claude Desktop (`claude_desktop_config.json`) / any stdio-MCP client:

```json
{
  "mcpServers": {
    "ken-demo-go-stdlib": {
      "command": "/usr/local/bin/ken-demo-go-stdlib"
    }
  }
}
```

No environment variables and no `repo` argument — the corpus is fixed inside the binary. Restart your client; the `search`, `find_related`, and `status` tools appear. (The embedded-corpus demo is built on `mcp.Run`, which exposes those three. The structural tools — `definition` / `references` / `callers` / `outline` / `symbols` / `recently_changed` — belong to the full `ken-mcp` server run against a live checkout, not the embedded binary.)

## What to ask

The 14 vetted demo queries are at [`QUERIES.md`](QUERIES.md) with the canonical answer for each. The headline ones to try first:

- *"where is goroutine scheduling decided"* — single-targeted hit at `runtime/proc.go`, vs **230 grep hits across 58 files**.
- *"how does context cancellation reach an in-flight HTTP request"* — lands on `net/http/transport.go::prepareTransportCancel`, vs **345 grep hits across 22 files**.
- *"where do goroutines block on a full channel"* — single hit at `runtime/chan.go`, vs **1,103 grep hits across 131 files**.

To exercise the structural tools (`definition("WithCancel")`, `references("Marshal")`, `outline("net/http/server.go")`, `outline("encoding/json/encode.go")`), run the full `ken-mcp` against a `$GOROOT/src` checkout — the embedded demo binary serves `search` / `find_related` / `status` only.

## Resource usage (Apple M1 Pro, measured)

| | |
|---|---|
| download size | ~129 MB per platform (compressed tarball; ~190 MB extracted) |
| startup (one-time, loads the embedded index) | ~2 s |
| query latency after startup | tens of ms |
| resident memory while running | ~600 MB |

Allow ~1 GB free RAM.

## Reproduce against your own `$GOROOT/src`

The 30-second self-serve repro — every demo claim above is verifiable against your own Go install.

```sh
# 1. Assemble the curated corpus (mirrors what this demo binary indexes).
mkdir -p /tmp/go-stdlib-curated
rsync -a --delete \
  --exclude='cmd/' \
  --exclude='testdata/' \
  --exclude='*/testdata/' \
  --exclude='vendor/' \
  "$(go env GOROOT)/src/" /tmp/go-stdlib-curated/

# 2. Point your already-installed ken-mcp at it (no extra binary needed
#    if you have ken installed normally; see https://github.com/townsendmerino/ken).
KEN_MCP_DEFAULT_REPO=/tmp/go-stdlib-curated ken-mcp
```

Then run the [`QUERIES.md`](QUERIES.md) queries through your MCP client. Results should match the canonical answers there — modulo your Go version's specific chunk line numbers if you're not on Go 1.26.3.

## Platforms

`darwin/arm64`, `darwin/amd64`, `linux/amd64`, `linux/arm64`. Static, no cgo, no dynamic dependencies. Verify your download against `SHA256SUMS`.

## Links

- ken: https://github.com/townsendmerino/ken
- Vetted demo queries: [`QUERIES.md`](QUERIES.md)
- Companion demos: [`../kubernetes/`](../kubernetes/), [`../postgres/`](../postgres/)
