# Go typed tier: rename plans in process, checked by making them (ADR-062)

Measured 2026-11-13 on goinfer (a snapshot of its Go and module files at `5c8b3b3e6`, three configurations: `default` = darwin/arm64, `darwin-gpu`, `linux-cuda`), M-series Mac, 16 GB, Go 1.27.4, gopls v0.23.0.

**Noise.** The comparisons with gopls ran three or four at a time and say nothing about speed. The timings were taken afterwards with nothing else of this work running, at a load average still falling from 12; a second run of each is given where the two differ.

## The same patch as gopls, or one that compiles where gopls's does not

`TestRenameDifferentialAgainstGopls` renames a declaration to a fresh name of the same exportedness, here and with gopls over LSP, and compares the files the two would produce. Then it applies the patches in memory and checks them (the check of ADR-062, decision 3): the one patch when the two agree, both when they differ. Declarations are the ones a symbol can name: functions, methods, types, interface methods, struct fields, constants, variables, test files included.

On the code as it stands:

| run | renames | same patch, and it passes the check | refused by both | this engine edits more, passes; gopls's patch fails | `main` or `init`: gopls renames, this engine refuses | same patch, and the check refuses it | unexplained |
|---|--:|--:|--:|--:|--:|--:|--:|
| `default`, the first 7,716 declarations in file order | 7,716 | 7,499 | 189 | 2 | 26 | 0 | **0** |
| `default`, 1 in 40 across the configuration | 681 | 645 | 34 | 1 | 0 | 1 | **0** |
| `default`, another 1 in 40 | 681 | 651 | 27 | 1 | 0 | 1 | **0** |
| `darwin-gpu`, every third, the first 1,129 in file order | 1,129 | 1,100 | 23 | 0 | 6 | 0 | **0** |
| `linux-cuda`, every third, the first 929 in file order | 929 | 916 | 7 | 0 | 6 | 0 | **0** |
| the tricky module, every declaration, 2 configurations | 147 | 119 to 121 | 20 | 4 | 0 | 0 | **0** |
| the rename module, every declaration | 50 | 42 | 2 | 2 | 0 | 1 | **0** |

Each 1-in-40 row overlaps the first row in 193 declarations, so the goinfer rows are 11,136 comparisons of 10,750 declarations. The two fixtures also hold the cases built to differ: on the tricky module, 2 renames this engine refuses where gopls's patch fails the check, and up to 2 that gopls refuses and this engine makes, with a patch that passes (gopls refuses that interface method in some runs and not in others, which is why the first column is a range); on the rename module, 1 gopls patch that fails (the alias), 1 refusal for a declaration outside the workspace, 1 refusal where gopls's patch fails.

What the columns are, on goinfer:

- **Refused by both** are concrete methods that an interface requires somewhere: the interface's method is the one to rename.
- **This engine edits more** are interface methods whose implementations are found in a package gopls has already visited (`chat/budget.go` `TokenID` and `EncodeSegments`, `serveapp/model_handle.go` `GenerateAudio` and `ResidentConcurrency`). gopls renames the interface's method and the calls, and leaves an implementation: its patch fails the check with "`*decoder.Model` does not implement `modelHandle` (missing method …)", 25 to 76 times.
- **Same patch, and the check refuses it** are `sampling.SamplingParams.LogitBias` and `decoder.TokenLogprob.ID`. goinfer keeps pairs of struct types with the same fields, one in `decoder` and one in `sampling`, and converts one to the other in `decoder/sampling.go`. Renaming a field of one leaves "cannot convert p (variable of struct type SamplingParams) to type sampling.SamplingParams". gopls hands that patch out. A plan from this engine comes back refused, with that line.
- No rename under the two tagged configurations was refused by gopls for their type errors in what was compared here. An earlier, partial run of `darwin-gpu` (1,485 renames, first version of the engine) met 5: gopls refused, this engine renamed, and the patch passed.

An earlier run, of the first version of the engine (two full type-checks per configuration, edits the same), covered `default` 1 in 4: 6,805 renames, 6,526 the same, 262 refused by both, 15 where this engine edits more and gopls's patch fails, and 2 that the first version refused and the present one makes (ADR-062, decision 2, "whose intention"). Only every 25th of its agreed patches was checked, which is how it missed `LogitBias`.

**Every declaration of the repository was started and stopped.** A pass over all 88,146 declarations of the three configurations was begun and stopped by hand at the figures of rows 1, 3 and 4: Francis asked whether that many were needed, and they are not. What a comparison finds depends on the kinds of declaration it meets, and every class above had been met within the first few hundred renames of the 1-in-40 sample. The reproduction commands below run a full pass (at the rate of rows 1 to 4, with four running at once: over an hour for `default`, several for each tagged configuration, where gopls takes up to a second a rename).

## The narrow check against the full one

A patch that renames no field and no method is checked with only the function bodies and packages it can reach (ADR-062, decision 6). `KEN_GOTYPES_RENAME_BOTH=1` makes every check twice, narrowly and in full, and counts the patches the two disagree about.

| run | renames | patches checked both ways | disagreements |
|---|--:|--:|--:|
| the tricky module: fresh names, and each declaration renamed to its neighbour's name | 147 + 147 | every one | **0** |
| the rename module: the same two | 50 + 50 | every one | **0** |
| goinfer `default`, 1 in 40, each renamed to its neighbour's name | 681 | every one this engine computed | **0** |
| goinfer `default`, 1 in 40, fresh names (the third row of the table above) | 681 | every one this engine computed | **0** |

Renamed to a neighbour's name, most renames clash. gopls's rules and this check are different tests and are not held equal: on the tricky module both refuse 100 and both rename 45 or 46, and gopls alone refuses 1 or 2 (the same interface method as above); on the rename module both refuse 28, both rename 17, and this engine alone refuses 5 (among them the method tied to `(*os.File).Close` and the type argument's method, above). On the goinfer sample both refuse 605, both rename 75, and this engine alone refuses 1: `ckptResident.failRes` renamed to `dropped`, the name of a field of a struct that embeds `ckptResident`, which would hide it there.

## What checking every patch found

Each of these came from applying a patch and type-checking, not from reading rules:

- the two struct conversions, above;
- three structs in `backend/metal` whose own field `q` hides an embedded struct's field `q`. The check first refused renaming either one, since the type would "gain" the other. Nothing written changes its meaning there (every use of the name was a use of the field that did the hiding), so a field is now allowed to come out from behind a renamed one; a method is not, because it would answer for the type wherever an interface asks for the name;
- that the type checker calls a type argument that does not satisfy its constraint a *soft* error, which the loader had always dropped: a patch that left `Zero[Heavy]()` uncompilable passed. That error is now kept, and the type argument's method is renamed with the constraint's;
- a data race in the refresh of a held program (ADR-061), under the race detector: a file's remembered facts were written again while another goroutine read them.

## Speed and memory

A plan for goinfer's three configurations, on the snapshot, no `wrap`.

| plan | edits | first version | now, programs held (`ken-mcp`, `engine.Workspace`) | now, `ken go rename-plan` (nothing held) | gopls engine (`--engine lsp`), command line |
|---|---|--:|--:|--:|--:|
| `decoder.Options`, a type | 2,527 lines in 980 files | about 10 s | **0.59 s, 0.64 s** | 5.2 s | 8.0 s |
| `decoder.NewModel`, a function | 14 lines in 9 files | about 10 s | **0.34 s** | 4.0 s | 7.6 s |
| `Options.Backend`, a field | 1,142 lines in 582 files | about 10 s | **1.60 s, 1.71 s** | 5.3 s | 7.3 s |
| `ResidentForward.Forward`, an interface method | 518 lines in 264 files | about 11 s | **3.75 s** the first time, **1.56 s** after | 7.7 s | 7.6 s, and 86 lines in 41 files: gopls refuses under the two tagged configurations |

The first version's column is three times what one configuration took measured alone (0.65 s listing, 0.97 s type-check, 0.18 s to record the identifiers, 0.92 s type-check of the patched tree, 0.18 s to record again, 0.03 s to compare); from the command line under load it took 11 to 14 s. Every plan in the table is the same patch in each column but the last, and comes back checked.

Where a plan's time goes now, one configuration alone, ten runs each: computing the edits 0.02 to 0.04 s; the check 0.07 s for the function, 0.18 s for the type, 0.42 s for the field. Outside the configurations, for the type, about 0.2 s in all: finding the declaration (0.07 s), merging the 980 files (0.08 s), scanning them for what changed, and the leftover mentions. The last two took 0.25 s and 0.28 s before they were spread over the processors, and were not timed again on their own.

What each step bought, `decoder.Options`, programs held:

| | plan |
|---|--:|
| first version: a program of its own per pass, every package searched in full detail | about 10 s |
| edits from the held program's tables, the check as a refresh of it, configurations side by side | 1.73 s |
| only the function bodies that spell the old or the new name | 1.24 s |
| the scan of the patched files and of the leftover mentions on every processor | 0.83 to 0.89 s |
| only the packages that spell a name, or lie between two that do; files read once for the three configurations; only the tables the comparison reads | 0.72 to 0.83 s |
| bodies emptied before parsing, where the held program remembers them | 0.60 to 0.67 s |

Recording the method ties while loading, so that a method's first rename would not have to find them, was measured and not done: it made every load 40 % slower (0.85 s to 1.20 s for a configuration; the largest package from 0.44 s to 0.70 s). Found on first use they cost the first method rename about 2 s for the three configurations, one at a time, and are kept: goinfer's default configuration has 2,032 such constraints in 127 packages.

Memory, the process's resident size. With the three programs held (0.65 GB live, ADR-061): 1.35 GB after loading them; 1.49 GB after the type's plan; 2.34 GB at the peak of a session of nine queries with two field and two method renames, which type-check everything they reach again, one configuration at a time. From the command line: 1.4 to 2.0 GB at the peak, against 1.26 GB for the first version and about 1.0 to 1.2 GB for gopls's own process: the three programs are held until the check is done.

## What is not covered

- One repository and two fixtures; one gopls version.
- A full pass, as said above.
- A workspace package that imports "C": an edit in a file the go command generated is left out and reported, and the check then refuses. Not exercised on a real cgo package.
- Windows.

## Reproducing

```bash
# the comparison with gopls (needs gopls; give it a tree nobody is editing). Every declaration, one configuration:
KEN_GOTYPES_RENAME_REPO=/path/to/snapshot KEN_GOTYPES_DIFF_CONFIG=default \
  go test ./internal/gotypes -run TestRenameDifferentialAgainstGopls -v -timeout 6h
# a sample, every difference written out:
KEN_GOTYPES_RENAME_REPO=/path/to/snapshot KEN_GOTYPES_DIFF_STEP=40 KEN_GOTYPES_DIFF_OUT=/tmp/differences.txt \
  go test ./internal/gotypes -run TestRenameDifferentialAgainstGopls -v -timeout 2h
# the narrow check against the full one, on clashing names:
KEN_GOTYPES_RENAME_REPO=/path/to/snapshot KEN_GOTYPES_DIFF_STEP=40 KEN_GOTYPES_DIFF_CONFIG=default \
  KEN_GOTYPES_RENAME_COLLIDE=1 KEN_GOTYPES_RENAME_BOTH=1 \
  go test ./internal/gotypes -run TestRenameDifferentialAgainstGopls -v -timeout 2h
# speed and memory
go build -o /tmp/gotypedbench ./tools/gotypedbench
/tmp/gotypedbench -repo /path/to/snapshot refs:decoder.Options rename:decoder.Options rename:decoder.NewModel \
  rename:Options.Backend rename:ResidentForward.Forward rename:ResidentForward.Forward
/usr/bin/time -l ken go rename-plan /path/to/snapshot decoder.Options OptionsX
/usr/bin/time -l ken go rename-plan /path/to/snapshot decoder.Options OptionsX --engine lsp
```
