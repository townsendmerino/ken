# ken — Users guide

Hybrid (BM25 + Model2Vec) code search over your repo, served as
an MCP server so your coding agent can call it the same way it
calls grep — but it returns ~40× fewer tokens per result, and it
handles "what's the auth flow in this codebase?" cleanly where
grep can't.

This guide is for **agent users**: install ken-mcp, point your
agent at it, and use the tools. If you're building on top of ken
(embedding a corpus, writing a custom chunker), see
[DEVELOPERS.md](DEVELOPERS.md).

## 5-minute install

Pick the agent you use. All five paths get you the same `ken`
server with all nine tools registered.

### Claude Code

```bash
claude mcp add ken -s user -- /absolute/path/to/ken-mcp
```

That's it. Reload Claude Code; `ken-mcp` should show up in the
MCP servers list. The first time an agent asks for a search, ken
will index the repo and cache it for the session.

### Cursor

`~/.cursor/mcp.json` (global) or `.cursor/mcp.json` (per-project):

```json
{ "mcpServers": { "ken": { "command": "/absolute/path/to/ken-mcp" } } }
```

### Codex

`~/.codex/config.toml`:

```toml
[mcp_servers.ken]
command = "/absolute/path/to/ken-mcp"
```

### OpenCode

`~/.opencode/config.json`:

```json
{ "mcp": { "ken": { "type": "local", "command": ["/absolute/path/to/ken-mcp"] } } }
```

### VS Code (with MCP)

`.vscode/mcp.json`:

```json
{ "servers": { "ken": { "command": "/absolute/path/to/ken-mcp" } } }
```

### Get the binary

Options:

1. **Package manager** (installs both `ken` and `ken-mcp`):
   - macOS / Linux: `brew install --cask townsendmerino/tap/ken`
   - Windows: `scoop bucket add townsendmerino https://github.com/townsendmerino/scoop-bucket` then `scoop install ken`
2. **Pre-built release**: download from
   [github.com/townsendmerino/ken/releases](https://github.com/townsendmerino/ken/releases)
   — macOS / Linux (`.tar.gz`) and Windows (`.zip`), amd64/arm64.
3. **Go**: `go install github.com/townsendmerino/ken/cmd/ken-mcp@latest`
   (and `.../cmd/ken@latest`).
4. **Demo binaries** (Go-stdlib / kubernetes / postgres, pre-indexed): see
   [releases/demos/v0.2.0](https://github.com/townsendmerino/ken/releases/tag/demos/v0.2.0).

### Get the embedding model

Most ken features need the Model2Vec embedding model
(`potion-code-16M`, ~60 MB). **ken-mcp fetches it automatically in
the background on first run** when it's missing — serving BM25-only
until it lands (a few seconds, one-time), then upgrading to hybrid.
Set `KEN_MCP_AUTO_FETCH=0` to disable that. The CLI (`ken search`)
does not auto-fetch; run `ken download-model` once to pre-seed it
(or pass `--mode bm25`):

```bash
ken download-model
```

This puts it under `~/.ken/model/`. ken-mcp finds it from there
by default; override with `KEN_MCP_MODEL_DIR=/path/to/model`.

If you want the optional neural reranker (better quality for
ambiguous queries, ~547 MB): `ken download-model --rerank`,
then enable with `KEN_MCP_RERANK=on`.

## First query

Once installed, just ask your agent something like:

> Where do we handle user authentication in this codebase?

A well-prompted agent should call ken's `search` tool with
`query: "user authentication"`, get a list of ranked code chunks
back, and answer from those. You can also be explicit:

> Use ken to find the authentication code, then read the top result.

The response shape is markdown by default — same format
[MinishLab/semble](https://github.com/MinishLab/semble) emits, so
agents trained against semble work unchanged.

## When to use ken vs grep

Use **ken** for:

- **Conceptual queries**: "where do we handle auth?" / "find the
  retry logic for HTTP requests" / "what's the database
  connection setup?"
- **Locating definitions and references**: "where is `Login`
  defined?" / "which files call `verify_token`?" — the
  `definition`, `references`, and `callers` tools resolve these
  more cleanly than text-grep can.
- **Exploring an unfamiliar codebase**: `outline` and `symbols`
  give a structural overview without you reading every file.
- **When the agent will read the result anyway** — ken's snippets
  cost ~40× fewer tokens than `grep + Read` at near-parity recall
  (~0.97 vs ~0.999 in default hybrid mode).

Use **grep** (or `rg`, `ag`, etc.) for:

- **Exhaustive enumeration**: refactors, pre-rename audits, "find
  EVERY place that uses this string" — grep guarantees 100%
  recall on literal matches; ken optimizes for relevance and
  reaches ~97% recall at top-10 in its default hybrid mode (0.970
  NL / 0.995 symbol on semble's benchmark). Without the embedding
  model installed it falls back to BM25-only at ~82–91% — run
  `ken download-model` once to stay on the default path. grep
  remains the right tool when you need every match. See
  [`docs/BENCH.md`](BENCH.md#default-mode-hybrid-recall--the-number-that-matters)
  "Default-mode (hybrid) recall."
- **Literal-string searches**: SQL fragments, error message text,
  config keys you know verbatim.
- **Speed-critical scripted searches**: `rg foo` returns in
  ~50 ms; ken's first query against a fresh repo includes index
  build time (1-3 s for medium / large repos — see
  [DEVELOPERS.md → Performance expectations](DEVELOPERS.md#performance-expectations)).

Rule of thumb: if the question is "find chunks that answer this
question," ken wins. If it's "find all instances of X," grep
wins.

## Tuning for your repo shape

Defaults — hybrid mode, regex chunker, snapshots on — are right for
most repos. Reach for the knobs below only when your repo has a shape
that stresses **cold start** (first query on a fresh index) or
**rebuilds** (re-indexing as you edit). All are `KEN_MCP_*` env vars
([full list](#configuration)); ken-mcp always watches, so live edits
re-index automatically.

| Your repo… | Reach for | Why |
|---|---|---|
| **Small / medium, stable** (≲ tens of thousands of files) | nothing — the defaults | Cold start is already 1–3 s, so `STAGED` / `LAZY_ENRICH` add moving parts for no gain. Just run `ken download-model` once to stay on the ~97% hybrid path instead of the ~82–91% bm25 fallback. |
| **Large** (100 K+ files — big monorepo, kernel-scale) | `KEN_MCP_STAGED=1` | Serves BM25 lexical **instantly** (~4× faster first query), then upgrades to hybrid in the background. Snapshots (on by default) make every *restart* after that instant. Trim vendored/generated trees with `.kenignore` to shrink the index. |
| **High-churn** (you edit it throughout the session) | `KEN_MCP_EMBED_CACHE=1` | Persistent `sha256(chunk)→vector` cache: a rebuild re-embeds only the chunks whose text actually changed — the rest are hits. Biggest win when you re-index repeatedly. |
| **Large *and* high-churn** (the hard case) | `KEN_MCP_STAGED=1` + `KEN_MCP_EMBED_CACHE=1` | Staged gets a usable first query out of the big cold build; the embed cache keeps every rebuild after it cheap as you edit. |
| **Structure-heavy exploration** (lots of `definition` / `outline` / `callers`) | `KEN_MCP_CHUNKER=treesitter` | AST-aligned chunk boundaries, so a retrieved span is one whole symbol rather than a line-window straddling two. (The `# func:` enrichment labels apply on any chunker.) |
| **No model / air-gapped** | nothing — bm25 is automatic | With no embedding model ken falls back to BM25-only (~82–91% recall). Fine for keyword-ish lookups; weaker on conceptual "where do we handle X." |

Starting points, not a routing algorithm — and `ken doctor` already
flags the health half (model missing, cache cold) plus, on a large
corpus, the `STAGED` suggestion above.

## The nine MCP tools

Quick reference. Pass `output: "json"` to any of these for a
structured response instead of markdown (covered tool-by-tool in
[DEVELOPERS.md → JSON output mode](DEVELOPERS.md#json-output-mode)).

### `search`
Hybrid code search. **The main tool.**

- Args: `query` (required), `repo`, `mode` (`bm25`/`semantic`/`hybrid`),
  `top_k`, `languages`, `path_contains`, `exclude_path_contains`,
  `output`.
- Returns: ranked snippets with file, line range, score, text.
- Example: `search(query: "rate limit middleware",
  languages: ["go"], path_contains: "internal")`.

### `find_related`
Semantic-similarity to a specific location. Use after a `search`
result to find more code that looks like it.

- Args: `file_path`, `line`, `repo`, `top_k`, `output`.
- Returns: ranked snippets similar to the chunk at the anchor
  location.

### `definition`
Find where a symbol is defined. Tree-sitter-grade, name-resolved
(NOT type-resolved — same-spelled names in different files all
return).

- Args: `symbol` (e.g. `"Login"` or `"User.Login"`; for Go also
  `"pkg.Func"` or `"pkg.Type.Method"`), `repo`, `output`.
- Returns: file, line span (`line`/`end_line`; for Go also `doc_line`,
  where the doc comment starts) and kind (`function` / `class` /
  `method`) for each site.

### `references`
Find every place a name is used: call sites, imports, raises.

- Args: `symbol`, `repo`, `output`.
- Returns: per-file list of reference kinds, with the call lines.

### `callers`
Find the calls to a given function: one entry per file (100%
precision on the Stage 8 Gate 2 sample), each call with its line and
the function or method it sits in.

- Args: `symbol` (for Go, `"pkg.Func"` narrows to calls written
  `pkg.Func(…)` plus bare calls inside package `pkg`), `repo`, `output`.
- Returns: file paths (`files`) and per-file call sites (`sites`).

All structural tools follow edits: the structural index is updated with
each file-watcher flush, so line spans stay current while you edit.
Files over `KEN_STRUCTURAL_MAX_FILE_BYTES` (1 MiB) aren't parsed.

### `outline`
Structural outline of a file or directory: top-level functions,
classes, methods, with parameters and line spans.

- Args: `path` (file or directory), `repo`, `output`.
- Returns: structured outline; for directories, one section per
  indexed file.

### `symbols`
List every top-level symbol (function or class) in the repo.

- Args: `path` (optional prefix filter), `repo`, `output`.
- Returns: flat list of names.

### `status`
Report ken's current state: build identity, model availability,
enrichment state, **token savings summary**.

- Args: `repo` (optional, for live index info), `verbose`,
  `output`.
- Returns: markdown overview by default. Pass `repo` for the
  live index file/chunk counts on that repo.

### `recently_changed`
Last N commits with the files each touched. Git-aware (uses
go-git on the local working tree).

- Args: `n` (default 10, max 100), `repo` (local path), `path`
  prefix filter, `output`.
- Returns: per-commit markdown with hash, author, time, message,
  and changed file list.

## Go: type-resolved answers across build configurations

`references` and `callers` match by name. For Go, five more tools answer from a type checker, in ken's own process: `go_references`, `go_implementations`, `go_uses`, `go_rename_plan` and `go_check_edit` (`ken go refs|impls|uses|rename-plan|check-edit` on the command line). They need the `go` command on PATH and nothing else (no gopls), and they work on local checkouts. `go_references` and `go_implementations` return the lines gopls would (that is tested, declaration by declaration, against gopls itself) and one kind gopls leaves out: uses of a generic type's field through an instantiation in another package.

A type checker sees one build configuration at a time, so code behind a build tag or another GOOS is invisible from the default one. List the repository's configurations once, in `.ken-go.json` at its root:

```json
{
  "configs": [
    {"name": "default"},
    {"name": "linux-cuda", "env": {"GOOS": "linux", "GOARCH": "amd64", "CGO_ENABLED": "0", "GOFLAGS": "-tags=cuda,testhooks"}}
  ]
}
```

Each query then runs under every configuration that compiles the symbol's declaration and merges the results:

```
$ ken go refs . decoder.ResidentForward.Forward
references of decoder.ResidentForward.Forward: 497 lines in 255 files
declared: decoder/residency.go:43  interface-method decoder.ResidentForward.Forward
per configuration: default 68 (2.2s) · darwin-gpu 276 (1.1s) · linux-cuda 249 (1.0s)
backend/cuda/a13_multimodel_test.go
  51 TestA13_MultiModelUnloadPoisons [linux-cuda]: before, err := rb.Forward(emb, 0)
```

- **Cost.** Each query lists and type-checks the repository once per configuration, one after another: a second or two and under 1 GB at its peak for each, on a 3,000-file repository (about 5 s for the three configurations above). The `ken go` command line asks once and keeps nothing. A long-lived host (`ken-mcp`, or a program using `engine.Workspace`) keeps the type-checked programs loaded after the first query, one per configuration, about 0.2 GB each, until they have been idle for `KEN_MCP_GO_IDLE` (default 5m): any question on an unchanged tree then takes 0.1 to 0.2 s, and after an edit only the packages the edit can affect are checked again (a second or two). `KEN_MCP_GO_RESIDENT=0` (`GoTypedOptions.MaxResident: -1` in a Go program) turns that off. `ken-mcp` also remembers answers (`KEN_MCP_GO_CACHE_MB`, default 32, 0 turns it off): an identical question on an unchanged tree takes 0.04 s. Everything kept is bounded and listed by `status`. Both check the files on disk before every answer, across the whole Go workspace, so an edit is always seen. In a Go program, `engine.GoTypedOptions` does the same. `--engine lsp` (`KEN_MCP_GO_ENGINE=lsp`) asks gopls itself instead.
- **Symbols** are `Name`, `Type.Member`, `pkg.Name`, `pkg.Type.Member`, or `dir/sub.Name` to pick a package by directory. A method or field needs its type.
- **`[linux-cuda]`** after a line means only that configuration saw it. No label means every configuration did.
- **`(via pkg.Type.Method)`** after a line of `go_references` means the line does not name the symbol you asked about but a method that corresponds to it through an interface: asking about a concrete method, a call made through an interface it satisfies; asking about an interface method, a call on a type that implements it. The header gives both counts (`177 direct, 320 through a corresponding method`), `--group-by` rows carry the direct count, and `direct_only` (`--direct`) leaves the others out. gopls counts all of them as references and does not say which is which; `--engine lsp` answers are not marked.
- **An `INCOMPLETE` line means a lower bound**: a declaration that no listed configuration compiles, or a configuration that failed to load. Add the missing configuration and ask again.
- **`group_by` gives counts, not lines.** `go_references` and `go_implementations` take `group_by: "package" | "dir" | "file"` (`--group-by` on the command line). The header stays; the lines become one row per group, largest first, each split into non-test and test lines (a test file is one ending `_test.go`). `max` caps the rows. `package` counts an external test package (`package x_test`) with the package it tests and adds the package name where it differs from the directory's (`cmd/ken (main)`); `dir` is the directory alone. Ask this way for a coupling question: the reply is a few rows where the full list can be thousands of lines.

  ```
  $ ken go refs . decoder.Options --group-by package
  references of decoder.Options: 2525 lines in 979 files
  declared: decoder/options.go:14  type decoder.Options
  per configuration: default 1089 (2.2s) · darwin-gpu 1902 (1.1s) · linux-cuda 1739 (1.0s)
  decoder                 51 non-test  916 test
  backend/cuda             0 non-test  622 test
  backend/metal            0 non-test  546 test
  internal/app/serveapp    3 non-test   36 test
  ```
- **`go_uses` is the reverse question**: which identifiers of one package another package names (`importer`, `imported`; `ken go uses <repo> <importer> <imported>`). It sizes a package boundary. Each package is a directory (`backend/cuda`), the tail of one, or a package name; the imported one may also be an import path, and need not be in the repository. Rows are package-level names and, as `Type.Member`, the methods and fields named, each with its non-test and test line counts and the same configuration label. It is type-resolved: an aliased or dot import counts, and a local variable that shares the package's name does not. A count is of lines, as in `go_references`, so the two agree on a symbol.

  ```
  $ ken go uses . backend/cuda decoder
  uses of decoder by backend/cuda: 650 identifiers, 7103 lines in 283 files
  import paths: github.com/townsendmerino/goinfer/decoder <- github.com/townsendmerino/goinfer/backend/cuda
  package-level: 189 (90 named by non-test code) · methods and fields: 461 (244 named by non-test code)
  per configuration (lines): default 0 (0.1s) · darwin-gpu 0 (0.1s) · linux-cuda 7103 (1.2s)
  Options      type     0 non-test  622 test  [linux-cuda]
  Model.Close  method   0 non-test  544 test  [linux-cuda]
  ```

  It costs a second or two per configuration: `go list` names the files each configuration builds (through `wrap`, like gopls), and ken type-checks them from source in its own process, which `wrap` does not cover. A configuration that does not build the importer is skipped; one whose files have type errors is counted and marked `partial`, with an `INCOMPLETE` line.
- **`go_rename_plan` writes nothing, and checks what it proposes.** It lists the files a rename edits under each configuration and the lines that still spell the old name in comments, strings, markdown and assembly; with `patch: true` (`--patch FILE` on the command line) it gives one merged diff for `git apply`. The edits follow gopls's rename: every identifier, the name in the declaration's doc comment, doc links, a type together with the embedded fields named after it, and an interface method together with the methods of the types used as that interface (a concrete method tied to an interface is refused: rename the interface's).

  Before it answers, the merged patch is applied in memory and every configuration is type-checked again. The plan says `checked` when nothing changed but the name: no new type error, every identifier still refers to the declaration it referred to (nothing shadowed or captured), every type has the fields and methods it had (nothing hidden, merged or made ambiguous), and every type assertion that could succeed still can. Otherwise it lists each difference as a `CONFLICT` line and gives **no patch**. A plan with an `INCOMPLETE` line (a declaration no listed configuration compiles) is a lower bound and is not called checked. A `PARTIAL` line means a configuration has type errors of its own: identifiers inside code that does not type-check are not renamed.

  What the check cannot see: reflection, struct tags, names inside strings, and a type that stops (or starts) satisfying an interface it is only ever converted to at run time, such as `fmt.Stringer`.
- **`go_check_edit` checks an edit before it is made.** Give it the edit as you would make it, `{file, old_string, new_string}` or `{file, content}` for a whole (or new) file, several at once if they belong together. It writes nothing: the edit is applied in memory and every package it reaches is type-checked again, under every configuration. On the command line: `ken go check-edit <repo> <file> <new-contents-file>`, or `--edits FILE` with the same JSON list.

### TypeScript and JavaScript: `ts_check_edit`

`ts_check_edit` (`ken ts check-edit <repo> <file> <new-contents-file>`, or `--edits FILE`) answers the same question for TypeScript and JavaScript: what would this edit do to the build. It asks the project's own compiler, so it needs TypeScript 7.0 or later installed in the repository (`npm install` done; the native compiler is `node_modules/@typescript/typescript-<os>-<arch>/lib/tsc`, and `--tsc PATH` names another). ken runs no Node. On TypeScript 5 or 6 it says which version it found and stops.

```
$ ken ts check-edit . --edits edits.json
check edit: 1 files edited; 58 source files checked with TypeScript 7.0.2 in 0.24s
  src/core/tvm.ts
ERROR: src/core/lightcards.test.ts:9: TS2554: Expected 2 arguments, but got 1.
ERROR: src/react/TvmCard.tsx:23: TS2554: Expected 2 arguments, but got 1.
WOULD NOT COMPILE: 2 new errors
```

The edit is opened as unsaved contents in the compiler; nothing is written. Errors that are there already are not listed, and errors the edit removes are counted. It covers every `tsconfig` project of the repository in one run. What it does not do, and `go_check_edit` does: report a name on an untouched line that now refers to another declaration, or list changed exports. Each call starts the compiler and asks about every source file twice; that took 0.24 s on the 58-file project above and has not been measured on a large one. The MCP tool appears when the server's default repository has the compiler at its root, and on a server without `KEN_MCP_ALLOWED_REPO_ROOTS` it checks that repository only, because the check runs a program the repository supplies.

  The reply has three kinds of line. `ERROR` is a compile error that is not there now, in the edited files or in any package that depends on them, with the configurations it holds under when that is not all of them: change a function's parameters and every caller is listed, the one in `x_windows.go` included. Unused variables and imports count, as they do for the compiler. `MOVED` is an identifier on a line the edit does not touch that would refer to another declaration: a variable the edit shadows, a builtin a new function captures. That code compiles and means something else, so check each is intended. `API` is an exported declaration of an edited package that the edit removes or changes: what code outside the repository would see, which nothing here can check. Errors that are already there are not reported, and the reply says how many of them the edit fixes.

  With no such line the reply says `checked`. That is a statement about types, not behaviour: tests still have to run. An edit that leaves a file's imports alone costs what a query after an edit costs (a fraction of a second on a server that holds the repository); one that changes them, or adds a file, has the go command list the packages again and takes a second or more per configuration. An edited file that does not parse is reported as that and nothing is type-checked. Only `.go` files can be edited, a file cannot be deleted, and `go vet`'s checks are not run.

  It will also refuse some renames an editor makes: `main` in package main and `init` (the result compiles and is a different program); a field of a struct that the code converts to or from another struct with the same fields (the conversion would stop compiling: rename both, or neither); a method of a type whose own method hides an embedded type's method of the same name (the embedded one would take its place wherever an interface asks for that name).

  **Cost.** On a host that already holds the programs (`ken-mcp` after any typed query), a plan is read off them and checked by type-checking again only what the patch touches: on the 3,000-file repository above, 0.6 s for a type used in 980 files, 0.3 s for a function, about 1.6 s for a field or a method (those are checked with every function body of every package they reach, one configuration at a time, holding about 0.7 GB more while they run), and a method's first rename takes about 2 s longer while ken works out which methods interfaces tie together. From the command line each configuration is loaded first: 4 to 5 s for the three, 7 to 8 s for a method.
- **Stability**: these four tools are new and best-effort; they are not part of the 1.0-stable nine.
- **`wrap`**: `"wrap": ["cmd", "arg", "--"]` in the file prefixes every `go` (and, under the `lsp` and `cli` engines, gopls) run, and ken holds the same lock while it type-checks in its own process, for a repository that serializes heavy work behind a lock. `ken-mcp` honors `wrap` for its default repo, or when `KEN_MCP_ALLOWED_REPO_ROOTS` confines it.

## Configuration

The env vars you'll actually touch:

| Variable | Default | What it controls |
|---|---|---|
| `KEN_MCP_DEFAULT_REPO` | empty | Pre-indexed source. Tools work without `repo` arg when set. |
| `KEN_MCP_MODE` | `hybrid` | Retrieval mode: `bm25`/`semantic`/`hybrid`. Serves `bm25` while the model is missing — fetched on first run by default (`KEN_MCP_AUTO_FETCH`). |
| `KEN_MCP_MODEL_DIR` | `~/.ken/model` | Model2Vec snapshot dir. Empty ⇒ bm25-only. |
| `KEN_MCP_CHUNKER` | `regex` | Chunker: `regex`/`treesitter`/`line`/`markdown`. |
| `KEN_MCP_CACHE_SIZE` | `16` | Repo LRU bound. `0` disables caching. |
| `KEN_MCP_LOG_LEVEL` | `warn` | `debug`/`info`/`warn`/`error`. Logs go to stderr. |
| `KEN_ENRICH` | (enabled) | Arm B structural enrichment. Set to `off` to build indexes without it (every build path; a pre-built index keeps what it was built with, and `status` shows which). |
| `KEN_ANN` | (exact `flat`) | Dense vector store: `flat-i8` (about 80% less dense-store memory; use at large-repository scale) or `flat-binary-i8` (fastest at repo scale). The measured cost of either is about 0.001 NDCG@10 on the semble benchmark in hybrid mode (not distinguishable from zero), with recall@10 unchanged. `status` shows the store the loaded index is serving. |
| `KEN_MCP_RERANK` | `off` | Opt into the neural reranker (better quality, +~30 ms per query when warm). |

Most users set `KEN_MCP_DEFAULT_REPO` to their main project and
leave everything else alone. The neural reranker is worth turning
on for ambiguous queries against larger codebases; see
[DEVELOPERS.md → Tuning rerank](DEVELOPERS.md#tuning-rerank) for
the `KEN_MCP_RERANK_*` knobs.

## Troubleshooting

### "No model at ~/.ken/model — downgrading to bm25"

Run `ken download-model` to fetch it. ~60 MB; one-time. After it
lands, restart ken-mcp. This is the single biggest retrieval-quality
lever you control: the BM25-only fallback caps around 82–91%
recall@10, while the default hybrid mode (model present) reaches
~97% (0.970 NL / 0.995 symbol) — see
[`docs/BENCH.md`](BENCH.md#default-mode-hybrid-recall--the-number-that-matters).

### "ken returns no results / weird results"

Try the `status` tool first:

```
status
```

This reports: build identity, models loaded, enrichment state,
token-saving stats. If `embedding: missing` shows up, you don't
have the model.

If the model is fine, try:
- Looser query terms (concepts, not literal strings)
- `mode: "bm25"` for keyword-heavy queries
- Higher `top_k` (default 5; try 20)
- Drop filters: `languages`, `path_contains`, `exclude_path_contains`

### "ken returns stale results after I edited a file"

ken-mcp watches files via `fsnotify` and re-indexes ~2 s after
changes settle. If results lag for more than a couple of
seconds:

- `status(repo: "...", verbose: true)` reports the last build time
- Restart ken-mcp if the watcher is stuck (rare; report a bug)

### "ken-mcp won't start / agent can't reach it"

Check stderr — it'll print one line per stage. Common gotchas:

- **Binary path is relative**: MCP configs need ABSOLUTE paths.
  `~/bin/ken-mcp` doesn't expand; use `$HOME/bin/ken-mcp` (or the
  full literal path).
- **Stale model dir**: `KEN_MCP_MODEL_DIR` points somewhere that
  no longer has a `model.safetensors`. Either `ken
  download-model` or unset the variable.
- **Tools missing from `tools/list`**: you're probably looking at
  an old ken-mcp release; check `status` for the version, upgrade
  to the latest. The 9 tools listed above are all 1.0-stable.

### "Search is fast on second query but slow on first"

Expected. First query against a fresh repo includes index build
time:

- Tiny repo (≤10 files): ~150 ms
- Medium repo (~250 files): ~600 ms
- Large repo (~1000 files): ~1.3 s

After that, queries are sub-millisecond. The numbers depend on
mode + model availability; see
[DEVELOPERS.md → Performance expectations](DEVELOPERS.md#performance-expectations).

### "Where are my token savings?"

The `status` tool's "Token savings" section reports it. Daily /
7-day / all-time bucketed, with a `~tokens` estimate (chars ÷ 4).
Stored at `~/.ken/savings.jsonl` — no query text, just counts;
opt out with `KEN_NO_USAGE_STATS=1`. CLI variant:
`ken savings --verbose`.

## Where to next

- **You want to embed a corpus in your own binary** (think:
  shipping a single-file MCP server with your SDK's docs
  pre-indexed) → [DEVELOPERS.md → mcp.Run library](DEVELOPERS.md#mcprun-library).
- **You want to tune the rerank pipeline** for your workload →
  [DEVELOPERS.md → Tuning rerank](DEVELOPERS.md#tuning-rerank).
- **You want to understand the algorithm** (BM25 + semantic +
  RRF + boosts + penalties + rerank) → [DESIGN.md](DESIGN.md).
- **You want to see the benchmark methodology** → [BENCH.md](BENCH.md).
- **You found a bug** →
  [github.com/townsendmerino/ken/issues](https://github.com/townsendmerino/ken/issues).
