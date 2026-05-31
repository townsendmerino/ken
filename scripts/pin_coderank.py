#!/usr/bin/env python3
"""pin_coderank.py — produce testdata/coderank_golden.json from the real
nomic-ai/CodeRankEmbed checkpoint.

This is the Milestone 1 reference dump for the neural reranker (see
outputs/ken-rerank-plan.md §11). For each hand-picked case it records:

  - the input text
  - whether the query-prefix is applied (model card says yes for queries
    only — using it on docs degrades cosine sharply)
  - the wrapped token IDs ([CLS] ... [SEP]) the model actually ate, with
    right-truncation to max_seq_length
  - the raw (UN-normalized) CLS hidden state the sentence-transformers
    pipeline produces; ken's eventual pure-Go forward pass L2-normalizes
    inside and compares via cosine (not bit-exact) per plan §11 — so the
    golden stores the raw vector and the Go test does the L2 itself.

The case mix targets the failure modes the plan called out: short / long /
empty / unicode / [UNK]-heavy / >512-token (truncation) / both query and
doc. This is the smallest fixture that catches a transposed weight, a
broken RoPE, or a wrong layernorm; the broader NDCG verdict is M6's job.

Non-finite floats sanitize to JSON null (Python's json.dumps emits bare
NaN, which is invalid JSON; Go's encoding/json rejects the entire file).
Same gotcha pin_inference.py documented.

Usage (from repo root):
    .venv/bin/python scripts/pin_coderank.py
    # writes testdata/coderank_golden.json directly (no copy step needed).
"""
from __future__ import annotations

import json
import math
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO_ROOT / "scripts"))


# Mandatory query prefix from the model card; mirrors coderank.QueryPrefix.
QUERY_PREFIX = "Represent this query for searching relevant code: "
MAX_SEQ_LEN = 512


def main() -> int:
    import numpy as np
    from sentence_transformers import SentenceTransformer
    from transformers import AutoTokenizer

    out_path = REPO_ROOT / "testdata" / "coderank_golden.json"

    sys.stderr.write("[pin_coderank] loading nomic-ai/CodeRankEmbed (CPU; deterministic) ...\n")
    sys.stderr.flush()
    model = SentenceTransformer("nomic-ai/CodeRankEmbed", trust_remote_code=True, device="cpu")
    model.max_seq_length = MAX_SEQ_LEN
    tok = AutoTokenizer.from_pretrained("nomic-ai/CodeRankEmbed", trust_remote_code=True)

    # ── cases — small, hand-curated, deliberately covers the failure modes ──
    cases: list[tuple[str, bool, str]] = [
        # (text, is_query, note)
        ("how do i parse json", True, "short query"),
        ("recursive directory walk that respects gitignore", True, "typical NL query"),
        ("def add(a, b):\n    return a + b", False, "tiny code doc"),
        ("import json\ndef load(s):\n    return json.loads(s)", False, "small code doc — json"),
        ("class Dog:\n    def bark(self):\n        print(\"woof\")", False, "code doc — class"),
        ("", False, "empty string — degenerate input"),
        ("[UNK] [UNK] [UNK]", False, "all-unk literal tokens"),
        ("こんにちは 世界 — 你好", False, "unicode (Japanese + Chinese)"),
        # A long doc above 512 tokens to exercise right-truncation. We
        # build it from a single long function source repeated until the
        # token count comfortably exceeds 512.
        ("def foo(x):\n    " + "y = x * 2\n    " * 200 + "return y", False, "long doc — truncation"),
        # The query-prefix tokenizes as ordinary wordpieces (no special
        # handling) — pin that by also storing the bare-prefix-as-doc to
        # confirm: tokens(EncodeQuery(q)) == tokens(EncodeDoc(prefix+q)).
        ("hello world", True, "short query — also doc-equivalent below"),
        ("Represent this query for searching relevant code: hello world", False,
         "doc-encoding of (prefix + 'hello world') — should match the row above's token ids"),
        # A code-search-y NL query and a matching code snippet (cosine
        # should be markedly higher than for the mismatched pair).
        ("compute the sha256 hash of a file", True, "NL query — paired with next row"),
        ("def sha256_of(path):\n    import hashlib\n    h = hashlib.sha256()\n    with open(path, 'rb') as f:\n        while chunk := f.read(8192):\n            h.update(chunk)\n    return h.hexdigest()", False, "matching doc for the row above"),
        # A clearly-unrelated doc for the same query — cosine should be lower.
        ("def fibonacci(n):\n    if n < 2: return n\n    return fibonacci(n-1) + fibonacci(n-2)", False, "unrelated doc"),
        # A code-source-as-query (CoIR-style) — pins that long code queries
        # also work and the truncation path is exercised on the query side.
        ("def parse_url(s):\n    # split scheme, host, path, query\n    " + "pass\n    " * 60, True, "long code-source query"),
        # Pure whitespace — another degenerate case.
        ("   \t  \n   ", False, "whitespace-only doc"),
        # Single character (sub-word fragment behavior).
        ("x", False, "single character"),
        # All-punctuation (BertNormalizer + WordPiece edge case).
        ("!!! ??? ...", False, "all-punctuation"),
        # A very BERT-vocab-friendly NL question.
        ("how does git rebase work", True, "very common NL query"),
        # And a matching natural-language doc (not code).
        ("Git rebase applies commits from one branch on top of another.", False, "NL doc"),
    ]

    cases_out: list[dict] = []
    for text, is_query, note in cases:
        model_input = (QUERY_PREFIX + text) if is_query else text
        # Reference tokenizer output: HF tokenizer with truncation+padding
        # match the model.max_seq_length. We strip the padding zeros (we
        # only need the actual id sequence, not the padded length).
        enc = tok(
            model_input,
            truncation=True,
            max_length=MAX_SEQ_LEN,
            padding=False,
            return_tensors=None,
        )
        token_ids = list(enc["input_ids"])

        # Reference embedding: raw (UN-normalized) CLS hidden state.
        # The model's modules.json is Transformer + Pooling (CLS), with
        # NO Normalize module — so encode(normalize_embeddings=False) is
        # exactly the raw CLS we want to compare against.
        vec = model.encode(
            [model_input],
            batch_size=1,
            normalize_embeddings=False,
            show_progress_bar=False,
            convert_to_numpy=True,
        )[0]
        # Sanitize non-finite → null so the JSON is well-formed for Go.
        embedding = [None if not math.isfinite(float(v)) else float(v) for v in vec]
        # Also record the L2 norm so the Go test can sanity-check its own
        # normalization without re-computing (and so a degenerate row
        # with zero norm is visible).
        norm = float(np.linalg.norm(vec))

        cases_out.append({
            "text": text,
            "is_query": is_query,
            "note": note,
            "model_input": model_input,
            "token_ids": token_ids,
            "embedding": embedding,
            "embedding_l2": norm,
        })
        sys.stderr.write(f"  {len(cases_out):>2}/{len(cases)}  is_query={is_query}  |{text[:60]!r}{'...' if len(text) > 60 else ''}\n")
        sys.stderr.flush()

    payload = {
        "model_id": "nomic-ai/CodeRankEmbed",
        "query_prefix": QUERY_PREFIX,
        "max_seq_length": MAX_SEQ_LEN,
        "embedding_dim": len(cases_out[0]["embedding"]),
        "n_cases": len(cases_out),
        # `vocab_size` here is the tokenizer vocab (30522). The model's
        # word_embedding tensor is padded to 30528 (multiple of 64) but
        # IDs past 30521 are unreachable through the tokenizer.
        "vocab_size": tok.vocab_size,
        "note": (
            "Raw (UN-normalized) CLS hidden state. Go forward pass should "
            "L2-normalize and compare via cosine. Non-finite floats → null."
        ),
        "cases": cases_out,
    }
    out_path.parent.mkdir(parents=True, exist_ok=True)
    # allow_nan=False mirrors pin_inference.py's discipline — any non-finite
    # that slipped past the sanitizer above fails loudly here.
    with out_path.open("w") as f:
        json.dump(payload, f, indent=2, allow_nan=False)
        f.write("\n")
    sys.stderr.write(f"[pin_coderank] wrote {out_path.relative_to(REPO_ROOT)} ({out_path.stat().st_size/1024:.1f} KB)\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())
