# Stage 8 follow-up — ColBERT MaxSim probe (parked, negative)

**Verdict:** Park. The cheap reuse path (rescore the existing rerank
shortlist with MaxSim over CodeRankEmbed's per-token vectors instead
of CLS-pool cosine) does not beat CLS-pool at the rerank stage. The
"do CodeRankEmbed token vectors carry late-interaction signal at
all" gate from the kickoff memo
([`docs/internal/colbert-late-interaction-for-ken.md`](../docs/internal/colbert-late-interaction-for-ken.md))
came back negative on a small slim sample; the kickoff's explicit
decision logic mapped that to *park shape B*.

## Setup

`scripts/maxsim_probe.go`: same hybrid shortlist, two scorers off
the same CodeRankEmbed forward pass:

- `cls`: L2-normalize position 0 of the token-state matrix on both
  query and doc, cosine. Identical to today's neural reranker
  baseline.
- `maxsim`: L2-normalize every token, score = Σ_{q tok} max_{d tok} (q_i · d_j).
  v0 excludes `[CLS]`, `[SEP]`, and (default) the QueryPrefix
  tokens on the query side.

Aikit changes for the probe: `encoder/forward_tokens.go` adds
`(*Weights).forwardTokens` (same pipeline as `forward` but returns
the full L×D hidden-state matrix instead of CLS-pooling) plus
`(*Model).EncodeTokens` / `EncodeTokensWithIDs` wrappers. The probe
script uses these to compute both scores from one forward pass.

## Results (slim, N=25, csn-python-nl-stripped, rerankN=50)

| Cell | cls NDCG@10 | maxsim NDCG@10 | Δ | qrel position moves |
|---|---:|---:|---:|---|
| prefix EXCLUDED (kickoff v0 default) | 0.8600 | 0.8252 | **−0.0348** | 0 up, 2 down, 23 unchanged |
| prefix INCLUDED (ablation) | 0.8600 | 0.8157 | **−0.0443** | 0 up, 3 down, 22 unchanged |

Both cells show MaxSim strictly worse than CLS-pool on the rerank
shortlist. Per-query analysis: 92% of queries (23/25 in the EXCLUDE
cell) have an identical qrel-position outcome under both scorers —
the two scoring functions agree on most of the ranking, and where
they disagree it's MaxSim losing.

Including the prefix tokens made MaxSim WORSE, not better, which
rules out "the slim version was misconfigured" as an explanation —
the slim version's prefix-exclusion was the correct MaxSim
configuration, and CodeRankEmbed still wins.

## Read against the kickoff's three-way branch

The kickoff defined:

- `maxsim` clearly beats `cls` → greenlight shape B
- `maxsim ≈ cls` → ambiguous → park
- `maxsim` clearly worse than `cls` → kill the cheap reuse path

The N=25 sample sits between "ambiguous" and "clearly worse." Per
the kickoff's explicit logic, weak evidence isn't enough to justify
the heavy build (shape B's first-stage token index requires
multi-week storage/compression/PLAID work even after the
scorer-level evidence is positive). Verdict: **park / kill the
cheap-reuse path**.

## What this does NOT prove

The probe measured rerank-stage *positioning*, not shape B's actual
prize — first-stage *recall* on a token-level index. They're
different questions. Negative evidence on rerank positioning is
*weak* evidence against shape B's recall win, not definitive
evidence. Stated here so a "MaxSim is dead" reading isn't
overgeneralized.

## What it does prove

CodeRankEmbed (CLS-trained) does not produce token vectors that
collectively beat its CLS pool at rerank-stage scoring on this
corpus. So if late-interaction is ever attempted for ken, it
likely cannot reuse CodeRankEmbed's token vectors as-is — it
would require porting a real MaxSim-trained model (ColBERT v2 or a
code-specific successor). That's a multi-week endeavor, blocked on
finding a code-specific MaxSim-trained checkpoint that exists at
all, before the storage + PLAID work even starts.

## Reopen triggers

- A code-specific MaxSim-trained encoder ships publicly (Jina,
  Voyage, Nomic — the obvious candidates).
- A specific bench gap surfaces that's plausibly a rerank-positioning
  issue rather than a recall issue.
- Someone runs the deferred first-stage recall-ceiling probe (an
  offline measurement of how many qrels the token-index would
  surface inside top-k vs the current hybrid) and it comes back
  strongly positive.

Until then, the retrieval campaign treats this as parked.

## Reproduction

```bash
KEN_MAXSIM_N=25 \
KEN_BENCH_DIR=$PWD/testdata/bench/csn-python-nl-stripped \
  go run scripts/maxsim_probe.go

# Ablation: include the QueryPrefix tokens on the query side
KEN_MAXSIM_INCLUDE_PREFIX=1 KEN_MAXSIM_N=25 \
KEN_BENCH_DIR=$PWD/testdata/bench/csn-python-nl-stripped \
  go run scripts/maxsim_probe.go
```

Each cell loads the model + bm25/hybrid index once and runs N
shortlist passes (default N=25; bumpable via env). Wall time
dominated by the forward passes (~10-15 min on f32 at N=25); each
shortlist doc gets ONE forward pass that both scorers consume.
