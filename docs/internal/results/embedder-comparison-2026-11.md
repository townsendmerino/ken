# potion-retrieval-32M against the shipped potion-code-16M (2026-11-14)

**Question.** ken ships `minishlab/potion-code-16M` as its Model2Vec embedder. `minishlab/potion-retrieval-32M` is a
larger static model of the same family, tuned for retrieval in general. Would it rank code better?

**Answer.** No. It is worse on semble's 63-repo set in both modes, on 18 of 19 languages in hybrid and on all 19 in
semantic. The shipped model stays. No ranking change is proposed, and none was made: this was a measurement.

## What was run

ken at `9649c5a` (the branch `after-1.18-ideas`; nothing on it touches retrieval), M1 Pro, 2026-11-14 13:40 to 13:44
local. `bench/semble/run_ken.py --mode hybrid` and `--mode semantic`, each once per model, with `--model` naming the
model directory. The runner sets `KEN_ENRICH_FILE_BUDGET_MS=0`, so runs repeat exactly (`docs/BENCH.md`). 63 repos,
1,251 tasks, regex chunker, NDCG@10.

| Model | Dimensions | File | Hybrid NDCG@10 | Semantic NDCG@10 | Hybrid p50 per query |
|---|---|---|---|---|---|
| potion-code-16M (shipped) | 256 | 64 MB | **0.8436** | **0.6402** | 2.27 ms |
| potion-retrieval-32M | 512 | 129 MB | 0.8201 | 0.5734 | 2.37 ms |
| difference | | | −0.0235 | −0.0668 | |

The shipped model's hybrid figure is the repeatable headline already recorded in `docs/BENCH.md` (0.8436), so the
harness is measuring what it measured before.

Per repository, hybrid: 12 better, 50 worse, 1 the same; the mean per-repo difference is −0.0235, with a bootstrap
95 % interval of −0.031 to −0.016 (10,000 resamples of the 63 repos). Semantic: 10 better, 53 worse; −0.067, interval
−0.085 to −0.050. Neither interval reaches zero.

Per language, hybrid, the only language not worse is Elixir (+0.001). The largest losses are Haskell (−0.064), C
(−0.048) and PHP (−0.041). In semantic mode the losses on Zig, Haskell, Rust and C++ are over 0.12.

## What it means

- A model twice the size, trained for retrieval of prose, embeds code queries worse than the one trained on code.
  Fusion with BM25 hides two thirds of the loss and still leaves a loss.
- It would also double the download and the vector memory. There is no trade to weigh.
- The deferred item "a better embedder" in the aikit adoption notes is answered for this candidate. A different
  candidate would be a code-trained model, not a larger general one.

## Not measured

- CoIR or CodeSearchNet, where queries are code, not questions.
- Reranking after either embedder (`hybrid-rerank`).
- Any model outside the potion family.

The four result files were kept outside the repository and are not committed; the command above reproduces them with
the model downloaded from `minishlab/potion-retrieval-32M` (`config.json`, `tokenizer.json`, `model.safetensors`).
