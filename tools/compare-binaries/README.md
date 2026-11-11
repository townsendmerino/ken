# compare-binaries

A before/after comparison of the `ken` and `ken-mcp` binaries, for changes that
must not alter what they print: refactors, moved wiring, dependency bumps.

```bash
# Build the two versions into separate directories.
git worktree add --detach /tmp/ken-old origin/main
(cd /tmp/ken-old && GOWORK=off go build -o /tmp/old/ken ./cmd/ken && GOWORK=off go build -o /tmp/old/ken-mcp ./cmd/ken-mcp)
GOWORK=off go build -o /tmp/new/ken ./cmd/ken && GOWORK=off go build -o /tmp/new/ken-mcp ./cmd/ken-mcp

python3 tools/compare-binaries/compare.py --old /tmp/old --new /tmp/new
```

It runs both builds over a small fixture corpus (the `testdata/repo` files, a
few of ken's own sources, a generated file and a minified bundle) under 30
environment settings, in bm25 and hybrid mode, and prints one line per setting:
`same`, `DIFF` with the first differing lines, or `timing`.

- **`ken`:** search (text and `--json`), `index --no-watch`, `bench`,
  `build-index` (file hash), `index --write-snapshot` (snapshot hash), `status`,
  `status --json`, `doctor`.
- **`ken-mcp`**, over JSON-RPC on stdio: `tools/list`, search (markdown, JSON,
  batch with `explain`, `max_tokens`), `find_related`, `definition`, `callers`,
  `references`, `outline`, `symbols`, `status`, and the hash of the snapshot it
  writes.

Version stamps, timestamps, durations and scratch paths are normalized away.
`--ignore-status-key KEY` drops a status field that a change adds on purpose.
`--keep DIR` saves every setting's full output for inspection.

One setting, `KEN_ENRICH_FILE_BUDGET_MS=1`, is reported as `timing`: a 1 ms
wall-clock parse budget gives different results between two runs of the same
binary. Comparing a build against itself should print 29 identical plus that
one for `ken`, and 30 identical for `ken-mcp`, in about two minutes.

Hybrid mode needs the embedding model at `~/.ken/model`. `ken perf` is not
compared: its output is timings. `mcp.Run` has no shipped binary over a code
corpus and is not covered here.
