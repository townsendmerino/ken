# ken demos

Downloadable, self-contained `ken-mcp` servers for popular OSS codebases. Each is a single static binary with a **pre-built search index + the Model2Vec model baked in** via `//go:embed` (ADR-024 / `mcp.Run`), so it starts in ~2-4 s with no corpus checkout, no model download, and no live indexing.

| demo | corpus | chunker | mode | chunks | role |
|---|---|---|---|---|---|
| [`go-stdlib`](go-stdlib/) ⭐ | Go 1.26.3 stdlib (`$GOROOT/src` minus `cmd/`, `*/testdata/*`) | regex | hybrid | 35,708 | **flagship** — every Go dev knows the corpus by heart; verify answers instantly; reproduce against your own `$GOROOT/src` in 30 s |
| [`kubernetes`](kubernetes/) | kubernetes source (vendor + generated excluded) | regex | hybrid | 59,795 | proof of scale (large polyglot-ish Go monorepo) |
| [`postgres`](postgres/) | postgres source (doc + test fixtures excluded) | treesitter | hybrid | 64,506 | proof of polyglot reach (treesitter chunker on C) |

The **go-stdlib** demo is the flagship: instant audience recognition, instant verifiability (every Go dev knows the stdlib), instant reproducibility against the audience's own `$GOROOT/src`. The two supporting demos exercise different ken capabilities — kubernetes for scale, postgres for the treesitter C-chunker path. See [`go-stdlib/QUERIES.md`](go-stdlib/QUERIES.md) for the 14 vetted Phase 1 queries with their canonical answers + three explicit grep-vs-ken head-to-head comparisons.

## Demo transcripts

Captured agent conversations against these binaries — the actual deliverable the demo write-up draws from — live in [`transcripts/`](transcripts/) (3 questions per codebase; postgres has both a regex and a treesitter arm for the A/B comparison). [`transcript-audit-rubric.md`](transcript-audit-rubric.md) is the rubric each transcript was graded against (grounding, citation accuracy, retrieval quality).

## Why in-tree

Originally the postgres demo *had* to live in-tree: the **treesitter** chunker was at `internal/chunk/treesitter`, and Go forbids importing `internal/` packages across module boundaries. [ADR-032](../docs/DECISIONS.md) promoted the chunker package to a public path (`chunk/treesitter`), so that constraint is gone — the demos could now be separate modules. They stay in-tree for a single paired launch and shared build tooling, not out of necessity. (The k8s demo only needs `regex`, registered transitively via `internal/search`.)

## Build tag + gitignored assets

The demo `main.go` files carry `//go:build kendemo` so they're **excluded from `go build ./...` and CI** — otherwise their `//go:embed index.bin` would fail on any checkout that doesn't have the (large, gitignored) assets present. The embedded assets — `<demo>/index.bin` (~150–170 MB) and `<demo>/model/` (~65 MB) — are **gitignored**; they're regenerated at build time, never committed (they'd bloat ken's git history by ~470 MB).

## Build steps

```sh
# 1. Assemble the go-stdlib curated corpus (cmd/ + testdata excluded —
#    see go-stdlib/README.md for the scope rationale).
mkdir -p /tmp/go-stdlib-curated
rsync -a --delete \
  --exclude='cmd/' --exclude='testdata/' --exclude='*/testdata/' --exclude='vendor/' \
  "$(go env GOROOT)/src/" /tmp/go-stdlib-curated/

# 2. Generate the pre-built index for each demo (kubernetes + postgres
#    excludes come from the corpus's own .gitignore, which the walker honors).
ken build-index /tmp/go-stdlib-curated -o demos/go-stdlib/index.bin   --mode=hybrid --chunker=regex      --model ~/.ken/model
ken build-index <kubernetes-src>       -o demos/kubernetes/index.bin  --mode=hybrid --chunker=regex      --model ~/.ken/model
ken build-index <postgres-src>         -o demos/postgres/index.bin    --mode=hybrid --chunker=treesitter --model ~/.ken/model

# 3. Copy the Model2Vec model (model.safetensors + tokenizer.json + config.json) into each demo's model/ dir.
for d in go-stdlib kubernetes postgres; do
  mkdir -p demos/$d/model
  cp ~/.ken/model/model.safetensors ~/.ken/model/tokenizer.json ~/.ken/model/config.json demos/$d/model/
done

# 4. Build (per platform). CGO_ENABLED=0 → static, portable. The kendemo tag activates the embeds.
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -tags=kendemo -o ken-demo-go-stdlib  ./demos/go-stdlib
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -tags=kendemo -o ken-demo-kubernetes ./demos/kubernetes
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -tags=kendemo -o ken-demo-postgres   ./demos/postgres
# ... repeat for darwin/arm64, darwin/amd64, linux/arm64.
```

A correct build logs `loaded pre-built index from Options.PrebuiltIndex (… chunks …)` at startup — **not** a live `indexed N chunks` line.

## Note on treesitter index determinism

The postgres (treesitter) index chunk count wobbles ~0.1% across rebuilds under machine load (the per-parse timeout occasionally yields a different parse without tripping the fallback counter). The cited demo files (e.g. `autovacuum.c`) are stable; the wobble is confined to a few large files. If you rebuild, spot-check that the published transcript citations still resolve.
