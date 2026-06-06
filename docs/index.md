---
layout: default
title: ken
---

# ken

Hybrid (BM25 + Model2Vec) code search via MCP. Pure Go. [Source on GitHub](https://github.com/townsendmerino/ken).

## Get started

- [**Users guide**](https://github.com/townsendmerino/ken/blob/main/docs/USERS.md) — install ken-mcp into Claude Code / Cursor / Codex, the nine tools, common config, troubleshooting.
- [**Developers guide**](https://github.com/townsendmerino/ken/blob/main/docs/DEVELOPERS.md) — mcp.Run library, public API stability, custom chunkers, tuning rerank.

## Demos

- [**ken-demo-kubernetes / ken-demo-postgres** (downloadable binaries)](https://github.com/townsendmerino/ken/releases/tag/demos/v0.1.0)
- [Audit transcripts under `demos/`](https://github.com/townsendmerino/ken/tree/main/demos)

## Writing

- [I shipped two downloadable code search binaries. The audit caught two bugs.](./demos-audit/) — 2026-05-28

## Technical docs

- [Architecture decision records](https://github.com/townsendmerino/ken/blob/main/docs/internal/DECISIONS.md)
- [Design notes](https://github.com/townsendmerino/ken/blob/main/docs/DESIGN.md)
- [Performance discipline](https://github.com/townsendmerino/ken/blob/main/docs/internal/PERF.md)
- [Benchmark conventions](https://github.com/townsendmerino/ken/blob/main/docs/BENCH.md)

## Planning

- [Structural call/dependency graph — plan](https://github.com/townsendmerino/ken/blob/main/docs/internal/structural-call-graph-plan.md) — phased plan to move the structural layer from name-resolved/file-level callers to a resolved, node-level call & dependency graph, with per-phase performance budgets.
