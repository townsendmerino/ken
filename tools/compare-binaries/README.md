# compare-binaries

A before/after comparison of the `ken` and `ken-mcp` binaries, for changes that
must not alter what they print: refactors, moved wiring, dependency bumps.

```bash
# Build the two versions into separate directories.
git worktree add --detach /tmp/ken-old origin/main
(cd /tmp/ken-old && GOWORK=off go build -o /tmp/old/ken ./cmd/ken && GOWORK=off go build -o /tmp/old/ken-mcp ./cmd/ken-mcp)
GOWORK=off go build -o /tmp/new/ken ./cmd/ken && GOWORK=off go build -o /tmp/new/ken-mcp ./cmd/ken-mcp
# Optional, to compare the mcp.Run shape too (build it in both trees):
GOWORK=off go build -o /tmp/new/runbin ./tools/compare-binaries/runbin

HOME=$(mktemp -d) python3 tools/compare-binaries/compare.py \
    --old /tmp/old --new /tmp/new --model /Users/me/.ken/model
```

**HOME must be a scratch directory.** The binaries read and write under
`~/.ken` (the rerank cache, usage stats, downloaded models), so a run against
your real home can overwrite real files. The script refuses to start unless
`HOME` is an existing directory under a temporary directory and is not your own
home. Pass the embedding model with `--model` as an absolute path; it is only
read.

It runs both builds over a small fixture corpus (the `testdata/repo` files, a
few of ken's own sources, a generated file and a minified bundle) under 30
environment settings, in bm25 and hybrid mode, and prints one line per setting:
`same`, `DIFF` with the first differing lines, or `timing`.

- **`ken`:** search (text and `--json`), `index --no-watch`, `bench`,
  `build-index` (file hash), `index --write-snapshot` (snapshot hash), `status`,
  `status --json`, `doctor`.
- **`ken-mcp`**, over JSON-RPC on stdio: `tools/list`, search (markdown, JSON,
  batch with `explain`, `max_tokens`), `find_related`, `definition`, `callers`,
  `references`, `outline`, `symbols`, `status`, the hash of the snapshot it
  writes, and its stderr log at info level (lines sorted, timestamps removed).
  Each over a live build and over a pre-built `.ken/index.bin` written by
  `ken build-index`. With `--rerank-model DIR`, two more sessions under the
  default settings run with `KEN_MCP_RERANK=1`: one with no cache file, one
  with the file the first saved, whose hash is compared too.

- **`runbin`**, when both directories hold one: a test server built on
  `mcp.Run` over the same corpus (`tools/compare-binaries/runbin`; ken ships no
  `mcp.Run` binary over code). Search, `find_related` and `status` over a live
  build, over a pre-built index written by `ken build-index`, and with DB
  chunks from a SQLite file (`reindex_db`, then search again). `runbin` links
  only `mcp` and `mcp/db`, like a third-party server would.

Version stamps, timestamps, durations and scratch paths are normalized away.
`--ignore-status-key KEY` drops a status field that a change adds on purpose.
`--keep DIR` saves every setting's full output for inspection.

One setting, `KEN_ENRICH_FILE_BUDGET_MS=1`, is reported as `timing`: a 1 ms
wall-clock parse budget gives different results between two runs of the same
binary. Comparing a build against itself should print 29 identical plus that
one for each of `ken`, `ken-mcp` and `runbin`, in about five minutes.

A setting that differs is run once more; if the builds agree on the retry it
is reported as `flaky` rather than `DIFF`. The known source is hybrid search
with DB chunks: every DB chunk's text starts with `-- indexed at <UTC time,
to the minute> from <host>`, that line is indexed and embedded with the rest,
and so hybrid scores shift slightly when the minute changes. If the old and
new builds run their DB session on either side of a minute boundary they
differ in the fifth digit of some scores, while this script, which replaces
timestamps with `<TS>`, shows identical text. BM25 scores do not move.

`ken perf` is not compared: its output is timings. In the DB variant the
harness waits until the DB chunks are being served before its first compared
query, because `mcp.Run` applies them asynchronously at startup, and both
sides read one shared SQLite file, because the DB chunks' text names it.
