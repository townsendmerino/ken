#!/usr/bin/env python3
"""m0_ceiling.py — M0 GO/NO-GO for the CodeRankEmbed neural reranker.

See outputs/ken-rerank-plan.md §12. This is the cheap, zero-forward-pass
gate that runs BEFORE the ~2-3 week pure-Go transformer port: it loads the
REAL CodeRankEmbed model (via sentence-transformers) and reranks ken's
ACTUAL stage-1 hybrid shortlist, then reports the NDCG@10 lift over hybrid
plus recall@rerankN (the stage-1 ceiling the reranker can never exceed).

If the real-model lift over ken's shortlist is only ~2-3 points, the port
is NOT worth the maintenance surface / 550 MB / multi-second latency, and
the project stops here.

Two benchmarks (the plan author named CoIR; we also run semble because
CoIR-CSN-Python has a documented substring-leak artifact that favors
lexical matching — see docs/BENCH.md — making it a weak gate for a
*semantic* reranker, whereas semble's diverse NL queries are where hybrid
actually wins and where per-category labels validate the isSymbolQuery
skip §9.3):

  coir   — reads testdata/bench/coir-csn-python/shortlist.jsonl exported
           by `go test -tags=bench -run TestCoIR_ExportShortlist` (the
           multi-line function-source queries can't stream to `ken bench`).
  semble — drives `ken bench <repo> --mode hybrid -k MAXCAND` per repo and
           reranks, scoring with semble's own NDCG metric (no drift).

Usage:
  scripts/m0_ceiling.py --bench both
  scripts/m0_ceiling.py --bench coir --rerank-ns 10,25,50,100
  scripts/m0_ceiling.py --bench semble --semble-checkout /tmp/semble
"""
from __future__ import annotations

import argparse
import json
import math
import os
import subprocess
import sys
import time
from collections import defaultdict
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO_ROOT / "scripts"))


# ── shared NDCG (CoIR convention; mirrors bench/ndcg/ndcg.go exactly) ──────


def ndcg_at_k_graded(ranked_docs: list[str], rels: dict[str, float], k: int) -> float:
    """NDCG@k for a doc-id ranking against a graded-relevance map.

    Matches bench/ndcg/ndcg.go::AtK: rel/log2(i+2), unjudged == 0,
    first occurrence of a duplicate doc counts.
    """
    dcg = 0.0
    seen: set[str] = set()
    rank = 0
    for doc in ranked_docs:
        if rank >= k:
            break
        if doc in seen:
            continue
        seen.add(doc)
        rank += 1
        rel = rels.get(doc, 0.0)
        if rel > 0:
            dcg += rel / math.log2(rank + 1)  # rank is now 1-based
    ideal = sorted((r for r in rels.values() if r > 0), reverse=True)
    if not ideal:
        return 0.0
    idcg = sum(rel / math.log2(i + 2) for i, rel in enumerate(ideal[:k]))
    return dcg / idcg if idcg > 0 else 0.0


def minmax(xs: list[float]) -> list[float]:
    """Min-max normalize to [0,1]; flat input → all 0.5."""
    lo, hi = min(xs), max(xs)
    if hi <= lo:
        return [0.5] * len(xs)
    return [(x - lo) / (hi - lo) for x in xs]


def aggregate_by_doc(doc_ids_in_order: list[str]) -> list[str]:
    """Chunk-level ranking → doc-level: keep best (first) chunk per doc."""
    seen: set[str] = set()
    out: list[str] = []
    for d in doc_ids_in_order:
        if d in seen:
            continue
        seen.add(d)
        out.append(d)
    return out


# ── CoIR path ──────────────────────────────────────────────────────────────


def run_coir(reranker, rerank_ns: list[int], bench_dir: Path, limit: int = 0) -> None:
    shortlist_path = bench_dir / "shortlist.jsonl"
    qrels_path = bench_dir / "qrels.jsonl"
    if not shortlist_path.exists():
        sys.exit(
            f"missing {shortlist_path}\n"
            "  export it first:\n"
            "    KEN_COIR_EXPORT=1 KEN_COIR_QUERY_LIMIT=1000 KEN_COIR_MAX_CAND=100 \\\n"
            "    go test -tags=bench ./bench/ndcg/ -run TestCoIR_ExportShortlist -v -timeout 60m"
        )

    # qrels: query_id -> {doc_id -> score}
    qrels: dict[str, dict[str, float]] = defaultdict(dict)
    with qrels_path.open() as f:
        for line in f:
            r = json.loads(line)
            if r["score"] > 0:
                qrels[r["query_id"]][r["doc_id"]] = float(r["score"])

    rows = [json.loads(line) for line in shortlist_path.open() if line.strip()]
    if limit and limit < len(rows):
        rows = rows[:limit]  # shortlist is sorted by query_id ⇒ deterministic subsample
    sys.stderr.write(f"[coir] {len(rows)} queries, max rerank-n {max(rerank_ns)}\n")
    sys.stderr.flush()

    from coderank_model import QUERY_PREFIX

    warm: list[str] = []
    for row in rows:
        warm.append(QUERY_PREFIX + row["query_text"])
        warm.extend(c["content"] for c in row["candidates"])
    reranker.prewarm(warm)

    base_scores: list[float] = []
    recall: dict[int, list[float]] = {n: [] for n in rerank_ns}
    rerank_scores: dict[int, list[float]] = {n: [] for n in rerank_ns}

    t0 = time.time()
    for qi, row in enumerate(rows):
        qid = row["query_id"]
        rels = qrels.get(qid, {})
        cands = row["candidates"]
        cand_docs = [c["doc_id"] for c in cands]
        cand_text = [c["content"] for c in cands]

        # Baseline: stage-1 hybrid order over the full shortlist.
        base = ndcg_at_k_graded(aggregate_by_doc(cand_docs), rels, 10)
        base_scores.append(base)

        # One forward pass over all candidates; slice per cutoff.
        scores = reranker.rerank_scores(row["query_text"], cand_text)
        order_full = sorted(range(len(cands)), key=lambda i: -scores[i])

        for n in rerank_ns:
            head_idx = list(range(min(n, len(cands))))
            # recall ceiling: any gold doc present among the first n candidates
            docs_in_head = {cand_docs[i] for i in head_idx}
            recall[n].append(1.0 if any(d in rels for d in docs_in_head) else 0.0)
            # rerank only the first n; their internal order is by cosine
            reranked_head = sorted(head_idx, key=lambda i: -scores[i])
            ranked_docs = aggregate_by_doc([cand_docs[i] for i in reranked_head])
            rerank_scores[n].append(ndcg_at_k_graded(ranked_docs, rels, 10))

        if (qi + 1) % 50 == 0:
            sys.stderr.write(
                f"  {qi+1}/{len(rows)} ({time.time()-t0:.0f}s, "
                f"{reranker.encode_calls} fwd)\n"
            )
            sys.stderr.flush()

    base = sum(base_scores) / len(base_scores)
    print("\n=== M0 CoIR-CSN-Python (hybrid stage-1 → CodeRankEmbed rerank) ===")
    print(f"queries: {len(rows)}   baseline hybrid NDCG@10: {base:.4f}")
    print(f"(BENCH.md reference hybrid@1000-subsample: 0.7839; bm25: 0.8743)")
    print(f"\n{'rerankN':>8} {'recall@N':>9} {'rerank NDCG':>12} {'Δ vs hybrid':>12}")
    print(f"{'-'*8} {'-'*9} {'-'*12} {'-'*12}")
    for n in rerank_ns:
        rr = sum(rerank_scores[n]) / len(rerank_scores[n])
        rc = sum(recall[n]) / len(recall[n])
        print(f"{n:>8} {rc:>9.4f} {rr:>12.4f} {rr-base:>+12.4f}")
    print(
        f"\nmodel forward passes: {reranker.encode_calls}  "
        f"(cache hits: {reranker.cache_hits})"
    )


# ── semble path ──────────────────────────────────────────────────────────────


def _bootstrap_semble(checkout: str):
    root = Path(checkout).expanduser().resolve()
    if not (root / "benchmarks" / "data.py").exists():
        sys.exit(f"semble checkout not found at {root} (pass --semble-checkout)")
    sys.path.insert(0, str(root))

    import types as _types

    sm = _types.ModuleType("semble")
    smt = _types.ModuleType("semble.types")

    class SearchResult:  # type-annotation placeholder
        pass

    smt.SearchResult = SearchResult
    sys.modules.setdefault("semble", sm)
    sys.modules.setdefault("semble.types", smt)
    return root


def run_semble(
    reranker,
    rerank_ns: list[int],
    *,
    ken_bin: str,
    model_dir: str,
    chunker: str,
    max_cand: int,
    checkout: str,
    repos_filter: list[str],
) -> None:
    _bootstrap_semble(checkout)
    from coderank_model import QUERY_PREFIX
    from benchmarks.data import (  # type: ignore
        available_repo_specs,
        grouped_tasks,
        load_tasks,
    )
    from benchmarks.metrics import ndcg_at_k, target_rank  # type: ignore

    class _Chunk:
        __slots__ = ("file_path", "start_line", "end_line")

        def __init__(self, fp, s, e):
            self.file_path, self.start_line, self.end_line = fp, s, e

    class _Res:
        __slots__ = ("chunk",)

        def __init__(self, c):
            self.chunk = c

    def shim(records: list[dict]):
        return [
            _Res(_Chunk(r["file_path"], r["start_line"], r["end_line"]))
            for r in records
        ]

    repo_specs = available_repo_specs()
    if not repo_specs:
        sys.exit("no semble repo checkouts — run benchmarks/sync_repos.py first")
    tasks = load_tasks(repo_specs)
    if repos_filter:
        tasks = [t for t in tasks if t.repo in repos_filter]
    grouped = grouped_tasks(tasks)
    sys.stderr.write(f"[semble] {len(grouped)} repos, {len(tasks)} tasks, k={max_cand}\n")

    # accumulators: per-cutoff and per-category
    base_all: list[float] = []
    rr_all: dict[int, list[float]] = {n: [] for n in rerank_ns}
    recall_all: dict[int, list[float]] = {n: [] for n in rerank_ns}
    base_cat: dict[str, list[float]] = defaultdict(list)
    rr_cat: dict[int, dict[str, list[float]]] = {n: defaultdict(list) for n in rerank_ns}

    # Score-blend probe (plan §9.3): does β·rerankCos + (1-β)·fusedScore beat
    # pure replacement / pure hybrid? Evaluated at the deepest cutoff. β=0 is
    # pure hybrid order (over the head), β=1 is pure rerank.
    betas = [0.0, 0.25, 0.5, 0.75, 1.0]
    blend_cutoff = max(rerank_ns)
    blend_all: dict[float, list[float]] = {b: [] for b in betas}
    blend_cat: dict[float, dict[str, list[float]]] = {b: defaultdict(list) for b in betas}

    t0 = time.time()

    # Pass 1: drive ken bench for every repo and collect (task, record) pairs.
    # Done before any encoding so we can do ONE global, length-sorted prewarm
    # instead of one per repo — the per-repo prewarm paid a large MPS
    # kernel-recompile penalty on each repo's new length distribution.
    pairs: list[tuple[object, dict]] = []  # (task, record)
    for ri, (repo_name, repo_tasks) in enumerate(sorted(grouped.items())):
        spec = repo_specs[repo_name]
        stdin_text = "\n".join(t.query for t in repo_tasks) + "\n"
        args = [
            ken_bin, "bench", str(spec.benchmark_dir),
            "--mode", "hybrid", "--chunker", chunker, "-k", str(max_cand),
            "--model", model_dir,
        ]
        proc = subprocess.run(args, input=stdin_text, capture_output=True, text=True)
        if proc.returncode != 0:
            sys.stderr.write(f"  {repo_name}: ken bench failed rc={proc.returncode}\n{proc.stderr[:500]}\n")
            sys.stderr.flush()
            continue
        # Split on '\n' only: ken bench terminates each record with '\n' and
        # escapes any newline inside content, but str.splitlines() would also
        # break on raw U+0085/U+2028/U+2029/FF that Go emits inside string
        # values, corrupting a record mid-string.
        try:
            records = [json.loads(l) for l in proc.stdout.split("\n") if l.strip()]
        except json.JSONDecodeError as e:
            sys.stderr.write(f"  {repo_name}: JSON parse failed ({e}); skipping repo\n")
            sys.stderr.flush()
            continue
        if len(records) != len(repo_tasks):
            sys.stderr.write(f"  warn {repo_name}: {len(records)} records != {len(repo_tasks)} tasks\n")
        for task, rec in zip(repo_tasks, records):
            pairs.append((task, rec))
        sys.stderr.write(f"  [bench {ri+1}/{len(grouped)}] {repo_name} ({time.time()-t0:.0f}s)\n")
        sys.stderr.flush()

    # Pass 2: ONE global prewarm over every query + candidate.
    warm = [QUERY_PREFIX + task.query for task, _ in pairs]
    for _, rec in pairs:
        warm.extend(r["content"] for r in rec["results"])
    reranker.prewarm(warm)

    # Pass 3: score every task from cached embeddings (cheap arithmetic).
    for pi, (task, rec) in enumerate(pairs):
        results = rec["results"]
        if True:
            if not results:
                base_all.append(0.0)
                base_cat[task.category].append(0.0)
                for n in rerank_ns:
                    rr_all[n].append(0.0)
                    recall_all[n].append(0.0)
                    rr_cat[n][task.category].append(0.0)
                for b in betas:
                    blend_all[b].append(0.0)
                    blend_cat[b][task.category].append(0.0)
                continue
            n_rel = len(task.all_relevant)

            # baseline NDCG@10 over the stage-1 order
            base_ranks = [r for t in task.all_relevant if (r := target_rank(shim(results), t)) is not None]
            base_ndcg = ndcg_at_k(base_ranks, n_rel, 10)
            base_all.append(base_ndcg)
            base_cat[task.category].append(base_ndcg)

            # one forward pass over all candidates; slice per cutoff
            scores = reranker.rerank_scores(task.query, [r["content"] for r in results])

            for n in rerank_ns:
                head = results[:n]
                tail = results[n:]
                # recall ceiling: any target covered within first n stage-1 results
                rc = any(
                    target_rank(shim(head), t) is not None for t in task.all_relevant
                )
                recall_all[n].append(1.0 if rc else 0.0)
                # rerank the head by cosine, keep tail in stage-1 order
                head_order = sorted(range(len(head)), key=lambda i: -scores[i])
                reordered = [head[i] for i in head_order] + tail
                ranks = [r for t in task.all_relevant if (r := target_rank(shim(reordered), t)) is not None]
                nd = ndcg_at_k(ranks, n_rel, 10)
                rr_all[n].append(nd)
                rr_cat[n][task.category].append(nd)

            # blend probe at the deepest cutoff: normalize fused score + cosine
            head = results[:blend_cutoff]
            tail = results[blend_cutoff:]
            cos_n = minmax([float(scores[i]) for i in range(len(head))])
            fused_n = minmax([head[i]["score"] for i in range(len(head))])
            for b in betas:
                blended = [b * cos_n[i] + (1.0 - b) * fused_n[i] for i in range(len(head))]
                order = sorted(range(len(head)), key=lambda i: -blended[i])
                reordered = [head[i] for i in order] + tail
                ranks = [r for t in task.all_relevant if (r := target_rank(shim(reordered), t)) is not None]
                nd = ndcg_at_k(ranks, n_rel, 10)
                blend_all[b].append(nd)
                blend_cat[b][task.category].append(nd)

        if (pi + 1) % 200 == 0:
            sys.stderr.write(f"  [score {pi+1}/{len(pairs)}] ({time.time()-t0:.0f}s)\n")
            sys.stderr.flush()

    def avg(xs):
        return sum(xs) / len(xs) if xs else 0.0

    base = avg(base_all)
    print("\n=== M0 semble bench (hybrid stage-1 → CodeRankEmbed rerank) ===")
    print(f"tasks: {len(base_all)}   baseline hybrid NDCG@10: {base:.4f}")
    print(f"(BENCH.md/semble reference hybrid: ~0.84-0.85)")
    print(f"\n{'rerankN':>8} {'recall@N':>9} {'rerank NDCG':>12} {'Δ vs hybrid':>12}")
    print(f"{'-'*8} {'-'*9} {'-'*12} {'-'*12}")
    for n in rerank_ns:
        print(f"{n:>8} {avg(recall_all[n]):>9.4f} {avg(rr_all[n]):>12.4f} {avg(rr_all[n])-base:>+12.4f}")

    cats = sorted(base_cat.keys())
    print(f"\nPer-category Δ vs hybrid (the isSymbolQuery-skip §9.3 test):")
    header = f"{'category':>14} {'baseline':>9} " + " ".join(f"{'Δ@'+str(n):>8}" for n in rerank_ns)
    print(header)
    print("-" * len(header))
    for c in cats:
        b = avg(base_cat[c])
        deltas = " ".join(f"{avg(rr_cat[n][c])-b:>+8.4f}" for n in rerank_ns)
        print(f"{c:>14} {b:>9.4f} {deltas}")

    print(f"\nScore-blend probe at cutoff={blend_cutoff} "
          f"(β·rerankCos + (1-β)·fusedScore; β=0 ≈ hybrid head, β=1 = pure rerank):")
    print(f"{'β':>6} {'NDCG@10':>9} {'Δ vs hybrid':>12}")
    print(f"{'-'*6} {'-'*9} {'-'*12}")
    for b in betas:
        print(f"{b:>6.2f} {avg(blend_all[b]):>9.4f} {avg(blend_all[b])-base:>+12.4f}")
    print(f"\nBlend Δ vs hybrid per category:")
    bhdr = f"{'category':>14} " + " ".join(f"{'β='+format(b,'.2f'):>9}" for b in betas)
    print(bhdr)
    print("-" * len(bhdr))
    for c in cats:
        cb = avg(base_cat[c])
        print(f"{c:>14} " + " ".join(f"{avg(blend_cat[b][c])-cb:>+9.4f}" for b in betas))
    print(
        f"\nmodel forward passes: {reranker.encode_calls}  "
        f"(cache hits: {reranker.cache_hits})"
    )


# ── main ──────────────────────────────────────────────────────────────────


def main() -> int:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--bench", choices=["coir", "semble", "both"], default="both")
    p.add_argument("--rerank-ns", default="10,25,50,100", help="comma-separated rerank depths")
    p.add_argument("--ken", default=os.environ.get("KEN_BIN", "ken"))
    p.add_argument("--model", default=os.environ.get("KEN_MODEL_DIR", str(Path.home() / ".ken" / "model")))
    p.add_argument("--chunker", default="regex")
    p.add_argument("--max-cand", type=int, default=100, help="stage-1 shortlist depth (ken bench -k)")
    p.add_argument("--coir-limit", type=int, default=0, help="truncate CoIR to first N queries (0=all)")
    p.add_argument("--semble-checkout", default=os.environ.get("SEMBLE_CHECKOUT", "/tmp/semble"))
    p.add_argument("--repo", action="append", default=[], help="limit semble to these repos")
    p.add_argument("--device", default=None, help="mps/cuda/cpu (default: auto)")
    p.add_argument("--batch-size", type=int, default=64)
    p.add_argument("--max-seq-length", type=int, default=512)
    args = p.parse_args()

    rerank_ns = sorted({int(x) for x in args.rerank_ns.split(",") if x.strip()})
    if not rerank_ns:
        sys.exit("--rerank-ns produced no values")

    from coderank_model import CodeRankReranker

    reranker = CodeRankReranker(
        device=args.device,
        max_seq_length=args.max_seq_length,
        batch_size=args.batch_size,
    )

    bench_dir = REPO_ROOT / "testdata" / "bench" / "coir-csn-python"
    if args.bench in ("coir", "both"):
        run_coir(reranker, rerank_ns, bench_dir, limit=args.coir_limit)
    if args.bench in ("semble", "both"):
        run_semble(
            reranker, rerank_ns,
            ken_bin=args.ken, model_dir=args.model, chunker=args.chunker,
            max_cand=args.max_cand, checkout=args.semble_checkout,
            repos_filter=args.repo,
        )
    return 0


if __name__ == "__main__":
    sys.exit(main())
