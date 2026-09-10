# Repowise trial — ken

**Context.** This is the same repowise evaluation as the one running against goinfer, fin, and
aikit (see `aikit/docs/prompts/repowise-trial.md` if you want the full backstory) — `repowise`
is an external codebase-intelligence / health-scoring CLI (github.com/repowise-dev/repowise).
Cowork ran goinfer and fin through the Mac device-bridge sandbox and hit two problems specific to
that bridge: its mounted folder isn't compatible with repowise's SQLite backend (`disk I/O
error`), and every shell call there is capped at ~45 seconds, which repowise's slower steps
(PageRank especially) didn't reliably fit into. Neither should apply to you running locally — real
filesystem, no per-command time cap. ken wasn't attempted yet; this is a fresh trial.

**Goal.** A complete index + health report for `~/tmcode/ken`.

## One thing specific to ken

`~/tmcode/ken/go.work` is a multi-module workspace:

```
use (
    .
    ../aikit
    ../aikit/chunk/treesitter
)
```

So ken's build pulls in aikit and aikit's tree-sitter submodule as workspace members. Watch for
repowise's Go import/package resolution picking up aikit code while indexing ken — that could
either enrich the picture (real cross-repo call edges) or double-count against aikit's own
separate index. Worth noting in what you report back either way, not necessarily a problem to fix.

## Setup

Needs Python 3.11+ (system Python was 3.10 in the sandbox). Cleanest path is `uv`, no sudo:

```
uv python install 3.11
uv tool install repowise --python 3.11
```

Confirmed working: repowise 0.45.0 installs cleanly this way. `repowise --help` / `repowise init
--help` are worth a skim — more commands than the README's public description suggests (ask,
risk, dead-code, security, decision, symbol, why, context, workspace, and more).

## Run

From inside `~/tmcode/ken`:

```
repowise init --no-prose --no-editor-setup -y .
```

- `--no-prose` — structure only, no LLM key, no spend. This is the mode we want for the trial.
- `--no-editor-setup` — **keep this flag.** Without it, `init` defaults to writing Claude Code /
  Claude Desktop MCP config and hooks machine-wide, plus `.mcp.json` / `.claude/CLAUDE.md` /
  `.vscode/mcp.json` into the repo — a decision for Francis to make deliberately once he's seen
  results, not a side effect of a trial run.
- `-y` — skips a confirmation prompt so it doesn't hang non-interactively.
- If interrupted, `repowise init --resume [same flags] .` picks it back up.
- If indexing drags, add `--mode fast` (skips per-file blame / co-change / LLM docs) — try
  standard first.

Then pull the health detail with the actual worst-scoring files, not just the aggregate:

```
repowise health --format md --refactoring-targets .
```

This recomputes analysis from scratch rather than reading the just-built index, so it can take a
real minute or two — that's expected, let it run and log progress since it'll cross the
couple-minute mark.

## Two known repowise rough edges (hit while testing goinfer/fin, may or may not recur here)

- Dead-code detection crashed once with `can't subtract offset-naive and offset-aware datetimes`,
  and the run's final summary then wrongly reported a clean "0 unreachable · 0 unused exports" —
  silently masking the crash instead of surfacing it. If you see that warning anywhere in the
  output, don't trust a clean dead-code count that follows — rerun `repowise dead-code .`
  standalone and confirm it completes without the error first.
- repowise's architectural-decision miner looks for formal ADR files / PR descriptions / inline
  markers. It found zero for goinfer despite real decision history, because that history lives in
  `docs/task-*.md` + `CLAUDE.md` instead. If ken comes back with 0 decisions too, check whether
  that's a real absence or the same convention mismatch before reporting it as a finding — ken has
  `docs/internal/DECISIONS.md`, which might or might not be a shape repowise recognizes.

## Report back

File/symbol/graph counts, language breakdown, health avg + worst score + the actual worst
files/refactor candidates, dead-code and unused-export counts (confirmed real, not zeroed by the
datetime bug), architectural-decision count (with the DECISIONS.md caveat above), the aikit
workspace-overlap question, and total elapsed time. Flag anything that breaks rather than smoothing
it over.
