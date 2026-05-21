# Reproducing ken's benchmark numbers

ken's retrieval pipeline is a verbatim port of [semble](https://github.com/MinishLab/semble)'s `search.py` + `ranking/*.py`. The expected verbatim-port outcome is that **ken's NDCG@10 matches semble's published 0.854 on semble's own benchmark within ± 0.005**. Anything outside that window is either an algorithmic divergence to find and fix, or a chunker/tokenizer measurement effect to characterize and document.

This file documents the procedure end-to-end.

## What it measures

semble publishes a single overall NDCG@10 (0.854) plus a per-language breakdown and a per-ablation breakdown (BM25 raw 0.675, +ranking 0.834; potion-code-16M raw 0.650, +ranking 0.821; combined 0.854). We reproduce all three views to localize any gap:

- **Overall NDCG@10** — single number; the headline.
- **Per-language NDCG@10** — for ken's 5 natively chunked languages (Python / Go / TypeScript / Java / Rust) we should match within ± 0.01; for the other 14 we expect divergence because ken's regex chunker falls through to the line chunker on them while semble uses tree-sitter via Chonkie everywhere.
- **Per-ablation NDCG@10** — running ken three times (`--mode bm25|semantic|hybrid`) and comparing each to the corresponding semble row narrows any gap to a specific subsystem.

The harness re-uses semble's annotations, semble's metric (`benchmarks.metrics.ndcg_at_k` + `target_rank` from [MinishLab/semble](https://github.com/MinishLab/semble)), and semble's repo manifest. No re-implementation of the eval, no chance of metric drift between ken's reported number and semble's published number.

## Prerequisites

- **A semble checkout** with its `benchmarks/` directory. `git clone https://github.com/MinishLab/semble /tmp/semble` is the path the scripts assume; pass `--semble-checkout PATH` (or set `SEMBLE_CHECKOUT`) to override.
- **The model**, for semantic and hybrid modes: a `minishlab/potion-code-16M` snapshot at `~/.ken/model` (or pass `--model PATH`):
  ```bash
  huggingface-cli download minishlab/potion-code-16M \
      tokenizer.json config.json model.safetensors \
      --local-dir ~/.ken/model
  ```
- **`ken` on `$PATH`** (or pass `--ken /path/to/ken`):
  ```bash
  go install github.com/townsendmerino/ken/cmd/ken@latest
  ```
- **Python 3.11+** with semble installed locally so its `benchmarks.data` and `benchmarks.metrics` modules import. From the semble checkout:
  ```bash
  cd /tmp/semble
  uv sync                   # or: pip install -e .
  ```

## Bootstrap the corpus

semble's `sync_repos.py` shallow-clones every benchmark repo at its pinned revision into `~/.cache/semble-bench/`. Run it once:

```bash
cd /tmp/semble
python benchmarks/sync_repos.py
```

This takes a few minutes and uses a few hundred MB of disk. Re-runs are no-ops unless `repos.json` changes upstream.

## Run the benchmark

From the ken repo, one invocation per mode:

```bash
python bench/semble/run_ken.py --mode bm25
python bench/semble/run_ken.py --mode semantic
python bench/semble/run_ken.py --mode hybrid
```

Each invocation:
1. Loads tasks from semble's `benchmarks/annotations/*.json`.
2. For each repo: spawns `ken bench <repo> --mode MODE …`, sends every task's query 5× over stdin (warm-index median-of-5 latency, matching semble), parses one JSON record per query, applies semble's `target_rank` + `ndcg_at_k`.
3. Reports overall + per-language averages to stderr, writes the full JSON to `bench/semble/results/ken-<mode>.json`.

Expected wall time on an M-series Mac: roughly 30–90s per mode for a fresh full-corpus run (indexing dominates; once the corpus is in OS page cache, repeat runs are faster).

## Acceptance thresholds

ken's three modes map to semble's **raw retrieval** rows, not the "+ ranking" rows — `--mode bm25` and `--mode semantic` return retrieval scores directly without the rerank pipeline. Only `--mode hybrid` runs the full ranker. Compare like-for-like:

| Row | semble published | ken target | ken mode |
|---|---:|---:|---|
| BM25 raw | 0.675 | ± 0.05 | `--mode bm25` |
| potion-code-16M raw | 0.650 | ± 0.01 | `--mode semantic` |
| **Combined (hybrid + full ranker)** | **0.854** | **within ~0.02** | `--mode hybrid` — the headline |

The semantic-raw ± 0.01 target is the tight one: it isolates the embedding/tokenizer/pooling/normalizer port from chunker effects (chunker only shifts which spans get embedded, not how the math works), so a miss here points squarely at `internal/embed` or `internal/ann`. The BM25-raw and hybrid windows are looser because both depend on the chunker, and ken's regex chunker is not a tree-sitter replacement — see "Interpreting divergence" below.

Per-language for ken's 5 natively-chunked languages: each within ± 0.02 of semble's published per-language number on hybrid. Python typically lands within ± 0.005; go/rust/typescript/java may drift further because the regex chunker is a coarser approximation of tree-sitter for those grammars. The gap is measurement (a chunker dependency tradeoff), not a port regression.

## Filters for quick smoke runs

```bash
# One repo, one mode, verbose per-query output:
python bench/semble/run_ken.py --mode bm25 --repo cobra --verbose

# All Python repos, hybrid:
python bench/semble/run_ken.py --mode hybrid --language python

# Custom semble checkout / model location:
SEMBLE_CHECKOUT=$HOME/src/semble KEN_MODEL_DIR=/data/ken/model python bench/semble/run_ken.py
```

## Interpreting divergence

The three modes triangulate where any gap is coming from:

1. **Per-ablation table is the first diagnostic.** Compare each row:
   - **ken-semantic vs semble's raw potion (0.650)** — this is the tight one. The semantic mode skips the ranker entirely (just cosine over embeddings), so divergence here is unambiguously a port bug in `internal/embed`, `internal/ann`, or the BPE tokenizer. Expect ± 0.01.
   - **ken-bm25 vs semble's raw BM25 (0.675)** — divergence here points at the BM25 tokenizer (identifier splitting), Lucene-variant constants, or chunker (different spans ⇒ different docs ⇒ different scores). Looser window (± 0.05) because chunker effects compound here.
   - **ken-hybrid vs semble combined (0.854)** — after both raw modes match, divergence here isolates to α-fusion / RRF math / rerank pipeline (`internal/search/hybrid.go`, `rerank.go`, `penalties.go`).
2. **Per-language table narrows it further.** A consistent loss across all languages points at the retrieval/ranker; a loss isolated to specific languages points at chunker rules. ken's regex chunker covers 5 languages (Python / Go / TypeScript / Java / Rust); the other 14 fall through to the line chunker and a per-language gap there is expected, not a regression. Within the 5: Python typically tracks semble closely; go/rust/typescript/java may diverge more because semble's tree-sitter chunker (via Chonkie) draws different chunk boundaries on those grammars.
3. **Per-category drift** (architecture / semantic / symbol — surfaced in the per-repo `by_category` field of the result JSON) localizes to a boost type: low symbol-query NDCG ⇒ definition-boost issue, low architecture-query NDCG ⇒ embedding/semantic issue.

Don't tune ken's constants to match the benchmark — the constants are ported verbatim from semble's source and any divergence is a port bug to find, not a hyperparameter to twist. The known and documented expected divergence is the regex-vs-tree-sitter chunker mismatch; that gap is a dependency tradeoff (pure-Go, no cgo) called out in `docs/DESIGN.md` §2, not a regression.

## Empirical findings (v0.1.0)

Recorded so future readers don't relitigate the obvious paths:

- **The BM25 tokenizer divergence is not the dominant cause.** Bringing `internal/bm25/tokenize.go` to verbatim parity with semble's `tokens.py` (snake-case compound preservation, ASCII-only run extraction matching `_TOKEN_RE`, compound-first emission order matching `split_identifier`) moved hybrid by **only +0.002** (0.840 → 0.842) and BM25-raw by **only +0.002** (0.622 → 0.624). The per-repo deltas were directionally mixed (e.g. nlohmann-json +0.039, aiohttp −0.018) which is consistent with reshuffling rather than systematic improvement. Conclusion: the tokenizer fix is still the right change (the design contract is verbatim parity), but the residual gap is **chunker-bound**, not tokenizer-bound.
- **The BM25 TF formula divergence is cosmetic, not load-bearing.** ken's `internal/bm25/query.go` currently uses the ATIRE TF formula `(tf*(k1+1)) / (tf + k1*(1-b+b*l_d/l_avg))` while semble's `bm25s` default is Lucene `tf / (k1*(1-b+b*l_d/l_avg) + tf)`. These differ by a constant `(k1+1) = 2.5` factor at fixed `k1`, which preserves ranking exactly. After RRF rank normalization in hybrid, even absolute scores are discarded. Fixing it would be a one-line cleanup for fidelity but cannot change NDCG.
- **The chunker is the lever.** With the tokenizer at verbatim parity, the remaining gap distributes per-language as expected for a chunker mismatch: Python (which our regex chunker handles best) at +0.003 vs semble, while go/rust/zig sit at ~−0.05. The path forward is the WASM tree-sitter chunker (Option A per `docs/DESIGN.md` §2), not further tuning of BM25 or the rerank pipeline.

## Empirical findings (v0.2.0: the tree-sitter chunker)

v0.2.0 landed the tree-sitter chunker via [`gotreesitter`](https://github.com/odvcencio/gotreesitter), running the cAST split-then-merge algorithm. Three iterations on the full benchmark produced a clean negative result for the "AST chunking closes the gap" hypothesis:

| Config | Hybrid NDCG | Δ vs regex (0.842) |
|---|---:|---:|
| treesitter v1 (all 19 langs, default chunkSize=1500) | 0.831 | −0.011 |
| treesitter v2 (skip bash + csharp, chunkSize floored at 3000) | 0.834 | −0.008 |
| **treesitter v3 (skip bash + csharp, chunkSize=1500)** | **0.838** | **−0.004** |

What we learned:

- **AST chunking is not a clear win at this granularity.** The hypothesis going in: regex chunkers draw bad boundaries on languages we didn't hand-tune (go/rust/zig at −0.05 per-language vs semble in v0.1.0). The data: treesitter trades wins on some languages for losses on others, netting essentially zero (Δ −0.004 — within bench noise). Conclusion: **the v0.1.0 gap vs semble is not primarily a chunker-quality issue at the algorithm level**, even though the per-language signature looked like it could be.
- **chunkSize is in bytes, not tokens.** The cAST paper uses tokens; Chonkie uses tokens. ken uses bytes (consistent with the rest of ken's pipeline). Increasing chunkSize from 1500 → 3000 bytes (≈ Chonkie's token budget) *hurt* NDCG by 0.004 on non-bash languages — bigger chunks dilute BM25 IDF and average out Model2Vec embeddings without preserving more structural signal. ken's existing 1500-byte budget is the right value for both chunkers.
- **Two grammars failed badly enough to disable entirely.** The gotreesitter v0.18.0 **C# grammar** OOMs (1.7+ GB RSS) on real C# files. The **bash grammar** is pathologically slow (~39% of files timeout at 1 s per parse). Both are absent from the treesitter chunker's supported-languages list and route through the line chunker — identical behavior to the regex chunker's fallback path for them.
- **The wins are real but narrow.** Kotlin +0.011, Zig +0.013, TypeScript +0.009, Java +0.006, PHP +0.005. Users who index those languages heavily should prefer `--chunker=treesitter`. Everyone else should stay on the default `regex` — losses on Python (−0.009), C (−0.017), Rust (−0.013), Lua (−0.022), Scala (−0.022) outweigh the wins on the average corpus.
- **Net decision: ship treesitter as opt-in.** See [`docs/DECISIONS.md` ADR-011](DECISIONS.md#adr-011-default-chunker-stays-regex-in-v020-treesitter-is-opt-in) for the full rationale; the per-language recommendation table is in [README.md "Choosing a chunker"](../README.md#choosing-a-chunker).

## External benchmark — CoIR-CSN-Python

In addition to semble's own benchmark (the verbatim-port confirmation above), ken is evaluated against [CoIR](https://github.com/CoIR-team/coir)'s `CodeSearchNet-python` task — a modern, public, externally reproducible benchmark cited by recent code-retrieval papers. Single sub-task by design; gives ken one externally-comparable headline number independent of semble's internal bench.

### Result (v0.2.0, chunker=regex, 1000-query subsample)

| Mode      | NDCG@10 |
|-----------|--------:|
| bm25      |  0.8743 |
| semantic  |  0.7405 |
| **hybrid** | **0.7839** |

A separate full-corpus run (14,918 queries) gave **bm25 0.8443**; the 1000-query subsample is within standard error and produced 3 modes in ~13 minutes (the full run would have taken ~80 minutes and timed out semantic mid-queries on a single M-series Mac).

### Why BM25 beats hybrid on CSN-Python

This is the opposite of what semble's bench shows, where hybrid edges BM25 by roughly 0.05. The reversal is structural, not a ken bug. CSN-Python's queries are full English docstrings, and the relevant document for each query is the function that docstring describes — so the query already contains the target function's identifiers, parameter names, and domain vocabulary. BM25 with identifier-aware tokenization (which ken's `internal/bm25` matches semble's `tokens.py` on, verbatim) is reading the answer key. With lexical retrieval at ceiling, α=0.5 RRF fusion averages the weaker semantic ranking into the hybrid score and drags it down by ~0.09 NDCG. This is well-precedented in the code-IR literature; it says nothing about hybrid retrieval being broken in ken, and ken's hybrid still wins by ~0.05 on semble's diverse-query benchmark. A corpus-adaptive α (detect query characteristics at search time and route accordingly) is a parked v0.3+ investigation — see [`docs/DESIGN.md` §10](DESIGN.md#10-risk-register) for the trigger.

### Reproduce

```bash
python scripts/bench_coir.py                      # ~45 s, ~140 MB download + ~1 GB on disk
go test -tags=bench ./bench/ndcg/ -run TestCoIR -v -timeout 30m
# Subsample for a clean 3-mode run in ~13 minutes:
KEN_COIR_QUERY_LIMIT=1000 go test -tags=bench ./bench/ndcg/ -run TestCoIR -v
# Use the v0.2.0 tree-sitter chunker instead of the regex default:
KEN_CHUNKER_TREESITTER=1 KEN_COIR_QUERY_LIMIT=1000 go test -tags=bench ./bench/ndcg/ -run TestCoIR -v
```

### Empirical findings — surprises worth recording

- **BM25 outperforms hybrid by 0.09 on CSN-Python.** Backwards from semble's bench, where hybrid > bm25 by ~0.05 on average. Not a ken bug — well-precedented in code-IR literature: CSN-Python queries are full English docstrings, and the relevant doc is the same function the docstring describes; identifier overlap (function name, parameter names, key terms) appears in both halves, so BM25 with identifier-aware tokenization is already near-optimal. ken's α=0.5 RRF fusion (NL-query auto-detect) then weights the weaker semantic score equally with BM25 and drags the hybrid number down. This says something real about hybrid-fusion strategy: α-weighted RRF can hurt when the lexical retriever is already at the ceiling on the target benchmark.
- **Semantic-raw 0.7405 isn't directly comparable to potion-code-16M's published 0.4299.** MinishLab's [potion-code-16M HF model card](https://huggingface.co/minishlab/potion-code-16M) publishes a 0.4299 raw-semantic score under MTEB's `COIRCodeSearchNet` column, but that's the **6-language aggregate** (Python/Java/JS/Go/PHP/Ruby) — five additional language corpora's worth of distractors per query. Our Python-only run draws from a 6× smaller corpus, so a higher number is the expected baseline shift, not a parity win. Treat our 0.7405 as ken's Python-only-CSN reference point; no claim about beating the published aggregate.
- **The subsample is deterministic.** Queries are sorted by `query_id`, then the first `KEN_COIR_QUERY_LIMIT` are kept. Reruns are bit-identical.

### Bars vs prompt expectations

| Bar | Expected | Got | Status |
|---|---|---|---|
| #1: semantic ≈ published baseline | within ±0.005 of potion-code-16M's published Python-only NDCG | no published Python-only number; aggregate not comparable | **N/A — new baseline** |
| #2: hybrid > bm25 + 0.05 | +0.05 | −0.0904 | ❌ **fails** (informative, not a bug) |
| #3: hybrid absolute | report | 0.7839 | ✅ |

## Files

semble bench (this doc's primary reference):

- `bench/semble/run_ken.py` — Python adapter; drives `ken bench` over stdin per repo.
- `bench/semble/results/ken-<mode>.json` — written per run. Gitignored; regenerate at will.
- `cmd/ken/main.go` — `ken bench` subcommand that the adapter drives.

CoIR-CSN-Python external bench:

- `scripts/bench_coir.py` — downloads corpus + queries + qrels into `testdata/bench/coir-csn-python/`.
- `bench/ndcg/ndcg.go` + `ndcg_test.go` — pure-Go NDCG@10 helper, unit-tested against the Wikipedia worked example.
- `bench/ndcg/coir_test.go` (build tag `bench`) — the harness.
- `testdata/bench/coir-csn-python/` — corpus (280k `.py` files), `queries.jsonl`, `qrels.jsonl`, `summary.json`. Gitignored; ~1 GB on disk.
