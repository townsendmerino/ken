# Budget packing: options beyond file saturation (2026-10-30)

**Status:** research note, no registrations. Since written: threshold 2 shipped as the opt-in `BudgetPacking` (ADR-051, Francis's call); the note's top option would replace it, not add a path. Asked for by Francis (2026-10-30 11:52): "try a milder setting but also can you investigate other approaches (more modes, what do peers do, etc.) get creative." The measured background is in [`docs/BENCH.md`](../BENCH.md), "ContextBench CB1": Lite results, the spread diagnostic, the ablation, and the packing (`KP`) and threshold-2 (`KS2`) gates.

## The problem in one paragraph

A caller that fills a token budget from ken's ranked list (an agent's context fill) gets a list shaped for a ranked top 10. The file-saturation decay inherited from semble halves each further chunk from the same file. That spreads the list across files: on ContextBench Lite at 8,192 tokens, shipped ken reaches the right files far more often than raw BM25 (file coverage 0.506 against 0.353) but covers fewer gold lines (0.159 against 0.185). Turning the decay off (`KP`) gains +0.071 lines and loses −0.063 files; a threshold of 2 (`KS2`) gains +0.028 and loses −0.030. Neither cleared the registered file floor (−0.04 lower bound). The question is whether a different shape gets most of the line gain while keeping files.

## What peers do

Sources: a read-only survey of primary sources (code, docs, papers), 2026-10-30.

- **Aider repo map** ([`aider/repomap.py`](https://github.com/Aider-AI/aider/blob/main/aider/repomap.py)). Tree-sitter definitions and references form a file graph, with edges weighted by identifier (×10 for identifiers mentioned in chat, ×0.1 for identifiers defined in more than 5 files). Personalized PageRank over the graph, seeded with chat files and mentioned names, ranks definitions. A binary search over the prefix of the ranked tag list then fits the rendered skeleton (definition lines plus enclosing scope headers, no bodies) to the token budget within 15%. There is no per-file cap. Breadth comes from showing signatures, not bodies.
- **Sourcegraph Cody** ([blog](https://sourcegraph.com/blog/lessons-from-building-ai-coding-assistants-context-retrieval-and-evaluation)). Parallel retrievers: keyword/BM25 via Zoekt, embeddings, a static-analysis graph, and local editor context. A transformer reranker scores items pointwise. Selection is framed as a knapsack problem: the top items that fit. No per-file cap or diversity step is documented.
- **Continue `@codebase`** ([`core/context/retrieval`](https://github.com/continuedev/continue/tree/main/core/context/retrieval)). `nFinal = min(25, contextLength/512/2)`. With a reranker, it retrieves 2×nFinal from full-text search, embeddings, recent files and the repo map, deduplicates, reranks and truncates. Without one, it uses fixed per-source quotas (25% recent, 25% full-text, 50% embeddings). No per-file cap.
- **Agentless** ([arXiv 2407.01489](https://arxiv.org/html/2407.01489v2)). Localizes hierarchically: top-3 files (LLM over the repo tree, combined with embedding retrieval), then classes and functions from file *skeletons*, then edit lines. Skeletons beat full files (58.3% against 53.7% containment) at a seventh of the cost, and going straight from files to lines did worse.
- **SWE-agent** ([arXiv 2405.15793](https://arxiv.org/html/2405.15793v3)). A 100-line file window beat both 30 lines and whole files (18.0% against 14.3% and 12.7% resolved). Search shows at most 50 results, and summarized search beat iterative search.
- **Context post-processors.**
  - LangChain/LlamaIndex `LongContextReorder` puts the best items at both ends.
  - `EmbeddingsRedundantFilter` drops near-duplicates (cosine > 0.95).
  - `EmbeddingsClusteringFilter` keeps one item per k-means cluster.
  - LongLLMLingua ([arXiv 2310.06839](https://arxiv.org/html/2310.06839)) reorders by question-conditioned perplexity and compresses each item by its rank.
- **MMR** (Carbonell & Goldstein, SIGIR '98). Picks greedily by λ·relevance − (1−λ)·max-similarity to items already chosen (LangChain default λ = 0.5). No code assistant above documents using it.
- **Lost in the Middle** ([arXiv 2307.03172](https://arxiv.org/html/2307.03172)). Answer accuracy is U-shaped in position. Put the best items first (or at both ends).
- **Hierarchical Context Pruning** ([arXiv 2406.18294](https://arxiv.org/html/2406.18294)). The top-k functions get full bodies and the next tier only signatures; the rest are dropped. Input shrank from about 50k to 8k tokens with higher exact match.

**Patterns.** (1) Coarse to fine: files, then symbols, then lines. (2) Signatures or skeletons for breadth, bodies for depth. (3) Graph ranking over references. (4) Rank globally, then fill the budget. (5) Per-source quotas and similarity-based diversity. Nobody documents a per-file chunk decay like ken's. (6) Order and compress after selection.

## Options

Each option lists: what changes in ken, a no-model screen on data we have, and the risk of fitting the benchmark. Screens use the offline simulator (below) on the **tuning half** of Lite only.

### Screen results (tuning half, 250 tasks; paired against simulated `K`; numpy bootstrap, seed 42)

At 8,192 tokens (16,384 in the second column pair):

| Policy | ΔLC @8k | ΔFC @8k | ΔLC @16k | ΔFC @16k |
|---|---|---|---|---|
| threshold 2 (`KS2`, shipped opt-in) | +0.035 [+0.018, +0.053] | −0.041 [−0.061, −0.023] | +0.027 | −0.026 |
| threshold 1, **decay 0.75** | +0.037 [+0.024, +0.053] | **−0.016** [−0.027, −0.007] | +0.034 | −0.021 |
| threshold 1, **decay 0.8** | **+0.041** [+0.026, +0.059] | **−0.020** [−0.034, −0.008] | +0.040 | −0.022 |
| threshold 1, decay 0.9 | +0.058 [+0.039, +0.079] | −0.034 [−0.050, −0.020] | +0.065 | −0.035 |
| threshold 3 | +0.058 | −0.049 | +0.050 | −0.037 |
| no saturation (`KP`) | +0.072 [+0.046, +0.099] | −0.080 [−0.108, −0.053] | +0.079 | −0.066 |
| per-file quota 2 / 3 / 4 | +0.030 / +0.057 / +0.064 | −0.039 / −0.048 / −0.051 | | |
| reserve top-5 files, then fill by score (no saturation) | +0.072 | −0.078 | | |
| reserve top-10 files, then fill | +0.068 | −0.065 | | |
| file-first: top 5 / top 10 files, then chunks | +0.067 / +0.080 | −0.202 / −0.124 | | |
| file-grouped: top 5 files' chunks together | +0.048 | −0.256 | | |
| language-adaptive: shipped saturation for C/C++, none elsewhere | +0.071 | −0.061 | +0.078 | −0.057 |
| language-adaptive: shipped for C/C++/Rust/Java, none elsewhere | +0.067 | −0.049 | +0.076 | −0.046 |

Decay sweep at threshold 1, 8,192 tokens (ΔLC / ΔFC): 0.6 +0.015/−0.011, 0.65 +0.025/−0.012, 0.7 +0.029/−0.014, 0.75 +0.037/−0.016, 0.8 +0.041/−0.020, 0.85 +0.049/−0.030, 0.9 +0.058/−0.034, 1.0 +0.072/−0.080. The frontier is smooth, and every decay from 0.6 to 0.8 trades more lines per file lost than any threshold, quota, reserve or file-first shape.

### Option 1 — a gentler decay instead of a later start (recommended next gate)

- **Change in ken.** `BudgetPacking` keeps threshold 1 and uses decay **0.8** instead of threshold 2 with decay 0.5. That is a constant in `engine` and a `SaturationDecay` field on `search.RankAblation` (the bench seam). Same option, same API. It would **replace** threshold 2 (Francis, 2026-10-30), not add a mode.
- **Why it should work.** Threshold 2 lets every file's second chunk through at full score, including files whose second chunk is weak, which costs files. A gentler decay lets a strong second chunk through and still pushes weak ones below other files' first chunks.
- **Screen.** On the tuning half it matches threshold 2's line gain (+0.041 against +0.035) at half the file cost (−0.020 against −0.041). At 16,384 it is +0.040 / −0.022.
- **Benchmark-fitting risk.** 0.8 was picked from a sweep on the tuning half. A gate must run on the **held-out half** only (250 tasks, chosen before any screen), with the same rules (lines lower bound > +0.01, files > −0.04) plus semble NDCG reported. Expect intervals about √2 wider than a 500-task gate's.
- **Cost.** A small code change, about 1 hour of mbp2 for 250 tasks × 1 arm, and about 2 minutes of semble scoring.

### Option 2 — breadth from outlines, depth from chunks (peer pattern; needs a harness step to screen)

- **Change in ken.** After packing chunks, spend a reserved slice of the budget (say 10–15%) on the signature outline (`engine.Outline`: definitions with line spans) of the top-N files that got no chunk. This is the Aider repo-map, Agentless-skeleton and HCP pattern: breadth across files costs a few tokens per file.
- **Why.** Every chunk-only shape above trades files for lines. Outline lines from the right file count for file coverage and touch symbols cheaply, which could let the depth side drop saturation entirely (+0.07 lines) while outlines recover the files.
- **Screen.** The simulator lacks outline text. Add `cmd/cb -save-outlines` (each touched file's outline spans with token counts) in one Lite pass, then screen on the tuning half. Whether an agent uses outlines well is a CB2 question; CB1 only sees coverage.
- **Benchmark-fitting risk.** Higher than option 1, because ContextBench's file coverage rewards any touched file, and outlines can inflate it without helping. Report outline-attributed file coverage separately, and keep symbol coverage and precision in the rule.
- **Cost.** About half a day of harness work, a 2-hour Lite pass to save outlines, then the screen. A gate would follow on the held-out half.

### Option 3 — language-adaptive saturation (not recommended)

- The file losses concentrate in C/C++ (and Rust and Java at 16k). Keeping shipped saturation there and none elsewhere screens at +0.071 / −0.061, which is no better a trade than option 1 because Python and JavaScript still lose files. The C/C++ split was also read off all 500 tasks (`KP`'s gate), held-out half included, so a held-out gate for it is contaminated.

### Not worth registering, from the screens

- **File-first and file-grouped (Agentless-style two-stage).** These lose 0.12–0.26 file coverage at an 8k budget, because restricting to a few files throws away the long tail where ken's file lead comes from. Agentless works because an LLM, not a fixed count, picks the files.
- **Reserve one chunk per top-N file, then fill.** This is the same as no saturation here: the top files' first chunks are already near the top, and file losses come from the tail.
- **Per-file quotas.** These sit on the same frontier as thresholds (quota q ≈ threshold q) and are no better than decay.
- **Ordering ("lost in the middle", `LongContextReorder`) and compression (LongLLMLingua, sentence or line trimming).** ContextBench CB1 scores sets, not order, and compression needs a model. These belong to CB2 (an agent in the loop), not to packing gates.
- **Graph expansion (Aider PageRank, Cody's graph retriever) and MMR on content.** These change the candidates, not the packing. They are larger retrieval projects, and ken's retrieval is a separate decision under ADR-050.

## Ranked next steps

1. **Option 1 gate (decay 0.8, held-out half).** About 1 h plus a small seam change. Replaces threshold 2 inside `BudgetPacking` if it passes.
2. **Option 2 screen (outlines).** About half a day plus a 2-hour pass, before any gate.
3. **CB2 (agent in the loop) for whichever packing ships.** The only way to learn whether more lines or more files help an agent finish tasks. Gated separately (`contextbench-leann` §8.2).


## How screening works, and the held-out split

- **Simulator.** `packsim.py`, kept outside the repository with the run data on mbp2. It replays packing policies over `KP`'s full ranked list per task: up to 500 candidates in the order after fusion, boosts and path penalties, before any file saturation, with token counts, saved by `cmd/cb -save-ranked`. It packs with the harness's rule and scores line and file coverage against the gold spans directly. Validation on all 500 tasks against the official evaluator's records: replaying `K` (saturation threshold 1), `KS2` and `KP` from `KP`'s list gives mean differences of at most 0.0004 line and 0.0013 file coverage, with at most 6 tasks per arm and budget off by more than 0.01. One fix was needed: Multi-SWE-bench gold paths are absolute (`/workspace/<repo>/…`), and the evaluator resolves them by path suffix. So the replay is accurate, and screened differences between policies can be trusted. The candidate pool is `KP`'s top 500 per task, which bounds anything a policy can reach.
- **Split.** Before any screen was read, Lite's 500 tasks were split by `numpy.random.default_rng(20261030).permutation` into a **tuning half** (250 tasks, sha256 `322be674…`) and a **held-out half** (250 tasks, sha256 `2b30d101…`). Options are screened and tuned on the tuning half only. A registered gate for any option runs on the held-out half, so the held-out half is never used to choose a variant, and its interval is about √2 wider than a 500-task gate's. SWE-bench Pro's 54 Lite tasks are a further cross-source check.
- **What screens cannot see.** Ordering effects (ContextBench scores sets, not order), whether an agent would use the extra files or lines (CB2), and anything needing text the simulator does not have (outlines, MMR on content).
