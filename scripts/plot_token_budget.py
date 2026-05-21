#!/usr/bin/env python3
"""
plot_token_budget.py — turn bench/tokens/results/{coir,semble}-tokens.json
into a markdown table for direct paste into docs/BENCH.md.

The PNG output the Prompt-17 spec mentions is nice-to-have but the
deliverable is the table; this script produces only the markdown so
it has no matplotlib / numpy dependency. Run from the repo root after
`go test -tags=bench ./bench/tokens/`.

Usage:
    python3 scripts/plot_token_budget.py              # both benches
    python3 scripts/plot_token_budget.py semble       # one
    python3 scripts/plot_token_budget.py coir
"""
from __future__ import annotations

import argparse
import json
import math
import statistics
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
RESULTS_DIR = REPO_ROOT / "bench" / "tokens" / "results"

KS = [1, 3, 5, 10]


def median_int(xs: list[int]) -> int:
    if not xs:
        return 0
    return int(statistics.median(xs))


def mean(xs: list[float]) -> float:
    if not xs:
        return 0.0
    return sum(xs) / len(xs)


def render_table(records: list[dict], title: str) -> str:
    """Build the per-class table the prompt specifies."""
    lines = []
    n = len(records)
    n_sym = sum(1 for r in records if r["query_class"] == "symbol")
    n_nl = n - n_sym
    lines.append(f"### {title}")
    lines.append("")
    lines.append(f"_{n} queries total — {n_sym} symbol, {n_nl} NL._")
    lines.append("")
    lines.append("| Class  | K  | ken med tokens | ken recall@K | grep med tokens | grep recall | grep variant |")
    lines.append("|--------|----|---------------:|-------------:|----------------:|------------:|:-------------|")

    for cls in ["symbol", "nl"]:
        by_cls = [r for r in records if r["query_class"] == cls]
        if not by_cls:
            continue
        # Pick the more favorable grep variant per class:
        # - symbol: literal grep (the realistic best case)
        # - nl: tokenized grep (literal is useless on NL)
        use_literal = cls == "symbol"
        grep_tokens, grep_recalls = [], []
        for r in by_cls:
            g = (
                r["grep_literal"]
                if (use_literal and r.get("grep_literal"))
                else r["grep_tokenized"]
            )
            grep_tokens.append(g["tokens"])
            grep_recalls.append(1.0 if g["recall"] else 0.0)
        g_med = median_int(grep_tokens)
        g_rec = mean(grep_recalls)
        g_label = "literal" if use_literal else "tokenized"

        for k in KS:
            ken_toks, ken_recalls = [], []
            for r in by_cls:
                for ka in r["ken"]:
                    if ka["k"] == k:
                        ken_toks.append(ka["tokens"])
                        ken_recalls.append(1.0 if ka["recall"] else 0.0)
            ken_med = median_int(ken_toks)
            ken_rec = mean(ken_recalls)
            lines.append(
                f"| {cls:<6} | {k:>2} | {ken_med:>14} | {ken_rec:>12.3f} | {g_med:>15} | {g_rec:>11.3f} | {g_label:<12} |"
            )
    lines.append("")
    return "\n".join(lines)


def render_headline(records: list[dict], bench: str) -> str:
    """One-paragraph headline: ratio of grep-to-ken tokens at recall@1
    on the median, per query class. Honest about ratios > 1 going
    either direction."""
    bits = []
    for cls in ["symbol", "nl"]:
        by_cls = [r for r in records if r["query_class"] == cls]
        if not by_cls:
            continue
        # ken @ K=1
        ken_toks = [
            ka["tokens"] for r in by_cls for ka in r["ken"] if ka["k"] == 1
        ]
        ken_rec = mean(
            [1.0 if ka["recall"] else 0.0 for r in by_cls for ka in r["ken"] if ka["k"] == 1]
        )
        ken_med = median_int(ken_toks)

        use_literal = cls == "symbol"
        g_toks, g_recalls = [], []
        for r in by_cls:
            g = (
                r["grep_literal"]
                if (use_literal and r.get("grep_literal"))
                else r["grep_tokenized"]
            )
            g_toks.append(g["tokens"])
            g_recalls.append(1.0 if g["recall"] else 0.0)
        g_med = median_int(g_toks)
        g_rec = mean(g_recalls)
        g_label = "literal grep" if use_literal else "tokenized grep"

        if ken_med == 0:
            ratio = "(undefined; ken med = 0)"
        else:
            r = g_med / ken_med
            ratio = f"{r:.1f}×"
        bits.append(
            f"- **{cls.upper()} queries**: median ken@1 = {ken_med:,} tokens "
            f"(recall {ken_rec:.2%}); median {g_label} = {g_med:,} tokens "
            f"(recall {g_rec:.2%}). Grep/ken ratio: {ratio}."
        )

    return f"**{bench} headline:**\n" + "\n".join(bits) + "\n"


def process(name: str) -> str:
    path = RESULTS_DIR / f"{name}-tokens.json"
    if not path.exists():
        return f"_(missing {path}; run the harness first)_\n"
    data = json.loads(path.read_text())
    title_map = {
        "coir": "CoIR-CSN-Python",
        "semble": "semble bench (63-repo cross-language)",
    }
    title = title_map.get(name, name)
    table = render_table(data, title)
    headline = render_headline(data, title)
    return f"{table}\n{headline}\n"


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "which",
        nargs="?",
        choices=["coir", "semble", "both"],
        default="both",
        help="which results to render (default both)",
    )
    args = parser.parse_args()

    if args.which in ("both", "semble"):
        print(process("semble"))
    if args.which in ("both", "coir"):
        print(process("coir"))


if __name__ == "__main__":
    main()
