#!/usr/bin/env python3
"""coderank_model.py — thin wrapper around nomic-ai/CodeRankEmbed for the
M0 ceiling experiment (see outputs/ken-rerank-plan.md §12) and, later,
the M1 golden generator.

CodeRankEmbed is a bi-encoder: a document's embedding is query-independent,
so doc-side vectors are cacheable. The mandatory query prefix
("Represent this query for searching relevant code: ") is applied to
queries only — documents (code) get no prefix. Both facts come straight
from the model card and are load-bearing for the eventual pure-Go port.

This module exists so the CoIR and semble M0 paths share one reranker
implementation (one model load, one cosine convention, one dedup cache).
It is Python-reference-only; it never enters ken's released binary.
"""
from __future__ import annotations

import sys
from typing import Iterable

QUERY_PREFIX = "Represent this query for searching relevant code: "
MODEL_ID = "nomic-ai/CodeRankEmbed"


def _pick_device(requested: str | None) -> str:
    import torch

    if requested and requested != "auto":
        return requested
    if torch.backends.mps.is_available():
        return "mps"
    if torch.cuda.is_available():
        return "cuda"
    return "cpu"


class CodeRankReranker:
    """Loads CodeRankEmbed once; reranks candidate texts against a query.

    Doc-side embeddings are memoized by text (the steady-state perf
    keystone the plan §8 relies on); within an M0 run the same chunk
    surfaces across many queries, so dedup turns ~100k encodes into far
    fewer. Embeddings are L2-normalized at encode time so reranking is a
    plain dot product.
    """

    def __init__(
        self,
        *,
        device: str | None = None,
        max_seq_length: int = 512,
        batch_size: int = 64,
    ) -> None:
        from sentence_transformers import SentenceTransformer

        self.device = _pick_device(device)
        self.batch_size = batch_size
        sys.stderr.write(
            f"[coderank] loading {MODEL_ID} on {self.device} "
            f"(max_seq_length={max_seq_length})...\n"
        )
        self.model = SentenceTransformer(
            MODEL_ID, trust_remote_code=True, device=self.device
        )
        # tokenizer_config caps at 512; the plan truncates chunk-sized
        # candidates here to keep latency bounded (§5).
        self.model.max_seq_length = max_seq_length
        # Cache keyed by the EXACT string fed to the model (prefixed query
        # or raw doc). One global batched encode (length-sorted by ST)
        # avoids the per-query MPS kernel-recompilation thrash that made
        # 600 small encode() calls ~16x slower than one big sorted batch.
        self._cache: dict[str, "object"] = {}  # text -> np.ndarray (d,)
        self.encode_calls = 0  # texts actually pushed through the model
        self.cache_hits = 0

    # -- encoding -----------------------------------------------------------

    def prewarm(self, strings: Iterable[str]) -> None:
        """Encode every unique uncached string in ONE batched, length-sorted
        pass and fill the cache. Pass already-prefixed query strings and raw
        doc strings together — the model is the same for both."""
        missing: list[str] = []
        seen: set[str] = set()
        for s in strings:
            if s in self._cache or s in seen:
                continue
            seen.add(s)
            missing.append(s)
        if not missing:
            return
        sys.stderr.write(f"[coderank] prewarming {len(missing)} unique strings...\n")
        sys.stderr.flush()
        mat = self.model.encode(
            missing,
            batch_size=self.batch_size,
            normalize_embeddings=True,
            show_progress_bar=True,
            convert_to_numpy=True,
        )
        self.encode_calls += len(missing)
        for s, v in zip(missing, mat):
            self._cache[s] = v

    def _vec(self, s: str):
        v = self._cache.get(s)
        if v is None:
            v = self.model.encode(
                [s], batch_size=1, normalize_embeddings=True,
                show_progress_bar=False, convert_to_numpy=True,
            )[0]
            self.encode_calls += 1
            self._cache[s] = v
        else:
            self.cache_hits += 1
        return v

    # -- reranking ----------------------------------------------------------

    def rerank_scores(self, query: str, texts: list[str]):
        """Cosine of each candidate against the query (higher = better).

        Vectors are already L2-normalized, so cosine == dot product. Call
        prewarm() with all inputs first for throughput; otherwise this
        encodes lazily one string at a time.
        """
        import numpy as np

        if not texts:
            return np.zeros(0, dtype="float32")
        q = self._vec(QUERY_PREFIX + query)
        d = np.stack([self._vec(t) for t in texts], axis=0)
        return d @ q
