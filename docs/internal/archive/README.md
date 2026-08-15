# docs/internal/archive/

Closed / resolved internal docs, kept for provenance. A doc lands here once its
work is done and it is no longer a live reference — the same policy
[`docs/internal/results/README.md`](../results/README.md) already applies to
that subdirectory, and the sibling `aikit` repo uses the same `archive/`
convention. Moving (not deleting) preserves the historical record + git
history; update inbound links when you archive something (the
`internal/buildchecks` doc-link test enforces this).

Current contents:
- `csharp-oom-root-cause.md` — gotreesitter C# OOM root cause (resolved in gotreesitter v0.20.2; C# shipped).

Archival candidate not yet moved: `../road-to-1.0.md` (the ✅-closed v1.0
tracker) — it carries ~15 outbound relative links that all shift a directory
level on the move, so it's left in place until those are updated in one pass.
