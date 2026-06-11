# Adding a language to ken's structural index

This guide walks through extending ken's structural-navigation
tools (`definition` / `references` / `callers` / `outline` /
`symbols`) to cover a new programming language. Aimed at
contributors comfortable writing Go.

At the time of writing, ken's structural index covers thirteen
languages: Python, Go, TypeScript, JavaScript, Java, Rust, C,
C++, C#, PHP, Ruby, Kotlin, Dart. Adding a fourteenth is well-paved.
(Swift is the one parked extractor — see swift-parse-root-cause.md.)

## The shape

Each supported language has a single Go file
[`internal/structural/extract_<lang>.go`](../../internal/structural)
that walks a gotreesitter-produced parse tree and fills a
`FileStruct{Path, Functions, Classes, Calls, Imports, Raises}`
record. The file is registered in two maps in
[`internal/structural/index.go`](../../internal/structural/index.go):

```go
var kenLangToTSLang = map[string]string{
    ".py": "python",
    // ... maps file extension to gotreesitter grammar name
}

var langExtractor = map[string]func(src []byte, root *gotreesitter.Node, lang *gotreesitter.Language, fs *FileStruct){
    "python": extractPython,
    // ... maps grammar name to the extractor function
}
```

The structural Index builds in two passes:

1. **Pass 1** (parallelized): for each file the walker visits,
   pick the grammar by extension, parse via gotreesitter,
   dispatch to the registered extractor.
2. **Pass 2** (single-threaded): build reverse maps (callers
   keyed by callee name, defs keyed by symbol name, methods
   keyed by both bare and qualified names).

Your job is Pass 1's extractor for the new language.

## Prerequisites

- The gotreesitter dependency at `aikit/go.mod` must ship a
  grammar for your target language. Check
  [grammars.DetectLanguageByName](https://github.com/odvcencio/gotreesitter)
  — `_, ok := grammars.DetectLanguageByName("cobol"); ok`
  tells you whether `cobol` is a valid grammar name.
- gotreesitter's grammar must be performant enough on
  real-world corpora. DESIGN.md §10 documents two grammars that
  failed this test (C# OOMs on dapper, bash is pathologically
  slow on bash-it). The dogfood pass below catches this.
- A representative test corpus you can run the dogfood harness
  against — ideally a popular GitHub repo with ~1k+ files of
  the language.

## Step 1: probe the AST

The repo carries a debug helper at
[`internal/structural/debug_ast_test.go`](../../internal/structural/debug_ast_test.go)
that dumps a parse tree for an inline fixture. Use this to
discover the exact node types and field names your extractor
needs to handle.

Add an entry for your language to `fixtureForLang` with a
representative source string covering functions, classes,
methods, calls, imports, and exception throws. Run:

```bash
KEN_DEBUG_AST=1 KEN_DEBUG_LANG=<grammar-name> \
    go test -run TestDebug_ASTShape ./internal/structural -v
```

The output lists each AST node + its field names. Looking for:

- The function declaration node — `function_declaration`,
  `function_definition`, `method_declaration`, `function_item`,
  `method`, etc. Names vary per grammar.
- The function name — usually a `name`-field child of the
  declaration node (sometimes the first `identifier`).
- The parameter list — usually a `parameters`-field child
  whose own children describe each parameter.
- The class / type declaration node.
- The call expression — `call_expression`, `call`,
  `method_invocation`, `function_call_expression`, etc.
- The import/use/include declaration.
- The exception throw / raise node (if the language has one).

**Pitfall (gotreesitter quirk)**: occasional subtrees come back
with empty field labels — `ChildByFieldName("name", lang)`
returns nil even though the identifier child is right there.
Rust's `extract_rust.go` documents this in detail and falls
back to a positional first-named-identifier lookup
([`rustFirstNamedIdentifier`](../../internal/structural/extract_rust.go)).
Be ready to add a similar fallback for your language.

## Step 2: write `extract_<lang>.go`

Follow the existing extractors as templates. Read
[`extract_typescript.go`](../../internal/structural/extract_typescript.go)
for a grammar with rich AST (class declarations, interfaces,
arrow functions, multiple call shapes). Read
[`extract_ruby.go`](../../internal/structural/extract_ruby.go)
for a grammar with idiomatic surprises (parenless calls,
`raise X` as a method call, modules vs classes).

The file's structure:

```go
package structural

import "github.com/odvcencio/gotreesitter"

// extractMyLang walks a tree-sitter-<lang> AST and fills FileStruct.
// Document which node types this handles + which Stage 8 v0
// limitations are accepted.
func extractMyLang(src []byte, root *gotreesitter.Node, lang *gotreesitter.Language, fs *FileStruct) {
    walkMyLang(src, root, lang, "", fs)
}

func walkMyLang(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, enclosingClass string, fs *FileStruct) {
    if n == nil {
        return
    }
    switch n.Type(lang) {
    case "function_declaration":
        // Build a FuncDef. Recurse into the body.
    case "class_declaration":
        // Build a ClassDef; recurse with enclosingClass=class.Name.
    case "call_expression":
        // Resolve the callee name; add to fs.Calls if not noise.
    case "import_declaration":
        // Resolve the bound name; add to fs.Imports.
    case "throw_statement":
        // Resolve the thrown name; add to fs.Raises.
    default:
        recurseChildrenMyLang(src, n, lang, enclosingClass, fs)
    }
}

func recurseChildrenMyLang(src []byte, n *gotreesitter.Node, lang *gotreesitter.Language, enclosingClass string, fs *FileStruct) {
    nc := n.NamedChildCount()
    for i := 0; i < nc; i++ {
        walkMyLang(src, n.NamedChild(i), lang, enclosingClass, fs)
    }
}

func myLangIsBuiltinOrNoise(name string) bool {
    switch name {
    // The language's universal idioms / stdlib calls that
    // every program has but carry no user-vocabulary signal.
    // Calibrate by running the dogfood pass first; the top
    // unfiltered calls list tells you what to add here.
    case "println", "print", "len":
        return true
    }
    return false
}
```

### Helpers shared across extractors

[`internal/structural/index.go`](../../internal/structural/index.go)
and the existing extractors provide:

- `nodeText(src, node)` — slice the source bytes by the node's
  byte range.
- `dedupAppend(slice, name)` — append-if-not-present helper for
  `fs.Calls`, `fs.Imports`, `fs.Raises`.
- `FuncDef`, `ClassDef`, `FileStruct` types you fill.
- The thread-safe gotreesitter parser pool — your extractor
  receives the live `*Language` handle; everything else is
  per-call.

## Step 3: register the language

Two map entries in
[`internal/structural/index.go`](../../internal/structural/index.go):

```go
var kenLangToTSLang = map[string]string{
    // ... existing entries
    ".mylang": "mylang",
    ".other":  "mylang", // multiple extensions for one grammar are fine
}

var langExtractor = map[string]func(...) {
    // ... existing entries
    "mylang": extractMyLang,
}
```

The first key is the file extension (lowercase, leading dot);
the value is the gotreesitter grammar name from step 1.

## Step 4: write `extract_<lang>_test.go`

Fixture-based unit tests. Look at
[`extract_python_test.go`](../../internal/structural/extract_python_test.go)
or [`extract_ruby_test.go`](../../internal/structural/extract_ruby_test.go)
for the standard shape: write a small inline source string with
known function / class / call / import / raise content, run
`structural.Build` on a `t.TempDir()` containing that source,
and assert on the resulting FileStruct fields.

Pin specifically:

- Function names captured (top-level + methods)
- Class / type / module names
- Method enclosing-class linkage (verify `IsMethod=true` and
  `EnclosingClass="ClassName"` for methods)
- Call extraction (including method-style `obj.foo()` calls)
- Import binding (the LOCAL name, not the module path —
  e.g. `import { foo } from "./bar"` binds "foo", not "bar")
- Exception throws (where applicable)
- Noise filter — assert that universal idioms (`println`,
  `length`, etc.) are NOT in `fs.Calls`

Run:

```bash
go test ./internal/structural/ -run 'TestBuild_MyLang' -v
```

## Step 5: dogfood against a real repo

Unit tests cover what you remembered to test. The dogfood
harness covers what you didn't.

[`scripts/dogfood_languages.go`](../../scripts/dogfood_languages.go)
clones eight popular repos, runs `structural.Build` against
each, and reports per-corpus stats. Add a row for your
language to its `targets` list — pick a popular ~1k-file repo
that uses your language idiomatically.

Run:

```bash
go run scripts/dogfood_languages.go
```

The output tells you:

- **Crashes / parses-but-extracts-nothing** — likely a
  gotreesitter quirk you need to handle. The C# OOM and bash
  timeout (DESIGN.md §10) surfaced this way.
- **`empty-Name` warnings** — extractor bug. The Rust extractor
  shipped initially with this; see the fallback note in step 1.
- **Top calls list** — should be dominated by user-vocabulary
  names. If it's flooded with stdlib idioms (`println`,
  `panic`, `slice`, etc.), extend your `myLangIsBuiltinOrNoise`
  filter and re-run.
- **Top imports list** — should bind the right name. If the
  list shows module paths rather than the imported symbol, the
  binding resolution is wrong.
- **File count vs `indexed files`** — large gap means many
  files matched the extension but the grammar dropped them
  (parse errors). Spot-check; some may be expected (e.g. PHP
  files that are HTML templates).

Refine the extractor until the dogfood numbers look sensible.
The existing 10-language extractors all show top-calls lists
that are recognizable as user vocabulary on their respective
repos.

## Step 6: validate precision

[`scripts/precision_sample_edges.go`](../../scripts/precision_sample_edges.go)
samples 50 random `(caller_file, callee_name)` edges and
verifies them with a regex-based oracle independent of the
gotreesitter extractor. Run it against your dogfood corpus:

```bash
KEN_PREC_CORPUS=/tmp/ken-dogfood/<your-corpus> \
    go run scripts/precision_sample_edges.go
```

Lenient precision should be **100% (no hard hallucinations)**.
Strict precision is normally 96-100% — lower values are usually
explainable language idioms (Ruby parenless calls, Rust macros,
Java `new Foo<>(`, PHP parenless `new Foo;`). If you see hard
hallucinations (the callee name never appears in the caller
file), the extractor is producing spurious edges and needs
fixing.

This is the same gate Stage 8 Gate 2 ran across 8 languages
([memo](../../outputs/stage8-gate-2-call-edge-precision.md)).

## Step 7: integrate, lint, commit

Verify the whole tree is green:

```bash
go build ./...
go test ./...
go vet ./...
gofmt -l cmd internal mcp bench
golangci-lint run ./...
```

Update [`CLAUDE.md`](../../CLAUDE.md)'s structural-extractor
language list if your contribution adds another language
(the current list is "thirteen languages: Python · Go · TypeScript
· JavaScript · Java · Rust · C · C++ · C# · PHP · Ruby · Kotlin ·
Dart").

**Cross-repo wiring (post-ADR-034).** The structural extractor
lives in ken (`internal/structural`), but the tree-sitter *chunker*
now lives in the sibling `aikit` repo. A complete language add
therefore touches three more places beyond this guide:

1. `aikit/chunk/treesitter`'s `KenToTreeSitter` language map (the
   chunker side) — and a tagged `aikit/chunk/treesitter` release
   that ken then re-pins in `go.mod`.
2. The `grammar_subset_<lang>` build tag in
   [`.goreleaser.yml`](../../.goreleaser.yml) (so slim release
   binaries embed the new grammar).
3. The drift guard in `internal/buildchecks`
   (`TestSubsetTagsMatchKenToTreeSitter`) ties (1) and (2) together
   and will fail until both are consistent.

See [add-language-support-kotlin-csharp-swift-dart.md](add-language-support-kotlin-csharp-swift-dart.md)
for a worked cross-repo example.

Commit per the project conventions
(see [DEVELOPERS.md → Pull requests](../DEVELOPERS.md#pull-requests)).
The commit message should mention the dogfood-validated repo
and the precision-sample result.

## Common patterns

- **Function with a receiver / type / class qualifier** — handle
  it as a method by setting `FuncDef.IsMethod=true` and
  `EnclosingClass=<TypeName>`. The Pass 2 builder indexes
  methods under both bare and qualified `Type.method` names,
  so `definition("User.Login")` resolves correctly.
- **Module-scoped functions** (Ruby `def self.foo`, Rust
  `fn foo` in an `impl Foo {}`) — extract as methods of the
  enclosing type, not as top-level defs. The Ruby
  `singleton_method` and Rust `impl_item` arms show the
  pattern.
- **Generic-type instantiation** — `new Foo<>()`,
  `Foo::new()`, `Vec<T>` — usually a call shape that needs a
  type-name-leaf helper. See `javaTypeLeafName` in
  [`extract_java.go`](../../internal/structural/extract_java.go) or
  `cppScopeLeaf` in
  [`extract_cpp.go`](../../internal/structural/extract_cpp.go).
- **Import binding** — the bound LOCAL name is what an agent
  searches for. `from foo import bar` binds "bar", not "foo".
  `use crate::foo::Bar as Quux` binds "Quux". The Python and
  Rust extractors handle the rename / alias forms; cross-
  reference them.

## When the dogfood pass kills your language

Two failure modes shipped recently and have documented
mitigations:

1. **Grammar OOMs on real-world files.** The gotreesitter
   v0.18.0 C# grammar grew parse tables unboundedly during
   dapper indexing (1.7+ GB RSS → SIGKILL), and C# was excluded
   from `kenLangToTSLang` for it. **Resolved 2026-06-06:**
   gotreesitter v0.20.2 fixed the OOM and C# shipped as ken's
   13th language (a 64 KiB `maxEnrichBytes` guard in
   `extract_file.go` also backstops the per-file blow-up path).
   The pattern still applies to any future grammar that misbehaves.
2. **Grammar is pathologically slow.** The gotreesitter v0.18.0
   bash grammar times out on ~39% of real bash-it files at
   1s/parse. Same exclusion treatment.

If your language hits either mode, the right outcome is **don't
add it** — even with a working extractor, the chunker would
fall through to line-chunking and produce poor structural
results. Document the finding in DESIGN.md §10 and open an
issue tagged with the gotreesitter dependency.

## Where to look when stuck

- **The ten existing extractors** — each documents the AST node
  types it handles + the gotreesitter quirks it works around.
  Read 2-3 close to your target language.
- **[BENCH.md](../BENCH.md)** — quality methodology; if you want to
  prove your extractor doesn't regress retrieval, this is the
  harness.
- **[GitHub issues](https://github.com/townsendmerino/ken/issues)**
  — search the structural-related issues; gotreesitter edge
  cases get filed here.
- **The dogfood + precision scripts** — `scripts/dogfood_*.go`
  and `scripts/probe_rust_*.go` are templates for ad-hoc
  diagnosis.
