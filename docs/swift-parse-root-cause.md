# gotreesitter Swift grammar misparse root cause (v0.20.0-rc3)

Written 2026-06-03 for filing upstream at
[github.com/odvcencio/gotreesitter](https://github.com/odvcencio/gotreesitter).
This is the diagnostic memo behind the ken-side Swift park documented
in [`DESIGN.md` §10](DESIGN.md#10-risk-register).

## TL;DR

The tree-sitter-swift grammar shipped with gotreesitter v0.20.0-rc3
misparses real-world Swift source files. The lexer fails to recognize
`//` line comments containing common English words as comments —
short Swift files whose first non-empty line is `//  software` or
`//  and` already parse to `root=ERROR`, treating the comment content
as Swift code (`software` becomes a `user_type`, `and` becomes an
`_unannotated_type`). Cross-corpus survey shows 0–35% clean-parse
rates across major shipping Swift codebases — broken on the vast
majority of real Swift.

This is different from the v0.20.0-rc3 C# OOM (separate memo in
[csharp-oom-root-cause.md](csharp-oom-root-cause.md)) — Swift parses
finish promptly, they just produce garbage trees. The downstream
effect is the same: a structural extractor built on
`gotreesitter.DetectLanguageByName("swift")` returns zero useful
data on real corpora.

## Cross-corpus survey

50-file sample per repo (depth-1 clone of latest main, walking
`.swift` files in deterministic order, parsing each with the default
parser pool):

| Repo                                    | Sampled | Clean root | ERROR root | % clean |
| --------------------------------------- | ------: | ---------: | ---------: | ------: |
| Alamofire/Alamofire                     |     50  |       0    |       50   |     0%  |
| apple/swift-nio                         |     50  |       1    |       49   |     2%  |
| apple/swift-collections                 |     50  |       4    |       46   |     8%  |
| sindresorhus/Defaults                   |     43  |      15    |       28   |    35%  |

A grammar that returns `root=ERROR` on the first file of every
Alamofire sample is not in a usable state. Reproducer at
`scripts/swift_survey.go` in the ken tree.

## Minimal reproducer

```swift
//
//  software
//
class Foo {}
```

35 bytes. Single parse:

- `pool.Parse(src)` returns a tree whose `RootNode().Type(lang)` is
  `"ERROR"` and `RootNode().HasError()` is `true`.
- Replace the word `software` with `Test.swift` (or any short
  identifier-shaped word) and the same file parses to
  `root=source_file`, `err=false`.

The bug is in the line-comment lexer: certain English words inside
`// …` content cause the lexer to terminate the comment and resume
in code-state, where `software` then matches as a `user_type` and
following tokens cascade into ERROR recovery.

## What does and does not trigger the bug

Parses cleanly (4-line file `//\n//  X\n//\n\nclass Foo {}`):

```
//  Test.swift           ✓ source_file
//  Copyright            ✓ source_file
//  Copyright (c) 2025   ✓ source_file
```

Parses to ERROR:

```
//  and                  ✗ ERROR
//  software             ✗ ERROR
//  software and         ✗ ERROR
//  associated           ✗ ERROR
//  Permission is hereby ✗ ERROR
//  copy of this         ✗ ERROR
//  to deal              ✗ ERROR
//  in the Software      ✗ ERROR
//  software and associated ✗ ERROR
```

The list of triggers reads like the start of the MIT license — which
is why every Alamofire / Apple Swift file fails: every Swift file in
those repos starts with the MIT-license boilerplate the lexer chokes
on.

## Stack location (educated guess)

This is a lexer bug, not a parser bug — the parse finishes promptly,
it's just that the lexer hands the parser a stream where words from
inside a `//` comment have been re-tokenized as Swift identifiers /
keywords. Likely candidates upstream:

- The custom external scanner that tree-sitter-swift uses for line
  comments. Some grammar ports drop or partially-port the external
  scanner — pure-Go ports of tree-sitter grammars have historically
  had lexer-state-machine issues that don't reproduce against the
  C reference (the gotreesitter C# OOM in
  [csharp-oom-root-cause.md](csharp-oom-root-cause.md) is the
  parallel case in this same release).
- The `comment` token rule, if it's regex-shaped (`//.*$`), depends
  on `$` matching end-of-line — if the lexer's newline handling drifts
  past the comment-terminator, code-state resumes mid-comment.
- A keyword-disambiguation rule that conditionally re-enters
  scanning when a known identifier appears.

Sanity check on the comment lexer (without instrumenting upstream):

```
//\n//  Copyright (c) 2025 Foo\n\nclass Foo {}                  ✓
//\n//  Copyright (c) 2025 Foo Foundation\n\nclass Foo {}        ✓
//\n//  software\n\nclass Foo {}                                 ✗ (35 bytes!)
//\n//  Test.swift\n//  software\n\nclass Foo {}                 ✗
```

The word `Foundation` (a Swift framework name) is *fine*; the word
`software` is *fatal*. So it's not a "treat capitalized identifiers
as types" heuristic — it's the specific lowercase token bag that's
treated as Swift identifiers.

## Suggested fix direction

We don't have the bandwidth from the ken side to debug
tree-sitter-swift's lexer / scanner internals. What we can offer
upstream:

- **Minimal reproducer above** (35 bytes).
- **Per-trigger-word list** above — useful for narrowing whether
  this is a keyword conflict, a scanner-state issue, or both.
- **Cross-corpus failure rates** that demonstrate this isn't an
  edge case.
- **A bounded fix proposal:** if the offending tokens are Swift
  contextual keywords (e.g. `and` doesn't exist in Swift but
  `associated` participates in `associatedtype`, etc.), making the
  scanner's "I'm inside a `//` line comment" state strictly precede
  keyword-detection would block the regression. The C tree-sitter
  reference grammar presumably has this right; the Go port appears
  to have lost it.

## Test that would have caught this

Append to gotreesitter's existing Swift tests:

```go
func TestSwiftLicenseHeaderParses(t *testing.T) {
    src := []byte(`//
//  Foo.swift
//
//  Copyright (c) 2025 Foo Foundation (http://example.org/)
//
//  Permission is hereby granted, free of charge, to any person obtaining a copy
//  of this software and associated documentation files (the "Software"), to deal

import Foundation
class Foo {}
`)
    parser := gotreesitter.NewParser(grammars.SwiftLanguage())
    tree, err := parser.Parse(src)
    if err != nil { t.Fatal(err) }
    defer tree.Release()
    if tree.RootNode().Type(grammars.SwiftLanguage()) != "source_file" {
        t.Fatalf("root = %s, want source_file", tree.RootNode().Type(grammars.SwiftLanguage()))
    }
    if tree.RootNode().HasError() {
        t.Fatal("root has error — license-header comment misparse")
    }
}
```

(The existing Swift tests in the gotreesitter repo presumably use
toy fixtures whose comment headers are short enough to not trigger
the bug.)

## Reproducer artifact

Run `scripts/swift_survey.go` against any 4 cloned Swift repos under
`/tmp/ken-dogfood/` to replicate the cross-corpus failure-rate table.
Trigger-word bisection takes ~10 lines of Go on top of the public
`gotreesitter.NewParserPool(grammars.SwiftLanguage())` API — see the
inline harness in the "What does and does not trigger the bug"
section above.
