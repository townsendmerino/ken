# testdata

Fixtures for ken's Go test suite. The Go tests only read these files, never
write them.

> **Embedding / tokenizer goldens moved to aikit (ADR-034).** The Model2Vec
> embedding spot-check (`golden.json`, 18 cases) and the corpus-scale
> tokenizer parity fixture (`parity.jsonl`) — plus their Python generators
> (`pin_inference.py`, `parity_dump.py`) and the Go tests that read them —
> now live in [`github.com/townsendmerino/aikit`](https://github.com/townsendmerino/aikit)
> under `embed/` + `scripts/`, since the `embed` package was extracted there.
> ken inherits that guarantee by pinning a tagged aikit; there's no embedding
> golden test in ken anymore. (A gitignored `parity.jsonl` may linger here
> from before the extraction — it's unused by ken's suite.)

What ken's `testdata/` holds now:

## `model/` (gitignored, per-machine)

A local snapshot of `minishlab/potion-code-16M` for ken's tests that exercise
semantic / hybrid search end-to-end. Tests using it `t.Skip()` when it's
absent — CI without HF access stays green.

ken's CLI resolves a model dir via the priority order `--model` →
`$KEN_MODEL_DIR` → `~/.ken/model` → `./testdata/model`, so repo developers
have two equally-supported options:

- **Follow the public convention** (preferred — same as end users):
  ```bash
  ken download-model            # → ~/.ken/model
  ```
- **Repo-local override** (useful when iterating on model-dependent code):
  ```bash
  ken download-model --to testdata/model
  ```

The HF tooling still works if you prefer it:

```bash
huggingface-cli download minishlab/potion-code-16M \
    tokenizer.json config.json model.safetensors \
    --local-dir testdata/model     # or ~/.ken/model
```

## `encoder-model/` (gitignored, per-machine)

A local snapshot of `nomic-ai/CodeRankEmbed` (~547 MB) for tests that exercise
the opt-in neural reranker (`--mode=hybrid-rerank`). Fetch with
`ken download-model --rerank` (→ `~/.ken/rerank-model`) or
`ken download-model --rerank --to testdata/encoder-model`. Reranker tests
`t.Skip()` when it's absent.

## `bench/` (committed)

Small benchmark fixtures (CoIR-CSN-Python, CoSQA, CSN-Python-NL shortlists)
used by the `-tags=bench` retrieval-quality tests under `bench/`. See
[`docs/BENCH.md`](../docs/BENCH.md) for the full reproduction harness.

## `repo/`

The polyglot smoke fixture (tiny files in Python/Go/TypeScript/Java/Rust plus
a markdown stub). Used by chunker tests and the search/MCP integration tests.
