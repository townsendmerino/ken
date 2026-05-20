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
import json
import os
import statistics
import subprocess
import sys
import time
from collections import defaultdict
from dataclasses import asdict, dataclass, field
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

    if len(records) != len(stdin_lines):
        sys.stderr.write(
            f"  warn: {spec.name}: expected {len(stdin_lines)} records, got {len(records)}\n"
        )

    ndcg10_sum = 0.0
    median_latencies: list[float] = []
    category_ndcg10: dict[str, list[float]] = defaultdict(list)

    for i, task in enumerate(tasks):
        runs = records[i * latency_runs : (i + 1) * latency_runs]
        if not runs:
            continue
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
        choices=["bm25", "semantic", "hybrid"],
        default="hybrid",
        help="ken retrieval mode (default: hybrid). bm25 needs no model.",
    )
    p.add_argument("--chunker", default="regex", help="ken chunker (default: regex).")
    p.add_argument(
        "--model",
        default=os.environ.get("KEN_MODEL_DIR", str(Path.home() / ".ken" / "model")),
        help="ken Model2Vec model dir (default: ~/.ken/model or $KEN_MODEL_DIR). ignored when --mode=bm25.",
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
    args = p.parse_args()

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
    summary = {
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
