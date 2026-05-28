---
layout: default
title: "I shipped two downloadable code search binaries. The audit caught two bugs."
date: 2026-05-28
permalink: /demos-audit/
---
# I shipped two downloadable code-search binaries. The audit caught two bugs.

I built [ken](https://github.com/townsendmerino/ken) to save Claude tokens.  It's a pure-Go hybrid (BM25 + Model2Vec embedding) code-search tool that speaks MCP, the Model Context Protocol. The way it's meant to be used is: you point a Claude/Cursor/Continue/whatever at ken, and the agent searches your codebase as one of its tools.

To show what that actually feels like — and to have something a non-developer can install and play with in two minutes — I packaged ken with two real codebases baked in. I'm releasing those as standalone binaries today:

- **`ken-demo-kubernetes`** — kubernetes v1.31.0 source, indexed with ken.
- **`ken-demo-postgres`** — PostgreSQL 17.0 source, indexed with ken.

Download, register in `claude_desktop_config.json`, restart Claude Desktop. Your agent can now answer "how does HorizontalPodAutoscaler decide when to scale?" or "what triggers an autovacuum?" by searching the actual source and citing it back at you.

Both are at <https://github.com/townsendmerino/ken/releases/tag/demos/v0.1.0> with builds for `darwin/arm64`, `darwin/amd64`, `linux/amd64`, `linux/arm64`. ~250 MB each. Static, `CGO_ENABLED=0`. About 4 seconds to ready on first launch, then queries respond in tens of milliseconds.

This post is about what happened when I tried to demo my own tool against real codebases. The interesting story isn't "ken is great" — it's the two bugs I found and fixed in ken because of the audit I ran before shipping these demos.

## What you get

Install on macOS:

```
curl -L https://github.com/townsendmerino/ken/releases/download/demos%2Fv0.1.0/ken-demo-kubernetes_darwin_arm64.tar.gz | tar xz
sudo mv ken-demo-kubernetes /usr/local/bin/
```

(`darwin/amd64`, `linux/amd64`, and `linux/arm64` builds are on the [release page](https://github.com/townsendmerino/ken/releases/tag/demos/v0.1.0) — same file naming convention, just swap the suffix.)

Register in `~/Library/Application Support/Claude/claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "ken-demo-kubernetes": {
      "command": "/usr/local/bin/ken-demo-kubernetes"
    }
  }
}
```

Quit-then-reopen Claude Desktop, and the agent has a new code-search tool pointed at the embedded kubernetes corpus. No `KEN_MCP_DEFAULT_REPO`, no model setup — the index and the embedding model are inside the binary.

What's actually in each binary:

|                          | `ken-demo-kubernetes` | `ken-demo-postgres` |
| ------------------------ | --------------------- | ------------------- |
| Indexed corpus           | kubernetes `9edcffc` (v1.31.0) | postgres `d7ec59a` (REL_17_0) |
| Chunker                  | `regex` (Go AST-tracking) | `treesitter` (real C AST) |
| Mode                     | `hybrid` (BM25 + Model2Vec) | `hybrid` |
| Chunks indexed           | 59,795                | 64,506              |
| Binary size              | 216 MB                | 265 MB              |
| Startup (to ready)       | ≈ 3.9 s               | ≈ 3.5 s             |
| First query latency      | 61 ms                 | 33 ms               |
| Runtime RSS              | ≈ 1.4 GB              | ≈ 1.4 GB            |

The 4-second startup isn't "instant" — it's loading a ~200 MB embedded index plus the Model2Vec model and bootstrapping the search engine. After that, queries return in tens of milliseconds. The full release notes include the exact build invocation, ken commit (`7efdbde`), and the source-only `.gitignore` exclude set, so you can rebuild bit-for-bit if you want.

## How I tested it before shipping

The temptation when shipping a demo of your own tool is to fire a good-looking query, take a screenshot, and call it done. I wanted to be a little more rigorous, so did a real audit.

I picked six questions in advance — three per codebase, before running them — chosen to span concrete locator questions ("where is X?"), control-flow questions ("how does Y decide?"), and architectural questions ("where does the scheduler call out to extender plugins?"). For postgres I ran each question twice, once with the `regex` chunker and once with `treesitter`, to test whether treesitter's AST-aware boundaries actually retrieve better than regex's line windows.

That's nine captured conversations, each with the full tool-call trace and every chunk ken returned. Each transcript got graded on:

- **Retrieval correctness.** Did ken surface the actually-relevant file in the top results? At what rank?
- **Hallucination trace-back.** Every factual claim in the agent's answer has to trace to a chunk ken actually returned. If the agent's answer is correct but ken under-retrieved, that proves the model's prior knowledge, not ken — and the demo failed.
- **Tool-use quality.** The demo isn't the agent's final prose; it's the chain of `search` and `find_related` calls the agent made along the way. Did the agent refine sensibly? Did `find_related` earn its place?
- **Postgres regex-vs-treesitter A/B.** Same question on both — which retrieved better, and was the difference real or aesthetic?

The transcripts are in the ken repo under [`demos/transcripts/`](https://github.com/townsendmerino/ken/tree/main/demos/transcripts). Every grade in this post is from one of those nine files.

## The two ken bugs the audit caught

**Bug 1: silent treesitter fallback.** ken's treesitter chunker is supposed to chunk C source at AST boundaries. But when treesitter fails on a particular file — parse timeout, malformed input — it silently falls back to a line chunker. That's by design (ADR-010, graceful degradation), but during the audit I realized: I had no way to tell whether treesitter on postgres was actually producing real AST chunks, or quietly degrading to line chunking on every `.c` file while pretending otherwise. The whole reason to ship treesitter for postgres is the AST boundaries; if those weren't actually happening, the chunker decision was theatre.

So I added a per-reason counter — `total / fallback / unsupported_lang / parse_err / nil_root / invalid_spans` — exposed through `ken perf index`'s JSON. Reran on postgres, and got 0% silent fallback on all 2,383 routed C/H files. Every file got real cAST chunks. The premise held, and now I have a way to monitor it permanently. ([PR #31](https://github.com/townsendmerino/ken/pull/31).)

That number — 0% — is the kind of thing the audit gives you that a screenshot doesn't.

**Bug 2: dev-loop and shipping divergence on cold start.** ken's design has a `mcp.Run` library function that SDK authors call to ship an embedded corpus with a pre-built index baked in. That's the pattern these demo binaries use. It's supposed to load the index from `corpus/.ken/index.bin` in milliseconds.

When I was capturing transcripts via the standalone `ken-mcp` binary (the dev-loop tool, not the embedded-corpus pattern), the postgres-treesitter server hung for four minutes on the first query. After investigation: `ken-mcp` the binary had never actually had the pre-built-load code path. The auto-discovery lived only in the `mcp.Run` library function. The dev tool and the shipping tool had silently diverged.

I unified them — `ken-mcp` now auto-loads `<repo>/.ken/index.bin` when present, hard-fails on mode/chunker mismatch, and falls back to a live build otherwise. Cold start dropped from 44 seconds (live build on first query) to ≈4 seconds to ready (the index now loads at startup), and queries after that return in tens of milliseconds. The demo binaries you can download today use the same code path the dev loop now exercises.

That fix wasn't planned. The audit's "I'm going to capture transcripts" routine surfaced it because the postgres-treesitter capture kept hanging.

## What the postgres regex-vs-treesitter A/B actually showed

I'd locked in `treesitter` for postgres before the audit, on the principle that ken's `regex` chunker has no C rules — it covers Go, Java, Python, Rust, TypeScript, and falls through to line chunking on everything else. The audit was the empirical check on whether AST-aware chunking matters in practice for these three questions.

The headline I'd written in my head was "treesitter pulls each function as one clean chunk; regex fragments them across line windows." That's roughly true for short-to-medium functions and false for long ones, because tree-sitter's chunker also splits — at AST-coherent points, not arbitrary line offsets. So this is NOT a "treesitter uses fewer searches" story. Search counts per question (treesitter / regex): Q1 6 / 10, Q2 6 / 9, Q3 4 / 2. On the autovacuum question, treesitter used more searches than regex, not fewer.

The real difference is more specific: treesitter exposes retrieval surfaces regex structurally can't. Three concrete examples from the audit:

**Q1 (hash join vs merge join choice).** Postgres's planner doesn't pick by rule; it generates both candidate paths and lets a fuzzy cost comparison decide. The function that runs that comparison is `compare_path_costs_fuzzily` in `pathnode.c`. Treesitter caught it as a clean doc-comment + body chunk at `pathnode.c:134-162`. Regex never surfaced that function — it got the "no single branch, cost-and-keep-cheapest" architecture right, but couldn't be as precise about the "if each path wins on a different cost dimension, both are kept" rule, because the function that encodes it wasn't in any retrieved chunk.

**Q2 (WAL writer flush + checkpoint triggers).** On synchronous commits, regex hedged — *"synchronous commits force a flush via `XLogFlush()`"* — because none of its ten search results retrieved `XLogFlush()` itself. Treesitter pulled `XLogFlush` at `xlog.c:2829-2872` via a `find_related` call, turning that hedge into a grounded citation. It also caught the shutdown `CreateCheckPoint` call site at `xlog.c:6604-6642`, which regex covered only generically.

**Q3 (what triggers an autovacuum).** The decision logic lives in `relation_needs_vacanalyze` in `autovacuum.c`. That function is about 220 lines long. Treesitter split it into multiple chunks — but one of those was the function's full doc comment (`autovacuum.c:2867-2903`), the PostgreSQL maintainers' own natural-language description of when a table gets autovacuumed, and the agent quoted it directly. Regex couldn't, because its line-windows fragmented the doc comment across other code, never producing it as a citable standalone unit.

So the finding is: treesitter exposes more retrieval surfaces — doc comments, fuzzy-comparison functions, sync-commit flushers — that regex structurally can't produce. It doesn't always mean fewer searches; it does mean more citable substance.

## Honest counter-points

A few things the audit found that were not optimal, but they are what they are.

On Q1, regex retrieved one specific chunk treesitter missed — the comment in `pathnode.c:2611-2660` that says "hashjoin never has pathkeys." Treesitter mentions that fact but couldn't cite the comment directly. It isn't strictly better on every chunk; the chunkers expose different surfaces, and sometimes regex's wider line-window catches something AST chunking misses.

On Q3, treesitter used more searches (4) than regex (2). I'd predicted the opposite. The extra searches did produce a more cited answer, but if you're optimizing for "agent reaches a good answer in the fewest tool calls," this question is one where regex wins.

The HPA question on kubernetes (Q1 of the k8s set) required the agent to refine past API-type boilerplate. The first search on "how does HorizontalPodAutoscaler decide when to scale a deployment?" returned client-go generated code; the agent had to re-search in control-flow terms ("reconcile loop computes desired replica count from observed metrics") to surface the real `pkg/controller/podautoscaler/` logic. That's the agent doing what an agent client should do, but it's also a real "hybrid search doesn't always hit cold" data point.

Residual noise after two rounds of source-only `.gitignore` curation includes cross-controller code on k8s queries about reconcile loops (semantic bleed to other controllers), and gettext `.po` translations of error messages on postgres queries about checkpointer warnings. The agent ignored them cleanly in every transcript, but they're there.

ken's own `mcp.Run` SDK pattern, today, only works for in-tree authors. The `treesitter` chunker lives at `internal/chunk/treesitter`, which Go module rules make unimportable from outside the ken module. The demo binaries work because they're inside the ken repo (`ken/demos/...`); an external SDK author following the documented pattern would hit that wall. Filed as [ken#36](https://github.com/townsendmerino/ken/issues/36).

## What's next

Two follow-ups I know about and want to fix:

- The chunk count for the postgres-treesitter index wobbles by ~0.1% across rebuilds under machine load. Cited files are stable across the wobble (verified against the shipped binary's citations), so it doesn't affect demo correctness, but the determinism contract should be tighter than that. Filed as [ken#35](https://github.com/townsendmerino/ken/issues/35).
- The external-author treesitter gap mentioned above. Filed as [ken#36](https://github.com/townsendmerino/ken/issues/36).

And one question I don't know the answer to yet: **does anyone download these?**

## Try it

- kubernetes binary: [github.com/townsendmerino/ken/releases/tag/demos/v0.1.0](https://github.com/townsendmerino/ken/releases/tag/demos/v0.1.0)
- postgres binary: same release.
- ken itself: [github.com/townsendmerino/ken](https://github.com/townsendmerino/ken).
- Full audit transcripts: [demos/transcripts/](https://github.com/townsendmerino/ken/tree/main/demos/transcripts).
- Audit rubric: [demos/transcript-audit-rubric.md](https://github.com/townsendmerino/ken/blob/main/demos/transcript-audit-rubric.md).

If you install one and the install or the answer to a question is interesting in either direction, I'd really like to hear about it.
