# ken — working context (as of 2026-05-31)

Durable orientation notes so a future session can pick up cold. ken is a pure-Go,
no-cgo port of MinishLab/semble — a hybrid code-search tool. Repo:
github.com/townsendmerino/ken. Editing rule: **Claude Code is the sole editor of
the repo**; the desktop/Cowork app does design + planning only and does not modify
the tree.

## Hard constraint (the whole point of the project)

Pure Go, **no cgo**, single static cross-compiled binary, no per-platform vendored
artifacts. This is non-negotiable and shapes every decision. GPU (cgo), Python
sidecars, and native ONNX runtimes are all off the table for anything compiled into
ken. Hand-written Go assembly (e.g. ARM64 NEON) is allowed — it's part of the Go
toolchain, not cgo.

## How retrieval works now

ken retrieves in stages over a chunked, gitignore-respecting walk of the repo. Every
chunk is indexed two ways in parallel: a Lucene-variant **BM25** lexical index with an
identifier-aware tokenizer (camelCase/snake_case/acronym splits), and a dense
**semantic** vector from the pure-Go **potion-code-16M** Model2Vec embedder, searched
by exact brute-force cosine over a flat index. At query time both retrievers over-fetch
(k×5 candidates), their results are converted to reciprocal-rank-fusion scores
(1/(60+rank)), and the two are combined with an adaptive **α-weighted blend** — α=0.3
for bare-symbol queries (leaning lexical), 0.5 for natural-language queries — chosen by
a regex that classifies query shape. The pipeline is ported verbatim from semble so
ranking can be diffed against the Python reference.

On top of the fused candidates ken runs a **heuristic rerank pass**: a file-coherence
boost (promotes the best chunk of multi-hit files), then query-aware boosts
(definition-keyword / file-stem / embedded-symbol matches), then path penalties and a
file-saturation decay so no single file dominates the top-k.

As of the recent campaign there's also an optional **second-stage neural reranker**: a
fully pure-Go forward pass of the **CodeRankEmbed** transformer (the nomic-BERT teacher
potion was distilled from) that re-scores the top-N shortlist with real contextual
embeddings, with doc-side vectors cached by content hash. It's opt-in
(`ModeHybridRerank`) and transparently downgrades to plain hybrid when no rerank model
is present, so default behavior is unchanged.

## What shipped recently (neural reranker — Stage 6)

vscode-claude (Claude Code) built the entire reranker over campaign milestones M0–M11,
living in `internal/coderank/` + `internal/search/neural_rerank*.go`,
`reranker.go`, `rerank_cache.go`. Notable: it went beyond the plan with **hand-written
ARM64 NEON assembly** (`dot_arm64.s` / `dotNEON8x4`, pure-Go fallback in
`dot_other.go`) and **int8 quantization** (`linalg_q8.go`, `weights_q8.go`,
`forward_q8.go`). Validation: golden-cosine bar ≥ 0.997, measured 1.000000 across all 18
fixtures. M11 was a profile-driven alloc cleanup (selfAttentionBatched scratch reuse:
−75% alloc bytes / −93% objects, wall time flat). Campaign closed: the matmul/NEON
kernel is ~79% flat CPU and saturated; the one residual lever is a fp32 fast-exp for
silu (~3% wall) that would perturb the cosine — documented, not shipped, awaiting an
explicit accuracy-for-speed decision. As of this session the reranker work was
**uncommitted on the tree** — worth committing behind its branch.

## Design philosophy that's working

- **Gate expensive builds on cheap Python validation first.** The reranker did M0
  (ceiling check) before the Go build. Apply the same to anything new.
- **NDCG is the acceptance gate; embedding cosine is a loose sanity bar.** Ordering
  quality is the product, not embedding fidelity.
- **Opt-in + transparent downgrade** for every new model-backed stage, so the
  zero-config default never regresses.
- **Parity discipline:** every model component is golden-tested against a Python
  reference (the `pin_*.py` + golden-fixture pattern).

## Next direction: query-side understanding (Stage 7)

The strategic insight: ken's job is *retrieval*, and the highest-leverage place for a
small generative model is **before** retrieval — transforming the query so the existing
BM25 + semantic + boost machinery retrieves better. A 135M–500M Llama-style model is
plenty (these are association/classification tasks, not hard generation), it's fast
enough in pure Go (short outputs, KV cache, query-plan caching), and every output feeds
a knob ken already has. Four candidate transforms, ranked by value:

1. **HyDE** (highest value, lowest integration risk) — generate a hypothetical code
   snippet for an NL query, embed it with potion, fuse with the query vector. Moves the
   query from "question space" into "code space" on the semantic retriever. The snippet
   needn't be correct, only code-shaped with plausible identifiers — which is why a tiny
   model suffices.
2. **Vocab-gap symbol/term prediction** — predict code identifiers absent from the
   query ("log people in" → authenticate, signIn, session); feed BM25 + symbol boosts.
3. **Query expansion / synonyms** — widen BM25 recall; highest drift risk.
4. **Intent classification** — definition/usage/explanation; feeds α + boosts; partly
   already done by `isSymbolQuery`/`resolveAlpha`. Use constrained decoding.

The one genuinely new heavy component for any of these is a **byte-level BPE tokenizer**
(SmolLM/Qwen use GPT-2-style BPE — the WordPiece tokenizer does NOT transfer). The
decoder forward pass otherwise reuses ~60% of the coderank encoder math (RoPE, SwiGLU,
attention, the NEON matmul) plus causal mask + KV cache + GQA + LM head + sampling.
Greedy/deterministic decoding by default; fuse-not-replace so a bad transform degrades
gracefully. Full plans live in `outputs/ken-rerank-plan.md` and
`outputs/ken-query-understanding-plan.md`.

## Generation (answer-side) — deliberately NOT in ken

Summarizing/explaining code needs a real (7B+) LLM, which can't run pure-Go/no-cgo at
useful speed (the quality/speed scissors: small enough to be fast = too weak; good
enough = too slow without C++ SIMD). That stays a **BYO-endpoint** concern: a thin
pure-Go HTTP `Generator` client (modeled on gobe's `inference.go`) pointing at
ollama/llama.cpp/an API. ken contains a *socket to* a model, never the weights.
