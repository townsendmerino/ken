#!/usr/bin/env python3
"""
merge_m0d.py — combine three M0d bench invocations' per-query CSVs into
Tables A / B / C for outputs/m0d-results.md.

Inputs (default paths, override via env or CLI):

  /tmp/m0d-stripped.csv         — invocation 1 (Arm A: baseline + oracle-max + oracle-df + encoder)
  /tmp/m0d-stripped-heur.csv    — invocation 2 (Arm B full-enriched)
  /tmp/m0d-stripped-heur-only.csv — invocation 3 (Arm B heuristic-only ablation)

Each CSV's schema (from bench/ndcg/hyde_test.go):
  qid, mode, cell, ndcg10, recall10, recall100, recall50, qrel_pos

The M0c 40 set is computed from invocation 1: it's the qids gained by
oracle-df<N> on hybrid+rerank vs the baseline cell. We anchor every arm
against invocation 1's `hybrid+rerank,baseline` per-query qrel_pos (the
unaugmented stripped baseline) so cross-arm comparisons are apples-to-
apples regardless of which corpus the arm ran on.

For each arm, report:
  gained_from_40  — qids in the M0c-40 whose qrel reached top-50 under this arm
  gained_outside  — qids NOT in M0c-40 whose qrel reached top-50 under this arm
                    when it was outside top-50 under inv1-baseline
  lost            — qids whose qrel was inside top-50 under inv1-baseline
                    but outside under this arm

McNemar p reported on the (gained vs lost) discordant pairs.

Pairwise overlap matrix on the gained-from-40 sets — directional at N=500,
not statistically tight.

Output: a markdown report on stdout. Pipe into outputs/m0d-results.md
or copy from the terminal.
"""

from __future__ import annotations

import argparse
import csv
import json
import math
import os
import sys
from collections import defaultdict
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent

DEFAULT_PATHS = {
    "stripped": "/tmp/m0d-stripped.csv",
    "stripped-heur": "/tmp/m0d-stripped-heur.csv",
    "stripped-heur-only": "/tmp/m0d-stripped-heur-only.csv",
}


def load_csv(path: Path) -> dict[tuple[str, str, str], dict]:
    """Returns {(mode, cell, qid): row_dict} for fast per-arm lookup."""
    out: dict[tuple[str, str, str], dict] = {}
    with open(path) as f:
        reader = csv.DictReader(f)
        for row in reader:
            key = (row["mode"], row["cell"], row["qid"])
            row["qrel_pos"] = int(row["qrel_pos"])
            row["ndcg10"] = float(row["ndcg10"])
            row["recall10"] = float(row["recall10"])
            row["recall100"] = float(row["recall100"])
            row["recall50"] = float(row["recall50"])
            out[key] = row
    return out


def in_top50(qrel_pos: int) -> bool:
    return 1 <= qrel_pos <= 50


def mcnemar_p(b: int, c: int) -> float:
    """McNemar χ² with continuity correction on discordant pairs (b, c).
    Returns the two-sided p-value approximation; b+c <= 25 falls back to
    a binomial calc for tightness."""
    n = b + c
    if n == 0:
        return 1.0
    if n <= 25:
        # Exact binomial two-sided: P(X ≤ min(b,c) | n, 0.5) * 2.
        k = min(b, c)
        from math import comb
        cdf = sum(comb(n, i) for i in range(k + 1)) / (2 ** n)
        return min(1.0, 2 * cdf)
    chi2 = (abs(b - c) - 1) ** 2 / n
    # χ²(1) survival → erfc(sqrt(chi2/2)).
    return math.erfc(math.sqrt(chi2 / 2))


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--stripped", default=DEFAULT_PATHS["stripped"])
    ap.add_argument("--heur", default=DEFAULT_PATHS["stripped-heur"])
    ap.add_argument("--heur-only", default=DEFAULT_PATHS["stripped-heur-only"])
    ap.add_argument("--mode", default="hybrid+rerank",
                    help="which mode's rows to read (default: hybrid+rerank — the M0d build target)")
    args = ap.parse_args()

    inv1 = load_csv(Path(args.stripped))
    inv2 = load_csv(Path(args.heur))
    inv3 = load_csv(Path(args.heur_only))

    # Discover the arm structure from inv1's cells.
    cells_inv1 = sorted({k[1] for k in inv1 if k[0] == args.mode})
    if "baseline" not in cells_inv1:
        sys.exit(f"inv1 ({args.stripped}) has no 'baseline' cell on mode={args.mode}: cells={cells_inv1}")

    # Find the oracle-df<N> cell name (df-floor varies).
    oracle_df_cell = next((c for c in cells_inv1 if c.startswith("oracle-df")), None)
    if oracle_df_cell is None:
        sys.exit(f"inv1 has no oracle-df cell on mode={args.mode}: cells={cells_inv1}")

    # Reference baseline = inv1's baseline cell.
    qids = sorted({k[2] for k in inv1 if k[0] == args.mode and k[1] == "baseline"})

    def pos(csvmap, cell, qid):
        row = csvmap.get((args.mode, cell, qid))
        return row["qrel_pos"] if row else 0

    # M0c-40 set: qids the oracle-df cell rescues into top-50 vs inv1 baseline.
    m0c_40 = {
        qid for qid in qids
        if not in_top50(pos(inv1, "baseline", qid))
        and in_top50(pos(inv1, oracle_df_cell, qid))
    }

    # Define the arms to report on.
    #   inv1: every non-baseline cell (oracle-max, oracle-df<N>, encoder-...)
    #   inv2: a single arm called "heur-full" (its baseline cell on the enriched corpus)
    #   inv3: a single arm called "heur-only" (its baseline cell on the ablation corpus)
    arm_specs: list[tuple[str, dict, str]] = []  # (label, csvmap, cell-name-in-its-csv)
    for cell in cells_inv1:
        if cell == "baseline":
            continue
        arm_specs.append((cell, inv1, cell))
    arm_specs.append(("heur-full", inv2, "baseline"))
    arm_specs.append(("heur-only", inv3, "baseline"))

    # Per-arm tallies: gained-from-40, gained-outside, lost. Reference
    # is inv1-baseline (unaugmented stripped, hybrid+rerank).
    table_a: dict[str, dict] = {}
    gained_sets: dict[str, set[str]] = {}
    for label, csvmap, cell in arm_specs:
        gained_from_40 = set()
        gained_outside = set()
        lost = set()
        ndcg_sum_arm = 0.0
        ndcg_sum_ref = 0.0
        nd_count = 0
        for qid in qids:
            base_pos = pos(inv1, "baseline", qid)
            arm_pos = pos(csvmap, cell, qid)
            base_in = in_top50(base_pos)
            arm_in = in_top50(arm_pos)
            if not base_in and arm_in:
                if qid in m0c_40:
                    gained_from_40.add(qid)
                else:
                    gained_outside.add(qid)
            elif base_in and not arm_in:
                lost.add(qid)
            # Aggregate NDCG@10 for the table-A NDCG-lift column.
            ref_row = inv1.get((args.mode, "baseline", qid))
            arm_row = csvmap.get((args.mode, cell, qid))
            if ref_row and arm_row:
                ndcg_sum_ref += ref_row["ndcg10"]
                ndcg_sum_arm += arm_row["ndcg10"]
                nd_count += 1
        b = len(gained_from_40) + len(gained_outside)
        c = len(lost)
        p = mcnemar_p(b, c)
        avg_ref = ndcg_sum_ref / nd_count if nd_count else 0.0
        avg_arm = ndcg_sum_arm / nd_count if nd_count else 0.0
        table_a[label] = {
            "gained_from_40": len(gained_from_40),
            "gained_outside": len(gained_outside),
            "lost": c,
            "net": (b - c),
            "ndcg_lift": avg_arm - avg_ref,
            "mcnemar_p": p,
            "arm_ndcg": avg_arm,
            "ref_ndcg": avg_ref,
        }
        gained_sets[label] = gained_from_40 | gained_outside  # all gained, for overlap

    # Table A: print.
    print(f"\n## Table A — flip-set per arm (mode={args.mode}, baseline=inv1.baseline)\n")
    print("| Arm | gained-from-40 | gained-outside | lost | net | NDCG@10 lift | McNemar p |")
    print("|---|---:|---:|---:|---:|---:|---:|")
    print(f"| inv1-baseline (ref) | — | — | — | — | (NDCG={avg_ref_ref(inv1, args.mode, qids):.4f}) | — |")
    for label, t in table_a.items():
        print(f"| {label} | {t['gained_from_40']} | {t['gained_outside']} | {t['lost']} | "
              f"{t['net']:+d} | {t['ndcg_lift']:+.4f} | {t['mcnemar_p']:.4f} |")

    print(f"\nM0c-40 size: {len(m0c_40)} (oracle-df cell {oracle_df_cell!r})\n")

    # Table B: 40-query capture. Per-qid matrix of which arms rescue it.
    arm_labels = list(table_a.keys())
    print("## Table B — M0c-40 per-query capture\n")
    header = "| qid | inv1-baseline-pos | " + " | ".join(arm_labels) + " |"
    sep = "|---|---:|" + "---:|" * len(arm_labels)
    print(header)
    print(sep)
    for qid in sorted(m0c_40):
        cells_pos = []
        for label, csvmap, cell in arm_specs:
            cells_pos.append(_fmt_pos(pos(csvmap, cell, qid)))
        print(f"| {qid} | {_fmt_pos(pos(inv1, 'baseline', qid))} | " + " | ".join(cells_pos) + " |")

    # Per-arm "of-40 captured" count summary just under Table B.
    print("\n**Of-40 captured per arm:**\n")
    for label, t in table_a.items():
        print(f"- `{label}`: **{t['gained_from_40']} / {len(m0c_40)}**")

    # Pairwise overlap matrix on the gained sets (any gain, in or
    # outside the 40).
    print("\n## Table B (overlap) — pairwise gained-set intersection / union\n")
    print("Cells are |A ∩ B| / |A ∪ B|. Diagonals = |A|. N=500 is directional, not tight.\n")
    print("| | " + " | ".join(arm_labels) + " |")
    print("|---|" + "---:|" * len(arm_labels))
    for a in arm_labels:
        cells_str = [f"`{a}`"]
        for b in arm_labels:
            if a == b:
                cells_str.append(str(len(gained_sets[a])))
            else:
                inter = len(gained_sets[a] & gained_sets[b])
                union = len(gained_sets[a] | gained_sets[b])
                cells_str.append(f"{inter}/{union}")
        print("| " + " | ".join(cells_str) + " |")

    # Reference: HyDE Phase B's gained set.
    hyde_phase_b = {"c134055", "c200465", "c265644", "c265774", "c265803", "c265879", "c265900"}
    print("\n**HyDE Phase B overlap** (vs the 7 qids HyDE rescued at w=0.3 on hybrid+rerank):\n")
    for label in arm_labels:
        ov = gained_sets[label] & hyde_phase_b
        only = gained_sets[label] - hyde_phase_b
        print(f"- `{label}`: overlap with HyDE={len(ov)}/7, arm-only={len(only)}")

    print()


def _fmt_pos(p: int) -> str:
    if not p:
        return "miss"
    return f"#{p}"


def avg_ref_ref(inv1, mode, qids):
    """Average NDCG@10 of inv1's baseline cell across qids."""
    s, n = 0.0, 0
    for qid in qids:
        r = inv1.get((mode, "baseline", qid))
        if r:
            s += r["ndcg10"]
            n += 1
    return s / n if n else 0.0


if __name__ == "__main__":
    main()
