# Fixes from agent field reports: plan

2026-11-17. A plan, not a decision: nothing here is built or measured. It answers the `KEN:` lines that Claude Code
worker sessions wrote into their status files between 2026-11-12 and 2026-11-17, across goinfer and tack on
three machines: eight reports, about 100 ken calls in all. Code is read at `b3dabd8` (v1.19.2).

## What the reports say

The type-resolved Go tools did well. One refactor made 38 `go_references` calls over three build configurations,
and the compiler found no call site ken had missed. `definition` and `go_references` were right every time, at 0 to 7 s.
The misses are below, and each item says where it is in the code.

| id | report | cause in the code | size |
|---|---|---|---|
| F1 | `search` with `path_contains` returned "No results match the filters (search returned 40 candidates before filtering)" for a file that exists | `mcp.searchCore` over-fetches `min(topK*10, 200)` and filters afterwards in `applySearchFilters` | medium |
| F2 | `outline` and `references` said "no extractors registered for any file" on Go repos; one file failed while its sibling worked | `RepoBundle.StructuralIndex` drops `Workspace.Structural`'s error, and `RepoBundle.StructuralPending` returns false on the Workspace path, so "still building" and "build failed" both print the unsupported-corpus message | small |
| F3 | `symbols path=backend/webgpu/residency` found nothing | `structural.Index.SymbolsInPath` appends `/`, so a prefix that ends inside a file name never matches | small |
| F4 | `callers` gives one name-resolved list across backends that can't be separated | the output has no package or module grouping and no path filter | small |
| F5 | conceptual `search` ranked the right struct 5th, behind docs | no way to ask for code only | small, after F1 |
| F6 | ken has no "which files read this file as text" query (source-scan tests) | not built | medium |
| F7 | `callers(emitTurn)` was empty: a method passed as a value, not called | fixed in 1.19.2 ("Passed as a value"); the report was made on 1.19.1 | test only |
| F8 | a session on nobara had no `go_*` tools; ToolSearch found nothing | `go_*` are registered only when `typed.goTools` holds at server start; that session ran ken 1.16, and `status` doesn't say whether the typed tier is on or why not | small |
| F9 | the edit hook made about 75 reports on `.go` edits after one restart, "all transient or expected" | the report counts what is broken right after each edit, including breakage the next edit in the same change fixes | measure first |

## F1. The path filter goes into the search, so there is no limit

Today the filter runs after retrieval. When the requested scope holds less than about 1 in 40 of the top candidates,
the call returns nothing although matching chunks exist. Raising the 200 cap only moves the cliff.

ken can filter inside the search at no extra cost, because both arms already score every chunk. The dense arm is a flat
store (`flatStore`, `flatI8Store`, `flatBinaryI8Store`, each `Query` a full scan), and the BM25 arm accumulates over
postings. A chunk outside the scope can be skipped before it is scored, so the top-k comes from the scope alone, exactly,
whatever its size.

1. **A scope type in the engine.** `engine.SearchOptions` gains `Scope` (languages, `PathContains`,
   `ExcludePathContains`; later F5's kind). It is compiled once per query into a chunk bitset: decide each distinct
   file once, then set its chunks' bits. That is one pass over the chunk table, small next to the dense scan.
   An empty scope compiles to nil, and nil means "every chunk", so the unfiltered path is untouched.
2. **Both arms take the bitset.** `Query(q, k, allow)` on the three flat stores skips a disallowed row before the dot
   product, so a narrow scope makes the dense arm faster. `bm25.TopK(tokens, k, allow)` skips disallowed documents when
   it collects from the accumulator. Corpus statistics (IDF, average length) stay whole-corpus: the scope narrows what is
   returned, not what a term weighs, so a chunk scores the same with or without a filter.
3. **Fusion and rerank see only in-scope candidates.** `fuseArms`, `hybridSearch` and `rerankFused` get the filtered
   arm lists unchanged. `searchCore` in `internal/search` passes the bitset through. `collectResults` is unchanged.
   `SearchFilesMode` takes the same scope.
4. **The MCP layer stops over-fetching.** `mcp.searchCore` passes the scope down and deletes `fetchK`,
   `applySearchFilters` and the "N candidates before filtering" message. `SearchFilterMeta` reports how many files
   and chunks are in scope instead, and a zero result reads "no chunk in the N files in scope matches", which is then
   true. An empty scope (no file matches the filter) is answered before searching, and names the filter.
5. **The CLI gets it too.** `ken search --path` and `--exclude-path` use the same scope.

**Gates** (tier 0, all on the existing fixtures; no retrieval-quality gate is needed because unfiltered results are
unchanged):
- Unfiltered results are byte-identical before and after, over the recall test set in every mode.
- For `bm25` and `semantic`, a filtered top-k equals the unbounded ranking (k = all chunks) with the filter applied
  afterwards, exactly. A test builds a corpus where the in-scope file ranks below position 200 unfiltered, and goes red
  on today's code first.
- For `hybrid` and `hybrid_rerank`, every result is in scope and the count is `min(k, in-scope chunks)`. Fusion is
  rank-based over the candidate lists, so the order is defined over the scope; that is the intended meaning.
- A narrow-scope query is not slower than the same query unfiltered (`bench`, median of 10).

The F5 kind filter and F4's path filter on `callers` reuse the scope type.

## F2. A building or failed structural index says so

`Workspace.Structural` already returns `ErrStructuralBuilding` or `ErrNoStructuralIndex`. `RepoBundle.StructuralIndex`
throws that error away on the Workspace path (`idx, _ :=`), and `RepoBundle.StructuralPending` checks only the older
builder fields. So on a Go repo, a call that arrives while the index is still building reports "no extractors
registered for any file", and so does a call made just after a file change. A change publishes a new index, and the
next call waits for it. That explains one file failing while its sibling worked, minutes apart in the same session.

- `RepoBundle.StructuralIndex` returns the error, and `structuralUnavailableResult` maps it to three messages:
  - building: retry in a few seconds, with how long it has been building;
  - failed: the build error and when it will retry;
  - unsupported: counts the files by language, and only then says no extractor applies.
- When a rebuild after a file change is in progress, serve the last good index rather than nil. It is immutable, so this
  is safe. Say in the answer that the index is from before the latest change.
- Red first: a Workspace-backed bundle with a build held open must report "building", not "no extractors". On
  today's code it does the latter.
- If a single file fails to parse, `outline` of that file says so and names the error, instead of a repo-wide
  message.

## F3. `symbols` and `outline` take any path prefix

`SymbolsInPath` turns the argument into a directory by appending `/`. Accept an exact file, a directory, or a plain
string prefix, so `backend/webgpu/residency` lists `residency.go` and `residency_build.go`. When nothing matches, say
what was matched and name the nearest directory that exists. `outline` keeps its file-or-directory contract and gets
the same zero-match message.

## F4. `callers` groups by package and takes a path filter

- **Grouping:** callers in Go are grouped by package import path, and by module when the repo's `go.work` lists several.
  goinfer's `backend/cuda`, `backend/metal` and `backend/webgpu` then come back as separate groups. Other languages
  group by directory.
- **Filter:** add `path_contains` and `exclude_path_contains`, using the F1 scope type. `callers` already enumerates
  everything, so this is a filter, not a limit.
- **Pointer:** the description says that `go_references` gives the type-resolved answer when a receiver type matters.

## F5. Search can ask for code only

`kind: code | docs | all` on `search`, default `all`, through the F1 scope. A file's kind comes from its extension and
the chunker's language. The default ranking is not changed. Weighting code above docs by default would change retrieval
results, and needs the pre-registered gate `CLAUDE.md` requires; that is a separate decision, and is not in this plan.

## F6. Which files read this file as text

A new tool, `text_refs(path)`. In Go, it lists string literals and `//go:embed` patterns that contain the file's path or
base name, with the file, the line and the enclosing function. Those come from the AST, so comments and identifiers
don't match. In other languages, it lists the string literals the chunker already sees. The description says it is a
lead list and points at grep for an exhaustive answer. Its first user is goinfer's source-scan tests, which read
`residency.go` as text.

## F7. Methods passed as values: lock in the 1.19.2 fix

Add the reported case as a fixture: a method value passed as a struct field (`Emit: s.emitTurn`) with no call site.
`callers` must list it under "Passed as a value". No code change unless the fixture is red.

## F8. `status` says whether the go_* tools are on

`status` gains a typed-tier line: on (go version, and gopls when found), or off and why (no `go` on the server's
PATH, a local checkout is needed, the version is too old). The `go_*` tools' absence is then diagnosable from one call.
Separately, nobara's ken is moved to 1.19.2 (not a code change).

## F9. The edit hook: measure before changing

Collect the hook's reports from one ordinary working session (the about 75 above, or a fresh day), and sort each one into:
- fixed by a later edit in the same change;
- real and acted on;
- real and ignored;
- wrong.

Only if the first group dominates, change the hook. Option (a) reports only breakage that survives to the end of the
turn. Option (b) reports only breakage that survives to the next edit of the same package. A report that was acted on
must still arrive. The numbers decide which option, if any; this plan doesn't.

## Order

1. F2 and F3: small, and they turn wrong answers into right ones.
2. F1: the largest; F4's filter and F5 depend on its scope type.
3. F7 and F8: a test and a status line.
4. F4, then F5.
5. F6, then F9's measurement.

Each item is one PR. F1 may take two: the engine scope with its gates, then the MCP and CLI change.
