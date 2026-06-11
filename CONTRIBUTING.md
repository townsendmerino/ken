# Contributing to ken

Thanks for your interest! ken is a pure-Go, no-cgo port of
[MinishLab/semble](https://github.com/MinishLab/semble) — a hybrid
code-search engine served over MCP. The substance of the developer
docs lives in **[docs/DEVELOPERS.md](docs/DEVELOPERS.md)**; this file is
the quick on-ramp.

## Setup

ken targets the Go toolchain pinned in [`go.mod`](go.mod) (1.26.x). With
the default `GOTOOLCHAIN=auto`, an older system Go auto-downloads it.

```bash
go build ./...     # build everything
go test ./...      # all tests (model-gated tests skip cleanly without a model)
```

## The bar for a change

CI runs `golangci-lint` + `go vet` + `gofmt -l cmd internal mcp bench`
(must print nothing) + the full test suite. Run all of it locally before
opening a PR:

```bash
go test ./... && go vet ./... && gofmt -l cmd internal mcp bench && golangci-lint run ./...
```

Two project-specific disciplines worth knowing up front:

- **Keep the tree `gofmt`-clean and `go test ./...` green on every
  commit.** No exceptions.
- **Quality and perf claims ship with a reproducible command.** Retrieval
  correctness is defined as parity with semble / the Python reference, not
  "looks reasonable" — see [docs/BENCH.md](docs/BENCH.md) and
  [docs/internal/PERF.md](docs/internal/PERF.md). A change that moves a
  ranking number needs the before/after.

## Concurrency

`ken-mcp` dispatches every tool call (`search`, `find_related`, the
structural tools) on its own goroutine, so anything on the search path runs
concurrently. **A package-level `var` that is written at runtime needs a
guard from day one** — a lazy memoization cache at package scope is the easy
trap (it once shipped a real `-race` bug in `defPatternCache`).

Convention: tag package-level shared state with a `// concurrency:` line so
the next reader (and reviewer) sees the contract immediately:

- **Mutated at runtime** → `// concurrency: guarded by <mutex>` (or use a
  `sync.Map`). Canonical examples: `defPatternCache` (RWMutex,
  `internal/search/rerank.go`) and `parserPools` (sync.Map,
  `internal/structural/index.go`).
- **Read-only after init** → `// concurrency: read-only after init` — safe
  for concurrent reads, but it documents that adding a runtime write would
  introduce a race.

When you add concurrency-relevant state, add a `-race` regression test that
actually drives the contended path (a shared input often won't — see
`TestDefPatternCache_ConcurrentDistinctSymbols_NoRace` for why distinct
inputs matter).

## Where things go

- **Architecture / why it's built this way** — [ARCHITECTURE.md](ARCHITECTURE.md)
  (current-state map), [docs/DESIGN.md](docs/DESIGN.md) (spec), and
  [docs/internal/DECISIONS.md](docs/internal/DECISIONS.md) (every ADR —
  read these before proposing a structural change; most paths have already
  been explored).
- **Public API stability** — the frozen 1.0 surface vs best-effort
  surfaces is documented in
  [docs/DEVELOPERS.md → Public API surface](docs/DEVELOPERS.md#public-api-surface).
  Breaking the frozen surface requires a major bump.
- **Adding a chunker, structural extractor (new language), or MCP tool** —
  step-by-step in
  [docs/DEVELOPERS.md → Adding a chunker, extractor, or MCP tool](docs/DEVELOPERS.md#adding-a-chunker-extractor-or-mcp-tool)
  and, for languages, [docs/internal/add-a-language.md](docs/internal/add-a-language.md).
- The reusable algorithm packages (`chunk`, `bm25`, `embed`, `ann`,
  `encoder`, …) live in the sibling
  [aikit](https://github.com/townsendmerino/aikit) module (ADR-034) — edit
  them there, then bump ken's pin.

## Commits & PRs

Conventional, imperative commit subjects (`fix(search): …`,
`docs: …`). Keep PRs focused; include the reproduction for any
quality/perf claim. See
[docs/DEVELOPERS.md → Pull requests](docs/DEVELOPERS.md#pull-requests).

## Reporting

- **Bugs / features** — [GitHub Issues](https://github.com/townsendmerino/ken/issues).
- **Security vulnerabilities** — **do not** open a public issue; see
  [SECURITY.md](SECURITY.md).

## License

ken is MIT ([LICENSE](LICENSE)). By contributing you agree your
contributions are licensed under the same terms.
