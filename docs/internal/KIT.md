# Kit extraction plan — `ken` → `aikit`

Plan to extract ken's reusable algorithm packages into a standalone,
importable Go module (`aikit`) and make ken consume it. This is the full
"Phase 2" — a real module split, not in-repo curation. Companion to
[`docs/DESIGN.md`](../DESIGN.md) (design spec) and [`docs/internal/DECISIONS.md`](DECISIONS.md)
(ADRs). When executed, this plan produces **ADR-034**.

> **Constraint reminder.** Keep the whole tree `gofmt`-clean, `go vet ./...`
> clean, and `go test ./...` green at every committable step. The migration is
> sequenced leaf-first precisely so each step compiles on its own.

---

## 1. Goal and end state

Today every reusable package lives under `internal/`, which Go's compiler
forbids any other module from importing. The goal is a second module whose
packages *can* be imported by future projects, with ken refactored to depend on
it (dogfooding the boundary).

**End state:**

- A new module `github.com/townsendmerino/aikit` holding the portable packages.
- ken's `go.mod` gains `require github.com/townsendmerino/aikit vX.Y.Z`; all
  former `internal/{topk,ann,bm25,embed,coderank}` and `chunk/*` imports point
  at `aikit/...`.
- ken's own app packages (`internal/search`, `internal/db`, `internal/sql`,
  `internal/repo`, `mcp/*`, `cmd/*`) are unchanged in behavior, only in import
  paths.
- A `go.work` at the workspace root lets both modules build in lockstep during
  development; releases pin a tagged `aikit` version.
- `go test ./...` green in both modules; ken's binaries (`ken`, `ken-mcp`,
  `ken-mcp-docs`) build and pass their existing tests unchanged.

---

## 2. What moves, what stays

### Moves to `aikit` (the dependency DAG, leaf-first)

| Order | Package (current path) | New path | Depends on | External deps |
|---|---|---|---|---|
| 1 | `internal/topk` | `aikit/topk` | — | none (stdlib) |
| 2 | `internal/ann` | `aikit/ann` | `topk` | none |
| 3 | `internal/bm25` | `aikit/bm25` | `topk` | none |
| 4 | `internal/embed` | `aikit/embed` | — | `golang.org/x/text` |
| 5 | `internal/coderank` | `aikit/encoder` *(renamed)* | `embed` | none (+ arm64 asm) |
| 6 | `chunk` (root) | `aikit/chunk` | — | none |
| 6a | `chunk/regex` | `aikit/chunk/regex` | `chunk` | none |
| 6b | `chunk/markdown` | `aikit/chunk/markdown` | `chunk` | none |
| 6c | `chunk/treesitter` | `aikit/chunk/treesitter` | `chunk` | `github.com/odvcencio/gotreesitter` |

The DAG is shallow: `topk` and `embed` and `chunk` are roots; `ann`/`bm25`
depend only on `topk`; `encoder` depends only on `embed`; the chunkers depend
only on `chunk`. There are **no back-edges** into ken — confirmed by grep. So
the whole set lifts out cleanly with no circular dependency on ken.

**Total external-dependency surface for `aikit`:** just `golang.org/x/text`
(embed's Unicode normalizer) and `github.com/odvcencio/gotreesitter` (treesitter
chunker). Everything else is stdlib. This keeps `aikit` lightweight for
consumers who only want, say, `topk` + `ann` (they pull neither x/text nor
gotreesitter unless they import embed/treesitter — Go's per-package build graph
handles that automatically).

### Stays in ken (application logic)

`internal/search` (snapshot/watch/atomic-swap orchestration, ADR-012),
`internal/db` + `internal/sql` (schema indexing), `internal/repo` (git-aware
walk), `internal/modelfetch`, `internal/perf`, `mcp/*` (MCP server surface),
`cmd/*` (binaries), `bench/*`. These become **consumers** of `aikit`.

---

## 3. Decisions to lock before starting

1. **Module name / location.** Recommended: a **separate repo**
   `github.com/townsendmerino/aikit`, developed alongside ken via `go.work`
   during the transition. Alternative: a multi-module monorepo (second `go.mod`
   under `ken/aikit/`). Separate repo is cleaner for the stated goal ("reuse by
   another project") and avoids dragging ken's history/deps into consumers.
   *This plan assumes the separate-repo path; differences for the monorepo
   variant are flagged inline.*

2. **`coderank` → `encoder` rename.** The package is a transformer encoder used
   for neural reranking, not a PageRank-style ranker; the name misleads. Rename
   to `encoder` (or `textencode`) on the move — this is the one moment the
   rename is free, since every import line is being rewritten anyway. Update the
   `Encoder` interface doc accordingly.

3. **`chunk` is already public (ADR-032).** Its 1.0-stable surface is the
   `Chunker` interface at `github.com/townsendmerino/ken/chunk`. Moving it to
   `aikit/chunk` is therefore a **breaking change to ken's public API** for
   external `mcp.Run` authors. Mitigation in §7.

4. **Versioning during transition.** Use `go.work` so ken builds against the
   local `aikit` working tree (no tag churn while iterating). Cut `aikit v0.1.0`
   only once its API and tests are stable, then replace the workspace
   dependency with a real `require`. Do **not** ship a ken release that depends
   on an untagged `aikit`.

5. **`testdata` ownership.** The embed/encoder tests depend on per-machine,
   uncommitted assets: `testdata/model/`, `testdata/golden.json`,
   `testdata/parity.jsonl`. These (and the generating scripts) must follow the
   packages into `aikit`. See §6.

---

## 4. Target repository layout (`aikit`)

```
aikit/
  go.mod                      # module github.com/townsendmerino/aikit; go 1.26.3
  go.sum
  LICENSE                     # carried from ken (MIT) + NOTICE
  THIRD_PARTY_LICENSES.md     # x/text, gotreesitter, + Model2Vec/semble attributions
  README.md                   # "portable AI building blocks"; per-pkg portability notes
  topk/        topk.go ...
  ann/         flat.go ...
  bm25/        index.go query.go tokenize.go ...
  embed/       *.go            # Model2Vec inference, safetensors, WordPiece
  encoder/     *.go            # was coderank: attention, rope, layernorm, mlp, q8, dot_arm64
  chunk/       chunk.go registry.go line.go ...
    regex/     chunker.go ...
    markdown/  markdown.go ...
    treesitter/chunker.go ...
  testdata/                   # golden.json, parity.jsonl (committed); model/ stays per-machine
  scripts/     pin_inference.py parity_dump.py   # golden-fixture harness
  docs/        # optional: move the embed/encoder parity notes here
```

---

## 5. Migration sequence (ordered, each step compiles)

Work the DAG leaf-first inside the new module, then flip ken over in one
import-rewrite pass.

**Stage A — stand up `aikit`.**
1. `git init` the `aikit` repo (or `mkdir aikit/` for the monorepo variant).
   `go mod init github.com/townsendmerino/aikit`; set `go 1.26.3` +
   `toolchain go1.26.3` to match ken.
2. Create a `go.work` one level above both checkouts:
   ```
   go 1.26.3
   use ./ken
   use ./aikit
   ```
   With the workspace active, ken can import `aikit/...` against the local tree
   before any tag exists.

**Stage B — move packages leaf-first** (within `aikit`, fixing intra-kit imports
as you go so the module stays green):
3. Move `topk/` → `aikit/topk/`. No import edits (stdlib only). `go test ./topk/`.
4. Move `embed/` → `aikit/embed/`. Update its testdata resolution (§6).
   `go test ./embed/` (parity/golden tests skip without `model/`, as today).
5. Move `ann/` and `bm25/` → `aikit/`; rewrite their internal import
   `…/ken/internal/topk` → `…/aikit/topk`.
6. Move `coderank/` → `aikit/encoder/`; **rename the package** `coderank` →
   `encoder`; rewrite `…/ken/internal/embed` → `…/aikit/embed`. Carry the
   build-tagged files (`dot_arm64.go`, `dot_other.go`, `forward_q8.go`,
   `weights_q8.go`, `linalg_q8.go`) and the `//go:build` tags verbatim — the
   SIMD/unsafe code is platform-sensitive (§8).
7. Move `chunk/` and `chunk/{regex,markdown,treesitter}/` → `aikit/chunk/...`;
   rewrite intra-package imports. `treesitter` keeps the `gotreesitter` require.
8. Add `aikit` deps to its `go.mod` (`golang.org/x/text`, `gotreesitter`);
   `go mod tidy`; `go test ./...` in `aikit` green.

**Stage C — flip ken to consume `aikit`.**
9. Mechanical import rewrite across ken (every file in §9's table):
   - `github.com/townsendmerino/ken/internal/topk`     → `…/aikit/topk`
   - `github.com/townsendmerino/ken/internal/ann`      → `…/aikit/ann`
   - `github.com/townsendmerino/ken/internal/bm25`     → `…/aikit/bm25`
   - `github.com/townsendmerino/ken/internal/embed`    → `…/aikit/embed`
   - `github.com/townsendmerino/ken/internal/coderank` → `…/aikit/encoder`
     (also rename the package selector `coderank.` → `encoder.` at use sites)
   - `github.com/townsendmerino/ken/chunk`             → `…/aikit/chunk`
   - `github.com/townsendmerino/ken/chunk/{regex,markdown,treesitter}` → `…/aikit/chunk/...`
   A `gofmt -r` rule or `find … | xargs sed` does the bulk; `goimports` fixes
   grouping. The `coderank.` → `encoder.` selector rename is the only edit that
   isn't pure path substitution.
10. Delete the now-empty `ken/internal/{topk,ann,bm25,embed,coderank}` and
    `ken/chunk/` trees.
11. Add `require github.com/townsendmerino/aikit v0.0.0` to ken (the `go.work`
    resolves it locally). `go mod tidy` ken — this drops `x/text` and
    `gotreesitter` from ken's direct requires if nothing else uses them (verify;
    `gotreesitter` may now be indirect via aikit only).
12. `go build ./...` and `go test ./...` green in ken under the workspace.

**Stage D — release decoupling.**
13. Stabilize `aikit`'s public surface (§10), tag `aikit v0.1.0`.
14. Replace ken's `v0.0.0` require with `v0.1.0`; `GOFLAGS=-mod=mod go get
    github.com/townsendmerino/aikit@v0.1.0`. Keep `go.work` for local dev but
    ensure ken *also* builds with the workspace disabled
    (`GOWORK=off go build ./...`) — that's the real release path.

---

## 6. testdata and the golden-fixture harness

The embed/encoder parity story (CLAUDE.md "Golden fixture workflow") moves with
the packages, or the contract that makes them trustworthy breaks.

- **Committed fixtures** `testdata/golden.json` (18-case embedding spot-check)
  and `testdata/parity.jsonl` (11,447-input tokenizer parity, `-tags=parity`)
  move to `aikit/testdata/`. The tests resolve them by walking up from the
  package dir; verify the relative path still lands after the move (the embed
  tests reference `testdata/...`; with the package now at module root the path
  shortens — adjust the resolver constants if they assumed repo-root depth).
- **Per-machine model** `testdata/model/` stays uncommitted; carry
  `testdata/README.md` explaining how to populate it. The skip-if-absent
  behavior (a green `go test` with embed/encoder parity tests skipped) is
  preserved.
- **Generator scripts** `scripts/pin_inference.py` and `scripts/parity_dump.py`
  move to `aikit/scripts/`. Update CLAUDE-equivalent docs in `aikit/README.md`:
  `python scripts/pin_inference.py && cp ken_golden.json testdata/golden.json`.
  Keep `allow_nan=False` on the dump (the NaN-sanitization gotcha).
- ken's own `testdata/bench`, `testdata/model` references for *ken's* tests
  (e.g. `internal/search` parity) now need `aikit/embed` + a model dir; confirm
  ken's `--model` resolution order (`~/.ken/model` → `./testdata/model`) is
  unaffected (it's runtime, not import-time).

---

## 7. Public-API / breaking-change handling

ADR-032 promoted `chunk` to a public, 1.0-stable surface at
`github.com/townsendmerino/ken/chunk`, and ADR-016/024's `mcp.Run` lets external
SDK authors register their own chunkers against it. Moving `chunk` to
`aikit/chunk` **breaks those imports**. Options, in order of preference:

1. **Re-export shim in ken** (recommended for one minor-version overlap): keep
   `ken/chunk` as a thin alias package —
   ```go
   package chunk
   import kit "github.com/townsendmerino/aikit/chunk"
   type Chunk = kit.Chunk
   type Chunker = kit.Chunker
   var (Register = kit.Register; Get = kit.Get; Names = kit.Names; …)
   ```
   Lets existing `mcp.Run` authors compile unchanged; deprecate with a doc note
   pointing at `aikit/chunk`; remove in ken's next major.
2. **Hard break + CHANGELOG/migration note** if there are no external consumers
   yet (likely true pre-1.0 — confirm before choosing). Simpler, no shim debt.

`mcp.Run`'s own signature (it takes a `chunk.Chunker`) changes its parameter
type to `aikit/chunk.Chunker`; document in the ADR and CHANGELOG either way.

---

## 8. Sharp edges

- **SIMD / unsafe in `encoder`.** `dot_arm64.go` (+ `.s` if present),
  `dot_other.go`, and the Q8 quant files use `//go:build` tags and `unsafe`.
  Move the *entire* set together and re-run `go test ./encoder/` on both arm64
  and amd64 (the golden-cosine tests guard correctness). A partial move silently
  drops the optimized path or breaks the build on one GOARCH.
- **`coderank` → `encoder` selector rename** touches use sites in
  `internal/search/neural_rerank.go`, `cmd/ken/main.go`, `cmd/ken-mcp/main.go`,
  and `bench/ndcg/coir_test.go`. Pure rename, but not a path-only `sed` — handle
  in the same pass.
- **`go mod tidy` direct/indirect churn.** After Stage C, `x/text` and
  `gotreesitter` likely drop from ken's *direct* requires (now reached via
  aikit). Don't hand-edit; let `tidy` resolve and review the diff.
- **`go.work` and CI.** CI must test ken with `GOWORK=off` against the tagged
  aikit (the real consumer path), not only inside the workspace, or a broken
  release dependency hides until users hit it.
- **Blank-import registration.** The chunker registry uses
  `_ "…/chunk/regex"` side-effect imports in `cmd/*`, `mcp/*`, tests, and
  `demos/postgres`. Every one of those paths must be rewritten to
  `_ "…/aikit/chunk/regex"` or the default chunker silently fails to register.

---

## 9. Import-rewrite worklist (every consumer to repoint)

Non-test consumers (must compile after Stage C):

- **chunk:** `internal/sql/{emit,fold}.go`; `internal/db/{db,emit,sqlite,mysql,refresh,setup}.go`;
  `internal/search/{watch,penalties,hybrid,rerank,reranker,neural_rerank,index,index_serialize}.go`;
  `mcp/{server,run}.go`, `mcp/db/setup.go`; `cmd/ken/{main,perf}.go`,
  `cmd/ken-mcp/main.go`, `cmd/ken-mcp-docs/main.go`; `demos/postgres/main.go`.
- **ann:** `internal/search/{hybrid,index}.go`.
- **bm25:** `internal/search/{hybrid,index}.go`; `bench/tokens/grep_baseline.go`.
- **embed:** `internal/search/{watch,index,index_serialize}.go`; `mcp/run.go`;
  `cmd/ken/build_index.go`; `cmd/ken-mcp/main.go`; (and `aikit/encoder` itself).
- **coderank→encoder:** `internal/search/neural_rerank.go`; `cmd/ken/main.go`;
  `cmd/ken-mcp/main.go`.

Test files to repoint (same paths): the `internal/search/*_test.go` suite,
`mcp/server_test.go`, `mcp/db/setup_test.go`, `internal/db/*_test.go`,
`internal/sql/sql_test.go`, `chunk/**/*_test.go` (move with their packages),
`bench/ndcg/coir_test.go`, `cmd/ken-mcp/loadorbuild_test.go`,
`internal/search/build_parity_test.go`, `internal/search/index_serialize_test.go`.

(Grep `townsendmerino/ken/(internal/(topk|ann|bm25|embed|coderank)|chunk)`
before and after to confirm zero stragglers.)

---

## 10. `aikit` public stability contract (state in its README)

Mirror ADR-032's tiering — declare what's 1.0-stable vs best-effort so consumers
know what can shift:

- **Stable:** `topk.New/Selector[T]`; `ann.New/Flat.Query/Hit`;
  `bm25.Build/Index/Result/Tokenize`; `embed.Load/LoadFromFS/StaticModel`,
  `embed.LoadTokenizer/Tokenizer`, `embed.OpenSafetensors*`;
  `encoder.Load/LoadFromFS/Model`, `encoder.Encoder` interface;
  `chunk.Chunker` interface + `chunk.{Chunk,Register,Get,Names,ChunkFile,Language}`.
- **Best-effort:** the concrete chunkers (esp. `treesitter`, gotreesitter-backed);
  Q8 paths (`encoder.LoadQ8/ModelQ8`); the safetensors mmap variant.

**Carry-over caveats for the README** (these assumptions travel with the code):

- `bm25`'s tokenizer and `encoder`'s model are **code-tuned** (identifier
  splitting; code corpus). A feature for code/RAG consumers, a hidden
  assumption for general NLP — say so.
- `ann` assumes **L2-normalized** input vectors; the normalization contract
  lives at the `embed` boundary, not in `ann`.
- `embed`'s float64-accumulation and `mapping[]`-indexing invariants
  (DESIGN.md §4) are correctness-critical — keep them in the package doc.

---

## 11. Docs, licensing, tooling to update

- **ADR-034** in [`docs/internal/DECISIONS.md`](DECISIONS.md): record the split — decision,
  alternatives (in-repo `kit/` vs separate module vs status quo), the `chunk`
  public-API break and its mitigation, the `coderank`→`encoder` rename.
- **CLAUDE.md** (ken): update "Architecture" (packages now external), the
  golden-fixture workflow pointer (now in aikit), and the dep list.
- **DESIGN.md / BENCH.md:** repoint package references to `aikit/...`.
- **CHANGELOG.md:** ken entry for the dependency extraction + any public break.
- **LICENSE / NOTICE / THIRD_PARTY_LICENSES.md:** copy into `aikit`; the
  Model2Vec/semble attribution chain must travel with `embed`/`bm25`/`encoder`.
- **`.goreleaser.yml`, `.golangci.yml`, `.github/`:** ken's release/lint configs
  are unaffected in shape but CI must add the `GOWORK=off` build (§8); `aikit`
  gets its own minimal CI (build + `go test ./...`, parity tests skipped).
- **Delete the old `kit/` curation framing** — this doc replaces it.

---

## 12. Verification & rollback

**Definition of done (both modules):** `gofmt -l` empty, `go vet ./...` clean,
`go test ./...` green; ken builds with `GOWORK=off` against tagged `aikit`; all
three ken binaries run a smoke index+search; the NDCG bench (`bench/ndcg`) and
embed/encoder golden tests reproduce prior numbers (no ranking drift — the move
is purely structural). A subagent diff-review of the import rewrite confirms no
behavioral edits sneaked in.

**Rollback:** until Stage D's tag, everything is behind `go.work`; reverting is
`git checkout` of ken + deleting the workspace file. After tagging, pin ken back
to the pre-split commit if needed. Because the change is mechanical (paths +
one rename + one shim), a clean revert is always available.

---

## Sequencing checklist

- [ ] Lock the §3 decisions (module name, rename, chunk break handling, versioning, testdata).
- [ ] Stage A: init `aikit`, write `go.work`.
- [ ] Stage B: move packages leaf-first (topk → embed → ann/bm25 → encoder → chunk); `aikit` tests green.
- [ ] Migrate testdata + golden scripts (§6); confirm skip-if-absent still holds.
- [ ] Stage C: rewrite ken imports (§9), delete vacated trees, `go mod tidy`, ken tests green under workspace.
- [ ] Add `chunk` re-export shim if external consumers exist (§7).
- [ ] Stage D: stabilize surface (§10), tag `aikit v0.1.0`, repoint ken's require, verify `GOWORK=off`.
- [ ] Docs/licensing/CI updates (§11); write ADR-034.
- [ ] Verification pass + subagent diff review (§12).
