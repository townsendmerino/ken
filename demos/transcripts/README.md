# Demo transcripts — provenance + staleness note

## What's here

Captured agent conversations against the **`demos/v0.1.0`** binaries
(2026-05-27). Three questions per codebase, kubernetes (one config) and
postgres (regex vs treesitter A/B). Each transcript names the ken commit
SHA and the binary config in its header line. Graded against
[`transcript-audit-rubric.md`](../transcript-audit-rubric.md).

## Staleness as of `demos/v0.2.0` (2026-06-03)

The `demos/v0.2.0` binaries are built against newer corpus revisions:

- **kubernetes:** v1.31.0 → **v1.36.0** (five minor versions of upstream
  drift)
- **postgres:** REL_17_0 → **REL_18_0** (one major version of upstream
  drift)
- **go-stdlib (new):** no prior corpus

Specific `path:line` citations in the transcripts here may not resolve
to the same lines (or, for refactored code, the same files) when the
demo's `search`/`find_related`/etc. are re-run against the
`demos/v0.2.0` binaries. The *query types* still demonstrate what ken
does — file-level granularity is preserved — but reproducing each
transcript's exact tool-call ladder against `demos/v0.2.0` will surface
drift.

Refreshing these transcripts against `demos/v0.2.0` is **separate
follow-on work**, not gated on the binary release. The transcripts here
stay as the published `demos/v0.1.0` evidence record; a parallel set
against `demos/v0.2.0` would land in a future commit if/when captured.

## If you re-run these against v0.2.0

The query prompts are reproducible:

- `kubernetes-q1-hpa.md` — "How does HorizontalPodAutoscaler decide
  when to scale a deployment?"
- `kubernetes-q2-eviction.md` — "How does the kubelet evict pods?"
- `kubernetes-q3-scheduler-extenders.md` — "How do scheduler extenders
  work?"
- `postgres-{regex,treesitter}-q1-join.md` — "How does PostgreSQL
  execute joins?"
- `postgres-{regex,treesitter}-q2-wal-checkpoint.md` — "How does the
  WAL checkpoint process work?"
- `postgres-{regex,treesitter}-q3-autovacuum.md` — "How does autovacuum
  decide which tables to vacuum?"

The same six questions against `demos/v0.2.0` would produce a
companion set graded against the same audit rubric, comparable to the
`demos/v0.1.0` record below.

## Files in this directory

| file | corpus pin | ken commit |
|---|---|---|
| `kubernetes-q1-hpa.md` | k8s v1.31.0 (`demos/v0.1.0`) | post-#31 (per the transcript header) |
| `kubernetes-q2-eviction.md` | k8s v1.31.0 | post-#31 |
| `kubernetes-q3-scheduler-extenders.md` | k8s v1.31.0 | post-#31 |
| `postgres-regex-q1-join.md` | pg REL_17_0 (`demos/v0.1.0`) | post-#31 |
| `postgres-regex-q2-wal-checkpoint.md` | pg REL_17_0 | post-#31 |
| `postgres-regex-q3-autovacuum.md` | pg REL_17_0 | post-#31 |
| `postgres-treesitter-q1-join.md` | pg REL_17_0 | post-#31 |
| `postgres-treesitter-q2-wal-checkpoint.md` | pg REL_17_0 | post-#31 |
| `postgres-treesitter-q3-autovacuum.md` | pg REL_17_0 | post-#31 |

(No transcripts yet for `go-stdlib` — Phase 3 of the demo plan;
deliberately separate from the binary release per the kickoff doc.)
