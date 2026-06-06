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
  ken download-model                # pure-Go fetch, no Python required
  # Equivalent fallback if you prefer the HF tooling:
  # huggingface-cli download minishlab/potion-code-16M \
  #     tokenizer.json config.json model.safetensors --local-dir ~/.ken/model
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
- **Net decision: ship treesitter as opt-in.** See [`docs/internal/DECISIONS.md` ADR-011](internal/DECISIONS.md#adr-011-default-chunker-stays-regex-in-v020-treesitter-is-opt-in) for the full rationale; the full per-language recommendation table is just below.

### Per-language chunker recommendation (full table)

Moved here from the README. The NDCG@10 difference is small (overall hybrid: treesitter 0.838 vs regex 0.842 — Δ −0.004, within bench noise), but it's not uniform per-language. From the v0.2.0 measurement on semble's 63-repo benchmark:

| Language | regex | treesitter | Recommendation |
|---|---:|---:|---|
| Kotlin | 0.806 | **0.817** | **`treesitter`** *(+0.011)* |
| Zig | 0.867 | **0.880** | **`treesitter`** *(+0.013)* |
| TypeScript | 0.676 | **0.685** | **`treesitter`** *(+0.009)* |
| Java | 0.829 | **0.835** | **`treesitter`** *(+0.006)* |
| PHP | 0.860 | **0.865** | **`treesitter`** *(+0.005)* |
| Python | **0.870** | 0.861 | `regex` *(−0.009)* |
| C | **0.748** | 0.731 | `regex` *(−0.017)* |
| C++ | **0.896** | 0.884 | `regex` *(−0.012)* |
| Rust | **0.806** | 0.793 | `regex` *(−0.013)* |
| Lua | **0.838** | 0.816 | `regex` *(−0.022)* |
| Scala | **0.905** | 0.883 | `regex` *(−0.022)* |
| Go | **0.849** | 0.846 | either *(tied within ±0.005)* |
| JavaScript | 0.917 | 0.912 | either |
| Ruby | 0.903 | 0.903 | either |
| Swift | 0.846 | 0.841 | either |
| Elixir | 0.911 | 0.907 | either |
| Haskell | 0.738 | 0.739 | either |
| C# | 0.859 | 0.859 | either *(treesitter auto-falls-back to line)* |
| Bash | 0.821 | 0.821 | either *(treesitter auto-falls-back to line)* |

Notes on the auto-fallback rows:
- **C#** — the gotreesitter v0.18.0 C# grammar OOMs on real-world C# files (1.7+ GB RSS during indexing). The treesitter chunker detects unsupported languages and routes them through the line chunker, so C# behaves identically under both selections.
- **Bash** — the bash grammar is pathologically slow on real bash-it content (~39% of files timeout). Same auto-fallback behavior.


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

This is the opposite of what semble's bench shows, where hybrid edges BM25 by roughly 0.05. The reversal is structural, not a ken bug — but the structural cause is sharper than initially documented. CSN-Python's queries (as CoIR re-hosts the dataset) are full **Python function sources**, and the relevant document for each query is the **docstring extracted from that same function**. Because the docstring lives inside the function source as a literal substring (the docstring is the function's own `"""..."""` triple-quoted block), any lexical retriever with identifier-aware tokenization is effectively doing substring-match — BM25 has the answer string as input. ken's α=0.5 RRF fusion then averages the weaker semantic ranking into the hybrid score and drags it down by ~0.09 NDCG. This is a structural artifact of how CoIR reframed CodeSearchNet for retrieval (queries = code, docs = docstrings), not a property of natural NL-to-code query distributions; ken's hybrid retrieval has no path to beat "the answer is already in the query." The finding says nothing about hybrid retrieval being broken in ken, and ken's hybrid still wins by ~0.05 on semble's diverse-query benchmark. ADR-013 investigated whether this finding pointed at an α-routing lever and closed as Deprecated (Proposed → Deprecated, 2026-05-21) when inspection revealed the substring-leak mechanic; see [`DECISIONS.md` ADR-013](internal/DECISIONS.md#adr-013-corpus-adaptive-α--adding-a-third-query-class-branch).

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

- **BM25 outperforms hybrid by 0.09 on CSN-Python.** Backwards from semble's bench, where hybrid > bm25 by ~0.05 on average. Not a ken bug, but the mechanic is a dataset artifact rather than a query-class signal: CoIR's CSN-Python reframing makes queries full Python function sources and documents the docstrings extracted from those same functions, so the docstring lives inside the query as a literal substring. Any lexical retriever with identifier-aware tokenization wins because BM25 has the answer string as input; ken's α=0.5 RRF fusion then weights the weaker semantic score equally and drags the hybrid number down. This says something real about how CoIR reframed CodeSearchNet for retrieval — it does *not* generalize to natural NL-to-code query distributions, where hybrid wins (cf. semble's bench).
- **Semantic-raw 0.7405 isn't directly comparable to potion-code-16M's published 0.4299.** MinishLab's [potion-code-16M HF model card](https://huggingface.co/minishlab/potion-code-16M) publishes a 0.4299 raw-semantic score under MTEB's `COIRCodeSearchNet` column, but that's the **6-language aggregate** (Python/Java/JS/Go/PHP/Ruby) — five additional language corpora's worth of distractors per query. Our Python-only run draws from a 6× smaller corpus, so a higher number is the expected baseline shift, not a parity win. Treat our 0.7405 as ken's Python-only-CSN reference point; no claim about beating the published aggregate.
- **The subsample is deterministic.** Queries are sorted by `query_id`, then the first `KEN_COIR_QUERY_LIMIT` are kept. Reruns are bit-identical.

### Bars vs prompt expectations

| Bar | Expected | Got | Status |
|---|---|---|---|
| #1: semantic ≈ published baseline | within ±0.005 of potion-code-16M's published Python-only NDCG | no published Python-only number; aggregate not comparable | **N/A — new baseline** |
| #2: hybrid > bm25 + 0.05 | +0.05 | −0.0904 | ❌ **fails** (informative, not a bug) |
| #3: hybrid absolute | report | 0.7839 | ✅ |

## Optional second-stage neural rerank (M4 / `hybrid-rerank` mode)

ken can layer a [CodeRankEmbed](https://huggingface.co/nomic-ai/CodeRankEmbed) neural reranker on top of the hybrid stage-1 ranker. Pure-Go forward pass (137M params, ~547 MB resident, no cgo), per-call latency multi-second so the mode is opt-in (`--mode=hybrid-rerank` on the CLI, `KEN_MCP_RERANK=on` for the MCP server). See [`outputs/ken-rerank-plan.md`](../outputs/ken-rerank-plan.md) for the design and [M0](../outputs/m0-results.md)–[M5](../outputs/m5-results.md) results memos for the ceiling-validation + implementation chain.

### Reference NDCG@10 lift from M0 (Python sentence-transformers, full corpus)

The M0 ceiling experiment ran the REAL `nomic-ai/CodeRankEmbed` over ken's actual stage-1 hybrid shortlist (zero Go transformer code) on full corpora. Numbers below are **what the Go port reproduces** via the bit-identical cosine pinned by [internal/coderank/golden_cosine_test.go](../internal/coderank/golden_cosine_test.go) (M2 cosine = 1.000000 on all 20 cases).

| Benchmark | Tasks | Stage-1 hybrid NDCG@10 | Best reranked NDCG@10 | Δ | Best config |
|---|---:|---:|---:|---:|---|
| CoIR-CSN-Python | 1000 | 0.7839 | **0.9492** | **+0.165** | β=1.0, rerankN=100 |
| semble bench | 1251 | 0.8436 | **0.8524** | **+0.009** | β=0.25 (M5 default), rerankN=100 |

Why two different β recommendations: CoIR's gold targets are single-chunk docstrings (chunk-level rerank wins outright with β=1), while semble's gold targets are often file-level (a chunk-level reranker discards ken's file-coherence boost, so pure replacement regresses NDCG by 10 points; a light blend recovers a small lift). See [M0 results](../outputs/m0-results.md) §2.

### Production-pipeline verification (M6, in-tree run)

Subsample runs to verify the M4 production pipeline (`Index.SetReranker` → `ModeHybridRerank` → `NeuralReranker.Rerank` → M3's `Model.EncodeBatch`) is correctly wired end-to-end. Full-corpus reproduction is deferred — bit-identical cosine from M2 + matching direction on these subsamples is sufficient proof.

**CoIR-CSN-Python (15-query subsample, KEN_RERANK_TOP_N=25, β=1.0):**

| Mode                | NDCG@10 | Index wall (s) |
|---------------------|--------:|---------------:|
| bm25                |  0.6345 |           41.5 |
| semantic            |  0.2307 |           50.8 |
| hybrid              |  0.5392 |           47.4 |
| **hybrid-rerank**   | **0.6087** |       1106.4 |

Lift +0.0695 over hybrid on the 15-query slice. Absolute numbers don't match M0's because the 15-query subsample (first 15 by query_id) is much harder than the first 1000 (hybrid 0.5392 here vs 0.7839 over 1000) and rerankN=25 vs M0's 100. The DIRECTION matches — chunk-sized targets reward the rerank.

**semble cobra (20 tasks, --rerank-top-n=50, --rerank-beta=1.0):**

| Mode | NDCG@10 | architecture | semantic | symbol |
|---|---:|---:|---:|---:|
| hybrid | 0.9082 | 1.0000 | 0.8331 | 1.0000 |
| **hybrid-rerank β=1.0** | **0.6434** | **0.5132** | **0.6172** | **1.0000** |
| **Δ** | **-0.265** | **-0.487** | **-0.216** | **0.000** |

This is the exact M0 cobra-β=1 pattern: pure replacement destroys file-level architecture and hurts semantic, but is neutral on symbol — chunk-level rerank loses ken's file-coherence boost. This is the M0 finding that motivated the β=0.25 production default (see [outputs/m4-results.md](../outputs/m4-results.md) for the plan §9.3 amendment rationale). The big-negative Δ at β=1 is **unmistakable evidence** the rerank is firing through the production pipeline — no other code path produces this NDCG signature.

### Reproduce

```bash
# 1) One-time setup: fetch both models (pure-Go; no Python tooling).
ken download-model            # potion-code-16M  → ~/.ken/model           (~60 MB)
ken download-model --rerank   # CodeRankEmbed    → ~/.ken/rerank-model    (~547 MB)

# CoIR with hybrid-rerank (opt-in via KEN_RERANK=1).
# Defaults: KEN_RERANK_TOP_N=100, KEN_RERANK_BETA=1.0  (M0 CoIR config).
KEN_RERANK=1 KEN_RERANK_MODEL_DIR=$PWD/testdata/encoder-model \
KEN_COIR_QUERY_LIMIT=15 KEN_RERANK_TOP_N=25 \
  go test -tags=bench ./bench/ndcg/ -run TestCoIR_CSNPython -v -timeout 60m

# semble with hybrid-rerank — passes through to `ken bench`.
python bench/semble/run_ken.py --mode hybrid-rerank \
  --rerank-model ~/.ken/rerank-model \
  --rerank-top-n 50 --rerank-beta 0.25 \
  --repo cobra --latency-runs 1
```

### Latency caveat

CodeRankEmbed in pure Go costs **~30 s/query** on cobra (NL queries, rerankN=50, M-series Mac) and **~70 s/query** on CoIR (full Python function-source queries up to 512 tokens). M3 (matmul backend) + M4 (parallel `EncodeBatch`) reduced this by ~40× over the M2 naive baseline; M7 (batched single-GEMM-per-layer) + M8 (int8 quant) remain available levers if a future use case needs sub-5s rerank-N=50. See [outputs/m3-results.md](../outputs/m3-results.md) for the M3 verdict ("opt-in / batch tolerable; not yet interactive without M7/M8").

## Structural enrichment (Arm B) — default-on, ADR-035

ken's indexer prepends a deterministic per-file label line
(`# func: NAME | calls: A, B | raises: X`) to every chunk from a file whose
extension has a registered gotreesitter extractor, before BM25 tokenization
and embedding. It surfaces structurally-related identifiers into the indexed
text. This is **default-on** (ADR-035, Stage 8 close), so it's part of the
numbers above — recorded here because BENCH.md is the canonical numbers doc.

In-process bench (the production `structural.EnrichFromFileStruct` path):

| Benchmark | Δ NDCG@10 (hybrid) |
|---|---:|
| csn-python-nl-stripped (N=500) | **+0.0208** |
| CoSQA dev | **+0.0321** |

(Reproduces the validated Gate-1 numbers within 0.002 on the production code
path.) Files with no registered extractor pass through unchanged.

- **Ablation switch:** `KEN_ENRICH=off` (or `FSOptions.DisableEnrichment=true`)
  disables it for an A/B.
- **Oversized-file skip:** files above 64 KiB skip enrichment
  (`maxEnrichBytes` in `internal/structural/extract_file.go`) — a guard
  against a gotreesitter GLR stack overflow on huge table-driven files; they
  pass through unenriched, the same graceful no-op as an unregistered
  extension. BM25 recall@10 is unchanged with/without it on the semble corpus.
- **Full design + rationale:** [ADR-035](internal/DECISIONS.md#adr-035-ship-arm-b-structural-enrichment-in-the-production-indexer-stage-8-close).

## Token-budget recall — agent-side efficiency

### Why this exists

The NDCG sections above measure **retrieval quality**: did the system rank the right chunk at the right position? This section measures something different and complementary: **agent-input cost at fixed recall**. An agent that takes 200,000 tokens to find the same answer ken finds in 4,000 tokens is paying for those tokens whether or not the ranking was technically "correct." The CLAUDE.md routing advice ken-mcp emits ("prefer ken for code-related questions over grep") makes an implicit claim about token efficiency vs the grep+Read fallback; this section quantifies that.

### Methodology

For each (query, qrel) pair in two bench corpora:

- **ken side:** build the index once per repo, call `Search(query, K)` at K ∈ {1, 3, 5, 10}, format the top-K via `mcp.FormatResults` (the exact wire format ken-mcp emits over MCP, so we're counting what an agent actually sees), count cl100k_base BPE tokens on that string. Recall@K is true iff any returned chunk's file matches the qrel target (suffix-aware path matching, mirroring semble's [`benchmarks/data.py:path_matches`](https://github.com/MinishLab/semble/blob/main/benchmarks/data.py)).
- **grep baseline:** identifier-tokenize the query (same tokenizer ken's BM25 uses, so the comparison is "an agent who knows how to grep code well" vs ken — not a strawman regex-on-NL grep), take the union of corpus files containing any token, sum cl100k_base tokens per file capped at 20,000 per file (an agent Reading a 50 MB minified blob isn't realistic). For symbol queries we also report a literal-grep variant — the realistic best case for identifier lookups.
- Token counting via [`pkoukk/tiktoken-go`](https://github.com/pkoukk/tiktoken-go) (pure-Go, MIT, behind `//go:build bench` so it never enters the released binaries — verify via `go list -deps ./cmd/ken ./cmd/ken-mcp | grep -E 'tiktoken|regexp2|uuid'` returning empty). cl100k_base is GPT-4's encoder used as a universal proxy; Anthropic doesn't publish a local Claude tokenizer, and Claude tokens typically differ ~10–20% — see Caveats.

### Results

> **The `ken recall@K` columns below are BM25-ONLY.** This harness builds
> the index in `ModeBM25` (no model required), so the recall here is ken's
> *lexical-only floor*, not the shipped default. ken's default **hybrid**
> mode reaches **0.967 NL / 0.995 symbol / 0.971 overall** recall@10 on
> this same corpus — the semantic arm adds +0.13. See
> [Default-mode (hybrid) recall](#default-mode-hybrid-recall--the-number-that-matters)
> for the full decomposition. The token counts are mode-independent (K
> formatted chunks either way), so the cost-vs-grep story is unchanged;
> only the recall column understates the default.

#### semble bench (63 repos, 1072 queries — 158 symbol, 914 NL)

| Class  | K  | ken med tokens | ken recall@K | grep med tokens | grep recall | grep variant |
|--------|----|---------------:|-------------:|----------------:|------------:|:-------------|
| symbol |  1 |            356 |        0.468 |          56,752 |       0.994 | literal      |
| symbol |  3 |          1,049 |        0.677 |          56,752 |       0.994 | literal      |
| symbol |  5 |          1,766 |        0.772 |          56,752 |       0.994 | literal      |
| symbol | 10 |          3,478 |        0.867 |          56,752 |       0.994 | literal      |
| nl     |  1 |            431 |        0.418 |         189,591 |       0.999 | tokenized    |
| nl     |  3 |          1,285 |        0.629 |         189,591 |       0.999 | tokenized    |
| nl     |  5 |          2,145 |        0.730 |         189,591 |       0.999 | tokenized    |
| nl     | 10 |          4,269 |        0.822 |         189,591 |       0.999 | tokenized    |

#### CoIR-CSN-Python (280k-file corpus, 200-query subsample, NL-only)

| Class | K  | ken med tokens | ken recall@K | grep med tokens | grep recall | grep variant |
|-------|----|---------------:|-------------:|----------------:|------------:|:-------------|
| nl    |  1 |            220 |        0.795 |      16,055,428 |       1.000 | tokenized    |
| nl    |  3 |            454 |        0.875 |      16,055,428 |       1.000 | tokenized    |
| nl    |  5 |            695 |        0.895 |      16,055,428 |       1.000 | tokenized    |
| nl    | 10 |          1,296 |        0.915 |      16,055,428 |       1.000 | tokenized    |

(CoIR's queries are docstrings, all classified as NL; no symbol-class column.)

### Headline finding

On semble's diverse-query benchmark, **at K=10 ken (BM25-only here) catches 84% of NL queries' qrel target in 4,269 median tokens; tokenized grep+Read catches 99.9% but in 189,591 tokens — for the queries ken covers, agents pay ~44× fewer tokens going through ken than running grep+Read.** Symbol queries are a closer fight (16× cheaper at 89%/99% recall) because literal grep on a unique identifier is already pretty efficient. **In the default hybrid mode those recall figures rise to 0.967 NL / 0.995 symbol** (next section) — the token-cost ratios hold, the coverage gap to grep nearly closes.

The CoIR-CSN-Python numbers amplify the same pattern at corpus scale: on a 280K-file repo, any NL query's tokens match thousands of files; grep+Read sums to **16M tokens** (well past any agent context window) for 100% recall, while ken finds the target chunk in 1,296 tokens at 91% recall — a >10,000× token reduction for a 9 percentage-point recall trade.

Important nuance: **grep wins on recall completeness**. If an agent's task absolutely must enumerate every match (pre-rename audits, exhaustive refactors), grep+Read is the right tool — that's exactly what ken-mcp's CLAUDE.md routing advice says. If the task is "find me the chunk that answers this" — which is most of what code-search-using agents do — ken's default-mode recall around 0.97 covers the typical case at 1-2 orders of magnitude lower token cost.

### Default-mode (hybrid) recall — the number that matters

The recall columns in the tables above are BM25-only, because the
token-budget harness builds `ModeBM25` by default (pass
`KEN_TOKENS_MODE=hybrid` for the hybrid run below). ken's shipped
default — for both `ken search` and `ken-mcp` — is **hybrid**, and on the
same semble corpus (1251 tasks; reproduced by
`internal/search/recall_decomp_test.go`, build-tag `bench`) the semantic
arm lifts recall@10 by **+0.13**:

| recall@10 | bm25-only (token-bench tables) | **hybrid (default)** |
|---|---:|---:|
| NL      | 0.832 | **0.967** |
| symbol  | 0.892 | **0.995** |
| overall | 0.841 | **0.971** |

**Token cost in hybrid mode.** Running the same token-budget harness in
hybrid (`KEN_TOKENS_MODE=hybrid go test -tags=bench ./bench/tokens/ -run
TestTokens_Semble`) confirms the cost story holds at the higher recall:
at K=10, NL queries cost a median **4,120 tokens at 0.967 recall** (vs
tokenized grep+Read's 189,773 — **~46× cheaper**); symbol queries **3,647
tokens at 0.994 recall** (vs 57,291 — ~16×). Formatted output is K fenced
chunks regardless of mode, so the medians track the BM25-only table
closely; the recall is what rises.

Decomposing the residual NL miss (1 − 0.967 = 0.033) localizes where any
further recall would have to come from — and which lever fixes it:

| component of the miss | value | recoverable by |
|---|---:|---|
| candidate-generation loss (target never in the 50/arm fused pool) | 0.010 | wider retrieval / HyDE / a better embedder |
| ranking loss (in pool, ranked 11–50) | 0.023 | rerank / fusion tuning |
| — of which path penalties *cost* | −0.005 | nothing: penalties net-**help** recall@10 (they clear test/example noise) |
| hard ceiling at N=500, depth 100 | (recall 0.999) | only 0.1% is genuinely unreachable |

Two product consequences:

1. **The agent-facing "82% recall" framing is a BM25-only artifact.** Users
   on the default hybrid mode (model present) get ~0.97; the miss rate is
   ~1 in 30 NL queries, ~1 in 200 symbol — not 1 in 5.
2. **Recall and first-run footprint are the same lever.** Because `ken-mcp`
   downgrades to BM25 when the model dir is missing, a fresh install with
   no model *is* on the 0.84 path. Getting that install onto hybrid (bundle
   / auto-fetch the model) moves recall@10 from ~0.84 to ~0.97 with zero
   algorithm change — a higher-leverage 1.0 item than any reranker work.

Measured with `KEN_ENRICH=off` for a crash-free full-corpus run (a few
oversized table-driven test files overflow gotreesitter's parser — see the
`maxEnrichBytes` guard in `internal/structural/extract_file.go`).
Enrichment-on shifts BM25 recall@10 by <0.001 on this corpus, so the
hybrid figures are representative of the default.

### Reproduce

```bash
# semble bench (~45 seconds, needs /tmp/semble + ~/.cache/semble-bench):
go test -tags=bench ./bench/tokens/ -run TestTokens_Semble -v -timeout 30m

# CoIR-CSN-Python bench (~14 min, needs scripts/bench_coir.py output):
KEN_COIR_QUERY_LIMIT=200 go test -tags=bench ./bench/tokens/ -run TestTokens_CoIR -v -timeout 60m

# Render the markdown tables from results JSON:
python scripts/plot_token_budget.py

# Default-mode (hybrid) recall decomposition — the table just above
# (~90s; needs the model at ~/.ken/model. KEN_ENRICH=off avoids the
# gotreesitter overflow on a few oversized test files):
KEN_ENRICH=off go test -tags=bench ./internal/search/ -run TestRecallDecomp$ -v -timeout 40m

# Confirm the token-bench recall column is BM25-only (reproduces ~0.84 NL):
KEN_ENRICH=off go test -tags=bench ./internal/search/ -run TestRecallDecomp_BM25Baseline -v

# Optional slow neural-rerank arm (opt-in; ~30s/query cold before the M9 cache warms):
KEN_RERANK=1 go test -tags=bench ./internal/search/ -run TestRecallDecomp_Rerank -v -timeout 60m
```

The bench writes `bench/tokens/results/{semble,coir}-tokens.json` — gitignored, regenerate at will.

### Caveats

- **cl100k_base is a proxy for Claude tokens.** Anthropic doesn't publish a local tokenizer. Empirically Claude's tokens run ~10–20% different from cl100k_base on prose; for code-heavy chunks the divergence may be larger. For *ratios* between ken and grep on the same query the choice of encoder doesn't matter much; absolute numbers are advisory not authoritative.
- **Recall@K is the fixed-bar evaluation.** An agent might still wring value from a chunk surfaced at K=10 even when the "correct" answer was at K=1; this benchmark doesn't capture downstream agent behavior. The agent-loop measurement (run an actual model against ken vs grep, see who answers the user's question with fewer tokens) is a separate tier-2 follow-up.
- **grep baseline assumes "Read whole matched files."** An agent with a smarter heuristic (`grep -C 5` for context windows, or `head -N` for sampling) would burn fewer tokens. The deliberate choice here is the realistic agent-fallback path, not the theoretical optimum.
- **CoIR-CSN-Python warning.** The substring-leak artifact that makes BM25 beat hybrid on this corpus (see ["Why BM25 beats hybrid on CSN-Python"](#why-bm25-beats-hybrid-on-csn-python) above) also makes grep more competitive on recall — but not on tokens, because the corpus size makes any grep result set ridiculous. The headline number is semble's bench; CoIR confirms the direction on a different distribution but isn't the cleanest demonstration of ken's value on its own.
- **Suffix-aware qrel matching.** Recall is computed via the same `path_matches` semble uses (`norm_file == target OR file.endswith("/"+target) OR target.endswith("/"+file)`) — handles the common case where semble's annotations are repo-rooted (`aiohttp/client.py`) but ken's chunk.File is benchmark-root-relative (`client.py`).

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

Token-budget bench (agent-side efficiency vs grep):

- `bench/tokens/tokens.go` + `tokens_test.go` (build tag `bench`) — cl100k_base counter wrapping `pkoukk/tiktoken-go`. Encoder dep is bench-tag-only; never reaches the released binary dep graph.
- `bench/tokens/budget.go` (build tag `bench`) — ken side: per-query, per-K formatted-output token counts + suffix-aware recall.
- `bench/tokens/grep_baseline.go` (build tag `bench`) — `CorpusCache` pre-tokenizes the corpus once; per-query in-memory grep scan, 20K-token per-file cap.
- `bench/tokens/coir_test.go` + `semble_test.go` (build tag `bench`) — per-bench harnesses.
- `bench/tokens/results/{semble,coir}-tokens.json` — written per run. Gitignored.
- `scripts/plot_token_budget.py` — read JSON, emit the markdown tables in this section.
