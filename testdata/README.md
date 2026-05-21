# testdata

Golden fixtures for the Go test suite. The Python reference scripts that
produce them live under [`../scripts/`](../scripts/); the Go tests only
read these files, never write them.

## `golden.json`

Produced by `scripts/pin_inference.py`. 18 hand-picked cases. For each:

- input text
- WordPiece token strings and IDs (from HF tokenizer reference)
- per-token weights (from the `weights` tensor)
- ground-truth output vector (from `StaticModel.encode()`)
- three candidate pooling-recipe outputs (for debugging)

The empty-string and all-`[UNK]` rows have `ground_truth: null` and a
`degenerate_ground_truth` flag — the Go golden test asserts the
zero-vector contract for those directly rather than via cosine.

To regenerate (from repo root):

```bash
./scripts/regen_golden.sh
```

The script bootstraps `.venv/` if it doesn't exist, pip-installs the Python reference deps (`model2vec`, `safetensors`, `tokenizers`, `huggingface_hub`, `numpy`), runs `scripts/pin_inference.py`, copies the produced `ken_golden.json` into `testdata/golden.json`, and prints a one-line summary (case count + byte size) so a truncated fixture is visible immediately. Idempotent — re-run safely.

Manual fallback if `regen_golden.sh` can't be used (e.g. existing venv with conflicting deps):

```bash
.venv/bin/python scripts/pin_inference.py
cp ken_golden.json testdata/golden.json
```

## `parity.jsonl` (gitignored)

Produced by `scripts/parity_dump.py`. The 100k-input corpus-scale
tokenizer parity fixture. Run the `parity`-tagged Go test against it:

```bash
.venv/bin/python scripts/parity_dump.py
go test -tags=parity ./internal/embed/ -run TestParity -v
```

## `model/` (gitignored, per-machine)

A local snapshot of `minishlab/potion-code-16M` for tests that exercise
the full inference pipeline (the golden cosine assertion, and the parity
harness). Tests using it `t.Skip()` when it's absent — CI without HF
access stays green:

```bash
# Pure-Go fetch via ken's own downloader (no Python toolchain needed):
ken download-model --to testdata/model

# Or, if you prefer the HF tooling:
huggingface-cli download minishlab/potion-code-16M \
    tokenizer.json config.json model.safetensors \
    --local-dir testdata/model
```

## `repo/`

The polyglot smoke fixture (tiny files in Python/Go/TypeScript/Java/Rust
plus a markdown stub). Used by chunker tests and the search/MCP
integration tests.
