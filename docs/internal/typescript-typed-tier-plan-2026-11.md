# A typed tier for TypeScript and JavaScript, and a check for any edit: plan

2026-11-14. A plan, not a decision: nothing here is built or measured. It follows the Go typed tier (ADR-060, 061,
062, shipped in 1.17.0) and asks what of it can be had for TypeScript and JavaScript, and whether the check behind
`go_rename_plan` can be offered for an edit the agent wrote itself.

## What is true today

Read from `microsoft/TypeScript` at `main` on 2026-11-14. No TypeScript tool was run for this plan.

- The TypeScript compiler is a Go program now. The staging repository `microsoft/typescript-go` was archived in
  September 2026 and the code lives in `microsoft/TypeScript`: module `github.com/microsoft/TypeScript/tsc`, Go 1.27,
  Apache-2.0. Releases: `v7.0.2` (2026-08-20), `v7.1-beta` (2026-10-20).
- **Every package is under `tsc/internal/`**: `checker` (2.7 MB of Go), `ls` (1.6 MB, with `findallreferences.go` and
  `rename.go`), `binder`, `parser`, `project`, `lsp`, `api`. Go does not let another module import them. The only
  thing outside `internal` is the command, `tsc/cmd/tsc`.
- The command has two ways in besides compiling: `tsc --lsp` (a language server) and `tsc --api` (a msgpack protocol
  with sessions, programs, a snapshot file system and text edits; its stability is not stated anywhere I read).
- A project that uses TypeScript 7 has this binary in its own `node_modules`. A project still on TypeScript 5 or 6
  has the JavaScript compiler and `tsserver`, which speak a different protocol.

## Three ways to stand on it

| | A. Client of `tsc --lsp` | B. Client of `tsc --api` | C. A copy of `tsc/internal` inside ken |
|---|---|---|---|
| Needs installed | TypeScript 7 (the project's own) | the same | nothing |
| In ken's process | no | no | yes |
| Pure Go, no cgo | yes (a child process) | yes | yes |
| Code ken reuses | the LSP session, pool, answer cache and tree fingerprint of `internal/gotypes` (about 2,000 lines) | none; a new protocol client | the resident-program design, rewritten against their checker |
| Compiler version | the project's | the project's | whichever ken copied |
| Upkeep | LSP is a standard | follows an API that may change | re-copy 26 MB of Go at every TypeScript release |

**Recommendation: A first, B for whatever the check cannot get from A, C not planned.**

The reason against C is not the size. TypeScript changes what type-checks from one minor version to the next on
purpose, so the right compiler for a repository is the one in its `node_modules`. A copy inside ken would answer with
ken's version. Go did not have this problem: `go/types` is compatible backwards. C would also need every default
writer in the copy audited against the stdout contract of `ken-mcp`.

What A gives up is the 1.17.0 headline. The tools would appear only where TypeScript 7 is installed, and ken would not
be faster than the server it asks. Nothing like "37 s to 0.1 s" should be expected or promised.

**Status on 2026-11-14:** Phase 0 is measured ([`results/typescript-lsp-phase0-2026-11.md`](results/typescript-lsp-phase0-2026-11.md)), the shared client is `internal/lsp`, and `ts_check_edit` is built (ADR-064; this plan's "063" below was written before `go_check_edit` took that number). Phases 1 and 2 are not started.

## Phase 0: measure before deciding (no ken code)

One medium TypeScript repository with several `tsconfig` files, and `tsc --lsp` driven by a throwaway client.

1. Time from start to the first complete answer, and the memory held. Does the server say when it is ready?
2. References, implementations and rename over LSP: do they cross `tsconfig` projects, or answer for one?
3. Overlays: after `didChange` with unsaved contents, how long until diagnostics for the changed file and for the
   files that depend on it? Are diagnostics pulled or only pushed? Do they cover unopened files?
4. Rename: can two renames be found that the server hands out and `tsc --noEmit` then rejects, or that change which
   declaration a name refers to? If none can be found in a day, a checked rename for TypeScript is not worth building.
5. How the npm package lays out its per-platform binary, and what a project on TypeScript 5 or 6 gets (nothing, or a
   message that names the reason).

Record goes in `docs/internal/results/`. The numbers decide Phase 1's shape; no thresholds are set here.

## Phase 1: references, implementations, rename plan (route A)

- `internal/tstypes` (new), with the language-neutral part of `internal/gotypes` moved out to a shared package first:
  `lsp.go`, `pool.go`, `cache.go`, `tree.go`, `group.go`, `render.go`. That move is its own change with no behaviour
  difference, guarded by the existing tests.
- A declaration finder for TypeScript names (`pkg/file.Class.method`) from the structural tier, which already
  extracts TypeScript and JavaScript.
- Configurations are `tsconfig` projects. A file in several projects is asked about in each and the answers merged,
  as the Go tier merges build configurations, unless Phase 0 shows the server already does it.
- Tools `ts_references`, `ts_implementations`, `ts_rename_plan`, best-effort like the four Go tools, shown only where
  a TypeScript 7 `tsc` is found for the repository. CLI `ken ts refs|impls|rename-plan`.
- JavaScript is covered as far as the compiler checks it: `checkJs`, `allowJs`, JSDoc. For plain JavaScript the
  answers rest on inferred types and the output says so.
- An ADR (the next number, 063) records the route, the version rule and what was measured.

## Phase 2: the checked rename for TypeScript

Only if Phase 0 item 4 found real breaks. The plan is made by the server; the check applies it as unsaved contents
and compares before and after: no new diagnostic, and every identifier spelled with the old or the new name still
refers to the same declaration (definition requests, or symbol identity from route B if LSP is too slow for it).
A refused plan carries no patch, as in Go.

## A check for any edit

**Built for Go on 2026-11-14 as `go_check_edit` (ADR-063).** What follows is the proposal as written before it; the ADR records what was built and measured.

The question: the agent sends an edit, ken applies it in memory and checks it, as `go_rename_plan` does for a patch
it computed itself.

**For Go this is mostly there.** `program.checkPatch` (`internal/gotypes/tverify.go`) already takes a patch as whole
file contents, refreshes the held program with it as an overlay, and compares the two programs. What is rename-only
is the comparison: it requires the tokens to be identical apart from identifiers and comments, and reads only the
names the patch changes. An edit that changes code needs a different comparison and cannot use the narrow check.

What `go_check_edit` would report, for one or more files, under every build configuration, without writing anything:

1. **New type errors**, in the edited files and in every package that depends on them, each with file and line.
   Errors that were already there are the baseline and are not reported.
2. **What the edit changed for others**: exported declarations removed or given another type, and types that no
   longer satisfy an interface they were assigned to.
3. **Names that moved**: an identifier outside the edited lines that now refers to a different declaration.

Input as the agent already writes edits: a file with old and new text, or whole new contents. Output in the plan
style: `checked: no new error under 3 configurations`, or the list.

Why an agent would use it and not just save the file and run `go build`: it answers before the file is written, for
all configurations at once, and from a held program it should cost what a refresh after an edit costs today (0.5 to
2 s on the 3,000-file repository; the check itself is unmeasured). It says nothing about behaviour: tests still
have to run.

Work, in order: an overlay refresh that carries no rename facts; a line map from a diff in place of `tokenMap`; the
error-set and exported-API comparison; the tool, the CLI (`ken go check-edit`) and tests that hold its verdict to
`go build` and `go vet` on the edited tree. No port and no differential against gopls is needed: the compiler is the
oracle.

**For TypeScript the same tool is the cheapest thing in this plan**: unsaved contents and a diagnostics diff are what
a language server is built for. It is also the one an agent would call most, since agents edit far more often than
they rename. If Phase 0 item 3 goes well, `ts_check_edit` should come before `ts_rename_plan`.

## Order proposed

1. `go_check_edit` on the engine that exists. No install, no new dependency, and it settles the tool's shape.
2. Phase 0 for TypeScript.
3. `ts_check_edit`, then Phase 1, then Phase 2 if it earned its place.

## Open questions for Francis

- One tool per language (`go_check_edit`, `ts_check_edit`) or one `check_edit` that picks by file type?
- Should a check ever write the edit when it passes, or stay read-only like the rename plan? This plan assumes
  read-only.
- Is "TypeScript 7 only" acceptable for a first version, or is a `tsserver` client for 5 and 6 wanted?
