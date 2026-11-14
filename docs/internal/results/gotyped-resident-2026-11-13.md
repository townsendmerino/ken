# Go typed tier: latency and memory by candidate (ADR-060)

Measured 2026-11-13 on goinfer at `d22c2c0c4` (3,136 Go files in seven workspace modules; `.ken-go.json` lists three configurations: `default` = darwin/arm64, `darwin-gpu` = tags `gpu,goinfer_testhooks,realckpt`, `linux-cuda` = linux/amd64 with tags `cuda,goinfer_testhooks,realckpt`; `wrap` = the timing lock, `--shared`). M-series Mac, 16 GB, Go 1.27.4, gopls v0.23.0.

**Noise.** Other sessions were running, load average 4 to 7 throughout, so absolute times are good to perhaps ±20 %. The conclusions below are ratios of 5× to 40×. One repository, one gopls version.

Two symbols, as the brief asked: **narrow** `ResidentForward.Forward` (an interface method: 497 lines in 255 files, 68 + 276 + 249 per configuration) and **wide** `decoder.Options` (a struct: 2,525 lines in 979 files, 1,089 + 1,902 + 1,739). "Held" is memory kept between queries; "peak" is the largest single process during a query (`/usr/bin/time -l`, which reports the biggest child).

## Summary

| | narrow, first | narrow, repeat | wide, first | wide, repeat | different symbol, tree unchanged | held between queries | peak while running |
|---|--:|--:|--:|--:|--:|--:|--:|
| **0. today (ADR-059, gopls command line)** | 37.4 s | 28.3 s | 263 s | 257 s | same as first | 0 | 2.9 GB / 2.3 GB |
| **5. gopls over LSP, one process per configuration, stopped after (new default)** | 7.0 s (11.7 s with an empty gopls file cache) | 7.0 s | 6.7 s | 6.7 s | 6.7 s | **0** | 1.0 GB |
| 3. + answer cache (opt-in) | 7.0 s | **0.04 s** | 6.9 s | **0.04 s** | 6.7 s | 0.1 to 0.6 MB per few answers; bounded by `CacheBytes` | 1.0 GB |
| 1. resident gopls, one per configuration (opt-in) | 5.8 s | 1.5 s | (second query:) 0.8 s | 0.8 s | **0.6 to 0.8 s** | **2.7 to 3.1 GB**, 3.3 to 4.2 GB after edits | 1.4 GB per gopls |
| 1b. resident, cap below the configuration count | 5.8 s | 6.0 s | | | no gain: each configuration evicts another | 0.9 GB (cap 1), 1.9 GB (cap 2) | |
| 2. go/types in this process (rejected) | 8 s (2.7 s per configuration) | 8 s | 8 s | 8 s | 8 s; 0.1 s if the program is held | 0; or 285 to 340 MB per configuration if held | 420 to 460 MB |
| 4. skip a configuration with the same compiled files | no configuration skipped | | | | | 0 | |

What each candidate buys: **5** is the 5× to 40× on a first query with nothing held. **3** adds the repeat. **1** is the only one that makes a *different* second query on an unchanged tree cost about a second, and it costs 2.7 GB or more for goinfer's three configurations.

## 0. Today: the gopls command line (baseline)

`ken go refs` as shipped in 1.16.0, three configurations, `wrap` on. Both runs on the same tree.

| | first run | repeat | per configuration (first run) | peak |
|---|--:|--:|---|--:|
| refs `ResidentForward.Forward` | 37.4 s | 28.3 s | default 5.2, darwin-gpu 16.8, linux-cuda 14.0 | 2.9 GB (repeat: 1.7 GB) |
| refs `decoder.Options` | 263 s | 257 s | default 8.6, darwin-gpu 143, linux-cuda 110 | 2.3 GB (repeat: 1.5 GB) |
| impls `ResidentForward` | 15.8 s | | default 3.3, darwin-gpu 5.7, linux-cuda 6.4 | 1.2 GB |
| rename-plan `ResidentForward` → `…Renamed` | 8.4 s | | 2.4 each | 1.1 GB |

Repeating a run changes it by under 10 %, so gopls's on-disk cache is not what makes it slow. Reading gopls's `cmd/references.go`: after the `References` call the command line calls `openFile` on **every file that holds a reference** to turn the offset into line:column, and each open makes gopls type-check that file's packages (979 files for `decoder.Options`; 489 s of CPU). The server answered the same request in well under a second.

## 5. gopls over its language-server protocol (chosen)

The same gopls, the same `Server.References` / `Implementation` / `Rename`, driven over stdio with `initialize`, `didOpen` of the declaration's file only, the request, `didClose`, `shutdown`. One process per configuration, started lazily, stopped when the query ends. Through `wrap`. `ken go …` with the new default engine, same machine:

| | time | peak | per configuration |
|---|--:|--:|---|
| refs `ResidentForward.Forward` | 7.0 s (7.4 s via the CLI) | 0.9 to 1.0 GB | 1.5 to 1.9 s each |
| refs `decoder.Options` | 6.7 s | 0.7 GB | 1.5 to 1.8 s each |
| impls `ResidentForward` | 6.8 s | 0.6 GB | 1.6 to 1.8 s each |
| rename-plan | 8.1 s | 1.0 GB | |
| refs `Forward`, **gopls file cache empty** (`HOME` redirected) | 11.7 s | | 4.2 / 2.7 / 3.4 s |
| then `decoder.Options` and `Forward` again in the same process, nothing held | 6.9 s, 7.1 s | | |

Rename is no faster than before (8.1 s against 8.4 s): its cost was never the file opens.

**Identical answers.** Against the ADR-059 baseline above, on the same tree: references 497 lines / 255 files and 2,525 / 979, each hit's file, line, configurations, enclosing function and text equal; implementations 64, of which 3 non-test; rename 59 files, mentions equal, no conflicts, and the merged patch **byte-identical** (`cmp`). Per configuration, the LSP line sets equal the command-line sets for all six (symbol, configuration) pairs. No `INCOMPLETE` in any run.

## 3. Answer cache, keyed on the tree

An answer is remembered with a fingerprint of every `.go` file and module file under the repository (path, size, mtime, plus the content of any file modified within 2 s of the scan); a lookup re-scans the tree and compares. A rename plan's fingerprint also covers the `.md` files and testdata it reads. Cost of a hit: **0.04 s** on goinfer's 3,139 files, almost all of it the scan (38 ms; 67 ms with the `.md` files a rename plan reads), then 1 ms to fingerprint and a copy of the answer. Memory: 0.1 MB for the 497-line answer, 0.5 MB for the 2,525-line one; bounded by `GoTypedOptions.CacheBytes`, least recently used first.

It holds nothing for a *different* query, and an edit anywhere in the tree invalidates every answer (an interface method's references can change from a file in a package the declaring package does not import, so a narrower key is not sound). It buys the repeat and nothing else.

## 1. A resident gopls per configuration

Same LSP driver, kept alive between queries (`MaxResident`), requests serialized per server, the tree scanned before each request and changed files sent as `workspace/didChangeWatchedFiles`.

| step (three configurations, cap 3) | time | held after |
|---|--:|--:|
| `Forward`, first (starts three, loads three workspaces) | 5.8 s | 2.67 GB |
| `decoder.Options` (a different symbol) | 0.79 s (0.1 s per configuration) | 2.68 GB |
| impls `ResidentForward` | 0.60 s | 2.72 GB |
| `Forward` again | 1.53 s | 3.06 GB |
| `decoder.Options` again | 0.79 s | 3.07 GB |
| on a Go-only copy of the tree: after an edit to `decoder/options.go`, `Forward` | 2.25 s (3.09 s with the answer cache also on; 6.6 s with nothing held) | 3.3 to 4.0 GB |
| after restoring the file | 2.13 s | 3.4 to 4.2 GB |

Per gopls: 0.6 to 0.9 GB after the first query, 0.9 to 1.4 GB after several (the process grows with use, and with edits). There is no hard bound: `GOMEMLIMIT` in a configuration's `env` is a soft one (one configuration, 1 GiB: peak 0.93 GB against 1.13 GB, first query +0.5 s).

A cap below the number of configurations does not help (1b): with three configurations and a cap of 1 or 2, each query starts the evicted ones again and costs 6.0 s, the same as nothing held, while holding 0.9 or 1.9 GB.

**Lock.** The pooled gopls is not started under `wrap`; each request holds the lock through a helper (`wrap sh -c 'echo ready; exec cat'`). While three pooled gopls processes sat idle: `timing_lock.py status` printed `free`, and a *timed* (exclusive) run started and finished normally. See ADR-060.

## 2. Answer in this process with go/types (rejected)

*Reversed by ADR-061 on the same day: a port of gopls's search, not an approximation of it, matched gopls on every declaration compared, and is now the default. See [`gotyped-inprocess-2026-11-13.md`](gotyped-inprocess-2026-11-13.md). What follows is the prototype that was rejected.*

A throwaway prototype (not kept; about 600 lines, standard library only): `go list -e -deps -test -json` for every module's `./...`, then `go/types` from source with function bodies for every in-repository package, and a scan of `Uses`. For methods it also follows the interface relation as gopls's `references` does, which gopls's source (`internal/golang/references.go`, `internal/cache/methodsets`) shows is more than "implements": a method corresponds to the same-named methods of every type in the workspace, in either direction, plus, inside the declaring package, to those of any interface from any dependency.

| per configuration | go list | type-check | query | peak | heap if held |
|---|--:|--:|--:|--:|--:|
| default | 0.6 s | 1.7 to 2.1 s | 0.1 s | 420 MB | 285 MB |
| darwin-gpu | 0.6 s | 2.1 s | 0.14 s | 457 MB | 334 to 341 MB |
| linux-cuda | 0.6 s | 1.9 to 2.0 s | 0.13 s | 445 MB | 313 MB |

About 8 s for three configurations one after another, a third of the memory of one gopls. Against gopls over LSP on the two symbols: **all six (symbol, configuration) line sets equal**.

Differential on declarations sampled across every kind, against gopls over LSP, per configuration. Rules were fitted to one 480-declaration sample of `default` (six mismatches at first, all interface methods, then fixed to none), then frozen and run on a disjoint sample of 5,428 queries across the three configurations:

| kind | queries | equal |
|---|--:|--:|
| types, functions, struct fields, constants, variables, interfaces | 4,257 | **4,257** |
| implementations of an interface | 28 | 27 |
| concrete methods | 1,014 | 996 |
| interface methods | 129 | 125 |
| **all** | 5,428 | 5,405 (99.6 %) |

All 23 differences are methods or interface types, and gopls reported more lines in all 23 (in one of them this prototype also had a line gopls did not). The typical one: `func (e *E) Error() string` is also "related" to `error.Error` and `io.Closer.Close` and the interfaces of the standard library, but only within the declaring package's own files (112 extra lines for one `Error`). Those rules are gopls v0.23.0's, fingerprint-based and approximate by design, and may change between releases. A query whose answer must be **identical** to gopls's cannot be served by re-deriving them. Rejected on that, not on cost.

## 4. Fewer configurations

Compiled file sets per package (`go list -deps -test`), 106 in-repository packages: `default` and `darwin-gpu` differ in 20, `default` and `linux-cuda` in 30, `darwin-gpu` and `linux-cuda` in 24 (the backends). `ResidentForward.Forward` is implemented in the CUDA, WebGPU and Metal backends, so every pair differs in a package that can name it, and `decoder.Options` is named by all of them. **No configuration can be skipped** for either symbol. (A configuration whose compiled files are the same as another's would give the same answer; goinfer has none. A tag nobody's file mentions would give one; the check costs a `go list` per configuration, about 0.6 s, and was left out.)

## Not measured

- A second repository. The go/types differential and the resident-memory figures are goinfer's; a small module (the fixtures) holds a few tens of MB per gopls.
- A fully cold start of the *whole machine* (Go build cache and module cache empty). The gopls file cache was emptied; `GOCACHE` and `GOMODCACHE` were left warm.
- Parallel configurations (peak memory would multiply; the one-shot path asks them one after another).
- gopls `-remote=auto`: **does not start from this shell.** `gopls -remote=auto references …` fails with `dialing remote: dial unix /var/folders/…/gopls-…-daemon.…: connect: no such file or directory`, the socket never appearing, as seen on the Mac before. Not relied on.

## Reproducing

```bash
go build -o /tmp/gotypedbench ./tools/gotypedbench
/tmp/gotypedbench -repo ~/tmcode/goinfer refs:ResidentForward.Forward refs:decoder.Options refs:ResidentForward.Forward      # 5
/tmp/gotypedbench -repo ~/tmcode/goinfer -cache-mb 32 refs:ResidentForward.Forward refs:ResidentForward.Forward              # 3
/tmp/gotypedbench -repo ~/tmcode/goinfer -resident 3 refs:ResidentForward.Forward refs:decoder.Options refs:ResidentForward.Forward   # 1
/tmp/gotypedbench -repo <a copy> -resident 3 -edit decoder/options.go refs:ResidentForward.Forward                            # after an edit
ken go refs ~/tmcode/goinfer decoder.Options --engine cli                                                                      # 0
```

`-edit` rewrites a file of the repository it is given (and restores it): run it on a copy. The edit rows above are on a copy holding only the Go and module files (no embedded files, no C sources), where the same first query took 6.6 s instead of 7.0 s.
