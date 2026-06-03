# Recommended Claude-Code-with-ken workflow

ken and Claude Code overlap at one obvious seam: ken is the MCP server
your agent talks to for code search and structural navigation, and
Claude Code is the agent. This doc is the recommended way to wire the
two together day-to-day, plus when to reach for Claude Code's
heavier-weight skills (`ultrareview`, `ultracode`) on top.

For the agent-user reference of ken's nine MCP tools and the
"when do I use ken vs grep" decision matrix, see
[USERS.md](USERS.md). This doc assumes you've read that one and
wants the *workflow* layer above it.

## Setup (one line)

```bash
claude mcp add ken -s user -- /absolute/path/to/ken-mcp
```

That's the whole thing. Per-repo routing tweaks (the optional
`CLAUDE.md` block telling the agent when to prefer grep over ken on
this specific repo) are documented in
[README.md → Tuning ken's routing for your repo](../README.md#tuning-kens-routing-for-your-repo);
skip until a specific repo's results convince you it matters.

## Layer 0 — Just ken (the routine case)

90% of Claude Code sessions need nothing past the install. The
default agent instructions ken-mcp ships with
([`mcp/server.go`](../mcp/server.go)) already tell the agent:

- Use ken for conceptual queries ("where do we handle X?"),
  locating definitions, and "show me the surface of this area"
  explorations.
- Fall back to native grep / file-search for refactors, renames,
  or any operation that must be exhaustive — grep gives 100%
  recall on literal matches; ken's hybrid search optimizes for
  relevance, not completeness.

You don't need to repeat this guidance in your prompts; it's already
embedded in `tools/list` and Claude Code reads it on session start.

**When this is the right layer:** routine day-to-day coding — the
agent finds the right file via `search`, opens it, edits it, runs
tests. Most pull-request-shaped work fits here.

## Layer 1 — Add `ultrareview` before merging substantial changes

[`ultrareview`](https://code.claude.com/docs/en/ultrareview.md) is a
Claude Code skill (not a ken feature) that runs a multi-agent
cloud-side code review. Each finding is reproduced and confirmed by
an independent verifier agent before it's reported — the
adversarial-verify pattern, automated. Higher signal than the local
`/review` slash command at the cost of 5–10 minutes wall time and
real money.

**How to invoke:**

```
/code-review ultra          # reviews the current branch's diff vs main
/code-review ultra <PR#>    # reviews a specific GitHub PR
```

Or non-interactively for CI / scripts:

```bash
claude ultrareview [PR#]
```

**When this is the right layer:** before merging anything that
touches a load-bearing surface — a new MCP tool, an extractor across
multiple languages, a perf-claim commit, an ADR-level decision. Skip
for typo fixes, doc edits, and small refactors where the local
`/review` is enough.

**What it costs (per Claude Code docs, current at time of writing):**
3 free runs per Pro/Max account one-time, then ~$5–$20 per review.
Team/Enterprise bill immediately to usage credits. Billing starts
when the cloud session begins, so don't kick one off speculatively.

**The dogfood path the road-to-1.0 doc calls out:** run `/code-review
ultra` on your ken PRs. It exercises the real codepath — ken-mcp
indexing ken's own repo while a Claude Code fleet asks questions
about the diff. If `search` results lead the reviewer to the wrong
chunk, that's a real-world ken bug surfacing in the natural way.

## Layer 2 — `ultracode` for ambitious sessions

[`ultracode`](https://code.claude.com/docs/en/workflows) (Claude
Code v2.1.154+, opt in via `/effort ultracode` or by including the
word "ultracode" in a single prompt) combines `xhigh` reasoning
effort with automatic multi-agent workflow orchestration. Claude
decides on its own when a task is workflow-shaped and fans out
subagents in parallel — codebase audits, large migrations,
multi-angle research.

**When this is the right layer:** the task is genuinely big and
parallelizable. Three concrete shapes that fit:

- **Codebase audit.** "Audit every place ken does X and tell me where
  the convention drifted." The audit fans out per package; each
  subagent uses ken's `search` / `outline` / `symbols` to map its
  slice.
- **Large coordinated migration.** "Rename `Foo` to `Bar` across the
  repo, fix call sites, update docstrings, fix the changelog." ken's
  `callers` + `references` find the impact surface; the subagents
  do the edits in parallel after the discovery phase.
- **Cross-cutting research.** "What's the actual ranking quality
  story across all the bench harnesses? Pull the numbers and
  cross-check." Each bench harness gets its own subagent; the
  synthesis is the headline.

**When this is NOT the right layer:** routine work, single-file
edits, debugging a specific error. Ultracode costs significantly
more tokens than `/effort high` — Claude Code's own guidance is
"run on a small slice first to gauge spend." The shape that pays off
is "lots of independent work that benefits from parallel agents,"
not "I want better answers on one question."

## How the layers compose

You don't pick one. The natural progression on a real PR:

1. **Layer 0 the whole way through development.** ken finds files,
   you (Claude Code) edit them, tests run. Hundreds of `search` /
   `outline` calls; no extra cost beyond your normal session.
2. **Before pushing, `/review` (Claude Code's local skill).** Fast
   feedback in seconds-to-minutes. Most diffs land here.
3. **For PRs that are load-bearing, also `/code-review ultra`.** Pay
   the cloud-fleet cost only on diffs where you'd actually act on a
   higher-confidence verdict.
4. **Ultracode is sideways from this loop.** Reach for it when the
   task itself is "do a lot of independent things in parallel," not
   on the merge gate. If you find yourself enabling `/effort
   ultracode` for routine work, the cost will surprise you.

## What this is NOT

- **ken does not run ultracode or ultrareview itself.** The
  framing in road-to-1.0 ("the easiest dogfood path") means Claude
  Code uses ken's MCP tools while running those skills; ken-mcp
  doesn't know or care which skill is invoking it.
- **You don't need a ken-specific config to use the skills.** Once
  ken-mcp is registered as an MCP server, every Claude Code skill
  that does code reasoning will use it transparently.
- **ken doesn't expose its own review or audit tool.** If you want
  a verified-finding review pass, `/code-review ultra` is the route.
  ken's structural tools (`callers`, `references`, `outline`) are
  retrieval primitives, not review verdicts.
- **The MCP install snippet stays the same across providers.** The
  `claude mcp add ken …` line in [USERS.md](USERS.md) works for
  Pro / Max / Team / Enterprise on any provider Claude Code supports.

## When to reach past this loop

Two cases where ken's tools aren't the right hammer and the workflow
is "fall back to grep":

- **Refactors / renames** — when you need every literal occurrence of
  a name, ken's relevance-optimized ranking is the wrong primitive.
  `grep -n "OldName"` gives you the exhaustive list.
- **Pre-rename impact audits** — same reason. ken's `callers` returns
  file-level adjacency today; for guaranteed completeness, grep wins.
  (See [structural-call-graph-plan.md](structural-call-graph-plan.md)
  for the deferred function-level upgrade and the MCP-log evidence
  that would trigger shipping it.)

The default agent instructions ken-mcp ships with already direct
Claude Code to this fallback; you don't need to coach the agent.

## Quick reference

| Want to … | Use |
| --- | --- |
| Find code that does X | ken `search` |
| Find code semantically similar to Y | ken `find_related` |
| Locate `Foo`'s definition | ken `definition` |
| Find references to `Foo` (file-level, ranked) | ken `references` |
| Find files calling `Foo` (file-level) | ken `callers` |
| See the surface of a file or directory | ken `outline` |
| List top-level symbols | ken `symbols` |
| Health-check / token-savings report | ken `status` |
| Last N commits + files touched | ken `recently_changed` |
| Every literal match of a string | native grep |
| Quick local review of an unpushed diff | Claude Code `/review` |
| Verified-finding cloud review of a PR | Claude Code `/code-review ultra` |
| Parallelized audit / migration / sweep | Claude Code ultracode |

---

References:

- ken's [USERS.md](USERS.md) — install + ken-vs-grep decision matrix + nine-tool reference
- ken's [README.md → Tuning ken's routing](../README.md#tuning-kens-routing-for-your-repo) — optional per-repo `CLAUDE.md` guidance
- Claude Code's [Ultrareview docs](https://code.claude.com/docs/en/ultrareview.md)
- Claude Code's [Dynamic Workflows / ultracode docs](https://code.claude.com/docs/en/workflows)
