#!/usr/bin/env python3
"""
bench_cosqa_heur.py — apply the M0d Arm B heuristic enrichment to the
cosqa-python bench, mirroring scripts/bench_csn_nl_stripped_heur.py
for the casual-register dataset.

This is the materializer half of the Stage 8 CosQA realism gate. The
gate question: when queries are casual web-search NL ("python check
relation is symmetric") instead of docstring-shaped NL, does the same
heuristic prefix line that helped csn-python-nl-stripped still
help — or does the win evaporate?

Reuses the exact heuristic line format from
bench_csn_nl_stripped_heur.py:

  # func: <name> | calls: <comma-sep callee names> | raises: <exception types>

Outputs:
  testdata/bench/cosqa-python-heur/       — heur prefix + body (Arm B)

queries.jsonl and qrels.jsonl are symlinked from cosqa-python (corpus-
independent). HyDE snippets are NOT generated here — the realism gate
runs without rerank to isolate the enrichment effect.
"""

from __future__ import annotations

import ast
import json
import os
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
SRC_DIR = REPO_ROOT / "testdata" / "bench" / "cosqa-python"
OUT_DIR_FULL = REPO_ROOT / "testdata" / "bench" / "cosqa-python-heur"


SKIP = {
    "len", "range", "str", "int", "float", "bool", "list", "dict",
    "set", "tuple", "type", "isinstance", "hasattr", "getattr",
    "setattr", "super", "print", "open", "format", "sorted",
    "reversed", "enumerate", "zip", "map", "filter", "any", "all",
    "next", "iter", "sum", "min", "max", "abs", "round",
    "self", "cls", "args", "kwargs",
}


def _name_from_func(node) -> str:
    if isinstance(node, ast.Name):
        return node.id
    if isinstance(node, ast.Attribute):
        return node.attr
    return ""


def _name_from_exc(node) -> str:
    if node is None:
        return ""
    if isinstance(node, ast.Name):
        return node.id
    if isinstance(node, ast.Call):
        return _name_from_func(node.func)
    if isinstance(node, ast.Attribute):
        return node.attr
    return ""


def extract_heuristic(src: str) -> str | None:
    try:
        tree = ast.parse(src)
    except SyntaxError:
        return None

    func_name = ""
    calls: list[str] = []
    raises: list[str] = []
    seen_calls: set[str] = set()
    seen_raises: set[str] = set()

    for node in ast.walk(tree):
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
            if not func_name:
                func_name = node.name
        elif isinstance(node, ast.Call):
            name = _name_from_func(node.func)
            if name and name not in SKIP and name not in seen_calls:
                seen_calls.add(name)
                calls.append(name)
        elif isinstance(node, ast.Raise):
            name = _name_from_exc(node.exc)
            if name and name not in SKIP and name not in seen_raises:
                seen_raises.add(name)
                raises.append(name)

    parts = []
    if func_name:
        parts.append(f"func: {func_name}")
    if calls:
        parts.append("calls: " + ", ".join(calls[:8]))
    if raises:
        parts.append("raises: " + ", ".join(raises[:4]))
    if not parts:
        return None
    return "# " + " | ".join(parts) + "\n"


def _symlink(target: Path, link: Path) -> None:
    if link.is_symlink() or link.exists():
        try:
            current = link.resolve()
        except FileNotFoundError:
            current = None
        if current == target.resolve():
            return
        link.unlink()
    link.symlink_to(target)


def main() -> None:
    if not SRC_DIR.exists():
        raise SystemExit(
            f"missing {SRC_DIR} — run `python scripts/cosqa_to_bench.py` first"
        )

    (OUT_DIR_FULL / "corpus").mkdir(parents=True, exist_ok=True)

    src_corpus = SRC_DIR / "corpus"
    written = 0
    no_heur = 0
    heur_len_sum = 0
    sample_full: list[tuple[str, str]] = []

    for src_path in sorted(src_corpus.iterdir()):
        if not src_path.name.endswith(".py"):
            continue
        original = src_path.read_text()
        prefix = extract_heuristic(original)
        out_path = OUT_DIR_FULL / "corpus" / src_path.name
        if prefix is None:
            no_heur += 1
            # Fall back to the original so the doc-id set stays
            # identical (every qrel still has a target file).
            out_path.write_text(original)
        else:
            heur_len_sum += len(prefix)
            out_path.write_text(prefix + original)
            if len(sample_full) < 3:
                sample_full.append((src_path.name, (prefix + original)[:300]))
        written += 1

    for name in ("queries.jsonl", "qrels.jsonl"):
        _symlink(SRC_DIR / name, OUT_DIR_FULL / name)

    mean_heur_len = heur_len_sum / max(written - no_heur, 1)
    summary = {
        "corpus_written": written,
        "no_heuristic_fallback_to_original": no_heur,
        "mean_heuristic_line_chars": round(mean_heur_len, 1),
        "sample_full": [{"name": n, "head": t} for n, t in sample_full],
    }
    (OUT_DIR_FULL / "summary.json").write_text(json.dumps(summary, indent=2))
    print(json.dumps(summary, indent=2))


if __name__ == "__main__":
    main()
