# Two Python type checkers as language servers, measured for an edit check (Phase 0)

2026-11-14, M1 Pro, Python 3.12.10. The question: can `ts_check_edit`'s design (ADR-064: open the edit as unsaved
contents in a language server, ask for errors before and after) be had for Python, and from which server. No ken code
was written. The throwaway LSP client from the TypeScript Phase 0 drove both servers.

- **pyright 1.1.414** (npm, 34 MB installed, runs on Node): `pyright-langserver --stdio`.
- **ty 0.0.104** (PyPI, one 29 MB native program): `ty server`.

**What this does not cover: size, and real code.** The tree is a hand-written fixture of six files in one package.
Every time and every memory figure below is for that. No third-party imports, no virtualenv, no stubs.

## Findings

| | pyright 1.1.414 | ty 0.0.104 |
|---|---|---|
| Needs | Node | nothing |
| `initialize` | 0.33 s | 0.009 s |
| Memory after the run | 207 MB | 37 MB |
| References, asked right after `didOpen` | **1 of 3** (0.23 s); the same request again returned all 3 | 3 of 3 (0.04 s) |
| Errors for a file that is not open | none, by pull or push | none by default; with `diagnosticMode: "workspace"` given in reply to `workspace/configuration`, yes |
| Errors for an open file, pulled | yes (not advertised in the handshake) | yes |
| Follows an unsaved change in another file | yes, for open files (0.014 s) | yes (0.005 s; unopened files too in workspace mode) |
| `workspace/diagnostic` | no answer in 10 s | no answer in 10 s when nothing had changed; everything in 0.001 s after a change |
| Rename onto a name in use | edits handed out, no warning | edits handed out, no warning |

Both messages for the same broken call: pyright `Argument missing for parameter "digits"`, ty `No argument provided
for required parameter` (rule `missing-argument`).

## What it means for a `py_check_edit`

1. **One procedure works with both: open every source file, pull each, apply the edit, pull each again.** Neither
   server reports on unopened files without configuration, and both report on open ones. This is the TypeScript
   procedure plus a `didOpen` per file. Its cost on a large repository is unknown: opening every file makes the
   server hold every file.
2. **ty's `workspace/diagnostic` cannot be the mechanism as measured.** It waits when there is nothing new to say,
   which is the clean-edit case. Per-file pulls did not wait.
3. **pyright answered a references request before it had finished looking.** An edit check does not ask for
   references, but a `py_references` tool would need a readiness rule for pyright. ty did not show this.
4. **Rename has the TypeScript problem in both.** Renaming a parameter onto a module-level name its function reads
   was handed out by both servers; the result is valid Python that means something else. Python has no compile step
   to catch the other kind either. A checked rename would be worth more here than in TypeScript.

## The question TypeScript did not have: whose checker

A TypeScript project has one compiler and it is in `node_modules`. A Python project may use mypy, pyright,
basedpyright, ty or pyrefly, or none, and they disagree about what an error is. An edit check that reports an error
the project's own checker would not is noise.

- The before-and-after comparison removes what a checker already complains about, so a second-choice checker stays
  usable: only errors the edit adds are shown.
- mypy has no language server (`dmypy` speaks its own protocol). A project on mypy would be checked by something
  else or not at all.
- ty is version 0.0.x. Its rules are still changing.

Not decided here. The options are in the report to Francis.

## Not done

- A real repository with dependencies and a virtualenv: does each server find the interpreter and the installed
  packages by itself, and what does opening every file cost?
- basedpyright (pip-installable, brings its own Node) and pyrefly.
- Whether pyright's pulled diagnostics for open files are complete right after `didOpen`, given that its references
  were not. One file was pulled, some seconds after it was opened.
- New files, deleted files, notebooks, stub files.
