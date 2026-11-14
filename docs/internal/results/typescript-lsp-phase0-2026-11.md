# TypeScript 7's language server, measured for a typed tier (Phase 0)

2026-11-14, M1 Pro. Phase 0 of [`typescript-typed-tier-plan-2026-11.md`](../typescript-typed-tier-plan-2026-11.md).
No ken code was written. A throwaway LSP client (stdlib Go, about 380 lines, not kept in the repository) drove
`tsc --lsp --stdio` from `typescript@7.0.2` (npm, server name `typescript-go`).

**What this does not cover: size.** The two trees measured are tiny: a hand-written fixture of four files in two
`tsconfig` projects joined by `references`, and a 58-file project with one `tsconfig`. Every time below is for those.
Load time and memory on a repository of thousands of files are not measured.

## Findings

| Question from the plan | Answer |
|---|---|
| Does the server say when it is ready? | It does not need to. A request sent right after `didOpen` waits for the load and returns the complete answer. No progress or status notification was seen. |
| Do answers cross `tsconfig` projects? | Yes. With only `core/src/shape.ts` open, references of `scale` came back with both uses in `app/src/main.ts`, a file in the other project that was never opened. The server's log shows it loading the project tree for the request. |
| Diagnostics: pulled or pushed? | Pulled, per file (`textDocument/diagnostic`). `workspace/diagnostic` is refused (`workspaceDiagnostics: false`). Nothing was pushed for source files. |
| Do they follow an unsaved change into other files? | Yes, including files never opened. After `didChange` gave `scale` a third parameter, a pull for `main.ts` (not open) returned `Expected 3 arguments, but got 2` on line 5. Taking the change back cleared it. |
| Does rename refuse a clash? | No. See below. |

Times, fixture: `initialize` 0.015 s; first references 0.037 s (includes the load); repeated 0.000 to 0.004 s; a pull
after `didChange` 0.001 s. The 58-file project: first references 0.034 s, repeated 0.001 to 0.003 s, 82 to 85 MB
resident after the load. A full `tsc --noEmit` of the same project took 0.08 s and 127 MB.

## Rename hands out plans the compiler rejects, and one it accepts that changes meaning

`prepareRename` accepted every one of these and `rename` returned edits with no warning or annotation. Each plan was
applied to a copy of the fixture and compiled.

| Rename | Result of applying it |
|---|---|
| exported `scale` to `double`, a name the importing file declares | `TS2440: Import declaration conflicts with local declaration of 'double'` |
| property `side` to `area`, a method the class has | `TS2300: Duplicate identifier 'area'`, and `Square` no longer satisfies `Shape` in the other project |
| local `name` to `k`, the function's parameter | `TS2300: Duplicate identifier 'k'` |
| parameter `max` to `limit`, a module constant the body reads | **compiles, exit 0**: `v + limit` now adds the parameter, not the constant |
| the constant `limit` to `max` (the reverse) | edits handed out; the same capture, not compiled separately |

The plan said a checked rename for TypeScript is worth building only if two such cases could be found in a day. Five
were found in the first hour, one of them silent. Phase 2 has earned its place.

## Other things seen

- References and implementations are nominal where the code is: `implementations of Shape.area` returned
  `Square.area` only, not the `area` of an object literal that is assigned to `Shape[]`. `implementations of Shape`
  did return that array position. The output of a `ts_implementations` tool has to say which it is.
- The server asks the client for `workspace/configuration` (sections `js/ts`, `typescript`, `javascript`, `editor`)
  and registers for `didChangeConfiguration`. Null answers were accepted.
- Without dynamic watch registration from the client it uses its own in-process file watcher, so it sees saved
  changes without `didChangeWatchedFiles`.
- `exit` sent with `"params": null` is rejected (`expected no params, got null`); the field must be absent.
  `internal/gotypes/lsp.go` should be checked for this before it is shared.
- `window/logMessage` is verbose (about 100 lines for the fixture run). A client must drain it.
- Layout: `typescript/bin/tsc` is a Node launcher; the native program is
  `node_modules/@typescript/typescript-<os>-<arch>/lib/tsc`. Starting it directly needs no Node.
- A project on TypeScript 5 or 6 has no such file in its `node_modules`. What ken says there is a design choice, not
  yet made. Pointing the 7.0.2 server at the 58-file project (which pins 5.9.3) worked, but that is ken's choice of
  compiler, not the project's.

## What this means for the design

1. **`ts_check_edit` is cheap and comes first.** Apply the edit with `didChange`, pull diagnostics for the edited
   files and for the files that import them, compare with the pull before, take the change back. The server has no
   workspace pull, so ken must choose which files to ask about; references of the changed exports, or every file of
   the projects that contain or reference the edited file.
2. **Readiness needs no per-language hook** for this server, unlike gopls. The shared client should keep the hook for
   servers that need it (rust-analyzer reports `quiescent`).
3. **One session serves all projects of a workspace.** The Go tier's merge across build configurations has no
   counterpart to build here.
4. **The checked rename is the server's plan plus `ts_check_edit` plus a meaning check.** Diagnostics catch four of
   the five cases. The silent one needs the second check: every identifier spelled with the old or the new name must
   still resolve to the same declaration (`textDocument/definition` before and after).

## Not done

- A repository of realistic size. It needs a clone and an `npm install`, which were not asked for.
- JavaScript with `checkJs`, and JSX beyond the 58-file project's `.tsx` files being loaded.
- `tsc --api`, the route the plan keeps for what LSP cannot give. Nothing so far needs it.
- Cost of the meaning check (one `definition` request per identifier) on a rename with hundreds of sites.
