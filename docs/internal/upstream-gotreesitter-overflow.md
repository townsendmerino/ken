# Upstream issue — gotreesitter stack overflow on large files

**Status: RESOLVED upstream in gotreesitter [v0.20.6](https://github.com/odvcencio/gotreesitter/releases/tag/v0.20.6); verified fixed at ken's pinned v0.48.1 (2026-08-18).**
Filed 2026-06-06 as [odvcencio/gotreesitter#110](https://github.com/odvcencio/gotreesitter/issues/110) (roadmap #18c), against **[odvcencio/gotreesitter](https://github.com/odvcencio/gotreesitter)**; captured from the ken crash investigation (2026-06-05).

The maintainer traced the unbounded recursion to Go result-compatibility
normalization (`normalizeGoDotLeafChildren` → `finalizeResultRoot` →
`buildResultFromNodes`), fixed it (parser/recovery work from #105), and
**released the fix in v0.20.6** with an opt-in cobra large-file regression
test. ken now pins **v0.48.1** (`go.mod`), far past the fix.

**Verification (2026-08-18):** the two exact crashers from this report —
`spf13/cobra@61968e8` `completions_test.go` (117,138 B) and
`command_test.go` (80,115 B) — both now parse to `stop=accepted`,
`root=source_file` under ken's pinned gotreesitter, no crash. A committed
always-on regression guard, `TestParse_LargeTableDrivenGo_NoFatalOverflow`
in `internal/structural/overflow_regression_test.go`, parses a synthetic
large table-driven Go file (the crasher shape, > the 117 KB real crasher)
directly through the parser; a future gotreesitter downgrade that
reintroduces the fatal overflow makes that test's process die (loud CI
failure). The test also accepts `KEN_GTS_COBRA_DIR` pointing at a
`spf13/cobra@61968e8` checkout to validate against the real crashers.

**Guard status:** the `maxEnrichBytes` 64 KiB ceiling in
`internal/structural/extract_file.go` is **retained as cheap
defense-in-depth**, not as the primary mitigation — the crash class it was
added for is gone. It can be raised or removed if enrichment coverage on
large (64 KiB–2 MiB) source files matters more than the belt-and-suspenders
bound; that is a retrieval-quality tuning decision, not a safety one.

The original filed text is retained below for the record.

---

**Title:** `Parser.Parse stack-overflows (fatal, unrecoverable) on large source files instead of returning an error`

**Version:** gotreesitter v0.20.1 · Go 1.26.3 · darwin/arm64 (also reproduces under the same version on linux/amd64 CI)

### Summary

`(*Parser).Parse` (via `(*ParserPool).Parse`) overflows the goroutine
stack on some large real-world source files. Because a Go stack overflow
is a **fatal runtime error**, not an `error` return, it:

- cannot be caught with `recover()`,
- bypasses the `(tree, err)` error path entirely (callers that carefully
  check `err` still crash), and
- **takes down the entire host process**, not just the parse.

For a library whose contract is "parse returns a tree or an error," a
fatal overflow on attacker-or-corpus-controlled input is a serious
robustness hole — any tool that parses files it didn't author (linters,
indexers, code-search, editors) can be crashed by a single large file.

### Reproduction

Two files in [spf13/cobra](https://github.com/spf13/cobra) at revision
`61968e893eee2f27696c2fbc8e34fa5c4afaf7c4` crash a Go-grammar parse:

| file | size | result |
|---|---:|---|
| `completions_test.go` | 117,138 B | **fatal stack overflow** |
| `command_test.go` | 80,115 B | **fatal stack overflow** |
| `command.go` | 61,142 B | parses fine |

Both crashers are large **table-driven test files** (long `[]struct{…}{…}`
literals with many entries). Minimal driver, adapted from how we call the
library:

```go
package main

import (
	"fmt"
	"os"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

func main() {
	data, _ := os.ReadFile("completions_test.go") // cobra @ 61968e8
	lang := grammars.DetectLanguageByName("go").Language()
	pool := gotreesitter.NewParserPool(lang)
	tree, err := pool.Parse(data) // <-- never returns; fatal stack overflow
	fmt.Println(tree, err)
}
```

### What we ruled out — it's size/node-count, not nesting depth

We initially suspected deep nesting. It is **not**: a synthetic
10,000-deep parenthesized expression (`var x = ((((…1…))))`, ~20 KB)
parses cleanly and returns a tree. The crash correlates with overall file
size / node count of these wide table-driven literals, with the threshold
for these particular Go files sitting between 61 KB (ok) and 80 KB (crash).

### Observed stack trace (truncated, repeats deeply)

```
fatal error: stack overflow

runtime stack:
...
github.com/odvcencio/gotreesitter.(*Parser).buildResultFromNodes
	.../parser_result_root_build.go:111
github.com/odvcencio/gotreesitter.(*Parser).buildResultFromNodes
	.../parser_result.go:767
github.com/odvcencio/gotreesitter.(*Parser).parseInternal.func10
	.../parser.go:2133
github.com/odvcencio/gotreesitter.(*Parser).parseInternal
	.../parser.go:2385
github.com/odvcencio/gotreesitter.(*Parser).Parse
	.../parser_api.go:533
github.com/odvcencio/gotreesitter.(*ParserPool).Parse
	.../parser_pool.go:159
```

The recursion is in result-tree construction (`buildResultFromNodes`
calling itself), so the depth of that recursion appears to track the
result tree's structure rather than source nesting.

### Why the existing knobs don't help

- `WithParserPoolTimeoutMicros` does **not** prevent this: the overflow
  happens in synchronous recursion and blows the stack before any
  wall-clock check fires.
- There is no exposed depth / node / stack cap on `ParserPool` /
  `Parser` (only timeout / logger / included-ranges / GLR-trace /
  ambiguity-profile options), so a caller cannot bound it defensively.

### Suggested fix (in rough order of preference)

1. Make `buildResultFromNodes` (and any sibling unbounded recursion in
   `parseInternal`) **iterative**, or bound its recursion with an explicit
   work stack, so large inputs build a tree instead of overflowing.
2. Failing that, add a **configurable max recursion depth / node budget**
   that, when exceeded, returns a normal parse error (or a tree whose
   `ParseStopReason()` is a non-`Accepted` stop reason) — i.e. degrade
   gracefully through the existing error/stop-reason channel rather than
   via `fatal error`.

Either way the key ask is: **`Parse` should never `fatal error`; it should
return through its documented `(tree, err)` / stop-reason contract** so
callers can degrade gracefully.

### Workaround we adopted (for other affected users)

We skip the parser for inputs above a 64 KiB byte ceiling and treat those
files as un-parseable (graceful no-op). It's a heuristic proxy for parse
cost — it clears the cases we hit, but it's not a real fix: it's a byte
threshold standing in for a node/recursion bound we can't express through
the API, and a smaller-but-pathological file could still overflow.
```
