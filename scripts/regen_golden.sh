#!/usr/bin/env bash
#
# regen_golden.sh — wrap the testdata/golden.json fixture regeneration so
# the next person doesn't have to reverse-engineer it from testdata/README.md.
#
# Idempotent. Creates .venv if it doesn't exist; pip-installs the Python
# reference deps; runs scripts/pin_inference.py against the HF model; copies
# the produced ken_golden.json into testdata/golden.json; prints a one-line
# summary so a sanity-check is visible in CI / shell history.
#
# Run from anywhere — the script cd's to the repo root by itself.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

if [[ ! -d .venv ]]; then
    echo "Creating .venv..."
    python3 -m venv .venv
fi

# shellcheck disable=SC1091
source .venv/bin/activate

# Reference deps that scripts/pin_inference.py imports. Keep this list in
# sync with that script's `import` block; mismatches surface as ImportError
# before anything writes to disk, which is what we want.
pip install -q --upgrade pip
pip install -q model2vec safetensors tokenizers huggingface_hub numpy

python scripts/pin_inference.py
cp ken_golden.json testdata/golden.json

bytes=$(wc -c < testdata/golden.json | tr -d ' ')
# pin_inference.py emits a JSON array of records; count them so the operator
# can spot a truncated fixture immediately.
cases=$(python -c 'import json; print(len(json.load(open("testdata/golden.json"))))')
echo "Regenerated testdata/golden.json — ${cases} cases, ${bytes} bytes"
