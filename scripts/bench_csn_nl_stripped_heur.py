#!/usr/bin/env python3
"""
bench_csn_nl_stripped_heur.py — Stage-7a M0d Arm B materializer.

For each chunk in csn-python-nl-stripped/corpus/, extract a deterministic
AST-derived context line and prepend it to the chunk text. Materializes
two variants under testdata/bench/:

  csn-python-nl-stripped-heur/      — heuristic line + original chunk body
                                       (Arm B full-enriched)
  csn-python-nl-stripped-heur-only/ — heuristic line only, no original
                                       body (Arm B sniff ablation)

The heuristic line, one per chunk:

  # func: <name> | calls: <comma-sep callee names> | raises: <exception types>

Empty sections are omitted. Function name comes from the top-level
ast.FunctionDef / ast.AsyncFunctionDef; callee names from ast.Call
nodes (capturing both Name.id and Attribute.attr forms); raises from
ast.Raise exception class names.

Idempotent: existing files kept; missing ones written. queries.jsonl,
qrels.jsonl, and the HyDE snippet caches are symlinked from the
unaugmented stripped bench (they're corpus-independent).

The bench layer measures: does the enriched corpus surface the qrel
into the rerank top-50 on queries where the unaugmented baseline
missed it? "Rescuing one of the M0c 40" = qrel ∈ top-50 on enriched
when it wasn't on unaugmented. The harness handles that comparison
via the merge step.
"""

from __future__ import annotations

import ast
import json
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
SRC_DIR = REPO_ROOT / "testdata" / "bench" / "csn-python-nl-stripped"
OUT_DIR_FULL = REPO_ROOT / "testdata" / "bench" / "csn-python-nl-stripped-heur"
OUT_DIR_HEUR = REPO_ROOT / "testdata" / "bench" / "csn-python-nl-stripped-heur-only"


def _builtin_skips() -> set[str]:
    """Common-enough Python builtins/idioms that add no retrieval signal
    when surfaced via the heuristic. The oracle/PRF stopword list isn't
    quite the right cut here (it's broader, drops `init` etc.) — for the
    heuristic specifically we want to drop syntactic noise but keep
    domain identifiers."""
    return {
        "len", "range", "str", "int", "float", "bool", "list", "dict",
        "set", "tuple", "type", "isinstance", "hasattr", "getattr",
        "setattr", "super", "print", "open", "format", "sorted",
        "reversed", "enumerate", "zip", "map", "filter", "any", "all",
        "next", "iter", "sum", "min", "max", "abs", "round",
        "self", "cls", "args", "kwargs",
    }


SKIP = _builtin_skips()


def _name_from_func(node) -> str:
    """Resolve the callee name from ast.Call's .func field. Covers the
    two common shapes: ast.Name (`foo()`) and ast.Attribute (`obj.foo()`).
    Other shapes (`(lambda: ...)()`, `arr[i]()`) get skipped."""
    if isinstance(node, ast.Name):
        return node.id
    if isinstance(node, ast.Attribute):
        return node.attr
    return ""


def _name_from_exc(node) -> str:
    """Resolve the exception type from ast.Raise's .exc field. Covers
    `raise X` (ast.Name) and `raise X(msg)` (ast.Call with Name func)
    and `raise mod.X(...)` (Attribute). Bare `raise` (re-raise) → empty.
    """
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
    """Parse src as Python and return a heuristic prefix line, or None
    if parsing fails (which on the stripped corpus shouldn't happen but
    is graceful-fallback territory)."""
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
            # First top-level def wins; nested defs are skipped (their
            # names aren't the chunk's identity).
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

    parts: list[str] = []
    if func_name:
        parts.append(f"func: {func_name}")
    if calls:
        # Cap at 8 calls to keep the prefix bounded. Calls are emitted
        # in source order, which is a deterministic but not
        # semantically-ranked ordering. Good enough for v0.
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
        except OSError:
            current = None
        if current == target.resolve():
            return
        link.unlink()
    link.symlink_to(target)


def main() -> None:
    if not SRC_DIR.exists():
        raise SystemExit(
            f"missing {SRC_DIR} — run `python scripts/bench_csn_nl_stripped.py` first"
        )

    for out_dir in (OUT_DIR_FULL, OUT_DIR_HEUR):
        (out_dir / "corpus").mkdir(parents=True, exist_ok=True)

    src_corpus = SRC_DIR / "corpus"
    written_full = 0
    written_heur = 0
    no_heur = 0
    sample_full: list[tuple[str, str]] = []
    sample_heur: list[tuple[str, str]] = []
    heur_len_sum = 0

    for src_path in sorted(src_corpus.iterdir()):
        if not src_path.name.endswith(".py"):
            continue
        original = src_path.read_text()
        prefix = extract_heuristic(original)
        if prefix is None:
            no_heur += 1
            # Both outputs fall back to the original chunk so the doc-id
            # set stays identical to the unaugmented bench (every qrel
            # still has a target file). Heur-only falls back to the
            # original since "nothing" would index zero tokens.
            (OUT_DIR_FULL / "corpus" / src_path.name).write_text(original)
            (OUT_DIR_HEUR / "corpus" / src_path.name).write_text(original)
            written_full += 1
            written_heur += 1
            continue

        heur_len_sum += len(prefix)
        full = prefix + original
        full_path = OUT_DIR_FULL / "corpus" / src_path.name
        if not full_path.exists():
            full_path.write_text(full)
        written_full += 1

        heur_only_path = OUT_DIR_HEUR / "corpus" / src_path.name
        if not heur_only_path.exists():
            heur_only_path.write_text(prefix)
        written_heur += 1

        if len(sample_full) < 3:
            sample_full.append((src_path.name, full[:300]))
            sample_heur.append((src_path.name, prefix.strip()))

    # Symlink queries/qrels/snippet caches into both output dirs.
    for out_dir in (OUT_DIR_FULL, OUT_DIR_HEUR):
        for name in ("queries.jsonl", "qrels.jsonl"):
            _symlink(SRC_DIR / name, out_dir / name)
        for snippets in SRC_DIR.glob("hyde-snippets-*.jsonl"):
            _symlink(snippets, out_dir / snippets.name)

    mean_heur_len = heur_len_sum / max(written_full - no_heur, 1)
    summary = {
        "corpus_full_written": written_full,
        "corpus_heur_written": written_heur,
        "no_heuristic_fallback_to_original": no_heur,
        "mean_heuristic_line_chars": round(mean_heur_len, 1),
        "sample_full": [{"name": n, "head": t} for n, t in sample_full],
        "sample_heur_only": [{"name": n, "head": t} for n, t in sample_heur],
    }
    (OUT_DIR_FULL / "summary.json").write_text(json.dumps(summary, indent=2))
    (OUT_DIR_HEUR / "summary.json").write_text(json.dumps(summary, indent=2))
    print(json.dumps(summary, indent=2))


if __name__ == "__main__":
    main()
