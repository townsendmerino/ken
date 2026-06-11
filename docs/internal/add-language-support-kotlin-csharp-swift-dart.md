# Plan: structural-index support for Kotlin, C#, Swift, Dart

> **Status: SUPERSEDED — outcome differs from this proposal.** Kotlin, Dart,
> and **C#** all shipped (C# landed 2026-06-06 once gotreesitter v0.20.2 fixed
> the OOM this plan predicted as a blocker). **Swift** is the one parked — not
> the "clean add" predicted here, but a license-header lexer misparse (see
> [swift-parse-root-cause.md](swift-parse-root-cause.md) /
> [csharp-oom-root-cause.md](csharp-oom-root-cause.md)). Read the per-language
> "Where each language starts today" calls below as a 2026-06-03 forecast, not
> current status.

**Status:** proposal · **Date:** 2026-06-03 · **Owner:** TBD

This plan scopes adding Kotlin, C#, Swift, and Dart to ken. "Support"
means two distinct things in two distinct layers, and the four languages
sit in different starting positions in each. Read the layer model first —
most of the apparent work collapses once it's clear which layer actually
needs new code.

## Two layers, not one

ken touches a language in two independent places:

1. **The chunker** (`aikit/chunk/treesitter`, dispatched via the exported
   `KenToTreeSitter` map). This is *generic* across grammars — it splits a
   file into retrieval chunks at AST node boundaries. It needs no
   per-language code; a language "works" the moment its grammar is in the
   dispatch map and embedded in the binary. This is the layer the v0.2.0
   NDCG numbers measured (Kotlin +0.011, etc.).

2. **The structural index** (`internal/structural`). This powers the
   exact-answer MCP tools (`definition`, `references`/`callers`, `outline`,
   `symbols`) and the Track-1 retrieval enrichment. It is *not* generic —
   each language needs a dedicated `extract_<lang>.go` that walks that
   grammar's specific AST node types and fills `FileStruct` (functions,
   classes, imports, calls, raises). Today only **ten** languages have
   extractors: Python, Go, TypeScript, JavaScript, Java, Rust, C, C++,
   PHP, Ruby.

A third concern couples the two: ken's **release binaries are built slim**
(ADR-033) — they embed only the ~17 grammars `KenToTreeSitter` dispatches,
gated by `grammar_subset_<lang>` tags in `.goreleaser.yml`. The structural
index calls the *same* `grammars.DetectLanguageByName` and therefore sees
the *same* embedded set. A grammar that isn't embedded returns `nil` →
`langCacheFor` returns `nil` → the file silently falls through. So a
structural extractor for a language whose grammar isn't in the slim subset
will work in `go test` (fat build, all 206 grammars) but be **dead code in
the shipped binary**. The drift-guard test
(`internal/buildchecks/subset_test.go`) enforces `.goreleaser.yml` ==
`KenToTreeSitter` in both directions, so the subset can't be widened for
the structural index alone without also widening the chunker map.

## Where each language starts today

| Language | Grammar embedded (slim)? | Chunker dispatch | Structural extractor | Net gap |
|---|---|---|---|---|
| **Kotlin** | ✅ `grammar_subset_kotlin` | ✅ in `KenToTreeSitter` (+0.011 NDCG) | ❌ none | extractor only |
| **Swift** | ✅ `grammar_subset_swift` | ✅ in `KenToTreeSitter` | ❌ none | extractor only |
| **C#** | ❌ deliberately excluded | ❌ excluded (OOM) | ❌ none | grammar risk **+** extractor |
| **Dart** | ❌ absent | ❌ absent | ❌ none | grammar verify **+** chunker map **+** extractor |

The extension→test-penalty regexes in `internal/search/penalties.go`
already name `.kt`, `.swift`, `.cs`, and `.dart`, so the search layer
already anticipates all four — no change needed there.

### Kotlin & Swift — clean adds

The grammar is already embedded and chunking already works. The only
missing piece is the structural extractor. Writing `extract_kotlin.go` and
`extract_swift.go` (+ tests + two map rows each) ships end-to-end with no
grammar-availability risk and no `.goreleaser.yml` change.

### C# — blocked on a known grammar defect

C# was **deliberately excluded** (DESIGN.md §10, ADR-011). The gotreesitter
v0.18.0 C# grammar OOMs on real-world files — 1.7+ GB RSS, SIGKILL on all
three C# bench repos during indexing. It currently routes through the line
chunker, same as the regex chunker did. Two things must happen before a C#
extractor is worth writing:

- **Re-test the C# grammar on the current pin (`v0.20.0-rc2`).** The OOM was
  measured on v0.18.0; the dep has moved twice since (v0.19.1 → v0.20.0-rc2,
  the slim-subset release). If the OOM persists, C# stays excluded and a
  structural extractor for it would be dead code (its grammar isn't embedded
  and shouldn't be).
- **If still broken, decide on a per-parse memory cap** at the chunker/index
  layer (the documented "trigger to revisit" in DESIGN.md §10), or wait for
  an upstream fix. The structural index already has a per-parse *time* cap
  (`parseTimeoutMicros = 1s`); there is no memory cap today.

C# is the highest-risk of the four and should be gated behind this
investigation, not bundled with Kotlin/Swift.

### Dart — needs grammar verification first

Dart is simply absent from `KenToTreeSitter` and the slim subset. Before
any extractor work:

- **Verify gotreesitter v0.20.0-rc2 actually ships a Dart grammar** and that
  `grammars.DetectLanguageByName("dart")` resolves to a loadable language
  (a one-line probe, mirroring `scripts/probe_rust_field_name.go`). Upstream
  tree-sitter-dart exists but is community-maintained; confirm it's vendored
  in this pin and not pathological on real Flutter code.
- If present and healthy, Dart needs **both** a `KenToTreeSitter` row
  (chunker) **and** a `grammar_subset_dart` tag in `.goreleaser.yml` (to keep
  the drift guard green and embed it in slim binaries), in addition to the
  structural extractor.

## The structural-extractor work (per language)

Each extractor is a self-contained `extract_<lang>.go` plus a golden test,
modeled on the ten existing ones (`extract_ruby.go` is 260 LOC and the
cleanest reference; `extract_rust.go` 383 LOC is the most thorough). The
shared contract is `FileStruct`: `Functions []FuncDef`, `Classes []ClassDef`,
`Imports []string`, `Calls []string`, `Raises []string`.

For each new language the steps are:

1. **Add two map rows in `internal/structural/index.go`:**
   - `kenLangToTSLang`: extension → grammar name
     (e.g. `.kt`/`.kts` → `"kotlin"`, `.swift` → `"swift"`,
     `.cs` → `"c_sharp"`, `.dart` → `"dart"` — confirm exact grammar names
     via `DetectLanguageByName`).
   - `langExtractor`: grammar name → the new `extract<Lang>` func.
2. **Write `extract<Lang>` + its `walk<Lang>` recursion**, dispatching on
   `n.Type(lang)` and reading fields with `n.ChildByFieldName(...)`. Use the
   `KEN_DEBUG_AST=1 KEN_DEBUG_LANG=<grammar>` harness in `debug_ast_test.go`
   to dump real parse trees and discover the node-type names for that
   grammar before writing the walker. Add a representative fixture to
   `fixtureForLang`.
3. **Write `extract_<lang>_test.go`** asserting functions/classes/imports/
   calls/raises on a small but representative source (the existing tests are
   the template; ~130 LOC each).

### Per-language AST notes to resolve up front

These determine how much each walker diverges from an existing template;
confirm each against a real `KEN_DEBUG_AST` dump:

- **Kotlin** — `class`/`object`/`interface` declarations; `fun` for both
  top-level functions and methods; primary-constructor params live on the
  class node; `import` directives; calls are `call_expression`. Closest
  template: Java/Kotlin share a lot of shape — start from `extract_java.go`.
- **Swift** — `class`/`struct`/`enum`/`extension` and `protocol`; `func`
  declarations; `init`; `import` declarations; `extension` blocks attach
  methods to a type defined elsewhere (decide whether to fold them into the
  extended type's `ClassDef` or emit standalone — mirror how Ruby handles
  `module` reopening). Swift has no checked exceptions; `Raises` likely stays
  empty (like Ruby/Go where it's special-cased or skipped).
- **C#** — `class`/`struct`/`interface`/`record`; `method_declaration`;
  `using` directives → `Imports`; `throw` statements → `Raises` (C# *does*
  have first-class throw, unlike Ruby). Namespaces wrap everything — decide
  whether to flatten or track as enclosing scope. Closest template: Java.
- **Dart** — `class`/`mixin`/`extension`; `function_signature` /
  `method_signature` / `function_body`; `import` directives; `throw`
  expressions → `Raises`. Flutter widget classes are deeply nested; verify
  the walker handles nested class bodies (Rust/C++ templates handle nesting
  well).

## Phased plan

**Phase 0 — grammar reconnaissance (gating, ~half day).**
Probe `DetectLanguageByName` for `"kotlin"`, `"swift"`, `"c_sharp"`, `"dart"`
on the current pin. Re-run the C# OOM repro on v0.20.0-rc2 against a real C#
repo (the three bench repos that SIGKILLed). Record results. **Output:** a
go/no-go per language and the exact grammar-name strings.

**Phase 1 — Kotlin + Swift extractors (~1–1.5 days each).**
Lowest risk, grammars already embedded. Each: debug-AST dump → walker →
golden test → two map rows. Ships immediately in slim binaries. Update the
`index.go` package doc ("ten languages" → "twelve") and the structural
sections of `docs/DESIGN.md`.

**Phase 2 — Dart (~1.5 days, only if Phase 0 green).**
Add the `KenToTreeSitter` row + `grammar_subset_dart` tag (drift guard will
fail until both agree), then the extractor + test. Verify the slim CI
compile-smoke still builds. Note the binary-size delta (each grammar adds
~1 MB).

**Phase 3 — C# (gated on Phase 0; potentially deferred).**
Only if the v0.20.0-rc2 grammar no longer OOMs, *or* after a per-parse
memory cap lands. Then it's the same recipe as Dart (chunker row + subset
tag + extractor). If still broken, write up the re-test result as a
DECISIONS.md note refreshing ADR-011's §10 risk register and **defer** —
don't ship a dead extractor.

**Phase 4 — docs + drift guards (~half day).**
Update `docs/DESIGN.md` §10 risk register, `CHANGELOG.md`, the
`index.go` package comment's language list, and confirm
`TestSubsetTagsMatchKenToTreeSitter` + `TestKenToTreeSitterGrammarsResolve`
are green. Add a per-language fixture to `debug_ast_test.go` for each
language shipped (the harness expects one per language).

## Risks & open decisions

- **C# OOM may persist** on the current pin. This is the single biggest
  unknown and is why C# is gated behind Phase 0 rather than bundled. Do not
  commit to a C# ship date before the re-test.
- **Dart grammar may be absent or pathological** in this gotreesitter pin.
  Phase 0 resolves this; if absent, Dart is blocked on an upstream/dep
  change, not on ken code.
- **Slim-binary coupling.** Any language whose grammar isn't already in the
  subset (C#, Dart) requires a coordinated `KenToTreeSitter` + `.goreleaser.yml`
  edit or the drift guard fails the build. Kotlin/Swift avoid this entirely.
- **Extractor fidelity is tree-sitter-grade, not compiler-grade** — by
  design (see the `index.go` package doc). Name-based resolution only; no
  type inference. Set test expectations accordingly; don't chase overload
  resolution.
- **`extension`/`partial`/reopened-type semantics** (Swift extensions, C#
  partial classes, Dart extensions) need an explicit modeling decision —
  fold into the base `ClassDef` or emit standalone. Pick one convention and
  apply it consistently; document it in each extractor's header comment as
  the existing ten do.

## Decision needed before starting

Scope for the first PR. Recommended: **Phase 0 + Phase 1 (Kotlin & Swift)**
as the first shippable unit — both are pure extractor adds with no grammar
risk and no release-config change — with C# and Dart sequenced behind the
Phase 0 reconnaissance results.
