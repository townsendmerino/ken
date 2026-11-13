# Go typed tier in process: accuracy against gopls, speed and memory (ADR-061)

Measured 2026-11-13 on goinfer (a snapshot of its Go and module files at `5c8b3b3e6` for the differential, so that nothing edits it mid-run; the live checkout for timings). Three configurations (`default` = darwin/arm64, `darwin-gpu`, `linux-cuda`), M-series Mac, 16 GB, Go 1.27.4, gopls v0.23.0.

**Noise.** Other work was running throughout. Timings marked *(a)* were taken at load average 7 to 9, those marked *(b)* at 14 to 20 with the differential itself running beside them, where everything took about 1.7 times as long. Ratios within one block are comparable; absolute times across blocks are not.

## Accuracy: the same lines as gopls

`TestDifferentialAgainstGopls` asks go/types here and gopls over LSP the same question, for declarations of every kind a symbol can name (functions, methods, types, interface methods, struct fields, constants, variables; test files included), and compares the set of (file, line) each returns. References for all; implementations too for types and methods.

| run | engine build | configurations | declarations | questions | differ |
|---|---|---|--:|--:|--:|
| every declaration | first port | default | 27,217 | 33,574 | **0** |
| 1 in 23 | first port | default | 1,184 | 1,459 | **0** |
| 1 in 17 | with the speed work (tables, parallel check, plain-package rule) | default, darwin-gpu, linux-cuda | 5,186 | 6,368 | **0** |
| every declaration | final | darwin-gpu, linux-cuda, then default | 31,524 + 29,405 + 27,217 | | *running when this was written; see the end* |
| the tricky module (`types_test.go`), every declaration | final | its 2 configurations | | 111 + 119 | **0** |

By kind, in the 1-in-17 run across the three configurations: references of 332 constants, 1,732 fields, 1,741 functions, 83 interface methods, 766 methods, 333 types, 199 variables; implementations of 83 interface methods, 766 methods, 333 types. All equal.

The two tagged configurations do not type-check cleanly (1,637 and 280 type errors in the searched packages: a dependency that needs cgo under `CGO_ENABLED=0`). Both engines see the same errors, and the answers were still equal.

**The comparison is of gopls's exact lines (`Runner.GoplsExact`).** By default the engine returns those and one kind of use gopls misses: a field of a generic type reached through an instantiation in another package (the first bullet below). On a small module gopls's references for such a field found the use in its own package and neither of the two in the importing package, while gopls's rename plan edited both. The default answer was checked to contain the exact one on every declaration of all three goinfer configurations (88,146 declarations) and to add **0 lines** there: goinfer has no such use. The tricky module has three such fields, and the extra lines are exactly their uses in the importing package.

**What gopls's lines are.** gopls's answer is not "every use the type checker can see", and the exact mode follows it where it is narrower. Read from gopls's source (`internal/golang/references.go`, `implementation.go`, `internal/cache/xrefs`, `internal/cache/methodsets`, `go/types/objectpath`), and reproduced:

- A use in another package is found only for an exported identifier whose object has an object path. A field reached through an instantiated generic type (`Box[int]{}.Item` in another package) has none, and is not found. (The default mode finds it.)
- A method corresponds to the same-named methods of every package-level named type *of the workspace* related to its receiver by interface satisfaction, in either direction (an interface it is part of, an interface that extends it, each implementing type). The comparison is by name: package path and type name, method by method. An anonymous non-empty interface in a signature matches any other.
- Inside the declaring package only, a method also corresponds to same-named methods of *any* type, the standard library's included: `(*E).Error` matches every `err.Error()` in its own package (112 lines for one of goinfer's) and none elsewhere.
- Packages are searched as gopls compiles them: `p`, `p` with its tests, and the external test package; never a package recompiled for another package's test.

The earlier prototype (ADR-060) approximated the second and third and got 99.6 %; this one ports them.

## Refresh after an edit: the same as a fresh load

`TestRefresh_equalsAFreshLoad` (the tricky module, a chain of 11 edits, both configurations): after each refreshable edit, every answer of the refreshed program equals a fresh load's, 111 and 119 questions per step; the edits that change what `go list` would say (a new import, a build constraint, a first method in a test file) are refused. Removing either the "importers are checked again" step or the "unchanged file keeps its positions" step fails it.

`TestRefreshAgainstAFreshLoadOnARepository` on a goinfer copy *(b)*: 5 files × 3 configurations, references of every 20th declaration compared after each edit: **22,020 comparisons, 0 differ.**

| edited file | packages checked again (of ~575) | refresh | fresh load |
|---|--:|--:|--:|
| `decoder/options.go` (imported by nearly everything) | 57 to 63 | 0.69 to 0.79 s | 2.8 to 3.2 s |
| `tokenizer/tekken.go` | 75 to 84 | 0.75 to 1.03 s | 2.8 to 3.2 s |
| `decoder/arch_test.go` (a test file) | 1 | 0.37 to 0.47 s | 2.9 to 3.3 s |
| `internal/app/serveapp/main.go` | 8 to 9 | 0.16 to 0.19 s | 2.7 to 3.3 s |
| `backend/cuda/backend.go` | 4 under linux-cuda; not compiled under the others | 0.59 s; 0 s | 2.7 to 3.0 s |

## Speed and memory

A merged query over the three configurations, `ken go`-equivalent (`tools/gotypedbench`), on the live goinfer checkout through its `wrap` lock.

| | first query | repeat | different symbol, tree unchanged | after an edit | held between queries | peak while running |
|---|--:|--:|--:|--:|--:|--:|
| gopls command line (ADR-059) | 37 s / 263 s | 28 s / 257 s | same | same | 0 | 2.9 GB |
| gopls over LSP (ADR-060) *(a)* | 8.3 s | 6.9 s | 6.9 s | 6.6 s | 0 | 1.1 GB |
| **go/types, nothing held (`ken go`; `MaxResident: -1`)** *(a)* | **5.3 s** | 4.7 s | 4.8 s | 4.7 s | **0** | **0.8 GB** |
| go/types, 3 configurations at once (`Parallel: 3`) *(a)* | 3.7 s | 4.0 s | 4.0 s | | 0 | 2.1 GB |
| go/types + answer cache (ken-mcp's default) *(a)* | 4.6 s | **0.04 s** | 4.8 s | 4.7 s | under 1 MB | 0.8 GB |
| **go/types, 3 programs resident (the default for a Workspace and `ken-mcp`)** | 6.4 s *(a)* | 0.1 to 0.2 s (0.3 to 0.4 s *(b)*) | **0.1 to 0.2 s** (0.3 to 0.6 s *(b)*) | **1.9 s** after a hub file, 0.5 to 2.2 s after a leaf or test file *(b)* | **0.65 GB** live (about 0.22 GB per program); the process's RSS is 1.3 GB just after loading | 1.4 GB |
| gopls resident ×3 (ADR-060) | 5.8 s | 1.5 s | 0.6 to 0.8 s | 2.1 to 3.1 s | 2.7 to 3.1 GB, 3.3 to 4.2 GB after edits | |

Where a cold configuration's time goes *(b)*: `go list -deps -test -compiled` 0.7 to 0.8 s (a subprocess; all configurations are listed at once), type-check 1.0 to 1.2 s on all processors (the longest single package, `decoder` with its tests, 723 to 850 files, is 0.5 to 0.9 s of it and cannot be split), the query itself 1 to 6 ms. Per query, outside the configurations: finding the declaration 55 to 110 ms, the tree scan 15 ms, source lines and enclosing functions for the hits 10 to 27 ms.

What the speed work bought, on the same machine state: loading one configuration went from 3.5 s (one goroutine, every variant with bodies, 2.2 GB peak for three configurations kept together) to 1.8 to 2.1 s (0.7 to 0.8 GB, one at a time); a resident query from 0.3 to 0.47 s to 0.1 to 0.2 s (declaration search and hit annotation on all processors, one tree scan); an edit under resident programs from 6.2 s (three reloads) to 1.9 s (three refreshes, together).

Not re-measured on a quiet machine yet: the resident and after-edit rows are from *(b)*.

## What is not covered

- **One repository and the fixture.** goinfer has no cgo package of its own (cgo appears only in dependencies); a workspace package that imports "C" is type-checked from the go command's cgo-processed files, and that path has not been compared with gopls.
- **One gopls version.** The rules reproduced are v0.23.0's. A later gopls that changes them would differ from this engine; the differential test is how that shows.
- **Rename.** A rename plan is still gopls's (over LSP): its edit rules are not ported.
- Windows. Parallel configurations beyond the timing above.

## Reproducing

```bash
# accuracy (needs gopls; give it a tree nobody is editing)
KEN_GOTYPES_DIFF_REPO=/path/to/snapshot go test ./internal/gotypes -run TestDifferentialAgainstGopls -v -timeout 8h
KEN_GOTYPES_DIFF_REPO=/path/to/snapshot KEN_GOTYPES_DIFF_STEP=17 KEN_GOTYPES_DIFF_CONFIG=default go test ./internal/gotypes -run TestDifferentialAgainstGopls -v
# refresh against a fresh load (edits and restores files: give it a copy)
KEN_GOTYPES_REFRESH_REPO=/path/to/copy go test ./internal/gotypes -run TestRefreshAgainstAFreshLoadOnARepository -v -timeout 1h
# speed and memory
go build -o /tmp/gotypedbench ./tools/gotypedbench
/tmp/gotypedbench -repo ~/tmcode/goinfer refs:ResidentForward.Forward refs:decoder.Options refs:ResidentForward.Forward
/tmp/gotypedbench -repo ~/tmcode/goinfer -resident 3 refs:ResidentForward.Forward refs:decoder.Options
/tmp/gotypedbench -repo /path/to/copy -resident 3 -edit decoder/options.go refs:ResidentForward.Forward
/tmp/gotypedbench -repo ~/tmcode/goinfer -engine lsp refs:ResidentForward.Forward
```

## The full run of the final engine

Started 14:19 on the snapshot, `darwin-gpu` and `linux-cuda` side by side, `default` after. gopls answers about one question in 0.2 to 0.3 s under those configurations, so each takes hours. At the time of writing: `darwin-gpu` 4,592 of 31,524 declarations, `linux-cuda` 1,223 of 29,405, **0 differences**. The result is appended to ADR-061 when it finishes.
