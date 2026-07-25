# DRAFT — upstream note for odvcencio/gotreesitter (PHP parse throughput)

**Status: DRAFT, not filed.** This is a draft issue for the human to review and
file manually against `odvcencio/gotreesitter`. It is framed as a **real-world
ratchet data point**, not a regression complaint — we understand and agree with
the correctness trade the engine rewrite made. Do not paste ken-internal file
paths or the campaign framing when filing; the text below the line is the
proposed issue body.

---

## PHP parse throughput on real corpora — a downstream data point (ken)

Hi — maintainer of [`townsendmerino/ken`](https://github.com/townsendmerino/ken)
here (pure-Go hybrid code search; gotreesitter is our tree-sitter engine). This
is a **data point, not a bug report** — we're sharing a downstream measurement
in case it's useful for the PHP throughput/cliff backlog, and to ask whether
there's planned work we can track.

### Our use case

ken does an **index-time structural parse of every source file** to build a
small per-file enrichment label (functions / calls / imports) that feeds our
lexical + semantic retrieval. So for us, gotreesitter's **parse throughput on
typical, well-formed source** is directly on the cold-start critical path — it's
the single largest cost of indexing a repo, ahead of embedding. PHP monorepos
(Yii/Laravel-style) are a workload our users care about.

### What we measured

Building ken at two of our own release points that differ (almost) only in the
gotreesitter pin, same corpus, and timing `index` in bm25 mode (which isolates
the parse — no embedding), median of 5, with an enrichment-off control that
removes the tree-sitter parse entirely:

- Corpus: `yiisoft/yii2` + `yiisoft/yii2-app-advanced` (1,184 `.php` files).
- Host: Apple M1 Pro / 16 GB, Go 1.26.

| config | ken @ gotreesitter 0.20.5 | ken @ gotreesitter 0.47.0 |
|---|---|---|
| parse **on** | 1598 ms | 3455 ms |
| parse **off** (control) | 537 ms | 557 ms |

The control is flat across the two versions (537 ≈ 557 ms), so the entire
difference is the tree-sitter parse: **~1061 ms → ~2898 ms, ≈ 2.7× on this PHP
corpus** across the 0.20.x → 0.47.0 span.

### We understand the trade — this is not a regression report

We've read `cgo_harness/perf_scan/perf_ratio_budgets.json` and understand the
context:

- 0.20.x was fast **because it skipped C-faithful work** (it had silently
  disabled the hand-written repeat-boundary conflict resolvers for PHP, which
  caused GLR stack-forking blowups on some inputs — exactly the kind of
  correctness bug we'd rather not ship).
- The ledger records **~1.9× C tree-sitter for PHP** as the accepted
  post-engine state, and separately logs a single-file cliff row
  (`bootstrap-5.blade.php` at ~159× aggregate vs a ~1.55× median) — i.e.
  template-like files are known to be individually pathological.

We deliberately **kept the newer engine for its correctness** and are taking the
parse off our cold path on our side (lazy/async enrichment + a per-file parse
budget) rather than asking you to trade correctness back for speed. So nothing
here needs "fixing" for us to ship.

### The one question

Are the **PHP throughput / template-cliff backlog rows** in
`perf_ratio_budgets.json` attached to any planned work (an issue/milestone) we
could **subscribe to or track**? If a future PHP fast-path or cliff mitigation
lands, it would let us re-enable full-corpus enrichment on the cold path for our
PHP users, so we'd like to know when to re-measure.

### An offer

We run the exact bench above (real Yii/Laravel corpora, median-of-N, parse-on
vs parse-off control) as part of our own release process. If a **recurring
real-world PHP throughput data point** from a downstream consumer would be
useful — e.g. to sanity-check the `perf_ratio_budgets.json` ratios against
production-shaped code rather than fixtures — we're happy to contribute it on
each of your releases. Just point us at where it'd be useful.

Thanks for the engine work — the C-faithful correctness is the right call for
us; we just want to track the throughput side.
