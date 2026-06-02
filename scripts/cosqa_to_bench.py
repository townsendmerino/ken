#!/usr/bin/env python3
"""
cosqa_to_bench.py — convert CoSQA dev set into the ken bench format
(testdata/bench/cosqa-python/{corpus,queries.jsonl,qrels.jsonl}) so we
can run the Stage 7a / 8 NDCG harness against it.

CoSQA's dev split is 604 query-code pairs (313 positive, 291 negative).
The casual web-search register ("python check relation is symmetric")
is the realism gate flagged at M0d: enrichment that helps on
csn-python-nl-stripped (docstring-shaped queries) may not transfer.

Output format mirrors csn-python-nl-stripped:
- corpus/<doc_id>.py        — one Python function per file, DOCSTRING
                              STRIPPED (so the heur label isn't
                              trivially leaked by the function's own
                              docstring)
- queries.jsonl             — {"query_id": "...", "text": "..."}
- qrels.jsonl               — {"query_id": "...", "doc_id": "...", "score": 1.0}
- summary.json              — corpus stats

Source: microsoft/CodeXGLUE main, NL-code-search-WebQuery/CoSQA/cosqa-dev.json.
"""

from __future__ import annotations

import ast
import json
import urllib.request
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
OUT_DIR = REPO_ROOT / "testdata" / "bench" / "cosqa-python"
SRC_URL = (
    "https://raw.githubusercontent.com/microsoft/CodeXGLUE/main/"
    "Text-Code/NL-code-search-WebQuery/CoSQA/cosqa-dev.json"
)
CACHE = Path("/tmp/cosqa-dev.json")


def strip_docstring(src: str) -> str:
    """Remove the leading docstring of the top-level function in src,
    preserving every other byte. Returns src unchanged if no parse, no
    function, or no docstring."""
    try:
        tree = ast.parse(src)
    except SyntaxError:
        return src
    fn = None
    for node in tree.body:
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
            fn = node
            break
    if fn is None or not fn.body:
        return src
    first = fn.body[0]
    if not (
        isinstance(first, ast.Expr)
        and isinstance(first.value, ast.Constant)
        and isinstance(first.value.value, str)
    ):
        return src
    # Slice out the docstring's source range. ast columns are 1-based
    # for lineno but 0-based for col_offset. We rewrite line-by-line
    # so dedent / whitespace stays exact.
    lines = src.splitlines(keepends=True)
    start_line = first.lineno - 1  # 0-based
    end_line = (first.end_lineno or first.lineno) - 1
    if start_line == end_line:
        # Single-line docstring: blank that line out, keep newline.
        line = lines[start_line]
        # Find the docstring span on this line and replace just that.
        s = first.col_offset
        e = first.end_col_offset
        lines[start_line] = line[:s] + line[e:]
    else:
        # Multi-line: blank the first line (post-col), drop full middle
        # lines, then trim the last.
        first_line = lines[start_line]
        last_line = lines[end_line]
        lines[start_line] = first_line[: first.col_offset]
        for i in range(start_line + 1, end_line):
            lines[i] = ""
        lines[end_line] = last_line[first.end_col_offset :]
    # Clean: collapse the now-empty area into a single blank line if
    # we just left whitespace and newlines.
    out = "".join(lines)
    # The function body still has a "    pass"-equivalent: if removing
    # the docstring leaves an empty function body, Python won't parse.
    # CoSQA functions have real bodies after the docstring; we don't
    # bother backfilling pass.
    return out


def main() -> int:
    if not CACHE.exists():
        print(f"fetching {SRC_URL}")
        urllib.request.urlretrieve(SRC_URL, CACHE)
    rows = json.loads(CACHE.read_text())

    corpus_dir = OUT_DIR / "corpus"
    corpus_dir.mkdir(parents=True, exist_ok=True)

    # Build a stable code→doc_id mapping over ALL distinct codes
    # (positive AND negative); the negatives are distractors that make
    # retrieval non-trivial.
    code_to_id: dict[str, str] = {}
    for r in rows:
        code = r["code"]
        if code not in code_to_id:
            code_to_id[code] = f"cosqa-{len(code_to_id):05d}"

    # Write corpus with docstrings stripped.
    n_stripped = 0
    n_unparseable = 0
    for code, doc_id in code_to_id.items():
        stripped = strip_docstring(code)
        if stripped != code:
            n_stripped += 1
        try:
            ast.parse(stripped)
        except SyntaxError:
            n_unparseable += 1
        (corpus_dir / f"{doc_id}.py").write_text(stripped)

    # Build queries + qrels from the POSITIVE rows only.
    pos_rows = [r for r in rows if r["label"] == 1]
    seen_queries: set[str] = set()
    queries_path = OUT_DIR / "queries.jsonl"
    qrels_path = OUT_DIR / "qrels.jsonl"
    n_queries = 0
    n_qrels = 0
    with queries_path.open("w") as qf, qrels_path.open("w") as rf:
        for r in pos_rows:
            qid = r["idx"]  # e.g. cosqa-dev-3
            qtext = r["doc"]
            doc_id = code_to_id[r["code"]]
            if qid not in seen_queries:
                qf.write(json.dumps({"query_id": qid, "text": qtext}) + "\n")
                seen_queries.add(qid)
                n_queries += 1
            rf.write(
                json.dumps({"query_id": qid, "doc_id": doc_id, "score": 1.0})
                + "\n"
            )
            n_qrels += 1

    summary = {
        "corpus_files": len(code_to_id),
        "docstrings_stripped": n_stripped,
        "unparseable_after_strip": n_unparseable,
        "queries": n_queries,
        "qrels": n_qrels,
        "positive_rows": len(pos_rows),
        "negative_rows": len(rows) - len(pos_rows),
    }
    (OUT_DIR / "summary.json").write_text(json.dumps(summary, indent=2))
    print(json.dumps(summary, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
