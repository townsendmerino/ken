#!/usr/bin/env python3
"""bench/semble/run_ken.py — drive ken over semble's NDCG@10 benchmark.

semble's benchmark rig lives at github.com/MinishLab/semble in
``benchmarks/`` (annotations + repo list + metric). This script imports
semble's loaders and metric directly, launches the ken binary in its
``bench`` subcommand per repo (one process per repo, queries streamed
over stdin so the index stays warm across all queries and all latency
runs), and reports overall + per-language NDCG@10 with median latency.

Per the docs/BENCH.md procedure, the bootstrap is:

    git clone https://github.com/MinishLab/semble /path/to/semble
    cd /path/to/semble && uv sync   # or pip install -e .
    python benchmarks/sync_repos.py  # clones the corpus into ~/.cache/semble-bench/

Then, from the ken repo:

    python bench/semble/run_ken.py --mode hybrid --semble-checkout /path/to/semble

Three modes (``bm25`` / ``semantic`` / ``hybrid``) line up against semble's
published per-ablation table (0.834 / 0.821 / 0.854); diverging by more
than ±0.005 on any one row points the diagnosis at a specific subsystem
(BM25 impl, embedding/pooling, fusion+rerank).
"""
from __future__ import annotations

import argparse
import hashlib
import json
import math
import os
import statistics
import subprocess
import sys
import time
from collections import defaultdict
from dataclasses import asdict, dataclass, field
from datetime import datetime, timezone
from pathlib import Path


def _bootstrap_semble_path() -> Path:
    """Resolve --semble-checkout and prepend it to sys.path before importing semble's benchmarks package."""
    parser = argparse.ArgumentParser(add_help=False)
    parser.add_argument("--semble-checkout", default=os.environ.get("SEMBLE_CHECKOUT", "/tmp/semble"))
    known, _ = parser.parse_known_args()
    root = Path(known.semble_checkout).expanduser().resolve()
    if not (root / "benchmarks" / "data.py").exists():
        sys.exit(
            f"semble checkout not found at {root}\n"
            "  pass --semble-checkout PATH or set SEMBLE_CHECKOUT.\n"
            "  bootstrap: git clone https://github.com/MinishLab/semble PATH"
        )
    sys.path.insert(0, str(root))
    return root


_SEMBLE_ROOT = _bootstrap_semble_path()


def _stub_semble_types() -> None:
    """semble's benchmarks.metrics imports `semble.types.SearchResult` for
    type annotations only — Python doesn't enforce it at runtime. We
    inject a minimal stub so importing metrics doesn't pull in semble's
    full runtime (model2vec, vicinity, …); the duck-typed _ResultShim
    objects we hand to target_rank still satisfy the call sites."""
    import types as _types

    sm = _types.ModuleType("semble")
    smt = _types.ModuleType("semble.types")

    class SearchResult:  # noqa: D401 — placeholder
        """Type-annotation placeholder; see _stub_semble_types."""

    smt.SearchResult = SearchResult
    sys.modules.setdefault("semble", sm)
    sys.modules.setdefault("semble.types", smt)


_stub_semble_types()

from benchmarks.data import (  # type: ignore[import-not-found]  # noqa: E402
    RepoSpec,
    Task,
    available_repo_specs,
    grouped_tasks,
    load_tasks,
)
from benchmarks.metrics import ndcg_at_k, target_rank  # type: ignore[import-not-found]  # noqa: E402


# ──────────────────────────────────────────────────────────────────────────
# Shims so we can hand parsed JSON records to semble's metric helpers,
# which expect objects with ``chunk.file_path`` / ``chunk.start_line`` /
# ``chunk.end_line``. Mirrors the minimum surface of semble's SearchResult
# without dragging in the semble Python package itself.
# ──────────────────────────────────────────────────────────────────────────


@dataclass(frozen=True)
class _ChunkShim:
    file_path: str
    start_line: int
    end_line: int


@dataclass(frozen=True)
class _ResultShim:
    chunk: _ChunkShim


def _shim(records: list[dict]) -> list[_ResultShim]:
    return [
        _ResultShim(
            chunk=_ChunkShim(
                file_path=r["file_path"],
                start_line=r["start_line"],
                end_line=r["end_line"],
            )
        )
        for r in records
    ]


# ──────────────────────────────────────────────────────────────────────────
# Per-repo runner
# ──────────────────────────────────────────────────────────────────────────


@dataclass
class RepoOutcome:
    repo: str
    language: str
    n_tasks: int
    ndcg10: float
    p50_ms: float
    by_category: dict[str, float] = field(default_factory=dict)


def run_repo(
    *,
    ken_bin: str,
    spec: RepoSpec,
    tasks: list[Task],
    mode: str,
    chunker: str,
    model_dir: Path | None,
    top_k: int,
    latency_runs: int,
    verbose: bool,
    rerank_model: Path | None = None,
    rerank_top_n: int | None = None,
    rerank_beta: float | None = None,
    alpha_symbol: float | None = None,
    alpha_nl: float | None = None,
    alpha_pairs: list[str] | None = None,
    per_task: list[dict] | None = None,
) -> RepoOutcome:
    """Run all queries for one repo through ken bench; return per-task NDCG@10 + median p50."""
    # Send each query latency_runs times so we can take the median of warm-
    # index timings (semble methodology). The index is built once and
    # reused for every query and every run, the same way semble's own
    # baselines build SembleIndex once per repo and then loop queries.
    stdin_lines: list[str] = []
    for task in tasks:
        for _ in range(latency_runs):
            stdin_lines.append(task.query)
    stdin_text = "\n".join(stdin_lines) + "\n"

    args = [
        ken_bin,
        "bench",
        str(spec.benchmark_dir),
        "--mode",
        mode,
        "--chunker",
        chunker,
        "-k",
        str(top_k),
    ]
    if model_dir is not None:
        args.extend(["--model", str(model_dir)])
    # M5/M6: forward the rerank flags to `ken bench` when --mode=hybrid-rerank.
    # Pass-through only when set; the Go side defaults match plan/M0 (top_n=50,
    # β=0.25). For an M0 CoIR-style sweep use --rerank-beta=1.0 explicitly.
    if rerank_model is not None:
        args.extend(["--rerank-model", str(rerank_model)])
    if rerank_top_n is not None:
        args.extend(["--rerank-top-n", str(rerank_top_n)])
    if rerank_beta is not None:
        args.extend(["--rerank-beta", str(rerank_beta)])
    # α pinning for the sensitivity sweep. Unset ⇒ ken uses its shipped
    # adaptive constants, which is every non-sweep run.
    if alpha_symbol is not None:
        args.extend(["--alpha-symbol", str(alpha_symbol)])
    if alpha_nl is not None:
        args.extend(["--alpha-nl", str(alpha_nl)])
    # α is a fusion input, so every pair can be scored against ONE index
    # build. Passing the whole grid here instead of re-invoking ken per
    # α is the difference between 13 passes over the corpus and 2.
    if alpha_pairs:
        args.extend(["--alpha-pairs", ",".join(alpha_pairs)])

    proc = subprocess.run(
        args,
        input=stdin_text,
        capture_output=True,
        text=True,
        check=False,
    )
    if proc.returncode != 0:
        sys.stderr.write(f"  ken bench failed for {spec.name}: returncode={proc.returncode}\n")
        if proc.stderr:
            sys.stderr.write("  --- stderr ---\n" + proc.stderr + "  --------------\n")
        return RepoOutcome(repo=spec.name, language=spec.language, n_tasks=len(tasks), ndcg10=0.0, p50_ms=0.0)

    records: list[dict] = []
    for line in proc.stdout.splitlines():
        if not line.strip():
            continue
        records.append(json.loads(line))

    # With --alpha-pairs ken emits one record per (query, pair), so the
    # expected count scales with the number of pairs.
    expected = len(stdin_lines) * max(1, len(alpha_pairs or []))
    if len(records) != expected:
        sys.stderr.write(
            f"  warn: {spec.name}: expected {expected} records, got {len(records)}\n"
        )

    # With --alpha-pairs, ken emits one record per (query, pair) rather
    # than one per query, so a "run" of a task is latency_runs ×
    # len(alpha_pairs) consecutive records. The per-pair rows go to
    # per_task (which is what the α sweep aggregates); the RepoOutcome
    # NDCG below is computed from the FIRST pair only, so a plain run —
    # where there is exactly one pair — is unchanged.
    pairs_per_query = max(1, len(alpha_pairs or []))
    stride = latency_runs * pairs_per_query

    ndcg10_sum = 0.0
    median_latencies: list[float] = []
    category_ndcg10: dict[str, list[float]] = defaultdict(list)

    for i, task in enumerate(tasks):
        block = records[i * stride : (i + 1) * stride]
        if not block:
            continue
        # Records within a block cycle pair-major per latency run; take
        # every pairs_per_query-th entry to recover one pair's runs.
        runs = block[0::pairs_per_query]
        # Same query, same index ⇒ deterministic results; any run will do
        # for NDCG. Take run 0 for results and median over all runs for ms.
        results = _shim(runs[0]["results"])
        median_latencies.append(statistics.median(r.get("query_ms", 0.0) for r in runs))

        relevant_ranks = [
            rank for t in task.all_relevant if (rank := target_rank(results, t)) is not None
        ]
        n_relevant = len(task.all_relevant)
        q_ndcg = ndcg_at_k(relevant_ranks, n_relevant, top_k)
        ndcg10_sum += q_ndcg
        category_ndcg10[task.category or "unknown"].append(q_ndcg)

        # per_task, when the caller supplies a list, collects the
        # query-level detail the α sweep aggregates on: NDCG, whether
        # any relevant chunk landed in the top-k (recall@k), and the
        # query's class as ken's own classifier judged it. The class
        # comes from the ken record rather than a Python copy of
        # semble's _SYMBOL_QUERY_RE — it must be the same verdict that
        # selected the α under test.
        if per_task is not None:
            # One row per α pair. The first latency run of each pair is
            # representative: same query, same index ⇒ deterministic.
            for offset in range(pairs_per_query):
                rec = block[offset]
                pair_results = _shim(rec["results"])
                pair_ranks = [
                    rank for t in task.all_relevant
                    if (rank := target_rank(pair_results, t)) is not None
                ]
                per_task.append({
                    "repo": spec.name,
                    "language": spec.language,
                    "query": task.query,
                    "category": task.category or "unknown",
                    "symbol_query": bool(rec.get("symbol_query", False)),
                    "alpha": _pair_label(rec),
                    "ndcg10": ndcg_at_k(pair_ranks, n_relevant, top_k),
                    "recall": any(1 <= r <= top_k for r in pair_ranks),
                })

        if verbose:
            sys.stderr.write(
                f"    ndcg@10={q_ndcg:.3f}  ranks={relevant_ranks}  "
                f"n_rel={n_relevant}  q={task.query!r}\n"
            )

    n_tasks = len(tasks)
    by_category = {cat: round(sum(v) / len(v), 4) for cat, v in sorted(category_ndcg10.items())}
    return RepoOutcome(
        repo=spec.name,
        language=spec.language,
        n_tasks=n_tasks,
        ndcg10=ndcg10_sum / n_tasks if n_tasks else 0.0,
        p50_ms=statistics.median(median_latencies) if median_latencies else 0.0,
        by_category=by_category,
    )


# ──────────────────────────────────────────────────────────────────────────
# Top-level driver
# ──────────────────────────────────────────────────────────────────────────


# ──────────────────────────────────────────────────────────────────────────
# α-sensitivity sweep (docs/internal/rag-thread-followups.md item 1).
#
# Claim under test: the inherited fusion weights (α_symbol=0.3, α_NL=0.5)
# are near-optimal, and RRF is flat around the middle anyway. A sweep is
# only evidence if it's tuned and reported on DIFFERENT data, so:
#
#   1. Split by REPO, not by query. Queries from one repo share an index,
#      so a per-query split would leak corpus statistics across the
#      boundary and flatter the tuned α.
#   2. Sweep α on the tune half only, per query class.
#   3. Take the per-class argmax and evaluate that ONE pair on the
#      holdout half, against the (0.3, 0.5) baseline on the same half.
#
# The parity constraint stands regardless of the outcome: α=(0.3, 0.5)
# remains the reference the ken-vs-semble comparison is run at, per
# docs/BENCH.md's "don't tune ken's constants" rule. A tuned pair is a
# labelled experiment, not a new default — that would need its own ADR.
#
# One run per α value scores BOTH curves. α_symbol only affects symbol
# queries and α_NL only affects NL queries, so a run pinned at (a, a)
# yields the symbol-class point at `a` from its symbol queries and the
# NL-class point at `a` from its NL queries. 11 runs, not 22.
#
# Aggregation is a QUERY-level mean within a half, not the per-repo mean
# the headline table uses: the class slices have very uneven per-repo
# counts, and a 3-query repo shouldn't weigh as much as a 40-query one.
# Sweep numbers are therefore comparable within the sweep, not against
# the published per-language table.
# ──────────────────────────────────────────────────────────────────────────

ALPHA_GRID = tuple(round(0.1 * i, 1) for i in range(11))  # 0.0 … 1.0

# Materiality threshold, NOT a noise floor: ken's retrieval is
# deterministic, so re-running the same α on the same corpus reproduces
# the same NDCG exactly — there is no run-to-run jitter to average out.
# The uncertainty that matters is SAMPLING: which repos landed in the
# holdout half and which queries they carry. 0.005 is the tightest
# agreement band docs/BENCH.md reports anywhere (per-language Python vs
# semble), so a delta under it is not worth acting on regardless of
# sign. The paired standard error computed below is the statistic that
# actually says whether a delta is distinguishable from zero — and on a
# few-hundred-query holdout it is typically LARGER than this threshold,
# which is the point.
ALPHA_NOISE_FLOOR = 0.005


def _split_repos(repo_names: list[str], tune_fraction: float = 0.6) -> tuple[list[str], list[str]]:
    """Split repo names into (tune, holdout), deterministically and stratified.

    Deterministic: ordering is by md5 of the repo name, so the split is
    reproducible across machines and runs and doesn't depend on dict or
    filesystem order. Not `hash()`, which is salted per process.

    Stratification happens in the caller, which groups by language before
    calling this — the goal is both halves keeping a similar mix rather
    than an exact ratio, so each stratum is split at the same fraction
    and the remainders fall where the hash order puts them.
    """
    ordered = sorted(repo_names, key=lambda n: hashlib.md5(n.encode()).hexdigest())
    cut = round(len(ordered) * tune_fraction)
    # With a single-repo stratum, round() can send it entirely to one
    # side; keep at least one repo per side whenever there are ≥2.
    if len(ordered) >= 2:
        cut = min(max(cut, 1), len(ordered) - 1)
    return ordered[:cut], ordered[cut:]


def _stratified_split(
    grouped: dict[str, list[Task]], repo_specs: dict[str, RepoSpec], tune_fraction: float = 0.6
) -> tuple[list[str], list[str]]:
    """Split repos into tune/holdout, stratified by language.

    Language is the stratum rather than the symbol/NL mix directly: query
    class correlates strongly with language (a Rust repo's annotations
    skew differently from a Python one's), and language is knowable
    before any ken run. The realized symbol/NL balance of both halves is
    reported next to the split so a bad draw is visible rather than
    silent.
    """
    by_language: dict[str, list[str]] = defaultdict(list)
    for repo_name in grouped:
        by_language[repo_specs[repo_name].language].append(repo_name)
    tune: list[str] = []
    holdout: list[str] = []
    for language in sorted(by_language):
        t, h = _split_repos(by_language[language], tune_fraction)
        tune.extend(t)
        holdout.extend(h)
    return sorted(tune), sorted(holdout)


def _pair_label(rec: dict) -> str:
    """Label the α pair a ken bench record was produced under.

    "adaptive" is the shipped per-class behavior; otherwise "sym:nl".
    Absent fields mean a single-pair run, which is adaptive unless the
    caller pinned it — and the sweep never does that via this path.
    """
    sym = rec.get("alpha_symbol", "adaptive")
    nl = rec.get("alpha_nl", "adaptive")
    if sym == "adaptive" and nl == "adaptive":
        return "adaptive"
    # Canonicalize through float: Go formats 0.0 as "0" and Python as
    # "0.0", so the raw strings don't compare equal even though the
    # values do. Round-tripping both sides through float makes the
    # label a reliable dict key.
    return f"{float(sym)}:{float(nl)}"


def _run_half(
    *,
    repo_names: list[str],
    grouped: dict[str, list[Task]],
    repo_specs: dict[str, RepoSpec],
    args: argparse.Namespace,
    model_dir: Path | None,
    alpha_pairs: list[str],
) -> dict[str, list[dict]]:
    """Score every query in one half at every α pair, in ONE pass.

    Returns {pair_label: rows}. The whole point is that each repo's index
    is built once and every α pair is fused against it — α participates
    only in the fusion, so rebuilding per pair measured nothing and cost
    ~11 minutes a point on the 63-repo corpus.

    A side benefit beyond speed: every pair sees the byte-identical
    index, so a curve can't pick up drift from two different builds.
    """
    rows: list[dict] = []
    for repo_name in repo_names:
        run_repo(
            ken_bin=args.ken,
            spec=repo_specs[repo_name],
            tasks=grouped[repo_name],
            mode=args.mode,
            chunker=args.chunker,
            model_dir=model_dir,
            top_k=args.top_k,
            # Latency is not part of this experiment; running each query
            # 5× would multiply the pass for nothing.
            latency_runs=1,
            verbose=False,
            alpha_pairs=alpha_pairs,
            per_task=rows,
        )
    by_pair: dict[str, list[dict]] = defaultdict(list)
    for row in rows:
        by_pair[row["alpha"]].append(row)
    missing = [p for p in alpha_pairs if p not in by_pair]
    if missing:
        sys.exit(f"ken bench returned no records for α pair(s) {missing} — record/pair alignment is off.")
    return by_pair


def _paired_delta(tuned_rows: list[dict], baseline_rows: list[dict]) -> dict[str, dict[str, float]]:
    """Paired per-query NDCG difference (tuned − baseline), per class.

    Both arms score the SAME queries, so the paired difference is the
    right statistic: it cancels the between-query variance that
    dominates an unpaired comparison (queries differ enormously in
    difficulty; α does not move most of them at all). Reported as the
    mean difference with its standard error, plus how many queries α
    actually moved — on a flat curve most pairs are exactly 0, and a
    mean over mostly-zeros with a couple of large movers is a very
    different claim from a broad shift.

    Rows are paired on (repo, query). A query present in only one arm is
    dropped, which shouldn't happen — both arms run the same repo list —
    but silently averaging an unpaired set would be worse than losing it.
    """
    def key(r: dict) -> tuple[str, str]:
        return (r["repo"], r["query"])

    baseline_by_key = {key(r): r for r in baseline_rows}
    out: dict[str, dict[str, float]] = {}
    buckets: dict[str, list[float]] = {"symbol": [], "nl": [], "overall": []}
    for row in tuned_rows:
        base = baseline_by_key.get(key(row))
        if base is None:
            continue
        d = row["ndcg10"] - base["ndcg10"]
        buckets["overall"].append(d)
        buckets["symbol" if row["symbol_query"] else "nl"].append(d)

    for name, diffs in buckets.items():
        n = len(diffs)
        if n == 0:
            out[name] = {"n": 0, "mean": 0.0, "stderr": 0.0, "moved": 0, "t": 0.0}
            continue
        mean = sum(diffs) / n
        if n > 1:
            var = sum((d - mean) ** 2 for d in diffs) / (n - 1)
            stderr = math.sqrt(var / n)
        else:
            stderr = 0.0
        out[name] = {
            "n": n,
            "mean": mean,
            "stderr": stderr,
            # How many queries α moved at all — the denominator that
            # makes a small mean interpretable.
            "moved": sum(1 for d in diffs if abs(d) > 1e-9),
            # Paired t. |t| < 2 ⇒ indistinguishable from zero at the
            # usual bar; reported rather than turned into a p-value,
            # since the queries are not an i.i.d. sample of anything.
            "t": mean / stderr if stderr > 0 else 0.0,
        }
    return out


def _class_means(rows: list[dict]) -> dict[str, dict[str, float]]:
    """Mean NDCG@10 + recall@k per query class, plus the overall row."""
    out: dict[str, dict[str, float]] = {}
    buckets = {
        "symbol": [r for r in rows if r["symbol_query"]],
        "nl": [r for r in rows if not r["symbol_query"]],
        "overall": rows,
    }
    for name, bucket in buckets.items():
        if not bucket:
            out[name] = {"n": 0, "ndcg10": 0.0, "recall": 0.0}
            continue
        out[name] = {
            "n": len(bucket),
            "ndcg10": sum(r["ndcg10"] for r in bucket) / len(bucket),
            "recall": sum(1.0 for r in bucket if r["recall"]) / len(bucket),
        }
    return out


def run_alpha_sweep(args: argparse.Namespace, model_dir: Path | None, grouped: dict[str, list[Task]],
                    repo_specs: dict[str, RepoSpec]) -> dict:
    """Tune α on one repo half, evaluate the winner once on the other."""
    if args.mode == "bm25":
        sys.exit("--alpha-sweep needs a fusion mode; α does nothing under --mode=bm25.")

    tune, holdout = _stratified_split(grouped, repo_specs, args.tune_fraction)
    if not tune or not holdout:
        sys.exit(f"split produced an empty half (tune={len(tune)} holdout={len(holdout)}) — need ≥2 repos.")

    n_tune_q = sum(len(grouped[r]) for r in tune)
    n_hold_q = sum(len(grouped[r]) for r in holdout)
    sys.stderr.write(
        f"α sweep: {len(tune)} tune repos ({n_tune_q} queries) / "
        f"{len(holdout)} holdout repos ({n_hold_q} queries)\n"
        f"  tune:    {', '.join(tune)}\n"
        f"  holdout: {', '.join(holdout)}\n\n"
    )

    # ── Sweep on the tune half ────────────────────────────────────────
    #
    # Skippable via --alpha-argmax: the tune sweep is 11 full passes and
    # the holdout evaluation is 2, so re-checking a known pair (or
    # recomputing a statistic that needs the per-query rows) shouldn't
    # cost two hours. The split is deterministic, so the holdout half is
    # identical either way.
    if args.alpha_argmax is not None:
        best = {"symbol": args.alpha_argmax[0], "nl": args.alpha_argmax[1]}
        curves = {"symbol": [], "nl": []}
        saturated: list[str] = []
        sys.stderr.write(
            f"skipping the tune sweep (--alpha-argmax): evaluating "
            f"α_symbol={best['symbol']} α_NL={best['nl']} on the holdout half only\n\n"
        )
        return _alpha_holdout(args, model_dir, grouped, repo_specs, tune, holdout,
                              n_tune_q, n_hold_q, best, curves, saturated)

    curves: dict[str, list[dict]] = {"symbol": [], "nl": []}
    sys.stderr.write(f"sweeping {len(ALPHA_GRID)} α values in one pass over the tune half...\n")
    grid_labels = [f"{a}:{a}" for a in ALPHA_GRID]
    by_pair = _run_half(
        repo_names=tune, grouped=grouped, repo_specs=repo_specs, args=args,
        model_dir=model_dir, alpha_pairs=grid_labels,
    )
    sys.stderr.write(f"\n{'α':>5}  {'symbol NDCG':>12} {'n':>5}  {'NL NDCG':>9} {'n':>5}\n")
    sys.stderr.write(f"{'-' * 5}  {'-' * 12} {'-' * 5}  {'-' * 9} {'-' * 5}\n")
    for a, label in zip(ALPHA_GRID, grid_labels):
        means = _class_means(by_pair[label])
        for cls in ("symbol", "nl"):
            curves[cls].append({"alpha": a, **means[cls]})
        sys.stderr.write(
            f"{a:>5.1f}  {means['symbol']['ndcg10']:>12.4f} {means['symbol']['n']:>5}  "
            f"{means['nl']['ndcg10']:>9.4f} {means['nl']['n']:>5}\n"
        )

    shipped = {"symbol": 0.3, "nl": 0.5}
    best = {}
    for cls in ("symbol", "nl"):
        if not any(p["n"] for p in curves[cls]):
            sys.exit(f"tune half has no {cls}-class queries — the split is unusable for this sweep.")
        # Ties go to the α nearest the shipped constant. This matters more
        # than it looks: the symbol class saturates at NDCG 1.0 on some
        # splits, and an every-point-equal curve otherwise hands the
        # argmax to whichever extreme the sort visited first — reporting
        # α=0.0 as "tuned" on the strength of no evidence at all. A tie is
        # not a reason to move off the default.
        best[cls] = max(
            curves[cls], key=lambda p: (p["ndcg10"], -abs(p["alpha"] - shipped[cls]))
        )["alpha"]
    saturated = [cls for cls in ("symbol", "nl")
                 if len({round(p["ndcg10"], 4) for p in curves[cls]}) == 1]
    sys.stderr.write(f"\ntune argmax: α_symbol={best['symbol']} α_NL={best['nl']}\n")
    for cls in saturated:
        sys.stderr.write(
            f"  note: the {cls} curve is flat to 4dp across the whole grid — α is inert for "
            f"this class on the tune half, and the argmax is the shipped {shipped[cls]} by tie-break.\n"
        )
    sys.stderr.write("\n")

    return _alpha_holdout(args, model_dir, grouped, repo_specs, tune, holdout,
                          n_tune_q, n_hold_q, best, curves, saturated)


def _alpha_holdout(args, model_dir, grouped, repo_specs, tune, holdout,
                   n_tune_q, n_hold_q, best, curves, saturated) -> dict:
    """Evaluate one α pair against the shipped baseline on the holdout half.

    Split out of run_alpha_sweep so --alpha-argmax can reach it without
    the 11-pass tune sweep. Nothing here reads the tune half — that is
    the point of the experiment.
    """
    # ── Single holdout evaluation: tuned pair vs the shipped baseline ──
    #
    # Both arms in one pass, against the same index build. The baseline
    # arm runs α UNPINNED rather than pinned to (0.3, 0.5): that
    # exercises the shipped adaptive path itself, so the comparison
    # can't be corrupted by the pinning plumbing.
    sys.stderr.write("holdout evaluation (no tuning happens here)\n")
    tuned_label = f"{best['symbol']}:{best['nl']}"
    if tuned_label == "0.3:0.5":
        # The tuned pair IS the shipped pair. Asking ken for both would
        # be a duplicate --alpha-pairs entry; the adaptive arm already
        # answers it, and Δ is exactly zero by construction.
        by_pair = _run_half(
            repo_names=holdout, grouped=grouped, repo_specs=repo_specs, args=args,
            model_dir=model_dir, alpha_pairs=["adaptive"],
        )
        baseline_rows = by_pair["adaptive"]
        tuned_rows = baseline_rows
        sys.stderr.write(
            "  note: the tune argmax IS the shipped pair — Δ is zero by construction, "
            "which is itself the result.\n"
        )
    else:
        by_pair = _run_half(
            repo_names=holdout, grouped=grouped, repo_specs=repo_specs, args=args,
            model_dir=model_dir, alpha_pairs=["adaptive", tuned_label],
        )
        baseline_rows = by_pair["adaptive"]
        tuned_rows = by_pair[tuned_label]
    tuned, baseline = _class_means(tuned_rows), _class_means(baseline_rows)
    paired = _paired_delta(tuned_rows, baseline_rows)

    sys.stderr.write(
        f"\n{'Class':<8} {'n':>5}  {'baseline':>9} {'tuned':>9}  {'Δ NDCG':>8} {'±SE':>7} {'t':>6} "
        f"{'moved':>6}  {'base rec':>9} {'tuned rec':>10}\n"
    )
    sys.stderr.write(
        f"{'-' * 8} {'-' * 5}  {'-' * 9} {'-' * 9}  {'-' * 8} {'-' * 7} {'-' * 6} {'-' * 6}  "
        f"{'-' * 9} {'-' * 10}\n"
    )
    for cls in ("symbol", "nl", "overall"):
        pd = paired[cls]
        sys.stderr.write(
            f"{cls:<8} {tuned[cls]['n']:>5}  {baseline[cls]['ndcg10']:>9.4f} "
            f"{tuned[cls]['ndcg10']:>9.4f}  {pd['mean']:>+8.4f} {pd['stderr']:>7.4f} "
            f"{pd['t']:>6.2f} {pd['moved']:>6}  "
            f"{baseline[cls]['recall']:>9.3f} {tuned[cls]['recall']:>10.3f}\n"
        )

    # Direction matters. A tuned pair that LOSES on held-out data is not
    # "a signal worth chasing" — it's the tune-half argmax failing to
    # transfer, which is itself evidence the curve is noise-dominated and
    # the shipped constants should stand.
    # Two independent bars: is the delta big enough to matter, and is it
    # distinguishable from zero at all? A delta can clear the first and
    # fail the second — that's the usual outcome of tuning a flat curve
    # on a few hundred queries, and reporting only the point estimate
    # would turn sampling scatter into a finding.
    overall = paired["overall"]
    delta, stderr, tstat = overall["mean"], overall["stderr"], overall["t"]
    pinned = f"α_symbol={best['symbol']}, α_NL={best['nl']}"
    significant = abs(tstat) >= 2.0
    if abs(delta) < ALPHA_NOISE_FLOOR:
        verdict = (
            f"flat — the tuned pair ({pinned}) moves holdout NDCG@10 by {delta:+.4f} "
            f"(±{stderr:.4f} SE, t={tstat:.2f}), inside the ±{ALPHA_NOISE_FLOOR} materiality "
            f"threshold and on only {overall['moved']}/{overall['n']} queries. "
            "Sweeping found nothing; keep semble's constants."
        )
    elif not significant:
        verdict = (
            f"not distinguishable from zero — Δ={delta:+.4f} clears the ±{ALPHA_NOISE_FLOOR} "
            f"threshold but not its own error bar (±{stderr:.4f} SE, t={tstat:.2f}, "
            f"{overall['moved']}/{overall['n']} queries moved). Keep semble's constants."
        )
    elif delta < 0:
        verdict = (
            f"did not transfer — the tune-half argmax ({pinned}) is {abs(delta):.4f} "
            f"±{stderr:.4f} WORSE than the shipped pair on the holdout (t={tstat:.2f}). "
            "Tuning α overfits the tune half; keep semble's constants."
        )
    else:
        verdict = (
            f"held up — the tuned pair ({pinned}) beats the shipped pair by {delta:.4f} "
            f"±{stderr:.4f} (t={tstat:.2f}) on data it was not tuned on. Changing the shipped "
            "default would still need an ADR amending the verbatim-port framing (docs/BENCH.md)."
        )
    sys.stderr.write(f"\nverdict: {verdict}\n")

    return {
        "experiment": "alpha-sensitivity",
        "shipped_alphas": {"symbol": 0.3, "nl": 0.5},
        "grid": list(ALPHA_GRID),
        "noise_floor": ALPHA_NOISE_FLOOR,
        "split": {
            "tune_fraction": args.tune_fraction,
            "stratified_by": "language",
            "order": "md5(repo_name)",
            "tune_repos": tune,
            "holdout_repos": holdout,
            "tune_queries": n_tune_q,
            "holdout_queries": n_hold_q,
        },
        "tune_curves": curves,
        "tune_argmax": best,
        "saturated_classes": saturated,
        "holdout": {
            "baseline": baseline,
            "tuned": tuned,
            # Paired per-query differences — the statistic the verdict
            # is based on. delta_ndcg10 is kept as the unpaired point
            # estimate for readers comparing against the means above.
            "paired_delta": paired,
            "delta_ndcg10": {c: tuned[c]["ndcg10"] - baseline[c]["ndcg10"]
                             for c in ("symbol", "nl", "overall")},
            # Per-query rows for both arms, so any further statistic
            # (per-language slices, a different significance bar) can be
            # computed offline without re-running the holdout.
            "per_query": {"baseline": baseline_rows, "tuned": tuned_rows},
        },
        "verdict": verdict,
    }


# ──────────────────────────────────────────────────────────────────────────
# Result provenance (docs/internal/rag-thread-followups.md item 4).
#
# Every bench result JSON records what produced it: commit, chunker,
# mode, α pair, model digest, corpus revisions, KEN_* env. Without it a
# number can't be reproduced once the codebase moves, and a regression
# is indistinguishable from a config change.
#
# The Go harnesses (bench/ndcg, bench/tokens) share
# bench/internal/provenance. This harness stays Python by design (see
# README.md — it reuses semble's own NDCG implementation), so it
# hand-builds the same block. _PROVENANCE_SCHEMA is the contract:
# bench/internal/provenance/schema_test.go reflects over the Go struct
# and fails if these paths and its json tags disagree, so the two
# can't drift apart silently. Adding a field means editing both sides.
#
# Build identity comes from `ken status --json` rather than from this
# checkout: run_ken.py benchmarks whatever binary --ken points at,
# which may not be built from the working tree.
# ──────────────────────────────────────────────────────────────────────────

_PROVENANCE_SCHEMA = (
    "captured_at",
    "config.alpha_nl",
    "config.alpha_override.nl",
    "config.alpha_override.symbol",
    "config.alpha_symbol",
    "config.chunker",
    "config.extra",
    "config.mode",
    "config.model.dir",
    "config.model.sha256",
    "config.model.size_bytes",
    "config.query_count",
    "config.rerank_model.dir",
    "config.rerank_model.sha256",
    "config.rerank_model.size_bytes",
    "config.top_k",
    "corpora[].dirty",
    "corpora[].name",
    "corpora[].path",
    "corpora[].repo",
    "corpora[].revision",
    "env",
    "harness",
    "ken.commit",
    "ken.deps",
    "ken.dirty",
    "ken.go_version",
    "ken.goarch",
    "ken.gomaxprocs",
    "ken.goos",
    "ken.version",
)

# Credential-shaped env names are blanked before they reach a result
# file someone might paste into a benchmark thread. Mirrors
# provenance.redactEnvValue.
_REDACT_MARKERS = ("TOKEN", "SECRET", "PASSWORD", "PASSWD", "CREDENTIAL", "APIKEY", "API_KEY")


def _git(dirpath: Path, *args: str) -> str:
    """git output in dirpath, or "" when git fails (not a repo, no git)."""
    try:
        proc = subprocess.run(
            ["git", *args], cwd=str(dirpath), capture_output=True, text=True, check=False
        )
    except OSError:
        return ""
    return proc.stdout.strip() if proc.returncode == 0 else ""


def _detect_corpus(name: str, path: Path, revision: str = "") -> dict:
    """Pin one corpus. `repo` is recorded separately from `revision` so a
    generated corpus sitting inside some other checkout is visible as such.

    git wins over the passed-in `revision` (semble's repos.json pin):
    sync_repos.py is supposed to have checked that revision out, and a
    checkout that drifted or picked up local edits is exactly what
    provenance exists to catch. The pin is the fallback when the corpus
    isn't a git work tree at all. Mirrors provenance.Detect."""
    entry = {"name": name, "path": str(path), "repo": "", "revision": revision, "dirty": False}
    if not path.exists():
        return entry
    top = _git(path, "rev-parse", "--show-toplevel")
    if not top:
        return entry
    entry["repo"] = top
    entry["revision"] = _git(path, "rev-parse", "HEAD") or revision
    entry["dirty"] = bool(_git(path, "status", "--porcelain"))
    return entry


def _inspect_model(model_dir: Path | None) -> dict:
    """Identify a model snapshot by content: two machines' ~/.ken/model can
    hold different weights under the same path, and that moves every
    semantic number."""
    if model_dir is None:
        return {"dir": "", "sha256": "", "size_bytes": 0}
    blob = model_dir / "model.safetensors"
    out = {"dir": str(model_dir), "sha256": "", "size_bytes": 0}
    if not blob.exists():
        return out
    out["size_bytes"] = blob.stat().st_size
    h = hashlib.sha256()
    with blob.open("rb") as f:
        for block in iter(lambda: f.read(4 << 20), b""):
            h.update(block)
    out["sha256"] = h.hexdigest()
    return out


def _ken_build(ken_bin: str) -> dict:
    """Build identity of the ken binary under test, via `ken status --json`.

    Falls back to an all-empty block if that fails — a partial provenance
    block beats aborting a 40-minute benchmark over it."""
    build = {
        "version": "",
        "commit": "",
        "dirty": False,
        "go_version": "",
        "goos": "",
        "goarch": "",
        "gomaxprocs": 0,
        "deps": {},
    }
    try:
        proc = subprocess.run(
            [ken_bin, "status", "--json"], capture_output=True, text=True, check=False
        )
        if proc.returncode != 0:
            raise ValueError(proc.stderr.strip() or f"exit {proc.returncode}")
        status = json.loads(proc.stdout)
    except (OSError, ValueError, json.JSONDecodeError) as exc:
        sys.stderr.write(f"  warn: provenance: `{ken_bin} status --json` failed ({exc}); "
                         "build identity will be blank\n")
        return build
    versions = status.get("Versions", {})
    process = status.get("Process", {})
    build["version"] = versions.get("Version", "") or ""
    build["commit"] = versions.get("VcsRevision", "") or ""
    build["dirty"] = bool(versions.get("VcsDirty", False))
    build["go_version"] = versions.get("GoVersion", "") or ""
    build["goos"] = process.get("GOOS", "") or ""
    build["goarch"] = process.get("GOARCH", "") or ""
    build["gomaxprocs"] = int(process.get("GOMAXPROCS", 0) or 0)
    for path_, key in (
        ("github.com/townsendmerino/aikit", "AikitVersion"),
        ("github.com/odvcencio/gotreesitter", "GotreesitterVersion"),
    ):
        if versions.get(key):
            build["deps"][path_] = versions[key]
    return build


def _schema_paths(node: object, declared: frozenset[str], prefix: str = "") -> list[str]:
    """Dotted paths of a provenance dict, matching the Go reflection walk in
    bench/internal/provenance/schema_test.go: structs descend, lists of
    structs descend under "[]", and a free-form mapping (env, ken.deps,
    config.extra) is a leaf because its keys are data, not schema.

    Python dicts carry no type distinction between a struct and a map, so
    `declared` supplies it: a path that _PROVENANCE_SCHEMA lists is a leaf
    and we stop there."""
    if prefix and prefix in declared:
        return [prefix]
    if node is None:
        # A null stands in for an object we can't walk (a nullable block
        # like config.alpha_override, which the Go side declares by its
        # components because reflection sees through the nil pointer).
        # Nothing to check; the declared component paths cover it.
        return [] if any(d.startswith(prefix + ".") for d in declared) else [prefix]
    if isinstance(node, dict):
        if not node:
            return [prefix] if prefix else []
        out: list[str] = []
        for key, value in node.items():
            path = f"{prefix}.{key}" if prefix else key
            out.extend(_schema_paths(value, declared, path))
        return out
    if isinstance(node, list):
        if not node:
            return []
        return _schema_paths(node[0], declared, f"{prefix}[]")
    return [prefix]


def collect_provenance(
    *,
    ken_bin: str,
    mode: str,
    chunker: str,
    model_dir: Path | None,
    rerank_model: Path | None,
    top_k: int,
    query_count: int,
    corpora: list[dict],
    extra: dict[str, str],
    alpha_override: dict | None = None,
) -> dict:
    prov = {
        "harness": "bench/semble/run_ken.py",
        "captured_at": datetime.now(timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z"),
        "ken": _ken_build(ken_bin),
        "corpora": corpora,
        "config": {
            "mode": mode,
            "chunker": chunker,
            # The pair is the shipped semble-parity default that
            # docs/BENCH.md's "don't tune ken's constants" rule fixes.
            # alpha_override is non-null only when --alpha-symbol /
            # --alpha-nl pinned a class (the item-1 sweep, or a manual
            # pinned run); a pinned run is a labelled experiment, never
            # a new default.
            "alpha_symbol": 0.3,
            "alpha_nl": 0.5,
            "alpha_override": alpha_override,
            "top_k": top_k,
            "query_count": query_count,
            "model": _inspect_model(model_dir),
            "rerank_model": _inspect_model(rerank_model),
            "extra": extra,
        },
        "env": {
            k: ("[redacted]" if v and any(m in k.upper() for m in _REDACT_MARKERS) else v)
            for k, v in sorted(os.environ.items())
            if k.startswith("KEN_")
        },
    }
    # Self-check: a block that doesn't match the declared schema is a bug
    # here, and the Go side only sees _PROVENANCE_SCHEMA, not this dict.
    # `corpora` and the free-form maps are skipped when empty (no shape
    # to walk), so compare only what's present.
    declared = frozenset(_PROVENANCE_SCHEMA)
    present = set(_schema_paths(prov, declared))
    unexpected = present - declared
    if unexpected:
        raise AssertionError(
            f"provenance block has fields absent from _PROVENANCE_SCHEMA: {sorted(unexpected)}"
        )
    return prov


def main() -> int:
    p = argparse.ArgumentParser(
        description="Run ken against the semble NDCG@10 benchmark (drop-in for the verbatim-port check)."
    )
    p.add_argument(
        "--semble-checkout",
        default=os.environ.get("SEMBLE_CHECKOUT", "/tmp/semble"),
        help="path to a MinishLab/semble checkout containing benchmarks/. default: /tmp/semble or $SEMBLE_CHECKOUT.",
    )
    p.add_argument(
        "--mode",
        choices=["bm25", "semantic", "hybrid", "hybrid-rerank"],
        default="hybrid",
        help="ken retrieval mode (default: hybrid). bm25 needs no model. "
        "hybrid-rerank requires both --model AND --rerank-model.",
    )
    p.add_argument("--chunker", default="regex", help="ken chunker (default: regex).")
    p.add_argument(
        "--model",
        default=os.environ.get("KEN_MODEL_DIR", str(Path.home() / ".ken" / "model")),
        help="ken Model2Vec model dir (default: ~/.ken/model or $KEN_MODEL_DIR). ignored when --mode=bm25.",
    )
    # M5/M6: rerank knobs forwarded to `ken bench`. Only used when --mode=hybrid-rerank.
    p.add_argument(
        "--rerank-model",
        default=os.environ.get("KEN_RERANK_MODEL_DIR", str(Path.home() / ".ken" / "rerank-model")),
        help="CodeRankEmbed dir (default: ~/.ken/rerank-model or $KEN_RERANK_MODEL_DIR). "
        "ignored unless --mode=hybrid-rerank.",
    )
    p.add_argument(
        "--rerank-top-n", type=int, default=None,
        help="rerank head depth (Go default: 50; M0 used 100 for CoIR-style sweeps).",
    )
    p.add_argument(
        "--rerank-beta", type=float, default=None,
        help="score-blend weight β: final = β·rerankCos + (1-β)·fusedScore. "
        "Go default: 0.25 (M0-validated for natural NL workloads); use 1.0 for "
        "M0 CoIR-style pure-replacement comparison.",
    )
    p.add_argument("--ken", default="ken", help="path to the ken binary (default: ken on $PATH).")
    p.add_argument("--top-k", type=int, default=10)
    p.add_argument(
        "--latency-runs",
        type=int,
        default=5,
        help="repeats per query for median-of-N latency timing (default: 5, matches semble).",
    )
    p.add_argument("--repo", action="append", default=[], help="limit to one or more repo names (repeatable).")
    p.add_argument("--language", action="append", default=[], help="limit to one or more languages (repeatable).")
    p.add_argument("--verbose", action="store_true", help="print per-query NDCG to stderr.")
    # α-sensitivity sweep (docs/internal/rag-thread-followups.md item 1).
    p.add_argument(
        "--alpha-sweep", action="store_true",
        help="run the α-sensitivity experiment instead of the plain benchmark: sweep α on a "
             "repo-split tune half, then evaluate the argmax pair once on the holdout half.",
    )
    p.add_argument(
        "--alpha-argmax", default=None, metavar="SYMBOL,NL",
        help="skip the tune sweep and evaluate this α pair on the holdout half "
             "(e.g. '0.3,0.4'). Same deterministic split; 2 passes instead of 13.",
    )
    p.add_argument(
        "--tune-fraction", type=float, default=0.6,
        help="fraction of repos in the tune half of the --alpha-sweep split (default: 0.6).",
    )
    p.add_argument(
        "--alpha-symbol", type=float, default=None,
        help="pin the symbol-class fusion weight for a plain run (0..1). Default: ken's adaptive 0.3. "
             "Ignored under --alpha-sweep, which sets it per grid point.",
    )
    p.add_argument(
        "--alpha-nl", type=float, default=None,
        help="pin the NL-class fusion weight for a plain run (0..1). Default: ken's adaptive 0.5. "
             "Ignored under --alpha-sweep.",
    )
    args = p.parse_args()
    if not 0.1 <= args.tune_fraction <= 0.9:
        sys.exit(f"--tune-fraction must be in [0.1, 0.9], got {args.tune_fraction}")
    if args.alpha_argmax is not None:
        try:
            parts = [float(x) for x in args.alpha_argmax.split(",")]
        except ValueError:
            parts = []
        if len(parts) != 2 or not all(0.0 <= v <= 1.0 for v in parts):
            sys.exit(f"--alpha-argmax expects SYMBOL,NL with both in [0,1], got {args.alpha_argmax!r}")
        args.alpha_argmax = (parts[0], parts[1])
    for name, value in (("--alpha-symbol", args.alpha_symbol), ("--alpha-nl", args.alpha_nl)):
        if value is not None and not 0.0 <= value <= 1.0:
            sys.exit(f"{name} must be in [0, 1], got {value}")

    model_dir: Path | None
    if args.mode == "bm25":
        model_dir = None
    else:
        model_dir = Path(args.model).expanduser().resolve()
        if not (model_dir / "model.safetensors").exists():
            sys.exit(
                f"--mode={args.mode} but no model.safetensors at {model_dir}\n"
                "  download: huggingface-cli download minishlab/potion-code-16M "
                "tokenizer.json config.json model.safetensors --local-dir ~/.ken/model"
            )

    # M5/M6: hybrid-rerank also needs the CodeRankEmbed snapshot.
    rerank_model: Path | None = None
    if args.mode == "hybrid-rerank":
        rerank_model = Path(args.rerank_model).expanduser().resolve()
        if not (rerank_model / "model.safetensors").exists():
            sys.exit(
                f"--mode=hybrid-rerank but no model.safetensors at {rerank_model}\n"
                "  download: `ken download-model --rerank` (fetches ~547 MB to ~/.ken/rerank-model)"
            )

    repo_specs = available_repo_specs()
    if not repo_specs:
        sys.exit(
            "no semble repo specs have a local checkout — run "
            f"`python benchmarks/sync_repos.py` from {_SEMBLE_ROOT} first."
        )
    tasks = load_tasks(repo_specs)
    if args.repo:
        tasks = [t for t in tasks if t.repo in args.repo]
    if args.language:
        tasks = [t for t in tasks if t.language in args.language]
    if not tasks:
        sys.exit("no benchmark tasks matched the requested --repo/--language filters.")

    grouped = grouped_tasks(tasks)

    if args.alpha_sweep:
        sweep = run_alpha_sweep(args, model_dir, grouped, repo_specs)
        out_dir = Path(__file__).resolve().parent / "results"
        out_dir.mkdir(exist_ok=True)
        out_path = out_dir / f"alpha-sweep-{args.mode}.json"
        sweep["provenance"] = collect_provenance(
            ken_bin=args.ken,
            mode=args.mode,
            chunker=args.chunker,
            model_dir=model_dir,
            rerank_model=rerank_model,
            top_k=args.top_k,
            query_count=sweep["split"]["tune_queries"] + sweep["split"]["holdout_queries"],
            corpora=[
                _detect_corpus(name, Path(repo_specs[name].benchmark_dir),
                               revision=str(getattr(repo_specs[name], "revision", "") or ""))
                for name in sorted(grouped)
            ],
            extra={
                "experiment": "alpha-sensitivity",
                "tune_fraction": str(args.tune_fraction),
                "grid": ",".join(str(a) for a in ALPHA_GRID),
            },
        )
        out_path.write_text(json.dumps(sweep, indent=2) + "\n")
        sys.stderr.write(f"\nSweep saved to {out_path}\n")
        return 0

    sys.stderr.write(f"ken-{args.mode}  ({len(grouped)} repos, {len(tasks)} tasks)\n")
    sys.stderr.write(
        f"{'Repo':<24} {'Language':<12} {'N':>3}  {'NDCG@10':>8}  {'p50':>7}\n"
    )
    sys.stderr.write(f"{'-' * 24} {'-' * 12} {'-' * 3}  {'-' * 8}  {'-' * 7}\n")

    started = time.perf_counter()
    outcomes: list[RepoOutcome] = []
    for repo_name, repo_tasks in sorted(grouped.items()):
        spec = repo_specs[repo_name]
        if args.verbose:
            sys.stderr.write(f"\n--- {repo_name} ({spec.language}) ---\n")
        o = run_repo(
            ken_bin=args.ken,
            spec=spec,
            tasks=repo_tasks,
            mode=args.mode,
            chunker=args.chunker,
            model_dir=model_dir,
            top_k=args.top_k,
            latency_runs=args.latency_runs,
            verbose=args.verbose,
            rerank_model=rerank_model,
            rerank_top_n=args.rerank_top_n,
            rerank_beta=args.rerank_beta,
            alpha_symbol=args.alpha_symbol,
            alpha_nl=args.alpha_nl,
        )
        outcomes.append(o)
        sys.stderr.write(
            f"{o.repo:<24} {o.language:<12} {o.n_tasks:>3}  {o.ndcg10:>8.3f}  {o.p50_ms:>5.1f}ms\n"
        )
    elapsed = time.perf_counter() - started

    n = len(outcomes)
    avg_ndcg = sum(o.ndcg10 for o in outcomes) / n if n else 0.0
    avg_p50 = sum(o.p50_ms for o in outcomes) / n if n else 0.0
    sys.stderr.write(f"{'-' * 24} {'-' * 12} {'-' * 3}  {'-' * 8}  {'-' * 7}\n")
    sys.stderr.write(
        f"{f'Average ({n})':<24} {'':<12} {'':<3}  {avg_ndcg:>8.3f}  {avg_p50:>5.1f}ms\n"
    )

    # Per-language summary (averaged within language, matching semble's
    # README table). Note: ken's regex chunker covers Python/Go/TS/Java/Rust;
    # other languages fall through to the line chunker, which is a known
    # divergence from semble (which uses tree-sitter for all 19).
    by_lang: dict[str, list[RepoOutcome]] = defaultdict(list)
    for o in outcomes:
        by_lang[o.language].append(o)
    sys.stderr.write("\nPer-language NDCG@10:\n")
    for lang in sorted(by_lang):
        lo = by_lang[lang]
        sys.stderr.write(
            f"  {lang:<12} {sum(o.ndcg10 for o in lo) / len(lo):.3f}  ({len(lo)} repo{'s' if len(lo) != 1 else ''})\n"
        )

    sys.stderr.write(f"\nTotal wall time: {elapsed:.1f}s\n")

    # Save full results JSON.
    out_dir = Path(__file__).resolve().parent / "results"
    out_dir.mkdir(exist_ok=True)
    out_path = out_dir / f"ken-{args.mode}.json"

    # One corpus entry per scored repo. semble's RepoSpec may already
    # carry the pinned revision sync_repos.py checked out; fall back to
    # asking git in the checkout when it doesn't.
    corpora = [
        _detect_corpus(
            o.repo,
            Path(repo_specs[o.repo].benchmark_dir),
            revision=str(getattr(repo_specs[o.repo], "revision", "") or ""),
        )
        for o in outcomes
    ]
    extra = {"latency_runs": str(args.latency_runs)}
    if args.mode == "hybrid-rerank":
        if args.rerank_top_n is not None:
            extra["rerank_top_n"] = str(args.rerank_top_n)
        if args.rerank_beta is not None:
            extra["rerank_beta"] = str(args.rerank_beta)
    if args.repo:
        extra["repo_filter"] = ",".join(args.repo)
    if args.language:
        extra["language_filter"] = ",".join(args.language)

    alpha_override = None
    if args.alpha_symbol is not None or args.alpha_nl is not None:
        alpha_override = {"symbol": args.alpha_symbol, "nl": args.alpha_nl}

    summary = {
        "provenance": collect_provenance(
            alpha_override=alpha_override,
            ken_bin=args.ken,
            mode=args.mode,
            chunker=args.chunker,
            model_dir=model_dir,
            rerank_model=rerank_model,
            top_k=args.top_k,
            query_count=sum(o.n_tasks for o in outcomes),
            corpora=corpora,
            extra=extra,
        ),
        "method": f"ken-{args.mode}",
        "mode": args.mode,
        "chunker": args.chunker,
        "model": str(model_dir) if model_dir else None,
        "n_repos": n,
        "n_tasks": sum(o.n_tasks for o in outcomes),
        "avg_ndcg10": round(avg_ndcg, 4),
        "avg_p50_ms": round(avg_p50, 2),
        "wall_s": round(elapsed, 2),
        "per_language": {
            lang: round(sum(o.ndcg10 for o in lo) / len(lo), 4) for lang, lo in by_lang.items()
        },
        "repos": [
            {**asdict(o), "ndcg10": round(o.ndcg10, 4), "p50_ms": round(o.p50_ms, 2)}
            for o in outcomes
        ],
    }
    out_path.write_text(json.dumps(summary, indent=2) + "\n")
    sys.stderr.write(f"Results saved to {out_path}\n")
    print(json.dumps(summary, indent=2))
    return 0


if __name__ == "__main__":
    sys.exit(main())
