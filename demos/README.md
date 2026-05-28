# ken demos

Downloadable, self-contained `ken-mcp` servers for popular OSS codebases. Each is a single static binary with a **pre-built search index + the Model2Vec model baked in** via `//go:embed` (ADR-024 / `mcp.Run`), so it starts in ~4 s with no corpus checkout, no model download, and no live indexing.

| demo | corpus | chunker | mode | chunks |
|---|---|---|---|---|
| [`kubernetes`](kubernetes/) | kubernetes source (vendor + generated excluded) | regex | hybrid | 59,795 |
| [`postgres`](postgres/) | postgres source (doc + test fixtures excluded) | treesitter | hybrid | 64,506 |

## Why in-tree (not separate repos)

The postgres demo needs the **treesitter** chunker, which lives at `internal/chunk/treesitter` — Go forbids importing `internal/` packages across module boundaries, so a separate module can't register it. Living inside the ken module is what lets `demos/postgres` import it. (The k8s demo only needs `regex`, registered transitively via `internal/search`.)

## Build tag + gitignored assets

The demo `main.go` files carry `//go:build kendemo` so they're **excluded from `go build ./...` and CI** — otherwise their `//go:embed index.bin` would fail on any checkout that doesn't have the (large, gitignored) assets present. The embedded assets — `<demo>/index.bin` (~150–170 MB) and `<demo>/model/` (~65 MB) — are **gitignored**; they're regenerated at build time, never committed (they'd bloat ken's git history by ~470 MB).

## Build steps

```sh
# 1. Generate the pre-built index for each demo (excludes come from the
#    corpus's own .gitignore, which the walker honors).
ken build-index <kubernetes-src> -o demos/kubernetes/index.bin --mode=hybrid --chunker=regex      --model ~/.ken/model
ken build-index <postgres-src>   -o demos/postgres/index.bin   --mode=hybrid --chunker=treesitter --model ~/.ken/model

# 2. Copy the Model2Vec model (model.safetensors + tokenizer.json + config.json) into each demo's model/ dir.
for d in kubernetes postgres; do
  mkdir -p demos/$d/model
  cp ~/.ken/model/model.safetensors ~/.ken/model/tokenizer.json ~/.ken/model/config.json demos/$d/model/
done

# 3. Build (per platform). CGO_ENABLED=0 → static, portable. The kendemo tag activates the embeds.
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -tags=kendemo -o ken-demo-kubernetes ./demos/kubernetes
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -tags=kendemo -o ken-demo-postgres   ./demos/postgres
# ... repeat for darwin/arm64, darwin/amd64, linux/arm64.
```

A correct build logs `loaded pre-built index from Options.PrebuiltIndex (… chunks …)` at startup — **not** a live `indexed N chunks` line.

## Note on treesitter index determinism

The postgres (treesitter) index chunk count wobbles ~0.1% across rebuilds under machine load (the per-parse timeout occasionally yields a different parse without tripping the fallback counter). The cited demo files (e.g. `autovacuum.c`) are stable; the wobble is confined to a few large files. If you rebuild, spot-check that the published transcript citations still resolve.
