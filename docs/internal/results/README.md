# results — measurement & gate memos

Point-in-time measurement memos that shipped claims elsewhere in the docs
cite as evidence. Moved here — tracked **deliberately** — from the gitignored
`outputs/` scratch dir, where they'd been committed before the ignore rule
(roadmap #11).

- **`perf-startup-m{0,1,2,4}-*.md`** — the v0.9.0 / ADR-036 startup +
  query-latency perf campaign: M0 cold-start baselines, then the M1/M2/M4
  milestone results. Cited by [docs/PERF-expectations.md](../../PERF-expectations.md),
  [docs/DEVELOPERS.md](../../DEVELOPERS.md), [road-to-1.0.md](../road-to-1.0.md),
  and [DECISIONS.md](../DECISIONS.md) (ADR-036).
- **`stage8-*.md`** — Stage 8 gate results: the Arm B CoSQA NDCG gate
  (gate-1), the `callers` call-edge precision sample (gate-2), the parked
  ColBERT/MaxSim probe, and query-graph expansion.

These are **archival** — they record what was measured at the time and are not
updated. `outputs/` itself stays gitignored for live scratch work; only these
load-bearing memos are promoted into version control.
