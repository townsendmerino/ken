# CLAUDE.md

`ken` is a **pure-Go, no-cgo** port of [MinishLab/semble](https://github.com/MinishLab/semble): hybrid code search (BM25 +
Model2Vec embeddings, RRF fusion, rerank heuristics), a CLI (`cmd/ken`), an MCP server (`cmd/ken-mcp`) and a library
(`engine`). Read [`docs/DESIGN.md`](docs/DESIGN.md) (algorithm spec, precision contracts, risk register) before any
non-trivial change; [`ARCHITECTURE.md`](ARCHITECTURE.md) maps the packages, [`docs/internal/DECISIONS.md`](docs/internal/DECISIONS.md)
holds the ADRs, [`docs/BENCH.md`](docs/BENCH.md) the benchmarks, and the [README](README.md) the install snippets and env vars.

## Ownership

**Claude Code is the sole editor of this repository.** The Claude app is for design discussion only and does not modify
files here. The algorithm packages (`chunk`, `bm25`, `embed`, `ann`, `encoder`, `topk`, `fuse`) live in the sibling
[`aikit`](https://github.com/townsendmerino/aikit) repo (ADR-034): edit them there. Keep the tree `gofmt`-clean and
`go test ./...` green on every change.

## Commands

```bash
go build ./... && go vet ./...                     # vet must be clean
go test ./...                                      # all tests; semantic/hybrid tests skip without testdata/model
go test ./internal/search/ -run TestSerializeRoundtrip_BM25 -v
make gofmt-check                                   # CI's gofmt gate (scope: Makefile GOFMT_DIRS)
GOWORK=off make check                              # pre-push: gofmt, the pinned golangci-lint, vet, tests, as CI runs them
go run ./cmd/ken index <path>                      # watches by default; --no-watch builds once
go run ./cmd/ken search <path> <query>... [-k N] [--mode=bm25|semantic|hybrid]
go run ./cmd/ken doctor
```

Hybrid and semantic modes find the model at `--model`, then `$KEN_MODEL_DIR`, `~/.ken/model` (`ken download-model` fills
it) and `./testdata/model` (not committed; `testdata/README.md`).

`go.mod`'s `go` directive is the single toolchain pin. After a `go` or aikit bump run `go mod tidy`, and for aikit re-run
the parity and recall checks (aikit's `TestGolden_EmbeddingCosine` against a real model). A golangci-lint older than the
toolchain cannot read its export data and checks nothing: use the Makefile's pinned version.

## Hard rules

- **Pure Go, no cgo, no per-platform vendored artifacts.** That is the point of the port (one static cross-compiled
  binary); a native dependency's pure-Go alternative goes behind an interface. Dependencies land with the stage that needs them.
- **stdout is `ken-mcp`'s JSON-RPC channel.** Any write to stdout outside the SDK's protocol writer corrupts the stream.
  Read the contract at the top of `cmd/ken-mcp/main.go` before adding any dependency, and audit each new one for default
  writers on stdout. `TestBinary_StdoutIsCleanJSONRPC` enforces it.
- **Embedding inference indexes through `mapping[]` and accumulates in float64** (`docs/DESIGN.md` §4): float32 silently
  fails ≥ 1−1e-5 cosine on longer inputs. Empty or all-`[UNK]` input is a zero vector, not NaN.
- **Ranking may diverge from semble (ADR-050), one measured change at a time.** The shipped pipeline is still the verbatim
  port. A divergence (fusion weights, boosts, penalties, file saturation, candidate counts) is one change per ADR, with a
  pre-registered gate on a benchmark that measures what it is for and NDCG@10 on semble's 63-repo set reported beside it;
  `docs/DESIGN.md` §7 keeps semble's value and source line for every constant and marks each diverged one with its ken
  value and ADR. The semble reference run stays at semble's values, so port bugs stay findable. **No ranking change ships
  without Francis's yes.**
- **Correctness is parity with Python, not "looks reasonable".** Tokenizer and embedding parity are aikit's gates; ken's
  own is the retrieval benchmark (`docs/BENCH.md`, `internal/search/recall_decomp_test.go`).

## Go style and structure

- **Structure over growth.** Do not append unrelated state to a struct, file or package that is already large
  (`internal/gotypes`, `internal/search`, `mcp`): group related state into a cohesive sub-struct, or split along a clean
  seam early. A new test seam is an injected option, a struct field or a constructor parameter, not a package-global
  `*ForTest` hook or a side-effect `init()` registration: globals break `t.Parallel()` and hide coupling. Existing
  globals stay unless touched.
- **An interface lives in the package that consumes it** and stays narrow (a handful of methods). Do not add a wide
  interface mirroring a whole concrete type only so a test can fake it. Existing interfaces stay as they are.
- **The standard library comes before a hand-rolled helper** (`go.mod` targets Go 1.27): `slices` (`Contains`, `Index`,
  `Clone`, `Sort`, `SortFunc`, `Sorted`, `Backward`), `maps` (`Clone`, `Copy`, `Keys`), `strings` (`Cut`, `CutPrefix`,
  `CutSuffix`, `SplitSeq`, `FieldsSeq`), `errors.AsType`, `sync.WaitGroup.Go`, `cmp.Or`, `min`/`max`, `for range n`,
  `new(expr)` and `math/rand/v2`. Keep `math/rand` v1 where a fixed seed must keep reproducing a recorded result
  (`bench/contextbench`'s bootstrap resampling).
- **Run `GOWORK=off go fix -diff` on the packages you touch** and review each suggestion before applying it: keep
  imports grouped, and check nil-map behavior before a hand copy becomes `maps.Clone` (a nil map clones to nil).
- **One receiver kind per type:** do not mix pointer and value receivers; a type with mutating methods, a lock or a
  large body takes pointer receivers. Return a modified slice instead of taking a `*[]T` out-parameter; explicit
  allocation-free scratch reuse and `sync.Pool` buffers are fine.
