# Audit-finding citations — legend

Many code comments across `internal/`, `mcp/`, and `cmd/` carry a short
provenance tag like `(audit §27)`, `(R4-6)`, `(audit N4)`, or `(audit db/mcp
§1)`. Unlike an `ADR-NNN` citation — which resolves to a section of
[`DECISIONS.md`](DECISIONS.md) — these tags refer to ken's internal
**engineering-audit passes**, whose working reports were not committed to the
repo. This file is the legend so a reader hitting one of these tags knows it is
a deliberate provenance marker, not a dangling reference to a doc they are
failing to find.

The durable record of each finding is:

1. **the comment itself** — the tag annotates a line that exists *because* the
   audit found something; the rationale is in the surrounding comment, and
2. **the commit that added it** — every remediation commit names its findings in
   the subject line, so `git log` is the index.

## Citation families

| Tag form | Meaning | Where the fix landed |
|----------|---------|----------------------|
| `audit §N` | Section N of the main engineering audit (2026-07-25). Numbered findings (Highs/Mediums/Lows). | branch `audit-fixes`, merged to `main` |
| `RN-M` (e.g. `R4-6`, `R5-1`), or bare `RN` (e.g. `R10`) | Re-audit **round N**, item M — the follow-up passes that re-reviewed the round-N−1 fixes. | branches `audit-fixes-round2` … `audit-fixes-round5` |
| `audit NN` (e.g. `N4`, `N7`) | The round-3 re-audit "N-series" findings (newly-surfaced issues, largely concurrency/lifecycle nits). | branch `audit-fixes-round3` |
| `audit db/mcp §N` (also seen as `db#N`) | A focused audit of the Tier-2 DB path + the MCP server. | folded in across the round branches |

## Resolving a specific tag

The remediation commits put the tag in the subject line, so grep the log:

```bash
git log --oneline --all --grep='R4-6'          # a round-4 item
git log --oneline --all --grep='audit N4'      # an N-series finding
git log --oneline --all -i --grep='round-3 re-audit'   # a whole round
```

The matching commit's diff + message is the finding's full context.

## When adding a new audit-tagged comment

Keep the comment **self-contained**: the tag is a back-reference for
archaeology, but the line should read correctly to someone who never looks it
up. Write "fsync the parent dir so the rename survives a crash (audit §27)", not
"see audit §27" — the *why* belongs in the comment, the tag is just the
provenance stamp.
