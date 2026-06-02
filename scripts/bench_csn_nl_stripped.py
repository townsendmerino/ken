#!/usr/bin/env python3
"""
bench_csn_nl_stripped.py — Phase A leak-free variant of csn-python-nl.

Why: csn-python-nl keeps the docstring inside the function source per
standard CSN convention. That means the docstring (which IS the query)
appears verbatim inside the corpus document. BM25 trivially aligns on
that lexical leak, and even the dense retriever benefits — the doc and
query share large substrings. The Stage-7a M0 measurement on
csn-python-nl shows stage-1 hybrid hits Recall@100 = 1.000 and
Recall@10 = 0.979, leaving virtually no headroom for any query-side
transform to help.

This script materializes a *stripped* variant where the docstring is
removed from each corpus document. Queries and qrels are reused
unchanged (symlinked); the HyDE snippet cache is reused too. The only
delta vs csn-python-nl is the corpus content — every function body has
its leading docstring scrubbed.

Outputs (under testdata/bench/csn-python-nl-stripped/, gitignored):

  corpus/<doc_id>.py — function source with the first triple-quoted
                       block (the docstring) removed. Comments and
                       string literals deeper in the function are kept.
  queries.jsonl       — symlinked to csn-python-nl/queries.jsonl
  qrels.jsonl         — symlinked to csn-python-nl/qrels.jsonl
  hyde-snippets-*.jsonl — symlinked from csn-python-nl/ (the snippets
                       are query-derived; the corpus change doesn't
                       invalidate them)
  summary.json        — counts + a sample of stripped vs original

Idempotent: existing files are kept; missing ones get written.
Run from anywhere.
"""

from __future__ import annotations

import json
import re
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
SRC_DIR = REPO_ROOT / "testdata" / "bench" / "csn-python-nl"
OUT_DIR = REPO_ROOT / "testdata" / "bench" / "csn-python-nl-stripped"
OUT_CORPUS = OUT_DIR / "corpus"
OUT_SUMMARY = OUT_DIR / "summary.json"

# Greedy match of the first triple-quoted block in the file. Both
# styles, DOTALL so multi-line works. We deliberately match the FIRST
# occurrence only — inner string literals (which may legitimately be
# triple-quoted) are left alone.
DOCSTRING_RE = re.compile(r'(?:\"\"\"(?:.*?)\"\"\"|\'\'\'(?:.*?)\'\'\')', re.DOTALL)


def strip_docstring(src: str) -> str:
    """Remove the first triple-quoted block from src, plus the line of
    whitespace it sat on if the block now leaves an empty line.
    """
    m = DOCSTRING_RE.search(src)
    if not m:
        return src
    start, end = m.span()
    # Extend to swallow the trailing newline (and any preceding spaces
    # on the same line) so we don't leave a dangling blank line where
    # the docstring used to be.
    line_start = src.rfind("\n", 0, start) + 1
    pre = src[line_start:start]
    if pre.strip() == "":
        start = line_start
    # Advance past the newline trailing the closing quotes, if any.
    if end < len(src) and src[end] == "\n":
        end += 1
    return src[:start] + src[end:]


def _symlink(target: Path, link: Path) -> None:
    """Symlink target → link, idempotently. If link already exists and
    points to target, do nothing; if it exists pointing elsewhere, warn
    and rewrite."""
    if link.is_symlink() or link.exists():
        try:
            current = link.resolve()
        except OSError:
            current = None
        if current == target.resolve():
            return
        link.unlink()
    link.symlink_to(target)


def main() -> None:
    if not SRC_DIR.exists():
        raise SystemExit(
            f"missing {SRC_DIR} — run `python scripts/bench_csn_nl.py` first"
        )

    OUT_DIR.mkdir(parents=True, exist_ok=True)
    OUT_CORPUS.mkdir(parents=True, exist_ok=True)

    # 1. Strip docstrings from each corpus doc, writing the result to
    #    the new corpus dir. Skip files whose stripped form is empty
    #    (just to be safe — shouldn't happen since CSN functions always
    #    have a body, but worth not silently producing zero-byte docs).
    src_corpus = SRC_DIR / "corpus"
    written = 0
    no_docstring = 0
    empty_after_strip = 0
    sample_before: list[tuple[str, str]] = []
    sample_after: list[tuple[str, str]] = []
    for src_path in sorted(src_corpus.iterdir()):
        if not src_path.name.endswith(".py"):
            continue
        original = src_path.read_text()
        stripped = strip_docstring(original)
        if stripped == original:
            no_docstring += 1
        if not stripped.strip():
            empty_after_strip += 1
            continue
        dst_path = OUT_CORPUS / src_path.name
        if not dst_path.exists():
            dst_path.write_text(stripped)
        written += 1
        if len(sample_before) < 3:
            sample_before.append((src_path.name, original[:200]))
            sample_after.append((src_path.name, stripped[:200]))

    # 2. Symlink queries.jsonl + qrels.jsonl + every cached snippet
    #    JSONL into the new bench dir. These artefacts are independent
    #    of the corpus content.
    for name in ["queries.jsonl", "qrels.jsonl"]:
        _symlink(SRC_DIR / name, OUT_DIR / name)
    for path in SRC_DIR.glob("hyde-snippets-*.jsonl"):
        _symlink(path, OUT_DIR / path.name)

    summary = {
        "corpus_written": written,
        "no_docstring_in_source": no_docstring,
        "empty_after_strip": empty_after_strip,
        "sample_before": [{"name": n, "head": t} for n, t in sample_before],
        "sample_after": [{"name": n, "head": t} for n, t in sample_after],
    }
    OUT_SUMMARY.write_text(json.dumps(summary, indent=2))
    print(json.dumps(summary, indent=2))


if __name__ == "__main__":
    main()
