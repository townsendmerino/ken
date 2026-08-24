#!/usr/bin/env python3
"""chunker_disagreement.py — where do two chunkers actually disagree?

docs/internal/rag-thread-followups.md item 2. The aggregate NDCG gap
between ken's regex and treesitter chunkers is inside noise, and
ADR-011 keeps regex as the default on that basis. The r/Rag objection
was that an average over 1,251 queries can hide a real win on a
specific failure class.

So: instead of comparing means, pair the runs query by query and look
at where they diverge. Reads two `run_ken.py --dump-per-query` result
files (which carry each query's target ranks and hit list) and slices
the disagreements by category, language, and query class.

    python3 scripts/chunker_disagreement.py \
        bench/semble/results/ken-hybrid.json \
        bench/semble/results/ken-hybrid-treesitter.json

A "disagreement" follows the doc's definition: one chunker found the
target and the other missed it, or one ranked it >=5 positions better.
The threshold exists because rank churn of 1-2 positions is ordinary
score jitter, not a chunking difference worth attributing.
"""
from __future__ import annotations

import argparse
import collections
import json
import math
import sys
from pathlib import Path

# Ranks are 1-based; None means the target never surfaced in the top-k.
# MISS sorts worse than any real rank so comparisons stay total.
MISS = math.inf

# A chunker must beat the other by at least this many rank positions
# for the pair to count as a disagreement. 1-2 positions of churn is
# score jitter between two near-identical fused scores.
RANK_MARGIN = 5


def load(path: Path) -> tuple[dict, dict]:
    d = json.loads(path.read_text())
    rows = d.get("per_query")
    if not rows:
        sys.exit(f"{path} has no per_query block — re-run with --dump-per-query.")
    # (repo, query) is the join key: query text alone collides across
    # repos (several repos have a task literally named "error handling").
    return d, {(r["repo"], r["query"]): r for r in rows}


def rank_of(row: dict) -> float:
    r = row.get("best_rank")
    return MISS if r is None else float(r)


def main() -> int:
    p = argparse.ArgumentParser(description=__doc__,
                                formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("baseline", type=Path, help="result JSON for the baseline chunker (regex)")
    p.add_argument("challenger", type=Path, help="result JSON for the challenger (treesitter)")
    p.add_argument("--margin", type=int, default=RANK_MARGIN,
                   help=f"rank positions a chunker must win by (default {RANK_MARGIN})")
    args = p.parse_args()

    dbase, base = load(args.baseline)
    dchal, chal = load(args.challenger)
    bname, cname = dbase.get("chunker", "baseline"), dchal.get("chunker", "challenger")

    shared = sorted(base.keys() & chal.keys())
    if not shared:
        sys.exit("the two runs share no (repo, query) keys — different corpora?")
    only_base, only_chal = len(base) - len(shared), len(chal) - len(shared)
    if only_base or only_chal:
        sys.stderr.write(
            f"warn: {only_base} queries only in {bname}, {only_chal} only in {cname}; "
            "comparing the intersection\n"
        )

    agree = 0
    wins = {bname: [], cname: []}
    for key in shared:
        b, c = base[key], chal[key]
        rb, rc = rank_of(b), rank_of(c)
        if rb == rc:
            agree += 1
            continue
        # A miss-vs-found pair always counts; otherwise require the margin.
        decisive = (rb == MISS) != (rc == MISS) or abs(rb - rc) >= args.margin
        if not decisive:
            agree += 1
            continue
        winner = cname if rc < rb else bname
        wins[winner].append({
            **{k: c[k] for k in ("repo", "language", "category", "symbol_query", "query")},
            "rank_base": None if rb == MISS else int(rb),
            "rank_chal": None if rc == MISS else int(rc),
            "ndcg_delta": c["ndcg10"] - b["ndcg10"],
        })

    nb, nc = len(wins[bname]), len(wins[cname])
    total = len(shared)
    print(f"\n{total} shared queries  ·  margin >= {args.margin} ranks or a miss/found flip")
    print(f"  agree (or within margin): {agree}  ({agree / total:.1%})")
    print(f"  {bname} wins: {nb}   {cname} wins: {nc}   net: {nc - nb:+d} for {cname}\n")

    if nb + nc == 0:
        print("no disagreements past the margin — the chunkers are interchangeable here.")
        return 0

    def table(title: str, keyfn) -> None:
        buckets = collections.defaultdict(lambda: [0, 0, 0.0])
        for w in wins[bname]:
            buckets[keyfn(w)][0] += 1
            buckets[keyfn(w)][2] -= w["ndcg_delta"]
        for w in wins[cname]:
            buckets[keyfn(w)][1] += 1
            buckets[keyfn(w)][2] += w["ndcg_delta"]
        print(f"{title}")
        print(f"| {'slice':<14} | {bname} wins | {cname} wins | net | Σ NDCG Δ |")
        print(f"|{'-' * 16}|{'-' * 12}|{'-' * 17}|{'-' * 5}|{'-' * 10}|")
        for k, (b, c, dsum) in sorted(buckets.items(), key=lambda kv: -(kv[1][0] + kv[1][1])):
            print(f"| {str(k):<14} | {b:>10} | {c:>15} | {c - b:>+3} | {dsum:>+8.3f} |")
        print()

    table("by query category", lambda w: w["category"])
    table("by language", lambda w: w["language"])
    table("by query class", lambda w: "symbol" if w["symbol_query"] else "nl")

    # Miss/found flips are the sharpest cases: one chunker surfaced the
    # target at all and the other did not.
    flips_c = [w for w in wins[cname] if w["rank_base"] is None]
    flips_b = [w for w in wins[bname] if w["rank_chal"] is None]
    print(f"miss -> found flips: {cname} rescues {len(flips_c)}, {bname} rescues {len(flips_b)}")
    for label, flips in ((cname, flips_c), (bname, flips_b)):
        for w in flips[:5]:
            print(f"    [{label}] {w['repo']}/{w['language']}: {w['query'][:64]!r}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
