#!/usr/bin/env python3
"""scripts/parity_dump.py — dump the HF reference tokenizer's outputs over
a code-realistic corpus into testdata/parity.jsonl, for the Go-side parity
test (internal/embed/parity_test.go, build tag `parity`) to diff against.

This is the corpus-scale Stage-3 acceptance bar from docs/DESIGN.md §3 — not the
18-case pin_inference.py spot-check.

Run from repo root:

    .venv/bin/python scripts/parity_dump.py

Output: testdata/parity.jsonl (gitignored; regeneratable). Each line is
    {"text": str, "normalized": str, "pre_tokens": [str], "ids": [int]}
The Go test classifies a mismatch by walking those intermediates so the
drift category (`normalize` / `pre_tokenize` / `wordpiece` / other) is
attributable.
"""
from __future__ import annotations

import json
import sys
from pathlib import Path

from transformers import AutoTokenizer

REPO = Path(__file__).resolve().parent.parent
OUT = REPO / "testdata" / "parity.jsonl"
ADVERSARIAL = REPO / "scripts" / "adversarial.txt"
MODEL_DIR = REPO / "testdata" / "model"
HF_REPO = "minishlab/potion-code-16M"

# Sliding-window step over each source file. Per docs/DESIGN.md §3 — pieces should
# be roughly the size of a chunk-shaped embedding input.
WINDOW = 200

# Per-file size cap mirrors internal/repo.DefaultMaxFileBytes (2 MiB).
MAX_BYTES = 2 << 20

# Directories pruned during the source walk. Mirrors the ken repo's
# .gitignore plus its sibling Stage-2 fallback list: dirs that contain
# either build artifacts, vendored deps, or per-machine binaries.
PRUNE_DIRS = {
    ".git", ".venv", "node_modules", "build", "dist", "vendor",
    "__pycache__", ".vscode", ".idea", "ken-mcp",
    # testdata/model holds the multi-MB safetensors snapshot.
    # (We still want to dump small testdata/ fixtures, just not the model.)
}

# File-suffix-level exclusions: artifacts/binaries that wouldn't reach the
# Go tokenizer in real use, plus the parity output itself (so a re-run
# doesn't recurse on its own previous output).
EXCLUDE_SUFFIXES = {
    ".safetensors", ".jsonl",      # binary or "our own output"
    ".pyc", ".so", ".dylib", ".o", ".a", ".bin",
    ".png", ".jpg", ".jpeg", ".gif", ".svg", ".pdf",
    ".zip", ".tar", ".tgz", ".gz", ".bz2", ".xz",
    ".lock",
}

EXCLUDE_BASENAMES = {
    "ken_golden.json", "golden.json",   # large embedding fixtures
    "go.sum",                           # checksum noise; dominates the dump
}


def load_tokenizer() -> AutoTokenizer:
    """Prefer the local testdata/model snapshot (offline, deterministic)
    over the HF cache so re-runs across machines tokenize identically."""
    if (MODEL_DIR / "tokenizer.json").exists():
        return AutoTokenizer.from_pretrained(str(MODEL_DIR))
    return AutoTokenizer.from_pretrained(HF_REPO)


def looks_binary(path: Path) -> bool:
    """Match internal/repo.isBinary: NUL in the first 8 KiB ⇒ binary."""
    try:
        with path.open("rb") as f:
            return b"\x00" in f.read(8192)
    except OSError:
        return True


def walk_corpus(root: Path):
    """Yield indexable files in lexicographic order (deterministic across
    runs and machines)."""
    def _walk(d: Path):
        try:
            entries = sorted(d.iterdir(), key=lambda p: p.name)
        except OSError:
            return
        for p in entries:
            if p.is_dir():
                if p.name in PRUNE_DIRS:
                    continue
                yield from _walk(p)
                continue
            if not p.is_file():
                continue
            if p.name in EXCLUDE_BASENAMES:
                continue
            if p.suffix.lower() in EXCLUDE_SUFFIXES:
                continue
            try:
                if p.stat().st_size > MAX_BYTES:
                    continue
            except OSError:
                continue
            if looks_binary(p):
                continue
            yield p

    yield from _walk(root)


def slice_text(text: str, window: int = WINDOW):
    """Yield non-overlapping `window`-character pieces of text.

    An empty file produces no pieces. A file shorter than the window
    produces one piece containing its full contents.
    """
    if not text:
        return
    for i in range(0, len(text), window):
        yield text[i:i + window]


def record(tok, text: str) -> dict:
    """Build one parity record by running HF's tokenizer through each
    intermediate stage we care about."""
    bt = tok.backend_tokenizer
    norm = bt.normalizer.normalize_str(text) if bt.normalizer is not None else text
    if bt.pre_tokenizer is not None:
        pre = [w for (w, _off) in bt.pre_tokenizer.pre_tokenize_str(norm)]
    else:
        pre = [norm]
    ids = tok.encode(text, add_special_tokens=False)
    return {"text": text, "normalized": norm, "pre_tokens": pre, "ids": ids}


def main() -> int:
    tok = load_tokenizer()
    OUT.parent.mkdir(parents=True, exist_ok=True)

    n_adv = n_repo = 0
    with OUT.open("w", encoding="utf-8") as out:
        # (b) Adversarial fixtures first — small, deterministic, hand-picked
        # corner cases. Each line of the file IS one input (including the
        # intentional empty first line for the empty-string case).
        if ADVERSARIAL.exists():
            for line in ADVERSARIAL.read_text(encoding="utf-8").split("\n"):
                rec = record(tok, line)
                out.write(json.dumps(rec, ensure_ascii=False) + "\n")
                n_adv += 1

        # (a) The ken repo, sliced into ~WINDOW-char pieces.
        for p in walk_corpus(REPO):
            try:
                data = p.read_text(encoding="utf-8")
            except UnicodeDecodeError:
                continue
            for piece in slice_text(data):
                rec = record(tok, piece)
                out.write(json.dumps(rec, ensure_ascii=False) + "\n")
                n_repo += 1

        # (c) Optional: a sibling semble checkout for broader real-world
        # code coverage (Python, more docstring styles). The prompt
        # mentions "outputs/ and any other accessible code" — using a
        # shallow-cloned /tmp/semble when present satisfies this without
        # baking any external assumption into the script.
        n_extra = 0
        for extra in (Path("/tmp/semble"),):
            if not extra.exists():
                continue
            for p in walk_corpus(extra):
                try:
                    data = p.read_text(encoding="utf-8")
                except UnicodeDecodeError:
                    continue
                for piece in slice_text(data):
                    rec = record(tok, piece)
                    out.write(json.dumps(rec, ensure_ascii=False) + "\n")
                    n_extra += 1

    total = n_adv + n_repo + n_extra
    print(f"wrote {OUT}: {n_adv} adversarial + {n_repo} ken + {n_extra} extra = {total} records",
          file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
