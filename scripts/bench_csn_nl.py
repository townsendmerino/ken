#!/usr/bin/env python3
"""
bench_csn_nl.py — derive an NL→code retrieval benchmark from the
on-disk CoIR-CSN-Python materials.

Why this exists: the canonical CoIR-CSN-Python split that
`bench_coir.py` downloads is CCR (code→comment) — queries are full
function source, "corpus" docs are just the docstring text. That's
the wrong direction for the Stage-7a HyDE M0: HyDE is designed to
bridge an NL-question → code embedding gap, and CCR queries already
live in code-space.

This script inverts the existing data into the canonical CSN NL→code
direction (Husain et al. 2019), reusing material already on disk so
the M0 doesn't need a new network fetch:

  Inputs (must already exist; produced by bench_coir.py):
    testdata/bench/coir-csn-python/queries.jsonl   (function source)
    testdata/bench/coir-csn-python/qrels.jsonl     (query_id ↔ doc_id)

  Outputs (under testdata/bench/csn-python-nl/, gitignored):
    queries.jsonl     {query_id, text} — text = docstring extracted
                                          from the original function
    corpus/<orig_query_id>.py — the original function source, kept as-is
                                (incl. the docstring) per standard CSN
                                convention. Stripping the docstring would
                                make BM25 useless and diverge from the
                                published benchmark.
    qrels.jsonl       {query_id, doc_id, score} — same query↔doc pairs,
                                roles swapped: new query_id = old doc_id,
                                new doc_id = old query_id.

Queries with no extractable docstring (1.3% of rows on the current
materialization) are dropped — they'd give an empty NL query.

Idempotent: existing files are kept; missing ones get written. Run
from anywhere.
"""

from __future__ import annotations

import json
import re
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
SRC_DIR = REPO_ROOT / "testdata" / "bench" / "coir-csn-python"
SRC_QUERIES = SRC_DIR / "queries.jsonl"
SRC_QRELS = SRC_DIR / "qrels.jsonl"

OUT_DIR = REPO_ROOT / "testdata" / "bench" / "csn-python-nl"
OUT_QUERIES = OUT_DIR / "queries.jsonl"
OUT_QRELS = OUT_DIR / "qrels.jsonl"
OUT_CORPUS = OUT_DIR / "corpus"
OUT_SUMMARY = OUT_DIR / "summary.json"

# First triple-quoted block (single or double) after the def header.
# Matched non-greedily; DOTALL so multi-line docstrings work.
DOCSTRING_RE = re.compile(r'(?:\"\"\"(.*?)\"\"\"|\'\'\'(.*?)\'\'\')', re.DOTALL)


def extract_docstring(src: str) -> str:
    m = DOCSTRING_RE.search(src)
    if not m:
        return ""
    return (m.group(1) or m.group(2) or "").strip()


def main() -> None:
    for p in (SRC_QUERIES, SRC_QRELS):
        if not p.exists():
            raise SystemExit(
                f"missing {p} — run `python scripts/bench_coir.py` first"
            )

    OUT_DIR.mkdir(parents=True, exist_ok=True)
    OUT_CORPUS.mkdir(parents=True, exist_ok=True)

    # Pass 1: build queries.jsonl from extracted docstrings, while
    # writing each function's source as a corpus doc keyed by its
    # original query_id. Skip rows with no extractable docstring.
    kept = 0
    dropped_no_docstring = 0
    docstring_lens: list[int] = []
    with open(SRC_QUERIES) as fin, open(OUT_QUERIES, "w") as fout:
        for line in fin:
            row = json.loads(line)
            qid = row["query_id"]
            src = row["text"]
            ds = extract_docstring(src)
            if not ds:
                dropped_no_docstring += 1
                continue
            # The function body (original "query.text") becomes the
            # corpus doc, written as <orig_query_id>.py so the
            # aggregateByDoc step in bench/ndcg/coir_test.go.aggregateByDoc
            # can derive the doc_id by stripping ".py".
            doc_path = OUT_CORPUS / f"{qid}.py"
            if not doc_path.exists():
                doc_path.write_text(src)
            # The new query keeps the original doc_id (c265608, etc.) so
            # the qrels swap below is a pure id-pair flip with no
            # renumbering. The text is the docstring.
            # We need the original doc_id from qrels to know what to
            # name this query — populated in pass 2 below. For now,
            # record the function-body→docstring mapping keyed by qid.
            json.dump({"query_id": qid, "_docstring": ds}, fout)
            fout.write("\n")
            kept += 1
            docstring_lens.append(len(ds))

    # Build a qid→ds map from the temp output, then rewrite using the
    # actual swapped IDs from qrels.
    temp = OUT_QUERIES.read_text().splitlines()
    qid_to_ds = {json.loads(l)["query_id"]: json.loads(l)["_docstring"] for l in temp}

    swapped_pairs: list[tuple[str, str]] = []  # (new_query_id, new_doc_id)
    new_query_texts: dict[str, str] = {}  # new_query_id → docstring
    with open(SRC_QRELS) as fin:
        for line in fin:
            r = json.loads(line)
            orig_qid = r["query_id"]
            orig_did = r["doc_id"]
            if orig_qid not in qid_to_ds:
                continue  # query dropped (no docstring) → drop its qrels too
            new_qid = orig_did  # old doc → new query
            new_did = orig_qid  # old query → new doc
            new_query_texts.setdefault(new_qid, qid_to_ds[orig_qid])
            swapped_pairs.append((new_qid, new_did))

    # Write the real queries.jsonl (one row per *new* query_id).
    with open(OUT_QUERIES, "w") as fout:
        for new_qid, text in sorted(new_query_texts.items()):
            json.dump({"query_id": new_qid, "text": text}, fout)
            fout.write("\n")

    # Write qrels with swapped roles, score 1.0 (CCR qrels were all 1.0).
    with open(OUT_QRELS, "w") as fout:
        for new_qid, new_did in swapped_pairs:
            json.dump({"query_id": new_qid, "doc_id": new_did, "score": 1.0}, fout)
            fout.write("\n")

    summary = {
        "queries_in": kept + dropped_no_docstring,
        "queries_out": len(new_query_texts),
        "dropped_no_docstring": dropped_no_docstring,
        "corpus_docs": kept,
        "qrel_rows": len(swapped_pairs),
        "docstring_len_mean": sum(docstring_lens) / max(len(docstring_lens), 1),
        "docstring_len_median": sorted(docstring_lens)[len(docstring_lens) // 2]
        if docstring_lens
        else 0,
    }
    OUT_SUMMARY.write_text(json.dumps(summary, indent=2))
    print(json.dumps(summary, indent=2))


if __name__ == "__main__":
    main()
