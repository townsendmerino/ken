#!/usr/bin/env python3
"""
pin_inference.py — resolve the three open Model2Vec inference questions for ken.

What this answers (run it once, read the printed summary):

  Q1. Is the `weights` tensor applied at runtime, or pre-multiplied into
      `embeddings` at distill time?  We compute two candidate pools and
      compare each to `StaticModel.encode()`'s ground truth. Whichever
      matches to ~1e-6 cosine is the algorithm.

  Q2. PAD masking is moot at v1 — ken encodes one chunk at a time, no
      batched padding. We don't test it. If batching is ever added,
      revisit.

  Q3. Is there any per-token scaling based on subword position / spacing
      (Model2Vec PR #259)?  We encode `"hello world"`, `"hello  world"`,
      and `"hello   world"` and check whether they produce identical
      output vectors.

It also writes `ken_golden.json` — a fixture file with token IDs,
intermediate values, and ground-truth output vectors for ~20 inputs.
The Go test suite loads this and asserts byte-equal token IDs and
~1e-5 cosine on output embeddings.

Requirements:
    pip install model2vec safetensors tokenizers huggingface_hub numpy

Usage (from repo root):
    .venv/bin/python scripts/pin_inference.py
    cp ken_golden.json testdata/golden.json
"""
from __future__ import annotations

import json
from pathlib import Path

import numpy as np
from huggingface_hub import snapshot_download
from model2vec import StaticModel
from safetensors.numpy import load_file
from tokenizers import Tokenizer

MODEL_ID = "minishlab/potion-code-16M"
FIXTURE_OUT = Path(__file__).resolve().parent.parent / "ken_golden.json"
MATCH_THRESHOLD = 1 - 1e-6  # cosine ≥ this counts as "match"


# ──────────────────────────────────────────────────────────────────────────────
# Setup
# ──────────────────────────────────────────────────────────────────────────────

local = snapshot_download(MODEL_ID)
print(f"Model snapshot at: {local}")

tensors = load_file(f"{local}/model.safetensors")
embeddings: np.ndarray = tensors["embeddings"]   # F32 [V, D]
mapping:    np.ndarray = tensors["mapping"]      # I64 [V]
weights:    np.ndarray = tensors["weights"]      # F64 [V]

V, D = embeddings.shape
print(f"  embeddings: {embeddings.shape} {embeddings.dtype}")
print(f"  mapping:    {mapping.shape}    {mapping.dtype}")
print(f"  weights:    {weights.shape}    {weights.dtype}")

# Sanity-check the mapping identity hypothesis from the doc.
is_identity = bool(np.array_equal(mapping, np.arange(V, dtype=mapping.dtype)))
print(f"  mapping is identity permutation: {is_identity}")
if not is_identity:
    print("  ⚠ mapping is NOT identity — Go port must use embeddings[mapping[id]]")

tok = Tokenizer.from_file(f"{local}/tokenizer.json")
model = StaticModel.from_pretrained(MODEL_ID)


# ──────────────────────────────────────────────────────────────────────────────
# Candidate pooling recipes
# ──────────────────────────────────────────────────────────────────────────────

def l2norm(v: np.ndarray, eps: float = 1e-12) -> np.ndarray:
    n = float(np.linalg.norm(v))
    return v / max(n, eps)


def pool_plain(ids: np.ndarray) -> np.ndarray:
    """Plain mean of gathered rows. The hypothesis: weights are pre-baked
    into `embeddings` at distill time, so runtime is just mean-pool."""
    rows = embeddings[mapping[ids]].astype(np.float64)
    return rows.mean(axis=0)


def pool_weighted_normalized(ids: np.ndarray) -> np.ndarray:
    """Weighted mean using `weights` at runtime, normalised by sum of weights.
    The alternative hypothesis."""
    rows = embeddings[mapping[ids]].astype(np.float64)
    w = weights[ids][:, None]
    return (rows * w).sum(axis=0) / w.sum()


def pool_weighted_unnormalized(ids: np.ndarray) -> np.ndarray:
    """Weighted sum divided by token count (not weight sum). Third hypothesis,
    less likely but worth checking."""
    rows = embeddings[mapping[ids]].astype(np.float64)
    w = weights[ids][:, None]
    return (rows * w).sum(axis=0) / len(ids)


RECIPES = {
    "plain_mean":              pool_plain,
    "weighted_mean":           pool_weighted_normalized,
    "weighted_sum_over_count": pool_weighted_unnormalized,
}


# ──────────────────────────────────────────────────────────────────────────────
# Test inputs — chosen to exercise the three questions
# ──────────────────────────────────────────────────────────────────────────────

CASES = [
    # Q1 — algorithm determination: any nontrivial input works
    "def hello(): return 42",
    "import numpy as np\nx = np.zeros((3, 3))",
    "The quick brown fox jumps over the lazy dog.",
    "save_pretrained(model, path)",
    "useEffect(() => { fetch('/api'); }, []);",

    # Q3 — whitespace / subword-position sensitivity
    "hello world",
    "hello  world",      # 2 spaces
    "hello   world",     # 3 spaces
    "helloworld",        # no space — different tokenization expected
    " hello world",      # leading space
    "hello world ",      # trailing space

    # edge cases — make sure these don't blow up the Go port
    "a",                 # single char
    "",                  # empty string (might error)
    "x" * 200,           # > max_input_chars_per_word (100) — should produce [UNK]
    "中文 mixed with English",  # CJK + ASCII
    "café résumé",       # accents — should be stripped by BertNormalizer
    "Müller weiß",       # German ß (lowercase tricky)
    "snake_case camelCase PascalCase",  # identifier-like
]


def cosine(a: np.ndarray, b: np.ndarray) -> float:
    return float(np.dot(a, b) / (np.linalg.norm(a) * np.linalg.norm(b)))


# ──────────────────────────────────────────────────────────────────────────────
# Main loop
# ──────────────────────────────────────────────────────────────────────────────

results = []
recipe_matches = {name: 0 for name in RECIPES}

for text in CASES:
    if text == "":
        # encode("") may raise; capture and continue
        try:
            ground = model.encode(text)
        except Exception as e:
            results.append({"text": text, "error": str(e)})
            print(f"\n=== {text!r} ===\n  ERROR: {e}")
            continue
    else:
        ground = model.encode(text)

    ground = np.asarray(ground, dtype=np.float64)

    enc = tok.encode(text)
    ids = np.array(enc.ids, dtype=np.int64)
    tokens = enc.tokens

    if len(ids) == 0:
        results.append({"text": text, "tokens": tokens, "ids": ids.tolist(),
                        "note": "empty token sequence"})
        print(f"\n=== {text!r} ===\n  empty token sequence")
        continue

    candidates = {}
    cosines = {}
    for name, recipe in RECIPES.items():
        v = l2norm(recipe(ids))
        candidates[name] = v
        cosines[name] = cosine(v, ground)
        if cosines[name] >= MATCH_THRESHOLD:
            recipe_matches[name] += 1

    print(f"\n=== {text!r} ===")
    print(f"  tokens: {tokens}")
    print(f"  ids:    {ids.tolist()}")
    for name, c in cosines.items():
        marker = "✓" if c >= MATCH_THRESHOLD else " "
        print(f"  {marker} cos({name:24s}, ground) = {c:.8f}")

    gnorm = float(np.linalg.norm(ground))
    degenerate = (not np.isfinite(ground).all()) or gnorm < 1e-12
    results.append({
        "text": text,
        "tokens": tokens,
        "ids": ids.tolist(),
        "weights_per_token": weights[ids].tolist(),
        # A zero-norm / non-finite ground truth (all-[UNK], etc.) carries no
        # directional information — cosine against it is undefined. Emit null
        # so the Go golden loader's `GroundTruth == nil` guard skips it for
        # the cosine assertion while still exercising token-ID parity.
        "ground_truth": None if degenerate else ground.tolist(),
        "degenerate_ground_truth": degenerate,
        "candidates": {k: v.tolist() for k, v in candidates.items()},
        "cosines": cosines,
    })


# ──────────────────────────────────────────────────────────────────────────────
# Question summaries
# ──────────────────────────────────────────────────────────────────────────────

print("\n" + "═" * 70)
print("  Q1: which pooling recipe matches StaticModel.encode()?")
print("═" * 70)
n_real = sum(1 for r in results if "ground_truth" in r)
for name, hits in recipe_matches.items():
    print(f"  {name:24s}: {hits}/{n_real} matches at cosine ≥ {MATCH_THRESHOLD}")
winner = max(recipe_matches, key=recipe_matches.get)
if recipe_matches[winner] == n_real:
    print(f"\n  ⇒ ALGORITHM: {winner}")
    if winner == "plain_mean":
        print("    `weights` is pre-multiplied into `embeddings` at distill time.")
        print("    Go inference: just mean(embeddings[mapping[id]]) + L2 norm.")
    elif winner == "weighted_mean":
        print("    `weights` is applied at runtime as the pooling weight.")
        print("    Go inference: Σ embeddings[mapping[id]]·weights[id] / Σ weights[id] + L2 norm.")
    else:
        print(f"    Surprising. Investigate.")
else:
    print(f"\n  ⇒ NO RECIPE MATCHES ALL INPUTS. Investigate per-input which one wins;")
    print(f"    the algorithm may be conditional (e.g., depends on token count).")

print("\n" + "═" * 70)
print("  Q3: is the output sensitive to whitespace beyond tokenization?")
print("═" * 70)
ws_pairs = [("hello world", "hello  world"),
            ("hello world", "hello   world"),
            ("hello world", " hello world"),
            ("hello world", "hello world ")]
ws_results = {r["text"]: r for r in results if r.get("text") in
              ["hello world", "hello  world", "hello   world",
               " hello world", "hello world "]}
for a, b in ws_pairs:
    if a not in ws_results or b not in ws_results:
        continue
    if "ground_truth" not in ws_results[a] or "ground_truth" not in ws_results[b]:
        continue
    va = np.array(ws_results[a]["ground_truth"])
    vb = np.array(ws_results[b]["ground_truth"])
    ids_a = ws_results[a]["ids"]
    ids_b = ws_results[b]["ids"]
    c = cosine(va, vb)
    same_ids = ids_a == ids_b
    flag = "" if c >= MATCH_THRESHOLD else "  ⚠ different output"
    print(f"  cos({a!r:22s}, {b!r:22s}) = {c:.8f}  same_ids={same_ids}{flag}")
print("  ⇒ if all cosines ≈ 1.0, no positional scaling — just match tokenization.")
print("  ⇒ if any are <1.0 AND same_ids=True, there IS extra-tokenization scaling.")


# ──────────────────────────────────────────────────────────────────────────────
# Write fixture
# ──────────────────────────────────────────────────────────────────────────────

fixture = {
    "model_id": MODEL_ID,
    "vocab_size": V,
    "embedding_dim": D,
    "mapping_is_identity": is_identity,
    "match_threshold": MATCH_THRESHOLD,
    "cases": results,
}
def _json_safe(obj):
    """Recursively replace non-finite floats (NaN, ±Inf) with None so the
    output is spec-valid JSON. Python's json.dumps emits bare `NaN`/`Infinity`
    tokens by default, which Go's encoding/json (and most strict parsers)
    reject — that previously made the entire fixture unparseable by the Go
    golden test, failing every case rather than just the degenerate one."""
    if isinstance(obj, dict):
        return {k: _json_safe(v) for k, v in obj.items()}
    if isinstance(obj, (list, tuple)):
        return [_json_safe(v) for v in obj]
    if isinstance(obj, float):
        return obj if np.isfinite(obj) else None
    return obj


# allow_nan=False makes a regression loud (raises) rather than silently
# re-emitting invalid JSON if a non-finite value ever slips past _json_safe.
FIXTURE_OUT.write_text(json.dumps(_json_safe(fixture), indent=2, allow_nan=False))
print(f"\nWrote {FIXTURE_OUT.name} ({FIXTURE_OUT.stat().st_size:,} bytes)")
print("\nNext: copy ken_golden.json into the ken Go project as testdata/golden.json")
print("      and write a test that asserts byte-equal token IDs and cos ≥ 1-1e-5.")
