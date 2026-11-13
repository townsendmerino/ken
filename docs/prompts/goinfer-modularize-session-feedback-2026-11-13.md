# Brief: what got in the way using ken-mcp for an architecture survey of goinfer (2026-11-13)

**Source.** One Claude Code session on the Mac, ken-mcp at a7e168614613 (the Go typed tier, ADR-059), against the local
goinfer checkout at df4d023fc. The job was an architecture survey: check five modularization suggestions against the tree
and find the real seams. About 25 ken calls. The result is goinfer's `docs/tasks/task-modularize-2026-11.md`.

**What this brief is.** Eight observations, each with the call that showed it, what happened, and a proposed change. It
is input for a ken task, not a plan: nothing here was reproduced a second time, and no ken code was read beyond
`docs/USERS.md`. Ordered by how much each one cost the session.

**What worked, so the fixes do not break it.**
- A batched `search` (three to five queries, `max_tokens` set) found the decisive prior art in the first call.
- `go_references` and `go_implementations`, once `.ken-go.json` existed, gave per-configuration counts that a
  single-configuration query gets badly wrong: `decoder.Options` has 1,088 references at the default configuration and
  2,523 across three; each real backend implements `decoder.ResidencyBackend` only under its own configuration, and the
  result labels say which.
- `outline` on one file in markdown is compact: 60 lines for a 2,308-line file, enough to see a 657-line method.
- An honest "no declaration found" for `decoder.WeightMat`: the type is aikit's `linalg.WeightMat`, and the caller was
  wrong.

## 1 · No aggregate mode: the caller wants counts by package, and gets every line

- **Call.** `go_references decoder.Model`, then `decoder.Options`, wanting "how many references per package, test and
  non-test".
- **What happened.** The only shape is one entry per reference. Markdown with a small `max` gives the header totals and
  no breakdown. JSON with a large `max` gives everything: 398,080 characters for `decoder.Model` (1,399 hits) and
  732,078 for `decoder.Options` (2,523 hits). Those spilled to files and were summed with jq. A third JSON call,
  `decoder.LayerWeights` with 92 hits, came back inline at roughly 20,000 tokens.
- **Why it matters.** For a coupling question the lines are noise and the counts are the answer. An agent that asks in
  JSON to get structure pays about ten times the markdown size.
- **Proposed.** A `group_by` argument on `go_references` (and `references`, `callers`): `package`, `dir` or `file`,
  returning counts only, split test and non-test, with the per-configuration header kept. Markdown default. Example:

  ```
  references of decoder.Options: 2523 lines in 977 files
  per configuration: default 1088 · darwin-gpu 1901 · linux-cuda 1737
  decoder            51 non-test   915 test
  backend/cuda        0 non-test   622 test
  ```

## 2 · JSON output is far larger than markdown, and nothing says so

- **Call.** `outline backend/cuda/resident.go` with `output: "json"`.
- **What happened.** About 1,400 lines of JSON inline for one file whose markdown outline is about 170 lines. Each entry
  repeats the file path and spells every field on its own line.
- **Proposed.** Compact the JSON (no indentation, file path once per group; the directory form already has `by_file`), and
  say in each tool's `output` description that JSON is for piping to a file, with the rough size ratio.

## 3 · No reverse query: which symbols of package X does package Y use

- **Wanted.** "The distinct `decoder` identifiers that non-test code in `backend/cuda` names, with counts", for each of
  twelve importing packages. This is the number that sizes a package seam.
- **What happened.** No tool answers it. The session used a regular expression over the importing files
  (`\bdecoder\.[A-Z]\w*`), which is name-grade and misses aliases and dot imports.
- **Proposed.** `go_uses` (or `go_surface`): given an importing package and an imported package, the imported
  identifiers it names, with counts, merged over the configurations. gopls already has the facts.

## 4 · No size ranking: longest functions and widest types

- **Wanted.** The longest non-test functions in the repository. This turned out to be the main finding of the survey
  (a 1,988-line function in a file the suggestions under review never named).
- **What happened.** Derived by dumping `outline` in JSON for four directories (1.1 to 1.7 MB each, all spilled to files)
  and ranking `end_line - start_line` with jq. A brace-matching script was written to cross-check.
- **Proposed.** A `largest` tool, or a `sort: "size"` and `top` on `symbols`: kind (function or type), path prefix, an
  exclude for test files, top N by line span. The structural index already holds the spans.

## 5 · The first structural call said the repository has no structural index

- **Call.** `definition WeightMat`, about a minute after the server's first `search` on the repository.
- **What happened.** "No structural index available for this repo. The structural index has no extractors registered for
  any file in this corpus (a repo with no supported source files has no structural index)." `status` for the same
  repository reported 17,906 top-level symbols, and `outline` worked on the next call.
- **Reading.** The index was probably still building. Not confirmed.
- **Proposed.** While the structural index is building, say that and either wait or give a retry hint. Keep the
  "no extractors" message for a repository that really has none.

## 6 · `go_rename_plan` failed under two of three configurations, and the reason is cut off

- **Call.** `go_rename_plan decoder.ResidencyBackend.BuildResident -> BuildResidentFor`.
- **What happened.** `default 18 (2.3s) · darwin-gpu FAILED (gopls: renaming "BuildResident" to "BuildResidentFor" not
  possible because "backendtag_guard.go" in "github.com/townsend…) · linux-cuda FAILED (… because "attn_segments.go" in
  "github.com/townsendmer…)`. The INCOMPLETE lines repeat the same truncated text. The reply was honest that it was a
  lower bound, which is the important part.
- **Why it matters.** The cut is exactly where gopls gives its reason, so the caller cannot tell a real conflict from a
  build-constraint problem, and the plan covers one configuration of three.
- **Proposed.** Print gopls's full error on the INCOMPLETE line (one line each is fine). Then look at why: the two named
  files are in other packages than the declaration, so this may be gopls refusing a rename that crosses a package it
  cannot load under that configuration, which is the case the tool exists for.

## 7 · Cost on a wide symbol

- **Calls.** `go_references decoder.Options`: default 7.5 s, darwin-gpu 131.8 s, linux-cuda 111.1 s. `decoder.Model`:
  5.9 s, 36.2 s, 36.6 s. Narrow symbols were 2 to 8 s per configuration.
- **Proposed.** Run the configurations concurrently when the repository's `wrap` allows it (goinfer's wrap takes a
  shared lock, so it does); keep one gopls alive per configuration across calls in a server session instead of a
  process per query; and show elapsed time per configuration as it finishes, since a four-minute silent call reads as a
  hang.

## 8 · Smaller ones

- **Truncation is easy to miss.** `outline backend` returned 500 of 1,240 files and did set `"truncated": true` and
  `"total_files": 1240`. The session missed it because the JSON spilled to a file, and found out only because the
  cross-check listed Metal functions the outline lacked (the 500 were 2,973 `cuda` entries and 137 `metal`). Put
  truncation in the first line of every reply, both formats, with the `offset` to continue from.
- **No cap on `callers`.** `callers decoder.Load` returned 109,243 characters. `go_references` has `max`; `callers` and
  `references` have none.
- **Receiver placeholders have no span.** A directory outline in JSON lists a method's receiver type as a `class` entry
  in each file that declares methods on it; where the type is declared in another file the entry has no `start_line` or
  `end_line` (55 such in `internal/app/serveapp`). A consumer that subtracts the two fails. Mark them (`"declared_in":
  "openai.go"` or `"placeholder": true`) or leave them out of `entries`.
- **Generated HTML in the index.** goinfer's untracked `site/_site` was indexed (12,043 files in all) and its pages
  ranked in `search` until `exclude_path_contains` was passed. A `.kenignore` in goinfer fixes this one; ken could also
  warn when a large share of an index is `.html` under a directory named `_site`, `dist` or `build`.
- **`.ken-go.json` was missing.** The typed tools said so on every reply, which is right. goinfer now has one on main.
- **The savings counter counts `search` only.** `status` reported `search 875` and no other call type after a session
  of outlines and typed queries.

## Suggested order

1 and 3 change what an agent can ask and would have removed most of this session's scripts. 4 is cheap, since the spans
are already indexed. 5 and 6 are correctness of messages. 2, 7 and 8 are polish.
