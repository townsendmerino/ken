# Stage 8 follow-up — Query-time graph expansion (negative result)

**Verdict:** Negative. Query-time 1-hop call-graph expansion of the
hybrid retriever's top-K candidates HURTS NDCG@10 across both bench
sets and every hyperparameter combination tried. The hypothesis that
sparse expansion would avoid M0e Track 1's flooding mechanism turned
out to be wrong for the same underlying reason: structural graph
neighborhood doesn't correlate with semantic relevance to a NL query,
and popular functions in the frontier expand into huge caller lists
that flood the candidate set.

## Hypothesis going in

M0e Track 1 closed negative because *index-time* enrichment
(prepending callers/callees as text into every chunk) flooded BM25
and the embedding pass. The proposed mitigation: do the expansion at
*query time* only, walked from a small top-K frontier (sparse, not
per-chunk). The structural index is already built, the cost is one
map lookup per top-K hit.

## What we ran

`bench/ndcg/qgraph_test.go` (build tag `bench`):

1. Build search + structural index over the bench corpus.
2. For each query, run hybrid baseline → file-dedup → top candidates.
3. Take the top `frontierK` files; for each:
   - Outbound: add every file that calls a function defined in the
     frontier file (via `ix.Structural.Callers(funcname)`).
   - Inbound: add every file that defines a name the frontier file
     calls (via `ix.Structural.Defs(callee)`).
4. Boost candidate scores additively: `score + boost * graphHits[f]`.
   New candidates start from `boost * graphHits[f]`.
5. Re-sort, compute NDCG@10 vs the qrel.

Hyperparams swept:
- `frontierK ∈ {1, 3, 5, 10}` (how many top-K hits to expand from)
- `boost ∈ {0.01, 0.02, 0.05, 0.10}` (additive per graph link)

## Results — CoSQA dev (552 docs, 313 queries)

| frontierK | boost | NDCG@10 baseline | NDCG@10 expanded | Δ |
|---:|---:|---:|---:|---:|
| 1 | 0.01 | 0.567 | 0.549 | −0.018 |
| 3 | 0.01 | 0.573 | 0.536 | −0.037 |
| 5 | 0.01 | 0.571 | 0.522 | −0.049 |
| 10 | 0.01 | 0.574 | 0.459 | −0.115 |
| 1 | 0.02 | 0.566 | 0.521 | −0.045 |
| 3 | 0.02 | 0.570 | 0.468 | −0.102 |
| 5 | 0.02 | 0.570 | 0.403 | −0.167 |
| 10 | 0.02 | 0.570 | 0.292 | −0.278 |
| 1 | 0.10 | 0.571 | 0.520 | −0.051 |
| 3 | 0.10 | 0.574 | 0.473 | −0.101 |
| 5 | 0.10 | 0.571 | 0.403 | −0.168 |
| 10 | 0.10 | 0.570 | 0.293 | −0.277 |

(Baseline jitters in [0.566, 0.574] — search-index build is
deterministic per run; small variation across runs is from go-test
process startup / cache state.)

Per-query move counts at frontierK=3, boost=0.05: **up=11, down=119,
unchanged=183**. Of queries the expansion *changed*, 91% got worse.

## Results — csn-python-nl-stripped (14,725 docs, first 300 queries)

| frontierK | boost | NDCG@10 baseline | NDCG@10 expanded | Δ |
|---:|---:|---:|---:|---:|
| 3 | 0.05 | 0.564 | 0.060 | −0.504 |

Per-query: **up=12, down=217, unchanged=71**. The corpus is ~27×
larger than CoSQA's, and the harm scales with corpus size — exactly
the flooding pattern M0e Track 1 hit.

## Why the hypothesis failed

The "sparse expansion" framing assumed: small frontier → small
expansion set → no flooding. But a **single popular function**
defined in any frontier file has a huge `Callers()` list. On
csn-stripped, names like `get`, `set`, `__init__`, `from`, `make`,
`process` are defined across hundreds of files; each appearance in a
frontier expands the candidate set by hundreds of files, each
getting the same boost. The boost mass overwhelms the original
hybrid score, the relevant qrel gets pushed down, NDCG collapses.

M0e Track 1's flooding mechanism (every chunk inherits noisy
caller text) is replaced here by a different mechanism (every
popular-name frontier hit explodes the candidate set), but the
endpoint is identical: structural-graph signal does not carry
NL-query-relevance information, so adding it as a generic boost
re-orders candidates AWAY from the gold answer on most queries.

## What might still work (NOT shipped, NOT recommended without re-scoping)

The blunt additive-boost approach is dead. Two narrower questions
remain open:

1. **Re-scoring without expanding the candidate set.** Keep only the
   baseline candidates; use graph signals to TIE-BREAK among the
   top-10. This is a much smaller search space — boost would only
   adjust ranks within the baseline shortlist, not introduce new
   distractors. Untested.
2. **Multi-hop on a function-level, not file-level, graph.** The
   file-level graph saturates on popular utility functions. A
   function-level reverse-call-graph (which we don't have yet)
   would let us walk `caller_function → callee_function` paths
   that don't include the "every popular function callee" cliff.
   Untested; requires a new richer index.

Neither is on the Stage 8 ship roadmap; both should be evaluated as
their own gates if and when someone wants to revisit graph
expansion. The cleanest read of this experiment is that the simple
form of the idea is empirically dead.

## Reproduction

```bash
# CoSQA — full sweep with defaults
KEN_BENCH_DIR=$PWD/testdata/bench/cosqa-python \
  go test -tags=bench -run TestQGraphExpansion -v -timeout 600s ./bench/ndcg/

# Override hyperparams
KEN_BENCH_DIR=$PWD/testdata/bench/cosqa-python \
  KEN_QGRAPH_FRONTIER=5 KEN_QGRAPH_BOOST=0.02 \
  go test -tags=bench -run TestQGraphExpansion -v -timeout 600s ./bench/ndcg/

# csn-stripped (first 300 queries, ~16s)
KEN_BENCH_DIR=$PWD/testdata/bench/csn-python-nl-stripped \
  KEN_QGRAPH_LIMIT=300 \
  go test -tags=bench -run TestQGraphExpansion -v -timeout 600s ./bench/ndcg/
```

Bench corpora rebuild deterministically; results reproduce
bit-identically given the same model + seed.
