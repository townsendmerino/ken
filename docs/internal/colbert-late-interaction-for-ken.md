# Late interaction (ColBERT) for ken — what it would take

Info / design analysis, no code. What ColBERT is, the two ways it could live in ken, what we'd
build vs reuse vs port, the storage reality, and the cheapest way to find out if it's worth it.

## The one insight that frames everything

ColBERT ("Contextualized Late interaction over BERT") is **a transformer with the final pooling
step removed.** A normal embedding model runs a chunk through the network and then *pools* the
per-token hidden states into one vector. ColBERT skips the pool: it keeps one vector per token
(usually projected down to 128 dims), and compares a query to a document by **MaxSim** — for each
query token, find its single best-matching document token (max cosine), and sum those maxima.
"Late interaction" = the query and document interact at scoring time, token-against-token,
instead of being pre-squashed into single vectors that meet only at the end.

**Why that matters for ken:** you already ported CodeRankEmbed's full transformer forward pass to
pure Go — attention, RoPE, SwiGLU, the NEON matmul — and you currently CLS-pool the final hidden
states into one vector. ColBERT-style late interaction is *that same forward pass, minus the
pool, plus a scoring function.* The hard, model-shaped part is ~90% already in the tree. What's
genuinely new is the **scoring** (MaxSim), the **storage** (many vectors instead of one), and —
maybe — a **projection** and a **candidate-generation engine**.

## Two ways to deploy it — and only one improves the thing that matters

**Shape A — ColBERT as a reranker (cheap; touches the already-solved problem).**
The hybrid stage hands you a top-N shortlist; the current reranker runs CodeRankEmbed's forward
pass on each candidate, pools, and cosine-scores. ColBERT-as-rerank does the same forward pass but
*keeps the token vectors and scores with MaxSim* instead of pool-then-cosine. **Marginal cost over
today's reranker ≈ zero** — you already pay for the forward pass; you only change the final
scoring. New code: MaxSim. But recall the campaign's hard lesson: the reranker re-scores from
scratch, so *positioning is already solved* — a rerank-stage ColBERT can only help if MaxSim
orders the shortlist **better** than CLS-cosine does. The upside is capped at "better ordering of
an already-good shortlist," not "more right docs found." Real but bounded.

**Shape B — ColBERT as a first-stage retriever (heavy; improves recall, the thing that matters).**
Precompute token vectors for *every* chunk at index time, store them, and retrieve from the whole
corpus using them. **This improves recall** — it pulls more right documents into the shortlist,
which is the only lever the campaign showed actually moves the needle. But it's the big build: a
token-vector index, compression to make it tractable, and a candidate-generation engine so you
don't brute-force MaxSim over the entire corpus per query. It also re-introduces the index-time
encoding cost (every chunk through the heavy model), the same instant-index tax doc-side
generation had.

The strategic shape is the campaign's recurring one: **the cheap version (A) touches the solved
problem; the valuable version (B) is the heavy build.**

## The components — build / reuse / port

**1. The token-vector encoder (the model) — the crux decision.**
- *Reuse path:* ken's existing pure-Go CodeRankEmbed forward pass, modified to emit per-token
  vectors instead of CLS-pooling. **Zero new model.** Caveats: (a) it was trained for CLS-pooled
  cosine, *not* MaxSim, so its raw token vectors may not deliver ColBERT's benefit; (b) it's
  768-dim per token (6× ColBERT's 128), which makes storage much worse unless you add a
  projection.
- *Port path:* take a real ColBERT checkpoint (trained with the MaxSim objective + a 128-dim
  projection head) and port its forward pass to pure Go — the *same kind of work* you already did
  for CodeRankEmbed (a BERT-family encoder), plus a trivial linear projection layer. Candidates:
  `colbert-ir/colbertv2.0` (BERT-base, English), `answerai-colbert-small`, `jina-colbert-v2`
  (multilingual), `Reason-ModernColBERT` (ModernBERT-based, 2025).
- *The gap:* there is **no widely-adopted code-specific ColBERT model.** So you choose between
  code-tuned-but-not-MaxSim-trained (CodeRankEmbed) or MaxSim-trained-but-not-code-tuned (a
  general ColBERT). Neither is ideal. Training a code ColBERT (e.g. via PyLate, the current
  late-interaction training framework) is a research/training project, not a port — out of scope
  unless this becomes a major bet.

**2. MaxSim scoring — BUILD (small, pure Go).** For each query-token vector, max cosine over the
doc's token vectors, summed. Reuses your NEON dot kernel; L2-normalize token vectors so dot =
cosine. Replicate two ColBERT details: *query augmentation* (pad the query with `[MASK]` tokens
to a fixed length — acts as learned query expansion) and *doc-side filtering* (drop punctuation/
stopword tokens to cut storage and noise). Low hundreds of LOC.

**3. Token-vector storage + compression — BUILD (the real engineering).**
- *Uncompressed:* extend the index serialization to hold M token vectors per chunk. Storage
  explodes (numbers below).
- *Compressed (ColBERTv2 residual / PLAID):* k-means over all token vectors to get centroids;
  store each token as `centroid_id (4 bytes) + a 1–2-bit-per-dim quantized residual` ≈ **20–36
  bytes/token** at 128-dim, versus ColBERTv1's 256 bytes/token. Build: k-means (gonum has it, or
  hand-roll) + residual encode/decode. Significant but fully specified by the ColBERTv2 paper.

**4. Candidate generation — BUILD (first-stage only; the PLAID engine).** You can't MaxSim the
whole corpus per query. PLAID uses the *centroid IDs* as a cheap first filter: find chunks whose
tokens fall near the query tokens' centroids, prune aggressively, then load residuals and run
MaxSim only on the survivors. This is the multi-stage retrieval engine, the most complex new
piece. **Escapable for a first cut at small-repo scale** (brute-force MaxSim over all token
vectors may be tolerable for a few thousand chunks), but required for monorepo scale.

## The storage reality (why this is the "heavy" one)

Per-chunk vector storage, ~200 tokens/chunk, 16k-chunk repo:

| Representation | Per chunk | 16k-chunk repo |
|---|---|---|
| **Today** (1 × 768-d f32) | ~3 KB | **~48 MB** |
| ColBERT, 768-d f32 (reuse CodeRankEmbed tokens raw) | ~600 KB | ~9.6 GB (untenable) |
| ColBERT, 128-d f32 | ~100 KB | ~1.6 GB |
| ColBERT, 128-d int8 | ~25 KB | ~400 MB |
| ColBERT, PLAID residual (~30 B/tok) | ~6 KB | **~96 MB** |

Takeaways: compression is **not optional** for shape B; even compressed it's ~2× today's index,
and reusing CodeRankEmbed's raw 768-d tokens (no projection) is ~200×. The 768→128 projection
isn't just a storage nicety — it's why ColBERT models ship a projection head, and skipping it is
the reuse path's biggest liability.

## Build vs reuse vs port — summary

- **Build in ken/aikit:** MaxSim scorer; token-vector storage format; query augmentation + doc
  token filtering; (shape B) k-means + residual compression; (shape B) centroid candidate
  generation; (reuse path) a 768→128 projection (PCA or learned).
- **Reuse from ken:** the CodeRankEmbed pure-Go forward pass (minus pooling), the NEON dot kernel,
  the safetensors loader, the index-serialization framework, and the existing two-stage retrieval
  architecture (which shape A slots straight into).
- **Port from external (study, don't link — same no-cgo posture as the CodeRankEmbed port):** the
  ColBERTv2 algorithms from the Stanford ColBERT repo + paper (MaxSim, residual compression,
  PLAID candidate gen). PyLate is the current Python reference for training/retrieval. There is no
  mature pure-Go ColBERT to bind to — you port the algorithms, not a library.
- **Model from HuggingFace (if not reusing CodeRankEmbed):** a ColBERT checkpoint — but all
  general/English, none code-specific. Whichever you pick, you port its forward pass to pure Go
  like CodeRankEmbed, plus the projection head.

## The honest recommendation — validate the crux for ~free first

Don't build the PLAID index speculatively. The campaign discipline applies: find the cheap probe
that answers the load-bearing question before committing to the heavy build.

- **Cheapest probe (answers the model dilemma for ~free):** in the *existing* reranker, you
  already run CodeRankEmbed's forward pass on each shortlist candidate. Add MaxSim as an
  alternative final score and bench **CLS-cosine rerank vs MaxSim rerank** on the same harness.
  This costs almost nothing (the forward pass is already paid) and tells you whether
  CodeRankEmbed's token vectors carry late-interaction signal *at all* — i.e. whether the reuse
  path is viable or you'd need to port a real ColBERT model. That single result de-risks the
  entire direction.
- **If MaxSim-rerank beats CLS-rerank meaningfully**, the next question is shape B: would a
  token-level *first-stage* index pull more of the unreached-30 into the shortlist than today's
  dense retriever? Probe that offline (oracle-style) before building compression + PLAID.
- **If it doesn't beat CLS**, you've learned — cheaply — that late interaction needs a properly
  MaxSim-trained model, which means a port + the code-specificity gap, and you can weigh that
  honestly instead of discovering it after building the index.

## Bottom line

Late interaction is the highest *ceiling* on the untried list and also the highest *cost* — the
"big lift, real reward" item. For ken the encoder is ~90% already built (it's your forward pass
minus the pool), MaxSim is small, but the value lives in shape B (first-stage recall), which
drags in token-scale storage, compression, candidate generation, an index-time encoding tax, and
a model dilemma (no code-specific ColBERT exists). The disciplined move is the near-free
MaxSim-vs-CLS rerank probe: it tells you whether your *existing* model's token vectors are good
enough for late interaction before you spend a line of code on the heavy index.

---

Sources: [ColBERTv2 paper (arXiv 2112.01488)](https://arxiv.org/pdf/2112.01488),
[PLAID engine (arXiv 2205.09707)](https://arxiv.org/pdf/2205.09707),
[Weaviate: overview of late-interaction models](https://weaviate.io/blog/late-interaction-overview),
[PyLate (arXiv 2508.03555)](https://arxiv.org/html/2508.03555v1),
[Zilliz: ColBERT token-level embedding explainer](https://zilliz.com/learn/explore-colbert-token-level-embedding-and-ranking-model-for-similarity-search)
