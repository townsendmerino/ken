#!/usr/bin/env bash
# Builds cmd/ken-mcp-docs by staging the Model2Vec model + ken's docs/
# tree into the demo binary's directory (so //go:embed can pick them up,
# since embed paths can't traverse ../). Output lands at bin/ken-mcp-docs.
#
# Usage: scripts/build-docs-mcp.sh
#
# Requires: a working Go toolchain. No Python, no system C compiler.
# On first run, downloads the Model2Vec model into ~/.ken/model via the
# ken CLI; subsequent runs reuse it.
set -euo pipefail

cd "$(dirname "$0")/.."   # repo root
mkdir -p bin

echo "[1/4] Building ken CLI (needed for download-model)..."
go build -o bin/ken ./cmd/ken

if [ ! -f "$HOME/.ken/model/model.safetensors" ]; then
  echo "[2/4] Downloading Model2Vec model to ~/.ken/model..."
  ./bin/ken download-model
else
  echo "[2/4] Reusing existing model at ~/.ken/model"
fi

echo "[3/4] Staging embeds into cmd/ken-mcp-docs/..."
rm -rf cmd/ken-mcp-docs/model cmd/ken-mcp-docs/docs
mkdir -p cmd/ken-mcp-docs/model cmd/ken-mcp-docs/docs
cp -R "$HOME/.ken/model/." cmd/ken-mcp-docs/model/
cp docs/*.md cmd/ken-mcp-docs/docs/

echo "[4/4] Building bin/ken-mcp-docs..."
# The embed_corpus build tag activates the //go:embed directives in
# cmd/ken-mcp-docs/main.go. Without the tag the package has zero
# buildable Go files, so `go build ./...` from the repo root skips it
# cleanly on a fresh clone (where the embed dirs don't yet exist).
go build -tags=embed_corpus -o bin/ken-mcp-docs ./cmd/ken-mcp-docs

size=$(ls -lh bin/ken-mcp-docs | awk '{print $5}')
echo
echo "Done. bin/ken-mcp-docs is ${size}."
